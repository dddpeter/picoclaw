package agent

import (
	"context"
	"sync"
	"testing"
	"time"
)

// Tests for async post-publish compaction (option A): Compact must not run
// synchronously on the finalize critical path. Option C (LeafChunkTokens
// 20000→8000) is pinned separately in pkg/seahorse/short_constants_test.go.

// fakeContextManagerForCompact implements the full ContextManager interface,
// recording Compact calls so tests can assert on when and how the pipeline
// triggers compaction.
type fakeContextManagerForCompact struct {
	mu          sync.Mutex
	calls       []CompactRequest
	block       chan struct{} // if non-nil, Compact blocks until released
	start       chan struct{} // closed when Compact begins executing
	releaseOnce sync.Once
}

func newFakeContextManagerForCompact() *fakeContextManagerForCompact {
	return &fakeContextManagerForCompact{
		start: make(chan struct{}),
	}
}

func (f *fakeContextManagerForCompact) Assemble(ctx context.Context, req *AssembleRequest) (*AssembleResponse, error) {
	return &AssembleResponse{}, nil
}

func (f *fakeContextManagerForCompact) Compact(ctx context.Context, req *CompactRequest) error {
	if f == nil || req == nil {
		return nil
	}
	f.mu.Lock()
	f.calls = append(f.calls, *req)
	f.mu.Unlock()

	if f.block != nil {
		// Signal start then block until the test releases us.
		select {
		case <-f.start:
		default:
			close(f.start)
		}
		<-f.block
	}
	return nil
}

func (f *fakeContextManagerForCompact) Ingest(ctx context.Context, req *IngestRequest) error {
	return nil
}

func (f *fakeContextManagerForCompact) Clear(ctx context.Context, sessionKey string) error {
	return nil
}

