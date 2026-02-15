package auth

import (
	"fmt"
	"testing"
	"time"
)

func TestParseRetryAfterHintFromMessage_ParsesResetsInSeconds(t *testing.T) {
	msg := `{"error":{"type":"usage_limit_reached","message":"The usage limit has been reached","resets_in_seconds":7200}}`

	got := parseRetryAfterHintFromMessage(msg)
	if got == nil {
		t.Fatalf("parseRetryAfterHintFromMessage() = nil, want non-nil")
	}
	if *got < 7190*time.Second || *got > 7210*time.Second {
		t.Fatalf("parseRetryAfterHintFromMessage() = %v, want around 2h", *got)
	}
}

func TestParseRetryAfterHintFromMessage_ParsesResetsAt(t *testing.T) {
	target := time.Now().Add(3 * time.Hour).Unix()
	msg := fmt.Sprintf(`{"error":{"type":"usage_limit_reached","resets_at":%d}}`, target)

	got := parseRetryAfterHintFromMessage(msg)
	if got == nil {
		t.Fatalf("parseRetryAfterHintFromMessage() = nil, want non-nil")
	}
	if *got < 2*time.Hour+55*time.Minute || *got > 3*time.Hour+5*time.Minute {
		t.Fatalf("parseRetryAfterHintFromMessage() = %v, want around 3h", *got)
	}
}

func TestClassifyResultError_UsesStructuredRetryAfterHint(t *testing.T) {
	msg := `{"error":{"type":"usage_limit_reached","message":"The usage limit has been reached","resets_in_seconds":18000}}`
	err := &Error{HTTPStatus: 429, Message: msg}

	kind, _, retryAfter, fatal := classifyResultError(err, nil)
	if kind != ErrorKindQuotaLimited5h {
		t.Fatalf("classifyResultError().kind = %q, want %q", kind, ErrorKindQuotaLimited5h)
	}
	if retryAfter == nil {
		t.Fatalf("classifyResultError().retryAfter = nil, want non-nil")
	}
	if *retryAfter < 17990*time.Second || *retryAfter > 18010*time.Second {
		t.Fatalf("classifyResultError().retryAfter = %v, want around 5h", *retryAfter)
	}
	if fatal {
		t.Fatalf("classifyResultError().fatal = true, want false")
	}
}

func TestClassifyResultError_TokenInvalidatedIsFatal(t *testing.T) {
	msg := `{"error":{"message":"Your authentication token has been invalidated. Please try signing in again.","code":"token_invalidated"}}`
	err := &Error{HTTPStatus: 401, Message: msg}

	kind, reason, retryAfter, fatal := classifyResultError(err, nil)
	if kind != ErrorKindAccountDeactivated {
		t.Fatalf("classifyResultError().kind = %q, want %q", kind, ErrorKindAccountDeactivated)
	}
	if reason == "" {
		t.Fatalf("classifyResultError().reason is empty, want non-empty")
	}
	if retryAfter != nil {
		t.Fatalf("classifyResultError().retryAfter = %v, want nil", *retryAfter)
	}
	if !fatal {
		t.Fatalf("classifyResultError().fatal = false, want true")
	}
}
