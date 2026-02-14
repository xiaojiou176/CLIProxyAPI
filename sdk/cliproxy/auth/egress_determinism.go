package auth

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	log "github.com/sirupsen/logrus"
)

const (
	defaultEgressMappingStateFile    = ".runtime-cache/state/egress-mapping.json"
	defaultEgressDriftAlertThreshold = 1
)

type egressMappingEntry struct {
	ProxyIdentity string    `json:"proxy_identity"`
	ProxyDigest   string    `json:"proxy_digest"`
	FirstSeenAt   time.Time `json:"first_seen_at"`
	LastSeenAt    time.Time `json:"last_seen_at"`
	DriftCount    int       `json:"drift_count"`
	LastDriftAt   time.Time `json:"last_drift_at,omitempty"`
	LastProvider  string    `json:"last_provider,omitempty"`
	LastModel     string    `json:"last_model,omitempty"`
}

type egressMappingState struct {
	Version   int                           `json:"version"`
	UpdatedAt time.Time                     `json:"updated_at"`
	Accounts  map[string]egressMappingEntry `json:"accounts"`
}

// EgressMappingSnapshot contains a read-only observability view for account egress mappings.
type EgressMappingSnapshot struct {
	Enabled                bool                          `json:"enabled"`
	StateFile              string                        `json:"state_file,omitempty"`
	DriftAlertThreshold    int                           `json:"drift_alert_threshold"`
	TotalAccounts          int                           `json:"total_accounts"`
	DriftedAccounts        int                           `json:"drifted_accounts"`
	AlertedAccounts        int                           `json:"alerted_accounts"`
	TotalDriftEvents       int                           `json:"total_drift_events"`
	InconsistentAccounts   int                           `json:"inconsistent_accounts"`
	TotalConsistencyIssues int                           `json:"total_consistency_issues"`
	Accounts               []EgressMappingSnapshotRecord `json:"accounts"`
}

// EgressMappingSnapshotRecord contains sanitized per-account egress mapping status.
type EgressMappingSnapshotRecord struct {
	AuthID            string    `json:"auth_id"`
	AuthIndex         string    `json:"auth_index,omitempty"`
	Provider          string    `json:"provider,omitempty"`
	ProxyIdentity     string    `json:"proxy_identity"`
	FirstSeenAt       time.Time `json:"first_seen_at,omitempty"`
	LastSeenAt        time.Time `json:"last_seen_at,omitempty"`
	DriftCount        int       `json:"drift_count"`
	DriftAlerted      bool      `json:"drift_alerted"`
	LastDriftAt       time.Time `json:"last_drift_at,omitempty"`
	LastProvider      string    `json:"last_provider,omitempty"`
	LastModel         string    `json:"last_model,omitempty"`
	ConsistencyStatus string    `json:"consistency_status"`
	ConsistencyIssues []string  `json:"consistency_issues,omitempty"`
}

func (m *Manager) applyEgressDeterminismConfig(cfg *internalconfig.Config) {
	if m == nil {
		return
	}
	enabled := false
	stateFile := defaultEgressMappingStateFile
	driftAlertThreshold := defaultEgressDriftAlertThreshold
	if cfg != nil {
		enabled = cfg.EgressDeterminism.Enabled
		if v := strings.TrimSpace(cfg.EgressDeterminism.StateFile); v != "" {
			stateFile = v
		}
		driftAlertThreshold = normalizeEgressDriftAlertThreshold(cfg.EgressDeterminism.DriftAlertThreshold)
	}
	stateFile = filepath.Clean(stateFile)

	m.egressMu.Lock()
	samePath := stateFile == m.egressStateFile
	alreadyLoaded := m.egressStateLoaded
	m.egressEnabled = enabled
	m.egressStateFile = stateFile
	m.egressDriftAlertThreshold = driftAlertThreshold
	if !enabled {
		m.egressStateLoaded = false
		m.egressMu.Unlock()
		return
	}
	if alreadyLoaded && samePath {
		m.egressMu.Unlock()
		return
	}
	m.egressMu.Unlock()

	mappings, err := loadEgressMappingState(stateFile)
	if err != nil {
		log.WithError(err).Warnf("failed to load egress mapping state from %s", stateFile)
		mappings = make(map[string]egressMappingEntry)
	}

	m.egressMu.Lock()
	defer m.egressMu.Unlock()
	if !m.egressEnabled || m.egressStateFile != stateFile {
		return
	}
	m.egressMappings = mappings
	m.egressStateLoaded = true
}

