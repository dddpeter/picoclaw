package lsp

import (
	"strings"
	"unicode/utf8"
)

// PositionToOffset converts an LSP position (0-based line + UTF-16 code
// unit character offset) into a byte offset in the UTF-8 text. Out-of-range
// positions clamp to the document boundaries.
//
// LSP positions count UTF-16 code units, not runes or bytes: BMP characters
// (Chinese, most symbols) count 1 while astral characters (emoji, rare CJK
// ext) count 2 because they are surrogate pairs in UTF-16. Go strings are
// UTF-8, so this walk converts explicitly — the #1 correctness trap when
// porting LSP clients from JS (design §3.7).
func PositionToOffset(text string, line, character int) int {
	if line < 0 {
		return 0
	}
	// Advance to the start of the target line.
	rest := text
	for l := 0; l < line; l++ {
		i := strings.IndexByte(rest, '\n')
		if i < 0 {
			return len(text)
		}
		rest = rest[i+1:]
	}
	if character <= 0 {
		return len(text) - len(rest)
	}
	// Walk the line counting UTF-16 code units.
	units := 0
	for i, r := range rest {
		if r == '\n' {
			return len(text) - len(rest) + i
		}
		r16 := 1
		if r > 0xFFFF {
			r16 = 2
		}
		if units+r16 > character {
			return len(text) - len(rest) + i
		}
		units += r16
		if units == character {
			return len(text) - len(rest) + i + utf8.RuneLen(r)
		}
	}
	return len(text) - len(rest) + len(rest)
}

// OffsetToPosition converts a byte offset into an LSP position (line +
// UTF-16 character). Clamps to the text boundaries.
func OffsetToPosition(text string, offset int) (line, character int) {
	if offset < 0 {
		offset = 0
	}
	if offset > len(text) {
		offset = len(text)
	}
	for i := 0; i < offset; {
		r, size := utf8.DecodeRuneInString(text[i:])
		if r == '\n' {
			line++
			character = 0
		} else if r > 0xFFFF {
			character += 2
		} else {
			character++
		}
		i += size
	}
	return line, character
}
