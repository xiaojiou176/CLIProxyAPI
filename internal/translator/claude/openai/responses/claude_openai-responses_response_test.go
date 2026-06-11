package responses

import (
	"context"
	"strings"
	"testing"

	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestConvertClaudeResponseToOpenAIResponsesNonStream_SplitsFlatMcpToolName(t *testing.T) {
	raw := []byte(strings.Join([]string{
		`data: {"type":"message_start","message":{"id":"msg_test"}}`,
		`data: {"type":"content_block_start","index":0,"content_block":{"id":"toolu_123","type":"tool_use","name":"mcp__MemPalace__mempalace_status"}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"query\":\"ok\"}"}}`,
		`data: {"type":"content_block_stop","index":0}`,
		`data: {"type":"message_stop"}`,
	}, "\n"))

	var state any
	out := ConvertClaudeResponseToOpenAIResponsesNonStream(context.Background(), "claude-opus-4-7", nil, nil, raw, &state)

	name := gjson.GetBytes(out, "output.0.name").String()
	namespace := gjson.GetBytes(out, "output.0.namespace").String()
	callID := gjson.GetBytes(out, "output.0.call_id").String()
	if name != "mempalace_status" {
		t.Fatalf("expected split leaf tool name, got %q body=%s", name, string(out))
	}
	if namespace != "mcp__MemPalace" {
		t.Fatalf("expected split namespace, got %q body=%s", namespace, string(out))
	}
	if callID != "toolu_123" {
		t.Fatalf("expected call_id to survive, got %q body=%s", callID, string(out))
	}
}


func TestSplitMcpFlatName_StripsMcpTrailingUnderscores(t *testing.T) {
	ns, leaf := splitMcpFlatName("mcp__MemPalace__mempalace_status")
	if ns != "mcp__MemPalace" || leaf != "mempalace_status" {
		t.Fatalf("mcp split mismatch: ns=%q leaf=%q", ns, leaf)
	}
}

func TestSplitMcpFlatName_StripsTrailingUnderscoresForCodexInternalNamespaces(t *testing.T) {
	cases := map[string][2]string{
		"multi_agent_v1__spawn_agent":  {"multi_agent_v1", "spawn_agent"},
		"multi_agent_v1__wait_agent":   {"multi_agent_v1", "wait_agent"},
		"multi_agent_v2__followup_task": {"multi_agent_v2", "followup_task"},
		"tool_search__tool_search_tool": {"tool_search", "tool_search_tool"},
		"codex_app__automation_update":  {"codex_app", "automation_update"},
	}
	for full, want := range cases {
		ns, leaf := splitMcpFlatName(full)
		if ns != want[0] || leaf != want[1] {
			t.Fatalf("split mismatch for %q: got (%q,%q) want (%q,%q)", full, ns, leaf, want[0], want[1])
		}
	}
}

func TestSplitMcpFlatName_PassThroughForUnknownNames(t *testing.T) {
	ns, leaf := splitMcpFlatName("exec_command")
	if ns != "" || leaf != "exec_command" {
		t.Fatalf("plain pass-through failed: ns=%q leaf=%q", ns, leaf)
	}
	ns, leaf = splitMcpFlatName("not_a_codex_namespace__hello")
	if ns != "" || leaf != "not_a_codex_namespace__hello" {
		t.Fatalf("unknown namespace should pass through: ns=%q leaf=%q", ns, leaf)
	}
}

func TestSplitMcpFlatName_BareLeafGetsCodexInternalNamespace(t *testing.T) {
	cases := map[string][2]string{
		"spawn_agent":      {"multi_agent_v1", "spawn_agent"},
		"wait_agent":       {"multi_agent_v1", "wait_agent"},
		"send_input":       {"multi_agent_v1", "send_input"},
		"resume_agent":     {"multi_agent_v1", "resume_agent"},
		"close_agent":      {"multi_agent_v1", "close_agent"},
		"tool_search_tool": {"tool_search", "tool_search_tool"},
	}
	for full, want := range cases {
		ns, leaf := splitMcpFlatName(full)
		if ns != want[0] || leaf != want[1] {
			t.Fatalf("bare leaf split mismatch for %q: got (%q,%q) want (%q,%q)", full, ns, leaf, want[0], want[1])
		}
	}
}

func TestSplitMcpFlatName_UnrelatedBareNameStillPassThrough(t *testing.T) {
	ns, leaf := splitMcpFlatName("automation_update")
	if ns != "" || leaf != "automation_update" {
		t.Fatalf("automation_update should pass through (only mapped via codex_app__ prefix), got ns=%q leaf=%q", ns, leaf)
	}
}
func parseClaudeResponsesSSEEvent(t *testing.T, chunk []byte) (string, gjson.Result) {
	t.Helper()

	var event string
	var data string
	for _, line := range strings.Split(string(chunk), "\n") {
		if strings.HasPrefix(line, "event: ") {
			event = strings.TrimPrefix(line, "event: ")
			continue
		}
		if strings.HasPrefix(line, "data: ") {
			data = strings.TrimPrefix(line, "data: ")
		}
	}
	if data == "" {
		t.Fatalf("SSE chunk has no data line: %s", string(chunk))
	}

	return event, gjson.Parse(data)
}

func translateClaudeResponsesStreamThroughRegistry(chunks [][]byte) [][]byte {
	var param any
	var outputs [][]byte
	for _, chunk := range chunks {
		outputs = append(outputs, sdktranslator.TranslateStream(context.Background(), sdktranslator.FormatClaude, sdktranslator.FormatOpenAIResponse, "claude-test", nil, nil, chunk, &param)...)
	}
	return outputs
}

func TestConvertClaudeResponseToOpenAIResponses_ThinkingIncludesSignature(t *testing.T) {
	signature := "claude_sig_123"
	chunks := [][]byte{
		[]byte(`data: {"type":"message_start","message":{"id":"msg_123","usage":{"input_tokens":1,"output_tokens":0}}}`),
		[]byte(`data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`),
		[]byte(`data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"internal "}}`),
		[]byte(`data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"reasoning"}}`),
		[]byte(`data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"` + signature + `"}}`),
		[]byte(`data: {"type":"content_block_stop","index":0}`),
		[]byte(`data: {"type":"message_stop"}`),
	}

	var param any
	var outputs [][]byte
	for _, chunk := range chunks {
		outputs = append(outputs, ConvertClaudeResponseToOpenAIResponses(context.Background(), "claude-test", nil, nil, chunk, &param)...)
	}

	var reasoningDone gjson.Result
	var completed gjson.Result
	for _, output := range outputs {
		event, data := parseClaudeResponsesSSEEvent(t, output)
		switch event {
		case "response.output_item.done":
			if data.Get("item.type").String() == "reasoning" {
				reasoningDone = data
			}
		case "response.completed":
			completed = data
		}
	}

	if !reasoningDone.Exists() {
		t.Fatal("expected reasoning output_item.done event")
	}
	if got := reasoningDone.Get("item.encrypted_content").String(); got != signature {
		t.Fatalf("reasoning encrypted_content = %q, want %q", got, signature)
	}
	if got := reasoningDone.Get("item.summary.0.text").String(); got != "internal reasoning" {
		t.Fatalf("reasoning summary text = %q", got)
	}
	if got := completed.Get("response.output.0.encrypted_content").String(); got != signature {
		t.Fatalf("completed reasoning encrypted_content = %q, want %q", got, signature)
	}
	if got := completed.Get("response.output.0.summary.0.text").String(); got != "internal reasoning" {
		t.Fatalf("completed reasoning summary text = %q", got)
	}
}

func TestConvertClaudeResponseToOpenAIResponses_SuppressesSignatureDeltaPassthrough(t *testing.T) {
	chunk := []byte(`data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"claude_sig_123"}}`)

	outputs := translateClaudeResponsesStreamThroughRegistry([][]byte{chunk})
	if len(outputs) != 0 {
		t.Fatalf("expected signature_delta to be suppressed, got %d chunks", len(outputs))
	}
}

func TestConvertClaudeResponseToOpenAIResponses_AggregatesTextBlocksUntilMessageStop(t *testing.T) {
	chunks := [][]byte{
		[]byte(`data: {"type":"message_start","message":{"id":"msg_123","usage":{"input_tokens":1,"output_tokens":0}}}`),
		[]byte(`data: {"type":"content_block_start","index":4,"content_block":{"type":"text","text":""}}`),
		[]byte(`data: {"type":"content_block_delta","index":4,"delta":{"type":"text_delta","text":"**对比竞品**\n- "}}`),
		[]byte(`data: {"type":"content_block_stop","index":4}`),
		[]byte(`data: {"type":"content_block_start","index":5,"content_block":{"type":"server_tool_use","id":"srv_123","name":"web_search","input":{}}}`),
		[]byte(`data: {"type":"content_block_delta","index":5,"delta":{"type":"input_json_delta","partial_json":"{\"query\":\"Qwen3\"}"}}`),
		[]byte(`data: {"type":"content_block_stop","index":5}`),
		[]byte(`data: {"type":"content_block_start","index":6,"content_block":{"type":"web_search_tool_result","tool_use_id":"srv_123","content":[{"type":"web_search_result","title":"Example","url":"https://example.com"}]}}`),
		[]byte(`data: {"type":"content_block_stop","index":6}`),
		[]byte(`data: {"type":"content_block_delta","index":5,"delta":{"type":"citations_delta","citation":{"type":"web_search_result_location","cited_text":"Qwen 3.7 Max","url":"https://example.com","title":"Example"}}}`),
		[]byte(`data: {"type":"content_block_start","index":7,"content_block":{"type":"text","text":""}}`),
		[]byte(`data: {"type":"content_block_delta","index":7,"delta":{"type":"text_delta","text":"Qwen 3.7 Max leads."}}`),
		[]byte(`data: {"type":"content_block_stop","index":7}`),
		[]byte(`data: {"type":"message_delta","usage":{"output_tokens":12}}`),
		[]byte(`data: {"type":"message_stop"}`),
	}

	outputs := translateClaudeResponsesStreamThroughRegistry(chunks)

	counts := map[string]int{}
	var outputTextDone gjson.Result
	var completed gjson.Result
	for _, output := range outputs {
		event, data := parseClaudeResponsesSSEEvent(t, output)
		counts[event]++
		if event == "response.output_text.done" {
			outputTextDone = data
		}
		if event == "response.completed" {
			completed = data
		}
		// Count output_item add/done per item type so we can assert the text
		// message aggregates into exactly one item, independently of any
		// web_search_call items this fork emits as separate output items.
		if event == "response.output_item.added" {
			counts["item.added."+data.Get("item.type").String()]++
		}
		if event == "response.output_item.done" {
			counts["item.done."+data.Get("item.type").String()]++
		}
		if strings.HasPrefix(event, "content_block_") || event == "message_delta" {
			t.Fatalf("unexpected anthropic-native event leaked: %s", event)
		}
	}

	// The aggregated text reply must be exactly one assistant `message` item.
	// (This fork additionally emits a separate `web_search_call` output item,
	// which is verified below and must not inflate the message-item count.)
	if counts["item.added.message"] != 1 {
		t.Fatalf("message output_item.added count = %d, want 1", counts["item.added.message"])
	}
	if counts["response.content_part.added"] != 1 {
		t.Fatalf("response.content_part.added count = %d, want 1", counts["response.content_part.added"])
	}
	if counts["response.output_text.done"] != 1 {
		t.Fatalf("response.output_text.done count = %d, want 1", counts["response.output_text.done"])
	}
	if counts["response.content_part.done"] != 1 {
		t.Fatalf("response.content_part.done count = %d, want 1", counts["response.content_part.done"])
	}
	if counts["item.done.message"] != 1 {
		t.Fatalf("message output_item.done count = %d, want 1", counts["item.done.message"])
	}
	// The fork-specific web_search_call must surface as its own output item
	// (in_progress added + completed done), not folded into the text message.
	if counts["item.added.web_search_call"] != 1 {
		t.Fatalf("web_search_call output_item.added count = %d, want 1", counts["item.added.web_search_call"])
	}
	if counts["item.done.web_search_call"] != 1 {
		t.Fatalf("web_search_call output_item.done count = %d, want 1", counts["item.done.web_search_call"])
	}
	if counts["response.function_call_arguments.delta"] != 0 {
		t.Fatalf("response.function_call_arguments.delta count = %d, want 0", counts["response.function_call_arguments.delta"])
	}

	wantText := "**对比竞品**\n- Qwen 3.7 Max leads."
	if got := outputTextDone.Get("text").String(); got != wantText {
		t.Fatalf("output_text.done text = %q, want %q", got, wantText)
	}
	if got := completed.Get("response.output.0.content.0.text").String(); got != wantText {
		t.Fatalf("completed message text = %q, want %q", got, wantText)
	}
	if got := completed.Get("response.output.0.content.0.annotations.0.type").String(); got != "web_search_result_location" {
		t.Fatalf("completed annotation type = %q", got)
	}
}

func TestConvertClaudeResponseToOpenAIResponsesNonStream_ThinkingIncludesSignature(t *testing.T) {
	signature := "claude_sig_nonstream"
	raw := []byte(strings.Join([]string{
		`data: {"type":"message_start","message":{"id":"msg_nonstream","usage":{"input_tokens":1,"output_tokens":0}}}`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"nonstream reasoning"}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"` + signature + `"}}`,
		`data: {"type":"content_block_stop","index":0}`,
		`data: {"type":"message_stop"}`,
	}, "\n"))

	out := ConvertClaudeResponseToOpenAIResponsesNonStream(context.Background(), "claude-test", nil, nil, raw, nil)
	root := gjson.ParseBytes(out)

	if got := root.Get("output.0.encrypted_content").String(); got != signature {
		t.Fatalf("non-stream reasoning encrypted_content = %q, want %q", got, signature)
	}
	if got := root.Get("output.0.summary.0.text").String(); got != "nonstream reasoning" {
		t.Fatalf("non-stream reasoning summary text = %q", got)
	}
}
