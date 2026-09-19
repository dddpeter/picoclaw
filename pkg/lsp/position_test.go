package lsp

import (
	"testing"
)

func TestPositionToOffsetAscii(t *testing.T) {
	text := "ab\ncd\n"
	if got := PositionToOffset(text, 0, 0); got != 0 {
		t.Fatalf("0,0 = %d", got)
	}
	if got := PositionToOffset(text, 1, 1); got != 4 { // "ab\nc" -> offset 4 is 'd'
		t.Fatalf("1,1 = %d, want 4", got)
	}
}

func TestPositionToOffsetChineseBMP(t *testing.T) {
	// 你好 are BMP: 1 UTF-16 unit each, 3 UTF-8 bytes each.
	text := "你好世界"
	if got := PositionToOffset(text, 0, 2); got != 6 { // after 你好
		t.Fatalf("0,2 = %d, want 6", got)
	}
	if got := PositionToOffset(text, 0, 4); got != 12 {
		t.Fatalf("0,4 = %d, want 12", got)
	}
}

func TestPositionToOffsetAstralSurrogatePair(t *testing.T) {
	// 🎉 (U+1F389) is astral: 2 UTF-16 units, 4 UTF-8 bytes. Preceded by a
	// BMP char (3 bytes). Position 3 (after a + surrogate pair) = byte 7.
	text := "a🎉b"
	if got := PositionToOffset(text, 0, 1); got != 1 {
		t.Fatalf("0,1 = %d, want 1", got)
	}
	if got := PositionToOffset(text, 0, 3); got != 5 { // after a🎉
		t.Fatalf("0,3 = %d, want 5", got)
	}
	// Character 2 lands inside the surrogate pair — clamp to rune start.
	if got := PositionToOffset(text, 0, 2); got != 1 {
		t.Fatalf("0,2 = %d, want 1 (mid-surrogate clamps to rune start)", got)
	}
}

func TestPositionToOffsetClamps(t *testing.T) {
	text := "ab\ncd"
	if got := PositionToOffset(text, 99, 0); got != len(text) {
		t.Fatalf("over line = %d, want len", got)
	}
	if got := PositionToOffset(text, 1, 99); got != len(text) {
		t.Fatalf("over char = %d, want len", got)
	}
	if got := PositionToOffset(text, -1, 0); got != 0 {
		t.Fatalf("negative = %d, want 0", got)
	}
}

func TestOffsetToPositionRoundTrip(t *testing.T) {
	text := "你好\n🎉x\n"
	cases := []struct{ offset, line, char int }{
		{0, 0, 0}, {3, 0, 1}, {6, 0, 2}, {7, 1, 0}, {11, 1, 2}, {12, 1, 3}, {13, 2, 0},
	}
	for _, c := range cases {
		line, char := OffsetToPosition(text, c.offset)
		if line != c.line || char != c.char {
			t.Fatalf("offset %d = (%d,%d), want (%d,%d)", c.offset, line, char, c.line, c.char)
		}
	}
}

func TestApplyEditsBasic(t *testing.T) {
	text := "hello world"
	edits := []TextEdit{
		{Range: r(0, 0, 0, 5), NewText: "goodbye"},
		{Range: r(0, 6, 0, 11), NewText: "there"},
	}
	got, err := ApplyEdits(text, edits)
	if err != nil {
		t.Fatal(err)
	}
	if got != "goodbye there" {
		t.Fatalf("got %q", got)
	}
}

func TestApplyEditsOverlapRejected(t *testing.T) {
	text := "abcdef"
	edits := []TextEdit{
		{Range: r(0, 1, 0, 3), NewText: "X"}, // replaces bc
		{Range: r(0, 2, 0, 4), NewText: "Y"}, // replaces cd — overlaps
	}
	if _, err := ApplyEdits(text, edits); err == nil {
		t.Fatal("overlapping replacements must be rejected")
	}
}

func TestApplyEditsAdjacentInsertsDoNotConflict(t *testing.T) {
	text := "ab"
	edits := []TextEdit{
		{Range: r(0, 1, 0, 1), NewText: "X"},
		{Range: r(0, 1, 0, 1), NewText: "Y"},
	}
	got, err := ApplyEdits(text, edits)
	if err != nil {
		t.Fatal(err)
	}
	if got != "aXYb" && got != "aYXb" {
		t.Fatalf("got %q", got)
	}
}

func TestApplyEditsInsertInsideReplacementConflicts(t *testing.T) {
	text := "abcd"
	edits := []TextEdit{
		{Range: r(0, 1, 0, 3), NewText: "X"}, // replaces bc
		{Range: r(0, 2, 0, 2), NewText: "I"}, // inserts inside bc
	}
	if _, err := ApplyEdits(text, edits); err == nil {
		t.Fatal("insert strictly inside a replacement must conflict")
	}
}

func r(l1, c1, l2, c2 int) Range {
	return Range{Start: Position{Line: l1, Character: c1}, End: Position{Line: l2, Character: c2}}
}
