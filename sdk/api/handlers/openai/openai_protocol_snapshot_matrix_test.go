package openai

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
)

type openAIProtocolMatrixExecutor struct {
	streamErr error
}

func (e *openAIProtocolMatrixExecutor) Identifier() string { return "openai-protocol-matrix" }

func (e *openAIProtocolMatrixExecutor) Execute(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{Payload: []byte(`{"ok":true}`)}, nil
}

func (e *openAIProtocolMatrixExecutor) ExecuteStream(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (<-chan coreexecutor.StreamChunk, error) {
	ch := make(chan coreexecutor.StreamChunk, 1)
	if e.streamErr != nil {
		ch <- coreexecutor.StreamChunk{Err: e.streamErr}
	}
	close(ch)
	return ch, nil
}

func (e *openAIProtocolMatrixExecutor) Refresh(ctx context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e *openAIProtocolMatrixExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (e *openAIProtocolMatrixExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

func setupOpenAIProtocolMatrixRouter(t *testing.T, entry string, streamErr error) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)

	executor := &openAIProtocolMatrixExecutor{streamErr: streamErr}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)

	auth := &coreauth.Auth{
		ID:       "openai-protocol-matrix-" + strings.ReplaceAll(t.Name(), "/", "-"),
		Provider: executor.Identifier(),
		Status:   coreauth.StatusActive,
	}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register auth: %v", err)
	}

	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{
		{
			ID:      "gpt-5.3-codex",
			Object:  "model",
			Created: time.Now().Unix(),
			OwnedBy: "test-suite",
		},
	})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	router := gin.New()
	switch entry {
	case "/v1/chat/completions":
		router.POST(entry, NewOpenAIAPIHandler(base).ChatCompletions)
	case "/v1/responses":
		router.POST(entry, NewOpenAIResponsesAPIHandler(base).Responses)
	default:
		t.Fatalf("unsupported entry %q", entry)
	}

	return router
}

func newOpenAIResponsesStreamHarness(t *testing.T) (*OpenAIResponsesAPIHandler, *gin.Context, *httptest.ResponseRecorder, http.Flusher) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)

	flusher, ok := ctx.Writer.(http.Flusher)
	if !ok {
		t.Fatal("gin writer does not implement http.Flusher")
	}

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, coreauth.NewManager(nil, nil, nil))
	return NewOpenAIResponsesAPIHandler(base), ctx, rec, flusher
}

type openAIErrorEnvelope struct {
	Error struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    string `json:"code"`
	} `json:"error"`
}

func decodeOpenAIErrorEnvelope(t *testing.T, raw string) openAIErrorEnvelope {
	t.Helper()
	var payload openAIErrorEnvelope
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatalf("unmarshal error envelope: %v, raw=%s", err, raw)
	}
	if payload.Error.Type == "" {
		t.Fatalf("error.type is empty, raw=%s", raw)
	}
	return payload
}

func extractOpenAIErrorEnvelopeFromStream(t *testing.T, body string) openAIErrorEnvelope {
	t.Helper()
	lines := strings.Split(body, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "data: ") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(trimmed, "data: "))
		if strings.HasPrefix(payload, "{") && strings.Contains(payload, `"error"`) {
			return decodeOpenAIErrorEnvelope(t, payload)
		}
	}
	t.Fatalf("stream body missing mapped error payload, body=%s", body)
	return openAIErrorEnvelope{}
}

