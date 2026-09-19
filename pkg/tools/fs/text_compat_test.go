package fstools

import (
	"strings"
	"testing"
)

func TestDetectEOLStyle(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{"empty", "", "\n"},
		{"no terminator", "abc", "\n"},
		{"pure LF", "a\nb\nc\n", "\n"},
		{"pure CRLF", "a\r\nb\r\nc\r\n", "\r\n"},
		{"pure CR", "a\rb\rc\r", "\r"},
		{"CRLF dominant", "a\r\nb\r\nc\n", "\r\n"},
		{"LF dominant", "a\r\nb\nc\nd\n", "\n"},
		{"CR dominant", "a\rb\rc\rd\r\n", "\r"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := detectEOLStyle(tt.content); got != tt.want {
				t.Fatalf("detectEOLStyle(%q) = %q, want %q", tt.content, got, tt.want)
			}
		})
	}
}

func TestNormalizeEOLToLF(t *testing.T) {
	tests := []struct{ in, want string }{
		{"a\r\nb", "a\nb"},
		{"a\rb", "a\nb"},
		{"a\r\nb\rc\nd", "a\nb\nc\nd"},
		{"plain", "plain"},
	}
	for _, tt := range tests {
		if got := normalizeEOLToLF(tt.in); got != tt.want {
			t.Fatalf("normalizeEOLToLF(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestNormalizeEOLWithMapping(t *testing.T) {
	orig := "ab\r\ncd\ref"
	normalized, mapping := normalizeEOLWithMapping(orig)
	if want := "ab\ncd\nef"; normalized != want {
		t.Fatalf("normalized = %q, want %q", normalized, want)
	}
	// Every normalized offset must map back to the byte that produced it
	// (a normalized \n produced from a CR compares as "\r").
	for i := 0; i < len(normalized); i++ {
		origByte := orig[mapping[i]]
		if origByte != normalized[i] && !(normalized[i] == '\n' && origByte == '\r') {
			t.Fatalf("mapping[%d] = %d, points at %q; want source of %q", i, mapping[i], origByte, normalized[i])
		}
	}
	if mapping[len(normalized)] != len(orig) {
		t.Fatalf("end sentinel = %d, want %d", mapping[len(normalized)], len(orig))
	}
}

func TestReplaceEditContent_CRLFFileWithLFNeedle(t *testing.T) {
	content := []byte("first line\r\nsecond line\r\nthird\r\n")
	got, err := replaceEditContent(content, "first line\nsecond line", "alpha\nbeta")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Replacement must adopt the file's CRLF style and leave the tail intact.
	if want := "alpha\r\nbeta\r\nthird\r\n"; string(got) != want {
		t.Fatalf("result = %q, want %q", got, want)
	}
	if strings.Contains(string(got), "\n") && strings.Contains(string(got), "\r\n") {
		// mixed endings check: every \n must be part of \r\n
		if normalizeEOLToLF(string(got)) != strings.ReplaceAll(string(got), "\r\n", "\n") {
			t.Fatalf("mixed line endings introduced: %q", got)
		}
	}
}

func TestReplaceEditContent_LFFileWithCRLFNeedle(t *testing.T) {
	content := []byte("one\ntwo\nthree\n")
	got, err := replaceEditContent(content, "one\r\ntwo", "A\r\nB")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// File is LF: the replacement is normalized to LF, no stray CRs land.
	if want := "A\nB\nthree\n"; string(got) != want {
		t.Fatalf("result = %q, want %q", got, want)
	}
}

func TestReplaceEditContent_LFFileKeepsByteExactNewText(t *testing.T) {
	// Legacy semantics preserved for LF files: exact match, new_text verbatim.
	content := []byte("a\nb\n")
	got, err := replaceEditContent(content, "a", "x\r\ny")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := "x\r\ny\nb\n"; string(got) != want {
		t.Fatalf("result = %q, want %q", got, want)
	}
}

func TestReplaceEditContent_CRLFFileSingleLineNeedleAdaptsNewText(t *testing.T) {
	content := []byte("head\r\nmiddle\r\ntail\r\n")
	got, err := replaceEditContent(content, "middle", "m1\nm2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := "head\r\nm1\r\nm2\r\ntail\r\n"; string(got) != want {
		t.Fatalf("result = %q, want %q", got, want)
	}
}

func TestReplaceEditContent_BOMTolerantAndPreserved(t *testing.T) {
	content := []byte(utf8BOM + "first line\r\nsecond\r\n")
	got, err := replaceEditContent(content, "first line", "changed")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := utf8BOM + "changed\r\nsecond\r\n"; string(got) != want {
		t.Fatalf("result = %q, want %q", got, want)
	}
}

func TestReplaceEditContent_BOMFirstLineMultiLine(t *testing.T) {
	content := []byte(utf8BOM + "line one\nline two\n")
	got, err := replaceEditContent(content, "line one\nline two", "merged")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := utf8BOM + "merged\n"; string(got) != want {
		t.Fatalf("result = %q, want %q", got, want)
	}
}

func TestReplaceEditContent_NormalizedAmbiguityRejected(t *testing.T) {
	// "x\n" matches twice once CRLF is normalized — must not silently pick one.
	content := []byte("x\r\nx\r\n")
	if _, err := replaceEditContent(content, "x\n", "y\n"); err == nil {
		t.Fatal("expected ambiguity error")
	} else if !strings.Contains(err.Error(), "2 times") {
		t.Fatalf("error = %v, want count 2", err)
	}
}

func TestReplaceEditContent_SingleLineNeedleNeverCrossesCR(t *testing.T) {
	// "abc" must not match "ab\rc" just because normalization strips a CR.
	content := []byte("ab\rc")
	if _, err := replaceEditContent(content, "abc", "z"); err == nil {
		t.Fatal("expected not-found error for cross-CR match")
	}
}

func TestReplaceEditContent_CRFileStyle(t *testing.T) {
	content := []byte("a\rb\rc\r")
	got, err := replaceEditContent(content, "a\nb", "x\ny")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := "x\ry\rc\r"; string(got) != want {
		t.Fatalf("result = %q, want %q", got, want)
	}
}

func TestReplaceEditContent_NotFoundMessage(t *testing.T) {
	if _, err := replaceEditContent([]byte("hello"), "nope\nlines", "x"); err == nil {
		t.Fatal("expected error")
	} else if !strings.Contains(err.Error(), "not found") {
		t.Fatalf("error = %v, want not-found message", err)
	}
}

func TestSplitUTF8BOM(t *testing.T) {
	if bom, rest := splitUTF8BOM(utf8BOM + "hi"); bom == "" || rest != "hi" {
		t.Fatalf("splitUTF8BOM with BOM = %q, %q", bom, rest)
	}
	if bom, rest := splitUTF8BOM("hi"); bom != "" || rest != "hi" {
		t.Fatalf("splitUTF8BOM without BOM = %q, %q", bom, rest)
	}
}
