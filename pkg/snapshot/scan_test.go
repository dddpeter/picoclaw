package snapshot

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestExecScanDetectsCreateModifyDelete pins the exec coverage contract:
// everything an exec run does to the workspace (create / modify / delete)
// lands in the turn's version with recoverable pre-state, and a revert
// undoes all three.
func TestExecScanDetectsCreateModifyDelete(t *testing.T) {
	root := t.TempDir()
	ws := t.TempDir()

	modified := filepath.Join(ws, "mod.txt")
	deleted := filepath.Join(ws, "gone.txt")
	writeFile(t, modified, "mod-old")
	writeFile(t, deleted, "gone-old")

	s, _ := NewStore(root, Config{TrackExec: true})
	tc := s.BeginTurn("sess-1", "t1", ws)

	// --- exec run starts: pre-scan caches the pre-exec state.
	s.ExecPreScan(tc)

	// The exec run: modify one file, delete another, create a third.
	writeFile(t, modified, "mod-new")
	_ = os.Remove(deleted)
	created := filepath.Join(ws, "made.txt")
	writeFile(t, created, "made")

	// --- exec run ends: post-scan records the delta.
	s.ExecPostScan(tc)

	v, err := s.CommitTurn(tc)
	if err != nil {
		t.Fatalf("CommitTurn: %v", err)
	}
	if v == nil {
		t.Fatal("exec turn must commit a version")
	}
	if !v.ExecUsed {
		t.Fatal("ExecUsed must be set")
	}
	ops := map[string]Op{}
	stored := map[string]bool{}
	for _, f := range v.Files {
		ops[filepath.Base(f.Path)] = f.Op
		stored[filepath.Base(f.Path)] = f.Stored
	}
	if ops["mod.txt"] != OpModified || !stored["mod.txt"] {
		t.Fatalf("mod.txt = %v stored=%v, want stored modified", ops["mod.txt"], stored["mod.txt"])
	}
	if ops["gone.txt"] != OpDeleted || !stored["gone.txt"] {
		t.Fatalf("gone.txt = %v stored=%v, want stored deleted", ops["gone.txt"], stored["gone.txt"])
	}
	if ops["made.txt"] != OpCreated {
		t.Fatalf("made.txt = %v, want created", ops["made.txt"])
	}

	// Revert undoes all three.
	if _, err := s.Revert("sess-1", ""); err != nil {
		t.Fatalf("Revert: %v", err)
	}
	if got, _ := os.ReadFile(modified); string(got) != "mod-old" {
		t.Fatalf("mod.txt = %q, want mod-old", got)
	}
	if got, err := os.ReadFile(deleted); err != nil || string(got) != "gone-old" {
		t.Fatalf("gone.txt = %q, %v; want recreated gone-old", got, err)
	}
	if _, err := os.Stat(created); !os.IsNotExist(err) {
		t.Fatalf("made.txt must be removed on revert, stat err = %v", err)
	}
}

// TestExecScanSecondRunSteadyState pins the incremental contract: a run that
// changes nothing records nothing, and files changed by a previous exec are
// cached on the next pre-scan (so the next run's changes stay recoverable).
func TestExecScanSecondRunSteadyState(t *testing.T) {
	root := t.TempDir()
	ws := t.TempDir()
	file := filepath.Join(ws, "f.txt")
	writeFile(t, file, "v0")

	s, _ := NewStore(root, Config{TrackExec: true})

	// Run 1: modify f.txt.
	tc := s.BeginTurn("sess-1", "t1", ws)
	s.ExecPreScan(tc)
	writeFile(t, file, "v1")
	s.ExecPostScan(tc)
	if v, err := s.CommitTurn(tc); err != nil || v == nil {
		t.Fatalf("run1 commit = %v, %v", v, err)
	}

	// Run 2: change f.txt again (its post-exec content must have been
	// re-cached by run 2's pre-scan).
	tc2 := s.BeginTurn("sess-1", "t2", ws)
	s.ExecPreScan(tc2)
	writeFile(t, file, "v2")
	s.ExecPostScan(tc2)
	v, err := s.CommitTurn(tc2)
	if err != nil || v == nil {
		t.Fatalf("run2 commit = %v, %v", v, err)
	}
	entry := findEntry(t, v, file)
	pre, _ := os.ReadFile(filepath.Join(s.preDir("sess-1"), entry.SHA256+".bin"))
	if string(pre) != "v1" {
		t.Fatalf("run2 pre-image = %q, want v1 (the run-2 baseline)", pre)
	}

	// Run 3: nothing changes → no version.
	tc3 := s.BeginTurn("sess-1", "t3", ws)
	s.ExecPreScan(tc3)
	s.ExecPostScan(tc3)
	v3, err := s.CommitTurn(tc3)
	if err != nil {
		t.Fatalf("run3 commit: %v", err)
	}
	if v3 != nil {
		t.Fatalf("steady-state run must not commit, got %+v", v3)
	}
}

