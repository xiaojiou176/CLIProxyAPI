package openai

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
)

func newOpenAIStreamHarness(t *testing.T) (*OpenAIAPIHandler, *gin.Context, *httptest.ResponseRecorder, http.Flusher) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	flusher, ok := ctx.Writer.(http.Flusher)
	if !ok {
		t.Fatal("gin writer does not implement http.Flusher")
	}

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, coreauth.NewManager(nil, nil, nil))
	return NewOpenAIAPIHandler(base), ctx, rec, flusher
}

func TestOpenAIHandleStreamResult_EmitsMappedTerminalErrorAfterFirstChunk(t *testing.T) {
	t.Parallel()

	handler, ctx, rec, flusher := newOpenAIStreamHarness(t)
	data := make(chan []byte)
	errs := make(chan *interfaces.ErrorMessage)
	cancelErr := make(chan error, 1)

	go func() {
		data <- []byte(`{"id":"chunk-1","choices":[{"delta":{"content":"hello"}}]}`)
		errs <- &interfaces.ErrorMessage{
			StatusCode: http.StatusForbidden,
			Error:      errors.New("model denied"),
		}
	}()

	handler.handleStreamResult(ctx, flusher, func(err error) { cancelErr <- err }, data, errs)

	body := rec.Body.String()
	if !strings.Contains(body, `data: {"id":"chunk-1","choices":[{"delta":{"content":"hello"}}]}`) {
		t.Fatalf("stream body missing first chunk, body=%s", body)
	}
	if !strings.Contains(body, `"message":"model denied"`) {
		t.Fatalf("stream body missing mapped message, body=%s", body)
	}
	if !strings.Contains(body, `"type":"permission_error"`) {
		t.Fatalf("stream body missing mapped type, body=%s", body)
	}
	if !strings.Contains(body, `"code":"insufficient_quota"`) {
		t.Fatalf("stream body missing mapped code, body=%s", body)
	}

	gotCancelErr := <-cancelErr
	if gotCancelErr == nil || gotCancelErr.Error() != "model denied" {
		t.Fatalf("cancel error=%v, want model denied", gotCancelErr)
	}
}

func TestOpenAIHandleStreamResult_WritesDoneOnCleanClose(t *testing.T) {
	t.Parallel()

	handler, ctx, rec, flusher := newOpenAIStreamHarness(t)
	data := make(chan []byte)
	errs := make(chan *interfaces.ErrorMessage)
	cancelErr := make(chan error, 1)

	go func() {
		data <- []byte(`{"id":"chunk-1","choices":[{"delta":{"content":"ok"}}]}`)
		close(data)
	}()

	handler.handleStreamResult(ctx, flusher, func(err error) { cancelErr <- err }, data, errs)

	body := rec.Body.String()
	if !strings.Contains(body, `data: {"id":"chunk-1","choices":[{"delta":{"content":"ok"}}]}`) {
		t.Fatalf("stream body missing chunk, body=%s", body)
	}
	if !strings.Contains(body, "data: [DONE]") {
		t.Fatalf("stream body missing [DONE], body=%s", body)
	}

	if gotCancelErr := <-cancelErr; gotCancelErr != nil {
		t.Fatalf("cancel error=%v, want nil", gotCancelErr)
	}
}
