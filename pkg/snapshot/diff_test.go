package snapshot

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDiffMaskedButPreImageRaw pins the masking contract: diffs are a
// display/query surface and must be masked; stored pre-images stay raw for
// byte-exact restore (TestRevertRestoresExactFileState depends on it).
func TestDiffMaskedButPreImageRaw(t *testing.T) {
	root := t.TempDir()
	ws := t.TempDir()
	secret := "sk-ant-api03-ABCDEFGHIJKLMNOPQRSTUVWXYZ1234567890abcd"
	file := filepath.Join(ws, "config.env")
	writeFile(t, file, "token = "+secret+"\n")

	s, _ := NewStore(root, testConfig())
	tc := s.BeginTurn("sess-1", "t1", ws)
	s.CaptureFile(tc, file, "config.env")
	writeFile(t, file, "token = rotated-value\n")
	v := mustCommit(t, s, tc)

	if v.DiffFiles != 1 {
		t.Fatalf("DiffFiles = %d, want 1", v.DiffFiles)
	}
	diff, err := s.ReadDiff("sess-1", v.ID)
	if err != nil {
		t.Fatalf("ReadDiff: %v", err)
	}
	if strings.Contains(string(diff), secret) {
		t.Fatal("diff leaks the raw API key")
	}
	if !strings.Contains(string(diff), "a/config.env") {
		t.Fatalf("diff missing file header:\n%s", diff)
	}

	entry := findEntry(t, v, file)
	pre, err := os.ReadFile(filepath.Join(s.preDir("sess-1"), entry.SHA256+".bin"))
	if err != nil {
		t.Fatalf("read pre-image: %v", err)
	}
	if !strings.Contains(string(pre), secret) {
		t.Fatal("pre-image must stay raw (masking it would corrupt restore)")
	}
}

// TestDiffCreatedAndDeleted covers the two asymmetric ops: created files
// diff against empty, deleted files diff to empty.
func TestDiffCreatedAndDeleted(t *testing.T) {
	root := t.TempDir()
	ws := t.TempDir()
	victim := filepath.Join(ws, "victim.txt")
	writeFile(t, victim, "doomed lines\n")

	s, _ := NewStore(root, testConfig())
	tc := s.BeginTurn("sess-1", "t1", ws)
	s.CaptureFile(tc, victim, "victim.txt")
	created := filepath.Join(ws, "fresh.txt")
	s.CaptureFile(tc, created, "fresh.txt")

	_ = os.Remove(victim)
	writeFile(t, created, "hello world\n")
	v := mustCommit(t, s, tc)

	diff, err := s.ReadDiff("sess-1", v.ID)
	if err != nil {
		t.Fatalf("ReadDiff: %v", err)
	}
	text := string(diff)
	if !strings.Contains(text, "+hello world") {
		t.Fatalf("created diff missing +line:\n%s", text)
	}
	if !strings.Contains(text, "-doomed lines") {
		t.Fatalf("deleted diff missing -line:\n%s", text)
	}
}

// TestDiffUnstoredEntryGetsNote pins the honest-degradation surface: a
// captured-but-unstored file shows a note in diffs.txt instead of a diff.
func TestDiffUnstoredEntryGetsNote(t *testing.T) {
	root := t.TempDir()
	ws := t.TempDir()
	file := filepath.Join(ws, "big.bin")
	writeFile(t, file, strings.Repeat("x", 64))

	s, _ := NewStore(root, Config{MaxFileSize: 8})
	tc := s.BeginTurn("sess-1", "t1", ws)
	s.CaptureFile(tc, file, "big.bin")
	writeFile(t, file, strings.Repeat("y", 64))
	v := mustCommit(t, s, tc)

	diff, err := s.ReadDiff("sess-1", v.ID)
	if err != nil {
		t.Fatalf("ReadDiff: %v", err)
	}
	if !strings.Contains(string(diff), "pre-image not retained") {
		t.Fatalf("unstored entry must be noted:\n%s", diff)
	}
}

// TestDiffOversizeSkipped pins the diff caps: content beyond the difflib
// budget gets an explicit skip note instead of an unbounded diff.
func TestDiffOversizeSkipped(t *testing.T) {
	root := t.TempDir()
	ws := t.TempDir()
	file := filepath.Join(ws, "huge.txt")
	writeFile(t, file, strings.Repeat("a", 128))

	s, _ := NewStore(root, testConfig())
	tc := s.BeginTurn("sess-1", "t1", ws)
	s.CaptureFile(tc, file, "huge.txt")
	writeFile(t, file, strings.Repeat("b", maxDiffInputBytes*2))
	v := mustCommit(t, s, tc)

	diff, err := s.ReadDiff("sess-1", v.ID)
	if err != nil {
		t.Fatalf("ReadDiff: %v", err)
	}
	if !strings.Contains(string(diff), "diff skipped") {
		t.Fatalf("oversize diff must be skipped with a note:\n%s", diff)
	}
}