// TestExecScanPreExistingDeletionNotAttributed pins the attribution
// contract: a file deleted OUTSIDE any exec run (between turns) must not be
// recorded as deleted by the next exec.
func TestExecScanPreExistingDeletionNotAttributed(t *testing.T) {
	root := t.TempDir()
	ws := t.TempDir()
	file := filepath.Join(ws, "f.txt")
	writeFile(t, file, "v0")

	s, _ := NewStore(root, Config{TrackExec: true})

	// Run 1 establishes the index.
	tc := s.BeginTurn("sess-1", "t1", ws)
	s.ExecPreScan(tc)
	s.ExecPostScan(tc)
	if _, err := s.CommitTurn(tc); err != nil {
		t.Fatalf("commit1: %v", err)
	}

	// The user deletes the file outside any turn.
	_ = os.Remove(file)

	// Run 2 does something unrelated (nothing): the deletion must not be
	// attributed to it.
	tc2 := s.BeginTurn("sess-1", "t2", ws)
	s.ExecPreScan(tc2)
	s.ExecPostScan(tc2)
	v, err := s.CommitTurn(tc2)
	if err != nil {
		t.Fatalf("commit2: %v", err)
	}
	if v != nil {
		t.Fatalf("external deletion must not be attributed, got %+v", v.Files)
	}
}

// TestExecScanOversizeHonest pins the cache budget degradation: files above
// max_file_size are walked (stat-only) and their exec changes are recorded
// unstored with a reason.
func TestExecScanOversizeHonest(t *testing.T) {
	root := t.TempDir()
	ws := t.TempDir()
	big := filepath.Join(ws, "big.bin")
	writeFile(t, big, strings.Repeat("x", 64))

	s, _ := NewStore(root, Config{TrackExec: true, MaxFileSize: 8})
	tc := s.BeginTurn("sess-1", "t1", ws)
	s.ExecPreScan(tc)
	writeFile(t, big, strings.Repeat("y", 64))
	s.ExecPostScan(tc)

	v, err := s.CommitTurn(tc)
	if err != nil || v == nil {
		t.Fatalf("commit = %v, %v", v, err)
	}
	entry := findEntry(t, v, big)
	if entry.Stored || entry.Reason == "" {
		t.Fatalf("oversize exec change must be unstored with reason, got %+v", entry)
	}
	if entry.Op != OpModified {
		t.Fatalf("op = %q, want modified", entry.Op)
	}
}

// TestExecScanExcludesNoise pins the walk exclusions: sessions/, tmp/,
// .git and node_modules are never tracked (agent bookkeeping, not user
// content).
func TestExecScanExcludesNoise(t *testing.T) {
	root := t.TempDir()
	ws := t.TempDir()

	writeFile(t, filepath.Join(ws, "real.txt"), "real")
	writeFile(t, filepath.Join(ws, "sessions", "x.jsonl"), "noise")
	writeFile(t, filepath.Join(ws, "tmp", "offload.log"), "noise")
	writeFile(t, filepath.Join(ws, ".git", "HEAD"), "noise")
	writeFile(t, filepath.Join(ws, "node_modules", "dep", "i.js"), "noise")

	s, _ := NewStore(root, Config{TrackExec: true})
	tc := s.BeginTurn("sess-1", "t1", ws)
	s.ExecPreScan(tc)

	// Nothing changed outside noise dirs → no version, and the noise files
	// were never indexed: mutate one and confirm the post-scan ignores it.
	writeFile(t, filepath.Join(ws, "tmp", "offload.log"), "noise2")
	s.ExecPostScan(tc)
	v, err := s.CommitTurn(tc)
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	if v != nil {
		for _, f := range v.Files {
			if strings.Contains(f.Rel, "tmp/") || strings.Contains(f.Rel, "sessions/") ||
				strings.Contains(f.Rel, ".git/") || strings.Contains(f.Rel, "node_modules/") {
				t.Fatalf("noise path tracked: %+v", f)
			}
		}
	}
}
