package agent

import (
	"strings"
	"sync"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/providers"
)

// subagentToolNames are the registry tools that expose subagent spawning /
// delegation; their definitions form the "subagents" context segment.
var subagentToolNames = map[string]bool{
	"spawn":        true,
	"spawn_status": true,
	"subagent":     true,
	"delegate":     true,
}

func isSubagentToolName(name string) bool {
	return subagentToolNames[name]
}

// computeContextUsage estimates current context window consumption for the
// given agent and session. Includes history, system prompt (with dynamic context,
// summary, and skills — mirroring BuildMessages composition), and tool definitions.
// The output reserve (MaxTokens) is not counted as "used" but reduces the
// effective budget, matching isOverContextBudget's compression trigger:
//
//	compress when: history + system + tools + maxTokens > contextWindow
//	equivalent to: history + system + tools > contextWindow - maxTokens
//
// Returns nil when the agent or session is unavailable.
func computeContextUsage(agent *AgentInstance, sessionKey string) *bus.ContextUsage {
	if agent == nil || agent.Sessions == nil {
		return nil
	}
	contextWindow := agent.ContextWindow
	if contextWindow <= 0 {
		return nil
	}

	// History tokens
	history := agent.Sessions.GetHistory(sessionKey)
	historyTokens := 0
	for _, m := range history {
		historyTokens += EstimateMessageTokens(m)
	}

	// System message tokens: estimateSystemTokenSegments mirrors the full
	// system message composition in BuildMessages (static prompt, dynamic
	// context, active skills, summary with wrapping prefix).
	// Skills segment (review P1): agents with a persistent skills filter
	// (agents.*.skills / frontmatter) get the "# Active Skills" block
	// injected EVERY turn (turn_state.activeSkills), so passing nil kept the
	// segment at a permanent zero and under-counted used. ForcedSkills (/use)
	// are rare and turn-scoped — the persistent filter is the honest
	// steady-state view.
	systemTokens := 0
	skillsTokens := 0
	if agent.ContextBuilder != nil {
		summary := agent.Sessions.GetSummary(sessionKey)
		systemTokens, skillsTokens = agent.ContextBuilder.estimateSystemTokenSegments(summary, agent.SkillsFilter)
	}

	// Tool definition tokens, split by origin (octop borrowing §五):
	// mcp_* tools → mcp; the subagent spawn set → subagents; everything
	// else → tool_definitions. MCP bloat becomes visible at a glance.
	// Segment base (review P1, §六-4): the split mirrors the LAST ASSEMBLED
	// OUTBOUND request for this session (recorded by the LLM pipeline after
	// profile filtering + deferred-tool loading) — the full registry the old
	// code walked counts every unloaded MCP schema (often 10k-30k tokens)
	// even when §六 progressive loading serves deferred references, so the
	// watermarks fired at a third of the real window. Falls back to the
	// full-registry estimate before a session's first outbound call.
	toolTokens := 0
	toolSeg, mcpSeg, subagentSeg := 0, 0, 0
	if cached, ok := agent.cachedOutboundToolSegments(sessionKey); ok {
		toolTokens, toolSeg, mcpSeg, subagentSeg = cached.total, cached.tool, cached.mcp, cached.subagents
	} else if agent.Tools != nil {
		for _, def := range agent.Tools.ToProviderDefs() {
			tokens := EstimateToolDefsTokens([]providers.ToolDefinition{def})
			toolTokens += tokens
			switch {
			case strings.HasPrefix(def.Function.Name, "mcp_"):
				mcpSeg += tokens
			case isSubagentToolName(def.Function.Name):
				subagentSeg += tokens
			default:
				toolSeg += tokens
			}
		}
	}

	// Used = history + system (includes summary) + tools
	usedTokens := historyTokens + systemTokens + toolTokens

	// Total reported to UIs is the DECLARED window (configured or derived,
	// before the 256k sanity clamp, then min-capped by the active model
	// entry's context_window): Feishu cards, pico/web usage, /context
	// and /status all render this as the model's context size, which should
	// reflect what the model advertises — not the fork's conservative
	// compaction bound the gates run on. Fall back to the (possibly clamped)
	// ContextWindow for agents hand-built without the declared field.
	totalTokens := agent.DeclaredContextWindow
	if totalTokens <= 0 {
		totalTokens = contextWindow
	}

	// Effective budget = contextWindow minus output reserve (maxTokens)
	effectiveWindow := contextWindow - agent.MaxTokens
	if effectiveWindow < 0 {
		effectiveWindow = contextWindow
	}

	// compressAt = effectiveWindow: aligns with isOverContextBudget's
	// proactive trigger (msgTokens + toolTokens + maxTokens > contextWindow).
	compressAt := effectiveWindow

	// summarizeAt = soft summarization trigger: matches maybeSummarize's
	// threshold (contextWindow * SummarizeTokenPercent / 100).
	//
	// The engine compares this against history-message tokens ONLY (not
	// UsedTokens).  HistoryTokens is exposed alongside UsedTokens so the
	// UI can show both values and avoid user confusion.
	summarizeAt := contextWindow * agent.SummarizeTokenPercent / 100
	if summarizeAt <= 0 {
		summarizeAt = compressAt
	}

	usedPercent := 0
	if compressAt > 0 {
		usedPercent = usedTokens * 100 / compressAt
	}
	if usedPercent > 100 {
		usedPercent = 100
	}

	return &bus.ContextUsage{
		UsedTokens:        usedTokens,
		TotalTokens:       totalTokens,
		HistoryTokens:     historyTokens,
		CompressAtTokens:  compressAt,
		SummarizeAtTokens: summarizeAt,
		UsedPercent:       usedPercent,
		Segments: []bus.ContextUsageSegment{
			{Key: "system_prompt", Tokens: systemTokens},
			{Key: "skills", Tokens: skillsTokens},
			{Key: "tool_definitions", Tokens: toolSeg},
			{Key: "mcp", Tokens: mcpSeg},
			{Key: "subagents", Tokens: subagentSeg},
			{Key: "conversation", Tokens: historyTokens},
		},
	}
}

