package responses

import (
	"testing"

	"github.com/tidwall/gjson"
)

// Codex hosts may send namespace-typed tools (e.g. codex_app/multi_agent_v1)
// whose child functions must be flattened into Gemini functionDeclarations.
// Regression: previously only top-level type=="function" survived, so every
// namespace child (js/browser/chrome/computer_use, thread mgmt, etc.) was
// silently dropped on the Gemini lane.
func TestConvertOpenAIResponsesRequestToGemini_NamespaceToolsFlattened(t *testing.T) {
	raw := []byte(`{
		"model": "gemini-3.1-pro-preview",
		"input": "hi",
		"tools": [
			{"type":"namespace","name":"codex_app","tools":[
				{"type":"function","name":"read_thread","description":"read","parameters":{"type":"object","properties":{}}},
				{"type":"function","name":"automation_update","description":"upd","parameters":{"type":"object","properties":{}}}
			]},
			{"type":"function","name":"exec_command","description":"exec","parameters":{"type":"object","properties":{}}}
		]
	}`)

	out := ConvertOpenAIResponsesRequestToGemini("gemini-3.1-pro-preview", raw, false)

	decls := gjson.GetBytes(out, "tools.0.functionDeclarations")
	if !decls.IsArray() {
		t.Fatalf("functionDeclarations missing/non-array: %s", string(out))
	}
	names := map[string]bool{}
	decls.ForEach(func(_, d gjson.Result) bool {
		names[d.Get("name").String()] = true
		return true
	})

	for _, want := range []string{"codex_app__read_thread", "codex_app__automation_update", "exec_command"} {
		if !names[want] {
			t.Fatalf("expected functionDeclaration %q, got %v body=%s", want, names, string(out))
		}
	}
}
