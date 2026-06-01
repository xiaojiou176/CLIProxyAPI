package responses

import (
	"testing"

	"github.com/tidwall/gjson"
)

// Vertex Gemini API rejects any contents[].role other than "user" or "model".
// Codex Responses uses role="developer" for system-style instructions; CPA
// must coerce that to systemInstruction. Any other unknown role must be
// coerced to "user" so Vertex does not return 400 INVALID_ARGUMENT.
func TestConvertOpenAIResponsesRequestToGemini_DeveloperRoleGoesToSystemInstruction(t *testing.T) {
	raw := []byte(`{
		"model": "gemini-3.1-pro-preview",
		"input": [
			{"role":"developer","type":"message","content":[{"type":"input_text","text":"You are helpful."}]},
			{"role":"user","type":"message","content":[{"type":"input_text","text":"hi"}]}
		]
	}`)

	out := ConvertOpenAIResponsesRequestToGemini("gemini-3.1-pro-preview", raw, false)

	sysText := gjson.GetBytes(out, "systemInstruction.parts.0.text").String()
	if sysText != "You are helpful." {
		t.Fatalf("expected developer text in systemInstruction, got %q body=%s", sysText, string(out))
	}

	contents := gjson.GetBytes(out, "contents")
	if !contents.IsArray() {
		t.Fatalf("contents missing/non-array: %s", string(out))
	}
	contents.ForEach(func(_, entry gjson.Result) bool {
		role := entry.Get("role").String()
		if role != "user" && role != "model" {
			t.Fatalf("invalid contents role %q body=%s", role, string(out))
		}
		return true
	})
}

func TestConvertOpenAIResponsesRequestToGemini_UnknownRoleCoercedToUser(t *testing.T) {
	raw := []byte(`{
		"model": "gemini-3.1-pro-preview",
		"input": [
			{"role":"tool_observer","type":"message","content":[{"type":"input_text","text":"watching"}]}
		]
	}`)

	out := ConvertOpenAIResponsesRequestToGemini("gemini-3.1-pro-preview", raw, false)

	role := gjson.GetBytes(out, "contents.0.role").String()
	if role != "user" {
		t.Fatalf("expected unknown role to be coerced to user, got %q body=%s", role, string(out))
	}
}
