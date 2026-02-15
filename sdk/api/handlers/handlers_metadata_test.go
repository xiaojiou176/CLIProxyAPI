package handlers

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	"golang.org/x/net/context"
)

func TestRequestExecutionMetadata_UsesHeaderSessionKey(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	req.Header.Set("x-session-id", "session-header-1")
	req.Header.Set("Idempotency-Key", "idem-1")
	c.Request = req

	ctx := context.WithValue(context.Background(), "gin", c)
	meta := requestExecutionMetadata(ctx, []byte(`{"model":"gpt-5.3-codex"}`))

	if got := meta[idempotencyKeyMetadataKey]; got != "idem-1" {
		t.Fatalf("idempotency metadata = %v, want %q", got, "idem-1")
	}
	if got := meta[coreexecutor.SessionAffinityKeyMetadataKey]; got != "session-header-1" {
		t.Fatalf("session affinity metadata = %v, want %q", got, "session-header-1")
	}
}

func TestRequestExecutionMetadata_UsesPayloadSessionKeyFallback(t *testing.T) {
	meta := requestExecutionMetadata(context.Background(), []byte(`{"model":"gpt-5.3-codex","previous_response_id":"resp-abc"}`))

	if got := meta[coreexecutor.SessionAffinityKeyMetadataKey]; got != "resp-abc" {
		t.Fatalf("session affinity metadata = %v, want %q", got, "resp-abc")
	}
	if got, ok := meta[idempotencyKeyMetadataKey]; !ok || got == "" {
		t.Fatalf("idempotency metadata missing")
	}
}
