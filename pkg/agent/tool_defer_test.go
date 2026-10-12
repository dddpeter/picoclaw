// PicoClaw - Ultra-lightweight personal AI agent
// License: MIT

package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/tools"
)

// mcpDefs builds n fake MCP tool definitions with searchable names.
func mcpDefs(n int) []providers.ToolDefinition {
	defs := make([]providers.ToolDefinition, 0, n)
	for i := range n {
		defs = append(defs, providers.ToolDefinition{
			Type: "function",
			Function: providers.ToolFunctionDefinition{
				Name: fmt.Sprintf("mcp_srv_issue_%02d", i),
				Description: fmt.Sprintf("Manages repository issue #%d.\n%s", i,
					strings.Repeat("Supports labels, milestones, assignees, comments and cross-references. ", 3)),
				Parameters: map[string]any{
					"type":       "object",
					"properties": map[string]any{"title": map[string]any{"type": "string"}},
				},
			},
		})
	}
	return defs
}

func builtinDef(name string) providers.ToolDefinition {
	return providers.ToolDefinition{
		Type: "function",
		Function: providers.ToolFunctionDefinition{
			Name:        name,
			Description: name + " does its builtin thing",
			Parameters:  map[string]any{"type": "object"},
		},
	}
}

func deferTestLoop(t *testing.T, threshold int) (*AgentLoop, *AgentInstance) {
	t.Helper()
	al, agent, cleanup := newTurnCoordTestLoop(t, &summarizeStubProvider{})
	t.Cleanup(cleanup)
	if al.cfg == nil {
		al.cfg = &config.Config{}
	}
	al.cfg.Tools.MCP.DeferredThreshold = threshold
	agent.Tools = tools.NewToolRegistry()
	return al, agent
}

// TestApplyToolDeferral_BelowThresholdNoChange pins the gate: below the
// threshold nothing is deferred and no tool_search meta-tool appears (small
// catalogs are not worth the extra hop — octop borrowing §六).
func TestApplyToolDeferral_BelowThresholdNoChange(t *testing.T) {
	al, _ := deferTestLoop(t, 20)
	defs := append(mcpDefs(3), builtinDef("read_file"))

	out := al.applyToolDeferral("s-def", defs)
	if len(out) != len(defs) {
		t.Fatalf("below threshold must not change the def count: got %d want %d", len(out), len(defs))
	}
	for i := range defs {
		if out[i].Function.Name != defs[i].Function.Name {
			t.Fatalf("def %d changed: %q", i, out[i].Function.Name)
		}
	}
}

// TestApplyToolDeferral_AboveThreshold pins the core rewrite: unloaded MCP
// tools become lightweight deferred references (first description line +
// hint suffix, minimal schema), builtins stay verbatim, tool_search joins,
// and the LLM-facing token cost drops.
func TestApplyToolDeferral_AboveThreshold(t *testing.T) {
	al, _ := deferTestLoop(t, 20)
	mcp := mcpDefs(21)
	defs := append(append([]providers.ToolDefinition(nil), mcp...), builtinDef("read_file"))

	out := al.applyToolDeferral("s-def", defs)

	deferred, search, builtin := 0, false, false
	for _, d := range out {
		switch {
		case d.Function.Name == toolSearchToolName:
			search = true
		case d.Function.Name == "read_file":
			builtin = true
			if d.Function.Parameters == nil {
				t.Fatal("builtin def must keep its schema verbatim")
			}
		default:
			if !strings.HasPrefix(d.Function.Name, "mcp_") {
				t.Fatalf("unexpected def %q", d.Function.Name)
			}
			deferred++
			if !strings.Contains(d.Function.Description, deferredToolHint) {
				t.Fatalf("deferred ref %q must carry the hint suffix", d.Function.Name)
			}
			if strings.Contains(d.Function.Description, "milestones") {
				t.Fatalf("deferred ref %q must truncate the description to its first line", d.Function.Name)
			}
		}
	}
	if deferred != 21 {
		t.Fatalf("deferred count = %d, want 21", deferred)
	}
	if !search {
		t.Fatal("tool_search meta-tool must join the def set when deferral is active")
	}
	if !builtin {
		t.Fatal("builtin read_file must stay in the def set")
	}
	if got := EstimateToolDefsTokens(out); got >= EstimateToolDefsTokens(defs) {
		t.Fatalf("deferred set (%d tokens) must cost less than the full set (%d)",
			got, EstimateToolDefsTokens(defs))
	}
}

