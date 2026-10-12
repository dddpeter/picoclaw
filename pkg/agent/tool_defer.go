// PicoClaw - Ultra-lightweight personal AI agent
// License: MIT

package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/tools"
)

// Progressive tool loading (octop borrowing §六, octop-harness
// middleware/tool_search.py): when the connected MCP tool count exceeds the
// configured threshold, not-yet-loaded MCP tools go to the LLM as
// lightweight deferred references (name + first description line + a hint
// suffix, minimal schema) and a tool_search meta-tool loads full schemas on
// demand. Loaded tools are session-sticky. A direct call to an unloaded
// deferred tool auto-loads it and answers with its schema plus a retry
// instruction instead of executing blindly (weak-model fallback).

const (
	// toolSearchToolName is the meta-tool the model calls to load full tool
	// definitions (octop's tool_search, query keywords + max_results 1-5).
	toolSearchToolName = "tool_search"

	// deferredToolHint is appended to every deferred reference so the model
	// knows what to do before using the tool.
	deferredToolHint = "[Deferred reference: call tool_search before use.]"

	// toolSearchDefaultResults / toolSearchMaxResults bound one search.
	toolSearchDefaultResults = 3
	toolSearchMaxResults     = 5

	// toolDeferMaxSessions bounds the session-sticky loaded sets (octop's
	// per-thread catalog LRU, 2048). Sessions are evicted FIFO.
	toolDeferMaxSessions = 2048
)

// toolDeferState holds per-session sets of loaded tool names.
type toolDeferState struct {
	mu       sync.Mutex
	loaded   map[string]map[string]bool
	eviction []string // session keys in insertion order (FIFO eviction)
}

func newToolDeferState() *toolDeferState {
	return &toolDeferState{loaded: make(map[string]map[string]bool)}
}

// markLoaded records toolName as fully visible for sessionKey.
func (d *toolDeferState) markLoaded(sessionKey, toolName string) {
	if d == nil || sessionKey == "" || toolName == "" {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	set, ok := d.loaded[sessionKey]
	if !ok {
		if len(d.loaded) >= toolDeferMaxSessions {
			// FIFO eviction: drop the oldest half to amortize the cost.
			drop := len(d.eviction) / 2
			for _, key := range d.eviction[:drop] {
				delete(d.loaded, key)
			}
			d.eviction = append([]string(nil), d.eviction[drop:]...)
		}
		set = make(map[string]bool)
		d.loaded[sessionKey] = set
		d.eviction = append(d.eviction, sessionKey)
	}
	set[toolName] = true
}

// isLoaded reports whether toolName is fully visible for sessionKey.
func (d *toolDeferState) isLoaded(sessionKey, toolName string) bool {
	if d == nil {
		return false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.loaded[sessionKey][toolName]
}

// deferralThreshold resolves the configured threshold: 0/absent = the
// recommended default (doc §六 "超过阈值自动启用"), negative = disabled.
func (al *AgentLoop) deferralThreshold() int {
	if al == nil || al.cfg == nil {
		return 0
	}
	switch v := al.cfg.Tools.MCP.DeferredThreshold; {
	case v < 0:
		return 0 // explicit off
	case v == 0:
		return config.DefaultMCPDeferredToolThreshold
	default:
		return v
	}
}

// applyToolDeferral rewrites the LLM-facing tool definitions for a session:
// when the MCP tool count exceeds the threshold, unloaded MCP tools become
// deferred references and the tool_search meta-tool joins the set. Loaded
// tools (session-sticky) and non-MCP tools stay verbatim.
func (al *AgentLoop) applyToolDeferral(sessionKey string, defs []providers.ToolDefinition) []providers.ToolDefinition {
	threshold := al.deferralThreshold()
	if threshold <= 0 || len(defs) == 0 {
		return defs
	}
	mcpCount := 0
	for _, d := range defs {
		if isMCPToolName(d.Function.Name) {
			mcpCount++
		}
	}
	if mcpCount <= threshold {
		return defs
	}

	out := make([]providers.ToolDefinition, 0, len(defs)+1)
	for _, d := range defs {
		if isMCPToolName(d.Function.Name) && !al.toolDefer.isLoaded(sessionKey, d.Function.Name) {
			out = append(out, deferredToolRef(d))
			continue
		}
		out = append(out, d)
	}
	out = append(out, toolSearchToolDef())
	return out
}

// deferredToolRef renders the lightweight reference the LLM sees for an
// unloaded MCP tool: name stays, description is its first line plus the
// hint suffix, schema is minimal.
func deferredToolRef(d providers.ToolDefinition) providers.ToolDefinition {
	desc := strings.TrimSpace(strings.SplitN(strings.TrimSpace(d.Function.Description), "\n", 2)[0])
	if desc == "" {
		desc = "MCP tool (full definition not loaded)"
	}
	return providers.ToolDefinition{
		Type: "function",
		Function: providers.ToolFunctionDefinition{
			Name:        d.Function.Name,
			Description: desc + "\n\n" + deferredToolHint,
			Parameters:  map[string]any{"type": "object", "properties": map[string]any{}},
		},
	}
}

// toolSearchToolDef is the meta-tool definition injected while deferral is
// active (kept out of the registry so it never appears when disabled).
func toolSearchToolDef() providers.ToolDefinition {
	return providers.ToolDefinition{
		Type: "function",
		Function: providers.ToolFunctionDefinition{
			Name: toolSearchToolName,
			Description: "Search the full tool catalog and load complete definitions for matching tools. " +
				"Use when a tool is marked \"[Deferred reference: call tool_search before use.]\". " +
				"Query with short capability keywords (English works best).",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query":       map[string]any{"type": "string", "description": "Short capability keywords"},
					"max_results": map[string]any{"type": "integer", "description": "1-5, default 3"},
				},
				"required": []string{"query"},
			},
		},
	}
}

