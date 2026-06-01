package openai

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/klauspost/compress/zstd"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
	"github.com/tidwall/gjson"
)

// compactStatusErr is a minimal test-only error that satisfies the
// `interface{ StatusCode() int }` contract relied on by BaseAPIHandler when it
// translates executor errors back into HTTP status codes.
type compactStatusErr struct {
	code int
	msg  string
}

func (e compactStatusErr) Error() string  { return e.msg }
func (e compactStatusErr) StatusCode() int { return e.code }

type compactCaptureExecutor struct {
	alt          string
	sourceFormat string
	calls        int
}

func (e *compactCaptureExecutor) Identifier() string { return "test-provider" }

func (e *compactCaptureExecutor) Execute(ctx context.Context, auth *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (coreexecutor.Response, error) {
	e.calls++
	e.alt = opts.Alt
	e.sourceFormat = opts.SourceFormat.String()
	return coreexecutor.Response{Payload: []byte(`{"ok":true}`)}, nil
}

func (e *compactCaptureExecutor) ExecuteStream(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	return nil, errors.New("not implemented")
}

func (e *compactCaptureExecutor) Refresh(ctx context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e *compactCaptureExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (e *compactCaptureExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

func TestOpenAIResponsesCompactRejectsStream(t *testing.T) {
	gin.SetMode(gin.TestMode)
	executor := &compactCaptureExecutor{}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)

	auth := &coreauth.Auth{ID: "auth1", Provider: executor.Identifier(), Status: coreauth.StatusActive}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register auth: %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "test-model"}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewOpenAIResponsesAPIHandler(base)
	router := gin.New()
	router.POST("/v1/responses/compact", h.Compact)

	req := httptest.NewRequest(http.MethodPost, "/v1/responses/compact", strings.NewReader(`{"model":"test-model","stream":true}`))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", resp.Code, http.StatusBadRequest)
	}
	if executor.calls != 0 {
		t.Fatalf("executor calls = %d, want 0", executor.calls)
	}
}

