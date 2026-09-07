package agent

import (
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
)

func defaultLoopDetectionForTest() config.LoopDetectionConfig {
	d := &config.AgentDefaults{}
	return d.EffectiveLoopDetection()
}

func TestTurnHealth_BashRetryFiresOnIdenticalFailures(t *testing.T) {
	h := newTurnHealth(defaultLoopDetectionForTest())

	errMsg := "exit status 1\nmain.go:12: undefined: Foo"

	// Two identical failures: accumulating, no warning yet.
	if w := h.note("exec", "go build ./...", errMsg, true); w != "" {
		t.Fatalf("premature warning on first failure: %q", w)
	}
	if w := h.note("exec", "go build ./...", errMsg, true); w != "" {
		t.Fatalf("premature warning on second failure: %q", w)
	}
	// Third identical failure fires and resets.
	w := h.note("exec", "go build ./...", errMsg, true)
	if !strings.Contains(w, "bash_retry") || !strings.Contains(w, "go build") {
		t.Fatalf("want bash_retry warning naming the command, got %q", w)
	}
	// Reset means the streak must rebuild from scratch.
	if w := h.note("exec", "go build ./...", errMsg, true); w != "" {
		t.Fatalf("detector should reset after firing, got %q", w)
	}
}

func TestTurnHealth_BashRetryVolatilePartsIgnored(t *testing.T) {
	h := newTurnHealth(defaultLoopDetectionForTest())

	h.note("exec", "test /tmp/abc123/run.sh", "took 12ms\nexit 1", true)
	h.note("exec", "test /tmp/xyz789/run.sh", "took 45ms\nexit 1", true)
	w := h.note("exec", "test /tmp/qwe000/run.sh", "took 3ms\nexit 1", true)
	if !strings.Contains(w, "bash_retry") {
		t.Fatalf("temp paths/durations must not break identity, got %q", w)
	}
}

func TestTurnHealth_BashRetryDifferentOutputDoesNotFire(t *testing.T) {
	h := newTurnHealth(defaultLoopDetectionForTest())

	h.note("exec", "make", "error A", true)
	h.note("exec", "make", "error B", true)
	if w := h.note("exec", "make", "error C", true); w != "" {
		t.Fatalf("changing failure output must not count as a retry loop, got %q", w)
	}
}

func TestTurnHealth_BashRetrySuccessResets(t *testing.T) {
	h := newTurnHealth(defaultLoopDetectionForTest())

	h.note("exec", "npm test", "fail", true)
	h.note("exec", "npm test", "fail", true)
	h.note("exec", "npm test", "", false) // success breaks the streak
	if w := h.note("exec", "npm test", "fail", true); w != "" {
		t.Fatalf("success must reset the streak, got %q", w)
	}
}

func TestTurnHealth_EditStreakFires(t *testing.T) {
	h := newTurnHealth(defaultLoopDetectionForTest())

	for i := 0; i < 3; i++ {
		if w := h.note("edit_file", "", "", false); w != "" {
			t.Fatalf("premature edit warning at %d: %q", i+1, w)
		}
	}
	w := h.note("edit_file", "", "", false)
	if !strings.Contains(w, "edit_streak") {
		t.Fatalf("want edit_streak warning, got %q", w)
	}
}

func TestTurnHealth_EditStreakBrokenByOtherTool(t *testing.T) {
	h := newTurnHealth(defaultLoopDetectionForTest())

	for i := 0; i < 3; i++ {
		h.note("edit_file", "", "", false)
	}
	h.note("read_file", "", "", false) // verification breaks the streak
	if w := h.note("edit_file", "", "", false); w != "" {
		t.Fatalf("interleaved non-edit action must reset the streak, got %q", w)
	}
}

func TestTurnHealth_DisabledViaConfig(t *testing.T) {
	off := newTurnHealth(defaultLoopDetectionForTest())
	off.cfg.Enabled = boolPtr(false)

	for i := 0; i < 6; i++ {
		if w := off.note("exec", "same", "same", true); w != "" {
			t.Fatalf("disabled detector must stay silent, got %q", w)
		}
	}
}

func TestTurnStateNoteToolHealthReadsCommandArg(t *testing.T) {
	h := newTurnHealth(defaultLoopDetectionForTest())
	ts := &turnState{health: h}

	args := map[string]any{"command": "go test ./..."}
	for i := 0; i < 2; i++ {
		if w := ts.noteToolHealth("exec", args, "boom", true); w != "" {
			t.Fatalf("premature warning: %q", w)
		}
	}
	if w := ts.noteToolHealth("exec", args, "boom", true); !strings.Contains(w, "go test") {
		t.Fatalf("warning should name the command, got %q", w)
	}
}
