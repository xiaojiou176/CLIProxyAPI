package responses

import (
	"testing"

	"github.com/tidwall/gjson"
)

// Codex sends namespace-typed tools (e.g. mcp__node_repl) whose child
// functions must be flattened into individual chat-completions function tools.
// Regression: previously only top-level type=="function" survived, so every
// namespace child (js/browser/chrome/computer_use, agent tools, etc.) was
// silently dropped on the DeepSeek/openai-compat lane.
func TestConvertOpenAIResponsesRequest_NamespaceToolsFlattened(t *testing.T) {
	raw := []byte(`{
		"model": "deepseek-v4-pro",
		"input": "hi",
		"tools": [
			{"type":"namespace","name":"mcp__node_repl","tools":[
				{"type":"function","name":"js","description":"run js","parameters":{"type":"object","properties":{}}}
			]},
			{"type":"namespace","name":"multi_agent_v1","tools":[
				{"type":"function","name":"spawn_agent","description":"spawn","parameters":{"type":"object","properties":{}}}
			]},
			{"type":"function","name":"exec_command","description":"exec","parameters":{"type":"object","properties":{}}}
		]
	}`)

	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("deepseek-v4-pro", raw, false)

	tools := gjson.GetBytes(out, "tools")
	if !tools.IsArray() {
		t.Fatalf("tools missing/non-array: %s", string(out))
	}
	names := map[string]bool{}
	tools.ForEach(func(_, tool gjson.Result) bool {
		names[tool.Get("function.name").String()] = true
		return true
	})

	for _, want := range []string{"mcp__node_repl__js", "multi_agent_v1__spawn_agent", "exec_command"} {
		if !names[want] {
			t.Fatalf("expected flattened function %q, got %v body=%s", want, names, string(out))
		}
	}
}
