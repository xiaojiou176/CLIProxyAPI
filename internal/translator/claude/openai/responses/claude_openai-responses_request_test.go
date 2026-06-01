package responses

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertOpenAIResponsesRequestToClaude_KeepsBuiltinWebSearchForKiro(t *testing.T) {
	input := []byte(`{
		"model":"kiro-api/claude-opus-4-7-thinking",
		"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"search"}]}],
		"tools":[
			{"type":"web_search"},
			{"type":"namespace","name":"mcp__WebSearch__","tools":[
				{"type":"function","name":"web_search","description":"DuckDuckGo search","parameters":{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}}
			]}
		]
	}`)

	out := ConvertOpenAIResponsesRequestToClaude("kiro-api/claude-opus-4-7-thinking", input, false)

	tools := gjson.GetBytes(out, "tools")
	if !tools.Exists() || !tools.IsArray() {
		t.Fatalf("expected Claude tools array, got: %s", string(out))
	}

	names := make([]string, 0, len(tools.Array()))
	typedBuiltins := 0
	tools.ForEach(func(_, tool gjson.Result) bool {
		if tool.Get("type").String() == "web_search_20250305" {
			typedBuiltins++
		}
		names = append(names, tool.Get("name").String())
		return true
	})

	// kiro-rs now natively handles web_search via Amazon Q /mcp, so CPA no longer
	// drops the builtin tool for kiro-api/ models; both tools must survive.
	if typedBuiltins != 1 {
		t.Fatalf("expected builtin Claude web_search tool to be kept for kiro-api, got %d in %v", typedBuiltins, names)
	}
	hasMcp := false
	for _, n := range names {
		if n == "mcp__WebSearch__web_search" {
			hasMcp = true
		}
	}
	if !hasMcp {
		t.Fatalf("expected MCP web_search tool to remain, got %v", names)
	}
}

func TestConvertOpenAIResponsesRequestToClaude_PreservesBuiltinWebSearchForNonKiroClaude(t *testing.T) {
	input := []byte(`{
		"model":"claude-opus-4-7",
		"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"search"}]}],
		"tools":[{"type":"web_search"}]
	}`)

	out := ConvertOpenAIResponsesRequestToClaude("claude-opus-4-7", input, false)

	gotType := gjson.GetBytes(out, "tools.0.type").String()
	gotName := gjson.GetBytes(out, "tools.0.name").String()
	if gotType != "web_search_20250305" || gotName != "web_search" {
		t.Fatalf("expected builtin Claude web search tool, got type=%q name=%q body=%s", gotType, gotName, string(out))
	}
}

func TestConvertOpenAIResponsesRequestToClaude_FunctionCallOutputImage(t *testing.T) {
	input := []byte(`{
		"model":"claude-opus-4-7",
		"input":[{"type":"function_call_output","call_id":"call_1","output":[
			{"type":"output_text","text":"here is the image"},
			{"type":"input_image","image_url":"data:image/png;base64,iVBORw0KGgo="}
		]}]
	}`)

	out := ConvertOpenAIResponsesRequestToClaude("claude-opus-4-7", input, false)

	content := gjson.GetBytes(out, "messages.0.content.0.content")
	if !content.IsArray() {
		t.Fatalf("expected tool_result.content to be an array, got: %s", string(out))
	}
	parts := content.Array()
	if len(parts) != 2 {
		t.Fatalf("expected 2 content parts, got %d: %s", len(parts), string(out))
	}
	if parts[0].Get("type").String() != "text" || parts[0].Get("text").String() != "here is the image" {
		t.Fatalf("expected first part text block, got: %s", parts[0].Raw)
	}
	if parts[1].Get("type").String() != "image" {
		t.Fatalf("expected second part image block, got: %s", parts[1].Raw)
	}
	if parts[1].Get("source.type").String() != "base64" ||
		parts[1].Get("source.media_type").String() != "image/png" ||
		parts[1].Get("source.data").String() != "iVBORw0KGgo=" {
		t.Fatalf("expected base64 image source, got: %s", parts[1].Raw)
	}
}

func TestConvertOpenAIResponsesRequestToClaude_FunctionCallOutputPlainText(t *testing.T) {
	input := []byte(`{
		"model":"claude-opus-4-7",
		"input":[{"type":"function_call_output","call_id":"call_2","output":"plain result"}]
	}`)

	out := ConvertOpenAIResponsesRequestToClaude("claude-opus-4-7", input, false)

	content := gjson.GetBytes(out, "messages.0.content.0.content")
	if content.IsArray() {
		t.Fatalf("expected tool_result.content to stay a string, got array: %s", string(out))
	}
	if content.String() != "plain result" {
		t.Fatalf("expected content %q, got %q: %s", "plain result", content.String(), string(out))
	}
}

func TestConvertOpenAIResponsesRequestToClaude_FunctionCallNamespaceRejoin(t *testing.T) {
	input := []byte(`{
		"model":"kiro-api/claude-opus-4-7-thinking",
		"input":[
			{"type":"function_call","call_id":"call_js","name":"js","namespace":"mcp__node_repl","arguments":"{}"},
			{"type":"function_call","call_id":"call_spawn","name":"spawn_agent","namespace":"multi_agent_v1","arguments":"{}"},
			{"type":"function_call","call_id":"call_exec","name":"exec_command","arguments":"{}"}
		],
		"tools":[]
	}`)

	out := ConvertOpenAIResponsesRequestToClaude("kiro-api/claude-opus-4-7-thinking", input, false)

	names := make(map[string]bool)
	gjson.GetBytes(out, "messages").ForEach(func(_, msg gjson.Result) bool {
		msg.Get("content").ForEach(func(_, blk gjson.Result) bool {
			if blk.Get("type").String() == "tool_use" {
				names[blk.Get("name").String()] = true
			}
			return true
		})
		return true
	})

	if !names["mcp__node_repl__js"] {
		t.Errorf("expected rejoined MCP tool name mcp__node_repl__js, got %v; out=%s", names, string(out))
	}
	if !names["multi_agent_v1__spawn_agent"] {
		t.Errorf("expected rejoined codex-internal name multi_agent_v1__spawn_agent, got %v; out=%s", names, string(out))
	}
	if !names["exec_command"] {
		t.Errorf("expected exec_command to pass through unchanged, got %v; out=%s", names, string(out))
	}
}