func TestOpenAIProtocolSnapshotMatrix_CursorAndCodexCLI_PreAndPostFirstPacketConsistency(t *testing.T) {
	tests := []struct {
		name             string
		client           string
		entry            string
		requestBody      string
		firstChunk       []byte
		wantStatus       int
		wantType         string
		wantCode         string
		expectEventError bool
	}{
		{
			name:             "cursor_openai_chat_completions",
			client:           "Cursor",
			entry:            "/v1/chat/completions",
			requestBody:      `{"model":"gpt-5.3-codex","messages":[{"role":"user","content":"hello"}],"stream":true}`,
			firstChunk:       []byte(`{"id":"chunk-1","choices":[{"delta":{"content":"hello"}}]}`),
			wantStatus:       http.StatusForbidden,
			wantType:         "permission_error",
			wantCode:         "insufficient_quota",
			expectEventError: false,
		},
		{
			name:             "codex_cli_openai_responses",
			client:           "Codex CLI",
			entry:            "/v1/responses",
			requestBody:      `{"model":"gpt-5.3-codex","input":"hello","stream":true}`,
			firstChunk:       []byte("event: response.output_text.delta\ndata: {\"delta\":\"hello\"}\n\n"),
			wantStatus:       http.StatusTooManyRequests,
			wantType:         "rate_limit_error",
			wantCode:         "rate_limit_exceeded",
			expectEventError: true,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			message := tc.client + " protocol matrix blocked"
			preRouter := setupOpenAIProtocolMatrixRouter(t, tc.entry, &coreauth.Error{
				Message:    message,
				HTTPStatus: tc.wantStatus,
			})

			req := httptest.NewRequest(http.MethodPost, tc.entry, strings.NewReader(tc.requestBody))
			req.Header.Set("Content-Type", "application/json")
			preResp := httptest.NewRecorder()
			preRouter.ServeHTTP(preResp, req)

			if preResp.Code != tc.wantStatus {
				t.Fatalf("[%s] pre-first status = %d, want %d, body=%s", tc.client, preResp.Code, tc.wantStatus, preResp.Body.String())
			}

			prePayload := decodeOpenAIErrorEnvelope(t, preResp.Body.String())
			if prePayload.Error.Type != tc.wantType {
				t.Fatalf("[%s] pre-first error.type = %q, want %q", tc.client, prePayload.Error.Type, tc.wantType)
			}
			if prePayload.Error.Code != tc.wantCode {
				t.Fatalf("[%s] pre-first error.code = %q, want %q", tc.client, prePayload.Error.Code, tc.wantCode)
			}
			if prePayload.Error.Message != message {
				t.Fatalf("[%s] pre-first error.message = %q, want %q", tc.client, prePayload.Error.Message, message)
			}

			data := make(chan []byte)
			errs := make(chan *interfaces.ErrorMessage)
			cancelErr := make(chan error, 1)
			go func() {
				data <- tc.firstChunk
				errs <- &interfaces.ErrorMessage{
					StatusCode: tc.wantStatus,
					Error:      errors.New(message),
				}
			}()

			var postBody string
			switch tc.entry {
			case "/v1/chat/completions":
				handler, ctx, rec, flusher := newOpenAIStreamHarness(t)
				handler.handleStreamResult(ctx, flusher, func(err error) { cancelErr <- err }, data, errs)
				postBody = rec.Body.String()
			case "/v1/responses":
				handler, ctx, rec, flusher := newOpenAIResponsesStreamHarness(t)
				handler.forwardResponsesStream(ctx, flusher, func(err error) { cancelErr <- err }, data, errs)
				postBody = rec.Body.String()
			default:
				t.Fatalf("unsupported entry %q", tc.entry)
			}

			postPayload := extractOpenAIErrorEnvelopeFromStream(t, postBody)
			if postPayload.Error.Type != prePayload.Error.Type {
				t.Fatalf("[%s] pre/post error.type mismatch: pre=%q post=%q", tc.client, prePayload.Error.Type, postPayload.Error.Type)
			}
			if postPayload.Error.Code != prePayload.Error.Code {
				t.Fatalf("[%s] pre/post error.code mismatch: pre=%q post=%q", tc.client, prePayload.Error.Code, postPayload.Error.Code)
			}
			if postPayload.Error.Message != prePayload.Error.Message {
				t.Fatalf("[%s] pre/post error.message mismatch: pre=%q post=%q", tc.client, prePayload.Error.Message, postPayload.Error.Message)
			}

			hasEventError := strings.Contains(postBody, "event: error")
			if hasEventError != tc.expectEventError {
				t.Fatalf("[%s] event:error present=%v, want=%v, body=%s", tc.client, hasEventError, tc.expectEventError, postBody)
			}

			if gotCancelErr := <-cancelErr; gotCancelErr == nil || gotCancelErr.Error() != message {
				t.Fatalf("[%s] cancel error=%v, want %q", tc.client, gotCancelErr, message)
			}
		})
	}
}
