package responses

import (
	"testing"

	"github.com/tidwall/gjson"
)

// DeepSeek's deepseek-reasoner / V4-pro family rejects multi-turn requests
// whose history is missing reasoning_content on the previous assistant turn.
// CPA must reuse the encrypted_content (or summary text fallback) carried by
// each Codex Responses "reasoning" item and attach it to the matching
// assistant message inside the chat-completions body it emits.
func TestConvertOpenAIResponsesRequest_AttachesReasoningContentToFollowingAssistant(t *testing.T) {
	raw := []byte(`{
		"model":"deepseek-v4-pro-reasoner",
		"input":[
			{"role":"user","type":"message","content":[{"type":"input_text","text":"hi"}]},
			{"type":"reasoning","encrypted_content":"thinking-blob","summary":[{"type":"summary_text","text":"i should answer hi"}]},
			{"role":"assistant","type":"message","content":[{"type":"output_text","text":"hello"}]},
			{"role":"user","type":"message","content":[{"type":"input_text","text":"and then?"}]}
		]
	}`)

	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("deepseek-v4-pro-reasoner", raw, false)

	messages := gjson.GetBytes(out, "messages")
	if !messages.IsArray() {
		t.Fatalf("messages missing/non-array: %s", string(out))
	}

	var foundAssistant bool
	messages.ForEach(func(_, m gjson.Result) bool {
		if m.Get("role").String() == "assistant" {
			foundAssistant = true
			rc := m.Get("reasoning_content").String()
			if rc != "thinking-blob" {
				t.Fatalf("expected assistant.reasoning_content=thinking-blob got %q in %s", rc, string(out))
			}
		}
		return true
	})
	if !foundAssistant {
		t.Fatalf("no assistant message emitted: %s", string(out))
	}
}

// When Codex hides the encrypted blob (e.g. on older builds) only the summary
// is available. We still need to send something through so DeepSeek does not
// 400; the summary text joined with blank lines is a reasonable default.
func TestConvertOpenAIResponsesRequest_FallsBackToSummaryWhenEncryptedAbsent(t *testing.T) {
	raw := []byte(`{
		"model":"deepseek-v4-pro-reasoner",
		"input":[
			{"role":"user","type":"message","content":[{"type":"input_text","text":"hi"}]},
			{"type":"reasoning","summary":[{"type":"summary_text","text":"hello"},{"type":"summary_text","text":"will greet"}]},
			{"role":"assistant","type":"message","content":[{"type":"output_text","text":"hello"}]}
		]
	}`)

	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("deepseek-v4-pro-reasoner", raw, false)

	messages := gjson.GetBytes(out, "messages")
	var rc string
	messages.ForEach(func(_, m gjson.Result) bool {
		if m.Get("role").String() == "assistant" {
			rc = m.Get("reasoning_content").String()
		}
		return true
	})
	if rc == "" {
		t.Fatalf("expected reasoning_content fallback to summary text, got empty body=%s", string(out))
	}
	if rc != "hello\n\nwill greet" {
		t.Fatalf("unexpected fallback content %q body=%s", rc, string(out))
	}
}

// Once attached to one assistant message, the buffered reasoning must NOT
// leak onto a later assistant turn.
func TestConvertOpenAIResponsesRequest_ReasoningIsConsumedExactlyOnce(t *testing.T) {
	raw := []byte(`{
		"model":"deepseek-v4-pro-reasoner",
		"input":[
			{"type":"reasoning","encrypted_content":"only-once"},
			{"role":"assistant","type":"message","content":[{"type":"output_text","text":"first"}]},
			{"role":"assistant","type":"message","content":[{"type":"output_text","text":"second"}]}
		]
	}`)

	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("deepseek-v4-pro-reasoner", raw, false)

	var assistantBlobs []string
	gjson.GetBytes(out, "messages").ForEach(func(_, m gjson.Result) bool {
		if m.Get("role").String() == "assistant" {
			assistantBlobs = append(assistantBlobs, m.Get("reasoning_content").String())
		}
		return true
	})
	if len(assistantBlobs) < 2 {
		t.Fatalf("expected two assistant messages, got %d body=%s", len(assistantBlobs), string(out))
	}
	if assistantBlobs[0] != "only-once" {
		t.Fatalf("first assistant should carry the reasoning, got %q body=%s", assistantBlobs[0], string(out))
	}
	if assistantBlobs[1] != "" {
		t.Fatalf("second assistant should NOT receive a leaked reasoning blob, got %q body=%s", assistantBlobs[1], string(out))
	}
}
