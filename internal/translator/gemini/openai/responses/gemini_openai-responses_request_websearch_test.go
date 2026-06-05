package responses

import (
	"testing"

	"github.com/tidwall/gjson"
)

// Gemini natively supports search grounding via the googleSearch builtin tool.
// Regression: previously a Responses {"type":"web_search"} tool fell into the
// switch default branch and was dropped ("dropping unsupported tool type"), so
// the model never received any search capability. It must now be mapped to a
// Gemini {"googleSearch":{}} tool node, sitting alongside functionDeclarations.
func TestConvertOpenAIResponsesRequestToGemini_WebSearchMapsToGoogleSearch(t *testing.T) {
	raw := []byte(`{
		"model": "gemini-3.1-pro-preview",
		"input": "what is the weather today?",
		"tools": [
			{"type":"web_search"},
			{"type":"function","name":"get_time","description":"get time","parameters":{"type":"object","properties":{}}}
		]
	}`)

	out := ConvertOpenAIResponsesRequestToGemini("gemini-3.1-pro-preview", raw, false)

	tools := gjson.GetBytes(out, "tools")
	if !tools.IsArray() {
		t.Fatalf("tools missing/non-array: %s", string(out))
	}

	hasGoogleSearch := false
	hasFunctionDecls := false
	tools.ForEach(func(_, tool gjson.Result) bool {
		if tool.Get("googleSearch").Exists() {
			hasGoogleSearch = true
		}
		if decls := tool.Get("functionDeclarations"); decls.IsArray() && len(decls.Array()) > 0 {
			hasFunctionDecls = true
		}
		return true
	})

	if !hasGoogleSearch {
		t.Fatalf("expected a googleSearch tool node, got: %s", string(out))
	}
	if !hasFunctionDecls {
		t.Fatalf("expected functionDeclarations to coexist with googleSearch, got: %s", string(out))
	}
}

// Even when web_search is the only tool (no function tools), it must still
// produce a googleSearch node. The old gate only emitted tools when there were
// function declarations, which would have dropped a lone web_search tool.
func TestConvertOpenAIResponsesRequestToGemini_WebSearchOnly(t *testing.T) {
	raw := []byte(`{
		"model": "gemini-3.1-pro-preview",
		"input": "latest news",
		"tools": [
			{"type":"web_search"}
		]
	}`)

	out := ConvertOpenAIResponsesRequestToGemini("gemini-3.1-pro-preview", raw, false)

	tools := gjson.GetBytes(out, "tools")
	if !tools.IsArray() || len(tools.Array()) == 0 {
		t.Fatalf("tools missing/empty for web_search-only request: %s", string(out))
	}

	hasGoogleSearch := false
	tools.ForEach(func(_, tool gjson.Result) bool {
		if tool.Get("googleSearch").Exists() {
			hasGoogleSearch = true
		}
		return true
	})
	if !hasGoogleSearch {
		t.Fatalf("expected a googleSearch tool node for web_search-only request, got: %s", string(out))
	}
}
