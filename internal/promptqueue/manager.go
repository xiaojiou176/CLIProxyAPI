package promptqueue

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/queuehealth"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
)

const (
	defaultSessionKey       = "default"
	defaultSessionQueueSize = 256
	defaultMaxEvents        = 4096
	defaultMaxSubmissions   = 20000
)

type SubmissionStatus string

const (
	SubmissionQueued    SubmissionStatus = "queued"
	SubmissionRunning   SubmissionStatus = "running"
	SubmissionSucceeded SubmissionStatus = "succeeded"
	SubmissionFailed    SubmissionStatus = "failed"
)

type Submission struct {
	ID         string           `json:"id"`
	SessionKey string           `json:"session_key"`
	Handler    string           `json:"handler"`
	Model      string           `json:"model"`
	RequestID  string           `json:"request_id,omitempty"`
	Status     SubmissionStatus `json:"status"`
	Error      string           `json:"error,omitempty"`
	EnqueuedAt time.Time        `json:"enqueued_at"`
	StartedAt  time.Time        `json:"started_at,omitempty"`
	FinishedAt time.Time        `json:"finished_at,omitempty"`
}

type Event struct {
	Seq          int64          `json:"seq"`
	EventID      string         `json:"event_id"`
	Type         string         `json:"type"`
	SubmissionID string         `json:"submission_id,omitempty"`
	SessionKey   string         `json:"session_key,omitempty"`
	Timestamp    time.Time      `json:"timestamp"`
	Payload      map[string]any `json:"payload,omitempty"`
}

type MetricsSnapshot struct {
	Submitted      int64          `json:"submitted_total"`
	Started        int64          `json:"started_total"`
	Succeeded      int64          `json:"succeeded_total"`
	Failed         int64          `json:"failed_total"`
	Overloaded     int64          `json:"overloaded_total"`
	CurrentQueued  int            `json:"current_queued"`
	QueueDepthBySK map[string]int `json:"queue_depth_by_session"`
}

type SubmitRequest struct {
	SubmissionID string
	SessionKey   string
	Handler      string
	Model        string
	RequestID    string
}

type ListOptions struct {
	SessionKey string
	Status     SubmissionStatus
	Offset     int
	Limit      int
}

type Config struct {
	StoreDir         string
	SessionQueueSize int
	MaxEvents        int
	MaxSubmissions   int
}

type Manager struct {
	mu sync.RWMutex

	workers map[string]*sessionWorker
	store   *diskStore

	nextSeq int64
	events  []Event

	submissions      map[string]*Submission
	submissionOrder  []string
	submissionEvictN int

	submittedTotal  int64
	startedTotal    int64
	succeededTotal  int64
	failedTotal     int64
	overloadedTotal int64

	sessionQueueSize int
	maxEvents        int
	maxSubmissions   int
}

type sessionWorker struct {
	sessionKey string
	jobs       chan *job
	manager    *Manager
}

type job struct {
	req SubmitRequest
	run func(string) error
	ack chan error
}

type diskStore struct {
	mu              sync.Mutex
	submissionsPath string
	eventsPath      string
}

type submissionJournalRecord struct {
	Submission Submission `json:"submission"`
}

type eventJournalRecord struct {
	Event Event `json:"event"`
}

var (
	defaultManagerOnce sync.Once
	defaultManager     *Manager
)

func GetDefaultManager() *Manager {
	defaultManagerOnce.Do(func() {
		cfg := Config{
			StoreDir:         defaultStoreDir(),
			SessionQueueSize: defaultSessionQueueSize,
			MaxEvents:        defaultMaxEvents,
			MaxSubmissions:   defaultMaxSubmissions,
		}
		defaultManager = NewManager(cfg)
	})
	return defaultManager
}

func defaultStoreDir() string {
	if base := strings.TrimSpace(util.WritablePath()); base != "" {
		return filepath.Join(base, ".runtime-cache", "prompt-queue")
	}
	home, err := os.UserHomeDir()
	if err == nil && strings.TrimSpace(home) != "" {
		return filepath.Join(home, ".cli-proxy-api", ".runtime-cache", "prompt-queue")
	}
	return filepath.Join(os.TempDir(), "cliproxyapi-prompt-queue")
}

