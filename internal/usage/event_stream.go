// Package usage provides SSE event streaming for real-time usage monitoring.
package usage

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/queuehealth"
	coreusage "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/usage"
)

// RequestEvent represents a single request event for SSE streaming.
type RequestEvent struct {
	Type      string    `json:"type"` // "request" | "quota_exceeded" | "error"
	Seq       int64     `json:"seq,omitempty"`
	EventID   string    `json:"event_id,omitempty"`
	Timestamp time.Time `json:"timestamp"`
	RequestID string    `json:"request_id,omitempty"`
	Provider  string    `json:"provider"`
	Model     string    `json:"model"`
	AuthFile  string    `json:"auth_file"`
	Source    string    `json:"source,omitempty"`
	Success   bool      `json:"success"`
	Tokens    int64     `json:"tokens"`
	Latency   int64     `json:"latency_ms,omitempty"`
	Error     string    `json:"error,omitempty"`
}

// EventStreamManager manages SSE subscribers for real-time usage events.
type EventStreamManager struct {
	mu          sync.RWMutex
	subscribers map[string]*eventSubscriber
	nextID      int64
	nextSeq     int64
	ledger      []RequestEvent
	maxLedger   int
	published   int64
	dropped     int64
}

var defaultEventStream = NewEventStreamManager()

type eventSubscriber struct {
	events  chan RequestEvent
	dropped int64
}

type EventStreamMetrics struct {
	PublishedTotal  int64            `json:"published_total"`
	DroppedTotal    int64            `json:"dropped_total"`
	SubscriberCount int              `json:"subscriber_count"`
	LedgerSize      int              `json:"ledger_size"`
	SubscriberDrop  map[string]int64 `json:"subscriber_dropped_total"`
	CurrentSeq      int64            `json:"current_seq"`
}

// GetEventStream returns the shared event stream manager.
func GetEventStream() *EventStreamManager { return defaultEventStream }

// NewEventStreamManager creates a new event stream manager.
func NewEventStreamManager() *EventStreamManager {
	return &EventStreamManager{
		subscribers: make(map[string]*eventSubscriber),
		maxLedger:   10000,
		ledger:      make([]RequestEvent, 0, 1024),
	}
}

// Subscribe adds a new subscriber and returns their event channel.
// The caller must call Unsubscribe when done.
func (m *EventStreamManager) Subscribe() (id string, events <-chan RequestEvent) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.nextID++
	id = fmt.Sprintf("sub-%d", m.nextID)
	ch := make(chan RequestEvent, 256) // Slightly larger buffer for burst replay/live overlap.
	m.subscribers[id] = &eventSubscriber{events: ch}
	return id, ch
}

// Unsubscribe removes a subscriber.
func (m *EventStreamManager) Unsubscribe(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if subscriber, ok := m.subscribers[id]; ok && subscriber != nil {
		close(subscriber.events)
		delete(m.subscribers, id)
	}
}

// Publish broadcasts an event to all subscribers.
func (m *EventStreamManager) Publish(event RequestEvent) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.nextSeq++
	event.Seq = m.nextSeq
	if strings.TrimSpace(event.EventID) == "" {
		event.EventID = strconv.FormatInt(event.Seq, 10)
	}
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now().UTC()
	}
	m.published++
	m.ledger = append(m.ledger, event)
	if len(m.ledger) > m.maxLedger {
		m.ledger = append([]RequestEvent(nil), m.ledger[len(m.ledger)-m.maxLedger:]...)
	}

	for _, subscriber := range m.subscribers {
		if subscriber == nil {
			continue
		}
		select {
		case subscriber.events <- event:
		default:
			subscriber.dropped++
			m.dropped++
			queuehealth.Inc("usage_subscriber_channel_full")
		}
	}
}

// SubscriberCount returns the number of active subscribers.
func (m *EventStreamManager) SubscriberCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.subscribers)
}

func (m *EventStreamManager) CurrentSeq() int64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.nextSeq
}

func (m *EventStreamManager) ReplaySince(sinceSeq int64, limit int) []RequestEvent {
	if limit <= 0 {
		limit = 500
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]RequestEvent, 0, limit)
	for i := 0; i < len(m.ledger); i++ {
		event := m.ledger[i]
		if event.Seq <= sinceSeq {
			continue
		}
		out = append(out, event)
		if len(out) >= limit {
			break
		}
	}
	return out
}

func (m *EventStreamManager) MetricsSnapshot() EventStreamMetrics {
	m.mu.RLock()
	defer m.mu.RUnlock()
	snap := EventStreamMetrics{
		PublishedTotal:  m.published,
		DroppedTotal:    m.dropped,
		SubscriberCount: len(m.subscribers),
		LedgerSize:      len(m.ledger),
		SubscriberDrop:  make(map[string]int64, len(m.subscribers)),
		CurrentSeq:      m.nextSeq,
	}
	for id, subscriber := range m.subscribers {
		if subscriber == nil {
			continue
		}
		snap.SubscriberDrop[id] = subscriber.dropped
	}
	return snap
}

// EventStreamPlugin implements coreusage.Plugin to broadcast usage events.
type EventStreamPlugin struct {
	manager *EventStreamManager
}

// NewEventStreamPlugin creates a plugin wired to the shared event stream.
func NewEventStreamPlugin() *EventStreamPlugin {
	return &EventStreamPlugin{manager: defaultEventStream}
}

// HandleUsage implements coreusage.Plugin.
func (p *EventStreamPlugin) HandleUsage(ctx context.Context, record coreusage.Record) {
	if p == nil || p.manager == nil {
		return
	}

	event := RequestEvent{
		Type:      "request",
		Timestamp: record.RequestedAt,
		RequestID: strings.TrimSpace(logging.GetRequestID(ctx)),
		Provider:  record.Provider,
		Model:     record.Model,
		AuthFile:  record.AuthIndex,
		Source:    record.Source,
		Success:   !record.Failed,
		Tokens:    record.Detail.TotalTokens,
	}

	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now()
	}
	if record.Failed {
		event.Type = "error"
	}

	p.manager.Publish(event)
}

// PublishQuotaExceeded sends a quota exceeded event.
func PublishQuotaExceeded(provider, model, authFile string) {
	defaultEventStream.Publish(RequestEvent{
		Type:      "quota_exceeded",
		Timestamp: time.Now(),
		Provider:  provider,
		Model:     model,
		AuthFile:  authFile,
		Success:   false,
		Error:     "quota exceeded",
	})
}

// PublishError sends an error event.
func PublishError(provider, model, authFile, errorMsg string) {
	defaultEventStream.Publish(RequestEvent{
		Type:      "error",
		Timestamp: time.Now(),
		Provider:  provider,
		Model:     model,
		AuthFile:  authFile,
		Success:   false,
		Error:     errorMsg,
	})
}

// EventToSSE formats an event as SSE data.
func EventToSSE(event RequestEvent) []byte {
	data, _ := json.Marshal(event)
	if event.Seq > 0 {
		return []byte(fmt.Sprintf("id: %d\ndata: %s\n\n", event.Seq, data))
	}
	return []byte(fmt.Sprintf("data: %s\n\n", data))
}

func init() {
	// Register the event stream plugin
	coreusage.RegisterPlugin(NewEventStreamPlugin())
}
