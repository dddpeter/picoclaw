package fstools

import (
	"bytes"
	"testing"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"
)

func TestDecodeText_UTF8(t *testing.T) {
	text, enc := decodeText([]byte("hello 世界"))
	if enc != encUTF8 || text != "hello 世界" {
		t.Fatalf("decodeText = %q, %v", text, enc)
	}
}

func TestDecodeText_UTF8BOM(t *testing.T) {
	raw := []byte(utf8BOM + "config")
	text, enc := decodeText(raw)
	if enc != encUTF8BOM {
		t.Fatalf("enc = %v, want encUTF8BOM", enc)
	}
	if text != "config" {
		t.Fatalf("text = %q, want BOM stripped", text)
	}
	// Round trip restores the BOM byte-identically.
	back, err := encodeText(text, enc)
	if err != nil || !bytes.Equal(back, raw) {
		t.Fatalf("round trip = %q, %v; want %q", back, err, raw)
	}
}

func TestDecodeText_GB18030(t *testing.T) {
	want := "你好，世界 —— 中文内容"
	raw, err := simplifiedchinese.GB18030.NewEncoder().String(want)
	if err != nil {
		t.Fatalf("encode fixture: %v", err)
	}
	if utf8.ValidString(raw) {
		t.Fatalf("fixture unexpectedly valid UTF-8: %q", raw)
	}

	text, enc := decodeText([]byte(raw))
	if enc != encGB18030 {
		t.Fatalf("enc = %v, want encGB18030", enc)
	}
	if text != want {
		t.Fatalf("text = %q, want %q", text, want)
	}

	back, err := encodeText(text, enc)
	if err != nil || string(back) != raw {
		t.Fatalf("round trip = %q, %v; want %q", back, err, raw)
	}
}

func TestDecodeText_NULNeverTranscoded(t *testing.T) {
	raw := []byte{'a', 0x81, 0x00, 'b'} // invalid UTF-8 with a NUL byte
	text, enc := decodeText(raw)
	if enc != encRaw || text != string(raw) {
		t.Fatalf("decodeText = %q, %v; want raw passthrough", text, enc)
	}
}

func TestDecodeText_RandomBinaryStaysRaw(t *testing.T) {
	// Sequences that may or may not decode as GB18030 must never panic and
	// only ever report the two non-UTF-8 outcomes.
	raw := []byte{0x81, 0x40, 0xFE, 0xFE, 0x7F, 0x81, 0x39, 0xFE}
	_, enc := decodeText(raw)
	if enc != encGB18030 && enc != encRaw {
		t.Fatalf("enc = %v, want encGB18030 or encRaw", enc)
	}
}

func TestTrimTrailingPartialSequence(t *testing.T) {
	tests := []struct {
		name  string
		in    []byte
		outOf int // expected remaining length
	}{
		{"ascii untouched", []byte("abc"), 3},
		{"no high bytes", []byte{'a', 0x7f}, 2},
		{"one high byte trimmed", []byte{'a', 0xd6}, 1},
		{"two high bytes trimmed", []byte{'a', 0x81, 0x40, 0xFE, 0xFE}, 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := trimTrailingPartialSequence(tt.in); len(got) != tt.outOf {
				t.Fatalf("len = %d, want %d", len(got), tt.outOf)
			}
		})
	}
}