func (m *Manager) observeEgress(auth *Auth, provider, model string) {
	if m == nil || auth == nil || strings.TrimSpace(auth.ID) == "" {
		return
	}

	if !m.ensureEgressMappingLoaded() {
		return
	}

	proxyIdentity, proxyDigest := normalizeProxyIdentityAndDigest(auth.ProxyURL)
	now := time.Now().UTC()
	authID := strings.TrimSpace(auth.ID)
	provider = strings.TrimSpace(strings.ToLower(provider))
	model = strings.TrimSpace(model)

	var (
		persistNeeded  bool
		drifted        bool
		prevIdentity   string
		driftCount     int
		driftThreshold int
		stateFile      string
		snapshot       map[string]egressMappingEntry
	)

	m.egressMu.Lock()
	if !m.egressEnabled {
		m.egressMu.Unlock()
		return
	}
	if m.egressMappings == nil {
		m.egressMappings = make(map[string]egressMappingEntry)
	}
	entry, exists := m.egressMappings[authID]
	if !exists {
		entry = egressMappingEntry{
			ProxyIdentity: proxyIdentity,
			ProxyDigest:   proxyDigest,
			FirstSeenAt:   now,
		}
		persistNeeded = true
	} else if entry.ProxyDigest != "" && entry.ProxyDigest != proxyDigest {
		drifted = true
		persistNeeded = true
		prevIdentity = entry.ProxyIdentity
		entry.DriftCount++
		entry.LastDriftAt = now
	}

	if entry.ProxyDigest == "" {
		persistNeeded = true
	}
	if entry.FirstSeenAt.IsZero() {
		entry.FirstSeenAt = now
		persistNeeded = true
	}

	entry.ProxyIdentity = proxyIdentity
	entry.ProxyDigest = proxyDigest
	entry.LastSeenAt = now
	entry.LastProvider = provider
	entry.LastModel = model
	m.egressMappings[authID] = entry

	if persistNeeded {
		stateFile = m.egressStateFile
		snapshot = cloneEgressMappings(m.egressMappings)
	}
	driftCount = entry.DriftCount
	driftThreshold = normalizeEgressDriftAlertThreshold(m.egressDriftAlertThreshold)
	m.egressMu.Unlock()

	if drifted {
		log.WithFields(log.Fields{
			"auth_id":               authID,
			"provider":              provider,
			"model":                 model,
			"from_proxy_identity":   prevIdentity,
			"to_proxy_identity":     proxyIdentity,
			"drift_count":           driftCount,
			"drift_alert_threshold": driftThreshold,
			"drift_alerted":         driftCount >= driftThreshold,
			"state_file":            strings.TrimSpace(stateFile),
		}).Warn("egress drift detected")
	}
	if persistNeeded {
		if err := persistEgressMappingState(stateFile, snapshot, now); err != nil {
			log.WithError(err).Warnf("failed to persist egress mapping state to %s", stateFile)
		}
	}
}

func (m *Manager) ensureEgressMappingLoaded() bool {
	if m == nil {
		return false
	}
	m.egressMu.Lock()
	enabled := m.egressEnabled
	loaded := m.egressStateLoaded
	stateFile := m.egressStateFile
	m.egressMu.Unlock()
	if !enabled {
		return false
	}
	if loaded {
		return true
	}
	mappings, err := loadEgressMappingState(stateFile)
	if err != nil {
		log.WithError(err).Warnf("failed to lazy-load egress mapping state from %s", stateFile)
		mappings = make(map[string]egressMappingEntry)
	}
	m.egressMu.Lock()
	defer m.egressMu.Unlock()
	if !m.egressEnabled {
		return false
	}
	if m.egressStateLoaded {
		return true
	}
	if m.egressStateFile != stateFile {
		return false
	}
	m.egressMappings = mappings
	m.egressStateLoaded = true
	return true
}