func NewManager(cfg Config) *Manager {
	if cfg.SessionQueueSize <= 0 {
		cfg.SessionQueueSize = defaultSessionQueueSize
	}
	if cfg.MaxEvents <= 0 {
		cfg.MaxEvents = defaultMaxEvents
	}
	if cfg.MaxSubmissions <= 0 {
		cfg.MaxSubmissions = defaultMaxSubmissions
	}

	m := &Manager{
		workers:          make(map[string]*sessionWorker),
		events:           make([]Event, 0, cfg.MaxEvents),
		submissions:      make(map[string]*Submission),
		submissionOrder:  make([]string, 0, cfg.MaxSubmissions),
		sessionQueueSize: cfg.SessionQueueSize,
		maxEvents:        cfg.MaxEvents,
		maxSubmissions:   cfg.MaxSubmissions,
	}
	if strings.TrimSpace(cfg.StoreDir) != "" {
		if store, err := newDiskStore(cfg.StoreDir); err == nil {
			m.store = store
			_ = m.restoreFromDisk()
		}
	}
	return m
}

func (m *Manager) SubmitAndWait(req SubmitRequest, run func(submissionID string) error) (string, error) {
	if m == nil {
		if run == nil {
			return "", errors.New("prompt queue not available")
		}
		id := strings.TrimSpace(req.SubmissionID)
		if id == "" {
			id = uuid.NewString()
		}
		return id, run(id)
	}
	if run == nil {
		return "", errors.New("run callback is required")
	}

	sessionKey := strings.TrimSpace(req.SessionKey)
	if sessionKey == "" {
		sessionKey = defaultSessionKey
	}
	req.SessionKey = sessionKey

	now := time.Now().UTC()
	submissionID := strings.TrimSpace(req.SubmissionID)
	if submissionID == "" {
		submissionID = uuid.NewString()
	}
	req.SubmissionID = submissionID

	s := Submission{
		ID:         submissionID,
		SessionKey: sessionKey,
		Handler:    strings.TrimSpace(req.Handler),
		Model:      strings.TrimSpace(req.Model),
		RequestID:  strings.TrimSpace(req.RequestID),
		Status:     SubmissionQueued,
		EnqueuedAt: now,
	}

	m.mu.Lock()
	if _, exists := m.submissions[submissionID]; exists {
		submissionID = uuid.NewString()
		req.SubmissionID = submissionID
		s.ID = submissionID
	}
	if err := m.storeSubmissionLocked(s); err != nil {
		m.mu.Unlock()
		return "", err
	}
	m.submissions[submissionID] = cloneSubmission(s)
	m.submissionOrder = append(m.submissionOrder, submissionID)
	m.submittedTotal++
	m.evictSubmissionsIfNeededLocked()
	_ = m.appendEventLocked("submission_queued", submissionID, sessionKey, map[string]any{
		"handler": s.Handler,
		"model":   s.Model,
	})

	worker := m.ensureWorkerLocked(sessionKey)
	queueLen := len(worker.jobs)
	queueCap := cap(worker.jobs)
	if queueCap > 0 && queueLen >= queueCap {
		m.overloadedTotal++
		queuehealth.Inc("prompt_queue_session_channel_full")
		_ = m.appendEventLocked("queue_overloaded", submissionID, sessionKey, map[string]any{
			"queue_depth": queueLen,
			"queue_cap":   queueCap,
		})
	}
	m.mu.Unlock()

	j := &job{
		req: req,
		run: run,
		ack: make(chan error, 1),
	}
	worker.jobs <- j
	err := <-j.ack
	return submissionID, err
}

func (m *Manager) ListSubmissions(opts ListOptions) []Submission {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()

	sessionKey := strings.TrimSpace(opts.SessionKey)
	statusFilter := strings.TrimSpace(string(opts.Status))

	out := make([]Submission, 0, len(m.submissionOrder))
	for i := len(m.submissionOrder) - 1; i >= 0; i-- {
		id := m.submissionOrder[i]
		sub, ok := m.submissions[id]
		if !ok || sub == nil {
			continue
		}
		if sessionKey != "" && sub.SessionKey != sessionKey {
			continue
		}
		if statusFilter != "" && string(sub.Status) != statusFilter {
			continue
		}
		out = append(out, *cloneSubmission(*sub))
	}

	offset := opts.Offset
	if offset < 0 {
		offset = 0
	}
	if offset >= len(out) {
		return []Submission{}
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = 200
	}
	end := offset + limit
	if end > len(out) {
		end = len(out)
	}
	return out[offset:end]
}

