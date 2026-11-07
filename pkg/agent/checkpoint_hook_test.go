package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	runtimeevents "github.com/sipeed/picoclaw/pkg/events"
	"github.com/sipeed/picoclaw/pkg/snapshot"
)

func newTestCheckpointHook(t *testing.T, restrict bool) (*CheckpointHook, *snapshot.Store, string) {
	t.Helper()
	return newTestCheckpointHookCfg(t, restrict, config.CheckpointConfig{})
}

func newTestCheckpointHookCfg(t *testing.T, restrict bool, cfg config.CheckpointConfig) (*CheckpointHook, *snapshot.Store, string) {
	t.Helper()
	t.Setenv(config.EnvHome, t.TempDir()) // isolate ~/.picoclaw (checkpoint root)
	ws := t.TempDir()
	store, err := snapshot.NewStore(filepath.Join(config.GetHome(), "checkpoints"), snapshot.Config{
		MaxFileSize: cfg.MaxFileSize, TrackExec: cfg.TrackExec,
		MaxVersions: cfg.MaxVersions, MaxTotalBytes: cfg.MaxTotalBytes,
		ScanDepth: cfg.ScanDepth, ScanMaxFiles: cfg.ScanMaxFiles,
	})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	hook := newCheckpointHook(store, cfg, func(string) string { return ws }, restrict, nil)
	return hook, store, ws
}

func turnEvent(kind runtimeevents.Kind, sessionKey, turnID string) runtimeevents.Event {
	return runtimeevents.Event{
		Kind: kind,
		Scope: runtimeevents.Scope{
			AgentID:    "default",
			SessionKey: sessionKey,
			TurnID:     turnID,
		},
	}
}

func hookCall(tool, path string) *ToolCallHookRequest {
	return &ToolCallHookRequest{
		Meta:      HookMeta{AgentID: "default", SessionKey: "sess-1", TurnID: "t1"},
		Tool:      tool,
		Arguments: map[string]any{"path": path, "content": "new content"},
	}
}

func versionCount(t *testing.T, s *snapshot.Store, sessionKey string) int {
	t.Helper()
	versions, err := s.Versions(sessionKey)
	if err != nil {
		t.Fatalf("Versions: %v", err)
	}
	return len(versions)
}

