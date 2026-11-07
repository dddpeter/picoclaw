package snapshot

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRevertRestoresExactFileState pins the core §2.1 promise: a revert
// restores modified files byte-for-byte, removes files the turn created, and
// recreates files the turn deleted — including the fresh-creation case where
// the fs tools never had a pre-image.
func TestRevertRestoresExactFileState(t *testing.T) {
	root := t.TempDir()
	ws := t.TempDir()

	modified := filepath.Join(ws, "modified.txt")
	created := filepath.Join(ws, "created.txt")
	deleted := filepath.Join(ws, "deleted.txt")
	writeFile(t, modified, "original modified")
	writeFile(t, deleted, "original deleted")

	s, _ := NewStore(root, testConfig())

	// Turn 1: modifies modified.txt, creates created.txt, deletes deleted.txt
	// (the deletion happens out-of-band — e.g. exec — and is picked up by the
	// fs-capture reclassification: modified → deleted with pre-image).
	tc := s.BeginTurn("sess-1", "t1", ws)
	s.CaptureFile(tc, modified, "modified.txt")
	s.CaptureFile(tc, created, "created.txt")
	s.CaptureFile(tc, deleted, "deleted.txt")
	writeFile(t, modified, "rewritten")
	writeFile(t, created, "born this turn")
	_ = os.Remove(deleted)
	v := mustCommit(t, s, tc)
	if len(v.Files) != 3 {
		t.Fatalf("version files = %d, want 3", len(v.Files))
	}

	result, err := s.Revert("sess-1", "")
	if err != nil {
		t.Fatalf("Revert: %v", err)
	}
	if result.Restored != 2 || result.Deleted != 1 || result.Versions != 1 {
		t.Fatalf("result = %+v, want 2 restored / 1 deleted / 1 version", result)
	}

	got, err := os.ReadFile(modified)
	if err != nil || string(got) != "original modified" {
		t.Fatalf("modified.txt = %q, %v; want byte-exact original", got, err)
	}
	if _, err := os.Stat(created); !os.IsNotExist(err) {
		t.Fatalf("created.txt must be removed on revert, stat err = %v", err)
	}
	got, err = os.ReadFile(deleted)
	if err != nil || string(got) != "original deleted" {
		t.Fatalf("deleted.txt = %q, %v; want recreated original", got, err)
	}

	// Consumed: the undone version is gone, a repeat has nothing newer.
	versions, _ := s.Versions("sess-1")
	if len(versions) != 0 {
		t.Fatalf("revert must consume undone versions, %d left", len(versions))
	}
	if _, err := s.Revert("sess-1", ""); !errors.Is(err, ErrNoVersions) {
		t.Fatalf("second revert err = %v, want ErrNoVersions", err)
	}
}

// TestRevertCascadeOrder pins the cascade semantics: /revert <id> rolls back
// every version from the target to the newest, newest-first, ending at the
// target's pre-turn state — and preserves changes older than the target.
func TestRevertCascadeOrder(t *testing.T) {
	root := t.TempDir()
	ws := t.TempDir()
	file := filepath.Join(ws, "f.txt")

	s, _ := NewStore(root, testConfig())

	// Three turns rewrite the same file.
	states := []string{"state0", "state1", "state2", "state3"}
	var ids []string
	writeFile(t, file, states[0])
	for i := 1; i <= 3; i++ {
		tc := s.BeginTurn("sess-1", "t"+string(rune('0'+i)), ws)
		s.CaptureFile(tc, file, "f.txt")
		writeFile(t, file, states[i])
		v := mustCommit(t, s, tc)
		ids = append(ids, v.ID)
	}

	// Cascade from v2: undoes v3 then v2 → workspace = state1 (before turn 2).
	result, err := s.Revert("sess-1", ids[1])
	if err != nil {
		t.Fatalf("Revert: %v", err)
	}
	if result.Versions != 2 || result.FromID != ids[1] || result.ToID != ids[2] {
		t.Fatalf("result = %+v, want 2 versions %s→%s", result, ids[1], ids[2])
	}
	got, _ := os.ReadFile(file)
	if string(got) != states[1] {
		t.Fatalf("file = %q, want %q (pre-turn state of v2)", got, states[1])
	}

	// Older-than-target changes (turn 1's rewrite) survive: consuming v1
	// next would restore state0, proving the cascade order is right.
	result, err = s.Revert("sess-1", "")
	if err != nil {
		t.Fatalf("Revert: %v", err)
	}
	if result.Versions != 1 {
		t.Fatalf("only v1 must remain, got %d versions undone", result.Versions)
	}
	got, _ = os.ReadFile(file)
	if string(got) != states[0] {
		t.Fatalf("file = %q, want %q", got, states[0])
	}
}

// TestRevertSkipsUnstored pins the honest-degradation surface at revert
// time: entries without retained content are listed as skipped, and their
// files are left untouched.
func TestRevertSkipsUnstored(t *testing.T) {
	root := t.TempDir()
	ws := t.TempDir()
	big := filepath.Join(ws, "big.bin")
	writeFile(t, big, strings.Repeat("x", 64))
	small := filepath.Join(ws, "small.txt")
	writeFile(t, small, "small-old")

	s, _ := NewStore(root, Config{MaxFileSize: 8})
	tc := s.BeginTurn("sess-1", "t1", ws)
	s.CaptureFile(tc, big, "big.bin")
	small = filepath.Join(ws, "small.txt")
	writeFile(t, small, "old") // 3 bytes: within the cap, restorable
	s.CaptureFile(tc, small, "small.txt")
	writeFile(t, big, strings.Repeat("y", 64))
	writeFile(t, small, "small-new")
	v := mustCommit(t, s, tc)

	result, err := s.Revert("sess-1", "")
	if err != nil {
		t.Fatalf("Revert: %v", err)
	}
	if len(result.Skipped) != 1 || result.Skipped[0].Rel != "big.bin" {
		t.Fatalf("skipped = %+v, want big.bin", result.Skipped)
	}
	got, _ := os.ReadFile(big)
	if !strings.HasPrefix(string(got), "yyy") {
		t.Fatalf("skipped file must be untouched, got %q", got[:8])
	}
	got, _ = os.ReadFile(small)
	if string(got) != "old" {
		t.Fatalf("restorable file must be restored, got %q", got)
	}
	_ = v
}

func TestRevertRejectsBadIDs(t *testing.T) {
	root := t.TempDir()
	ws := t.TempDir()
	file := filepath.Join(ws, "f.txt")
	writeFile(t, file, "x")

	s, _ := NewStore(root, testConfig())
	if _, err := s.Revert("sess-1", "../escape"); err == nil {
		t.Fatal("path traversal id must be rejected")
	}
	// Any target on a session without history reports the empty case.
	if _, err := s.Revert("sess-1", "v999-none"); !errors.Is(err, ErrNoVersions) {
		t.Fatalf("unknown version on empty session: %v, want ErrNoVersions", err)
	}

	tc := s.BeginTurn("sess-1", "t1", ws)
	s.CaptureFile(tc, file, "f.txt")
	writeFile(t, file, "y")
	mustCommit(t, s, tc)

	if _, err := s.Revert("sess-1", "v999-none"); err == nil {
		t.Fatal("unknown version must be rejected")
	}
}
