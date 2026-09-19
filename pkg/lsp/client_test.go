package lsp

import (
	"bufio"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/lsp/fakeserver"
)

// TestFakeServerHelper is not a real test: it is the fake-server process
// entry point (see pkg/lsp/fakeserver for the env contract).
func TestFakeServerHelper(t *testing.T) {
	if !fakeserver.Active() {
		t.Skip("helper process entry point only")
	}
	fakeserver.Serve(os.Stdin, os.Stdout)
	os.Exit(0)
}

// fakeServerConfig returns a ServerConfig whose command re-invokes this
// test binary as a fake server.
func fakeServerConfig() ServerConfig {
	return ServerConfig{
		Name:    "fake",
		Command: os.Args[0],
		Args:    []string{"-test.run=^TestFakeServerHelper$", "--"},
	}
}

// startFakeClient launches a fake-server-backed client with the given env.
func startFakeClient(t *testing.T, env map[string]string) *Client {
	t.Helper()
	dir := t.TempDir()
	cfg := fakeServerConfig()
	cfg.Env = env
	c, err := Start(context.Background(), cfg, dir, 5*time.Second)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { c.Shutdown(time.Second) })
	return c
}

func docKeyForPath(path string) string { return DocumentKey(FileURI(path)) }

func diagAt(line, char, severity int, message string) fakeserver.Diag {
	return fakeserver.DiagAt(line, char, severity, message)
}

