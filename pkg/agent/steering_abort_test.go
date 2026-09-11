package agent

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/tools"
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

// --- 2026-09-11 review defects: responsive-abort wipe, zombie clobber, duplicate tool results ---

// TestRunTurn_HardAbortDuringLLMCall_PreservesHistory: the COMMON /stop path
// (aborting a responsive turn mid-LLM-call) used to wipe the session: the
// goroutine unwinds normally through abortTurn → restoreSession, whose
// restore point was captured BEFORE the user message was persisted — on a
// fresh session that is an empty history, i.e. the "0-byte jsonl" data loss.
func TestRunTurn_HardAbortDuringLLMCall_PreservesHistory(t *testing.T) {
	al, agent, cleanup := newTurnCoordTestLoop(t, &slowMockProvider{delay: 30 * time.Second})
	defer cleanup()

	const sessionKey = "test-session-responsive-abort"
	opts := makeTestProcessOpts(sessionKey)
	opts.Dispatch.SessionKey = sessionKey
	opts.Dispatch.UserMessage = "please verify agent-browser"
	pipeline := NewPipeline(al)
	ts := newTurnState(agent, opts, turnEventScope{
		turnID:  "turn-responsive-abort",
		context: newTurnContext(nil, nil, nil),
	})

	done := make(chan struct{})
	go func() {
		al.runTurn(context.Background(), ts, pipeline)
		close(done)
	}()

	// Wait until the turn actually started (user message persisted).
	startDeadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(startDeadline) {
		if len(agent.Sessions.GetHistory(sessionKey)) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(agent.Sessions.GetHistory(sessionKey)) == 0 {
		t.Fatal("turn did not start within 5s")
	}

	if err := al.HardAbort(sessionKey); err != nil {
		t.Fatalf("HardAbort failed: %v", err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("runTurn did not unwind after abort")
	}

	history := agent.Sessions.GetHistory(sessionKey)
	if len(history) == 0 {
		t.Fatal("defect reproduced: responsive /stop wiped the session history — restoreSession rolled back to a snapshot taken before the user message")
	}
	if history[0].Role != "user" {
		t.Fatalf("expected the user message to survive the abort, first role=%s", history[0].Role)
	}
}

// TestZombieTurn_LateUnwindPreservesNewTurnHistory: after the abort watchdog
// force-releases a wedged turn's registration, a new turn may claim the
// session. If the zombie goroutine eventually unwinds, its abort cleanup
// must not roll the session back over the new turn's records.
func TestZombieTurn_LateUnwindPreservesNewTurnHistory(t *testing.T) {
	oldGrace := hardAbortUnwindGrace
	hardAbortUnwindGrace = 300 * time.Millisecond
	defer func() { hardAbortUnwindGrace = oldGrace }()

	al, agent, cleanup := newTurnCoordTestLoop(t, &simpleConvProvider{})
	defer cleanup()

	const sessionKey = "test-session-zombie-late"
	opts := makeTestProcessOpts(sessionKey)
	opts.Dispatch.SessionKey = sessionKey
	ts := newTurnState(agent, opts, turnEventScope{
		turnID:  "turn-zombie-late",
		context: newTurnContext(nil, nil, nil),
	})
	al.registerActiveTurn(ts)

	if err := al.HardAbort(sessionKey); err != nil {
		t.Fatalf("HardAbort failed: %v", err)
	}

	releaseDeadline := time.Now().Add(3 * time.Second)
	for al.getActiveTurnState(sessionKey) != nil && time.Now().Before(releaseDeadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if al.getActiveTurnState(sessionKey) != nil {
		t.Fatal("watchdog did not force-release the wedged turn")
	}

	// A new turn claims the session and persists a message.
	agent.Sessions.AddMessage(sessionKey, "user", "new turn message")

	// The zombie goroutine finally unwinds and runs its abort cleanup.
	al.abortTurn(ts)

	history := agent.Sessions.GetHistory(sessionKey)
	found := false
	for _, msg := range history {
		if msg.Content == "new turn message" {
			found = true
		}
	}
	if !found {
		t.Fatalf("defect reproduced: late zombie unwind wiped the new turn's history (history now %d messages)", len(history))
	}
}

type alwaysToolCallProvider struct{}

func (p *alwaysToolCallProvider) Chat(
	_ context.Context,
	_ []providers.Message,
	_ []providers.ToolDefinition,
	_ string,
	_ map[string]any,
) (*providers.LLMResponse, error) {
	return &providers.LLMResponse{
		ToolCalls: []providers.ToolCall{{
			ID:       "call-block",
			Type:     "function",
			Function: &providers.FunctionCall{Name: "block_tool", Arguments: "{}"},
		}},
		FinishReason: "tool_calls",
	}, nil
}

func (p *alwaysToolCallProvider) GetDefaultModel() string { return "tool-model" }

type ctxBlockingTool struct {
	startedOnce sync.Once
	started     chan struct{}
}

func (t *ctxBlockingTool) Name() string        { return "block_tool" }
func (t *ctxBlockingTool) Description() string { return "blocks until the context is canceled" }
func (t *ctxBlockingTool) Parameters() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": true}
}

func (t *ctxBlockingTool) Execute(ctx context.Context, _ map[string]any) *tools.ToolResult {
	t.startedOnce.Do(func() { close(t.started) })
	<-ctx.Done()
	return tools.SilentResult("late real tool result")
}

// TestRunTurn_HardAbortMidTool_LateResultNotDuplicated: HardAbort seals the
// dangling tool call immediately; the in-flight tool's real result returns
// after the seal and must NOT be persisted — otherwise the session holds two
// tool messages for the same tool_call_id and the next LLM request can 400.
func TestRunTurn_HardAbortMidTool_LateResultNotDuplicated(t *testing.T) {
	al, agent, cleanup := newTurnCoordTestLoop(t, &alwaysToolCallProvider{})
	defer cleanup()

	blockingTool := &ctxBlockingTool{started: make(chan struct{})}
	al.RegisterTool(blockingTool)

	const sessionKey = "test-session-midtool-abort"
	opts := makeTestProcessOpts(sessionKey)
	opts.Dispatch.SessionKey = sessionKey
	opts.Dispatch.UserMessage = "run the blocking tool"
	pipeline := NewPipeline(al)
	ts := newTurnState(agent, opts, turnEventScope{
		turnID:  "turn-midtool-abort",
		context: newTurnContext(nil, nil, nil),
	})

	done := make(chan struct{})
	go func() {
		al.runTurn(context.Background(), ts, pipeline)
		close(done)
	}()

	select {
	case <-blockingTool.started:
	case <-time.After(5 * time.Second):
		t.Fatal("tool never started within 5s")
	}

	if err := al.HardAbort(sessionKey); err != nil {
		t.Fatalf("HardAbort failed: %v", err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("runTurn did not unwind after abort")
	}

	history := agent.Sessions.GetHistory(sessionKey)
	toolCount := 0
	toolContent := ""
	for _, msg := range history {
		if msg.Role == "tool" && msg.ToolCallID == "call-block" {
			toolCount++
			toolContent = msg.Content
		}
	}
	if toolCount != 1 {
		t.Fatalf("defect reproduced: expected exactly one tool message for call-block (the sealed synthetic), got %d", toolCount)
	}
	if toolContent != abortedToolResultNote {
		t.Fatalf("expected the synthetic seal content, got %q", toolContent)
	}
}
