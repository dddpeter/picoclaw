package fstools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newTestGrep(t *testing.T) (*GrepTool, string) {
	t.Helper()
	ws := t.TempDir()
	return NewGrepTool(ws, false), ws
}

// m-3: files over the size cap must be skipped (not read into memory) and
// reported in the tail note.
func TestGrepSkipsOversizedFiles(t *testing.T) {
	origCap := grepMaxFileSize
	grepMaxFileSize = 64
	t.Cleanup(func() { grepMaxFileSize = origCap })

	tool, ws := newTestGrep(t)
	if err := os.WriteFile(filepath.Join(ws, "small.txt"), []byte("needle here\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	big := strings.Repeat("x", 200) + "\nneedle in big\n"
	if err := os.WriteFile(filepath.Join(ws, "big.log"), []byte(big), 0o644); err != nil {
		t.Fatal(err)
	}

	res := tool.Execute(t.Context(), map[string]any{"pattern": "needle"})
	if res.IsError {
		t.Fatalf("grep failed: %s", res.ForLLM)
	}
	if !strings.Contains(res.ForLLM, "small.txt") {
		t.Fatalf("small file match missing: %s", res.ForLLM)
	}
	if strings.Contains(res.ForLLM, "big.log") {
		t.Fatalf("oversized file should be skipped, got: %s", res.ForLLM)
	}
	if !strings.Contains(res.ForLLM, "skipped") {
		t.Fatalf("skipped-file note missing: %s", res.ForLLM)
	}
}

// m-4: long lines with multibyte characters must be truncated on rune
// boundaries — no stray replacement characters from mid-rune byte cuts.
func TestGrepLineTruncationRespectsRunes(t *testing.T) {
	tool, ws := newTestGrep(t)
	longCJK := strings.Repeat("汉", 400)
	if err := os.WriteFile(filepath.Join(ws, "cjk.txt"), []byte(longCJK+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	res := tool.Execute(t.Context(), map[string]any{"pattern": "汉"})
	if res.IsError {
		t.Fatalf("grep failed: %s", res.ForLLM)
	}
	if got := strings.Count(res.ForLLM, "汉"); got != 300 {
		t.Fatalf("expected 300 kept runes, got %d", got)
	}
	if strings.ContainsRune(res.ForLLM, '\uFFFD') {
		t.Fatalf("mid-rune byte cut produced replacement characters: %q", res.ForLLM)
	}
}
