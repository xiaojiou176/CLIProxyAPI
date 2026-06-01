package responses

import (
	"context"
	"testing"
)

// When DeepSeek/openai-compat returns a flattened MCP tool name
// ("mcp__SERVER__TOOL"), the response translation must split it into leaf
// name + namespace so the Codex tool registry can dispatch the call.
// Regression: previously the flat name was emitted verbatim, so the host
// router rejected the call with "unsupported call".
func TestConvertOpenAIChatCompletionsResponseToOpenAIResponses_McpToolNameSplit(t *testing.T) {
	in := []string{
		`data: {"id":"r","object":"chat.completion.chunk","created":1,"model":"deepseek-v4-pro","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_x","type":"function","function":{"name":"mcp__node_repl__js","arguments":""}}]},"finish_reason":null}]}`,
		`data: {"id":"r","object":"chat.completion.chunk","created":1,"model":"deepseek-v4-pro","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"code\":\"1\"}"}}]},"finish_reason":null}]}`,
		`data: {"id":"r","object":"chat.completion.chunk","created":1,"model":"deepseek-v4-pro","choices":[{"index":0,"delta":{"tool_calls":null},"finish_reason":"tool_calls"}],"usage":{"completion_tokens":1,"total_tokens":2,"prompt_tokens":1}}`,
		`data: [DONE]`,
	}
	request := []byte(`{"model":"deepseek-v4-pro","tool_choice":"auto"}`)

	var param any
	var out [][]byte
	for _, line := range in {
		out = append(out, ConvertOpenAIChatCompletionsResponseToOpenAIResponses(context.Background(), "deepseek-v4-pro", request, request, []byte(line), &param)...)
	}

	checked := false
	for _, chunk := range out {
		ev, data := parseOpenAIResponsesSSEEvent(t, chunk)
		if (ev == "response.output_item.added" || ev == "response.output_item.done") &&
			data.Get("item.type").String() == "function_call" {
			checked = true
			if n := data.Get("item.name").String(); n != "js" {
				t.Fatalf("%s: expected item.name=js, got %q (item=%s)", ev, n, data.Get("item").Raw)
			}
			if ns := data.Get("item.namespace").String(); ns != "mcp__node_repl" {
				t.Fatalf("%s: expected item.namespace=mcp__node_repl, got %q (item=%s)", ev, ns, data.Get("item").Raw)
			}
		}
		if ev == "response.completed" {
			for _, item := range data.Get("response.output").Array() {
				if item.Get("type").String() == "function_call" {
					if n := item.Get("name").String(); n != "js" {
						t.Fatalf("completed: expected name=js, got %q", n)
					}
					if ns := item.Get("namespace").String(); ns != "mcp__node_repl" {
						t.Fatalf("completed: expected namespace=mcp__node_repl, got %q", ns)
					}
				}
			}
		}
	}
	if !checked {
		t.Fatalf("no function_call item events seen; out=%v", out)
	}
}
