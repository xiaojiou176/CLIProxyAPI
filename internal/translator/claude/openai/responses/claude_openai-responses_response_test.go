package responses

import (
	"context"
	"strings"
	"testing"

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
