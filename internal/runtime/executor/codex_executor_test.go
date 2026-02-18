package executor

import (
	"errors"
	"testing"
	"time"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	"github.com/tidwall/gjson"
)

func TestCodexTerminalEventType(t *testing.T) {
	t.Run("detects terminal from data payload", func(t *testing.T) {
		line := []byte(`data: {"type":"response.completed","response":{"id":"r_1"}}`)
		eventType, terminal := codexTerminalEventType(line)
		if eventType != "response.completed" || !terminal {
			t.Fatalf("unexpected eventType=%q terminal=%v", eventType, terminal)
		}
	})

	t.Run("detects terminal from event header only", func(t *testing.T) {
		line := []byte(`event: response.done`)
		eventType, terminal := codexTerminalEventType(line)
		if eventType != "response.done" || !terminal {
			t.Fatalf("unexpected eventType=%q terminal=%v", eventType, terminal)
		}
	})

	t.Run("reports non-terminal event type", func(t *testing.T) {
		line := []byte(`event: response.output_text.delta`)
		eventType, terminal := codexTerminalEventType(line)
		if eventType != "response.output_text.delta" || terminal {
			t.Fatalf("unexpected eventType=%q terminal=%v", eventType, terminal)
		}
	})
}

func TestCodexDisconnectedStreamErr(t *testing.T) {
	err := codexDisconnectedStreamErr(codexStreamDisconnectInfo{
		cause:         "scanner_error",
		lastEventType: "response.output_text.delta",
		chunksSeen:    12,
		sawTerminal:   false,
		sawOutputText: true,
		scannerErr:    errors.New("read: connection reset by peer"),
	})
	if err.StatusCode() != 408 {
		t.Fatalf("unexpected status code: got %d, want 408", err.StatusCode())
	}
	msg := err.Error()
	if got := gjson.Get(msg, "type").String(); got != "stream.disconnected" {
		t.Fatalf("unexpected type: %q", got)
	}
	if got := gjson.Get(msg, "error.cause").String(); got != "scanner_error" {
		t.Fatalf("unexpected cause: %q", got)
	}
	if got := gjson.Get(msg, "error.last_event_type").String(); got != "response.output_text.delta" {
		t.Fatalf("unexpected last_event_type: %q", got)
	}
	if got := gjson.Get(msg, "error.chunks_seen").Int(); got != 12 {
		t.Fatalf("unexpected chunks_seen: %d", got)
	}
}

func TestCodexTerminalPayload(t *testing.T) {
	t.Run("extracts response completed payload", func(t *testing.T) {
		line := []byte(`data: {"type":"response.completed","response":{"id":"r_123"}}`)
		data, ok := codexTerminalPayload(line)
		if !ok {
			t.Fatalf("expected response.completed payload")
		}
		if string(data) != `{"type":"response.completed","response":{"id":"r_123"}}` {
			t.Fatalf("unexpected payload: %s", string(data))
		}
	})

	t.Run("extracts response done payload", func(t *testing.T) {
		line := []byte(`data: {"type":"response.done","response":{"id":"r_999"}}`)
		data, ok := codexTerminalPayload(line)
		if !ok {
			t.Fatalf("expected response.done payload")
		}
		if string(data) != `{"type":"response.done","response":{"id":"r_999"}}` {
			t.Fatalf("unexpected payload: %s", string(data))
		}
	})

	t.Run("ignores done marker", func(t *testing.T) {
		line := []byte(`data: [DONE]`)
		if _, ok := codexTerminalPayload(line); ok {
			t.Fatalf("expected done marker to be ignored")
		}
	})

	t.Run("ignores non completed events", func(t *testing.T) {
		line := []byte(`data: {"type":"response.output_text.delta","delta":"hi"}`)
		if _, ok := codexTerminalPayload(line); ok {
			t.Fatalf("expected non completed event to be ignored")
		}
	})

	t.Run("ignores non data lines", func(t *testing.T) {
		line := []byte(`event: ping`)
		if _, ok := codexTerminalPayload(line); ok {
			t.Fatalf("expected non data line to be ignored")
		}
	})
}