func TestOpenAIResponsesCompactExecute(t *testing.T) {
	gin.SetMode(gin.TestMode)
	executor := &compactCaptureExecutor{}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)

	auth := &coreauth.Auth{ID: "auth2", Provider: executor.Identifier(), Status: coreauth.StatusActive}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register auth: %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "test-model"}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewOpenAIResponsesAPIHandler(base)
	router := gin.New()
	router.POST("/v1/responses/compact", h.Compact)

	req := httptest.NewRequest(http.MethodPost, "/v1/responses/compact", strings.NewReader(`{"model":"test-model","input":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.Code, http.StatusOK)
	}
	if executor.alt != "responses/compact" {
		t.Fatalf("alt = %q, want %q", executor.alt, "responses/compact")
	}
	if executor.sourceFormat != "openai-response" {
		t.Fatalf("source format = %q, want %q", executor.sourceFormat, "openai-response")
	}
	if strings.TrimSpace(resp.Body.String()) != `{"ok":true}` {
		t.Fatalf("body = %s", resp.Body.String())
	}
}

func TestOpenAIResponsesCompactDecodesZstdRequestBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	executor := &compactCaptureExecutor{}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)

	auth := &coreauth.Auth{ID: "auth3", Provider: executor.Identifier(), Status: coreauth.StatusActive}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register auth: %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "test-model"}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewOpenAIResponsesAPIHandler(base)
	router := gin.New()
	router.POST("/v1/responses/compact", h.Compact)

	var compressed bytes.Buffer
	encoder, err := zstd.NewWriter(&compressed)
	if err != nil {
		t.Fatalf("zstd.NewWriter: %v", err)
	}
	if _, errWrite := encoder.Write([]byte(`{"model":"test-model","input":"hello"}`)); errWrite != nil {
		t.Fatalf("zstd write: %v", errWrite)
	}
	if errClose := encoder.Close(); errClose != nil {
		t.Fatalf("zstd close: %v", errClose)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/responses/compact", bytes.NewReader(compressed.Bytes()))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Content-Encoding", "zstd")
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", resp.Code, http.StatusOK, resp.Body.String())
	}
	if executor.calls != 1 {
		t.Fatalf("executor calls = %d, want 1", executor.calls)
	}
	if executor.alt != "responses/compact" {
		t.Fatalf("alt = %q, want %q", executor.alt, "responses/compact")
	}
	if strings.TrimSpace(resp.Body.String()) != `{"ok":true}` {
		t.Fatalf("body = %s", resp.Body.String())
	}
}

// compactFallbackExecutor returns 501 for one configured "original" model and
// a success payload for the configured "fallback" model. It records the
// sequence of (model, alt) pairs it observed so the test can assert that the
// retry actually happened against the rewritten model.
type compactFallbackExecutor struct {
	originalModel string
	fallbackModel string
	failureStatus int    // 501 by default
	failureMsg    string // executor error message
	successBody   []byte // body returned for fallbackModel; defaults to {"ok":true,"from":"fallback"}
	calls         []compactFallbackCall
}

type compactFallbackCall struct {
	model       string
	alt         string
	payloadMod  string // gjson-extracted model field from the raw payload (to assert sjson rewrite happened)
	sourceFmt   string
}

func (e *compactFallbackExecutor) Identifier() string { return "test-provider" }

func (e *compactFallbackExecutor) Execute(ctx context.Context, auth *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (coreexecutor.Response, error) {
	e.calls = append(e.calls, compactFallbackCall{
		model:      req.Model,
		alt:        opts.Alt,
		payloadMod: gjson.GetBytes(req.Payload, "model").String(),
		sourceFmt:  opts.SourceFormat.String(),
	})
	if strings.EqualFold(req.Model, e.originalModel) {
		status := e.failureStatus
		if status == 0 {
			status = http.StatusNotImplemented
		}
		msg := e.failureMsg
		if msg == "" {
			msg = "/responses/compact not supported"
		}
		return coreexecutor.Response{}, compactStatusErr{code: status, msg: msg}
	}
	body := e.successBody
	if len(body) == 0 {
		body = []byte(`{"ok":true,"from":"fallback"}`)
	}
	return coreexecutor.Response{Payload: append([]byte(nil), body...)}, nil
}

func (e *compactFallbackExecutor) ExecuteStream(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	return nil, errors.New("not implemented")
}

func (e *compactFallbackExecutor) Refresh(ctx context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e *compactFallbackExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (e *compactFallbackExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

// registerCompactFallbackHarness wires a single executor that serves both the
// original model (returns 501) and the fallback model (returns success), and
// returns a Compact-routed gin router using the given SDKConfig.
func registerCompactFallbackHarness(t *testing.T, cfg *sdkconfig.SDKConfig, originalModel, fallbackModel string) (*compactFallbackExecutor, *gin.Engine) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	executor := &compactFallbackExecutor{
		originalModel: originalModel,
		fallbackModel: fallbackModel,
	}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)

	auth := &coreauth.Auth{ID: "auth-fallback-" + originalModel, Provider: executor.Identifier(), Status: coreauth.StatusActive}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register auth: %v", err)
	}
	models := []*registry.ModelInfo{{ID: originalModel}}
	if fallbackModel != "" && fallbackModel != originalModel {
		models = append(models, &registry.ModelInfo{ID: fallbackModel})
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, models)
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	base := handlers.NewBaseAPIHandlers(cfg, manager)
	h := NewOpenAIResponsesAPIHandler(base)
	router := gin.New()
	router.POST("/v1/responses/compact", h.Compact)
	return executor, router
}

// TestOpenAIResponsesCompactFallbackSucceeds is the happy path: the executor
// returns 501 for the original model and 200 for the fallback model, so the
// handler should rewrite payload.model and return the fallback's payload
// together with the `x-cpa-compact-fallback` marker header.
func TestOpenAIResponsesCompactFallbackSucceeds(t *testing.T) {
	t.Setenv("CPA_COMPACT_FALLBACK_MODEL", "")
	cfg := &sdkconfig.SDKConfig{CompactFallbackModel: "gpt-fallback"}
	executor, router := registerCompactFallbackHarness(t, cfg, "claude-original", "gpt-fallback")

	req := httptest.NewRequest(http.MethodPost,
		"/v1/responses/compact",
		strings.NewReader(`{"model":"claude-original","input":"long history"}`))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s, want 200", resp.Code, resp.Body.String())
	}
	if got := strings.TrimSpace(resp.Body.String()); got != `{"ok":true,"from":"fallback"}` {
		t.Fatalf("body = %q, want fallback payload", got)
	}
	if got := resp.Header().Get("x-cpa-compact-fallback"); got != "claude-original->gpt-fallback" {
		t.Fatalf("x-cpa-compact-fallback = %q, want %q", got, "claude-original->gpt-fallback")
	}
	if len(executor.calls) != 2 {
		t.Fatalf("executor calls = %d, want 2 (original then fallback): %#v", len(executor.calls), executor.calls)
	}
	if executor.calls[0].model != "claude-original" {
		t.Fatalf("first call model = %q, want claude-original", executor.calls[0].model)
	}
	if executor.calls[0].payloadMod != "claude-original" {
		t.Fatalf("first call payload.model = %q, want claude-original", executor.calls[0].payloadMod)
	}
	if executor.calls[1].model != "gpt-fallback" {
		t.Fatalf("second call model = %q, want gpt-fallback", executor.calls[1].model)
	}
	if executor.calls[1].payloadMod != "gpt-fallback" {
		t.Fatalf("second call payload.model = %q, want gpt-fallback (sjson rewrite must have happened)", executor.calls[1].payloadMod)
	}
	if executor.calls[1].alt != "responses/compact" {
		t.Fatalf("second call alt = %q, want responses/compact", executor.calls[1].alt)
	}
}

// TestOpenAIResponsesCompactFallbackEnvOverride asserts env var precedence:
// the SDK config field is empty but the env var supplies a fallback model.
func TestOpenAIResponsesCompactFallbackEnvOverride(t *testing.T) {
	t.Setenv("CPA_COMPACT_FALLBACK_MODEL", "gpt-fallback")
	cfg := &sdkconfig.SDKConfig{} // intentionally empty
	executor, router := registerCompactFallbackHarness(t, cfg, "claude-original", "gpt-fallback")

	req := httptest.NewRequest(http.MethodPost,
		"/v1/responses/compact",
		strings.NewReader(`{"model":"claude-original","input":"hi"}`))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s, want 200", resp.Code, resp.Body.String())
	}
	if resp.Header().Get("x-cpa-compact-fallback") == "" {
		t.Fatalf("env-var fallback should still stamp the x-cpa-compact-fallback header")
	}
	if len(executor.calls) != 2 {
		t.Fatalf("executor calls = %d, want 2", len(executor.calls))
	}
}

// TestOpenAIResponsesCompactFallbackDisabled asserts the original 501 is
// propagated unchanged when no fallback is configured (no env var, no SDK
// config field) — preserving the pre-fallback behaviour for operators that
// did not opt in.
func TestOpenAIResponsesCompactFallbackDisabled(t *testing.T) {
	t.Setenv("CPA_COMPACT_FALLBACK_MODEL", "")
	cfg := &sdkconfig.SDKConfig{} // CompactFallbackModel unset
	executor, router := registerCompactFallbackHarness(t, cfg, "claude-original", "gpt-fallback")

	req := httptest.NewRequest(http.MethodPost,
		"/v1/responses/compact",
		strings.NewReader(`{"model":"claude-original"}`))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501 (fallback should remain disabled)", resp.Code)
	}
	if resp.Header().Get("x-cpa-compact-fallback") != "" {
		t.Fatalf("x-cpa-compact-fallback should not be set when fallback is disabled")
	}
	if len(executor.calls) != 1 {
		t.Fatalf("executor calls = %d, want exactly 1 (no retry)", len(executor.calls))
	}
}

// TestOpenAIResponsesCompactFallbackEqualsOriginal guards against an
// infinite-loop misconfiguration: when fallback == original we must not retry.
func TestOpenAIResponsesCompactFallbackEqualsOriginal(t *testing.T) {
	t.Setenv("CPA_COMPACT_FALLBACK_MODEL", "")
	cfg := &sdkconfig.SDKConfig{CompactFallbackModel: "claude-original"}
	executor, router := registerCompactFallbackHarness(t, cfg, "claude-original", "claude-original")

	req := httptest.NewRequest(http.MethodPost,
		"/v1/responses/compact",
		strings.NewReader(`{"model":"claude-original"}`))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501", resp.Code)
	}
	if len(executor.calls) != 1 {
		t.Fatalf("executor calls = %d, want 1 (loop guard)", len(executor.calls))
	}
}

// counterFailingExecutor returns 501 on the first Execute call and 503 on every
// subsequent call. Used to verify the fallback-also-fails path.
type counterFailingExecutor struct {
	calls int
}

func (e *counterFailingExecutor) Identifier() string { return "test-provider" }
func (e *counterFailingExecutor) Execute(ctx context.Context, auth *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (coreexecutor.Response, error) {
	e.calls++
	if e.calls == 1 {
		return coreexecutor.Response{}, compactStatusErr{code: http.StatusNotImplemented, msg: "/responses/compact not supported"}
	}
	return coreexecutor.Response{}, compactStatusErr{code: http.StatusServiceUnavailable, msg: "fallback upstream offline"}
}
func (e *counterFailingExecutor) ExecuteStream(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	return nil, errors.New("not implemented")
}
func (e *counterFailingExecutor) Refresh(ctx context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}
func (e *counterFailingExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}
func (e *counterFailingExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

func TestOpenAIResponsesCompactFallbackRetryAlsoFailsCounter(t *testing.T) {
	t.Setenv("CPA_COMPACT_FALLBACK_MODEL", "")
	gin.SetMode(gin.TestMode)
	executor := &counterFailingExecutor{}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	auth := &coreauth.Auth{ID: "auth-double-fail", Provider: executor.Identifier(), Status: coreauth.StatusActive}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register auth: %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider,
		[]*registry.ModelInfo{{ID: "claude-original"}, {ID: "gpt-fallback"}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })

	cfg := &sdkconfig.SDKConfig{CompactFallbackModel: "gpt-fallback"}
	base := handlers.NewBaseAPIHandlers(cfg, manager)
	h := NewOpenAIResponsesAPIHandler(base)
	router := gin.New()
	router.POST("/v1/responses/compact", h.Compact)

	req := httptest.NewRequest(http.MethodPost,
		"/v1/responses/compact",
		strings.NewReader(`{"model":"claude-original"}`))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (fallback error should replace original 501)", resp.Code)
	}
	if executor.calls != 2 {
		t.Fatalf("executor calls = %d, want 2 (original + fallback)", executor.calls)
	}
}

// TestOpenAIResponsesCompactNon501NotRetried makes sure non-501 errors are
// not retried — the fallback is only meant to mask "executor doesn't support
// compact", not arbitrary upstream failures.
func TestOpenAIResponsesCompactNon501NotRetried(t *testing.T) {
	t.Setenv("CPA_COMPACT_FALLBACK_MODEL", "")
	gin.SetMode(gin.TestMode)
	executor := &counterFailingExecutor{calls: 1} // start at 1 so the first real call hits the 503 branch
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	auth := &coreauth.Auth{ID: "auth-non501", Provider: executor.Identifier(), Status: coreauth.StatusActive}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register auth: %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider,
		[]*registry.ModelInfo{{ID: "claude-original"}, {ID: "gpt-fallback"}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })

	cfg := &sdkconfig.SDKConfig{CompactFallbackModel: "gpt-fallback"}
	base := handlers.NewBaseAPIHandlers(cfg, manager)
	h := NewOpenAIResponsesAPIHandler(base)
	router := gin.New()
	router.POST("/v1/responses/compact", h.Compact)

	req := httptest.NewRequest(http.MethodPost,
		"/v1/responses/compact",
		strings.NewReader(`{"model":"claude-original"}`))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (no retry on non-501)", resp.Code)
	}
	// One real call only (we pre-incremented calls to 1, so this call makes calls=2
	// but no fallback retry happens; we assert "calls" stayed at 2, not 3).
	if executor.calls != 2 {
		t.Fatalf("executor calls = %d, want 2 (no retry on non-501)", executor.calls)
	}
}
