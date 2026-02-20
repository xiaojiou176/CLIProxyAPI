package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/interfaces"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
)

func TestBuildErrorResponseBody_StatusMapping(t *testing.T) {
	tests := []struct {
		name     string
		status   int
		message  string
		wantType string
		wantCode string
	}{
		{name: "401 authentication", status: http.StatusUnauthorized, message: "unauthorized", wantType: "authentication_error", wantCode: "invalid_api_key"},
		{name: "403 permission", status: http.StatusForbidden, message: "forbidden", wantType: "permission_error", wantCode: "insufficient_quota"},
		{name: "429 rate limit", status: http.StatusTooManyRequests, message: "too many requests", wantType: "rate_limit_error", wantCode: "rate_limit_exceeded"},
		{name: "404 model not found", status: http.StatusNotFound, message: "missing model", wantType: "invalid_request_error", wantCode: "model_not_found"},
		{name: "500 server error", status: http.StatusInternalServerError, message: "boom", wantType: "server_error", wantCode: "internal_server_error"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := BuildErrorResponseBody(tt.status, tt.message)
			var payload ErrorResponse
			if err := json.Unmarshal(body, &payload); err != nil {
				t.Fatalf("unmarshal error response body: %v, raw=%s", err, string(body))
			}

			if payload.Error.Type != tt.wantType {
				t.Fatalf("type = %q, want %q", payload.Error.Type, tt.wantType)
			}
			if payload.Error.Code != tt.wantCode {
				t.Fatalf("code = %q, want %q", payload.Error.Code, tt.wantCode)
			}
			if payload.Error.Message != tt.message {
				t.Fatalf("message = %q, want %q", payload.Error.Message, tt.message)
			}
		})
	}
}

func TestBuildErrorResponseBody_PreservesJSONPayload(t *testing.T) {
	raw := `{"error":{"message":"upstream raw payload","type":"provider_error","code":"upstream_failed"}}`
	body := BuildErrorResponseBody(http.StatusBadGateway, raw)
	if string(body) != raw {
		t.Fatalf("body = %s, want %s", string(body), raw)
	}
}

func TestWriteErrorResponse_PropagatesStatusHeadersAndBody(t *testing.T) {
	gin.SetMode(gin.TestMode)

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/test", nil)

	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, coreauth.NewManager(nil, nil, nil))
	msg := &interfaces.ErrorMessage{
		StatusCode: http.StatusForbidden,
		Error:      errors.New("model blocked by visibility guard"),
		Addon: http.Header{
			"X-Upstream-Reason": []string{"policy", "guard"},
			"Retry-After":       []string{"120"},
		},
	}

	handler.WriteErrorResponse(ctx, msg)

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusForbidden)
	}
	var payload ErrorResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal response body: %v, raw=%s", err, recorder.Body.String())
	}
	if payload.Error.Type != "permission_error" {
		t.Fatalf("error.type = %q, want %q", payload.Error.Type, "permission_error")
	}
	if payload.Error.Code != "insufficient_quota" {
		t.Fatalf("error.code = %q, want %q", payload.Error.Code, "insufficient_quota")
	}
	if payload.Error.Message != "model blocked by visibility guard" {
		t.Fatalf("error.message = %q, want %q", payload.Error.Message, "model blocked by visibility guard")
	}
}
