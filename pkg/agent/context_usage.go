package agent

import (
	"strings"

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

	// System message tokens: uses EstimateSystemTokens which mirrors
	// the full system message composition in BuildMessages (static prompt,
	// dynamic context, active skills, summary with wrapping prefix).
	// The segment split (octop borrowing §五) separates the base prompt
	// from the active-skills block; pass nil for active skills — skills are
	// only injected when the user explicitly activates them via /use, which
	// is rare. Using nil matches the common case and avoids over-counting.
	systemTokens := 0
	skillsTokens := 0
	if agent.ContextBuilder != nil {
		summary := agent.Sessions.GetSummary(sessionKey)
		systemTokens, skillsTokens = agent.ContextBuilder.estimateSystemTokenSegments(summary, nil)
	}

	// Tool definition tokens, split by origin (octop borrowing §五):
	// mcp_* tools → mcp; the subagent spawn set → subagents; everything
	// else → tool_definitions. MCP bloat becomes visible at a glance.
	toolTokens := 0
	toolSeg, mcpSeg, subagentSeg := 0, 0, 0
	if agent.Tools != nil {
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
