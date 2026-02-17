package executor

import (
	"strings"
	"testing"
)

func TestBuildPromptDebugSection_OpenAIMessages(t *testing.T) {
	body := []byte(`{
  "model":"gpt-5",
  "messages":[
    {"role":"system","content":"You are a strict coding assistant."},
    {"role":"user","content":"Refactor this function and explain why."}
  ]
}`)

	got := buildPromptDebugSection(body)
	if !strings.Contains(got, "messages[1] (system, chars=") || !strings.Contains(got, "You are a strict coding assistant.") {
		t.Fatalf("missing system message, got:\n%s", got)
	}
	if !strings.Contains(got, "messages[2] (user, chars=") || !strings.Contains(got, "Refactor this function and explain why.") {
		t.Fatalf("missing user message, got:\n%s", got)
	}
}

func TestBuildPromptDebugSection_AnthropicSystemAndMessages(t *testing.T) {
	body := []byte(`{
  "system":[{"type":"text","text":"Always answer in JSON."}],
  "messages":[
    {"role":"user","content":[{"type":"text","text":"List 3 risks."}]}
  ]
}`)

	got := buildPromptDebugSection(body)
	if !strings.Contains(got, "system[1] (system, chars=") || !strings.Contains(got, "Always answer in JSON.") {
		t.Fatalf("missing system prompt, got:\n%s", got)
	}
	if !strings.Contains(got, "messages[1] (user, chars=") || !strings.Contains(got, "List 3 risks.") {
		t.Fatalf("missing user prompt, got:\n%s", got)
	}
}

func TestBuildPromptDebugSection_ResponsesInput(t *testing.T) {
	body := []byte(`{
  "input":[
    {"role":"system","content":[{"type":"input_text","text":"Be concise."}]},
    {"role":"user","content":[{"type":"input_text","text":"Summarize this diff."}]}
  ]
}`)

	got := buildPromptDebugSection(body)
	if !strings.Contains(got, "input[1] (system, chars=") || !strings.Contains(got, "Be concise.") {
		t.Fatalf("missing input system prompt, got:\n%s", got)
	}
	if !strings.Contains(got, "input[2] (user, chars=") || !strings.Contains(got, "Summarize this diff.") {
		t.Fatalf("missing input user prompt, got:\n%s", got)
	}
}

func TestBuildPromptDebugSection_NonJSON(t *testing.T) {
	got := buildPromptDebugSection([]byte("plain text payload"))
	if got != "- <non-json body>" {
		t.Fatalf("unexpected non-json marker: %q", got)
	}
}

func TestFormatAuthInfo_NoRedactionForAPIKey(t *testing.T) {
	info := upstreamRequestLog{
		Provider:  "openai",
		AuthID:    "auth-1",
		AuthLabel: "primary",
		AuthType:  "api_key",
		AuthValue: "sk-test-secret-value",
	}

	got := formatAuthInfo(info)
	if !strings.Contains(got, "type=api_key value=sk-test-secret-value") {
		t.Fatalf("api key should be logged in raw form, got: %s", got)
	}
}
