package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	gin "github.com/gin-gonic/gin"
	proxyconfig "github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v6/sdk/access"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
)

func newTestServer(t *testing.T) *Server {
	return newTestServerWithPort(t, 0)
}

func newTestServerWithPort(t *testing.T, port int) *Server {
	t.Helper()

	gin.SetMode(gin.TestMode)

	tmpDir := t.TempDir()
	authDir := filepath.Join(tmpDir, "auth")
	if err := os.MkdirAll(authDir, 0o700); err != nil {
		t.Fatalf("failed to create auth dir: %v", err)
	}

	cfg := &proxyconfig.Config{
		SDKConfig: sdkconfig.SDKConfig{
			APIKeys: []string{"test-key"},
		},
		Port:                   port,
		AuthDir:                authDir,
		Debug:                  true,
		LoggingToFile:          false,
		UsageStatisticsEnabled: false,
	}

	authManager := auth.NewManager(nil, nil, nil)
	accessManager := sdkaccess.NewManager()

	configPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("port: 2456\n"), 0o644); err != nil {
		t.Fatalf("failed to create config file: %v", err)
	}
	return NewServer(cfg, authManager, accessManager, configPath)
}

type serverVisibilityExecutor struct {
	calls int
}

func (e *serverVisibilityExecutor) Identifier() string { return "server-model-visibility-provider" }

func (e *serverVisibilityExecutor) Execute(context.Context, *auth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	e.calls++
	return coreexecutor.Response{Payload: []byte(`{"ok":true}`)}, nil
}

func (e *serverVisibilityExecutor) ExecuteStream(context.Context, *auth.Auth, coreexecutor.Request, coreexecutor.Options) (<-chan coreexecutor.StreamChunk, error) {
	return nil, errors.New("not implemented")
}

func (e *serverVisibilityExecutor) Refresh(ctx context.Context, entry *auth.Auth) (*auth.Auth, error) {
	return entry, nil
}

func (e *serverVisibilityExecutor) CountTokens(context.Context, *auth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (e *serverVisibilityExecutor) HttpRequest(context.Context, *auth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

func readServerModelIDs(t *testing.T, body []byte) []string {
	t.Helper()
	var payload struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("unmarshal /v1/models response: %v", err)
	}
	ids := make([]string, 0, len(payload.Data))
	for _, item := range payload.Data {
		ids = append(ids, item.ID)
	}
	sort.Strings(ids)
	return ids
}

func TestAmpProviderModelRoutes(t *testing.T) {
	testCases := []struct {
		name         string
		path         string
		wantStatus   int
		wantContains string
	}{
		{
			name:         "openai root models",
			path:         "/api/provider/openai/models",
			wantStatus:   http.StatusOK,
			wantContains: `"object":"list"`,
		},
		{
			name:         "groq root models",
			path:         "/api/provider/groq/models",
			wantStatus:   http.StatusOK,
			wantContains: `"object":"list"`,
		},
		{
			name:         "openai models",
			path:         "/api/provider/openai/v1/models",
			wantStatus:   http.StatusOK,
			wantContains: `"object":"list"`,
		},
		{
			name:         "anthropic models",
			path:         "/api/provider/anthropic/v1/models",
			wantStatus:   http.StatusOK,
			wantContains: `"data"`,
		},
		{
			name:         "google models v1",
			path:         "/api/provider/google/v1/models",
			wantStatus:   http.StatusOK,
			wantContains: `"models"`,
		},
		{
			name:         "google models v1beta",
			path:         "/api/provider/google/v1beta/models",
			wantStatus:   http.StatusOK,
			wantContains: `"models"`,
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			server := newTestServer(t)

			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			req.Header.Set("Authorization", "Bearer test-key")

			rr := httptest.NewRecorder()
			server.engine.ServeHTTP(rr, req)

			if rr.Code != tc.wantStatus {
				t.Fatalf("unexpected status code for %s: got %d want %d; body=%s", tc.path, rr.Code, tc.wantStatus, rr.Body.String())
			}
			if body := rr.Body.String(); !strings.Contains(body, tc.wantContains) {
				t.Fatalf("response body for %s missing %q: %s", tc.path, tc.wantContains, body)
			}
		})
	}
}

