package management

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	internalconfig "github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

func TestRequestCodexToken_WebUIPortInUseReturnsConflict(t *testing.T) {
	gin.SetMode(gin.TestMode)

	occupiedListener, listenErr := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", codexCallbackPort))
	if listenErr != nil && !strings.Contains(strings.ToLower(listenErr.Error()), "address already in use") {
		t.Fatalf("net.Listen(codex callback port): %v", listenErr)
	}
	if occupiedListener != nil {
		defer occupiedListener.Close()
	}

	handler := &Handler{
		cfg: &internalconfig.Config{
			Port:    8317,
			AuthDir: t.TempDir(),
		},
	}

	router := gin.New()
	router.GET("/codex-auth-url", handler.RequestCodexToken)

	req := httptest.NewRequest(http.MethodGet, "/codex-auth-url?is_webui=true", nil)
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d, body=%s", resp.Code, http.StatusConflict, resp.Body.String())
	}

	var body struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
		t.Fatalf("Unmarshal(response): %v", err)
	}
	if !strings.Contains(body.Error, "1455") {
		t.Fatalf("error message = %q, want contains callback port", body.Error)
	}
}
