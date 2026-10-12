package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/commands"
	"github.com/sipeed/picoclaw/pkg/providers"
)

// seedCompactHistory fills a session with count messages.
func seedCompactHistory(t *testing.T, al *AgentLoop, agent *AgentInstance, sessionKey string, count int) {
	t.Helper()
	for i := range count {
		agent.Sessions.AddFullMessage(sessionKey, providers.Message{
			Role:    "user",
			Content: "消息内容" + string(rune('a'+i%26)) + strings.Repeat("Padding", 8),
		})
	}
	if err := agent.Sessions.Save(sessionKey); err != nil {
		t.Fatalf("Save: %v", err)
	}
}

// TestRunManualCompact_Nothing pins the nothing_to_compact tier: a session
// without enough history reports instead of running a pointless summary
// (octop's MIN_SUMMARIZE_MESSAGES guard against "summarized 1 message" no-ops).
func TestRunManualCompact_Nothing(t *testing.T) {
	store := newIdleTestStore(t)
	cm := &countingContextManager{}
	al, agent := newIdleTestLoop(t, store, cm, 2) // only 2 messages seeded
	seedCompactHistory(t, al, agent, "s1", 0)     // keep the 2 from the helper

	outcome, err := al.runManualCompact(context.Background(), agent, "s1")
	if err != nil {
		t.Fatalf("runManualCompact: %v", err)
	}
	if outcome.State != commands.CompactStateNothing {
		t.Fatalf("state = %q, want nothing_to_compact", outcome.State)
	}
	if cm.callCount() != 0 {
		t.Fatalf("nothing tier must not compact, calls=%d", cm.callCount())
	}
}

// TestRunManualCompact_IdleRunsSync pins the idle tier: /compact runs
// synchronously with the manual reason and reports before/after stats.
func TestRunManualCompact_IdleRunsSync(t *testing.T) {
	store := newIdleTestStore(t)
	cm := &countingContextManager{}
	al, agent := newIdleTestLoop(t, store, cm, 20)
	seedCompactHistory(t, al, agent, "s1", 0) // 20 messages from the helper

	outcome, err := al.runManualCompact(context.Background(), agent, "s1")
	if err != nil {
		t.Fatalf("runManualCompact: %v", err)
	}
	if outcome.State != commands.CompactStateDone {
		t.Fatalf("state = %q, want done (outcome=%+v)", outcome.State, outcome)
	}
	if cm.callCount() != 1 {
		t.Fatalf("idle tier must compact exactly once, calls=%d", cm.callCount())
	}
	if got := cm.calls[0].Reason; got != ContextCompressReasonManual {
		t.Fatalf("compact reason = %q, want %q", got, ContextCompressReasonManual)
	}
	if got := cm.calls[0].SessionKey; got != "s1" {
		t.Fatalf("compact session = %q, want s1", got)
	}
	if outcome.MessagesBefore < 20 || outcome.TotalTokens <= 0 || outcome.UsedBefore <= 0 {
		t.Fatalf("done outcome must carry before stats, got %+v", outcome)
	}
}

// TestRunManualCompact_TurnActiveSchedules pins the busy-session tier:
// /compact during an active turn must NOT block or run inline — it goes
// through the scheduler to run when the turn ends (non-blocking semantics,
// aligned with /new and /switch).
func TestRunManualCompact_TurnActiveSchedules(t *testing.T) {
	store := newIdleTestStore(t)
	cm := &countingContextManager{fireCh: make(chan struct{}, 8)}
	al, agent := newIdleTestLoop(t, store, cm, 20)

	// Forge an active turn for s1 (placeholder shape, as the pump claims it).
	al.activeTurnStates.Store("s1", &turnState{
		turnID:     makePendingTurnID("s1", 1),
		sessionKey: "s1",
		phase:      TurnPhaseSetup,
	})

	outcome, err := al.runManualCompact(context.Background(), agent, "s1")
	if err != nil {
		t.Fatalf("runManualCompact: %v", err)
	}
	if outcome.State != commands.CompactStateScheduled {
		t.Fatalf("state = %q, want scheduled", outcome.State)
	}
	// The compaction itself runs asynchronously through the scheduler.
	select {
	case <-cm.fireCh:
	case <-time.After(3 * time.Second):
		t.Fatal("scheduled compaction never ran")
	}
	if cm.callCount() != 1 {
		t.Fatalf("scheduled tier must compact exactly once, calls=%d", cm.callCount())
	}
	if got := cm.calls[0].Reason; got != ContextCompressReasonManual {
		t.Fatalf("scheduled reason = %q, want %q", got, ContextCompressReasonManual)
	}
}

// TestRunManualCompact_BusyWhenInFlight pins the dedup tier: a compaction
// already running for the session reports busy instead of piling up.
func TestRunManualCompact_BusyWhenInFlight(t *testing.T) {
	store := newIdleTestStore(t)
	cm := &countingContextManager{fireCh: make(chan struct{}, 8)}
	al, agent := newIdleTestLoop(t, store, cm, 20)

	al.compactScheduler.inFlight.Store("s1", struct{}{})
	defer al.compactScheduler.inFlight.Delete("s1")

	outcome, err := al.runManualCompact(context.Background(), agent, "s1")
	if err != nil {
		t.Fatalf("runManualCompact: %v", err)
	}
	if outcome.State != commands.CompactStateBusy {
		t.Fatalf("state = %q, want busy", outcome.State)
	}
	if cm.callCount() != 0 {
		t.Fatalf("busy tier must not compact, calls=%d", cm.callCount())
	}
}

// TestTryHandleCompactCommand pins the busy-branch interception: /compact
// typed while a turn is active is consumed with a scheduled reply instead
// of leaking into the steering queue (the /stop precedent).
func TestTryHandleCompactCommand(t *testing.T) {
	store := newIdleTestStore(t)
	cm := &countingContextManager{fireCh: make(chan struct{}, 8)}
	al, _ := newIdleTestLoop(t, store, cm, 20)

	al.activeTurnStates.Store("s1", &turnState{
		turnID:     makePendingTurnID("s1", 1),
		sessionKey: "s1",
		phase:      TurnPhaseSetup,
	})

	if !al.tryHandleCompactCommand(context.Background(),
		bus.InboundMessage{Channel: "feishu", ChatID: "c", Content: "/compact"}, "s1") {
		t.Fatal("/compact during an active turn must be consumed")
	}
	select {
	case <-cm.fireCh:
	case <-time.After(3 * time.Second):
		t.Fatal("scheduled compaction never ran")
	}

	// Not ours: other commands and plain text fall through.
	if al.tryHandleCompactCommand(context.Background(),
		bus.InboundMessage{Channel: "feishu", ChatID: "c", Content: "/status"}, "s1") {
		t.Fatal("/status must not be consumed by the compact interceptor")
	}
	if al.tryHandleCompactCommand(context.Background(),
		bus.InboundMessage{Channel: "feishu", ChatID: "c", Content: "compact the context please"}, "s1") {
		t.Fatal("plain text must not be consumed by the compact interceptor")
	}
}