func TestManagementEgressMappingRoute_RegisteredAndRedacted(t *testing.T) {
	const managementPassword = "route-test-management-password"
	if err := os.Setenv("MANAGEMENT_PASSWORD", managementPassword); err != nil {
		t.Fatalf("Setenv(MANAGEMENT_PASSWORD): %v", err)
	}
	t.Cleanup(func() {
		_ = os.Unsetenv("MANAGEMENT_PASSWORD")
	})

	server := newTestServer(t)
	request := httptest.NewRequest(http.MethodGet, "/v0/management/egress-mapping", nil)
	request.Header.Set("X-Management-Key", managementPassword)

	response := httptest.NewRecorder()
	server.engine.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", response.Code, http.StatusOK, response.Body.String())
	}

	var body struct {
		Available          bool   `json:"available"`
		SensitiveRedaction string `json:"sensitive_redaction"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("Unmarshal(response): %v", err)
	}
	if !body.Available {
		t.Fatalf("available = false, want true")
	}
	if body.SensitiveRedaction != "applied" {
		t.Fatalf("sensitive_redaction = %q, want %q", body.SensitiveRedaction, "applied")
	}
	if strings.Contains(response.Body.String(), "proxy_digest") {
		t.Fatalf("response leaked proxy_digest: %s", response.Body.String())
	}
}

func TestManagementModelVisibility_ImmediateEffectOnModelsAndBlocking(t *testing.T) {
	const managementPassword = "model-visibility-management-password"
	if err := os.Setenv("MANAGEMENT_PASSWORD", managementPassword); err != nil {
		t.Fatalf("Setenv(MANAGEMENT_PASSWORD): %v", err)
	}
	t.Cleanup(func() {
		_ = os.Unsetenv("MANAGEMENT_PASSWORD")
	})

	server := newTestServer(t)

	executor := &serverVisibilityExecutor{}
	server.handlers.AuthManager.RegisterExecutor(executor)
	entry := &auth.Auth{
		ID:       "server-model-visibility-auth",
		Provider: executor.Identifier(),
		Status:   auth.StatusActive,
	}
	if _, err := server.handlers.AuthManager.Register(context.Background(), entry); err != nil {
		t.Fatalf("Register(auth): %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(entry.ID, entry.Provider, []*registry.ModelInfo{
		{ID: "gpt-5.3-codex", Object: "model", Created: 1, OwnedBy: "tests"},
		{ID: "gemini-3.0-pro", Object: "model", Created: 2, OwnedBy: "tests"},
	})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(entry.ID)
	})

	beforeReq := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	beforeReq.Header.Set("Authorization", "Bearer test-key")
	beforeResp := httptest.NewRecorder()
	server.engine.ServeHTTP(beforeResp, beforeReq)
	if beforeResp.Code != http.StatusOK {
		t.Fatalf("before /v1/models status = %d, want %d, body=%s", beforeResp.Code, http.StatusOK, beforeResp.Body.String())
	}
	beforeIDs := readServerModelIDs(t, beforeResp.Body.Bytes())
	if len(beforeIDs) != 2 || beforeIDs[0] != "gemini-3.0-pro" || beforeIDs[1] != "gpt-5.3-codex" {
		t.Fatalf("before /v1/models ids = %v, want [gemini-3.0-pro gpt-5.3-codex]", beforeIDs)
	}

	updateBody := `{
		"enabled": true,
		"namespaces": {
			"codex": ["gpt-5.3-codex"],
			"antigravity": ["gemini-3.0-pro"]
		},
		"host-namespaces": {
			"codex.local": "codex",
			"antigravity.local": "antigravity"
		}
	}`
	updateReq := httptest.NewRequest(http.MethodPut, "/v0/management/model-visibility", strings.NewReader(updateBody))
	updateReq.Header.Set("Content-Type", "application/json")
	updateReq.Header.Set("X-Management-Key", managementPassword)
	updateResp := httptest.NewRecorder()
	server.engine.ServeHTTP(updateResp, updateReq)
	if updateResp.Code != http.StatusOK {
		t.Fatalf("PUT /v0/management/model-visibility status = %d, want %d, body=%s", updateResp.Code, http.StatusOK, updateResp.Body.String())
	}

	filteredReq := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	filteredReq.Header.Set("Authorization", "Bearer test-key")
	filteredReq.Host = "antigravity.local:2456"
	filteredResp := httptest.NewRecorder()
	server.engine.ServeHTTP(filteredResp, filteredReq)
	if filteredResp.Code != http.StatusOK {
		t.Fatalf("after /v1/models status = %d, want %d, body=%s", filteredResp.Code, http.StatusOK, filteredResp.Body.String())
	}
	filteredIDs := readServerModelIDs(t, filteredResp.Body.Bytes())
	if len(filteredIDs) != 1 || filteredIDs[0] != "gemini-3.0-pro" {
		t.Fatalf("after /v1/models ids = %v, want [gemini-3.0-pro]", filteredIDs)
	}

	blockedReq := httptest.NewRequest(
		http.MethodPost,
		"/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-5.3-codex","messages":[{"role":"user","content":"hello"}]}`),
	)
	blockedReq.Header.Set("Authorization", "Bearer test-key")
	blockedReq.Header.Set("Content-Type", "application/json")
	blockedReq.Host = "antigravity.local:2456"
	blockedResp := httptest.NewRecorder()
	server.engine.ServeHTTP(blockedResp, blockedReq)
	if blockedResp.Code != http.StatusForbidden {
		t.Fatalf("blocked /v1/chat/completions status = %d, want %d, body=%s", blockedResp.Code, http.StatusForbidden, blockedResp.Body.String())
	}
	if executor.calls != 0 {
		t.Fatalf("executor calls = %d, want 0", executor.calls)
	}
}

