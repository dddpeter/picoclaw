package pico

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFileForPreview(t *testing.T, workspace, rel, content string) string {
	t.Helper()
	abs := filepath.Join(workspace, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return abs
}

func authedFileRequest(ch *PicoChannel, target string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.Header.Set("Authorization", "Bearer test-token")
	rec := httptest.NewRecorder()
	ch.handleFileRead(rec, req)
	return rec
}

func TestHandleFileRead_TextPreview(t *testing.T) {
	workspace := t.TempDir()
	ch, _ := newUploadTestChannel(t, workspace, 1024*1024)
	writeFileForPreview(t, workspace, "notes/report.md", "# Hello\n世界")

	rec := authedFileRequest(ch, "/pico/file?path=notes/report.md")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-Picoclaw-File-Kind"); got != "text" {
		t.Fatalf("kind = %q, want text", got)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/markdown") && !strings.Contains(ct, "text/plain") {
		t.Fatalf("content-type = %q", ct)
	}
	if disp := rec.Header().Get("Content-Disposition"); !strings.Contains(disp, "inline") {
		t.Fatalf("disposition = %q, want inline", disp)
	}
	if body := rec.Body.String(); !strings.Contains(body, "世界") {
		t.Fatalf("body missing content: %q", body)
	}
}

func TestHandleFileRead_ImageInlineAndForceDownload(t *testing.T) {
	workspace := t.TempDir()
	ch, _ := newUploadTestChannel(t, workspace, 1024*1024)
	writeFileForPreview(t, workspace, "shots/a.png", "\x89PNG\r\n\x1a\n")

	rec := authedFileRequest(ch, "/pico/file?path=shots/a.png")
	if rec.Code != http.StatusOK || rec.Header().Get("X-Picoclaw-File-Kind") != "image" {
		t.Fatalf("status=%d kind=%q", rec.Code, rec.Header().Get("X-Picoclaw-File-Kind"))
	}
	if disp := rec.Header().Get("Content-Disposition"); !strings.Contains(disp, "inline") {
		t.Fatalf("image disposition = %q, want inline", disp)
	}

	rec = authedFileRequest(ch, "/pico/file?path=shots/a.png&download=1")
	if disp := rec.Header().Get("Content-Disposition"); !strings.Contains(disp, "attachment") {
		t.Fatalf("download=1 disposition = %q", disp)
	}
}

func TestHandleFileRead_SvgForcedAttachment(t *testing.T) {
	workspace := t.TempDir()
	ch, _ := newUploadTestChannel(t, workspace, 1024*1024)
	writeFileForPreview(t, workspace, "logo.svg", `<svg onload="alert(1)"/>`)

	rec := authedFileRequest(ch, "/pico/file?path=logo.svg")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if disp := rec.Header().Get("Content-Disposition"); !strings.Contains(disp, "attachment") {
		t.Fatalf("svg disposition = %q, want attachment (XSS posture)", disp)
	}
}

func TestHandleFileRead_RangeSupport(t *testing.T) {
	workspace := t.TempDir()
	ch, _ := newUploadTestChannel(t, workspace, 1024*1024)
	writeFileForPreview(t, workspace, "data.bin", strings.Repeat("0123456789", 10))

	req := httptest.NewRequest(http.MethodGet, "/pico/file?path=data.bin", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	req.Header.Set("Range", "bytes=0-4")
	rec := httptest.NewRecorder()
	ch.handleFileRead(rec, req)
	if rec.Code != http.StatusPartialContent {
		t.Fatalf("status = %d, want 206", rec.Code)
	}
	if rec.Body.String() != "01234" {
		t.Fatalf("range body = %q", rec.Body.String())
	}
}

func TestHandleFileRead_HtmlServedAsTextForSandboxedPreview(t *testing.T) {
	workspace := t.TempDir()
	ch, _ := newUploadTestChannel(t, workspace, 1024*1024)
	writeFileForPreview(t, workspace, "page.html", "<html><body><h1>hi</h1></body></html>")

	rec := authedFileRequest(ch, "/pico/file?path=page.html")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-Picoclaw-File-Kind"); got != "text" {
		t.Fatalf("kind = %q, want text (html renders in a sandboxed iframe)", got)
	}
	if disp := rec.Header().Get("Content-Disposition"); !strings.Contains(disp, "inline") {
		t.Fatalf("disposition = %q, want inline", disp)
	}
	if body := rec.Body.String(); !strings.Contains(body, "<h1>hi</h1>") {
		t.Fatalf("body missing markup: %q", body)
	}
}

