package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/lsp"
	"github.com/sipeed/picoclaw/pkg/lsp/fakeserver"
)

// TestFakeServerHelper is the fake LSP server process entry point.
func TestFakeServerHelper(t *testing.T) {
	if !fakeserver.Active() {
		t.Skip("helper process entry point only")
	}
	fakeserver.Serve(os.Stdin, os.Stdout)
	os.Exit(0)
}

func TestResolveLspServersMergeSemantics(t *testing.T) {
	// Defaults: all 8 built-ins.
	base := resolveLspServers(config.LspToolsConfig{})
	if len(base) != 8 {
		t.Fatalf("built-in servers = %d, want 8: %v", len(base), serverNames(base))
	}

	disable := false
	cfg := config.LspToolsConfig{
		Servers: map[string]config.LspServerConfig{
			// disable one built-in
			"clangd": {Disabled: &disable},
			// override a built-in's command
			"gopls": {Command: []string{"gopls", "-v=1"}},
			// add a brand-new server
			"myls": {Command: []string{"myls", "--stdio"}, Extensions: []string{"ml"}},
		},
	}
	// re-enable clangd via Disabled: false is meaningless (nil-able false
	// means NOT disabled); use an explicitly disabled entry instead.
	yes := true
	cfg.Servers["clangd"] = config.LspServerConfig{Disabled: &yes}

	got := resolveLspServers(cfg)
	names := strings.Join(serverNames(got), ",")
	for _, wantAbsent := range []string{"clangd"} {
		if strings.Contains(names, wantAbsent) {
			t.Fatalf("disabled server still present: %s", names)
		}
	}
	for _, want := range []string{"gopls", "myls", "typescript-language-server", "vue-language-server", "jdtls"} {
		if !strings.Contains(names, want) {
			t.Fatalf("missing server %s in %s", want, names)
		}
	}
	if len(got) != 8 { // 8 - clangd + myls
		t.Fatalf("servers = %d (%s), want 8", len(got), names)
	}
	for _, s := range got {
		if s.Name == "gopls" {
			if s.Command != "gopls" || len(s.Args) != 1 || s.Args[0] != "-v=1" {
				t.Fatalf("gopls override not applied: %+v", s)
			}
		}
		if s.Name == "myls" {
			if !s.Supports("x.ml") {
				t.Fatalf("myls extensions not normalized: %+v", s.Extensions)
			}
		}
	}
}

func TestSelectLspRoutesSkipsMissingCommands(t *testing.T) {
	servers := []lsp.ServerConfig{
		{Name: "no-such-lsp-cmd-xyz", Command: "no-such-lsp-cmd-xyz", Extensions: []string{".go"}},
	}
	routes, skipped, err := selectLspRoutes(servers, t.TempDir(), nil, nil, 10)
	if err != nil {
		t.Fatalf("implicit selection must not error on missing commands: %v", err)
	}
	if len(routes) != 0 || len(skipped) != 1 {
		t.Fatalf("routes = %d, skipped = %v", len(routes), skipped)
	}

	// Explicit selection of a missing command IS an error.
	_, _, err = selectLspRoutes(servers, t.TempDir(), nil, []string{"no-such-lsp-cmd-xyz"}, 10)
	if err == nil {
		t.Fatal("explicit selection of missing command must error")
	}
}

func fakeLspServerForTool(diags map[string][]fakeserver.Diag, pull, push bool) config.LspServerConfig {
	return config.LspServerConfig{
		Command: []string{os.Args[0], "-test.run=^TestFakeServerHelper$", "--"},
		Env:     fakeserver.Env(diags, pull, push),
	}
}

func TestLspDiagnosticsToolEndToEnd(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "a.go")
	if err := os.WriteFile(file, []byte("package a\nconst x = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	key := lsp.DocumentKey(lsp.FileURI(file))
	diags := map[string][]fakeserver.Diag{
		key: {fakeserver.DiagAt(1, 6, 1, "unused constant x")},
	}
	serverCfg := fakeLspServerForTool(diags, true, false)
	serverCfg.Extensions = []string{".go"}

	pool := lsp.NewPool(time.Minute, 5*time.Second)
	t.Cleanup(pool.Close) // registered after TempDir: LIFO closes before cleanup
	tool := NewLspDiagnosticsToolWithPool(dir, false, nil,
		config.LspToolsConfig{Servers: map[string]config.LspServerConfig{"fake": serverCfg}}, pool)

	// Explicit server: real servers on PATH (e.g. gopls) must not join the
	// implicit route in tests.
	result := tool.Execute(context.Background(), map[string]any{"path": dir, "server": "fake"})
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.ForLLM)
	}
	for _, want := range []string{
		"LSP diagnostics: 1 diagnostic(s) across 1 file(s)",
		"fake: 1 diagnostic(s) in 1 file(s)",
		"a.go:2:7: error fake: unused constant x",
	} {
		if !strings.Contains(result.ForLLM, want) {
			t.Fatalf("output missing %q:\n%s", want, result.ForLLM)
		}
	}
}

func TestLspDiagnosticsToolNoSupportedFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	serverCfg := fakeLspServerForTool(nil, true, false)
	serverCfg.Extensions = []string{".go"}
	pool := lsp.NewPool(time.Minute, 5*time.Second)
	t.Cleanup(pool.Close)
	tool := NewLspDiagnosticsToolWithPool(dir, false, nil,
		config.LspToolsConfig{Servers: map[string]config.LspServerConfig{"fake": serverCfg}}, pool)

	result := tool.Execute(context.Background(), map[string]any{"path": dir, "server": "fake"})
	if result.IsError {
		t.Fatalf("no supported files must not be an error: %s", result.ForLLM)
	}
	if !strings.Contains(result.ForLLM, "No supported files found") {
		t.Fatalf("unexpected output: %s", result.ForLLM)
	}
}

func TestLspDiagnosticsToolUnknownServer(t *testing.T) {
	dir := t.TempDir()
	tool := NewLspDiagnosticsTool(dir, false, nil, config.LspToolsConfig{})
	result := tool.Execute(context.Background(), map[string]any{"path": dir, "server": "nope"})
	if !result.IsError {
		t.Fatal("unknown server must error")
	}
	if !strings.Contains(result.ForLLM, "unknown LSP server") {
		t.Fatalf("error = %s", result.ForLLM)
	}
}

