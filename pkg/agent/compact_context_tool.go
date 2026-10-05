package agent

import (
	"context"
	"fmt"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/tools"
)

// CompactContextTool gives the model an initiative path into context
// compaction (fork, agentscope-go borrowing §二,
// docs/design/agentscope-go-borrowing-analysis.zh.md).
//
// Design invariants:
//   - The tool NEVER rewrites history from inside tool execution — mid-turn
//     history mutation is exactly what the split-turn mechanism avoids. It
//     only raises a flag that the existing iteration-boundary checkpoint
//     (compactBeforeLLMCall) consumes before the next LLM call, reusing the
//     same Compact→Assemble path instead of adding a seventh compaction
//     mechanism.
//   - Honesty principle (borrowed from agentscope-go's compress_context):
//     the result must state plainly whether anything was compacted. Claiming
//     compression when nothing happened teaches the model that details it can
//     no longer see are still in context.
//   - The threshold sits at half the automatic usage gate: the automatic path
//     already fires at the full threshold, so an identical tool threshold
//     would leave the tool nothing to do.
type CompactContextTool struct{}

// NewCompactContextTool returns the model-initiated compaction tool. It is
// stateless per se; per-turn state lives on the turnState it reaches via
// TurnStateFromContext.
func NewCompactContextTool() *CompactContextTool { return &CompactContextTool{} }

// compactContextToolMaxUses bounds the tool per turn so a confused model
// cannot loop request→compact→request.
const compactContextToolMaxUses = 2

func (t *CompactContextTool) Name() string { return "compact_context" }

func (t *CompactContextTool) Description() string {
	return `Request context compaction early, on your own initiative.

Use when a task has run many tool iterations and the conversation has grown long, and you want to free context BEFORE hitting the window limit — for example when a long task is only half done and you want to keep working without losing the thread.

Parameters:
- reason (optional): why compaction is needed now (for logs).

Behavior:
- If history usage is still below the tool's threshold, nothing is compacted and the result says so honestly.
- Otherwise compaction runs before your next model call: older messages are replaced by a structured summary. If the compaction attempt fails, nothing is summarized and the context stays unchanged.
- Details that were summarized stay recoverable with the short_expand tool — re-read them there if you need exact content again.
- Usable at most 2 times per turn; every call counts toward that limit, including no-op calls below the threshold.`
}

func (t *CompactContextTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"reason": map[string]any{
				"type":        "string",
				"description": "Why compression is needed now (optional, for logs)",
			},
		},
	}
}

func (t *CompactContextTool) Execute(ctx context.Context, args map[string]any) *tools.ToolResult {
	ts := TurnStateFromContext(ctx)
	if ts == nil || ts.agent == nil {
		return tools.ErrorResult("compact_context is only available inside an active agent turn.")
	}

	if ts.compactContextToolUses.Add(1) > compactContextToolMaxUses {
		return tools.SilentResult(fmt.Sprintf(
			"compact_context has already been used %d times this turn; automatic compaction still runs whenever the context approaches the window. Do not call it again this turn.",
			compactContextToolMaxUses))
	}

	usage := computeContextUsage(ts.agent, ts.sessionKey)
	if usage == nil {
		return tools.SilentResult(
			"No compaction was performed: current context usage could not be estimated for this session. Nothing was summarized and no detail was lost.")
	}
	ratio, threshold := compactContextUsageRatio(ts.agent, usage)
	if ratio < threshold {
		return tools.SilentResult(fmt.Sprintf(
			"No compaction was performed: estimated history usage is about %.0f%% of the effective context window, below this tool's %.0f%% threshold. Nothing was summarized and no detail was lost.",
			ratio*100, threshold*100))
	}

	ts.compactContextRequested.Store(true)
	return tools.SilentResult(fmt.Sprintf(
		"Compaction requested: history usage is about %.0f%% of the effective window (tool threshold %.0f%%). Compaction will run before your next model call and summarize older messages; if the attempt fails, nothing is summarized and the context stays unchanged. Summarized details stay recoverable via the short_expand tool — re-read them there if you need exact content again.",
		ratio*100, threshold*100))
}

// compactContextUsageRatio returns the history-usage ratio on the same basis
// as shouldCompactNow (HistoryTokens over window minus the output reserve)
// plus the tool's trigger threshold. ratio is 0 when usage is unavailable.
func compactContextUsageRatio(agent *AgentInstance, usage *bus.ContextUsage) (ratio, threshold float64) {
	threshold = agent.CompactToolTriggerRatio
	if threshold <= 0 {
		threshold = 0.375
	}
	if usage == nil {
		return 0, threshold
	}
	window := agent.ContextWindow - agent.MaxTokens
	if window <= 0 {
		window = agent.ContextWindow
	}
	if window <= 0 {
		return 0, threshold
	}
	return float64(usage.HistoryTokens) / float64(window), threshold
}
