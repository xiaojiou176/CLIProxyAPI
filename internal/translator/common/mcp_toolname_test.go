package common

import "testing"

// TestSplitMcpFlatName_TwoSegment covers the common, well-behaved case where
// the MCP server namespace is a single segment (no embedded "__"). These must
// keep working exactly as before the three-segment fix.
func TestSplitMcpFlatName_TwoSegment(t *testing.T) {
	cases := []struct {
		full   string
		wantNS string
		wantLf string
	}{
		{"mcp__node_repl__js", "mcp__node_repl", "js"},
		{"mcp__Serena__find_symbol", "mcp__Serena", "find_symbol"},
		{"mcp__computer_use__click", "mcp__computer_use", "click"},
	}
	for _, c := range cases {
		ns, lf := SplitMcpFlatName(c.full)
		if ns != c.wantNS || lf != c.wantLf {
			t.Errorf("SplitMcpFlatName(%q) = (%q, %q), want (%q, %q)",
				c.full, ns, lf, c.wantNS, c.wantLf)
		}
	}
}

// TestSplitMcpFlatName_ThreeSegment covers the codex_apps connector family,
// where the server namespace itself contains "__"
// ("mcp__codex_apps__<connector>"). The flat join therefore looks like
// "mcp__codex_apps__<connector>___<leaf>" (note the "___": the "__" separator
// plus a leaf that begins with "_"). The host dispatches on
// (namespace="mcp__codex_apps__<connector>", name="_<leaf>").
func TestSplitMcpFlatName_ThreeSegment(t *testing.T) {
	cases := []struct {
		full   string
		wantNS string
		wantLf string
	}{
		{
			"mcp__codex_apps__sites___list_projects",
			"mcp__codex_apps__sites",
			"_list_projects",
		},
		{
			"mcp__codex_apps__canva___autofill_design",
			"mcp__codex_apps__canva",
			"_autofill_design",
		},
		{
			"mcp__codex_apps__sites___mint_source_repository_write__792fad77",
			"mcp__codex_apps__sites",
			"_mint_source_repository_write__792fad77",
		},
	}
	for _, c := range cases {
		ns, lf := SplitMcpFlatName(c.full)
		if ns != c.wantNS || lf != c.wantLf {
			t.Errorf("SplitMcpFlatName(%q) = (%q, %q), want (%q, %q)",
				c.full, ns, lf, c.wantNS, c.wantLf)
		}
	}
}

// TestSplitMcpFlatName_Internal covers Codex-internal namespaces and pass-through.
func TestSplitMcpFlatName_Internal(t *testing.T) {
	cases := []struct {
		full   string
		wantNS string
		wantLf string
	}{
		{"multi_agent_v1__spawn_agent", "multi_agent_v1", "spawn_agent"},
		{"codex_app__automation_update", "codex_app", "automation_update"},
		{"tool_search__tool_search_tool", "tool_search", "tool_search_tool"},
		{"spawn_agent", "multi_agent_v1", "spawn_agent"},
		{"plain_tool", "", "plain_tool"},
		{"mcp__only", "", "mcp__only"},
	}
	for _, c := range cases {
		ns, lf := SplitMcpFlatName(c.full)
		if ns != c.wantNS || lf != c.wantLf {
			t.Errorf("SplitMcpFlatName(%q) = (%q, %q), want (%q, %q)",
				c.full, ns, lf, c.wantNS, c.wantLf)
		}
	}
}
