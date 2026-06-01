package responses

import (
	"encoding/base64"
	sigcompat "github.com/router-for-me/CLIProxyAPI/v7/internal/signature"
	"github.com/tidwall/gjson"
	"google.golang.org/protobuf/encoding/protowire"
	"testing"
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

func TestConvertOpenAIResponsesRequestToClaude_ReasoningItemToThinkingBlock(t *testing.T) {
	rawSignature, expectedSignature := testClaudeResponsesThinkingSignature(t)
	raw := []byte(`{
		"model":"claude-test",
		"input":[
			{
				"type":"reasoning",
				"encrypted_content":"` + rawSignature + `",
				"summary":[{"type":"summary_text","text":"internal reasoning"}]
			},
			{
				"type":"message",
				"role":"assistant",
				"content":[{"type":"output_text","text":"visible answer"}]
			},
			{
				"type":"message",
				"role":"user",
				"content":[{"type":"input_text","text":"continue"}]
			}
		]
	}`)

	out := ConvertOpenAIResponsesRequestToClaude("claude-test", raw, false)
	root := gjson.ParseBytes(out)

	assistant := root.Get("messages.0")
	if got := assistant.Get("role").String(); got != "assistant" {
		t.Fatalf("first message role = %q, want assistant. Output: %s", got, string(out))
	}
	if got := assistant.Get("content.0.type").String(); got != "thinking" {
		t.Fatalf("first content type = %q, want thinking. Output: %s", got, string(out))
	}
	if got := assistant.Get("content.0.signature").String(); got != expectedSignature {
		t.Fatalf("thinking signature = %q, want %q", got, expectedSignature)
	}
	if got := assistant.Get("content.0.thinking").String(); got != "internal reasoning" {
		t.Fatalf("thinking text = %q, want internal reasoning", got)
	}
	if got := assistant.Get("content.1.type").String(); got != "text" {
		t.Fatalf("second content type = %q, want text. Output: %s", got, string(out))
	}
	if got := assistant.Get("content.1.text").String(); got != "visible answer" {
		t.Fatalf("assistant text = %q, want visible answer", got)
	}
	if got := root.Get("messages.1.role").String(); got != "user" {
		t.Fatalf("second message role = %q, want user. Output: %s", got, string(out))
	}
}

func TestConvertOpenAIResponsesRequestToClaude_SignatureOnlyReasoningFlushesBeforeUser(t *testing.T) {
	rawSignature, expectedSignature := testClaudeResponsesThinkingSignature(t)
	raw := []byte(`{
		"model":"claude-test",
		"input":[
			{
				"type":"reasoning",
				"encrypted_content":"` + rawSignature + `",
				"summary":[]
			},
			{
				"type":"message",
				"role":"user",
				"content":[{"type":"input_text","text":"continue"}]
			}
		]
	}`)

	out := ConvertOpenAIResponsesRequestToClaude("claude-test", raw, false)
	root := gjson.ParseBytes(out)

	thinking := root.Get("messages.0.content.0")
	if got := thinking.Get("type").String(); got != "thinking" {
		t.Fatalf("first content type = %q, want thinking. Output: %s", got, string(out))
	}
	if got := thinking.Get("signature").String(); got != expectedSignature {
		t.Fatalf("thinking signature = %q, want %q", got, expectedSignature)
	}
	if got := thinking.Get("thinking").String(); got != "" {
		t.Fatalf("thinking text = %q, want empty", got)
	}
	if got := root.Get("messages.1.role").String(); got != "user" {
		t.Fatalf("second message role = %q, want user. Output: %s", got, string(out))
	}
}

func TestConvertOpenAIResponsesRequestToClaude_DropsIncompatibleReasoningSignature(t *testing.T) {
	raw := []byte(`{
		"model":"claude-test",
		"input":[
			{
				"type":"reasoning",
				"encrypted_content":"` + testGPTResponsesReasoningSignature() + `",
				"summary":[{"type":"summary_text","text":"must not become Claude thinking"}]
			},
			{
				"type":"message",
				"role":"user",
				"content":[{"type":"input_text","text":"continue"}]
			}
		]
	}`)

	out := ConvertOpenAIResponsesRequestToClaude("claude-test", raw, false)

	if gjson.GetBytes(out, "messages.0.content.0.type").String() == "thinking" {
		t.Fatalf("GPT encrypted_content should not become Claude thinking. Output: %s", string(out))
	}
	if gjson.GetBytes(out, "messages.0.content.0.signature").Exists() {
		t.Fatalf("incompatible signature should not be forwarded. Output: %s", string(out))
	}
	if got := gjson.GetBytes(out, "messages.0.role").String(); got != "user" {
		t.Fatalf("first message role = %q, want user. Output: %s", got, string(out))
	}
}

func testClaudeResponsesThinkingSignature(t *testing.T) (string, string) {
	t.Helper()
	channelBlock := []byte{}
	channelBlock = protowire.AppendTag(channelBlock, 1, protowire.VarintType)
	channelBlock = protowire.AppendVarint(channelBlock, 12)
	channelBlock = protowire.AppendTag(channelBlock, 2, protowire.VarintType)
	channelBlock = protowire.AppendVarint(channelBlock, 2)
	channelBlock = protowire.AppendTag(channelBlock, 6, protowire.BytesType)
	channelBlock = protowire.AppendString(channelBlock, "claude-sonnet-4-6")

	container := []byte{}
	container = protowire.AppendTag(container, 1, protowire.BytesType)
	container = protowire.AppendBytes(container, channelBlock)

	payload := []byte{}
	payload = protowire.AppendTag(payload, 2, protowire.BytesType)
	payload = protowire.AppendBytes(payload, container)
	payload = protowire.AppendTag(payload, 3, protowire.VarintType)
	payload = protowire.AppendVarint(payload, 1)

	rawSignature := base64.StdEncoding.EncodeToString(payload)
	normalized, ok := sigcompat.CompatibleSignatureForProvider(sigcompat.SignatureProviderClaude, rawSignature)
	if !ok {
		t.Fatal("test Claude signature should be compatible")
	}
	return rawSignature, normalized
}

func testGPTResponsesReasoningSignature() string {
	payload := make([]byte, 1+8+16+16+32)
	payload[0] = 0x80
	payload[8] = 1
	for i := 9; i < len(payload); i++ {
		payload[i] = byte(i)
	}
	return base64.URLEncoding.EncodeToString(payload)
}

// Merge regression: reasoning + parallel tool_use must keep reasoning attached
// to the assistant message and not leak/duplicate across the parallel calls.
func TestConvertOpenAIResponsesRequestToClaude_ReasoningWithParallelToolUse(t *testing.T) {
	rawSignature, _ := testClaudeResponsesThinkingSignature(t)
	raw := []byte(`{
		"model":"claude-test",
		"input":[
			{"type":"reasoning","encrypted_content":"` + rawSignature + `","summary":[{"type":"summary_text","text":"plan"}]},
			{"type":"function_call","call_id":"c1","name":"exec_command","arguments":"{}"},
			{"type":"function_call","call_id":"c2","name":"read_file","arguments":"{}"}
		]
	}`)
	out := ConvertOpenAIResponsesRequestToClaude("claude-test", raw, false)
	// thinking blocks across whole output must be exactly 1 (no leak/dup)
	n := 0
	gjson.GetBytes(out, "messages").ForEach(func(_, msg gjson.Result) bool {
		msg.Get("content").ForEach(func(_, c gjson.Result) bool {
			if c.Get("type").String() == "thinking" {
				n++
			}
			return true
		})
		return true
	})
	if n != 1 {
		t.Fatalf("expected exactly 1 thinking block (reasoning not lost/duplicated), got %d; out=%s", n, string(out))
	}
	// both tool_use must survive
	tu := 0
	gjson.GetBytes(out, "messages").ForEach(func(_, msg gjson.Result) bool {
		msg.Get("content").ForEach(func(_, c gjson.Result) bool {
			if c.Get("type").String() == "tool_use" {
				tu++
			}
			return true
		})
		return true
	})
	if tu != 2 {
		t.Fatalf("expected 2 tool_use (parallel merge), got %d; out=%s", tu, string(out))
	}
}
