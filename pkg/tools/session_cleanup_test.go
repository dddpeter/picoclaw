package tools

import (
	"testing"
	"time"
)

// T6 regression: cleanup keys on the reap time (finishedAt), not StartTime.
// A long-running task that exited moments ago must survive a cleanup sweep
// — deleting it minutes after exit would contradict the documented
// "keep output for 30 minutes after it finishes" promise.
func TestCleanupOldSessionsKeysOnFinishedAt(t *testing.T) {
	sm := NewSessionManager()
	t.Cleanup(sm.Stop)

	now := time.Now()
	old := now.Add(-2 * time.Hour).Unix()
	recentExit := now.Add(-time.Minute).Unix()

	staleDone := &ProcessSession{ID: "stale-done", Status: "done", StartTime: old, finishedAt: old}
	longTaskJustExited := &ProcessSession{ID: "long-just-exited", Status: "done", StartTime: old, finishedAt: recentExit}
	stillRunning := &ProcessSession{ID: "still-running", Status: "running", StartTime: old}
	doneNoFinishedAt := &ProcessSession{ID: "done-no-finishedat", Status: "done", StartTime: old}
	for _, s := range []*ProcessSession{staleDone, longTaskJustExited, stillRunning, doneNoFinishedAt} {
		sm.Add(s)
	}

	sm.cleanupOldSessions()

	for _, id := range []string{"long-just-exited", "still-running"} {
		if _, err := sm.Get(id); err != nil {
			t.Errorf("session %s must survive cleanup", id)
		}
	}
	for _, id := range []string{"stale-done"} {
		if _, err := sm.Get(id); err == nil {
			t.Errorf("session %s (done 2h ago) must be cleaned up", id)
		}
	}
	// Sessions that never recorded a reap (legacy/crashed) fall back to
	// StartTime, matching the old behavior.
	if _, err := sm.Get("done-no-finishedat"); err == nil {
		t.Errorf("done session with old StartTime and no finishedAt must be cleaned up")
	}
}
