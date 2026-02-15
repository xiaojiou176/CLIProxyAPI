package openai

import (
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestRewriteSpawnAgentExplorerChunk_RewritesFunctionCallArgumentsDone(t *testing.T) {
	chunk := []byte(
		`event: response.function_call_arguments.done
data: {"type":"response.function_call_arguments.done","arguments":"{\"message\":\"scan\",\"agent_type\":\"explorer\"}"}`,
	)

	rewritten := rewriteSpawnAgentExplorerChunk(chunk)
	lines := strings.Split(string(rewritten), "\n")
	if len(lines) < 2 {
		t.Fatalf("unexpected chunk format: %s", string(rewritten))
	}
	payload := strings.TrimSpace(strings.TrimPrefix(lines[1], "data:"))
	arguments := gjson.Get(payload, "arguments").String()
	if gjson.Get(arguments, "agent_type").Exists() {
		t.Fatalf("agent_type should be removed, got %s", arguments)
	}
	if got := gjson.Get(arguments, "message").String(); got != "scan" {
		t.Fatalf("message = %q, want %q", got, "scan")
	}
}

func TestRewriteSpawnAgentExplorerChunk_RewritesOutputItemDoneForSpawnAgent(t *testing.T) {
	chunk := []byte(
		`event: response.output_item.done
data: {"type":"response.output_item.done","item":{"type":"function_call","name":"spawn_agent","arguments":"{\"agent_type\":\"explorer\",\"message\":\"analyze\"}"}}`,
	)

	rewritten := rewriteSpawnAgentExplorerChunk(chunk)
	lines := strings.Split(string(rewritten), "\n")
	if len(lines) < 2 {
		t.Fatalf("unexpected chunk format: %s", string(rewritten))
	}
	payload := strings.TrimSpace(strings.TrimPrefix(lines[1], "data:"))
	arguments := gjson.Get(payload, "item.arguments").String()
	if gjson.Get(arguments, "agent_type").Exists() {
		t.Fatalf("agent_type should be removed, got %s", arguments)
	}
	if got := gjson.Get(arguments, "message").String(); got != "analyze" {
		t.Fatalf("message = %q, want %q", got, "analyze")
	}
}

func TestRewriteSpawnAgentExplorerChunk_DoesNotRewriteNonSpawnAgent(t *testing.T) {
	chunk := []byte(
		`event: response.output_item.done
data: {"type":"response.output_item.done","item":{"type":"function_call","name":"wait","arguments":"{\"agent_type\":\"explorer\",\"ids\":[\"a\"]}"}}`,
	)

	rewritten := rewriteSpawnAgentExplorerChunk(chunk)
	if string(rewritten) != string(chunk) {
		t.Fatalf("chunk should remain unchanged, got %s", string(rewritten))
	}
}

func TestRewriteSpawnAgentExplorerInNonStreamingResponse_RewritesArguments(t *testing.T) {
	body := []byte(`{
		"output": [
			{
				"type": "function_call",
				"name": "spawn_agent",
				"arguments": "{\"agent_type\":\"explorer\",\"message\":\"scan\"}"
			},
			{
				"type": "function_call",
				"name": "wait",
				"arguments": "{\"agent_type\":\"explorer\"}"
			}
		]
	}`)

	rewritten := rewriteSpawnAgentExplorerInNonStreamingResponse(body)
	parsed := gjson.ParseBytes(rewritten)

	spawnArgs := parsed.Get("output.0.arguments").String()
	if gjson.Get(spawnArgs, "agent_type").Exists() {
		t.Fatalf("spawn agent_type should be removed, got %s", spawnArgs)
	}
	if got := gjson.Get(spawnArgs, "message").String(); got != "scan" {
		t.Fatalf("spawn message = %q, want %q", got, "scan")
	}

	waitArgs := parsed.Get("output.1.arguments").String()
	if !gjson.Get(waitArgs, "agent_type").Exists() {
		t.Fatalf("non-spawn call should remain unchanged, got %s", waitArgs)
	}
}