func TestCodexStreamFailure(t *testing.T) {
	t.Run("parses response.failed into structured status error", func(t *testing.T) {
		line := []byte(`data: {"type":"response.failed","response":{"error":{"code":"rate_limit_exceeded","message":"Rate limit reached. Please try again in 11.054s.","resets_in_seconds":12}}}`)
		streamErr, ok := codexStreamFailure(line)
		if !ok {
			t.Fatalf("expected response.failed to be parsed as stream failure")
		}
		if streamErr.StatusCode() != 429 {
			t.Fatalf("unexpected status code: got %d, want 429", streamErr.StatusCode())
		}
		if streamErr.retryAfter == nil {
			t.Fatalf("expected retryAfter for rate_limit_exceeded")
		}
		if *streamErr.retryAfter < 11*time.Second || *streamErr.retryAfter > 13*time.Second {
			t.Fatalf("unexpected retryAfter: %v", *streamErr.retryAfter)
		}
		msg := streamErr.Error()
		if got := gjson.Get(msg, "type").String(); got != "response.failed" {
			t.Fatalf("unexpected message type: got %q", got)
		}
		if got := gjson.Get(msg, "error.code").String(); got != "rate_limit_exceeded" {
			t.Fatalf("unexpected error code: got %q", got)
		}
		if got := gjson.Get(msg, "error.message").String(); got == "" {
			t.Fatalf("expected structured error.message")
		}
	})

	t.Run("parses response.incomplete with reason", func(t *testing.T) {
		line := []byte(`data: {"type":"response.incomplete","response":{"incomplete_details":{"reason":"max_output_tokens"}}}`)
		streamErr, ok := codexStreamFailure(line)
		if !ok {
			t.Fatalf("expected response.incomplete to be parsed as stream failure")
		}
		if streamErr.StatusCode() != 408 {
			t.Fatalf("unexpected status code: got %d, want 408", streamErr.StatusCode())
		}
		msg := streamErr.Error()
		if got := gjson.Get(msg, "type").String(); got != "response.incomplete" {
			t.Fatalf("unexpected message type: got %q", got)
		}
		if got := gjson.Get(msg, "error.code").String(); got != "response_incomplete" {
			t.Fatalf("unexpected error code: got %q", got)
		}
		if got := gjson.Get(msg, "error.reason").String(); got != "max_output_tokens" {
			t.Fatalf("unexpected error reason: got %q", got)
		}
	})

	t.Run("ignores non failure event types", func(t *testing.T) {
		line := []byte(`data: {"type":"response.output_text.delta","delta":"hello"}`)
		if _, ok := codexStreamFailure(line); ok {
			t.Fatalf("expected non failure event to be ignored")
		}
	})
}

func TestResolveCodexClientVersionPrefersAuthAttribute(t *testing.T) {
	t.Setenv("CODEX_CLI_VERSION", "0.999.0")
	auth := &cliproxyauth.Auth{
		Attributes: map[string]string{
			"codex_client_version": "0.103.0",
		},
	}

	if got := resolveCodexClientVersion(auth); got != "0.103.0" {
		t.Fatalf("resolveCodexClientVersion(auth)=%q, want %q", got, "0.103.0")
	}
}

func TestResolveCodexClientVersionUsesEnvFallback(t *testing.T) {
	t.Setenv("CODEX_CLI_VERSION", "0.104.1")
	if got := resolveCodexClientVersion(nil); got != "0.104.1" {
		t.Fatalf("resolveCodexClientVersion(nil)=%q, want %q", got, "0.104.1")
	}
}

func TestResolveCodexUserAgentFallback(t *testing.T) {
	t.Setenv("CODEX_CLI_VERSION", "0.200.0")
	if got := resolveCodexUserAgent(nil); got != "codex_cli_rs/0.200.0" {
		t.Fatalf("resolveCodexUserAgent(nil)=%q, want %q", got, "codex_cli_rs/0.200.0")
	}
}
