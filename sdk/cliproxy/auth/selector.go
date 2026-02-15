package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/thinking"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
)

// RoundRobinSelector provides a simple provider scoped round-robin selection strategy.
type RoundRobinSelector struct {
	mu      sync.Mutex
	cursors map[string]string
	maxKeys int
}

// FillFirstSelector selects the first available credential (deterministic ordering).
// This "burns" one account before moving to the next, which can help stagger
// rolling-window subscription caps (e.g. chat message limits).
type FillFirstSelector struct{}

type sessionAffinityBinding struct {
	AuthID    string
	ExpiresAt time.Time
}

const sessionAffinityTTL = 30 * time.Minute

var (
	sessionAffinityMu       sync.Mutex
	sessionAffinityBindings = map[string]sessionAffinityBinding{}
)

func resetSessionAffinityBindings() {
	sessionAffinityMu.Lock()
	defer sessionAffinityMu.Unlock()
	clear(sessionAffinityBindings)
}

type blockReason int

const (
	blockReasonNone blockReason = iota
	blockReasonCooldown
	blockReasonDisabled
	blockReasonOther
)

type modelCooldownError struct {
	model    string
	resetIn  time.Duration
	provider string
}

func newModelCooldownError(model, provider string, resetIn time.Duration) *modelCooldownError {
	if resetIn < 0 {
		resetIn = 0
	}
	return &modelCooldownError{
		model:    model,
		provider: provider,
		resetIn:  resetIn,
	}
}

func (e *modelCooldownError) Error() string {
	modelName := e.model
	if modelName == "" {
		modelName = "requested model"
	}
	message := fmt.Sprintf("All credentials for model %s are cooling down", modelName)
	if e.provider != "" {
		message = fmt.Sprintf("%s via provider %s", message, e.provider)
	}
	resetSeconds := int(math.Ceil(e.resetIn.Seconds()))
	if resetSeconds < 0 {
		resetSeconds = 0
	}
	displayDuration := e.resetIn
	if displayDuration > 0 && displayDuration < time.Second {
		displayDuration = time.Second
	} else {
		displayDuration = displayDuration.Round(time.Second)
	}
	errorBody := map[string]any{
		"code":          "model_cooldown",
		"message":       message,
		"model":         e.model,
		"reset_time":    displayDuration.String(),
		"reset_seconds": resetSeconds,
	}
	if e.provider != "" {
		errorBody["provider"] = e.provider
	}
	payload := map[string]any{"error": errorBody}
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Sprintf(`{"error":{"code":"model_cooldown","message":"%s"}}`, message)
	}
	return string(data)
}

func (e *modelCooldownError) StatusCode() int {
	return http.StatusTooManyRequests
}

func (e *modelCooldownError) Headers() http.Header {
	headers := make(http.Header)
	headers.Set("Content-Type", "application/json")
	resetSeconds := int(math.Ceil(e.resetIn.Seconds()))
	if resetSeconds < 0 {
		resetSeconds = 0
	}
	headers.Set("Retry-After", strconv.Itoa(resetSeconds))
	return headers
}

func authPriority(auth *Auth) int {
	if auth == nil || auth.Attributes == nil {
		return 0
	}
	raw := strings.TrimSpace(auth.Attributes["priority"])
	if raw == "" {
		return 0
	}
	parsed, err := strconv.Atoi(raw)
	if err != nil {
		return 0
	}
	return parsed
}

func canonicalModelKey(model string) string {
	model = strings.TrimSpace(model)
	if model == "" {
		return ""
	}
	parsed := thinking.ParseSuffix(model)
	modelName := strings.TrimSpace(parsed.ModelName)
	if modelName == "" {
		return model
	}
	return modelName
}

func sessionAffinityKey(provider, model string, opts cliproxyexecutor.Options) string {
	if len(opts.Metadata) == 0 {
		return ""
	}
	raw, ok := opts.Metadata[cliproxyexecutor.SessionAffinityKeyMetadataKey]
	if !ok || raw == nil {
		return ""
	}
	sessionID, ok := raw.(string)
	if !ok {
		return ""
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return ""
	}
	return provider + "|" + canonicalModelKey(model) + "|" + sessionID
}

