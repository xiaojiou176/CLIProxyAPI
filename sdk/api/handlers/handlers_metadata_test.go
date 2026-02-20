package handlers

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	"golang.org/x/net/context"
)

func TestRequestExecutionMetadata_UsesIdempotencyHeader(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	req.Header.Set("Idempotency-Key", "idem-1")
	c.Request = req

	ctx := context.WithValue(context.Background(), "gin", c)
	meta := requestExecutionMetadata(ctx)

	if got := meta[idempotencyKeyMetadataKey]; got != "idem-1" {
		t.Fatalf("idempotency metadata = %v, want %q", got, "idem-1")
	}
}

func TestRequestExecutionMetadata_UsesGeneratedIdempotencyFallback(t *testing.T) {
	meta := requestExecutionMetadata(context.Background())
	if got, ok := meta[idempotencyKeyMetadataKey]; !ok || got == "" {
		t.Fatalf("idempotency metadata missing")
	}
}

func TestRequestExecutionMetadata_IncludesExecutionSessionID(t *testing.T) {
	ctx := WithExecutionSessionID(context.Background(), "session-abc")
	meta := requestExecutionMetadata(ctx)

	if got := meta[coreexecutor.ExecutionSessionMetadataKey]; got != "session-abc" {
		t.Fatalf("execution session metadata = %v, want %q", got, "session-abc")
	}
}
