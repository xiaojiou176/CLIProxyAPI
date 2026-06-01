package common

import (
	"strings"

	"github.com/tidwall/sjson"
)

// codexInternalNamespacePrefixes lists Codex-internal namespaces that get
// flattened with "__" when emitted to non-Responses-native protocols.
var codexInternalNamespacePrefixes = []string{
	"multi_agent_v1",
	"multi_agent_v2",
	"tool_search",
	"codex_app",
}

// bareCodexInternalLeafToNamespace maps the bare leaf names of well-known
// Codex-internal namespaced tools back to their owning namespace, for
// upstream backends that drop the "<ns>__" prefix on the way back.
var bareCodexInternalLeafToNamespace = map[string]string{
	"spawn_agent":      "multi_agent_v1",
	"wait_agent":       "multi_agent_v1",
	"send_input":       "multi_agent_v1",
	"resume_agent":     "multi_agent_v1",
	"close_agent":      "multi_agent_v1",
	"followup_task":    "multi_agent_v2",
	"list_agents":      "multi_agent_v2",
	"send_message":     "multi_agent_v2",
	"tool_search_tool": "tool_search",
}

// SplitMcpFlatName splits a Codex-style flat namespaced tool name into
// (namespace, leaf) so the Codex tool registry can dispatch on
// (namespace, name). Non-namespaced names pass through as ("", fullName).
//
//   - "mcp__SERVER__TOOL"            -> ("mcp__SERVER", "TOOL")
//   - "multi_agent_v1__spawn_agent"  -> ("multi_agent_v1", "spawn_agent")
//   - "codex_app__automation_update" -> ("codex_app", "automation_update")
func SplitMcpFlatName(fullName string) (string, string) {
	if strings.HasPrefix(fullName, "mcp__") {
		rest := fullName[len("mcp__"):]
		idx := strings.Index(rest, "__")
		if idx <= 0 || idx+2 >= len(rest) {
			return "", fullName
		}
		return "mcp__" + rest[:idx], rest[idx+2:]
	}
	for _, ns := range codexInternalNamespacePrefixes {
		marker := ns + "__"
		if strings.HasPrefix(fullName, marker) {
			leaf := fullName[len(marker):]
			if leaf == "" {
				return "", fullName
			}
			return ns, leaf
		}
	}
	if ns, ok := bareCodexInternalLeafToNamespace[fullName]; ok {
		return ns, fullName
	}
	return "", fullName
}

// SetMcpToolNameOnItem splits an MCP-style flat tool name into the leaf tool
// `name` plus its `namespace` on a function_call JSON item, so the Codex tool
// registry can dispatch the call. Non-MCP names pass through unchanged.
//
// itemPrefix is the dotted JSON path prefix to the function_call item
// ("item" for streaming events, "" for non-stream output items).
func SetMcpToolNameOnItem(buf []byte, itemPrefix, fullName string) []byte {
	namespace, leaf := SplitMcpFlatName(fullName)
	nameKey := "name"
	nsKey := "namespace"
	if itemPrefix != "" {
		nameKey = itemPrefix + ".name"
		nsKey = itemPrefix + ".namespace"
	}
	buf, _ = sjson.SetBytes(buf, nameKey, leaf)
	if namespace != "" {
		buf, _ = sjson.SetBytes(buf, nsKey, namespace)
	}
	return buf
}