func (f *fakeContextManagerForCompact) callCount() int {
	if f == nil {
		return 0
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// release unblocks a blocked Compact exactly once; safe to call from both
// the test body and t.Cleanup.
func (f *fakeContextManagerForCompact) release() {
	f.releaseOnce.Do(func() {
		if f.block != nil {
			close(f.block)
		}
	})
}

func (f *fakeContextManagerForCompact) lastCall() (CompactRequest, bool) {
	if f == nil {
		return CompactRequest{}, false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) == 0 {
		return CompactRequest{}, false
	}
	return f.calls[len(f.calls)-1], true
}

// newCompactTestPipeline wires an agent loop whose context manager is the
// fake compactor.
func newCompactTestPipeline(t *testing.T, fcm ContextManager) *Pipeline {
	t.Helper()
	al := newLegacyTestAgentLoop(t, &summarizingRecordingProvider{response: "unused"})
	al.contextManager = fcm
	return NewPipeline(al)
}

// newCompactTurnState sets the session key on opts.Dispatch — the field
// newTurnState actually reads into ts.sessionKey (opts.SessionKey alone is
// NOT sufficient).
func newCompactTurnState(t *testing.T, al *AgentLoop, sessionKey string, opts processOptions) *turnState {
	t.Helper()
	agent := al.registry.GetDefaultAgent()
	if agent == nil {
		t.Fatal("expected default agent")
	}
	opts.SessionKey = sessionKey
	opts.Dispatch.SessionKey = sessionKey
	return newTurnState(agent, opts, al.newTurnEventScope(agent.ID, sessionKey, nil))
}

// TestFinalize_CompactAsync verifies option A: Finalize must enqueue
// compaction asynchronously and return immediately — Compact must NOT block
// Finalize.
func TestFinalize_CompactAsync(t *testing.T) {
	fcm := newFakeContextManagerForCompact()
	fcm.block = make(chan struct{})
	t.Cleanup(fcm.release)

	pipeline := newCompactTestPipeline(t, fcm)
	ts := newCompactTurnState(t, pipeline.al, "session-async", processOptions{EnableSummary: true})
	exec := &turnExecution{llmModelName: "test-model"}

	done := make(chan struct{})
	var finalizeErr error
	go func() {
		defer close(done)
		_, finalizeErr = pipeline.Finalize(context.Background(), context.Background(), ts, exec,
			TurnEndStatusCompleted, "async answer")
	}()

	// Compact must start but must NOT block Finalize: Finalize returns while
	// the compaction is still in flight.
	select {
	case <-fcm.start:
		// Compaction started; Finalize must still be able to complete.
	case <-time.After(2 * time.Second):
		t.Fatal("Compact was never called by Finalize")
	}

	select {
	case <-done:
		// Finalize returned while compaction still blocked in flight: async ✓
	case <-time.After(2 * time.Second):
		t.Fatal("Finalize blocked until Compact finished; compaction must be async (option A)")
	}
	fcm.release()

	if finalizeErr != nil {
		t.Fatalf("Finalize failed: %v", finalizeErr)
	}
	req, ok := fcm.lastCall()
	if !ok {
		t.Fatal("expected a Compact call")
	}
	if req.SessionKey != "session-async" {
		t.Fatalf("unexpected session key %q", req.SessionKey)
	}
	if req.Reason != ContextCompressReasonSummarize {
		t.Fatalf("unexpected reason %q", req.Reason)
	}
	if req.Budget != pipeline.al.registry.GetDefaultAgent().ContextWindow {
		t.Fatalf("unexpected budget %d", req.Budget)
	}
}

// TestFinalize_CompactSkippedWhenNoHistory pins the heartbeat behavior:
// NoHistory turns must not trigger post-turn compaction.
func TestFinalize_CompactSkippedWhenNoHistory(t *testing.T) {
	fcm := newFakeContextManagerForCompact()
	pipeline := newCompactTestPipeline(t, fcm)
	ts := newCompactTurnState(t, pipeline.al, "session-nohist",
		processOptions{EnableSummary: true, NoHistory: true})
	exec := &turnExecution{llmModelName: "test-model"}

	if _, err := pipeline.Finalize(context.Background(), context.Background(), ts, exec,
		TurnEndStatusCompleted, "heartbeat answer"); err != nil {
		t.Fatalf("Finalize failed: %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	if got := fcm.callCount(); got != 0 {
		t.Fatalf("NoHistory turn must not compact, got %d calls", got)
	}
}

// TestFinalize_CompactSkippedWhenSummaryDisabled covers processOptions with
// EnableSummary=false (e.g. background task turns): no post-turn compaction.
func TestFinalize_CompactSkippedWhenSummaryDisabled(t *testing.T) {
	fcm := newFakeContextManagerForCompact()
	pipeline := newCompactTestPipeline(t, fcm)
	ts := newCompactTurnState(t, pipeline.al, "session-nosummary",
		processOptions{EnableSummary: false})
	exec := &turnExecution{llmModelName: "test-model"}

	if _, err := pipeline.Finalize(context.Background(), context.Background(), ts, exec,
		TurnEndStatusCompleted, "bg answer"); err != nil {
		t.Fatalf("Finalize failed: %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	if got := fcm.callCount(); got != 0 {
		t.Fatalf("EnableSummary=false turn must not compact, got %d calls", got)
	}
}

// TestFinalize_CompactNilContextManagerSafe: a nil context manager (compaction
// disabled) must not panic Finalize.
func TestFinalize_CompactNilContextManagerSafe(t *testing.T) {
	pipeline := newCompactTestPipeline(t, nil)
	ts := newCompactTurnState(t, pipeline.al, "session-nilcm",
		processOptions{EnableSummary: true})
	exec := &turnExecution{llmModelName: "test-model"}

	if _, err := pipeline.Finalize(context.Background(), context.Background(), ts, exec,
		TurnEndStatusCompleted, "answer"); err != nil {
		t.Fatalf("Finalize failed: %v", err)
	}
}

// TestFinalize_CompactErrorNonFatal: the turn must complete normally
// regardless of what Compact does — compaction is best effort.
func TestFinalize_CompactErrorNonFatal(t *testing.T) {
	fcm := newFakeContextManagerForCompact()
	pipeline := newCompactTestPipeline(t, fcm)
	ts := newCompactTurnState(t, pipeline.al, "session-cmperr",
		processOptions{EnableSummary: true})
	exec := &turnExecution{llmModelName: "test-model"}

	result, err := pipeline.Finalize(context.Background(), context.Background(), ts, exec,
		TurnEndStatusCompleted, "answer")
	if err != nil {
		t.Fatalf("Finalize failed: %v", err)
	}
	if result.status != TurnEndStatusCompleted {
		t.Fatalf("async compaction must not fail the turn, got %q", result.status)
	}
	if got := fcm.callCount(); got != 1 {
		t.Fatalf("expected exactly 1 Compact call, got %d", got)
	}
}

// TestFinalize_CompactAllResponsesHandledPath pins the tool-delivered path
// (allResponsesHandled=true): Finalize's early-return branch must NOT
// schedule compaction — ExecuteTools' tool-satisfied branch already did it
// for this turn (mirroring HEAD behavior); scheduling again here would
// double-compact every tool-delivered turn.
func TestFinalize_CompactAllResponsesHandledPath(t *testing.T) {
	fcm := newFakeContextManagerForCompact()
	pipeline := newCompactTestPipeline(t, fcm)
	ts := newCompactTurnState(t, pipeline.al, "session-toolhandled",
		processOptions{EnableSummary: true})
	exec := &turnExecution{
		allResponsesHandled: true,
		llmModelName:        "test-model",
	}

	result, err := pipeline.Finalize(context.Background(), context.Background(), ts, exec,
		TurnEndStatusCompleted, "")
	if err != nil {
		t.Fatalf("Finalize failed: %v", err)
	}
	if result.status != TurnEndStatusCompleted {
		t.Fatalf("expected completed, got %q", result.status)
	}
	time.Sleep(50 * time.Millisecond)
	if got := fcm.callCount(); got != 0 {
		t.Fatalf("allResponsesHandled early return must not re-compact, got %d calls", got)
	}
}
