package pico

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
)

func newUploadTestChannel(t *testing.T, workspace string, maxBytes int64) (*PicoChannel, *bus.MessageBus) {
	t.Helper()

	msgBus := bus.NewMessageBus()
	bc := &config.Channel{Type: config.ChannelPico, Enabled: true}
	cfg := &config.PicoSettings{}
	cfg.SetToken("test-token")
	ch, err := NewPicoChannel(bc, cfg, msgBus)
	if err != nil {
		t.Fatalf("NewPicoChannel: %v", err)
	}
	ch.ctx = context.Background()
	ch.SetRunning(true)
	ch.SetUploadOptions(workspace, maxBytes)
	return ch, msgBus
}

func multipartBody(t *testing.T, filename string, content []byte) (*bytes.Buffer, string) {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}
	if _, err := part.Write(content); err != nil {
		t.Fatalf("write part: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	return &body, writer.FormDataContentType()
}

func TestHandleUpload_Success(t *testing.T) {
	workspace := t.TempDir()
	ch, _ := newUploadTestChannel(t, workspace, 1024*1024)

	content := []byte("id,name\n1,alpha\n")
	body, contentType := multipartBody(t, "sales.csv", content)

	req := httptest.NewRequest(http.MethodPost, "/pico/upload", body)
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Authorization", "Bearer test-token")
	rec := httptest.NewRecorder()
	ch.handleUpload(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Path        string `json:"path"`
		Filename    string `json:"filename"`
		ContentType string `json:"content_type"`
		Size        int    `json:"size"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Filename != "sales.csv" {
		t.Fatalf("filename = %q, want sales.csv", resp.Filename)
	}
	if resp.Size != len(content) {
		t.Fatalf("size = %d, want %d", resp.Size, len(content))
	}
	if !strings.HasPrefix(resp.Path, "inbound/") {
		t.Fatalf("path = %q, want inbound/ prefix", resp.Path)
	}
	if strings.Contains(resp.Path, "\\") {
		t.Fatalf("path = %q, want slash separators", resp.Path)
	}

	// The referenced file exists on disk with the promised content.
	abs := filepath.Join(workspace, filepath.FromSlash(resp.Path))
	data, err := os.ReadFile(abs)
	if err != nil {
		t.Fatalf("read uploaded file: %v", err)
	}
	if !bytes.Equal(data, content) {
		t.Fatalf("uploaded content mismatch: %q", data)
	}

	// The index records the upload for the file manager.
	index, err := os.ReadFile(filepath.Join(workspace, uploadInboundDir, uploadIndexName))
	if err != nil {
		t.Fatalf("read index: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(index)), "\n")
	if len(lines) != 1 {
		t.Fatalf("index lines = %d, want 1", len(lines))
	}
	var entry uploadIndexEntry
	if err := json.Unmarshal([]byte(lines[0]), &entry); err != nil {
		t.Fatalf("index line not JSON: %v", err)
	}
	if entry.Path != resp.Path || entry.Name != "sales.csv" {
		t.Fatalf("index entry = %+v", entry)
	}
}

func TestHandleUpload_Unauthenticated(t *testing.T) {
	ch, _ := newUploadTestChannel(t, t.TempDir(), 1024*1024)
	body, contentType := multipartBody(t, "a.txt", []byte("x"))
	req := httptest.NewRequest(http.MethodPost, "/pico/upload", body)
	req.Header.Set("Content-Type", contentType)
	rec := httptest.NewRecorder()
	ch.handleUpload(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestHandleUpload_TooLarge(t *testing.T) {
	ch, _ := newUploadTestChannel(t, t.TempDir(), 16)
	body, contentType := multipartBody(t, "big.bin", bytes.Repeat([]byte("x"), 64))
	req := httptest.NewRequest(http.MethodPost, "/pico/upload", body)
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Authorization", "Bearer test-token")
	rec := httptest.NewRecorder()
	ch.handleUpload(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", rec.Code)
	}
}

func TestHandleUpload_NotConfigured(t *testing.T) {
	ch := newTestPicoChannel(t) // no SetUploadOptions
	body, contentType := multipartBody(t, "a.txt", []byte("x"))
	req := httptest.NewRequest(http.MethodPost, "/pico/upload", body)
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Authorization", "Bearer test-token")
	rec := httptest.NewRecorder()
	ch.handleUpload(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}

func TestHandleUpload_MissingFileField(t *testing.T) {
	ch, _ := newUploadTestChannel(t, t.TempDir(), 1024*1024)
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	_ = writer.WriteField("filename", "no-file")
	_ = writer.Close()
	req := httptest.NewRequest(http.MethodPost, "/pico/upload", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Authorization", "Bearer test-token")
	rec := httptest.NewRecorder()
	ch.handleUpload(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandleUpload_SanitizesHostileFilename(t *testing.T) {
	workspace := t.TempDir()
	ch, _ := newUploadTestChannel(t, workspace, 1024*1024)
	body, contentType := multipartBody(t, "../../etc/cron.d/evil.sh", []byte("x"))

	req := httptest.NewRequest(http.MethodPost, "/pico/upload", body)
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Authorization", "Bearer test-token")
	rec := httptest.NewRecorder()
	ch.handleUpload(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Path string `json:"path"`
	}
	_ = json.NewDecoder(rec.Body).Decode(&resp)
	abs := filepath.Join(workspace, filepath.FromSlash(resp.Path))
	if !strings.HasPrefix(abs, filepath.Join(workspace, uploadInboundDir)) {
		t.Fatalf("path %q escapes inbound dir", resp.Path)
	}
	// The original traversal components must not survive sanitization.
	if strings.Contains(resp.Path, "..") || strings.Contains(resp.Path, "cron.d") {
		t.Fatalf("path %q kept traversal parts", resp.Path)
	}
}

func TestSanitizeUploadFilename(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"sales.csv", "sales.csv"},
		{"../../etc/passwd", "passwd"},
		{".", "upload.bin"},
		{"..", "upload.bin"},
		{"  ", "upload.bin"},
		{"a\u0000b.txt", "ab.txt"},
		{"c:\\windows\\system32\\x.dll", "cwindowssystem32x.dll"}, // Linux Base keeps the whole string; separators stripped
	}
	for _, tc := range cases {
		if got := sanitizeUploadFilename(tc.in); got != tc.want {
			t.Errorf("sanitizeUploadFilename(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	long := strings.Repeat("a", 300) + ".txt"
	if got := sanitizeUploadFilename(long); len(got) != 200 { // 196 chars + ".txt"
		t.Errorf("long name length = %d, want 200", len(got))
	}
}

func TestValidateInboundFilePath(t *testing.T) {
	workspace := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workspace, uploadInboundDir), 0o755); err != nil {
		t.Fatal(err)
	}
	// A symlink pointing outside the workspace must be rejected — including
	// when the final file does not exist yet (ancestor symlink resolution).
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(workspace, uploadInboundDir, "escape")); err == nil {
		rel, err := validateInboundFilePath("inbound/escape/file.txt", workspace)
		if err == nil {
			t.Fatalf("symlink escape accepted: %q", rel)
		}
	}

	cases := []struct {
		in      string
		wantErr bool
	}{
		{"inbound/20260101/abc_sales.csv", false},
		{"inbound/x.bin", false},
		{"", true},
		{"/etc/passwd", true},
		{"inbound/../secrets.txt", true},
		{"../outside.txt", true},
		{"outbound/x.txt", true},
		{"inbound", true}, // the dir itself is not a valid file ref
	}
	for _, tc := range cases {
		_, err := validateInboundFilePath(tc.in, workspace)
		if (err != nil) != tc.wantErr {
			t.Errorf("validateInboundFilePath(%q) err = %v, wantErr %v", tc.in, err, tc.wantErr)
		}
	}
}

func TestParseInboundFiles(t *testing.T) {
	workspace := t.TempDir()
	ch, _ := newUploadTestChannel(t, workspace, 1024*1024)

	payload := map[string]any{
		"files": []any{
			map[string]any{
				"path":         "inbound/20260101/abc_sales.csv",
				"filename":     "sales.csv",
				"content_type": "text/csv",
			},
			map[string]any{
				"path":         "inbound/20260101/abc_shot.png",
				"filename":     "shot.png",
				"content_type": "image/png",
			},
		},
	}
	files, tags, err := ch.parseInboundFiles(payload, workspace)
	if err != nil {
		t.Fatalf("parseInboundFiles: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("files = %d, want 2", len(files))
	}
	wantTags := []string{"[file:inbound/20260101/abc_sales.csv]", "[image:inbound/20260101/abc_shot.png]"}
	for i, want := range wantTags {
		if tags[i] != want {
			t.Fatalf("tag[%d] = %q, want %q", i, tags[i], want)
		}
	}

	if _, _, err := ch.parseInboundFiles(map[string]any{
		"files": []any{map[string]any{"path": "/etc/passwd"}},
	}, workspace); err == nil {
		t.Fatal("absolute path accepted, want error")
	}
	if _, _, err := ch.parseInboundFiles(map[string]any{
		"files": "not-an-array",
	}, workspace); err == nil {
		t.Fatal("non-array accepted, want error")
	}
}

func TestHandleMessageSend_FilesTagAndForward(t *testing.T) {
	workspace := t.TempDir()
	ch, msgBus := newUploadTestChannel(t, workspace, 1024*1024)

	ch.handleMessageSend(&picoConn{id: "conn-1", sessionID: "sess-files"}, PicoMessage{
		Type:      TypeMessageSend,
		ID:        "msg-files-1",
		SessionID: "sess-files",
		Payload: map[string]any{
			PayloadKeyContent: "分析这个数据集",
			"files": []any{
				map[string]any{
					"path":         "inbound/20260101/abc_sales.csv",
					"filename":     "sales.csv",
					"content_type": "text/csv",
				},
			},
		},
	})

	select {
	case inbound := <-msgBus.InboundChan():
		if !strings.Contains(inbound.Content, "分析这个数据集") {
			t.Fatalf("content lost user text: %q", inbound.Content)
		}
		if !strings.HasSuffix(inbound.Content, "[file:inbound/20260101/abc_sales.csv]") {
			t.Fatalf("content missing file tag: %q", inbound.Content)
		}
		if len(inbound.Files) != 1 {
			t.Fatalf("files = %d, want 1", len(inbound.Files))
		}
		if inbound.Files[0].Path != "inbound/20260101/abc_sales.csv" ||
			inbound.Files[0].Filename != "sales.csv" {
			t.Fatalf("file payload = %+v", inbound.Files[0])
		}
	case <-time.After(time.Second):
		t.Fatal("expected inbound message with files")
	}
}

func TestAppendFileTags_EmptyContent(t *testing.T) {
	got := appendFileTags("", []string{"[file:inbound/x]"})
	if got != "[file:inbound/x]" {
		t.Fatalf("appendFileTags empty = %q", got)
	}
	got = appendFileTags("hi", []string{"[file:a]", "[image:b]"})
	if got != "hi [file:a] [image:b]" {
		t.Fatalf("appendFileTags = %q", got)
	}
}