func TestHandleFileRead_Denials(t *testing.T) {
	workspace := t.TempDir()
	ch, _ := newUploadTestChannel(t, workspace, 1024*1024)

	// Workspace-external absolute path → 403.
	outside := writeFileForPreview(t, t.TempDir(), "secret.txt", "x")
	if rec := authedFileRequest(ch, "/pico/file?path="+outside); rec.Code != http.StatusForbidden {
		t.Fatalf("outside status = %d, want 403", rec.Code)
	}
	// System directory → 403 via the shared veto.
	if rec := authedFileRequest(ch, "/pico/file?path=/etc/passwd"); rec.Code != http.StatusForbidden {
		t.Fatalf("etc status = %d, want 403", rec.Code)
	}
	// Missing → 404.
	if rec := authedFileRequest(ch, "/pico/file?path=notes/missing.md"); rec.Code != http.StatusNotFound {
		t.Fatalf("missing status = %d, want 404", rec.Code)
	}
	// Directory → 400.
	if err := os.MkdirAll(filepath.Join(workspace, "adir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if rec := authedFileRequest(ch, "/pico/file?path=adir"); rec.Code != http.StatusBadRequest {
		t.Fatalf("dir status = %d, want 400", rec.Code)
	}
	// Unauthenticated → 401.
	req := httptest.NewRequest(http.MethodGet, "/pico/file?path=adir", nil)
	rec := httptest.NewRecorder()
	ch.handleFileRead(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauth status = %d, want 401", rec.Code)
	}
	// Not configured → 503.
	bare := newTestPicoChannel(t)
	if rec := authedFileRequest(bare, "/pico/file?path=x"); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("unconfigured status = %d, want 503", rec.Code)
	}
}

func TestHandleFileRead_AccessRoots(t *testing.T) {
	workspace := t.TempDir()
	extra := t.TempDir()
	target := writeFileForPreview(t, extra, "proj/main.go", "package main")

	ch, _ := newUploadTestChannel(t, workspace, 1024*1024)
	// Before configuring the root: denied.
	if rec := authedFileRequest(ch, "/pico/file?path="+target); rec.Code != http.StatusForbidden {
		t.Fatalf("pre-root status = %d, want 403", rec.Code)
	}
	ch.SetFileAccessRoots([]string{extra})
	if rec := authedFileRequest(ch, "/pico/file?path="+target); rec.Code != http.StatusOK {
		t.Fatalf("post-root status = %d, body=%s", rec.Code, rec.Body.String())
	}
	// Relative root entries resolve against the workspace.
	ch2, _ := newUploadTestChannel(t, workspace, 1024*1024)
	ch2.SetFileAccessRoots([]string{"allowed"})
	writeFileForPreview(t, workspace, "allowed/ok.txt", "ok")
	if rec := authedFileRequest(ch2, "/pico/file?path=allowed/ok.txt"); rec.Code != http.StatusOK {
		t.Fatalf("relative-root status = %d, want 200", rec.Code)
	}
}

func TestHandleTree_ListingAndRules(t *testing.T) {
	workspace := t.TempDir()
	ch, _ := newUploadTestChannel(t, workspace, 1024*1024)
	writeFileForPreview(t, workspace, "inbound/20260118/a.csv", "x")
	writeFileForPreview(t, workspace, "inbound/20260118/b.csv", "y")
	writeFileForPreview(t, workspace, ".hidden/secret", "x")
	writeFileForPreview(t, workspace, "node_modules/pkg/index.js", "x")
	writeFileForPreview(t, workspace, "top.md", "# t")

	req := httptest.NewRequest(http.MethodGet, "/pico/tree?path=inbound&depth=2", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	rec := httptest.NewRecorder()
	ch.handleTree(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"inbound/20260118/a.csv"`) {
		t.Fatalf("missing recursive entry: %s", body)
	}
	if strings.Contains(body, "secret") || strings.Contains(body, "node_modules") {
		t.Fatalf("hidden entries leaked: %s", body)
	}

	// Root listing hides dot entries but shows top-level files.
	req = httptest.NewRequest(http.MethodGet, "/pico/tree", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	rec = httptest.NewRecorder()
	ch.handleTree(rec, req)
	body = rec.Body.String()
	if !strings.Contains(body, `"top.md"`) {
		t.Fatalf("root listing missing top.md: %s", body)
	}
	if strings.Contains(body, `.hidden`) {
		t.Fatalf("root listing leaked .hidden: %s", body)
	}

	// Outside the workspace → 403; missing → 404; invalid depth → 400.
	req = httptest.NewRequest(http.MethodGet, "/pico/tree?path=/etc", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	rec = httptest.NewRecorder()
	ch.handleTree(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("etc status = %d", rec.Code)
	}
	req = httptest.NewRequest(http.MethodGet, "/pico/tree?path=nope&depth=x", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	rec = httptest.NewRecorder()
	ch.handleTree(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad depth status = %d", rec.Code)
	}
	// Unauthenticated → 401.
	req = httptest.NewRequest(http.MethodGet, "/pico/tree", nil)
	rec = httptest.NewRecorder()
	ch.handleTree(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauth status = %d", rec.Code)
	}
}

func TestHandleTree_Truncation(t *testing.T) {
	workspace := t.TempDir()
	ch, _ := newUploadTestChannel(t, workspace, 1024*1024)
	dir := filepath.Join(workspace, "many")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < maxTreeEntries+5; i++ {
		if err := os.WriteFile(filepath.Join(dir, "f"+strings.Repeat("0", 6)+"_"+itoa(i)+".txt"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/pico/tree?path=many", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	rec := httptest.NewRecorder()
	ch.handleTree(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"truncated":true`) {
		t.Fatalf("expected truncated=true: %s", body[:200])
	}
	if got := strings.Count(body, `"is_dir"`); got > maxTreeEntries {
		t.Fatalf("entries = %d, cap %d", got, maxTreeEntries)
	}
	_, _ = io.Discard, body
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}
