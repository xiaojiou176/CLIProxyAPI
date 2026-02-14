package claude

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

type claudeErrorPathExecutor struct {
	executeErr error
	streamErr  error
}

func (e *claudeErrorPathExecutor) Identifier() string { return "claude" }

func (e *claudeErrorPathExecutor) Execute(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	if e.executeErr != nil {
		return coreexecutor.Response{}, e.executeErr
	}
	return coreexecutor.Response{Payload: []byte(`{"ok":true}`)}, nil
}

func (e *claudeErrorPathExecutor) ExecuteStream(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (<-chan coreexecutor.StreamChunk, error) {
	ch := make(chan coreexecutor.StreamChunk, 1)
	if e.streamErr != nil {
		ch <- coreexecutor.StreamChunk{Err: e.streamErr}
	}
	close(ch)
	return ch, nil
}

func (e *claudeErrorPathExecutor) Refresh(ctx context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e *claudeErrorPathExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (e *claudeErrorPathExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

func setupClaudeErrorPathHarness(t *testing.T, executor *claudeErrorPathExecutor) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)

	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)

	auth := &coreauth.Auth{
		ID:       "claude-error-path-" + strings.ReplaceAll(t.Name(), "/", "-"),
		Provider: executor.Identifier(),
		Status:   coreauth.StatusActive,
	}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register auth: %v", err)
	}

	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{
		{
			ID:      "claude-sonnet-4-5",
			Object:  "model",
			Created: time.Now().Unix(),
			OwnedBy: "test-suite",
		},
	})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewClaudeCodeAPIHandler(base)

	router := gin.New()
	router.POST("/v1/messages", h.ClaudeMessages)
	return router
}

func TestClaudeMessages_StreamPreHeaderError_UsesMappedJSONResponse(t *testing.T) {
	executor := &claudeErrorPathExecutor{
		streamErr: &coreauth.Error{
			Message:    "claude stream throttled",
			HTTPStatus: http.StatusTooManyRequests,
		},
	}
	router := setupClaudeErrorPathHarness(t, executor)

	req := httptest.NewRequest(
		http.MethodPost,
		"/v1/messages",
		strings.NewReader(`{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"hello"}],"stream":true}`),
	)
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want %d, body=%s", resp.Code, http.StatusTooManyRequests, resp.Body.String())
	}
	body := resp.Body.String()
	if !strings.Contains(body, `"type":"rate_limit_error"`) {
		t.Fatalf("body missing mapped rate_limit_error, body=%s", body)
	}
	if !strings.Contains(body, `"code":"rate_limit_exceeded"`) {
		t.Fatalf("body missing mapped rate_limit_exceeded, body=%s", body)
	}
	if !strings.Contains(body, `"message":"claude stream throttled"`) {
		t.Fatalf("body missing mapped message, body=%s", body)
	}
	if strings.Contains(body, "event: error") {
		t.Fatalf("pre-header path should be JSON response, not SSE error event, body=%s", body)
	}
}

func TestClaudeMessages_NonStreamingError_UsesMappedJSONResponse(t *testing.T) {
	executor := &claudeErrorPathExecutor{
		executeErr: &coreauth.Error{
			Message:    "claude blocked",
			HTTPStatus: http.StatusForbidden,
		},
	}
	router := setupClaudeErrorPathHarness(t, executor)

	req := httptest.NewRequest(
		http.MethodPost,
		"/v1/messages",
		strings.NewReader(`{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"hello"}],"stream":false}`),
	)
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d, body=%s", resp.Code, http.StatusForbidden, resp.Body.String())
	}
	body := resp.Body.String()
	if !strings.Contains(body, `"type":"permission_error"`) {
		t.Fatalf("body missing mapped permission_error, body=%s", body)
	}
	if !strings.Contains(body, `"code":"insufficient_quota"`) {
		t.Fatalf("body missing mapped insufficient_quota, body=%s", body)
	}
	if !strings.Contains(body, `"message":"claude blocked"`) {
		t.Fatalf("body missing mapped message, body=%s", body)
	}
}