func TestCheckpointHook_CapturesTurnWrites(t *testing.T) {
	hook, store, ws := newTestCheckpointHook(t, false)
	ctx := context.Background()

	existing := filepath.Join(ws, "f.txt")
	if err := os.WriteFile(existing, []byte("original"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	created := filepath.Join(ws, "g.txt")

	hook.OnRuntimeEvent(ctx, turnEvent(runtimeevents.KindAgentTurnStart, "sess-1", "t1"))

	if _, _, err := hook.BeforeTool(ctx, hookCall("write_file", existing)); err != nil {
		t.Fatalf("BeforeTool write_file: %v", err)
	}
	if err := os.WriteFile(existing, []byte("new content"), 0o644); err != nil {
		t.Fatalf("simulate write: %v", err)
	}
	if _, _, err := hook.BeforeTool(ctx, hookCall("append_file", created)); err != nil {
		t.Fatalf("BeforeTool append_file: %v", err)
	}
	if err := os.WriteFile(created, []byte("new content"), 0o644); err != nil {
		t.Fatalf("simulate create: %v", err)
	}

	hook.OnRuntimeEvent(ctx, turnEvent(runtimeevents.KindAgentTurnEnd, "sess-1", "t1"))

	versions, err := store.Versions("sess-1")
	if err != nil || len(versions) != 1 {
		t.Fatalf("Versions() = %d, %v; want 1", len(versions), err)
	}
	v := versions[0]
	if len(v.Files) != 2 {
		t.Fatalf("version files = %d, want 2", len(v.Files))
	}
	var sawModified, sawCreated bool
	for _, f := range v.Files {
		switch f.Op {
		case snapshot.OpModified:
			sawModified = true
			if !f.Stored {
				t.Fatalf("modified entry not stored: %+v", f)
			}
			pre, err := os.ReadFile(filepath.Join(store.PreRoot("sess-1"), f.SHA256+".bin"))
			if err != nil {
				t.Fatalf("read pre-image: %v", err)
			}
			if string(pre) != "original" {
				t.Fatalf("pre-image = %q, want original", pre)
			}
		case snapshot.OpCreated:
			sawCreated = true
		}
	}
	if !sawModified || !sawCreated {
		t.Fatalf("want one modified + one created entry, got %+v", v.Files)
	}
}

func TestCheckpointHook_ReadOnlyTurnCommitsNothing(t *testing.T) {
	hook, store, ws := newTestCheckpointHook(t, false)
	ctx := context.Background()

	hook.OnRuntimeEvent(ctx, turnEvent(runtimeevents.KindAgentTurnStart, "sess-1", "t1"))
	// Read-only and unknown tools never capture.
	if _, _, err := hook.BeforeTool(ctx, hookCall("read_file", filepath.Join(ws, "f.txt"))); err != nil {
		t.Fatalf("BeforeTool read_file: %v", err)
	}
	hook.OnRuntimeEvent(ctx, turnEvent(runtimeevents.KindAgentTurnEnd, "sess-1", "t1"))

	if got := versionCount(t, store, "sess-1"); got != 0 {
		t.Fatalf("read-only turn committed %d versions, want 0", got)
	}
}

// TestCheckpointHook_RespectsWorkspaceBoundary pins the snapshot boundary:
// in restrict mode a write outside the workspace produces no capture (and
// therefore can never be restored either).
func TestCheckpointHook_RespectsWorkspaceBoundary(t *testing.T) {
	hook, store, _ := newTestCheckpointHook(t, true)
	ctx := context.Background()

	outside := filepath.Join(t.TempDir(), "outside.txt")

	hook.OnRuntimeEvent(ctx, turnEvent(runtimeevents.KindAgentTurnStart, "sess-1", "t1"))
	if _, _, err := hook.BeforeTool(ctx, hookCall("write_file", outside)); err != nil {
		t.Fatalf("BeforeTool: %v", err)
	}
	hook.OnRuntimeEvent(ctx, turnEvent(runtimeevents.KindAgentTurnEnd, "sess-1", "t1"))

	if got := versionCount(t, store, "sess-1"); got != 0 {
		t.Fatalf("out-of-workspace capture committed %d versions, want 0", got)
	}
}

// TestCheckpointHook_StaleCaptureCommittedByNextTurn covers the wedged-turn
// recovery: a turn that never saw turn_end has its already-captured
// observations committed when the next turn opens its own capture.
func TestCheckpointHook_StaleCaptureCommittedByNextTurn(t *testing.T) {
	hook, store, ws := newTestCheckpointHook(t, false)
	ctx := context.Background()

	fileA := filepath.Join(ws, "a.txt")
	if err := os.WriteFile(fileA, []byte("a-old"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	hook.OnRuntimeEvent(ctx, turnEvent(runtimeevents.KindAgentTurnStart, "sess-1", "t1"))
	hook.BeforeTool(ctx, hookCall("write_file", fileA))
	if err := os.WriteFile(fileA, []byte("a-new"), 0o644); err != nil {
		t.Fatalf("simulate write: %v", err)
	}
	// Turn t1 wedges; no turn_end ever arrives.

	fileB := filepath.Join(ws, "b.txt")
	hook.OnRuntimeEvent(ctx, turnEvent(runtimeevents.KindAgentTurnStart, "sess-1", "t2"))
	hook.BeforeTool(ctx, &ToolCallHookRequest{
		Meta:      HookMeta{AgentID: "default", SessionKey: "sess-1", TurnID: "t2"},
		Tool:      "write_file",
		Arguments: map[string]any{"path": fileB, "content": "b"},
	})

	versions, err := store.Versions("sess-1")
	if err != nil || len(versions) != 1 {
		t.Fatalf("stale capture must commit when the next turn starts, got %d versions, %v", len(versions), err)
	}
	if versions[0].TurnID != "t1" {
		t.Fatalf("stale version turn_id = %q, want t1", versions[0].TurnID)
	}

	if err := os.WriteFile(fileB, []byte("b"), 0o644); err != nil {
		t.Fatalf("simulate write: %v", err)
	}
	hook.OnRuntimeEvent(ctx, turnEvent(runtimeevents.KindAgentTurnEnd, "sess-1", "t2"))

	versions, _ = store.Versions("sess-1")
	if len(versions) != 2 || versions[1].TurnID != "t2" {
		t.Fatalf("want t1+t2 versions, got %+v", versions)
	}
}

// TestCheckpointHook_LateTurnEndIgnored covers the reverse hazard: a stale
// turn_end from a wedged turn arriving after the next turn opened its
// capture must not commit the newer turn's partial capture.
func TestCheckpointHook_LateTurnEndIgnored(t *testing.T) {
	hook, store, _ := newTestCheckpointHook(t, false)
	ctx := context.Background()

	hook.OnRuntimeEvent(ctx, turnEvent(runtimeevents.KindAgentTurnStart, "sess-1", "t1"))
	fileA := filepath.Join(t.TempDir(), "a.txt")
	hook.BeforeTool(ctx, hookCall("write_file", fileA))
	if err := os.WriteFile(fileA, []byte("new content"), 0o644); err != nil {
		t.Fatalf("simulate write: %v", err)
	}
	hook.OnRuntimeEvent(ctx, turnEvent(runtimeevents.KindAgentTurnEnd, "sess-1", "t1"))

	hook.OnRuntimeEvent(ctx, turnEvent(runtimeevents.KindAgentTurnStart, "sess-1", "t2"))
	fileB := filepath.Join(t.TempDir(), "b.txt")
	hook.BeforeTool(ctx, &ToolCallHookRequest{
		Meta:      HookMeta{AgentID: "default", SessionKey: "sess-1", TurnID: "t2"},
		Tool:      "write_file",
		Arguments: map[string]any{"path": fileB, "content": "b"},
	})
	if err := os.WriteFile(fileB, []byte("b"), 0o644); err != nil {
		t.Fatalf("simulate write: %v", err)
	}

	// Late duplicate turn_end from t1: must be ignored.
	hook.OnRuntimeEvent(ctx, turnEvent(runtimeevents.KindAgentTurnEnd, "sess-1", "t1"))
	if got := versionCount(t, store, "sess-1"); got != 1 {
		t.Fatalf("late turn_end committed %d extra version(s), want 1 total", got)
	}

	hook.OnRuntimeEvent(ctx, turnEvent(runtimeevents.KindAgentTurnEnd, "sess-1", "t2"))
	if got := versionCount(t, store, "sess-1"); got != 2 {
		t.Fatalf("want 2 versions after t2 ends, got %d", got)
	}
}

// TestCheckpointHook_MountWiring pins the agent_init wiring: the hook and
// store are mounted only when tools.checkpoint.enabled is true, and the
// store root follows PICOCLAW_HOME.
func TestCheckpointHook_MountWiring(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())

	cfg := config.DefaultConfig()
	al := NewAgentLoop(cfg, bus.NewMessageBus(), &mockProvider{})
	if al.CheckpointStore() != nil {
		t.Fatal("checkpoint store must be nil while tools.checkpoint is disabled")
	}

	cfg.Tools.Checkpoint.Enabled = true
	al2 := NewAgentLoop(cfg, bus.NewMessageBus(), &mockProvider{})
	if al2.CheckpointStore() == nil {
		t.Fatal("checkpoint store must be mounted when tools.checkpoint.enabled=true")
	}
	if _, err := os.Stat(filepath.Join(config.GetHome(), "checkpoints")); err != nil {
		t.Fatalf("store root not created under PICOCLAW_HOME: %v", err)
	}
}

// TestCheckpointHook_ExecScanWiring pins the exec coverage wiring: a run
// action triggers pre/post scans around the tool call and everything the
// run did to the workspace is committed as part of the turn's version.
func TestCheckpointHook_ExecScanWiring(t *testing.T) {
	hook, store, ws := newTestCheckpointHookCfg(t, false, config.CheckpointConfig{TrackExec: true, Enabled: true})
	ctx := context.Background()

	existing := filepath.Join(ws, "f.txt")
	if err := os.WriteFile(existing, []byte("pre-exec"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	hook.OnRuntimeEvent(ctx, turnEvent(runtimeevents.KindAgentTurnStart, "sess-1", "t1"))

	execReq := &ToolCallHookRequest{
		Meta:      HookMeta{AgentID: "default", SessionKey: "sess-1", TurnID: "t1"},
		Tool:      "exec",
		Arguments: map[string]any{"action": "run", "command": "echo hi"},
	}
	if _, _, err := hook.BeforeTool(ctx, execReq); err != nil {
		t.Fatalf("BeforeTool exec: %v", err)
	}

	// The exec run's filesystem effects.
	if err := os.WriteFile(existing, []byte("post-exec"), 0o644); err != nil {
		t.Fatalf("modify: %v", err)
	}
	created := filepath.Join(ws, "out.txt")
	if err := os.WriteFile(created, []byte("output"), 0o644); err != nil {
		t.Fatalf("create: %v", err)
	}

	hook.AfterTool(ctx, &ToolResultHookResponse{
		Meta:      HookMeta{AgentID: "default", SessionKey: "sess-1", TurnID: "t1"},
		Tool:      "exec",
		Arguments: execReq.Arguments,
	})
	hook.OnRuntimeEvent(ctx, turnEvent(runtimeevents.KindAgentTurnEnd, "sess-1", "t1"))

	versions, err := store.Versions("sess-1")
	if err != nil || len(versions) != 1 {
		t.Fatalf("Versions() = %d, %v; want 1", len(versions), err)
	}
	v := versions[0]
	if !v.ExecUsed {
		t.Fatal("version must be marked exec-used")
	}
	ops := map[string]snapshot.Op{}
	for _, f := range v.Files {
		ops[filepath.Base(f.Path)] = f.Op
	}
	if ops["f.txt"] != snapshot.OpModified || ops["out.txt"] != snapshot.OpCreated {
		t.Fatalf("ops = %v, want f.txt modified + out.txt created", ops)
	}

	// Non-run actions never scan.
	hook.OnRuntimeEvent(ctx, turnEvent(runtimeevents.KindAgentTurnStart, "sess-1", "t2"))
	hook.BeforeTool(ctx, &ToolCallHookRequest{
		Meta:      HookMeta{AgentID: "default", SessionKey: "sess-1", TurnID: "t2"},
		Tool:      "exec",
		Arguments: map[string]any{"action": "list"},
	})
	hook.AfterTool(ctx, &ToolResultHookResponse{
		Meta:      HookMeta{AgentID: "default", SessionKey: "sess-1", TurnID: "t2"},
		Tool:      "exec",
		Arguments: map[string]any{"action": "list"},
	})
	hook.OnRuntimeEvent(ctx, turnEvent(runtimeevents.KindAgentTurnEnd, "sess-1", "t2"))
	if versions, _ := store.Versions("sess-1"); len(versions) != 1 {
		t.Fatalf("non-run exec must not commit, got %d versions", len(versions))
	}
}
