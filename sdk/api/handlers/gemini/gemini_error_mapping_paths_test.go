package gemini

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
)

type geminiErrorPathExecutor struct {
	executeErr error
	streamErr  error
	countErr   error
}

func (e *geminiErrorPathExecutor) Identifier() string { return "gemini" }

func (e *geminiErrorPathExecutor) Execute(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	if e.executeErr != nil {
		return coreexecutor.Response{}, e.executeErr
	}
	return coreexecutor.Response{Payload: []byte(`{"ok":true}`)}, nil
}

func (e *geminiErrorPathExecutor) ExecuteStream(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	ch := make(chan coreexecutor.StreamChunk, 1)
	if e.streamErr != nil {
		ch <- coreexecutor.StreamChunk{Err: e.streamErr}
	}
	close(ch)
	return &coreexecutor.StreamResult{Chunks: ch}, nil
}

func (e *geminiErrorPathExecutor) Refresh(ctx context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e *geminiErrorPathExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	if e.countErr != nil {
		return coreexecutor.Response{}, e.countErr
	}
	return coreexecutor.Response{Payload: []byte(`{"totalTokens":1}`)}, nil
}

func (e *geminiErrorPathExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

func setupGeminiErrorPathHarness(t *testing.T, executor *geminiErrorPathExecutor) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)

	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)

	auth := &coreauth.Auth{
		ID:       "gemini-error-path-" + strings.ReplaceAll(t.Name(), "/", "-"),
		Provider: executor.Identifier(),
		Status:   coreauth.StatusActive,
	}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register auth: %v", err)
	}

	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{
		{
			ID:      "gemini-3.0-pro",
			Object:  "model",
			Created: time.Now().Unix(),
			OwnedBy: "test-suite",
		},
	})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewGeminiAPIHandler(base)

	router := gin.New()
	router.POST("/v1beta/models/*action", h.GeminiHandler)
	return router
}

func TestGeminiHandler_StreamPreHeaderError_UsesMappedJSONResponse(t *testing.T) {
	executor := &geminiErrorPathExecutor{
		streamErr: &coreauth.Error{
			Message:    "gemini unauthorized",
			HTTPStatus: http.StatusUnauthorized,
		},
	}
	router := setupGeminiErrorPathHarness(t, executor)

	req := httptest.NewRequest(
		http.MethodPost,
		"/v1beta/models/gemini-3.0-pro:streamGenerateContent",
		strings.NewReader(`{"contents":[{"parts":[{"text":"hello"}]}]}`),
	)
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d, body=%s", resp.Code, http.StatusUnauthorized, resp.Body.String())
	}
	body := resp.Body.String()
	if !strings.Contains(body, `"type":"authentication_error"`) {
		t.Fatalf("body missing authentication_error, body=%s", body)
	}
	if !strings.Contains(body, `"code":"invalid_api_key"`) {
		t.Fatalf("body missing invalid_api_key, body=%s", body)
	}
	if !strings.Contains(body, `"message":"gemini unauthorized"`) {
		t.Fatalf("body missing mapped message, body=%s", body)
	}
	if strings.Contains(body, "event: error") {
		t.Fatalf("pre-header path should be JSON response, not SSE error event, body=%s", body)
	}
}

func TestGeminiHandler_CountTokensError_UsesMappedJSONResponse(t *testing.T) {
	executor := &geminiErrorPathExecutor{
		countErr: &coreauth.Error{
			Message:    "gemini quota exhausted",
			HTTPStatus: http.StatusForbidden,
		},
	}
	router := setupGeminiErrorPathHarness(t, executor)

	req := httptest.NewRequest(
		http.MethodPost,
		"/v1beta/models/gemini-3.0-pro:countTokens",
		strings.NewReader(`{"contents":[{"parts":[{"text":"count me"}]}]}`),
	)
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d, body=%s", resp.Code, http.StatusForbidden, resp.Body.String())
	}
	body := resp.Body.String()
	if !strings.Contains(body, `"type":"permission_error"`) {
		t.Fatalf("body missing permission_error, body=%s", body)
	}
	if !strings.Contains(body, `"code":"insufficient_quota"`) {
		t.Fatalf("body missing insufficient_quota, body=%s", body)
	}
	if !strings.Contains(body, `"message":"gemini quota exhausted"`) {
		t.Fatalf("body missing mapped message, body=%s", body)
	}
}
