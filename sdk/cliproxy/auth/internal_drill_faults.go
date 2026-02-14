package auth

import (
	"context"
	"fmt"
	"net/http"
	"strings"
)

const (
	InternalDrillFaultScenarioProxyFailure          = "proxy-failure"
	InternalDrillFaultScenarioAccountQuotaExhausted = "account-quota-exhausted"
)

type internalDrillFaultAction int

const (
	internalDrillFaultActionNone internalDrillFaultAction = iota
	internalDrillFaultActionFailFast
	internalDrillFaultActionContinue
)

func NormalizeInternalDrillFaultScenario(raw string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case InternalDrillFaultScenarioProxyFailure:
		return InternalDrillFaultScenarioProxyFailure, true
	case InternalDrillFaultScenarioAccountQuotaExhausted:
		return InternalDrillFaultScenarioAccountQuotaExhausted, true
	default:
		return "", false
	}
}

func (m *Manager) ApplyInternalDrillFault(scenario string, count int) (int, error) {
	if m == nil {
		return 0, fmt.Errorf("manager is nil")
	}
	normalized, ok := NormalizeInternalDrillFaultScenario(scenario)
	if !ok {
		return 0, fmt.Errorf("unsupported scenario: %s", strings.TrimSpace(scenario))
	}
	if count <= 0 {
		count = 1
	}

	m.drillMu.Lock()
	defer m.drillMu.Unlock()
	if m.drillFaultRemaining == nil {
		m.drillFaultRemaining = make(map[string]int)
	}
	m.drillFaultRemaining[normalized] += count
	return m.drillFaultRemaining[normalized], nil
}

func (m *Manager) InternalDrillFaultSnapshot() map[string]int {
	out := make(map[string]int)
	if m == nil {
		return out
	}

	m.drillMu.Lock()
	defer m.drillMu.Unlock()
	for scenario, remaining := range m.drillFaultRemaining {
		if remaining > 0 {
			out[scenario] = remaining
		}
	}
	return out
}

func (m *Manager) consumeInternalDrillFault(scenario string) bool {
	if m == nil {
		return false
	}

	m.drillMu.Lock()
	defer m.drillMu.Unlock()
	remaining := m.drillFaultRemaining[scenario]
	if remaining <= 0 {
		return false
	}
	remaining--
	if remaining == 0 {
		delete(m.drillFaultRemaining, scenario)
		return true
	}
	m.drillFaultRemaining[scenario] = remaining
	return true
}

func (m *Manager) consumeInternalDrillFaultForPickedAuth(ctx context.Context, auth *Auth, provider, model string) (internalDrillFaultAction, error) {
	if m.consumeInternalDrillFault(InternalDrillFaultScenarioProxyFailure) {
		return internalDrillFaultActionFailFast, &Error{
			Code:       "proxy_failure_injected",
			Message:    "internal drill injected proxy failure",
			Retryable:  true,
			HTTPStatus: http.StatusBadGateway,
		}
	}
	if m.consumeInternalDrillFault(InternalDrillFaultScenarioAccountQuotaExhausted) {
		injectedErr := &Error{
			Code:       "quota_exhausted_injected",
			Message:    "internal drill injected account quota exhaustion",
			Retryable:  true,
			HTTPStatus: http.StatusTooManyRequests,
		}
		if auth != nil {
			m.MarkResult(ctx, Result{
				AuthID:   auth.ID,
				Provider: provider,
				Model:    model,
				Success:  false,
				Error:    injectedErr,
			})
		}
		return internalDrillFaultActionContinue, injectedErr
	}
	return internalDrillFaultActionNone, nil
}
