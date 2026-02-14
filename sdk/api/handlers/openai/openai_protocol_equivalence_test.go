package openai

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestShouldTreatAsResponsesFormat(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body string
		want bool
	}{
		{
			name: "chat completions payload",
			body: `{"model":"gpt-5.3-codex","messages":[{"role":"user","content":"hello"}]}`,
			want: false,
		},
		{
			name: "responses payload with input",
			body: `{"model":"gpt-5.3-codex","input":"hello"}`,
			want: true,
		},
		{
			name: "responses payload with instructions",
			body: `{"model":"gpt-5.3-codex","instructions":"be concise"}`,
			want: true,
		},
		{
			name: "messages takes precedence over input",
			body: `{"model":"gpt-5.3-codex","messages":[{"role":"user","content":"hello"}],"input":"shadow"}`,
			want: false,
		},
		{
			name: "neither messages nor responses fields",
			body: `{"model":"gpt-5.3-codex","temperature":0.2}`,
			want: false,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := shouldTreatAsResponsesFormat([]byte(tc.body))
			if got != tc.want {
				t.Fatalf("shouldTreatAsResponsesFormat()=%v, want=%v, body=%s", got, tc.want, tc.body)
			}
		})
	}
}

func TestConvertCompletionsRequestToChatCompletions(t *testing.T) {
	t.Parallel()

	in := []byte(`{
		"model":"gpt-5.3-codex",
		"prompt":"hello world",
		"max_tokens":128,
		"temperature":0.2,
		"top_p":0.9,
		"frequency_penalty":0.1,
		"presence_penalty":0.4,
		"stop":["END"],
		"stream":true,
		"logprobs":true,
		"top_logprobs":2,
		"echo":false
	}`)
	out := convertCompletionsRequestToChatCompletions(in)

	checks := []struct {
		path string
		want string
	}{
		{path: "model", want: "gpt-5.3-codex"},
		{path: "messages.0.role", want: "user"},
		{path: "messages.0.content", want: "hello world"},
	}
	for _, check := range checks {
		got := gjson.GetBytes(out, check.path).String()
		if got != check.want {
			t.Fatalf("%s=%q, want=%q; out=%s", check.path, got, check.want, string(out))
		}
	}
	if !gjson.GetBytes(out, "stream").Bool() {
		t.Fatalf("stream=false, want=true; out=%s", string(out))
	}
	if gjson.GetBytes(out, "max_tokens").Int() != 128 {
		t.Fatalf("max_tokens=%d, want=128; out=%s", gjson.GetBytes(out, "max_tokens").Int(), string(out))
	}
	if gjson.GetBytes(out, "top_logprobs").Int() != 2 {
		t.Fatalf("top_logprobs=%d, want=2; out=%s", gjson.GetBytes(out, "top_logprobs").Int(), string(out))
	}
	if gotStop := gjson.GetBytes(out, "stop.0").String(); gotStop != "END" {
		t.Fatalf("stop[0]=%q, want=%q; out=%s", gotStop, "END", string(out))
	}

	defaultPromptOut := convertCompletionsRequestToChatCompletions([]byte(`{"model":"gpt-5.3-codex"}`))
	if got := gjson.GetBytes(defaultPromptOut, "messages.0.content").String(); got != "Complete this:" {
		t.Fatalf("default prompt=%q, want=%q; out=%s", got, "Complete this:", string(defaultPromptOut))
	}
}

func TestConvertChatCompletionsResponseToCompletions(t *testing.T) {
	t.Parallel()

	in := []byte(`{
		"id":"resp_123",
		"object":"chat.completion",
		"created":1700000000,
		"model":"gpt-5.3-codex",
		"choices":[
			{
				"index":0,
				"message":{"role":"assistant","content":"done"},
				"finish_reason":"stop"
			}
		],
		"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}
	}`)
	out := convertChatCompletionsResponseToCompletions(in)

	if got := gjson.GetBytes(out, "object").String(); got != "text_completion" {
		t.Fatalf("object=%q, want=%q; out=%s", got, "text_completion", string(out))
	}
	if got := gjson.GetBytes(out, "id").String(); got != "resp_123" {
		t.Fatalf("id=%q, want=%q; out=%s", got, "resp_123", string(out))
	}
	if got := gjson.GetBytes(out, "choices.0.text").String(); got != "done" {
		t.Fatalf("choices[0].text=%q, want=%q; out=%s", got, "done", string(out))
	}
	if got := gjson.GetBytes(out, "choices.0.finish_reason").String(); got != "stop" {
		t.Fatalf("choices[0].finish_reason=%q, want=%q; out=%s", got, "stop", string(out))
	}
	if got := gjson.GetBytes(out, "usage.total_tokens").Int(); got != 3 {
		t.Fatalf("usage.total_tokens=%d, want=3; out=%s", got, string(out))
	}
}

func TestConvertChatCompletionsStreamChunkToCompletions(t *testing.T) {
	t.Parallel()

	emptyChunk := []byte(`{
		"id":"chunk_1",
		"created":1700000000,
		"model":"gpt-5.3-codex",
		"choices":[{"index":0,"delta":{}}]
	}`)
	if out := convertChatCompletionsStreamChunkToCompletions(emptyChunk); out != nil {
		t.Fatalf("expected nil chunk for empty delta; out=%s", string(out))
	}

	contentChunk := []byte(`{
		"id":"chunk_2",
		"created":1700000001,
		"model":"gpt-5.3-codex",
		"choices":[{"index":0,"delta":{"content":"he"}}]
	}`)
	contentOut := convertChatCompletionsStreamChunkToCompletions(contentChunk)
	if contentOut == nil {
		t.Fatal("content chunk converted to nil, want non-nil")
	}
	if got := gjson.GetBytes(contentOut, "choices.0.text").String(); got != "he" {
		t.Fatalf("choices[0].text=%q, want=%q; out=%s", got, "he", string(contentOut))
	}

	finishChunk := []byte(`{
		"id":"chunk_3",
		"created":1700000002,
		"model":"gpt-5.3-codex",
		"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]
	}`)
	finishOut := convertChatCompletionsStreamChunkToCompletions(finishChunk)
	if finishOut == nil {
		t.Fatal("finish_reason chunk converted to nil, want non-nil")
	}
	if got := gjson.GetBytes(finishOut, "choices.0.finish_reason").String(); got != "stop" {
		t.Fatalf("choices[0].finish_reason=%q, want=%q; out=%s", got, "stop", string(finishOut))
	}
}