func (m *Manager) EventsSince(sessionKey string, sinceSeq int64, limit int) []Event {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()

	sessionKey = strings.TrimSpace(sessionKey)
	if limit <= 0 {
		limit = 500
	}
	out := make([]Event, 0, limit)
	for i := 0; i < len(m.events); i++ {
		ev := m.events[i]
		if ev.Seq <= sinceSeq {
			continue
		}
		if sessionKey != "" && ev.SessionKey != sessionKey {
			continue
		}
		out = append(out, cloneEvent(ev))
		if len(out) >= limit {
			break
		}
	}
	return out
}

func (m *Manager) MetricsSnapshot() MetricsSnapshot {
	snap := MetricsSnapshot{
		QueueDepthBySK: make(map[string]int),
	}
	if m == nil {
		return snap
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	snap.Submitted = m.submittedTotal
	snap.Started = m.startedTotal
	snap.Succeeded = m.succeededTotal
	snap.Failed = m.failedTotal
	snap.Overloaded = m.overloadedTotal
	for key, worker := range m.workers {
		if worker == nil {
			continue
		}
		depth := len(worker.jobs)
		snap.QueueDepthBySK[key] = depth
		snap.CurrentQueued += depth
	}
	return snap
}

func (m *Manager) restoreFromDisk() error {
	if m == nil || m.store == nil {
		return nil
	}
	submissions, err := m.store.readSubmissionJournal()
	if err != nil {
		return err
	}
	events, err := m.store.readEventJournal()
	if err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	for _, sub := range submissions {
		clone := cloneSubmission(sub)
		if _, exists := m.submissions[clone.ID]; !exists {
			m.submissionOrder = append(m.submissionOrder, clone.ID)
		}
		m.submissions[clone.ID] = clone
	}
	sort.SliceStable(m.submissionOrder, func(i, j int) bool {
		left := m.submissions[m.submissionOrder[i]]
		right := m.submissions[m.submissionOrder[j]]
		if left == nil || right == nil {
			return m.submissionOrder[i] < m.submissionOrder[j]
		}
		return left.EnqueuedAt.Before(right.EnqueuedAt)
	})
	for _, ev := range events {
		if ev.Seq > m.nextSeq {
			m.nextSeq = ev.Seq
		}
		m.events = append(m.events, cloneEvent(ev))
	}
	if len(m.events) > m.maxEvents {
		m.events = append([]Event(nil), m.events[len(m.events)-m.maxEvents:]...)
	}
	m.evictSubmissionsIfNeededLocked()
	return nil
}

func (m *Manager) ensureWorkerLocked(sessionKey string) *sessionWorker {
	if sessionKey == "" {
		sessionKey = defaultSessionKey
	}
	if worker, ok := m.workers[sessionKey]; ok && worker != nil {
		return worker
	}
	worker := &sessionWorker{
		sessionKey: sessionKey,
		jobs:       make(chan *job, m.sessionQueueSize),
		manager:    m,
	}
	m.workers[sessionKey] = worker
	go worker.run()
	return worker
}

func (m *Manager) evictSubmissionsIfNeededLocked() {
	if m.maxSubmissions <= 0 {
		return
	}
	over := len(m.submissionOrder) - m.maxSubmissions
	if over <= 0 {
		return
	}
	for i := 0; i < over; i++ {
		id := m.submissionOrder[i]
		delete(m.submissions, id)
	}
	m.submissionOrder = append([]string(nil), m.submissionOrder[over:]...)
	m.submissionEvictN += over
}

func (m *Manager) markRunning(submissionID, sessionKey string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	sub, ok := m.submissions[submissionID]
	if !ok || sub == nil {
		return
	}
	sub.Status = SubmissionRunning
	sub.StartedAt = time.Now().UTC()
	m.startedTotal++
	_ = m.storeSubmissionLocked(*sub)
	_ = m.appendEventLocked("submission_started", submissionID, sessionKey, nil)
}

func (m *Manager) markFinished(submissionID, sessionKey string, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	sub, ok := m.submissions[submissionID]
	if !ok || sub == nil {
		return
	}
	sub.FinishedAt = time.Now().UTC()
	if err != nil {
		sub.Status = SubmissionFailed
		sub.Error = err.Error()
		m.failedTotal++
		_ = m.appendEventLocked("submission_failed", submissionID, sessionKey, map[string]any{
			"error": sub.Error,
		})
	} else {
		sub.Status = SubmissionSucceeded
		sub.Error = ""
		m.succeededTotal++
		_ = m.appendEventLocked("submission_succeeded", submissionID, sessionKey, nil)
	}
	_ = m.storeSubmissionLocked(*sub)
}

func (m *Manager) appendEventLocked(eventType, submissionID, sessionKey string, payload map[string]any) error {
	m.nextSeq++
	ev := Event{
		Seq:          m.nextSeq,
		EventID:      uuid.NewString(),
		Type:         eventType,
		SubmissionID: submissionID,
		SessionKey:   sessionKey,
		Timestamp:    time.Now().UTC(),
		Payload:      payload,
	}
	if err := m.storeEventLocked(ev); err != nil {
		return err
	}
	m.events = append(m.events, ev)
	if len(m.events) > m.maxEvents {
		m.events = append([]Event(nil), m.events[len(m.events)-m.maxEvents:]...)
	}
	return nil
}

func (m *Manager) storeSubmissionLocked(s Submission) error {
	if m.store == nil {
		return nil
	}
	return m.store.appendSubmission(s)
}

func (m *Manager) storeEventLocked(ev Event) error {
	if m.store == nil {
		return nil
	}
	return m.store.appendEvent(ev)
}

func (w *sessionWorker) run() {
	for j := range w.jobs {
		if j == nil {
			continue
		}
		w.manager.markRunning(j.req.SubmissionID, w.sessionKey)
		var execErr error
		func() {
			defer func() {
				if recovered := recover(); recovered != nil {
					execErr = fmt.Errorf("prompt queue worker panic: %v", recovered)
				}
			}()
			execErr = j.run(j.req.SubmissionID)
		}()
		w.manager.markFinished(j.req.SubmissionID, w.sessionKey, execErr)
		j.ack <- execErr
	}
}

func newDiskStore(dir string) (*diskStore, error) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return nil, errors.New("store dir is empty")
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}
	return &diskStore{
		submissionsPath: filepath.Join(dir, "submissions.jsonl"),
		eventsPath:      filepath.Join(dir, "events.jsonl"),
	}, nil
}

