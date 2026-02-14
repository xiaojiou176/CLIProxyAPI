package claude

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	internalconfig "github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
)

type claudeVisibilityCaptureExecutor struct {
	calls int
}

func (e *claudeVisibilityCaptureExecutor) Identifier() string {
	return "test-provider-claude-visibility"
}

func (e *claudeVisibilityCaptureExecutor) Execute(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	e.calls++
	return coreexecutor.Response{Payload: []byte(`{"ok":true}`)}, nil
}

func (e *claudeVisibilityCaptureExecutor) ExecuteStream(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (<-chan coreexecutor.StreamChunk, error) {
	return nil, errors.New("not implemented")
}

func (e *claudeVisibilityCaptureExecutor) Refresh(ctx context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e *claudeVisibilityCaptureExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (e *claudeVisibilityCaptureExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

type claudeModelsResponse struct {
	Data []struct {
		ID string `json:"id"`
	} `json:"data"`
}

func setupClaudeVisibilityHarness(t *testing.T, cfg *sdkconfig.SDKConfig, modelIDs []string) (*gin.Engine, *claudeVisibilityCaptureExecutor) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	executor := &claudeVisibilityCaptureExecutor{}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)

	auth := &coreauth.Auth{
		ID:       "visibility-claude-auth-" + strings.ReplaceAll(t.Name(), "/", "-"),
		Provider: executor.Identifier(),
		Status:   coreauth.StatusActive,
	}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register auth: %v", err)
	}

	now := time.Now().Unix()
	modelInfos := make([]*registry.ModelInfo, 0, len(modelIDs))
	for idx, modelID := range modelIDs {
		modelInfos = append(modelInfos, &registry.ModelInfo{
			ID:      modelID,
			Object:  "model",
			Created: now + int64(idx),
			OwnedBy: "test-suite",
		})
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, modelInfos)
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	base := handlers.NewBaseAPIHandlers(cfg, manager)
	h := NewClaudeCodeAPIHandler(base)

	router := gin.New()
	router.GET("/v1/models", h.ClaudeModels)
	router.POST("/v1/messages", h.ClaudeMessages)
	return router, executor
}

func readClaudeModelIDs(t *testing.T, body []byte) []string {
	t.Helper()

	var payload claudeModelsResponse
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

func TestClaudeModels_FiltersByModelVisibility(t *testing.T) {
	cfg := &sdkconfig.SDKConfig{
		ModelVisibility: internalconfig.ModelVisibilityConfig{
			Enabled: true,
			Namespaces: map[string][]string{
				"default": {"gpt-5.3-codex"},
			},
		},
	}
	router, _ := setupClaudeVisibilityHarness(t, cfg, []string{"gpt-5.3-codex", "gemini-3.0-pro"})

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", resp.Code, http.StatusOK, resp.Body.String())
	}

	ids := readClaudeModelIDs(t, resp.Body.Bytes())
	if len(ids) != 1 || ids[0] != "gpt-5.3-codex" {
		t.Fatalf("visible model ids = %v, want [gpt-5.3-codex]", ids)
	}
}

func TestClaudeMessages_RejectsUnauthorizedModelByVisibility(t *testing.T) {
	cfg := &sdkconfig.SDKConfig{
		ModelVisibility: internalconfig.ModelVisibilityConfig{
			Enabled: true,
			Namespaces: map[string][]string{
				"default": {"gpt-5.3-codex"},
			},
		},
	}
	router, executor := setupClaudeVisibilityHarness(t, cfg, []string{"gpt-5.3-codex", "gemini-3.0-pro"})

	req := httptest.NewRequest(
		http.MethodPost,
		"/v1/messages",
		strings.NewReader(`{"model":"gemini-3.0-pro","messages":[{"role":"user","content":"hello"}],"stream":false}`),
	)
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d, body=%s", resp.Code, http.StatusForbidden, resp.Body.String())
	}
	if executor.calls != 0 {
		t.Fatalf("executor calls = %d, want 0", executor.calls)
	}
}
