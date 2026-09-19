package agent

import (
	"testing"
	"time"
)

// scheduleCompact is the async enqueue helper extracted from Finalize.
// Tests here pin its gating rules independently of the full pipeline.

// TestScheduleCompactGating verifies when scheduleCompact enqueues work:
// nil context manager, NoHistory, and EnableSummary=false must all be no-ops.
func TestScheduleCompactGating(t *testing.T) {
	cases := []struct {
		name        string
		fcm         ContextManager
		noHistory   bool
		enableSum   bool
		wantEnqueue bool
	}{
		{"nil manager", nil, false, true, false},
		{"no history", newFakeContextManagerForCompact(), true, true, false},
		{"summary disabled", newFakeContextManagerForCompact(), false, false, false},
		{"normal", newFakeContextManagerForCompact(), false, true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pipeline := newCompactTestPipeline(t, tc.fcm)
			ts := newCompactTurnState(t, pipeline.al, "session-gating",
				processOptions{EnableSummary: tc.enableSum, NoHistory: tc.noHistory})
			budget := pipeline.al.registry.GetDefaultAgent().ContextWindow

			pipeline.al.scheduleCompact(ts.sessionKey, budget, ts.opts)

			var fcm *fakeContextManagerForCompact
			if tc.fcm != nil {
				fcm = tc.fcm.(*fakeContextManagerForCompact)
			}
			want := tc.wantEnqueue
			waitForCondition(t, time.Second, func() bool {
				got := fcm.callCount()
				if want {
					return got >= 1
				}
				return got == 0
			})
		})
	}
}

// TestScheduleCompactDedup pins the per-session in-flight guard: while a
// compaction is running for a session, further schedules for the same
// session are skipped; once it completes, the next schedule runs again.
// This keeps consecutive turn endings (auto-continue segments) from piling
// up compactions, and prevents concurrent Compact calls on one session.
func TestScheduleCompactDedup(t *testing.T) {
	fcm := newFakeContextManagerForCompact()
	fcm.block = make(chan struct{})
	t.Cleanup(fcm.release)

	pipeline := newCompactTestPipeline(t, fcm)
	ts := newCompactTurnState(t, pipeline.al, "session-dedup",
		processOptions{EnableSummary: true})
	budget := pipeline.al.registry.GetDefaultAgent().ContextWindow

	// Two schedules while the first is blocked: only one may run.
	pipeline.al.scheduleCompact(ts.sessionKey, budget, ts.opts)
	pipeline.al.scheduleCompact(ts.sessionKey, budget, ts.opts)

	select {
	case <-fcm.start:
	case <-time.After(2 * time.Second):
		t.Fatal("first scheduleCompact never called Compact")
	}
	time.Sleep(100 * time.Millisecond)
	if got := fcm.callCount(); got != 1 {
		t.Fatalf("in-flight session compacted twice, callCount = %d, want 1", got)
	}

	// After release, the next schedule must run again.
	fcm.release()
	waitForCondition(t, time.Second, func() bool {
		pipeline.al.scheduleCompact(ts.sessionKey, budget, ts.opts)
		return fcm.callCount() >= 2
	})
}
