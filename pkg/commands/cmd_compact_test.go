package commands

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// /compact (octop borrowing §五): force context compaction on the current
// session — the manual counterpart to the automatic watermarks. The command
// is a thin formatter over the agent-side RunCompact outcome.
func TestCompactCommand(t *testing.T) {
	replyOf := func(t *testing.T, rt *Runtime) string {
		t.Helper()
		var reply string
		res := NewExecutor(NewRegistry(BuiltinDefinitions()), rt).Execute(context.Background(), Request{
			Text: "/compact",
			Reply: func(text string) error {
				reply = text
				return nil
			},
		})
		if res.Outcome != OutcomeHandled {
			t.Fatalf("/compact must be handled, got %+v", res)
		}
		if res.Err != nil {
			t.Fatalf("/compact execute error: %v", res.Err)
		}
		return reply
	}

	t.Run("unavailable without runtime hook", func(t *testing.T) {
		if got := replyOf(t, &Runtime{}); got != unavailableMsg {
			t.Fatalf("want unavailable message, got %q", got)
		}
	})

	t.Run("done reports before/after stats", func(t *testing.T) {
		rt := &Runtime{RunCompact: func(context.Context) (*CompactOutcome, error) {
			return &CompactOutcome{
				State:            CompactStateDone,
				MessagesBefore:   42,
				MessagesAfter:    12,
				UsedBefore:       18_200,
				UsedAfter:        9_400,
				TotalTokens:      32_000,
				CompressAtTokens: 24_000,
			}, nil
		}}
		reply := replyOf(t, rt)
		for _, want := range []string{"42", "12", "18.2k", "9.4k", "56%", "32.0k"} {
			if !strings.Contains(reply, want) {
				t.Fatalf("done reply %q must mention %q", reply, want)
			}
		}
	})

	t.Run("nothing to compact", func(t *testing.T) {
		rt := &Runtime{RunCompact: func(context.Context) (*CompactOutcome, error) {
			return &CompactOutcome{State: CompactStateNothing, MessagesBefore: 2}, nil
		}}
		reply := replyOf(t, rt)
		if !strings.Contains(reply, "Nothing to compact") {
			t.Fatalf("nothing reply = %q", reply)
		}
	})

	t.Run("scheduled when turn active", func(t *testing.T) {
		rt := &Runtime{RunCompact: func(context.Context) (*CompactOutcome, error) {
			return &CompactOutcome{State: CompactStateScheduled}, nil
		}}
		reply := replyOf(t, rt)
		if !strings.Contains(reply, "scheduled") {
			t.Fatalf("scheduled reply = %q", reply)
		}
	})

	t.Run("busy when a compaction is already running", func(t *testing.T) {
		rt := &Runtime{RunCompact: func(context.Context) (*CompactOutcome, error) {
			return &CompactOutcome{State: CompactStateBusy}, nil
		}}
		reply := replyOf(t, rt)
		if !strings.Contains(reply, "already") {
			t.Fatalf("busy reply = %q", reply)
		}
	})

	t.Run("error surfaces", func(t *testing.T) {
		rt := &Runtime{RunCompact: func(context.Context) (*CompactOutcome, error) {
			return nil, fmt.Errorf("engine exploded")
		}}
		reply := replyOf(t, rt)
		if !strings.Contains(reply, "engine exploded") {
			t.Fatalf("error reply = %q", reply)
		}
	})
}