func TestLspFixToolPreviewAndWrite(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "a.go")
	if err := os.WriteFile(file, []byte("package a\n\nconst  x=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	uri := lsp.FileURI(file)
	actions := fmt.Sprintf(`[{"title":"fix spaces","kind":"source.fixAll","uri":%q,"edits":[{"sl":2,"sc":6,"el":2,"ec":7,"newText":""}]}]`, uri)
	env := fakeserver.Env(nil, true, false)
	env["PICOCLAW_FAKESERVER_ACTIONS"] = actions
	serverCfg := config.LspServerConfig{
		Command:    []string{os.Args[0], "-test.run=^TestFakeServerHelper$", "--"},
		Env:        env,
		Extensions: []string{".go"},
	}

	pool := lsp.NewPool(time.Minute, 5*time.Second)
	t.Cleanup(pool.Close)
	tool := NewLspFixToolWithPool(dir, false, nil, nil,
		config.LspToolsConfig{Servers: map[string]config.LspServerConfig{"fake": serverCfg}}, pool)

	// Preview (write=false): full fixed text returned, file untouched.
	res := tool.Execute(context.Background(), map[string]any{"path": file, "server": "fake"})
	if res.IsError {
		t.Fatalf("preview: %s", res.ForLLM)
	}
	if !strings.Contains(res.ForLLM, "computed changes for a.go") || !strings.Contains(res.ForLLM, "const x=1") {
		t.Fatalf("preview output: %s", res.ForLLM)
	}
	if data, _ := os.ReadFile(file); string(data) != "package a\n\nconst  x=1\n" {
		t.Fatalf("preview must not write, file = %q", data)
	}

	// write=true: file updated on disk.
	res = tool.Execute(context.Background(), map[string]any{"path": file, "server": "fake", "write": true})
	if res.IsError {
		t.Fatalf("write: %s", res.ForLLM)
	}
	if !strings.Contains(res.ForLLM, "updated a.go") {
		t.Fatalf("write output: %s", res.ForLLM)
	}
	data, _ := os.ReadFile(file)
	if string(data) != "package a\n\nconst x=1\n" {
		t.Fatalf("file after write = %q", data)
	}

	// Unchanged case: a kind with no matching actions → "left unchanged".
	res = tool.Execute(context.Background(), map[string]any{"path": file, "server": "fake", "write": true, "kind": "source.organizeImports"})
	if res.IsError {
		t.Fatalf("third call: %s", res.ForLLM)
	}
	if !strings.Contains(res.ForLLM, "left a.go unchanged") {
		t.Fatalf("unchanged output: %s", res.ForLLM)
	}
}

func TestLspFixToolAmbiguousServer(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "x.py")
	if err := os.WriteFile(file, []byte("x=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Default catalog has pyright + ruff for .py (both missing on PATH, but
	// ambiguity is detected before availability).
	tool := NewLspFixToolWithPool(dir, false, nil, nil, config.LspToolsConfig{}, nil)
	res := tool.Execute(context.Background(), map[string]any{"path": file})
	if !res.IsError || !strings.Contains(res.ForLLM, "multiple servers support") {
		t.Fatalf("want ambiguity error, got: %s", res.ForLLM)
	}
}

func TestEditDiagnosticsInjection(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "a.go")
	if err := os.WriteFile(file, []byte("package a\nconst x = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	key := lsp.DocumentKey(lsp.FileURI(file))
	diags := map[string][]fakeserver.Diag{key: {fakeserver.DiagAt(1, 6, 1, "unused constant x")}}
	serverCfg := fakeLspServerForTool(diags, true, false)
	serverCfg.Extensions = []string{".go"}
	cfg := config.LspToolsConfig{Servers: map[string]config.LspServerConfig{"fake": serverCfg}}

	pool := lsp.NewPool(time.Minute, 5*time.Second)
	t.Cleanup(pool.Close)

	edit := WithEditDiagnostics(NewEditFileTool(dir, false), cfg, dir, pool)
	res := edit.Execute(context.Background(), map[string]any{
		"path": file, "old_text": "x = 1", "new_text": "y = 2",
	})
	if res.IsError {
		t.Fatalf("edit: %s", res.ForLLM)
	}
	if !strings.Contains(res.ForLLM, "LSP errors detected in this file, please fix:") ||
		!strings.Contains(res.ForLLM, "ERROR [2:7] unused constant x") ||
		!strings.Contains(res.ForLLM, `<diagnostics file="a.go">`) {
		t.Fatalf("injected output:\n%s", res.ForLLM)
	}

	// Clean file (no error diagnostics): no injection at all. Separate
	// dir+pool: session pools are keyed (root, server) and would otherwise
	// reuse the error-diagnostics session above.
	dir2 := t.TempDir()
	file2 := filepath.Join(dir2, "b.go")
	if err := os.WriteFile(file2, []byte("package b\nconst y = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	pool2 := lsp.NewPool(time.Minute, 5*time.Second)
	t.Cleanup(pool2.Close)
	serverCfg2 := fakeLspServerForTool(nil, true, false)
	serverCfg2.Extensions = []string{".go"}
	cfg2 := config.LspToolsConfig{Servers: map[string]config.LspServerConfig{"fake": serverCfg2}}
	edit2 := WithEditDiagnostics(NewEditFileTool(dir2, false), cfg2, dir2, pool2)
	res2 := edit2.Execute(context.Background(), map[string]any{
		"path": file2, "old_text": "y = 1", "new_text": "z = 2",
	})
	if res2.IsError {
		t.Fatalf("edit2: %s", res2.ForLLM)
	}
	if strings.Contains(res2.ForLLM, "diagnostics") {
		t.Fatalf("clean file must inject nothing:\n%s", res2.ForLLM)
	}

	// Failed edit: injection must not run (error result passes through).
	res3 := edit2.Execute(context.Background(), map[string]any{
		"path": file2, "old_text": "no-such-text", "new_text": "x",
	})
	if !res3.IsError || strings.Contains(res3.ForLLM, "LSP errors detected") {
		t.Fatalf("error result must pass through unwrapped: %+v", res3)
	}
}

func TestEditDiagnosticsInjectionDisabled(t *testing.T) {
	dir := t.TempDir()
	off := false
	cfg := config.LspToolsConfig{InjectOnEdit: &off}
	tool := NewEditFileTool(dir, false)
	wrapped := WithEditDiagnostics(tool, cfg, dir, nil)
	if _, same := wrapped.(*EditFileTool); !same {
		t.Fatalf("inject_on_edit=false must return the tool unwrapped, got %T", wrapped)
	}
}
