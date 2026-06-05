package responses

import (
	"context"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

// When Gemini uses the googleSearch builtin, results come back under
// candidates[0].groundingMetadata (webSearchQueries / groundingChunks), not as
// a functionCall. The responses lane must aggregate that into an OpenAI
// Responses web_search_call output item.
const geminiGroundingResponse = `{
	"responseId": "abc123",
	"candidates": [{
		"content": {"role": "model", "parts": [{"text": "Seattle is rainy today."}]},
		"finishReason": "STOP",
		"groundingMetadata": {
			"webSearchQueries": ["weather in Seattle today"],
			"groundingChunks": [
				{"web": {"uri": "https://example.com/weather", "title": "Seattle Weather"}},
				{"web": {"uri": "https://example.com/forecast", "title": "Forecast"}}
			]
		}
	}],
	"usageMetadata": {"promptTokenCount": 5, "candidatesTokenCount": 7, "totalTokenCount": 12}
}`

func TestGeminiResponses_NonStream_GroundingProducesWebSearchCall(t *testing.T) {
	req := []byte(`{"model":"gemini-3.1-pro-preview","tools":[{"type":"web_search"}]}`)
	out := ConvertGeminiResponseToOpenAIResponsesNonStream(context.Background(), "gemini-3.1-pro-preview", req, req, []byte(geminiGroundingResponse), nil)

	output := gjson.GetBytes(out, "output")
	if !output.IsArray() {
		t.Fatalf("output missing/non-array: %s", string(out))
	}

	var wsc gjson.Result
	output.ForEach(func(_, item gjson.Result) bool {
		if item.Get("type").String() == "web_search_call" {
			wsc = item
		}
		return true
	})

	if !wsc.Exists() {
		t.Fatalf("expected a web_search_call output item, got: %s", string(out))
	}
	if got := wsc.Get("status").String(); got != "completed" {
		t.Fatalf("web_search_call status = %q, want completed. body=%s", got, string(out))
	}
	if got := wsc.Get("action.type").String(); got != "search" {
		t.Fatalf("web_search_call action.type = %q, want search. body=%s", got, string(out))
	}
	if got := wsc.Get("action.query").String(); got != "weather in Seattle today" {
		t.Fatalf("web_search_call action.query = %q, want %q. body=%s", got, "weather in Seattle today", string(out))
	}
	// result entries preserve title/url from groundingChunks.
	if got := wsc.Get("result.0.url").String(); got != "https://example.com/weather" {
		t.Fatalf("web_search_call result.0.url = %q, want %q. body=%s", got, "https://example.com/weather", string(out))
	}
	if got := wsc.Get("result.0.title").String(); got != "Seattle Weather" {
		t.Fatalf("web_search_call result.0.title = %q, want %q. body=%s", got, "Seattle Weather", string(out))
	}
}

// Streaming aggregation: the final response.completed must also carry a
// completed web_search_call output item when grounding is present.
func TestGeminiResponses_Stream_GroundingProducesWebSearchCall(t *testing.T) {
	req := []byte(`{"model":"gemini-3.1-pro-preview","tools":[{"type":"web_search"}]}`)
	var param any
	events := ConvertGeminiResponseToOpenAIResponses(context.Background(), "gemini-3.1-pro-preview", req, req, []byte(geminiGroundingResponse), &param)

	var completedOut gjson.Result
	for _, ev := range events {
		s := string(ev)
		if !strings.Contains(s, `"response.completed"`) {
			continue
		}
		// SSE frame: "event: ...\ndata: {json}\n\n" -> grab the JSON payload.
		idx := strings.Index(s, "data:")
		if idx < 0 {
			continue
		}
		payload := strings.TrimSpace(s[idx+len("data:"):])
		completedOut = gjson.Parse(payload).Get("response.output")
	}

	if !completedOut.IsArray() {
		t.Fatalf("response.completed output missing/non-array; events=%v", joinEvents(events))
	}

	found := false
	completedOut.ForEach(func(_, item gjson.Result) bool {
		if item.Get("type").String() == "web_search_call" && item.Get("status").String() == "completed" {
			found = true
		}
		return true
	})
	if !found {
		t.Fatalf("response.completed output missing completed web_search_call; output=%s", completedOut.Raw)
	}
}

// When grounding is absent (model did not search), no web_search_call must be
// fabricated.
func TestGeminiResponses_NonStream_NoGroundingNoWebSearchCall(t *testing.T) {
	resp := `{
		"responseId": "no-ground",
		"candidates": [{
			"content": {"role": "model", "parts": [{"text": "plain answer"}]},
			"finishReason": "STOP"
		}],
		"usageMetadata": {"promptTokenCount": 3, "candidatesTokenCount": 2, "totalTokenCount": 5}
	}`
	req := []byte(`{"model":"gemini-3.1-pro-preview","tools":[{"type":"web_search"}]}`)
	out := ConvertGeminiResponseToOpenAIResponsesNonStream(context.Background(), "gemini-3.1-pro-preview", req, req, []byte(resp), nil)

	gjson.GetBytes(out, "output").ForEach(func(_, item gjson.Result) bool {
		if item.Get("type").String() == "web_search_call" {
			t.Fatalf("did not expect a web_search_call when grounding absent: %s", string(out))
		}
		return true
	})
}

func joinEvents(events [][]byte) string {
	var b strings.Builder
	for _, e := range events {
		b.Write(e)
	}
	return b.String()
}