// interceptDeferredTool runs in the tool loop before registry execution.
// It handles two cases (and reports handled=false otherwise):
//   - tool_search: match the catalog, load, and answer with full definitions;
//   - a direct call to an unloaded deferred MCP tool: auto-load it and answer
//     with its schema plus a retry instruction (never execute blindly).
func (al *AgentLoop) interceptDeferredTool(
	_ context.Context,
	sessionKey, toolName string,
	args map[string]any,
	reg *tools.ToolRegistry,
) (*tools.ToolResult, bool) {
	threshold := al.deferralThreshold()
	if threshold <= 0 || reg == nil {
		return nil, false
	}
	defs := reg.ToProviderDefs()
	mcpCount := 0
	for _, d := range defs {
		if isMCPToolName(d.Function.Name) {
			mcpCount++
		}
	}
	if mcpCount <= threshold {
		return nil, false
	}

	if toolName == toolSearchToolName {
		return al.runToolSearch(sessionKey, args, defs), true
	}
	if isMCPToolName(toolName) && !al.toolDefer.isLoaded(sessionKey, toolName) {
		for _, d := range defs {
			if d.Function.Name != toolName {
				continue
			}
			al.toolDefer.markLoaded(sessionKey, toolName)
			return tools.NewToolResult(fmt.Sprintf(
				"Tool %s was a deferred reference and has now been loaded for this session. "+
					"Its full definition:\n\n%s\n\nCall it again with arguments matching the schema above.",
				toolName, formatToolDefForSearch(d))), true
		}
	}
	return nil, false
}

// runToolSearch matches the query against names and descriptions
// (case-insensitive; name hits rank first), loads and returns up to
// max_results full definitions.
func (al *AgentLoop) runToolSearch(
	sessionKey string,
	args map[string]any,
	defs []providers.ToolDefinition,
) *tools.ToolResult {
	query := strings.ToLower(strings.TrimSpace(fmt.Sprintf("%v", args["query"])))
	if query == "" || query == "<nil>" {
		return tools.ErrorResult("tool_search requires a query")
	}
	maxResults := toolSearchDefaultResults
	switch v := args["max_results"].(type) {
	case float64:
		if int(v) > 0 {
			maxResults = min(int(v), toolSearchMaxResults)
		}
	case int:
		if v > 0 {
			maxResults = min(v, toolSearchMaxResults)
		}
	}

	type ranked struct {
		def  providers.ToolDefinition
		rank int
	}
	// Token-AND matching: every query keyword must appear in name+description
	// (case-insensitive); tools whose NAME contains any token rank first.
	queryTokens := strings.Fields(query)
	var matches []ranked
	for _, d := range defs {
		if d.Function.Name == toolSearchToolName {
			continue
		}
		name := strings.ToLower(d.Function.Name)
		desc := strings.ToLower(d.Function.Description)
		haystack := name + " " + desc
		matched, nameHit := true, false
		for _, tok := range queryTokens {
			if !strings.Contains(haystack, tok) {
				matched = false
				break
			}
			if strings.Contains(name, tok) {
				nameHit = true
			}
		}
		if !matched {
			continue
		}
		rank := 1
		if nameHit {
			rank = 0
		}
		matches = append(matches, ranked{d, rank})
	}
	// Name hits first, then description hits, both in catalog order.
	slices.SortStableFunc(matches, func(a, b ranked) int { return a.rank - b.rank })
	if len(matches) > maxResults {
		matches = matches[:maxResults]
	}
	if len(matches) == 0 {
		return tools.NewToolResult(fmt.Sprintf(
			"No tools matched %q. Try broader capability keywords.", query))
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Loaded %d tool(s) for this session:\n", len(matches))
	for _, m := range matches {
		al.toolDefer.markLoaded(sessionKey, m.def.Function.Name)
		b.WriteString("\n")
		b.WriteString(formatToolDefForSearch(m.def))
	}
	return tools.NewToolResult(b.String())
}

// formatToolDefForSearch renders one full definition for the model.
func formatToolDefForSearch(d providers.ToolDefinition) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Name: %s\nDescription: %s\n", d.Function.Name, d.Function.Description)
	if d.Function.Parameters != nil {
		if schemaJSON, err := json.Marshal(d.Function.Parameters); err == nil {
			fmt.Fprintf(&b, "Parameters: %s\n", schemaJSON)
		}
	}
	return b.String()
}

// isMCPToolName reports whether the registry tool came from an MCP server
// (integration/mcp_tool.go names them mcp_<server>_<tool>).
func isMCPToolName(name string) bool {
	return strings.HasPrefix(name, "mcp_")
}
