package responses

import (
	"context"
	"testing"
)

// When Gemini returns a flattened MCP tool name ("mcp__SERVER__TOOL"), the
// translation must split it into leaf name + namespace so the Codex tool
// registry can dispatch the call. Regression: previously the flat name was
// emitted verbatim, so the host router rejected it with "unsupported call".
func TestConvertGeminiResponseToOpenAIResponses_McpToolNameSplit(t *testing.T) {
	in := []string{
		`data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"mcp__node_repl__js","args":{"code":"1"}}}]}}],"modelVersion":"test-model","responseId":"req_vrtx_1"},"traceId":"t1"}`,
		`data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"text":""}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5,"totalTokenCount":15},"modelVersion":"test-model","responseId":"req_vrtx_1"},"traceId":"t1"}`,
	}

	var param any
	var out [][]byte
	for _, line := range in {
		out = append(out, ConvertGeminiResponseToOpenAIResponses(context.Background(), "test-model", nil, nil, []byte(line), &param)...)
	}

	foundAdded := false
	for _, chunk := range out {
		ev, data := parseSSEEvent(t, chunk)
		if ev == "response.output_item.added" && data.Get("item.type").String() == "function_call" {
			foundAdded = true
			if got := data.Get("item.name").String(); got != "js" {
				t.Fatalf("expected item.name=js, got %q (full=%s)", got, data.Get("item").Raw)
			}
			if got := data.Get("item.namespace").String(); got != "mcp__node_repl" {
				t.Fatalf("expected item.namespace=mcp__node_repl, got %q (full=%s)", got, data.Get("item").Raw)
			}
		}
		if ev == "response.completed" {
			if got := data.Get("response.output.0.name").String(); got != "js" {
				t.Fatalf("expected completed output[0].name=js, got %q", got)
			}
			if got := data.Get("response.output.0.namespace").String(); got != "mcp__node_repl" {
				t.Fatalf("expected completed output[0].namespace=mcp__node_repl, got %q", got)
			}
		}
	}
	if !foundAdded {
		t.Fatalf("no function_call output_item.added event found; out=%v", out)
	}
}
