package gemini

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

func newGeminiStreamHarness(t *testing.T, rawURL string) (*GeminiAPIHandler, *gin.Context, *httptest.ResponseRecorder, http.Flusher) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPost, rawURL, nil)

	flusher, ok := ctx.Writer.(http.Flusher)
	if !ok {
		t.Fatal("gin writer does not implement http.Flusher")
	}

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, coreauth.NewManager(nil, nil, nil))
	return NewGeminiAPIHandler(base), ctx, rec, flusher
}

func TestGeminiForwardStream_SSEModeEmitsMappedErrorEvent(t *testing.T) {
	t.Parallel()

	handler, ctx, rec, flusher := newGeminiStreamHarness(t, "/v1beta/models/gemini-3.0-pro:streamGenerateContent")
	data := make(chan []byte)
	errs := make(chan *interfaces.ErrorMessage)
	cancelErr := make(chan error, 1)

	go func() {
		data <- []byte(`{"candidates":[{"content":{"parts":[{"text":"hello"}]}}]}`)
		errs <- &interfaces.ErrorMessage{
			StatusCode: http.StatusForbidden,
			Error:      errors.New("gemini blocked"),
		}
	}()

	handler.forwardGeminiStream(ctx, flusher, "", func(err error) { cancelErr <- err }, data, errs)

	body := rec.Body.String()
	if !strings.Contains(body, `data: {"candidates":[{"content":{"parts":[{"text":"hello"}]}}]}`) {
		t.Fatalf("stream body missing first chunk, body=%s", body)
	}
	if !strings.Contains(body, "event: error\ndata:") {
		t.Fatalf("stream body missing error event, body=%s", body)
	}
	if !strings.Contains(body, `"message":"gemini blocked"`) {
		t.Fatalf("stream body missing mapped message, body=%s", body)
	}
	if !strings.Contains(body, `"type":"permission_error"`) {
		t.Fatalf("stream body missing mapped type, body=%s", body)
	}
	if !strings.Contains(body, `"code":"insufficient_quota"`) {
		t.Fatalf("stream body missing mapped code, body=%s", body)
	}
	if gotCancelErr := <-cancelErr; gotCancelErr == nil || gotCancelErr.Error() != "gemini blocked" {
		t.Fatalf("cancel error=%v, want gemini blocked", gotCancelErr)
	}
}

func TestGeminiForwardStream_AltModeWritesRawErrorBody(t *testing.T) {
	t.Parallel()

	handler, ctx, rec, flusher := newGeminiStreamHarness(t, "/v1beta/models/gemini-3.0-pro:streamGenerateContent?alt=json")
	data := make(chan []byte)
	errs := make(chan *interfaces.ErrorMessage)
	cancelErr := make(chan error, 1)

	go func() {
		data <- []byte(`{"candidates":[{"content":{"parts":[{"text":"ok"}]}}]}`)
		errs <- &interfaces.ErrorMessage{
			StatusCode: http.StatusTooManyRequests,
			Error:      errors.New("rate exceeded"),
		}
	}()

	handler.forwardGeminiStream(ctx, flusher, "json", func(err error) { cancelErr <- err }, data, errs)

	body := rec.Body.String()
	if strings.Contains(body, "event: error") {
		t.Fatalf("alt=json should not emit SSE error event, body=%s", body)
	}
	if strings.Contains(body, "data: ") {
		t.Fatalf("alt=json should not wrap payload with SSE data prefix, body=%s", body)
	}
	if !strings.Contains(body, `"message":"rate exceeded"`) {
		t.Fatalf("alt=json body missing mapped message, body=%s", body)
	}
	if !strings.Contains(body, `"type":"rate_limit_error"`) {
		t.Fatalf("alt=json body missing mapped type, body=%s", body)
	}
	if !strings.Contains(body, `"code":"rate_limit_exceeded"`) {
		t.Fatalf("alt=json body missing mapped code, body=%s", body)
	}
	if gotCancelErr := <-cancelErr; gotCancelErr == nil || gotCancelErr.Error() != "rate exceeded" {
		t.Fatalf("cancel error=%v, want rate exceeded", gotCancelErr)
	}
}
