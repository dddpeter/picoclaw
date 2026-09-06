package tools

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTruncateHead_NoTruncationNeeded(t *testing.T) {
	content := "line one\nline two\nline three\n"
	result := TruncateHead(content, TruncationOptions{MaxLines: 10, MaxBytes: 1000})
	if result.Truncated {
		t.Fatalf("expected no truncation, got %+v", result)
	}
	if result.Content != content {
		t.Fatalf("expected content unchanged, got %q", result.Content)
	}
	if result.TotalLines != 3 {
		t.Fatalf("expected 3 lines, got %d", result.TotalLines)
	}
}

func TestTruncateHead_LineLimitKeepsHead(t *testing.T) {
	lines := make([]string, 20)
	for i := range lines {
		lines[i] = fmt.Sprintf("line-%02d", i)
	}
	content := strings.Join(lines, "\n")

	result := TruncateHead(content, TruncationOptions{MaxLines: 5, MaxBytes: 10000})
	if !result.Truncated {
		t.Fatal("expected truncation")
	}
	if result.TruncatedBy != TruncatedByLines {
		t.Fatalf("expected lines limit, got %q", result.TruncatedBy)
	}
	if result.OutputLines != 5 {
		t.Fatalf("expected 5 kept lines, got %d", result.OutputLines)
	}
	if result.Content != "line-00\nline-01\nline-02\nline-03\nline-04" {
		t.Fatalf("unexpected content: %q", result.Content)
	}
	if result.TotalLines != 20 {
		t.Fatalf("expected 20 total lines, got %d", result.TotalLines)
	}
}

func TestTruncateHead_ByteLimit(t *testing.T) {
	lines := make([]string, 10)
	for i := range lines {
		lines[i] = strings.Repeat("a", 50)
	}
	content := strings.Join(lines, "\n")

	// 4 lines = 4*50 + 3 newlines = 203 bytes; 5 lines = 254.
	result := TruncateHead(content, TruncationOptions{MaxLines: 100, MaxBytes: 203})
	if !result.Truncated || result.TruncatedBy != TruncatedByBytes {
		t.Fatalf("expected byte-limit truncation, got %+v", result)
	}
	if result.OutputLines != 4 {
		t.Fatalf("expected 4 kept lines, got %d", result.OutputLines)
	}
}

func TestTruncateHead_FirstLineExceedsLimit(t *testing.T) {
	content := strings.Repeat("x", 300) + "\nsecond line"
	result := TruncateHead(content, TruncationOptions{MaxLines: 10, MaxBytes: 100})
	if !result.Truncated || !result.FirstLineExceedsLimit {
		t.Fatalf("expected firstLineExceedsLimit, got %+v", result)
	}
	if result.Content != "" {
		t.Fatalf("expected empty content, got %q", result.Content)
	}
}

func TestTruncateTail_KeepsLastLines(t *testing.T) {
	lines := make([]string, 20)
	for i := range lines {
		lines[i] = fmt.Sprintf("line-%02d", i)
	}
	content := strings.Join(lines, "\n")

	result := TruncateTail(content, TruncationOptions{MaxLines: 5, MaxBytes: 10000})
	if !result.Truncated {
		t.Fatal("expected truncation")
	}
	if result.Content != "line-15\nline-16\nline-17\nline-18\nline-19" {
		t.Fatalf("unexpected content: %q", result.Content)
	}
	if result.OutputLines != 5 || result.TotalLines != 20 {
		t.Fatalf("unexpected accounting: %+v", result)
	}
}

func TestTruncateTail_ByteLimitPartialLineUTF8Safe(t *testing.T) {
	// One huge line of multi-byte runes: the tail cut must land on a rune
	// boundary and keep exactly the end.
	content := strings.Repeat("中", 100) // 300 bytes
	result := TruncateTail(content, TruncationOptions{MaxLines: 10, MaxBytes: 30})
	if !result.Truncated || !result.LastLinePartial {
		t.Fatalf("expected partial last line, got %+v", result)
	}
	if result.Content != strings.Repeat("中", 10) {
		t.Fatalf("unexpected content: %q", result.Content)
	}
	if !utf8.ValidString(result.Content) {
		t.Fatal("expected valid UTF-8 after truncation")
	}
}

func TestTruncateTail_Notice(t *testing.T) {
	lines := make([]string, 100)
	for i := range lines {
		lines[i] = fmt.Sprintf("line-%03d", i)
	}
	content := strings.Join(lines, "\n")

	result := TruncateTail(content, TruncationOptions{MaxLines: 10, MaxBytes: 10000})
	notice := result.Notice()
	if !strings.Contains(notice, "lines 91-100 of 100") {
		t.Fatalf("expected line-range notice, got %q", notice)
	}
	if !strings.HasSuffix(notice, "]") {
		t.Fatalf("expected bracketed notice, got %q", notice)
	}

	partial := TruncateTail(strings.Repeat("中", 100), TruncationOptions{MaxLines: 5, MaxBytes: 30})
	if !strings.Contains(partial.Notice(), "line 1") {
		t.Fatalf("expected partial-line notice, got %q", partial.Notice())
	}

	if notice := TruncateHead(content, TruncationOptions{MaxLines: 1000, MaxBytes: 100000}).Notice(); notice != "" {
		t.Fatalf("expected empty notice when not truncated, got %q", notice)
	}
}

func TestTruncateDefaultsApplied(t *testing.T) {
	lines := make([]string, DefaultMaxLines+10)
	for i := range lines {
		lines[i] = "x"
	}
	result := TruncateTail(strings.Join(lines, "\n"), TruncationOptions{})
	if !result.Truncated {
		t.Fatal("expected default line cap to trigger truncation")
	}
	if result.MaxLines != DefaultMaxLines || result.MaxBytes != DefaultMaxBytes {
		t.Fatalf("expected defaults resolved, got %+v", result)
	}
}
