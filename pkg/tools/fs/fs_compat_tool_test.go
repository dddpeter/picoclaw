package fstools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/text/encoding/simplifiedchinese"
)

// TestEditFileTool_CRLFFile_MatchesLFNeedle verifies the core Windows fix:
// old_text written with "\n" matches a CRLF file, the replacement adopts the
// file's CRLF style, and no mixed line endings are introduced. Runs through
// both the host and the sandbox filesystem paths.
func TestEditFileTool_CRLFFile_MatchesLFNeedle(t *testing.T) {
	for _, tc := range []struct {
		name     string
		restrict bool
	}{
		{"host", false},
		{"sandbox", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workspace := t.TempDir()
			testFile := filepath.Join(workspace, "win.txt")
			if err := os.WriteFile(testFile, []byte("first\r\nsecond\r\n"), 0o644); err != nil {
				t.Fatal(err)
			}

			tool := NewEditFileTool(workspace, tc.restrict)
			result := tool.Execute(context.Background(), map[string]any{
				"path":     testFile,
				"old_text": "first\nsecond",
				"new_text": "alpha\nbeta",
			})
			if result.IsError {
				t.Fatalf("unexpected error: %s", result.ForLLM)
			}

			data, err := os.ReadFile(testFile)
			if err != nil {
				t.Fatal(err)
			}
			if want := "alpha\r\nbeta\r\n"; string(data) != want {
				t.Fatalf("file = %q, want %q", data, want)
			}
		})
	}
}

func TestEditFileTool_PreservesGB18030Encoding(t *testing.T) {
	workspace := t.TempDir()
	testFile := filepath.Join(workspace, "gbk.txt")
	original := "第一行：中文内容\n第二行\n"
	raw, err := simplifiedchinese.GB18030.NewEncoder().String(original)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(testFile, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}

	// The model always works in UTF-8 — the needle below is what read_file
	// would have shown it, not the GB18030 bytes on disk.
	tool := NewEditFileTool(workspace, false)
	result := tool.Execute(context.Background(), map[string]any{
		"path":     testFile,
		"old_text": "第一行：中文内容",
		"new_text": "已修改的行",
	})
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.ForLLM)
	}

	data, err := os.ReadFile(testFile)
	if err != nil {
		t.Fatal(err)
	}
	// On-disk form must still be GB18030, not silently converted to UTF-8.
	decoded, err := simplifiedchinese.GB18030.NewDecoder().Bytes(data)
	if err != nil {
		t.Fatalf("file is no longer valid GB18030: %v (%q)", err, data)
	}
	if want := "已修改的行\n第二行\n"; string(decoded) != want {
		t.Fatalf("decoded = %q, want %q", decoded, want)
	}
	if !strings.Contains(string(data), string([]byte{0xB8, 0xC4})) { // "改" in GBK
		t.Fatalf("file appears to have been rewritten as UTF-8: %q", data)
	}
}

func TestAppendFileTool_CRLFFileAdaptsAppendedEOLs(t *testing.T) {
	workspace := t.TempDir()
	testFile := filepath.Join(workspace, "log.txt")
	if err := os.WriteFile(testFile, []byte("line one\r\nline two\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := NewAppendFileTool(workspace, false)
	result := tool.Execute(context.Background(), map[string]any{
		"path":    testFile,
		"content": "line three\nline four",
	})
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.ForLLM)
	}

	data, err := os.ReadFile(testFile)
	if err != nil {
		t.Fatal(err)
	}
	if want := "line one\r\nline two\r\nline three\r\nline four"; string(data) != want {
		t.Fatalf("file = %q, want %q", data, want)
	}
}

