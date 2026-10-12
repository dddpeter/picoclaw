package commands

import (
	"context"
	"fmt"
)

// FormatCompactReply renders the /compact reply from a runManualCompact
// outcome. Shared by the command handler (idle path) and the busy-branch
// interceptor (scheduled path) so both surfaces speak identically.
func FormatCompactReply(outcome *CompactOutcome, err error) string {
	if err != nil {
		return "Compaction failed: " + err.Error()
	}
	if outcome == nil {
		return unavailableMsg
	}
	switch outcome.State {
	case CompactStateNothing:
		// Two producers share this tier: history below the command floor, and
		// engine-side floors (leaf chunk fanout, protected tail) that left
		// nothing compactable — both mean "re-run once the conversation
		// grows", so one message serves both.
		return fmt.Sprintf(
			"Nothing to compact: history has %d messages and no compactable chunk outside the protected tail.",
			outcome.MessagesBefore)
	case CompactStateScheduled:
		return "♻️ A turn is active on this session; compaction is scheduled to run when it ends."
	case CompactStateBusy:
		return "A compaction is already running for this session; try again shortly."
	case CompactStateDone:
		pct := 0
		if outcome.TotalTokens > 0 {
			pct = outcome.UsedBefore * 100 / outcome.TotalTokens
		}
		reply := fmt.Sprintf(
			"✅ Compacted: history %d → %d messages; context ~%s → ~%s of %s tokens (%d%% of window).",
			outcome.MessagesBefore, outcome.MessagesAfter,
			formatCompactTokens(outcome.UsedBefore), formatCompactTokens(outcome.UsedAfter),
			formatCompactTokens(outcome.TotalTokens), pct)
		if outcome.TokensSaved > 0 {
			reply += fmt.Sprintf(" Engine reclaimed ~%s context tokens.", formatCompactTokens(outcome.TokensSaved))
		}
		return reply + " Full transcript preserved; details stay re-readable."
	default:
		return "Compaction finished with unknown state: " + outcome.State
	}
}

// formatCompactTokens renders a token count compactly (18.2k).
func formatCompactTokens(n int) string {
	if n >= 1000 {
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	}
	return fmt.Sprintf("%d", n)
}

// compactCommand forces context compaction on the current session — the
// manual counterpart to the automatic watermarks (octop borrowing §五: the
// user can clear the window before a big task, or retry after an automatic
// compaction failure). Unlike /new it does NOT rotate the session; the full
// transcript stays on disk and compressed details remain re-readable
// (short_expand for seahorse sessions).
func compactCommand() Definition {
	return Definition{
		Name:        "compact",
		Description: "Force context compaction on the current session (keeps the session; details stay re-readable)",
		Usage:       "/compact",
		Handler: func(ctx context.Context, req Request, rt *Runtime) error {
			if rt == nil || rt.RunCompact == nil {
				return req.Reply(unavailableMsg)
			}
			outcome, err := rt.RunCompact(ctx)
			return req.Reply(FormatCompactReply(outcome, err))
		},
	}
}