// TestApplyToolDeferral_LoadedSticky pins session stickiness: once loaded
// (via tool_search or a direct call), the full schema stays visible for the
// session — and sessions do not leak into each other.
func TestApplyToolDeferral_LoadedSticky(t *testing.T) {
	al, _ := deferTestLoop(t, 20)
	defs := mcpDefs(21)

	// Load one tool for s-a.
	al.toolDefer.markLoaded("s-a", defs[3].Function.Name)

	outA := al.applyToolDeferral("s-a", defs)
	kept := 0
	for _, d := range outA {
		if d.Function.Name == defs[3].Function.Name && !strings.Contains(d.Function.Description, deferredToolHint) {
			kept++
		}
	}
	if kept != 1 {
		t.Fatal("loaded tool must keep its full schema for its session")
	}

	outB := al.applyToolDeferral("s-b", defs)
	for _, d := range outB {
		if d.Function.Name == defs[3].Function.Name && !strings.Contains(d.Function.Description, deferredToolHint) {
			t.Fatal("loaded set must not leak across sessions")
		}
	}
}

// TestToolSearchIntercept pins the tool_search execution: it matches
// name/description case-insensitively, honors max_results, returns the full
// definitions as the result, and marks matches loaded.
func TestToolSearchIntercept(t *testing.T) {
	al, agent := deferTestLoop(t, 20)
	defs := append(mcpDefs(21), builtinDef("issue_tracker_local"))
	for _, d := range defs {
		agent.Tools.Register(&segTestTool{
			name: d.Function.Name,
			desc: d.Function.Description,
		})
	}

	res, handled := al.interceptDeferredTool(context.Background(), "s-ts", toolSearchToolName,
		map[string]any{"query": "issue labels", "max_results": 2}, agent.Tools, config.EffectiveTurnProfile{})
	if !handled {
		t.Fatal("tool_search must be handled by the interceptor")
	}
	if res == nil || res.IsError {
		t.Fatalf("tool_search result: %+v", res)
	}
	content := res.ContentForLLM()
	if !strings.Contains(content, "mcp_srv_issue_") {
		t.Fatalf("tool_search result must list matched definitions, got: %.200s", content)
	}
	// max_results honored: no more than 2 definition blocks.
	if got := strings.Count(content, "Name:"); got > 2 {
		t.Fatalf("max_results=2 must cap the result at 2 definitions, got %d", got)
	}
	// The matches are now loaded: their defs are no longer deferred.
	out := al.applyToolDeferral("s-ts", append(mcpDefs(21), builtinDef("issue_tracker_local")))
	full := 0
	for _, d := range out {
		if strings.HasPrefix(d.Function.Name, "mcp_srv_issue_") && !strings.Contains(d.Function.Description, deferredToolHint) {
			full++
		}
	}
	if full == 0 || full > 2 {
		t.Fatalf("loaded count after search = %d, want 1..2", full)
	}
}