// outboundToolSegments is the per-origin token split of one session's last
// assembled outbound tool-definition set (review P1, octop §六-4): mirrors
// what the model actually sees after profile filtering, deferred-tool
// references and meta-tool injection — not the full registry.
type outboundToolSegments struct {
	tool      int // builtin + meta tools (tool_definitions segment)
	mcp       int // mcp_* tools, deferred references included at their served size
	subagents int // the subagent spawn set
	total     int // sum of the above
}

// outboundToolSegCache is the lazily-shared per-agent cache of outbound
// tool-def token splits (see AgentInstance.outboundSegs).
type outboundToolSegCache struct {
	mu   sync.Mutex
	segs map[string]outboundToolSegments
}

// outboundSegCacheBound bounds the per-session segment cache (FIFO by
// insertion order, same discipline as toolDeferState's session LRU).
const outboundSegCacheBound = 256

// recordOutboundToolSegments caches the tool-def token split of the request
// being assembled. Called from the LLM pipeline right after the defs are
// settled (exec.toolDefTokens), so every later computeContextUsage snapshot
// for the session reflects the REAL outbound cost of deferred loading.
func (a *AgentInstance) recordOutboundToolSegments(sessionKey string, defs []providers.ToolDefinition) {
	if a == nil || sessionKey == "" || len(defs) == 0 {
		return
	}
	seg := outboundToolSegments{}
	for _, def := range defs {
		tokens := EstimateToolDefsTokens([]providers.ToolDefinition{def})
		seg.total += tokens
		switch {
		case strings.HasPrefix(def.Function.Name, "mcp_"):
			seg.mcp += tokens
		case isSubagentToolName(def.Function.Name):
			seg.subagents += tokens
		default:
			seg.tool += tokens
		}
	}
	if a.outboundSegs == nil {
		// Hand-built agent (tests): no cache — snapshots fall back to the
		// full-registry estimate.
		return
	}
	c := a.outboundSegs
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.segs == nil {
		c.segs = make(map[string]outboundToolSegments, 8)
	} else if len(c.segs) >= outboundSegCacheBound {
		if _, exists := c.segs[sessionKey]; !exists {
			for key := range c.segs {
				delete(c.segs, key)
				break // drop one arbitrary victim; bounded by map iteration cost
			}
		}
	}
	c.segs[sessionKey] = seg
}

// cachedOutboundToolSegments returns the recorded split for a session, if
// any. A false return means the session has not assembled an outbound
// request yet (or the cache was evicted) — callers fall back to the
// conservative full-registry estimate.
func (a *AgentInstance) cachedOutboundToolSegments(sessionKey string) (outboundToolSegments, bool) {
	if a == nil || sessionKey == "" {
		return outboundToolSegments{}, false
	}
	if a.outboundSegs == nil {
		return outboundToolSegments{}, false
	}
	c := a.outboundSegs
	c.mu.Lock()
	defer c.mu.Unlock()
	seg, ok := c.segs[sessionKey]
	return seg, ok
}

// snapshotContextUsage is computeContextUsage under the model-state read
// lock, for callers running outside a live turn (idle compaction scanner,
// /context command): ContextWindow/DeclaredContextWindow mutate when
// swapAgentModelLocked re-caps the per-model window. In-turn callers must
// keep using computeContextUsage directly — a nested RLock from the
// read-locked turn goroutine can deadlock against Close()'s blocking write
// lock while it waits for the turn to drain.
func (a *AgentInstance) snapshotContextUsage(sessionKey string) *bus.ContextUsage {
	if a == nil {
		return nil
	}
	mu := a.modelStateMutex()
	mu.RLock()
	defer mu.RUnlock()
	return computeContextUsage(a, sessionKey)
}
