package fstools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func globTestTree(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.MkdirAll(filepath.Join(dir, "src", "deep"), 0o755))
	must(os.MkdirAll(filepath.Join(dir, "node_modules", "pkg"), 0o755))
	must(os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644))
	must(os.WriteFile(filepath.Join(dir, "src", "app.go"), []byte("package src\n// TODO fix\n"), 0o644))
	must(os.WriteFile(filepath.Join(dir, "src", "deep", "util.go"), []byte("package deep\n"), 0o644))
	must(os.WriteFile(filepath.Join(dir, "src", "note.md"), []byte("# notes\n"), 0o644))
	must(os.WriteFile(filepath.Join(dir, "node_modules", "pkg", "dep.go"), []byte("package dep\n"), 0o644))
	return dir
}

func TestGlobTool_RecursivePattern(t *testing.T) {
	dir := globTestTree(t)
	tool := NewGlobTool(dir, false)
	res := tool.Execute(context.Background(), map[string]any{"pattern": "**/*.go"})
	if res.IsError {
		t.Fatalf("glob failed: %s", res.ForLLM)
	}
	for _, want := range []string{"main.go", "src/app.go", "src/deep/util.go"} {
		if !strings.Contains(res.ForLLM, want) {
			t.Fatalf("missing %s in:\n%s", want, res.ForLLM)
		}
	}
	if strings.Contains(res.ForLLM, "node_modules") {
		t.Fatalf("node_modules must be skipped:\n%s", res.ForLLM)
	}
}

func TestGlobTool_SingleStarStaysInSegment(t *testing.T) {
	dir := globTestTree(t)
	tool := NewGlobTool(dir, false)
	res := tool.Execute(context.Background(), map[string]any{"pattern": "*.go", "path": "src"})
	if res.IsError {
		t.Fatalf("glob failed: %s", res.ForLLM)
	}
	if !strings.Contains(res.ForLLM, "src/app.go") || strings.Contains(res.ForLLM, "deep") {
		t.Fatalf("single star must not recurse:\n%s", res.ForLLM)
	}
}

func TestGlobTool_MetacharsAreLiteral(t *testing.T) {
	// The matcher escapes regex metachars: a "[x]" in a pattern matches a
	// literal filename, never a character class (predictable-by-design).
	dir := globTestTree(t)
	if err := os.WriteFile(filepath.Join(dir, "[x].go"), []byte("package x"), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := NewGlobTool(dir, false)
	res := tool.Execute(context.Background(), map[string]any{"pattern": "[x].go"})
	if res.IsError {
		t.Fatalf("glob failed: %s", res.ForLLM)
	}
	if !strings.Contains(res.ForLLM, "[x].go") {
		t.Fatalf("literal bracket filename not matched: %s", res.ForLLM)
	}
}

func TestGrepTool_LineMatches(t *testing.T) {
	dir := globTestTree(t)
	tool := NewGrepTool(dir, false)
	res := tool.Execute(context.Background(), map[string]any{"pattern": "TODO fix"})
	if res.IsError {
		t.Fatalf("grep failed: %s", res.ForLLM)
	}
	if !strings.Contains(res.ForLLM, "src/app.go:2: // TODO fix") {
		t.Fatalf("expected path:line hit, got:\n%s", res.ForLLM)
	}
}

func TestGrepTool_IncludeFilterAndCaseInsensitive(t *testing.T) {
	dir := globTestTree(t)
	tool := NewGrepTool(dir, false)
	res := tool.Execute(context.Background(), map[string]any{
		"pattern": "PACKAGE", "include": "*.go", "case_insensitive": true,
	})
	if res.IsError {
		t.Fatalf("grep failed: %s", res.ForLLM)
	}
	if strings.Contains(res.ForLLM, "note.md") {
		t.Fatalf("include filter leaked:\n%s", res.ForLLM)
	}
	if !strings.Contains(res.ForLLM, "main.go:1:") {
		t.Fatalf("expected case-insensitive hit:\n%s", res.ForLLM)
	}
}

func TestGrepTool_NoMatches(t *testing.T) {
	dir := globTestTree(t)
	tool := NewGrepTool(dir, false)
	res := tool.Execute(context.Background(), map[string]any{"pattern": "definitely-not-present-xyz"})
	if res.IsError || !strings.Contains(res.ForLLM, "No matches") {
		t.Fatalf("expected no-match notice, got %+v", res)
	}
}
