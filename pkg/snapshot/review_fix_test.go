package snapshot

// Regression tests for the post-implementation review batch: each pins a
// defect found by the self-review + independent review pass.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestExecScanIncompleteWalkNoPhantomCreated pins the P0 fix: a walk
// truncated by scan_max_files must never let the vanish cleanup drop index
// entries nor attribute unseen files as created — /revert would otherwise
// delete real files that exec never touched.
func TestExecScanIncompleteWalkNoPhantomCreated(t *testing.T) {
	root := t.TempDir()
	ws := t.TempDir()

	// Five files; the scan cap (3) truncates every walk.
	names := []string{"a.txt", "b.txt", "c.txt", "d.txt", "e.txt"}
	for _, n := range names {
		writeFile(t, filepath.Join(ws, n), "content-"+n)
	}

	s, _ := NewStore(root, Config{TrackExec: true, ScanMaxFiles: 3})
	tc := s.BeginTurn("sess-1", "t1", ws)
	s.ExecPreScan(tc)

	// The exec run touches only a.txt (and the truncated tail is untouched).
	writeFile(t, filepath.Join(ws, "a.txt"), "content-a2")
	s.ExecPostScan(tc)

	v, err := s.CommitTurn(tc)
	if err != nil {
		t.Fatalf("CommitTurn: %v", err)
	}
	if v == nil {
		t.Fatal("a real modification must still be recorded")
	}
	for _, f := range v.Files {
		if f.Op == OpCreated {
			t.Fatalf("incomplete walks must not attribute created: %+v", f)
		}
		if filepath.Base(f.Path) == "d.txt" || filepath.Base(f.Path) == "e.txt" {
			t.Fatalf("truncated-tail file wrongly tracked: %+v", f)
		}
	}

	// And reverting it must not delete anything beyond the real change.
	result, err := s.Revert("sess-1", "")
	if err != nil {
		t.Fatalf("Revert: %v", err)
	}
	if result.Deleted != 0 {
		t.Fatalf("no created files to remove, got %+v", result)
	}
	for _, n := range names {
		if _, err := os.Stat(filepath.Join(ws, n)); err != nil {
			t.Fatalf("%s must survive the revert: %v", n, err)
		}
	}
}

