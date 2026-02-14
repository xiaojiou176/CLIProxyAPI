package claude

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

func newClaudeStreamHarness(t *testing.T) (*ClaudeCodeAPIHandler, *gin.Context, *httptest.ResponseRecorder, http.Flusher) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	flusher, ok := ctx.Writer.(http.Flusher)
	if !ok {
		t.Fatal("gin writer does not implement http.Flusher")
	}

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, coreauth.NewManager(nil, nil, nil))
	return NewClaudeCodeAPIHandler(base), ctx, rec, flusher
}

func TestClaudeForwardStream_EmitsClaudeErrorEventAfterFirstChunk(t *testing.T) {
	t.Parallel()

	handler, ctx, rec, flusher := newClaudeStreamHarness(t)
	data := make(chan []byte)
	errs := make(chan *interfaces.ErrorMessage)
	cancelErr := make(chan error, 1)

	go func() {
		data <- []byte("event: message\ndata: hello\n\n")
		errs <- &interfaces.ErrorMessage{
			StatusCode: http.StatusForbidden,
			Error:      errors.New("claude blocked"),
		}
	}()

	handler.forwardClaudeStream(ctx, flusher, func(err error) { cancelErr <- err }, data, errs)

	body := rec.Body.String()
	if !strings.Contains(body, "event: message\ndata: hello") {
		t.Fatalf("stream body missing first chunk, body=%s", body)
	}
	if !strings.Contains(body, "event: error\ndata:") {
		t.Fatalf("stream body missing error event, body=%s", body)
	}
	if !strings.Contains(body, `"type":"error"`) {
		t.Fatalf("stream body missing claude error type, body=%s", body)
	}
	if !strings.Contains(body, `"type":"api_error"`) {
		t.Fatalf("stream body missing claude api_error detail, body=%s", body)
	}
	if !strings.Contains(body, `"message":"claude blocked"`) {
		t.Fatalf("stream body missing claude message, body=%s", body)
	}

	if gotCancelErr := <-cancelErr; gotCancelErr == nil || gotCancelErr.Error() != "claude blocked" {
		t.Fatalf("cancel error=%v, want claude blocked", gotCancelErr)
	}
}

func TestClaudeForwardStream_CleanCloseWithoutErrorEvent(t *testing.T) {
	t.Parallel()

	handler, ctx, rec, flusher := newClaudeStreamHarness(t)
	data := make(chan []byte)
	errs := make(chan *interfaces.ErrorMessage)
	cancelErr := make(chan error, 1)

	go func() {
		data <- []byte("event: message\ndata: ok\n\n")
		close(data)
	}()

	handler.forwardClaudeStream(ctx, flusher, func(err error) { cancelErr <- err }, data, errs)

	body := rec.Body.String()
	if !strings.Contains(body, "event: message\ndata: ok") {
		t.Fatalf("stream body missing first chunk, body=%s", body)
	}
	if strings.Contains(body, "event: error") {
		t.Fatalf("stream body unexpectedly contains error event, body=%s", body)
	}
	if gotCancelErr := <-cancelErr; gotCancelErr != nil {
		t.Fatalf("cancel error=%v, want nil", gotCancelErr)
	}
}