// EgressMappingSnapshot returns a read-only and sanitized snapshot for management observability.
func (m *Manager) EgressMappingSnapshot() EgressMappingSnapshot {
	snapshot := EgressMappingSnapshot{
		Accounts: make([]EgressMappingSnapshotRecord, 0),
	}
	if m == nil {
		return snapshot
	}

	m.egressMu.Lock()
	snapshot.Enabled = m.egressEnabled
	snapshot.StateFile = strings.TrimSpace(m.egressStateFile)
	snapshot.DriftAlertThreshold = normalizeEgressDriftAlertThreshold(m.egressDriftAlertThreshold)
	m.egressMu.Unlock()
	if !snapshot.Enabled {
		return snapshot
	}

	_ = m.ensureEgressMappingLoaded()

	m.egressMu.Lock()
	snapshot.Enabled = m.egressEnabled
	snapshot.StateFile = strings.TrimSpace(m.egressStateFile)
	snapshot.DriftAlertThreshold = normalizeEgressDriftAlertThreshold(m.egressDriftAlertThreshold)
	mappings := cloneEgressMappings(m.egressMappings)
	m.egressMu.Unlock()
	if !snapshot.Enabled || len(mappings) == 0 {
		return snapshot
	}

	ids := make([]string, 0, len(mappings))
	for authID := range mappings {
		ids = append(ids, authID)
	}
	sort.Strings(ids)

	m.mu.RLock()
	records := make([]EgressMappingSnapshotRecord, 0, len(ids))
	driftedAccounts := 0
	alertedAccounts := 0
	totalDriftEvents := 0
	inconsistentAccounts := 0
	totalConsistencyIssues := 0
	driftAlertThreshold := normalizeEgressDriftAlertThreshold(snapshot.DriftAlertThreshold)
	for _, authID := range ids {
		entry := mappings[authID]
		consistencyIssues := validateEgressMappingEntryConsistency(entry)
		record := EgressMappingSnapshotRecord{
			AuthID:            authID,
			ProxyIdentity:     redactProxyIdentityForSnapshot(entry.ProxyIdentity, entry.ProxyDigest),
			FirstSeenAt:       entry.FirstSeenAt,
			LastSeenAt:        entry.LastSeenAt,
			DriftCount:        entry.DriftCount,
			DriftAlerted:      entry.DriftCount >= driftAlertThreshold,
			LastDriftAt:       entry.LastDriftAt,
			LastProvider:      entry.LastProvider,
			LastModel:         entry.LastModel,
			ConsistencyStatus: "ok",
		}
		if authEntry, ok := m.auths[authID]; ok && authEntry != nil {
			record.AuthIndex = strings.TrimSpace(authEntry.Index)
			record.Provider = strings.TrimSpace(authEntry.Provider)
		}
		if record.DriftCount > 0 {
			driftedAccounts++
		}
		if record.DriftAlerted {
			alertedAccounts++
		}
		totalDriftEvents += record.DriftCount
		if len(consistencyIssues) > 0 {
			record.ConsistencyStatus = "inconsistent"
			record.ConsistencyIssues = consistencyIssues
			inconsistentAccounts++
			totalConsistencyIssues += len(consistencyIssues)
		}
		records = append(records, record)
	}
	m.mu.RUnlock()

	snapshot.TotalAccounts = len(records)
	snapshot.DriftedAccounts = driftedAccounts
	snapshot.AlertedAccounts = alertedAccounts
	snapshot.TotalDriftEvents = totalDriftEvents
	snapshot.InconsistentAccounts = inconsistentAccounts
	snapshot.TotalConsistencyIssues = totalConsistencyIssues
	snapshot.Accounts = records
	return snapshot
}

func normalizeEgressDriftAlertThreshold(raw int) int {
	if raw <= 0 {
		return defaultEgressDriftAlertThreshold
	}
	return raw
}