func TestInternalDrillFaultRoute_ShadowGuardAndApply(t *testing.T) {
	const envKey = "CLIPROXY_ENABLE_SHADOW_DRILL_FAULTS"
	const path = "/internal/drill/faults"

	if err := os.Unsetenv(envKey); err != nil {
		t.Fatalf("Unsetenv(%s): %v", envKey, err)
	}
	serverDisabled := newTestServerWithPort(t, 2456)
	disabledReq := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"scenario":"proxy-failure"}`))
	disabledReq.RemoteAddr = "127.0.0.1:9999"
	disabledReq.Header.Set("Content-Type", "application/json")
	disabledResp := httptest.NewRecorder()
	serverDisabled.engine.ServeHTTP(disabledResp, disabledReq)
	if disabledResp.Code != http.StatusNotFound {
		t.Fatalf("disabled status = %d, want %d, body=%s", disabledResp.Code, http.StatusNotFound, disabledResp.Body.String())
	}

	if err := os.Setenv(envKey, "1"); err != nil {
		t.Fatalf("Setenv(%s): %v", envKey, err)
	}
	t.Cleanup(func() {
		_ = os.Unsetenv(envKey)
	})
	serverEnabled := newTestServerWithPort(t, 2456)

	remoteReq := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"scenario":"proxy-failure"}`))
	remoteReq.Header.Set("Content-Type", "application/json")
	remoteResp := httptest.NewRecorder()
	serverEnabled.engine.ServeHTTP(remoteResp, remoteReq)
	if remoteResp.Code != http.StatusForbidden {
		t.Fatalf("remote status = %d, want %d, body=%s", remoteResp.Code, http.StatusForbidden, remoteResp.Body.String())
	}

	localReq := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"scenario":"proxy-failure","count":2}`))
	localReq.RemoteAddr = "127.0.0.1:9999"
	localReq.Header.Set("Content-Type", "application/json")
	localResp := httptest.NewRecorder()
	serverEnabled.engine.ServeHTTP(localResp, localReq)
	if localResp.Code != http.StatusOK {
		t.Fatalf("local status = %d, want %d, body=%s", localResp.Code, http.StatusOK, localResp.Body.String())
	}

	var body struct {
		OK        bool           `json:"ok"`
		Scenario  string         `json:"scenario"`
		Applied   int            `json:"applied"`
		Remaining int            `json:"remaining"`
		Snapshot  map[string]int `json:"snapshot"`
		Evidence  struct {
			RemainingAfterApply int            `json:"remaining_after_apply"`
			SnapshotAfterApply  map[string]int `json:"snapshot_after_apply"`
		} `json:"evidence"`
		Guardrails struct {
			ShadowOnly         bool     `json:"shadow_only"`
			LocalhostOnly      bool     `json:"localhost_only"`
			EnvKey             string   `json:"env_key"`
			Port               int      `json:"port"`
			LocalhostIP        string   `json:"localhost_ip"`
			SupportedScenarios []string `json:"supported_scenarios"`
		} `json:"guardrails"`
	}
	if err := json.Unmarshal(localResp.Body.Bytes(), &body); err != nil {
		t.Fatalf("Unmarshal(local response): %v", err)
	}
	if !body.OK {
		t.Fatalf("ok = false, want true")
	}
	if body.Scenario != "proxy-failure" {
		t.Fatalf("scenario = %q, want %q", body.Scenario, "proxy-failure")
	}
	if body.Applied != 2 {
		t.Fatalf("applied = %d, want %d", body.Applied, 2)
	}
	if body.Remaining != 2 {
		t.Fatalf("remaining = %d, want %d", body.Remaining, 2)
	}
	if body.Snapshot["proxy-failure"] != 2 {
		t.Fatalf("snapshot[proxy-failure] = %d, want %d", body.Snapshot["proxy-failure"], 2)
	}
	if body.Evidence.RemainingAfterApply != 2 {
		t.Fatalf("evidence.remaining_after_apply = %d, want %d", body.Evidence.RemainingAfterApply, 2)
	}
	if body.Evidence.SnapshotAfterApply["proxy-failure"] != 2 {
		t.Fatalf("evidence.snapshot_after_apply[proxy-failure] = %d, want %d", body.Evidence.SnapshotAfterApply["proxy-failure"], 2)
	}
	if !body.Guardrails.ShadowOnly {
		t.Fatalf("guardrails.shadow_only = false, want true")
	}
	if !body.Guardrails.LocalhostOnly {
		t.Fatalf("guardrails.localhost_only = false, want true")
	}
	if body.Guardrails.EnvKey != envKey {
		t.Fatalf("guardrails.env_key = %q, want %q", body.Guardrails.EnvKey, envKey)
	}
	if body.Guardrails.Port != 2456 {
		t.Fatalf("guardrails.port = %d, want %d", body.Guardrails.Port, 2456)
	}
	if body.Guardrails.LocalhostIP != "127.0.0.1" {
		t.Fatalf("guardrails.localhost_ip = %q, want %q", body.Guardrails.LocalhostIP, "127.0.0.1")
	}
	hasProxyScenario := false
	hasQuotaScenario := false
	for _, supported := range body.Guardrails.SupportedScenarios {
		if supported == "proxy-failure" {
			hasProxyScenario = true
		}
		if supported == "account-quota-exhausted" {
			hasQuotaScenario = true
		}
	}
	if !hasProxyScenario || !hasQuotaScenario {
		t.Fatalf("guardrails.supported_scenarios = %v, want both proxy-failure and account-quota-exhausted", body.Guardrails.SupportedScenarios)
	}

	accountReq := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"scenario":"account-quota-exhausted"}`))
	accountReq.RemoteAddr = "127.0.0.1:9999"
	accountReq.Header.Set("Content-Type", "application/json")
	accountResp := httptest.NewRecorder()
	serverEnabled.engine.ServeHTTP(accountResp, accountReq)
	if accountResp.Code != http.StatusOK {
		t.Fatalf("account scenario status = %d, want %d, body=%s", accountResp.Code, http.StatusOK, accountResp.Body.String())
	}

	var accountBody struct {
		Scenario  string         `json:"scenario"`
		Applied   int            `json:"applied"`
		Remaining int            `json:"remaining"`
		Snapshot  map[string]int `json:"snapshot"`
	}
	if err := json.Unmarshal(accountResp.Body.Bytes(), &accountBody); err != nil {
		t.Fatalf("Unmarshal(account response): %v", err)
	}
	if accountBody.Scenario != "account-quota-exhausted" {
		t.Fatalf("account scenario = %q, want %q", accountBody.Scenario, "account-quota-exhausted")
	}
	if accountBody.Applied != 1 {
		t.Fatalf("account applied = %d, want %d", accountBody.Applied, 1)
	}
	if accountBody.Remaining != 1 {
		t.Fatalf("account remaining = %d, want %d", accountBody.Remaining, 1)
	}
	if accountBody.Snapshot["account-quota-exhausted"] != 1 {
		t.Fatalf("account snapshot[account-quota-exhausted] = %d, want %d", accountBody.Snapshot["account-quota-exhausted"], 1)
	}
}
