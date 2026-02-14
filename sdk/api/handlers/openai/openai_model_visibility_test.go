package openai

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

type visibilityCaptureExecutor struct {
	calls int
}

func (e *visibilityCaptureExecutor) Identifier() string { return "test-provider-visibility" }

func (e *visibilityCaptureExecutor) Execute(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	e.calls++
	return coreexecutor.Response{Payload: []byte(`{"ok":true}`)}, nil
}

func (e *visibilityCaptureExecutor) ExecuteStream(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (<-chan coreexecutor.StreamChunk, error) {
	return nil, errors.New("not implemented")
}

func (e *visibilityCaptureExecutor) Refresh(ctx context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e *visibilityCaptureExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (e *visibilityCaptureExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

type openAIModelsResponse struct {
	Object string `json:"object"`
	Data   []struct {
		ID string `json:"id"`
	} `json:"data"`
}

func setupOpenAIVisibilityHarness(t *testing.T, cfg *sdkconfig.SDKConfig, modelIDs []string) (*gin.Engine, *visibilityCaptureExecutor) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	executor := &visibilityCaptureExecutor{}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)

	auth := &coreauth.Auth{
		ID:       "visibility-auth-" + strings.ReplaceAll(t.Name(), "/", "-"),
		Provider: executor.Identifier(),
		Status:   coreauth.StatusActive,
	}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register auth: %v", err)
	}

	infos := make([]*registry.ModelInfo, 0, len(modelIDs))
	now := time.Now().Unix()
	for idx, modelID := range modelIDs {
		infos = append(infos, &registry.ModelInfo{
			ID:      modelID,
			Object:  "model",
			Created: now + int64(idx),
			OwnedBy: "test-suite",
		})
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, infos)
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	base := handlers.NewBaseAPIHandlers(cfg, manager)
	h := NewOpenAIAPIHandler(base)

	router := gin.New()
	router.GET("/v1/models", h.OpenAIModels)
	router.POST("/v1/chat/completions", h.ChatCompletions)
	return router, executor
}

func setupOpenAIResponsesVisibilityHarness(t *testing.T, cfg *sdkconfig.SDKConfig, modelIDs []string) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)

	manager := coreauth.NewManager(nil, nil, nil)
	executor := &visibilityCaptureExecutor{}
	manager.RegisterExecutor(executor)

	auth := &coreauth.Auth{
		ID:       "visibility-openai-responses-auth-" + strings.ReplaceAll(t.Name(), "/", "-"),
		Provider: executor.Identifier(),
		Status:   coreauth.StatusActive,
	}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register auth: %v", err)
	}

	infos := make([]*registry.ModelInfo, 0, len(modelIDs))
	now := time.Now().Unix()
	for idx, modelID := range modelIDs {
		infos = append(infos, &registry.ModelInfo{
			ID:      modelID,
			Object:  "model",
			Created: now + int64(idx),
			OwnedBy: "test-suite",
		})
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, infos)
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	base := handlers.NewBaseAPIHandlers(cfg, manager)
	h := NewOpenAIResponsesAPIHandler(base)

	router := gin.New()
	router.GET("/v1/models", h.OpenAIResponsesModels)
	return router
}

func readModelIDs(t *testing.T, body []byte) []string {
	t.Helper()
	var payload openAIModelsResponse
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

func TestOpenAIModels_FiltersByModelVisibility(t *testing.T) {
	cfg := &sdkconfig.SDKConfig{
		ModelVisibility: internalconfig.ModelVisibilityConfig{
			Enabled: true,
			Namespaces: map[string][]string{
				"default": {"gpt-5.3-codex"},
			},
		},
	}
	router, _ := setupOpenAIVisibilityHarness(t, cfg, []string{"gpt-5.3-codex", "gemini-3.0-pro"})

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", resp.Code, http.StatusOK, resp.Body.String())
	}

	ids := readModelIDs(t, resp.Body.Bytes())
	if len(ids) != 1 || ids[0] != "gpt-5.3-codex" {
		t.Fatalf("visible model ids = %v, want [gpt-5.3-codex]", ids)
	}
}

