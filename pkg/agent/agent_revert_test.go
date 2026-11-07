package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
	runtimeevents "github.com/sipeed/picoclaw/pkg/events"
	"github.com/sipeed/picoclaw/pkg/snapshot"
)

func newTestRevertLoop(t *testing.T) (*AgentLoop, *snapshot.Store, string) {
	t.Helper()
	t.Setenv(config.EnvHome, t.TempDir())
	store, err := snapshot.NewStore(filepath.Join(config.GetHome(), "checkpoints"), snapshot.Config{})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	al := &AgentLoop{checkpointStore: store}
	al.runtimeEvents = runtimeevents.NewBus()
	return al, store, t.TempDir()
}

func TestRevertCommand_DisabledStore(t *testing.T) {
	al := &AgentLoop{}
	al.runtimeEvents = runtimeevents.NewBus()
	got := al.executeRevertCommand("sess-1", "default", "pico", "pico:1", "")
	if !strings.Contains(got, "disabled") {
		t.Fatalf("reply = %q, want disabled hint", got)
	}
}

func TestRevertCommand_ListAndRevert(t *testing.T) {
	al, store, ws := newTestRevertLoop(t)

	file := filepath.Join(ws, "f.txt")
	if err := os.WriteFile(file, []byte("original"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	tc := store.BeginTurn("sess-1", "t1", ws)
	store.CaptureFile(tc, file, "f.txt")
	if err := os.WriteFile(file, []byte("rewritten"), 0o644); err != nil {
		t.Fatalf("modify: %v", err)
	}
	if _, err := store.CommitTurn(tc); err != nil {
		t.Fatalf("CommitTurn: %v", err)
	}

	list := al.executeRevertCommand("sess-1", "default", "pico", "pico:1", "list")
	if !strings.Contains(list, "Workspace versions") || !strings.Contains(list, "/revert <id>") {
		t.Fatalf("list reply = %q", list)
	}

	reply := al.executeRevertCommand("sess-1", "default", "pico", "pico:1", "")
	if !strings.Contains(reply, "Reverted 1 turn") || !strings.Contains(reply, "1 file(s) restored") {
		t.Fatalf("revert reply = %q", reply)
	}
	got, err := os.ReadFile(file)
	if err != nil || string(got) != "original" {
		t.Fatalf("file = %q, %v; want byte-exact original", got, err)
	}

	empty := al.executeRevertCommand("sess-1", "default", "pico", "pico:1", "")
	if !strings.Contains(empty, "nothing to revert") {
		t.Fatalf("exhausted reply = %q", empty)
	}
}

// TestRevertRefusesWhenTurnActive pins the busy guard: while the session has
// a live (or placeholder) turn, restores are refused; the read-only list
// stays available.
func TestRevertRefusesWhenTurnActive(t *testing.T) {
	al, store, ws := newTestRevertLoop(t)

	file := filepath.Join(ws, "f.txt")
	if err := os.WriteFile(file, []byte("original"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	tc := store.BeginTurn("sess-1", "t1", ws)
	store.CaptureFile(tc, file, "f.txt")
	if err := os.WriteFile(file, []byte("rewritten"), 0o644); err != nil {
		t.Fatalf("modify: %v", err)
	}
	if _, err := store.CommitTurn(tc); err != nil {
		t.Fatalf("CommitTurn: %v", err)
	}

	// Placeholder registration mirrors the pump's LoadOrStore claim.
	al.activeTurnStates.Store("sess-1", &turnState{turnID: makePendingTurnID("sess-1", 1)})

	busy := al.executeRevertCommand("sess-1", "default", "pico", "pico:1", "")
	if !strings.Contains(busy, "while a turn is active") {
		t.Fatalf("busy reply = %q", busy)
	}
	list := al.executeRevertCommand("sess-1", "default", "pico", "pico:1", "list")
	if !strings.Contains(list, "Workspace versions") {
		t.Fatalf("list must stay available while busy, got %q", list)
	}

	// After the turn state clears, the revert goes through.
	al.activeTurnStates.Delete("sess-1")
	reply := al.executeRevertCommand("sess-1", "default", "pico", "pico:1", "")
	if !strings.Contains(reply, "Reverted 1 turn") {
		t.Fatalf("post-turn revert reply = %q", reply)
	}
}

func TestRevertNotifierInvoked(t *testing.T) {
	al, store, ws := newTestRevertLoop(t)

	file := filepath.Join(ws, "f.txt")
	if err := os.WriteFile(file, []byte("original"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	tc := store.BeginTurn("sess-1", "t1", ws)
	store.CaptureFile(tc, file, "f.txt")
	if err := os.WriteFile(file, []byte("rewritten"), 0o644); err != nil {
		t.Fatalf("modify: %v", err)
	}
	if _, err := store.CommitTurn(tc); err != nil {
		t.Fatalf("CommitTurn: %v", err)
	}

	type call struct {
		channel, chatID, sessionKey string
		restored                    int
	}
	calls := make(chan call, 1)
	al.SetCheckpointNotifier(func(channel, chatID, sessionKey string, result *snapshot.RevertResult) {
		calls <- call{channel, chatID, sessionKey, result.Restored}
	})

	if reply := al.executeRevertCommand("sess-1", "default", "pico", "pico:1", ""); !strings.Contains(reply, "Reverted") {
		t.Fatalf("revert reply = %q", reply)
	}
	select {
	case c := <-calls:
		if c.channel != "pico" || c.chatID != "pico:1" || c.sessionKey != "sess-1" || c.restored != 1 {
			t.Fatalf("notifier call = %+v", c)
		}
	default:
		t.Fatal("checkpoint notifier was not invoked")
	}
}