// TestVersionIDsNumericOrder pins the P1 fix: version ordering is numeric,
// so v1000 sorts after v999 (a long-lived session crosses three digits) and
// the bare /revert undoes the actual newest version.
func TestVersionIDsNumericOrder(t *testing.T) {
	root := t.TempDir()
	ws := t.TempDir()
	file := filepath.Join(ws, "f.txt")

	s, _ := NewStore(root, Config{MaxVersions: 2000})
	// Forge the tail of a long-lived session: v999 is old, v1000 is newest.
	// Entries point at the real file so restores land where the test looks.
	writeFile(t, file, "state-999")
	if err := os.MkdirAll(filepath.Join(s.sessionDir("sess-1"), "v999-old"), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// v999's own pre-image (state-998) differs from v1000's (state-999) —
	// identical blobs would dedup and the created-flag guard would trip.
	writeManifest(t, s, "sess-1", "v999-old", file, "state-998")

	writeFile(t, file, "state-1000")
	if err := os.MkdirAll(filepath.Join(s.sessionDir("sess-1"), "v1000-new"), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// v1000's pre-image is the state BEFORE turn 1000 (= state-999).
	writeManifest(t, s, "sess-1", "v1000-new", file, "state-999")

	versions, err := s.Versions("sess-1")
	if err != nil || len(versions) != 2 {
		t.Fatalf("Versions = %d, %v; want 2", len(versions), err)
	}
	if versions[0].ID != "v999-old" || versions[1].ID != "v1000-new" {
		t.Fatalf("order = [%s, %s], want v999 first", versions[0].ID, versions[1].ID)
	}

	// Bare /revert must undo v1000 (the newest), not v999.
	if _, err := s.Revert("sess-1", ""); err != nil {
		t.Fatalf("Revert: %v", err)
	}
	got, _ := os.ReadFile(file)
	if string(got) != "state-999" {
		t.Fatalf("file = %q, want state-999 (v1000's pre-image)", got)
	}
	versions, _ = s.Versions("sess-1")
	if len(versions) != 1 || versions[0].ID != "v999-old" {
		t.Fatalf("only v1000 must be consumed, left %+v", versions)
	}
}

func writeManifest(t *testing.T, s *Store, sessionKey, id, path, preState string) {
	t.Helper()
	// A minimal valid manifest: one modified entry whose pre-image exists.
	data := []byte(preState)
	hash, created, err := s.storePreImageBytes(sessionKey, data)
	if err != nil || !created {
		t.Fatalf("storePreImageBytes: %v created=%v", err, created)
	}
	v := Version{
		ID:    id,
		Num:   versionNum(id),
		Files: []FileEntry{{Path: path, Rel: "f.txt", Op: OpModified, SHA256: hash, Stored: true}},
	}
	if err := writeJSONFile(filepath.Join(s.sessionDir(sessionKey), id, "manifest.json"), v); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
}

// TestDiffAfterOversizeSkipped pins the P1 fix: a file the turn grew past
// the diff input cap is stat-checked BEFORE reading — the diff notes the
// skip instead of loading the whole file.
func TestDiffAfterOversizeSkipped(t *testing.T) {
	root := t.TempDir()
	ws := t.TempDir()
	file := filepath.Join(ws, "grew.txt")
	writeFile(t, file, "small")

	s, _ := NewStore(root, testConfig())
	tc := s.BeginTurn("sess-1", "t1", ws)
	s.CaptureFile(tc, file, "grew.txt")
	// The turn blows the file up past the 64KB diff-input cap.
	writeFile(t, file, strings.Repeat("x", maxDiffInputBytes*3))
	v := mustCommit(t, s, tc)

	diff, err := s.ReadDiff("sess-1", v.ID)
	if err != nil {
		t.Fatalf("ReadDiff: %v", err)
	}
	if !strings.Contains(string(diff), "diff skipped: current file") {
		t.Fatalf("oversized post-state must be skipped with a note:\n%.200s", diff)
	}
	if strings.Count(string(diff), "x") > maxDiffInputBytes {
		t.Fatal("diff must not contain the full oversized content")
	}
}

// TestExecScanCacheBlobGoneHonest pins the P2 fix: a cache entry whose blob
// was removed out-of-band (eviction of a shared hash, version cleanup)
// degrades to an unstored entry instead of a manifest claiming a dead hash.
func TestExecScanCacheBlobGoneHonest(t *testing.T) {
	root := t.TempDir()
	ws := t.TempDir()
	file := filepath.Join(ws, "f.txt")
	writeFile(t, file, "v0")

	s, _ := NewStore(root, Config{TrackExec: true})
	tc := s.BeginTurn("sess-1", "t1", ws)
	s.ExecPreScan(tc)

	// Simulate out-of-band blob loss (shared-hash eviction / version GC).
	preEntries, _ := os.ReadDir(s.preDir("sess-1"))
	for _, pe := range preEntries {
		_ = os.Remove(filepath.Join(s.preDir("sess-1"), pe.Name()))
	}

	writeFile(t, file, "v1")
	s.ExecPostScan(tc)
	v, err := s.CommitTurn(tc)
	if err != nil || v == nil {
		t.Fatalf("commit = %v, %v", v, err)
	}
	entry := findEntry(t, v, file)
	if entry.Stored || entry.Reason == "" {
		t.Fatalf("dead-blob entry must be unstored with reason, got %+v", entry)
	}
}

// TestRevertConfinesToManifestWorkspace pins the P2 fix: entries outside the
// version's recorded workspace root are skipped with a note (relocated
// workspace must not overwrite whatever now lives at the old paths).
func TestRevertConfinesToManifestWorkspace(t *testing.T) {
	root := t.TempDir()
	ws := t.TempDir()
	file := filepath.Join(ws, "f.txt")
	writeFile(t, file, "original")

	s, _ := NewStore(root, testConfig())
	tc := s.BeginTurn("sess-1", "t1", ws)
	s.CaptureFile(tc, file, "f.txt")
	writeFile(t, file, "rewritten")
	mustCommit(t, s, tc)

	// Relocate: rewrite the manifest's workspace to an unrelated root.
	versions, _ := s.Versions("sess-1")
	v := versions[0]
	v.Workspace = filepath.Join(t.TempDir(), "elsewhere")
	if err := writeJSONFile(filepath.Join(s.sessionDir("sess-1"), v.ID, "manifest.json"), v); err != nil {
		t.Fatalf("rewrite manifest: %v", err)
	}

	result, err := s.Revert("sess-1", "")
	if err != nil {
		t.Fatalf("Revert: %v", err)
	}
	if result.Restored != 0 {
		t.Fatalf("out-of-workspace entries must not restore, got %+v", result)
	}
	got, _ := os.ReadFile(file)
	if string(got) != "rewritten" {
		t.Fatalf("file must be untouched, got %q", got)
	}
	found := false
	for _, note := range result.Notes {
		if strings.Contains(note, "outside the version's workspace") {
			found = true
		}
	}
	if !found {
		t.Fatalf("skip must be noted, notes = %v", result.Notes)
	}
}