func TestClientPullDiagnostics(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "a.go")
	if err := os.WriteFile(file, []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	diags := map[string][]fakeserver.Diag{
		docKeyForPath(file): {
			diagAt(1, 4, 1, "undefined: b"),
			diagAt(2, 0, 2, "unused import"),
		},
	}
	c := startFakeClient(t, fakeserver.Env(diags, true, false))

	uri, err := c.TouchFile(context.Background(), file, "package a\n", "go")
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.Diagnostics(context.Background(), uri)
	if err != nil {
		t.Fatalf("Diagnostics: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("diagnostics = %d, want 2: %+v", len(got), got)
	}
	if got[0].Message != "undefined: b" || got[0].Severity != 1 {
		t.Fatalf("first diagnostic = %+v", got[0])
	}
}

func TestClientPushDiagnostics(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "a.go")
	if err := os.WriteFile(file, []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	diags := map[string][]fakeserver.Diag{
		docKeyForPath(file): {diagAt(0, 1, 1, "push error")},
	}
	// Push-only server (no pull capability) that publishes on didOpen.
	c := startFakeClient(t, fakeserver.Env(diags, false, true))
	c.cfg.PushDiagnosticsGraceMs = 3000

	uri, err := c.TouchFile(context.Background(), file, "package a\n", "go")
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.Diagnostics(context.Background(), uri)
	if err != nil {
		t.Fatalf("Diagnostics: %v", err)
	}
	if len(got) != 1 || got[0].Message != "push error" {
		t.Fatalf("diagnostics = %+v", got)
	}
}

func TestClientPushCleanFileWithGrace(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "b.go")
	if err := os.WriteFile(file, []byte("package b\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Push server that never publishes: with PushDiagnosticsGraceMs set,
	// silence means clean.
	cfg := fakeServerConfig()
	cfg.Env = fakeserver.Env(nil, false, false)
	cfg.PushDiagnosticsGraceMs = 300
	c, err := Start(context.Background(), cfg, dir, 3*time.Second)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { c.Shutdown(time.Second) })

	uri, err := c.TouchFile(context.Background(), file, "package b\n", "go")
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.Diagnostics(context.Background(), uri)
	if err != nil {
		t.Fatalf("Diagnostics: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("want clean, got %+v", got)
	}
}

func TestClientTouchFileSendsDidChangeOnSecondTouch(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "c.go")
	if err := os.WriteFile(file, []byte("package c\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	diags := map[string][]fakeserver.Diag{docKeyForPath(file): {diagAt(0, 0, 1, "x")}}
	c := startFakeClient(t, fakeserver.Env(diags, true, true))

	ctx := context.Background()
	if _, err := c.TouchFile(ctx, file, "package c\n", "go"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.TouchFile(ctx, file, "package c // v2\n", "go"); err != nil {
		t.Fatal(err)
	}
	got, err := c.Diagnostics(ctx, FileURI(file))
	if err != nil || len(got) != 1 {
		t.Fatalf("diagnostics = %+v, err = %v", got, err)
	}
}

func TestDocumentKeyNormalizesWindowsEncodings(t *testing.T) {
	a := DocumentKey("file:///C:/dir/A%20B.go")
	b := DocumentKey("file:///c%3A/dir/A B.go")
	if a != b {
		t.Fatalf("keys differ: %q vs %q", a, b)
	}
}

func TestPoolReusesSessionsAndBreaksFailures(t *testing.T) {
	dir := t.TempDir() // must register BEFORE pool.Close: LIFO runs Close first
	pool := NewPool(time.Minute, 5*time.Second)
	t.Cleanup(pool.Close)

	cfg := fakeServerConfig()
	cfg.Env = fakeserver.Env(nil, true, false)
	c1, err := pool.Acquire(context.Background(), cfg, dir)
	if err != nil {
		t.Fatalf("acquire 1: %v", err)
	}
	c2, err := pool.Acquire(context.Background(), cfg, dir)
	if err != nil {
		t.Fatalf("acquire 2: %v", err)
	}
	if c1 != c2 {
		t.Fatal("pool must reuse the live session for the same (root, server)")
	}
	if pool.SessionCount() != 1 {
		t.Fatalf("sessions = %d, want 1", pool.SessionCount())
	}

	// A failing server gets circuit-broken for the process lifetime.
	bad := fakeServerConfig()
	bad.Name = "broken"
	bad.Command = "picoclaw-no-such-lsp-command-xyz"
	if _, err := pool.Acquire(context.Background(), bad, dir); err == nil {
		t.Fatal("expected start failure")
	}
	if _, err := pool.Acquire(context.Background(), bad, dir); err == nil {
		t.Fatal("second acquire must fail fast via the broken set")
	} else if _, ok := pool.Broken(dir, "broken"); !ok {
		t.Fatal("broken set must record the failure")
	}
}

func TestPoolConcurrentAcquireSingleSession(t *testing.T) {
	dir := t.TempDir() // must register BEFORE pool.Close: LIFO runs Close first
	pool := NewPool(time.Minute, 5*time.Second)
	t.Cleanup(pool.Close)
	cfg := fakeServerConfig()
	cfg.Env = fakeserver.Env(nil, true, false)

	type result struct {
		c   *Client
		err error
	}
	results := make(chan result, 4)
	for range 4 {
		go func() {
			c, err := pool.Acquire(context.Background(), cfg, dir)
			results <- result{c, err}
		}()
	}
	var first *Client
	for range 4 {
		r := <-results
		if r.err != nil {
			t.Fatalf("acquire: %v", r.err)
		}
		if first == nil {
			first = r.c
		} else if r.c != first {
			t.Fatal("concurrent acquires must share one session (spawning dedup)")
		}
	}
	if pool.SessionCount() != 1 {
		t.Fatalf("sessions = %d, want 1", pool.SessionCount())
	}
}

func TestPoolReapsIdleSessions(t *testing.T) {
	dir := t.TempDir() // must register BEFORE pool.Close: LIFO runs Close first
	pool := NewPool(50*time.Millisecond, 3*time.Second)
	t.Cleanup(pool.Close)
	cfg := fakeServerConfig()
	cfg.Env = fakeserver.Env(nil, true, false)

	if _, err := pool.Acquire(context.Background(), cfg, dir); err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if pool.SessionCount() != 1 {
		t.Fatalf("sessions = %d, want 1", pool.SessionCount())
	}
	time.Sleep(60 * time.Millisecond)
	pool.reapIdle()
	if got := pool.SessionCount(); got != 0 {
		t.Fatalf("sessions after reap = %d, want 0", got)
	}
}

func TestFramingHeaderParsing(t *testing.T) {
	r := bufio.NewReader(strings.NewReader("Content-Type: application/vscode-jsonrpc; charset=utf-8\r\ncontent-length: 7\r\n\r\n{}"))
	n, err := readFrameHeader(r)
	if err != nil || n != 7 {
		t.Fatalf("readFrameHeader = %d, %v; want 7", n, err)
	}
}

func TestFileURIUsesAuthorityLessForm(t *testing.T) {
	// The canonical file URI form is file:///path — a Windows drive path
	// rendered as file://C:/... would make the drive letter the URI host,
	// and real servers reply with the canonical form, breaking matching.
	dir := t.TempDir()
	p := filepath.Join(dir, "a b.go")
	uri := FileURI(p)
	if !strings.HasPrefix(uri, "file:///") {
		t.Fatalf("FileURI = %q, want file:/// prefix", uri)
	}
	// Round trip: DocumentKey must resolve back to the same key regardless
	// of encoding form.
	lower := strings.ToLower(uri)
	if DocumentKey(uri) != DocumentKey(lower) {
		t.Fatalf("DocumentKey case sensitivity mismatch: %q vs %q", DocumentKey(uri), DocumentKey(lower))
	}
}

func TestShutdownSendsGracefulHandshake(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "shutdown.marker")
	cfg := fakeServerConfig()
	cfg.Env = fakeserver.Env(nil, true, false)
	cfg.Env["PICOCLAW_FAKESERVER_SHUTDOWN_MARKER"] = marker

	c, err := Start(context.Background(), cfg, dir, 3*time.Second)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	c.Shutdown(2 * time.Second)

	if data, err := os.ReadFile(marker); err != nil || string(data) != "shutdown" {
		t.Fatalf("graceful shutdown request was not delivered (marker: %v)", err)
	}
}