func cleanupSessionAffinityLocked(now time.Time) {
	for key, binding := range sessionAffinityBindings {
		if binding.ExpiresAt.IsZero() || !binding.ExpiresAt.After(now) {
			delete(sessionAffinityBindings, key)
		}
	}
}

func loadSessionAffinity(key string, now time.Time) string {
	if key == "" {
		return ""
	}
	sessionAffinityMu.Lock()
	defer sessionAffinityMu.Unlock()
	cleanupSessionAffinityLocked(now)
	binding, ok := sessionAffinityBindings[key]
	if !ok {
		return ""
	}
	return binding.AuthID
}

func storeSessionAffinity(key, authID string, now time.Time) {
	if key == "" || strings.TrimSpace(authID) == "" {
		return
	}
	sessionAffinityMu.Lock()
	defer sessionAffinityMu.Unlock()
	cleanupSessionAffinityLocked(now)
	sessionAffinityBindings[key] = sessionAffinityBinding{
		AuthID:    authID,
		ExpiresAt: now.Add(sessionAffinityTTL),
	}
}

func collectAvailableByPriority(auths []*Auth, model string, now time.Time) (available map[int][]*Auth, cooldownCount int, earliest time.Time) {
	available = make(map[int][]*Auth)
	for i := 0; i < len(auths); i++ {
		candidate := auths[i]
		blocked, reason, next := isAuthBlockedForModel(candidate, model, now)
		if !blocked {
			priority := authPriority(candidate)
			available[priority] = append(available[priority], candidate)
			continue
		}
		if reason == blockReasonCooldown {
			cooldownCount++
			if !next.IsZero() && (earliest.IsZero() || next.Before(earliest)) {
				earliest = next
			}
		}
	}
	return available, cooldownCount, earliest
}

func getAvailableAuths(auths []*Auth, provider, model string, now time.Time) ([]*Auth, error) {
	if len(auths) == 0 {
		return nil, &Error{Code: "auth_not_found", Message: "no auth candidates"}
	}

	availableByPriority, cooldownCount, earliest := collectAvailableByPriority(auths, model, now)
	if len(availableByPriority) == 0 {
		if cooldownCount == len(auths) && !earliest.IsZero() {
			providerForError := provider
			if providerForError == "mixed" {
				providerForError = ""
			}
			resetIn := earliest.Sub(now)
			if resetIn < 0 {
				resetIn = 0
			}
			return nil, newModelCooldownError(model, providerForError, resetIn)
		}
		return nil, &Error{Code: "auth_unavailable", Message: "no auth available"}
	}

	bestPriority := 0
	found := false
	for priority := range availableByPriority {
		if !found || priority > bestPriority {
			bestPriority = priority
			found = true
		}
	}

	available := availableByPriority[bestPriority]
	if len(available) > 1 {
		sort.Slice(available, func(i, j int) bool { return available[i].ID < available[j].ID })
	}
	return available, nil
}

