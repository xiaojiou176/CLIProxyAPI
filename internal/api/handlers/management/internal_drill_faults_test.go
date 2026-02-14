package management

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	internalconfig "github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

func TestPostInternalDrillFault_ProxyFailureEmitsPenaltyAssertionSignals(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv(internalDrillFaultEnvKey, "true")

	handler := &Handler{
		cfg:         &internalconfig.Config{Port: 2456},
		authManager: coreauth.NewManager(nil, nil, nil),
	}
	statusCode, body := executeInternalDrillFaultRequest(
		t,
		handler,
		map[string]any{
			"scenario": coreauth.InternalDrillFaultScenarioProxyFailure,
			"count":    1,
		},
	)
	if statusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%v", statusCode, http.StatusOK, body)
	}

	assertions := mustNestedMap(t, body, "assertions")
	if !mustBool(t, assertions, "account_penalty_unchanged") {
		t.Fatalf("assertions.account_penalty_unchanged = false, want true")
	}
	if got := mustNumber(t, assertions, "account_penalty_delta"); got != 0 {
		t.Fatalf("assertions.account_penalty_delta = %v, want 0", got)
	}
	if !mustBool(t, body, "account_penalty_unchanged") {
		t.Fatalf("account_penalty_unchanged = false, want true")
	}

	evidence := mustNestedMap(t, body, "evidence")
	signals := mustNestedMap(t, evidence, "assertion_signals")
	if !mustBool(t, signals, "account_penalty_unchanged") {
		t.Fatalf("evidence.assertion_signals.account_penalty_unchanged = false, want true")
	}
}

func TestPostInternalDrillFault_QuotaScenarioEmitsAccountSwitchSignal(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv(internalDrillFaultEnvKey, "true")

	handler := &Handler{
		cfg:         &internalconfig.Config{Port: 2456},
		authManager: coreauth.NewManager(nil, nil, nil),
	}
	statusCode, body := executeInternalDrillFaultRequest(
		t,
		handler,
		map[string]any{
			"scenario": coreauth.InternalDrillFaultScenarioAccountQuotaExhausted,
			"count":    1,
		},
	)
	if statusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%v", statusCode, http.StatusOK, body)
	}

	assertions := mustNestedMap(t, body, "assertions")
	if !mustBool(t, assertions, "account_switched") {
		t.Fatalf("assertions.account_switched = false, want true")
	}
	if !mustBool(t, body, "account_switched") {
		t.Fatalf("account_switched = false, want true")
	}

	selection := mustNestedMap(t, body, "selection")
	if !mustBool(t, selection, "account_switched") {
		t.Fatalf("selection.account_switched = false, want true")
	}

	evidence := mustNestedMap(t, body, "evidence")
	signals := mustNestedMap(t, evidence, "assertion_signals")
	if !mustBool(t, signals, "account_switched") {
		t.Fatalf("evidence.assertion_signals.account_switched = false, want true")
	}
}

func executeInternalDrillFaultRequest(t *testing.T, handler *Handler, payload map[string]any) (int, map[string]any) {
	t.Helper()

	rawPayload, errMarshal := json.Marshal(payload)
	if errMarshal != nil {
		t.Fatalf("Marshal(payload): %v", errMarshal)
	}

	router := gin.New()
	router.POST("/internal/drill/faults", handler.PostInternalDrillFault)
	req := httptest.NewRequest(http.MethodPost, "/internal/drill/faults", bytes.NewReader(rawPayload))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "127.0.0.1:39001"

	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	body := map[string]any{}
	if len(resp.Body.Bytes()) == 0 {
		return resp.Code, body
	}
	if errUnmarshal := json.Unmarshal(resp.Body.Bytes(), &body); errUnmarshal != nil {
		t.Fatalf("Unmarshal(response): %v | raw=%s", errUnmarshal, resp.Body.String())
	}
	return resp.Code, body
}

func mustNestedMap(t *testing.T, root map[string]any, key string) map[string]any {
	t.Helper()
	rawValue, ok := root[key]
	if !ok {
		t.Fatalf("missing key %q in %v", key, root)
	}
	nested, ok := rawValue.(map[string]any)
	if !ok {
		t.Fatalf("key %q type=%T, want map[string]any", key, rawValue)
	}
	return nested
}

func mustBool(t *testing.T, root map[string]any, key string) bool {
	t.Helper()
	rawValue, ok := root[key]
	if !ok {
		t.Fatalf("missing key %q in %v", key, root)
	}
	value, ok := rawValue.(bool)
	if !ok {
		t.Fatalf("key %q type=%T, want bool", key, rawValue)
	}
	return value
}

func mustNumber(t *testing.T, root map[string]any, key string) float64 {
	t.Helper()
	rawValue, ok := root[key]
	if !ok {
		t.Fatalf("missing key %q in %v", key, root)
	}
	value, ok := rawValue.(float64)
	if !ok {
		t.Fatalf("key %q type=%T, want float64", key, rawValue)
	}
	return value
}
