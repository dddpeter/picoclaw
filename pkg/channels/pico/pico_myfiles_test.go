package pico

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTranscript(t *testing.T, dir, name string, lines []string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := strings.Join(lines, "\n")
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCollectMyFiles_UploadsAndGenerated(t *testing.T) {
	workspace := t.TempDir()
	ch, _ := newUploadTestChannel(t, workspace, 1024*1024)

	// an upload recorded in the inbound index
	if err := os.MkdirAll(filepath.Join(workspace, uploadInboundDir), 0o755); err != nil {
		t.Fatal(err)
	}
	indexLine := `{"id":"abcd1234","name":"resume.pdf","stored_name":"abcd1234_resume.pdf",` +
		`"path":"inbound/20261118/abcd1234_resume.pdf","size":167461,` +
		`"content_type":"application/pdf","mtime":"2026-11-18T04:19:15Z","session":"s1"}`
	if err := os.WriteFile(
		filepath.Join(workspace, uploadInboundDir, uploadIndexName),
		[]byte(indexLine+"\n"), 0o644,
	); err != nil {
		t.Fatal(err)
	}

	// generated files recovered from a transcript
	sessions := filepath.Join(workspace, "sessions")
	writeTranscript(t, sessions, "s1.jsonl", []string{
		`{"role":"assistant","content":"done","tool_calls":[{"id":"c1","type":"function","function":{"name":"write_file","arguments":"{\"path\":\"out/report.md\",\"content\":\"# hi\"}"}}]}`,
		`{"role":"user","content":"see [file:inbound/20261118/abcd1234_resume.pdf] and [image:out/chart.png]"}`,
		`{"role":"tool","content":"truncated\n\nFull output: /tmp/ws/shell-output-1.log"}`,
	})
	ch.SetSessionFileScanner(func() string { return sessions })

	files := ch.collectMyFiles()
	byPath := make(map[string]MyFile)
	for _, file := range files {
		byPath[file.Path] = file
	}

	upload, ok := byPath["inbound/20261118/abcd1234_resume.pdf"]
	if !ok {
		t.Fatalf("upload missing, got %v", byPath)
	}
	if upload.Origin != "upload" {
		t.Fatalf("upload origin = %q", upload.Origin)
	}
	if upload.Filename != "resume.pdf" {
		t.Fatalf("upload filename = %q", upload.Filename)
	}

	generated, ok := byPath["out/report.md"]
	if !ok {
		t.Fatalf("generated write_file path missing, got %v", byPath)
	}
	if generated.Origin != "generated" {
		t.Fatalf("generated origin = %q", generated.Origin)
	}
	if _, ok := byPath["out/chart.png"]; !ok {
		t.Fatalf("inline tag path missing, got %v", byPath)
	}
	// Paths outside the workspace are excluded: the web allowlist would
	// refuse them, so listing them would only produce dead entries.
	if _, ok := byPath["/tmp/ws/shell-output-1.log"]; ok {
		t.Fatalf("workspace-external path listed, got %v", byPath)
	}
}

func TestNormalizeToWorkspacePath(t *testing.T) {
	workspace := t.TempDir()
	cases := []struct {
		in    string
		want  string
		allow bool
	}{
		{"out/report.md", "out/report.md", true},
		{"memory/202610/20261001.md", "memory/202610/20261001.md", true},
		{"/etc/passwd", "", false},
		{".../config.yaml", "", false},
		{"", "", false},
	}
	for _, tc := range cases {
		got, ok := normalizeToWorkspacePath(workspace, tc.in)
		if ok != tc.allow {
			t.Errorf("normalizeToWorkspacePath(%q) ok = %v, want %v", tc.in, ok, tc.allow)
			continue
		}
		if ok && got != tc.want {
			t.Errorf("normalizeToWorkspacePath(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestCollectMyFiles_SkipsNoise(t *testing.T) {
	workspace := t.TempDir()
	ch, _ := newUploadTestChannel(t, workspace, 1024*1024)

	sessions := filepath.Join(workspace, "sessions")
	writeTranscript(t, sessions, "s2.jsonl", []string{
		// version-like token, not a file
		`{"role":"assistant","content":"model v1.0.2"}`,
		// read-only tool: not a producer
		`{"role":"assistant","content":"","tool_calls":[{"id":"c2","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"docs/only-read.md\"}"}}]}`,
		// write tool with a real path
		`{"role":"assistant","content":"","tool_calls":[{"id":"c3","type":"function","function":{"name":"write_file","arguments":"{\"path\":\"docs/real.md\"}"}}]}`,
	})
	ch.SetSessionFileScanner(func() string { return sessions })

	byPath := make(map[string]bool)
	for _, file := range ch.collectMyFiles() {
		byPath[file.Path] = true
	}
	if byPath["v1.0.2"] {
		t.Fatal("version token treated as a file")
	}
	if byPath["docs/only-read.md"] {
		t.Fatal("read-only tool path treated as generated")
	}
	if !byPath["docs/real.md"] {
		t.Fatalf("write_file path missing, got %v", byPath)
	}
}

func TestReadUploadIndex_SkipsCorruptLines(t *testing.T) {
	workspace := t.TempDir()
	dir := filepath.Join(workspace, uploadInboundDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"id":"a","name":"ok.txt","path":"inbound/a.txt"}
not-json
{"id":"b","name":"ok2.txt","path":"inbound/b.txt"}
`
	if err := os.WriteFile(filepath.Join(dir, uploadIndexName), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	entries := readUploadIndex(workspace)
	if len(entries) != 2 {
		t.Fatalf("entries = %d, want 2 (corrupt line skipped)", len(entries))
	}
}

func TestHandleMyFiles_Endpoint(t *testing.T) {
	workspace := t.TempDir()
	ch, _ := newUploadTestChannel(t, workspace, 1024*1024)
	ch.SetSessionFileScanner(func() string { return filepath.Join(workspace, "sessions") })

	req := httptest.NewRequest(http.MethodGet, "/pico/myfiles", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	rec := httptest.NewRecorder()
	ch.handleMyFiles(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Files []MyFile `json:"files"`
		Count int      `json:"count"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Count != len(resp.Files) {
		t.Fatalf("count = %d, files = %d", resp.Count, len(resp.Files))
	}

	// Unauthenticated → 401.
	req = httptest.NewRequest(http.MethodGet, "/pico/myfiles", nil)
	rec = httptest.NewRecorder()
	ch.handleMyFiles(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauth status = %d, want 401", rec.Code)
	}
}

func TestLooksLikeFilePath(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"out/report.md", true},
		{"out/chart.png", true},
		{"/tmp/ws/shell-output-1.log", true},
		{"v1.0.2", false},
		{"1.0", false},
		{"", false},
		{".", false},
		{"..", false},
		{"no-extension", false},
	}
	for _, tc := range cases {
		if got := looksLikeFilePath(tc.in); got != tc.want {
			t.Errorf("looksLikeFilePath(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}