// Pick selects auths using sticky pointer semantics:
// keep using the current credential while it remains usable; only move when it is blocked.
func (s *RoundRobinSelector) Pick(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
	_ = ctx
	now := time.Now()
	available, err := getAvailableAuths(auths, provider, model, now)
	if err != nil {
		return nil, err
	}
	affinityKey := sessionAffinityKey(provider, model, opts)
	if pinnedID := loadSessionAffinity(affinityKey, now); pinnedID != "" {
		for idx, candidate := range available {
			if candidate != nil && candidate.ID == pinnedID {
				key := provider + ":" + canonicalModelKey(model)
				s.mu.Lock()
				if s.cursors == nil {
					s.cursors = make(map[string]string)
				}
				s.cursors[key] = available[idx].ID
				s.mu.Unlock()
				storeSessionAffinity(affinityKey, candidate.ID, now)
				return candidate, nil
			}
		}
	}
	key := provider + ":" + canonicalModelKey(model)
	s.mu.Lock()
	if s.cursors == nil {
		s.cursors = make(map[string]string)
	}
	limit := s.maxKeys
	if limit <= 0 {
		limit = 4096
	}
	lastSelectedID, hasCursor := s.cursors[key]
	if !hasCursor && len(s.cursors) >= limit {
		s.cursors = make(map[string]string)
		lastSelectedID = ""
		hasCursor = false
	}
	selected := available[0]
	if hasCursor && strings.TrimSpace(lastSelectedID) != "" {
		foundCurrent := false
		for i := 0; i < len(available); i++ {
			if available[i] != nil && available[i].ID == lastSelectedID {
				selected = available[i]
				foundCurrent = true
				break
			}
		}
		// Previous credential is no longer available: advance deterministically to the
		// next ID in sorted order, wrapping around.
		if !foundCurrent {
			nextIdx := sort.Search(len(available), func(i int) bool {
				if available[i] == nil {
					return false
				}
				return available[i].ID > lastSelectedID
			})
			if nextIdx >= len(available) {
				nextIdx = 0
			}
			selected = available[nextIdx]
		}
	}
	s.cursors[key] = selected.ID
	s.mu.Unlock()
	storeSessionAffinity(affinityKey, selected.ID, now)
	return selected, nil
}

// Pick selects the first available auth for the provider in a deterministic manner.
func (s *FillFirstSelector) Pick(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
	_ = ctx
	now := time.Now()
	available, err := getAvailableAuths(auths, provider, model, now)
	if err != nil {
		return nil, err
	}
	affinityKey := sessionAffinityKey(provider, model, opts)
	if pinnedID := loadSessionAffinity(affinityKey, now); pinnedID != "" {
		for _, candidate := range available {
			if candidate != nil && candidate.ID == pinnedID {
				storeSessionAffinity(affinityKey, candidate.ID, now)
				return candidate, nil
			}
		}
	}
	selected := available[0]
	storeSessionAffinity(affinityKey, selected.ID, now)
	return selected, nil
}

func isAuthBlockedForModel(auth *Auth, model string, now time.Time) (bool, blockReason, time.Time) {
	if auth == nil {
		return true, blockReasonOther, time.Time{}
	}
	if auth.Disabled || auth.Status == StatusDisabled {
		return true, blockReasonDisabled, time.Time{}
	}
	if model != "" {
		if len(auth.ModelStates) > 0 {
			state, ok := auth.ModelStates[model]
			if (!ok || state == nil) && model != "" {
				baseModel := canonicalModelKey(model)
				if baseModel != "" && baseModel != model {
					state, ok = auth.ModelStates[baseModel]
				}
			}
			if ok && state != nil {
				if state.Status == StatusDisabled {
					return true, blockReasonDisabled, time.Time{}
				}
				if state.Unavailable {
					if state.NextRetryAfter.IsZero() {
						return false, blockReasonNone, time.Time{}
					}
					if state.NextRetryAfter.After(now) {
						next := state.NextRetryAfter
						if !state.Quota.NextRecoverAt.IsZero() && state.Quota.NextRecoverAt.After(now) {
							next = state.Quota.NextRecoverAt
						}
						if next.Before(now) {
							next = now
						}
						if state.Quota.Exceeded {
							return true, blockReasonCooldown, next
						}
						return true, blockReasonOther, next
					}
				}
				return false, blockReasonNone, time.Time{}
			}
		}
		return false, blockReasonNone, time.Time{}
	}
	if auth.Unavailable && auth.NextRetryAfter.After(now) {
		next := auth.NextRetryAfter
		if !auth.Quota.NextRecoverAt.IsZero() && auth.Quota.NextRecoverAt.After(now) {
			next = auth.Quota.NextRecoverAt
		}
		if next.Before(now) {
			next = now
		}
		if auth.Quota.Exceeded {
			return true, blockReasonCooldown, next
		}
		return true, blockReasonOther, next
	}
	return false, blockReasonNone, time.Time{}
}
