package gemini

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
)

func TestGeminiCLIHandler_DirectBranchTransportErrorMappedToInvalidRequest(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := &sdkconfig.SDKConfig{
		ProxyURL: "http://127.0.0.1:1",
	}
	base := handlers.NewBaseAPIHandlers(cfg, coreauth.NewManager(nil, nil, nil))
	h := NewGeminiCLIAPIHandler(base)

	router := gin.New()
	router.POST("/v1internal:method", h.CLIHandler)

	req := httptest.NewRequest(
		http.MethodPost,
		"/v1internal:directCloudCodeCall",
		strings.NewReader(`{"model":"gemini-3.0-pro","contents":[{"parts":[{"text":"hello"}]}]}`),
	)
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "127.0.0.1:24567"

	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d, body=%s", resp.Code, http.StatusBadRequest, resp.Body.String())
	}
	body := resp.Body.String()
	if !strings.Contains(body, `"type":"invalid_request_error"`) {
		t.Fatalf("body missing invalid_request_error type, body=%s", body)
	}
	if !strings.Contains(body, `"message":"Invalid request:`) {
		t.Fatalf("body missing mapped invalid request message, body=%s", body)
	}
}