func TestAppendFileTool_LFFileUntouched(t *testing.T) {
	workspace := t.TempDir()
	testFile := filepath.Join(workspace, "unix.txt")
	if err := os.WriteFile(testFile, []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := NewAppendFileTool(workspace, false)
	result := tool.Execute(context.Background(), map[string]any{
		"path":    testFile,
		"content": "b\r\nc",
	})
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.ForLLM)
	}
	data, err := os.ReadFile(testFile)
	if err != nil {
		t.Fatal(err)
	}
	// LF files keep legacy byte-for-byte append semantics.
	if want := "a\nb\r\nc"; string(data) != want {
		t.Fatalf("file = %q, want %q", data, want)
	}
}

func TestReadFileLinesTool_CRLFStrippedFromOutput(t *testing.T) {
	workspace := t.TempDir()
	testFile := filepath.Join(workspace, "crlf.txt")
	if err := os.WriteFile(testFile, []byte("alpha\r\nbeta\r\ngamma"), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := NewReadFileLinesTool(workspace, false, MaxReadFileSize)
	result := tool.Execute(context.Background(), map[string]any{"path": testFile})
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.ForLLM)
	}
	if strings.Contains(result.ForLLM, "\r") {
		t.Fatalf("output contains carriage returns:\n%q", result.ForLLM)
	}
	for _, want := range []string{"1|alpha\n", "2|beta\n", "3|gamma"} {
		if !strings.Contains(result.ForLLM, want) {
			t.Fatalf("output missing %q:\n%s", want, result.ForLLM)
		}
	}
}

func TestReadFileLinesTool_GB18030Decoded(t *testing.T) {
	workspace := t.TempDir()
	testFile := filepath.Join(workspace, "chinese.txt")
	raw, err := simplifiedchinese.GB18030.NewEncoder().String("中文第一行\n中文第二行\n")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(testFile, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := NewReadFileLinesTool(workspace, false, MaxReadFileSize)
	result := tool.Execute(context.Background(), map[string]any{"path": testFile})
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.ForLLM)
	}
	if !strings.Contains(result.ForLLM, "encoding: gb18030") {
		t.Fatalf("header missing encoding note:\n%s", result.ForLLM)
	}
	for _, want := range []string{"1|中文第一行", "2|中文第二行"} {
		if !strings.Contains(result.ForLLM, want) {
			t.Fatalf("output missing %q:\n%s", want, result.ForLLM)
		}
	}
}

func TestReadFileLinesTool_UTF8BOMStripped(t *testing.T) {
	workspace := t.TempDir()
	testFile := filepath.Join(workspace, "bom.txt")
	if err := os.WriteFile(testFile, []byte(utf8BOM+"hello\nworld\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := NewReadFileLinesTool(workspace, false, MaxReadFileSize)
	result := tool.Execute(context.Background(), map[string]any{"path": testFile})
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.ForLLM)
	}
	if strings.Contains(result.ForLLM, "\ufeff") {
		t.Fatalf("BOM leaked into output:\n%q", result.ForLLM)
	}
	if !strings.Contains(result.ForLLM, "encoding: utf-8 (BOM)") {
		t.Fatalf("header missing BOM note:\n%s", result.ForLLM)
	}
	if !strings.Contains(result.ForLLM, "1|hello") {
		t.Fatalf("first line corrupted:\n%s", result.ForLLM)
	}
}

func TestReadFileLinesTool_BinaryStillRejected(t *testing.T) {
	workspace := t.TempDir()
	testFile := filepath.Join(workspace, "blob.bin")
	if err := os.WriteFile(testFile, []byte{0x00, 0x01, 0x02, 0x00, 0x03}, 0o644); err != nil {
		t.Fatal(err)
	}

	tool := NewReadFileLinesTool(workspace, false, MaxReadFileSize)
	result := tool.Execute(context.Background(), map[string]any{"path": testFile})
	if !result.IsError {
		t.Fatalf("expected binary rejection, got:\n%s", result.ForLLM)
	}
	if !strings.Contains(result.ForLLM, "binary") {
		t.Fatalf("error = %s, want binary notice", result.ForLLM)
	}
}
