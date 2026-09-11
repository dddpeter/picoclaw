package agent

import (
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/providers"
)

func assistantWithToolCalls(ids ...string) providers.Message {
	calls := make([]providers.ToolCall, 0, len(ids))
	for _, id := range ids {
		calls = append(calls, providers.ToolCall{
			ID:       id,
			Type:     "function",
			Function: &providers.FunctionCall{Name: "exec"},
		})
	}
	return providers.Message{Role: "assistant", ToolCalls: calls}
}

func toolResultFor(id string) providers.Message {
	return providers.Message{Role: "tool", ToolCallID: id, Content: "ok"}
}

func TestSealDanglingToolCalls(t *testing.T) {
	t.Run("appends synthetic results for unanswered trailing tool calls", func(t *testing.T) {
		history := []providers.Message{
			{Role: "user", Content: "run two things"},
			assistantWithToolCalls("call-1", "call-2"),
			toolResultFor("call-1"),
		}

		sealed := sealDanglingToolCalls(history)

		if len(sealed) != len(history)+1 {
			t.Fatalf("expected one synthetic result appended, got %d messages (want %d)", len(sealed), len(history)+1)
		}
		last := sealed[len(sealed)-1]
		if last.Role != "tool" || last.ToolCallID != "call-2" {
			t.Fatalf("expected synthetic tool result for call-2, got role=%s id=%s", last.Role, last.ToolCallID)
		}
		if strings.TrimSpace(last.Content) == "" {
			t.Fatal("synthetic tool result must carry non-empty content for the next LLM request")
		}
	})

	t.Run("seals every call when no results at all", func(t *testing.T) {
		history := []providers.Message{
			{Role: "user", Content: "run"},
			assistantWithToolCalls("a", "b", "c"),
		}

		sealed := sealDanglingToolCalls(history)
		if len(sealed) != len(history)+3 {
			t.Fatalf("expected three synthetic results, got %d messages", len(sealed))
		}
		got := map[string]bool{}
		for _, msg := range sealed[len(history):] {
			if msg.Role != "tool" {
				t.Fatalf("expected only tool messages appended, got %s", msg.Role)
			}
			got[msg.ToolCallID] = true
		}
		for _, id := range []string{"a", "b", "c"} {
			if !got[id] {
				t.Fatalf("missing synthetic result for %s", id)
			}
		}
	})

	t.Run("no-op when every tool call already has a result", func(t *testing.T) {
		history := []providers.Message{
			{Role: "user", Content: "run"},
			assistantWithToolCalls("call-1"),
			toolResultFor("call-1"),
		}
		sealed := sealDanglingToolCalls(history)
		if len(sealed) != len(history) {
			t.Fatalf("expected unchanged history, got %d messages (want %d)", len(sealed), len(history))
		}
	})

	t.Run("no-op when trailing assistant has no tool calls", func(t *testing.T) {
		history := []providers.Message{
			{Role: "user", Content: "hi"},
			{Role: "assistant", Content: "hello"},
		}
		sealed := sealDanglingToolCalls(history)
		if len(sealed) != len(history) {
			t.Fatalf("expected unchanged history, got %d messages (want %d)", len(sealed), len(history))
		}
	})
}

// TestHardAbort_ForceReleasesWedgedTurnRegistration: when the aborted turn's
// goroutine never unwinds (the Windows exec-pipe deadlock — the goroutine is
// stuck inside a tool call), its activeTurnStates registration used to stay
// forever, so every new message queued behind a dead worker and only a
// gateway restart recovered. HardAbort must force-release the registration
// after a grace period.
func TestHardAbort_ForceReleasesWedgedTurnRegistration(t *testing.T) {
	oldGrace := hardAbortUnwindGrace
	hardAbortUnwindGrace = 300 * time.Millisecond
	defer func() { hardAbortUnwindGrace = oldGrace }()

	al, agent, cleanup := newTurnCoordTestLoop(t, &simpleConvProvider{})
	defer cleanup()

	const sessionKey = "test-session-zombie"
	opts := makeTestProcessOpts(sessionKey)
	opts.Dispatch.SessionKey = sessionKey
	ts := newTurnState(agent, opts, turnEventScope{
		turnID:  "turn-zombie",
		context: newTurnContext(nil, nil, nil),
	})
	// Simulate a wedged runTurn: it registered the turn, but the goroutine
	// never returns, so the deferred clearActiveTurn never runs.
	al.registerActiveTurn(ts)

	if err := al.HardAbort(sessionKey); err != nil {
		t.Fatalf("HardAbort failed: %v", err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for al.getActiveTurnState(sessionKey) != nil && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if al.getActiveTurnState(sessionKey) != nil {
		t.Fatal("deadlock reproduced: HardAbort left the wedged turn registered — the session stays busy forever and only a restart recovers")
	}
}