func validateEgressMappingEntryConsistency(entry egressMappingEntry) []string {
	issues := make([]string, 0, 4)
	if strings.TrimSpace(entry.ProxyIdentity) == "" {
		issues = append(issues, "missing_proxy_identity")
	}
	if strings.TrimSpace(entry.ProxyDigest) == "" {
		issues = append(issues, "missing_proxy_digest")
	}
	if !entry.FirstSeenAt.IsZero() && !entry.LastSeenAt.IsZero() && entry.FirstSeenAt.After(entry.LastSeenAt) {
		issues = append(issues, "first_seen_after_last_seen")
	}
	if entry.DriftCount > 0 && entry.LastDriftAt.IsZero() {
		issues = append(issues, "drift_without_timestamp")
	}
	if entry.DriftCount == 0 && !entry.LastDriftAt.IsZero() {
		issues = append(issues, "stale_drift_timestamp")
	}
	return issues
}

func redactProxyIdentityForSnapshot(identity, digest string) string {
	trimmedIdentity := strings.TrimSpace(identity)
	if strings.EqualFold(trimmedIdentity, "direct") {
		return "direct"
	}

	tokenSeed := strings.TrimSpace(digest)
	if tokenSeed == "" {
		tokenSeed = trimmedIdentity
	}
	if tokenSeed == "" {
		return "proxy"
	}

	token := digestString(tokenSeed)
	if len(token) > 12 {
		token = token[:12]
	}
	return "proxy#" + token
}

func normalizeProxyIdentityAndDigest(proxyURL string) (string, string) {
	trimmed := strings.TrimSpace(proxyURL)
	if trimmed == "" {
		return "direct", digestString("direct")
	}

	identity := "proxy"
	normalized := trimmed
	if parsed, err := url.Parse(trimmed); err == nil {
		scheme := strings.ToLower(strings.TrimSpace(parsed.Scheme))
		host := strings.ToLower(strings.TrimSpace(parsed.Host))
		if scheme != "" && host != "" {
			identity = scheme + "://" + host
		} else if host != "" {
			identity = host
		}

		parsed.User = nil
		parsed.RawQuery = ""
		parsed.Fragment = ""
		normalized = strings.TrimSpace(parsed.String())
		if normalized == "" {
			normalized = trimmed
		}
	}
	return identity, digestString(normalized)
}

func digestString(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func persistEgressMappingState(path string, mappings map[string]egressMappingEntry, now time.Time) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil
	}
	state := egressMappingState{
		Version:   1,
		UpdatedAt: now,
		Accounts:  mappings,
	}
	raw, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if dir != "." {
		if errMkdir := os.MkdirAll(dir, 0o700); errMkdir != nil {
			return errMkdir
		}
	}
	tmp := path + ".tmp"
	if errWrite := os.WriteFile(tmp, raw, 0o600); errWrite != nil {
		return errWrite
	}
	return os.Rename(tmp, path)
}

func loadEgressMappingState(path string) (map[string]egressMappingEntry, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return make(map[string]egressMappingEntry), nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return make(map[string]egressMappingEntry), nil
		}
		return nil, err
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return make(map[string]egressMappingEntry), nil
	}
	var state egressMappingState
	if errUnmarshal := json.Unmarshal(data, &state); errUnmarshal != nil {
		return nil, errUnmarshal
	}
	out := make(map[string]egressMappingEntry, len(state.Accounts))
	for rawID, entry := range state.Accounts {
		id := strings.TrimSpace(rawID)
		if id == "" {
			continue
		}
		entry.ProxyIdentity = strings.TrimSpace(entry.ProxyIdentity)
		entry.ProxyDigest = strings.TrimSpace(entry.ProxyDigest)
		if entry.DriftCount < 0 {
			entry.DriftCount = 0
		}
		entry.LastProvider = strings.TrimSpace(strings.ToLower(entry.LastProvider))
		entry.LastModel = strings.TrimSpace(entry.LastModel)
		out[id] = entry
	}
	return out, nil
}

func cloneEgressMappings(source map[string]egressMappingEntry) map[string]egressMappingEntry {
	if len(source) == 0 {
		return make(map[string]egressMappingEntry)
	}
	out := make(map[string]egressMappingEntry, len(source))
	for k, v := range source {
		out[k] = v
	}
	return out
}