func TestOpenAIChatCompletions_RejectsUnauthorizedModelByVisibility(t *testing.T) {
	cfg := &sdkconfig.SDKConfig{
		ModelVisibility: internalconfig.ModelVisibilityConfig{
			Enabled: true,
			Namespaces: map[string][]string{
				"default": {"gpt-5.3-codex"},
			},
		},
	}
	router, executor := setupOpenAIVisibilityHarness(t, cfg, []string{"gpt-5.3-codex", "gemini-3.0-pro"})

	req := httptest.NewRequest(
		http.MethodPost,
		"/v1/chat/completions",
		strings.NewReader(`{"model":"gemini-3.0-pro","messages":[{"role":"user","content":"hello"}]}`),
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

func TestOpenAIModelVisibility_UnconfiguredPreservesBehavior(t *testing.T) {
	router, executor := setupOpenAIVisibilityHarness(t, &sdkconfig.SDKConfig{}, []string{"gpt-5.3-codex", "gemini-3.0-pro"})

	modelReq := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	modelResp := httptest.NewRecorder()
	router.ServeHTTP(modelResp, modelReq)

	if modelResp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", modelResp.Code, http.StatusOK)
	}
	ids := readModelIDs(t, modelResp.Body.Bytes())
	want := []string{"gemini-3.0-pro", "gpt-5.3-codex"}
	if len(ids) != len(want) || ids[0] != want[0] || ids[1] != want[1] {
		t.Fatalf("model ids = %v, want %v", ids, want)
	}

	chatReq := httptest.NewRequest(
		http.MethodPost,
		"/v1/chat/completions",
		strings.NewReader(`{"model":"gemini-3.0-pro","messages":[{"role":"user","content":"hello"}]}`),
	)
	chatReq.Header.Set("Content-Type", "application/json")
	chatResp := httptest.NewRecorder()
	router.ServeHTTP(chatResp, chatReq)

	if chatResp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", chatResp.Code, http.StatusOK, chatResp.Body.String())
	}
	if executor.calls != 1 {
		t.Fatalf("executor calls = %d, want 1", executor.calls)
	}
}

func TestOpenAIModels_UsesNamespaceHeader(t *testing.T) {
	cfg := &sdkconfig.SDKConfig{
		ModelVisibility: internalconfig.ModelVisibilityConfig{
			Enabled: true,
			Namespaces: map[string][]string{
				"default": {"gpt-5.3-codex"},
				"team-b":  {"gemini-3.0-pro"},
			},
		},
	}
	router, _ := setupOpenAIVisibilityHarness(t, cfg, []string{"gpt-5.3-codex", "gemini-3.0-pro"})

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("X-Model-Namespace", "team-b")
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", resp.Code, http.StatusOK, resp.Body.String())
	}

	ids := readModelIDs(t, resp.Body.Bytes())
	if len(ids) != 1 || ids[0] != "gemini-3.0-pro" {
		t.Fatalf("visible model ids = %v, want [gemini-3.0-pro]", ids)
	}
}

func TestOpenAIModels_UsesHostNamespaceMapping(t *testing.T) {
	cfg := &sdkconfig.SDKConfig{
		ModelVisibility: internalconfig.ModelVisibilityConfig{
			Enabled: true,
			Namespaces: map[string][]string{
				"default":     {"gpt-5.3-codex"},
				"antigravity": {"gemini-3.0-pro"},
			},
			HostNamespaces: map[string]string{
				"codex.local":       "default",
				"antigravity.local": "antigravity",
			},
		},
	}
	router, _ := setupOpenAIVisibilityHarness(t, cfg, []string{"gpt-5.3-codex", "gemini-3.0-pro"})

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Host = "antigravity.local:2456"
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", resp.Code, http.StatusOK, resp.Body.String())
	}

	ids := readModelIDs(t, resp.Body.Bytes())
	if len(ids) != 1 || ids[0] != "gemini-3.0-pro" {
		t.Fatalf("visible model ids = %v, want [gemini-3.0-pro]", ids)
	}
}

func TestOpenAIResponsesModels_FiltersByModelVisibility(t *testing.T) {
	cfg := &sdkconfig.SDKConfig{
		ModelVisibility: internalconfig.ModelVisibilityConfig{
			Enabled: true,
			Namespaces: map[string][]string{
				"default": {"gpt-5.3-codex"},
			},
		},
	}
	router := setupOpenAIResponsesVisibilityHarness(t, cfg, []string{"gpt-5.3-codex", "gemini-3.0-pro"})

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", resp.Code, http.StatusOK, resp.Body.String())
	}

	ids := readModelIDs(t, resp.Body.Bytes())
	if len(ids) != 1 || ids[0] != "gpt-5.3-codex" {
		t.Fatalf("visible model ids = %v, want [gpt-5.3-codex]", ids)
	}
}
