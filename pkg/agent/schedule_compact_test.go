package agent

import (
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/bus"
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

// TestShouldCompactNow pins the usage-gate math (pi-style raw-history
// preservation): below the threshold compaction is skipped, at/above it
// runs, and unknown usage conservatively compacts.
func TestShouldCompactNow(t *testing.T) {
	agent := &AgentInstance{ContextWindow: 100_000, MaxTokens: 8_192, CompactUsageThreshold: 0.75}
	window := 100_000 - 8_192

	cases := []struct {
		name  string
		usage *bus.ContextUsage
		want  bool
	}{
		{"nil usage compacts", nil, true},
		{"far below skips", &bus.ContextUsage{UsedTokens: window / 2}, false},
		{"just below skips", &bus.ContextUsage{UsedTokens: int(float64(window) * 0.74)}, false},
		{"at threshold compacts", &bus.ContextUsage{UsedTokens: int(float64(window) * 0.75)}, true},
		{"above compacts", &bus.ContextUsage{UsedTokens: window + 5_000}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldCompactNow(agent.CompactUsageThreshold, tc.usage, agent); got != tc.want {
				t.Fatalf("shouldCompactNow = %v, want %v", got, tc.want)
			}
		})
	}

	// Zero threshold falls back to the 0.75 default.
	agent.CompactUsageThreshold = 0
	if shouldCompactNow(0, &bus.ContextUsage{UsedTokens: window / 2}, agent) {
		t.Fatal("zero threshold must behave as the 0.75 default (skip at 50%)")
	}
}

// TestScheduleCompactUsageGateBlocksLowUsage verifies end-to-end that a
// session far below the threshold does not spawn compaction at all.
func TestScheduleCompactUsageGateBlocksLowUsage(t *testing.T) {
	fcm := newFakeContextManagerForCompact()
	pipeline := newCompactTestPipeline(t, fcm)
	// Restore the real default threshold (the helper forces it open).
	if agent := pipeline.al.registry.GetDefaultAgent(); agent != nil {
		agent.CompactUsageThreshold = 0.75
	}
	ts := newCompactTurnState(t, pipeline.al, "session-lowusage",
		processOptions{EnableSummary: true})
	budget := pipeline.al.registry.GetDefaultAgent().ContextWindow

	pipeline.al.scheduleCompact(ts.sessionKey, budget, ts.opts)
	time.Sleep(100 * time.Millisecond)
	if got := fcm.callCount(); got != 0 {
		t.Fatalf("low-usage session must not compact, got %d calls", got)
	}
}
