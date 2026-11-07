package snapshot

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func testConfig() Config {
	return Config{MaxVersions: 20, MaxTotalBytes: 128 << 20, MaxFileSize: 4 << 20, TrackExec: true}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func mustCommit(t *testing.T, s *Store, tc *TurnCapture) *Version {
	t.Helper()
	v, err := s.CommitTurn(tc)
	if err != nil {
		t.Fatalf("CommitTurn: %v", err)
	}
	if v == nil {
		t.Fatal("CommitTurn returned no version")
	}
	return v
}

func findEntry(t *testing.T, v *Version, path string) FileEntry {
	t.Helper()
	for _, f := range v.Files {
		if filepath.Clean(f.Path) == filepath.Clean(path) {
			return f
		}
	}
	t.Fatalf("version %s has no entry for %s", v.ID, path)
	return FileEntry{}
}

func TestCaptureCommitModified(t *testing.T) {
	root := t.TempDir()
	ws := t.TempDir()
	file := filepath.Join(ws, "hello.txt")
	writeFile(t, file, "original content")

	s, err := NewStore(root, testConfig())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	tc := s.BeginTurn("sess-1", "default-turn-1", ws)
	s.CaptureFile(tc, file, "hello.txt")
	if err := os.WriteFile(file, []byte("changed content"), 0o644); err != nil {
		t.Fatalf("modify: %v", err)
	}
	v := mustCommit(t, s, tc)

	entry := findEntry(t, v, file)
	if entry.Op != OpModified || !entry.Stored || entry.SHA256 == "" {
		t.Fatalf("entry = %+v, want stored modified", entry)
	}

	// The pre-image must hold the ORIGINAL content, byte-for-byte.
	pre, err := os.ReadFile(filepath.Join(s.preDir("sess-1"), entry.SHA256+".bin"))
	if err != nil {
		t.Fatalf("read pre-image: %v", err)
	}
	if string(pre) != "original content" {
		t.Fatalf("pre-image = %q, want original content", pre)
	}

	versions, err := s.Versions("sess-1")
	if err != nil || len(versions) != 1 {
		t.Fatalf("Versions() = %v, %v; want 1 version", versions, err)
	}
}

func TestCaptureCommitCreated(t *testing.T) {
	root := t.TempDir()
	ws := t.TempDir()
	file := filepath.Join(ws, "new.txt")

	s, _ := NewStore(root, testConfig())
	tc := s.BeginTurn("sess-1", "t1", ws)
	s.CaptureFile(tc, file, "new.txt") // does not exist yet → created
	writeFile(t, file, "brand new")
	v := mustCommit(t, s, tc)

	entry := findEntry(t, v, file)
	if entry.Op != OpCreated || entry.Stored {
		t.Fatalf("entry = %+v, want unstored created", entry)
	}
}

// TestCaptureDedupSamePathKeepsEarliest pins the pre-TURN semantics: two
// writes to the same file in one turn keep the earliest pre-state only.
func TestCaptureDedupSamePathKeepsEarliest(t *testing.T) {
	root := t.TempDir()
	ws := t.TempDir()
	file := filepath.Join(ws, "a.txt")
	writeFile(t, file, "v0")

	s, _ := NewStore(root, testConfig())
	tc := s.BeginTurn("sess-1", "t1", ws)
	s.CaptureFile(tc, file, "a.txt")
	writeFile(t, file, "v1")
	s.CaptureFile(tc, file, "a.txt") // second write must not win
	writeFile(t, file, "v2")
	v := mustCommit(t, s, tc)

	entry := findEntry(t, v, file)
	pre, _ := os.ReadFile(filepath.Join(s.preDir("sess-1"), entry.SHA256+".bin"))
	if string(pre) != "v0" {
		t.Fatalf("pre-image = %q, want v0 (earliest)", pre)
	}
}

// TestCommitDropsNoOpAndEmpty pins the workspace_never_modified contract:
// unchanged content nets to no entry; an all-no-op turn commits nothing.
func TestCommitDropsNoOpAndEmpty(t *testing.T) {
	root := t.TempDir()
	ws := t.TempDir()
	file := filepath.Join(ws, "same.txt")
	writeFile(t, file, "stable")

	s, _ := NewStore(root, testConfig())
	tc := s.BeginTurn("sess-1", "t1", ws)
	s.CaptureFile(tc, file, "same.txt")
	// No modification happens (e.g. the tool failed).
	v, err := s.CommitTurn(tc)
	if err != nil {
		t.Fatalf("CommitTurn: %v", err)
	}
	if v != nil {
		t.Fatalf("no-op turn must not commit, got %+v", v)
	}
	versions, _ := s.Versions("sess-1")
	if len(versions) != 0 {
		t.Fatalf("Versions() = %d, want 0", len(versions))
	}
}

// TestCaptureOversizeHonestSkip pins the honest-degradation contract: the
// change is recorded, the content is not, and the reason says why.
func TestCaptureOversizeHonestSkip(t *testing.T) {
	root := t.TempDir()
	ws := t.TempDir()
	file := filepath.Join(ws, "big.bin")
	writeFile(t, file, strings.Repeat("x", 64))

	s, _ := NewStore(root, Config{MaxFileSize: 8})
	tc := s.BeginTurn("sess-1", "t1", ws)
	s.CaptureFile(tc, file, "big.bin")
	writeFile(t, file, strings.Repeat("y", 64))
	v := mustCommit(t, s, tc)

	entry := findEntry(t, v, file)
	if entry.Stored || entry.SHA256 != "" {
		t.Fatalf("oversize entry must be unstored, got %+v", entry)
	}
	if entry.Reason == "" {
		t.Fatal("oversize entry must carry a reason")
	}
	if entry.Op != OpModified {
		t.Fatalf("op = %q, want modified", entry.Op)
	}
}

// TestFsWriteThenExecDeleteNetsToDeleted covers the reclassification path:
// a captured modification followed by an out-of-band deletion (exec) nets
// to a deleted entry whose pre-image still restores the file.
func TestFsWriteThenExecDeleteNetsToDeleted(t *testing.T) {
	root := t.TempDir()
	ws := t.TempDir()
	file := filepath.Join(ws, "gone.txt")
	writeFile(t, file, "before")

	s, _ := NewStore(root, testConfig())
	tc := s.BeginTurn("sess-1", "t1", ws)
	s.CaptureFile(tc, file, "gone.txt")
	_ = os.Remove(file) // exec deleted it after the fs write
	v := mustCommit(t, s, tc)

	entry := findEntry(t, v, file)
	if entry.Op != OpDeleted || !entry.Stored {
		t.Fatalf("entry = %+v, want stored deleted", entry)
	}
	pre, _ := os.ReadFile(filepath.Join(s.preDir("sess-1"), entry.SHA256+".bin"))
	if string(pre) != "before" {
		t.Fatalf("pre-image = %q, want before", pre)
	}
}

// TestEvictionByCountAndBudget pins the bounded-store contract: the oldest
// versions are evicted at commit time and the eviction is noted.
func TestEvictionByCountAndBudget(t *testing.T) {
	root := t.TempDir()
	ws := t.TempDir()

	s, _ := NewStore(root, Config{MaxVersions: 2, MaxTotalBytes: 1 << 20})
	for i := 0; i < 3; i++ {
		file := filepath.Join(ws, "f.txt")
		writeFile(t, file, "content-0")
		tc := s.BeginTurn("sess-1", "t"+string(rune('a'+i)), ws)
		s.CaptureFile(tc, file, "f.txt")
		writeFile(t, file, "content-"+string(rune('1'+i)))
		if _, err := s.CommitTurn(tc); err != nil {
			t.Fatalf("commit %d: %v", i, err)
		}
	}

	versions, _ := s.Versions("sess-1")
	if len(versions) != 2 {
		t.Fatalf("Versions() = %d, want 2 after eviction", len(versions))
	}
	if versions[0].Num != 2 {
		t.Fatalf("oldest retained = v%d, want v2 (v1 evicted)", versions[0].Num)
	}

	// Budget softness for the newest version: a single version storing more
	// than the whole budget is retained (undo of the last turn is the
	// primary use case) and the breach is noted, not silently swallowed.
	s2, _ := NewStore(filepath.Join(root, "b"), Config{MaxVersions: 10, MaxTotalBytes: 8})
	file := filepath.Join(ws, "big.txt")
	writeFile(t, file, strings.Repeat("z", 64))
	tc := s2.BeginTurn("sess-2", "t1", ws)
	s2.CaptureFile(tc, file, "big.txt")
	writeFile(t, file, strings.Repeat("w", 64))
	v := mustCommit(t, s2, tc)
	for _, note := range v.Notes {
		if strings.Contains(note, "disk budget exceeded") {
			versions, _ := s2.Versions("sess-2")
			if len(versions) != 1 {
				t.Fatalf("newest version must be retained despite budget, got %d", len(versions))
			}
			return
		}
	}
	t.Fatalf("budget breach must be noted, notes = %v", v.Notes)
}

// TestVersionsSkipTornDir pins crash tolerance: a version directory without
// a readable manifest is invisible, not fatal.
func TestVersionsSkipTornDir(t *testing.T) {
	root := t.TempDir()
	ws := t.TempDir()

	s, _ := NewStore(root, testConfig())
	file := filepath.Join(ws, "f.txt")
	writeFile(t, file, "one")
	tc := s.BeginTurn("sess-1", "t1", ws)
	s.CaptureFile(tc, file, "f.txt")
	writeFile(t, file, "two")
	mustCommit(t, s, tc)

	// Simulate a crashed commit: directory exists, manifest does not.
	if err := os.MkdirAll(filepath.Join(s.sessionDir("sess-1"), "v099-dead"), 0o700); err != nil {
		t.Fatalf("mkdir torn: %v", err)
	}

	versions, err := s.Versions("sess-1")
	if err != nil {
		t.Fatalf("Versions: %v", err)
	}
	if len(versions) != 1 {
		t.Fatalf("Versions() = %d, want 1 (torn dir skipped)", len(versions))
	}
	if _, err := s.Version("sess-1", "v099-dead"); err == nil {
		t.Fatal("Version(torn) must error")
	}
	if _, err := s.Version("sess-1", "../escape"); err == nil {
		t.Fatal("Version(path traversal) must be rejected")
	}
}

// TestSweepOrphanPreImages pins the crashed-turn cleanup: unreferenced
// pre-images older than the sweep age are removed at store open.
func TestSweepOrphanPreImages(t *testing.T) {
	root := t.TempDir()
	ws := t.TempDir()

	s, _ := NewStore(root, testConfig())
	file := filepath.Join(ws, "f.txt")
	writeFile(t, file, "referenced")
	tc := s.BeginTurn("sess-1", "t1", ws)
	s.CaptureFile(tc, file, "f.txt")
	writeFile(t, file, "changed")
	mustCommit(t, s, tc)

	// An orphaned blob (crashed turn) plus a torn version dir, both aged.
	orphan := filepath.Join(s.preDir("sess-1"), strings.Repeat("a", 64)+".bin")
	writeFile(t, orphan, "orphan")
	stale := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(orphan, stale, stale); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	// A fresh orphan must survive (may belong to a live capture).
	fresh := filepath.Join(s.preDir("sess-1"), strings.Repeat("b", 64)+".bin")
	writeFile(t, fresh, "fresh")

	s2, err := NewStore(root, testConfig())
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Fatalf("aged orphan must be swept, stat err = %v", err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatalf("fresh blob must survive sweep: %v", err)
	}
	versions, _ := s2.Versions("sess-1")
	if len(versions) != 1 {
		t.Fatalf("committed version must survive sweep, got %d", len(versions))
	}
}

// TestConcurrentCaptureDifferentSessions pins store-level concurrency
// safety: parallel turns in different sessions commit without corruption.
func TestConcurrentCaptureDifferentSessions(t *testing.T) {
	root := t.TempDir()
	ws := t.TempDir()

	s, _ := NewStore(root, testConfig())
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			key := "sess-" + string(rune('a'+n))
			file := filepath.Join(ws, key+".txt")
			writeFile(t, file, "old-"+key)
			tc := s.BeginTurn(key, "t"+key, ws)
			s.CaptureFile(tc, file, key+".txt")
			writeFile(t, file, "new-"+key)
			if _, err := s.CommitTurn(tc); err != nil {
				t.Errorf("commit %s: %v", key, err)
			}
		}(i)
	}
	wg.Wait()

	for i := 0; i < 8; i++ {
		key := "sess-" + string(rune('a'+i))
		versions, err := s.Versions(key)
		if err != nil || len(versions) != 1 {
			t.Errorf("session %s: %d versions, err %v; want 1", key, len(versions), err)
		}
	}
}
