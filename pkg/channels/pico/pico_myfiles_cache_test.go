package pico

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func newTestCache() *fileCache {
	return &fileCache{
		pathsByTranscript: make(map[string]map[string]struct{}),
		fingerprints:      make(map[string]transcriptFingerprint),
	}
}

func TestFileCache_ReusesUnchangedTranscripts(t *testing.T) {
	dir := t.TempDir()
	transcript := filepath.Join(dir, "s1.jsonl")
	body := `{"role":"assistant","content":"","tool_calls":[{"id":"c1","type":"function","function":{"name":"write_file","arguments":"{\"path\":\"out/a.md\"}"}}]}`
	if err := os.WriteFile(transcript, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	cache := newTestCache()
	_, paths := cache.collect(dir, "")
	if _, ok := paths["out/a.md"]; !ok {
		t.Fatalf("path missing, got %v", paths)
	}

	// Second call: the transcript is unchanged → cached paths reused.
	_, paths = cache.collect(dir, "")
	if _, ok := paths["out/a.md"]; !ok {
		t.Fatalf("cached path missing, got %v", paths)
	}
	scans, cached, _ := cache.stats()
	if cached != 1 {
		t.Fatalf("cached transcripts = %d, want 1", cached)
	}
	if scans != 2 {
		t.Fatalf("directory walks = %d, want 2 (one per call)", scans)
	}
}

func TestFileCache_RescansChangedTranscript(t *testing.T) {
	dir := t.TempDir()
	transcript := filepath.Join(dir, "s1.jsonl")
	if err := os.WriteFile(transcript,
		[]byte(`{"role":"assistant","content":"","tool_calls":[{"id":"c1","type":"function","function":{"name":"write_file","arguments":"{\"path\":\"out/a.md\"}"}}]}`),
		0o644); err != nil {
		t.Fatal(err)
	}

	cache := newTestCache()
	_, paths := cache.collect(dir, "")
	if _, ok := paths["out/a.md"]; !ok {
		t.Fatalf("path missing, got %v", paths)
	}

	// Append a new path; the transcript fingerprint changes → re-scanned.
	time.Sleep(10 * time.Millisecond)
	extra := `{"role":"assistant","content":"","tool_calls":[{"id":"c2","type":"function","function":{"name":"write_file","arguments":"{\"path\":\"out/b.md\"}"}}]}`
	f, err := os.OpenFile(transcript, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("\n" + extra); err != nil {
		t.Fatal(err)
	}
	f.Close()

	_, paths = cache.collect(dir, "")
	if _, ok := paths["out/b.md"]; !ok {
		t.Fatalf("new path missing after change, got %v", paths)
	}
}

func TestFileCache_DropsDeletedTranscripts(t *testing.T) {
	dir := t.TempDir()
	transcript := filepath.Join(dir, "s1.jsonl")
	if err := os.WriteFile(transcript,
		[]byte(`{"role":"assistant","content":"","tool_calls":[{"id":"c1","type":"function","function":{"name":"write_file","arguments":"{\"path\":\"out/a.md\"}"}}]}`),
		0o644); err != nil {
		t.Fatal(err)
	}

	cache := newTestCache()
	_, paths := cache.collect(dir, "")
	if len(paths) != 1 {
		t.Fatalf("paths = %v", paths)
	}
	if err := os.Remove(transcript); err != nil {
		t.Fatal(err)
	}
	_, paths = cache.collect(dir, "")
	if len(paths) != 0 {
		t.Fatalf("deleted transcript still cached: %v", paths)
	}
	_, cached, _ := cache.stats()
	if cached != 0 {
		t.Fatalf("cached transcripts = %d, want 0", cached)
	}
}

func TestFileCache_UploadIndexCachedUntilChanged(t *testing.T) {
	workspace := t.TempDir()
	dir := filepath.Join(workspace, uploadInboundDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	indexPath := filepath.Join(dir, uploadIndexName)
	line := `{"id":"a","name":"one.txt","path":"inbound/one.txt","size":10}` + "\n"
	if err := os.WriteFile(indexPath, []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}

	cache := newTestCache()
	uploads, _ := cache.collect("", workspace)
	if len(uploads) != 1 || uploads[0].Path != "inbound/one.txt" {
		t.Fatalf("uploads = %+v", uploads)
	}

	// Append a second upload → index fingerprint changes → re-read.
	time.Sleep(10 * time.Millisecond)
	f, err := os.OpenFile(indexPath, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"id":"b","name":"two.txt","path":"inbound/two.txt","size":20}` + "\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()

	uploads, _ = cache.collect("", workspace)
	if len(uploads) != 2 {
		t.Fatalf("uploads after append = %d, want 2", len(uploads))
	}
}

func TestFileCache_InvalidateClearsState(t *testing.T) {
	dir := t.TempDir()
	transcript := filepath.Join(dir, "s1.jsonl")
	if err := os.WriteFile(transcript,
		[]byte(`{"role":"assistant","content":"","tool_calls":[{"id":"c1","type":"function","function":{"name":"write_file","arguments":"{\"path\":\"out/a.md\"}"}}]}`),
		0o644); err != nil {
		t.Fatal(err)
	}

	cache := newTestCache()
	cache.collect(dir, "")
	cache.invalidate()
	_, cached, _ := cache.stats()
	if cached != 0 {
		t.Fatalf("cached transcripts after invalidate = %d, want 0", cached)
	}
}