// TestDeferredDirectCallAutoLoads pins the weak-model fallback (octop
// borrowing §六-3): a direct call to an unloaded deferred tool does not
// execute blindly — the tool is auto-loaded and the result carries the full
// schema with a retry instruction. The second call falls through to real
// execution.
func TestDeferredDirectCallAutoLoads(t *testing.T) {
	al, agent := deferTestLoop(t, 20)
	defs := mcpDefs(21)
	for _, d := range defs {
		agent.Tools.Register(&segTestTool{
			name: d.Function.Name,
			desc: d.Function.Description,
		})
	}

	res, handled := al.interceptDeferredTool(context.Background(), "s-direct",
		defs[7].Function.Name, map[string]any{"title": "x"}, agent.Tools, config.EffectiveTurnProfile{})
	if !handled {
		t.Fatal("direct call to an unloaded deferred tool must be intercepted")
	}
	content := res.ContentForLLM()
	if !strings.Contains(content, "Parameters:") || !strings.Contains(content, defs[7].Function.Name) {
		t.Fatalf("auto-load result must carry the full schema, got: %.200s", content)
	}

	// Loaded now: the same call falls through to the real tool.
	_, handled = al.interceptDeferredTool(context.Background(), "s-direct",
		defs[7].Function.Name, map[string]any{"title": "x"}, agent.Tools, config.EffectiveTurnProfile{})
	if handled {
		t.Fatal("loaded tool must execute for real, not be intercepted again")
	}
}

// TestToolDefer_DisabledByNegativeThreshold pins the off switch: a
// negative threshold keeps today's behavior exactly (all schemas always
// visible, no tool_search injected).
func TestToolDefer_DisabledByNegativeThreshold(t *testing.T) {
	al, _ := deferTestLoop(t, -1)
	defs := mcpDefs(50)

	out := al.applyToolDeferral("s-off", defs)
	if len(out) != len(defs) {
		t.Fatalf("disabled deferral must not change the def set: %d vs %d", len(out), len(defs))
	}
	for _, d := range out {
		if strings.Contains(d.Function.Description, deferredToolHint) {
			t.Fatal("disabled deferral must not rewrite descriptions")
		}
	}
}

// TestToolDefer_DefaultThresholdPinsDoc pins the default: an unset config
// enables deferral at the recommended threshold (20).
func TestToolDefer_DefaultThresholdPinsDoc(t *testing.T) {
	al, _ := deferTestLoop(t, 0) // 0/absent = default
	def21 := mcpDefs(21)
	if out := al.applyToolDeferral("s-def", def21); len(out) != len(def21)+1 {
		t.Fatalf("21 MCP tools over the default 20 must defer (+tool_search): %d defs", len(out))
	}
	def20 := mcpDefs(20)
	if out := al.applyToolDeferral("s-def", def20); len(out) != len(def20) {
		t.Fatalf("20 MCP tools at the default 20 must not defer: %d defs", len(out))
	}
}

// TestAppendMetaToolDefs_Idempotent pins the P0-1 fix: feeding the rewritten
// set back (the BeforeLLM echo path — HookManager returns a clone even with
// no hooks) must not duplicate tool_search nor drop ask_user. Providers
// reject duplicate tool names (Anthropic 400s), so this is a turn-killer
// when it regresses.
func TestAppendMetaToolDefs_Idempotent(t *testing.T) {
	al, _ := deferTestLoop(t, 20)
	base := append(mcpDefs(30), builtinDef("read"))

	once := al.appendMetaToolDefs("s-idem", base)
	twice := al.appendMetaToolDefs("s-idem", once) // the echo-path replay

	countNamed := func(defs []providers.ToolDefinition, name string) int {
		n := 0
		for _, d := range defs {
			if d.Function.Name == name {
				n++
			}
		}
		return n
	}
	if got := countNamed(twice, toolSearchToolName); got != 1 {
		t.Fatalf("tool_search must appear exactly once after replay, got %d", got)
	}
	if got := countNamed(twice, askUserToolName); got != 1 {
		t.Fatalf("ask_user must appear exactly once after replay, got %d", got)
	}

	// A deliberately cleared tool list (graceful terminal shape) stays empty.
	if got := al.appendMetaToolDefs("s-idem", nil); len(got) != 0 {
		t.Fatalf("empty tool list must stay empty, got %d defs", len(got))
	}
}
