package auth

import (
	"strings"
	"time"
)

// MergeRuntimeFailureState carries over active cooldown/disabled runtime fields
// from src into dst. It prevents refresh/reload flows from resurrecting accounts
// that were recently frozen or disabled by policy.
func MergeRuntimeFailureState(dst *Auth, src *Auth, now time.Time) {
	if dst == nil || src == nil {
		return
	}

	// Fatal/disabled state always wins.
	if src.Disabled || src.Status == StatusDisabled {
		dst.Disabled = true
		dst.Status = StatusDisabled
		dst.Unavailable = true
		dst.NextRetryAfter = time.Time{}
		dst.Quota = src.Quota
		if strings.TrimSpace(src.StatusMessage) != "" {
			dst.StatusMessage = src.StatusMessage
		}
		dst.LastError = cloneError(src.LastError)
		dst.ModelStates = cloneModelStates(src.ModelStates)
		return
	}

	// Carry auth-level cooldown/blocked state only while still active.
	if src.Unavailable && src.NextRetryAfter.After(now) {
		dst.Unavailable = true
		dst.NextRetryAfter = src.NextRetryAfter
		dst.Quota = src.Quota
		if dst.Status != StatusDisabled {
			dst.Status = StatusError
		}
		if strings.TrimSpace(src.StatusMessage) != "" {
			dst.StatusMessage = src.StatusMessage
		}
		dst.LastError = cloneError(src.LastError)
	}

	// Carry per-model blocked states that are still active.
	if len(src.ModelStates) == 0 {
		return
	}
	if dst.ModelStates == nil {
		dst.ModelStates = make(map[string]*ModelState, len(src.ModelStates))
	}
	for model, state := range src.ModelStates {
		if state == nil {
			continue
		}
		if shouldCarryModelRuntimeState(state, now) {
			dst.ModelStates[model] = state.Clone()
		}
	}
	updateAggregatedAvailability(dst, now)
}

func shouldCarryModelRuntimeState(state *ModelState, now time.Time) bool {
	if state == nil {
		return false
	}
	if state.Status == StatusDisabled {
		return true
	}
	if state.Unavailable {
		if state.NextRetryAfter.IsZero() || state.NextRetryAfter.After(now) {
			return true
		}
	}
	if state.Quota.Exceeded {
		if state.Quota.NextRecoverAt.IsZero() || state.Quota.NextRecoverAt.After(now) {
			return true
		}
	}
	return false
}

func cloneModelStates(states map[string]*ModelState) map[string]*ModelState {
	if len(states) == 0 {
		return nil
	}
	out := make(map[string]*ModelState, len(states))
	for model, state := range states {
		if state != nil {
			out[model] = state.Clone()
		}
	}
	return out
}