func (s *diskStore) appendSubmission(sub Submission) error {
	if s == nil {
		return nil
	}
	rec := submissionJournalRecord{Submission: sub}
	return s.appendLine(s.submissionsPath, rec)
}

func (s *diskStore) appendEvent(ev Event) error {
	if s == nil {
		return nil
	}
	rec := eventJournalRecord{Event: ev}
	return s.appendLine(s.eventsPath, rec)
}

func (s *diskStore) appendLine(path string, v any) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err = f.Write(append(data, '\n')); err != nil {
		return err
	}
	return f.Sync()
}

func (s *diskStore) readSubmissionJournal() ([]Submission, error) {
	if s == nil {
		return nil, nil
	}
	lines, err := s.readLines(s.submissionsPath)
	if err != nil {
		return nil, err
	}
	state := make(map[string]Submission)
	order := make([]string, 0, len(lines))
	for _, line := range lines {
		var rec submissionJournalRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			continue
		}
		sub := rec.Submission
		if strings.TrimSpace(sub.ID) == "" {
			continue
		}
		if _, ok := state[sub.ID]; !ok {
			order = append(order, sub.ID)
		}
		state[sub.ID] = sub
	}
	out := make([]Submission, 0, len(order))
	for _, id := range order {
		out = append(out, state[id])
	}
	return out, nil
}

func (s *diskStore) readEventJournal() ([]Event, error) {
	if s == nil {
		return nil, nil
	}
	lines, err := s.readLines(s.eventsPath)
	if err != nil {
		return nil, err
	}
	out := make([]Event, 0, len(lines))
	for _, line := range lines {
		var rec eventJournalRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			continue
		}
		if rec.Event.Seq <= 0 {
			continue
		}
		out = append(out, rec.Event)
	}
	return out, nil
}

func (s *diskStore) readLines(path string) ([][]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	out := make([][]byte, 0)
	for scanner.Scan() {
		raw := strings.TrimSpace(scanner.Text())
		if raw == "" {
			continue
		}
		out = append(out, []byte(raw))
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func cloneSubmission(in Submission) *Submission {
	out := in
	return &out
}

func cloneEvent(in Event) Event {
	out := in
	if in.Payload != nil {
		out.Payload = make(map[string]any, len(in.Payload))
		for k, v := range in.Payload {
			out.Payload[k] = v
		}
	}
	return out
}
