package gemini

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

type geminiVisibilityCaptureExecutor struct {
	calls int
}

func (e *geminiVisibilityCaptureExecutor) Identifier() string {
	return "test-provider-gemini-visibility"
}

func (e *geminiVisibilityCaptureExecutor) Execute(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	e.calls++
	return coreexecutor.Response{Payload: []byte(`{"ok":true}`)}, nil
}

func (e *geminiVisibilityCaptureExecutor) ExecuteStream(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	return nil, errors.New("not implemented")
}

func (e *geminiVisibilityCaptureExecutor) Refresh(ctx context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e *geminiVisibilityCaptureExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (e *geminiVisibilityCaptureExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

type geminiModelsResponse struct {
	Models []struct {
		Name string `json:"name"`
	} `json:"models"`
}

func setupGeminiVisibilityHarness(t *testing.T, cfg *sdkconfig.SDKConfig, modelIDs []string) (*gin.Engine, *geminiVisibilityCaptureExecutor) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	executor := &geminiVisibilityCaptureExecutor{}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)

	auth := &coreauth.Auth{
		ID:       "visibility-gemini-auth-" + strings.ReplaceAll(t.Name(), "/", "-"),
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
	h := NewGeminiAPIHandler(base)

	router := gin.New()
	router.GET("/v1beta/models", h.GeminiModels)
	router.GET("/v1beta/models/*action", h.GeminiGetHandler)
	router.POST("/v1beta/models/*action", h.GeminiHandler)
	return router, executor
}

func readGeminiModelNames(t *testing.T, body []byte) []string {
	t.Helper()

	var payload geminiModelsResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("unmarshal /v1beta/models response: %v", err)
	}
	names := make([]string, 0, len(payload.Models))
	for _, item := range payload.Models {
		names = append(names, item.Name)
	}
	sort.Strings(names)
	return names
}

func TestGeminiModels_FiltersByModelVisibility(t *testing.T) {
	cfg := &sdkconfig.SDKConfig{
		ModelVisibility: internalconfig.ModelVisibilityConfig{
			Enabled: true,
			Namespaces: map[string][]string{
				"default": {"gemini-3.0-pro"},
			},
		},
	}
	router, _ := setupGeminiVisibilityHarness(t, cfg, []string{"gpt-5.3-codex", "gemini-3.0-pro"})

	req := httptest.NewRequest(http.MethodGet, "/v1beta/models", nil)
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", resp.Code, http.StatusOK, resp.Body.String())
	}

	names := readGeminiModelNames(t, resp.Body.Bytes())
	if len(names) != 2 || names[0] != "models/gemini-3.0-pro" || names[1] != "models/gpt-5.3-codex" {
		t.Fatalf("visible model names = %v, want [models/gemini-3.0-pro models/gpt-5.3-codex]", names)
	}
}

func TestGeminiGetModel_HidesUnauthorizedModelByVisibility(t *testing.T) {
	cfg := &sdkconfig.SDKConfig{
		ModelVisibility: internalconfig.ModelVisibilityConfig{
			Enabled: true,
			Namespaces: map[string][]string{
				"default": {"gpt-5.3-codex"},
			},
		},
	}
	router, _ := setupGeminiVisibilityHarness(t, cfg, []string{"gpt-5.3-codex", "gemini-3.0-pro"})

	req := httptest.NewRequest(http.MethodGet, "/v1beta/models/gemini-3.0-pro", nil)
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", resp.Code, http.StatusOK, resp.Body.String())
	}
}

func TestGeminiGenerateContent_RejectsUnauthorizedModelByVisibility(t *testing.T) {
	cfg := &sdkconfig.SDKConfig{
		ModelVisibility: internalconfig.ModelVisibilityConfig{
			Enabled: true,
			Namespaces: map[string][]string{
				"default": {"gpt-5.3-codex"},
			},
		},
	}
	router, executor := setupGeminiVisibilityHarness(t, cfg, []string{"gpt-5.3-codex", "gemini-3.0-pro"})

	req := httptest.NewRequest(
		http.MethodPost,
		"/v1beta/models/gemini-3.0-pro:generateContent",
		strings.NewReader(`{"contents":[{"parts":[{"text":"hello"}]}]}`),
	)
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", resp.Code, http.StatusOK, resp.Body.String())
	}
	if executor.calls != 1 {
		t.Fatalf("executor calls = %d, want 1", executor.calls)
	}
}
