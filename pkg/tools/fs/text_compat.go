package fstools

import (
	"errors"
	"fmt"
	"strings"
)

// utf8BOM is the UTF-8 byte order mark. Windows editors (Notepad, PowerShell
// 5.x, some legacy tooling) still prepend it to text files, and an invisible
// BOM glued to the first character breaks exact old_text matching.
const utf8BOM = "\xEF\xBB\xBF"

// errOldTextNotFound is returned when neither an exact nor a
// line-ending-insensitive match succeeds.
var errOldTextNotFound = errors.New(
	"old_text not found in file. Make sure it matches exactly (line-ending differences are tolerated automatically)",
)

// splitUTF8BOM splits a leading UTF-8 BOM off s, returning ("", s) when absent.
func splitUTF8BOM(s string) (bom, rest string) {
	if strings.HasPrefix(s, utf8BOM) {
		return utf8BOM, s[len(utf8BOM):]
	}
	return "", s
}

// normalizeEOLToLF converts every line terminator (CRLF, lone CR, LF) to LF.
func normalizeEOLToLF(s string) string {
	if !strings.Contains(s, "\r") {
		return s
	}
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\r", "\n")
}

// normalizeEOLWithMapping returns s with every line terminator normalized to
// LF, plus a mapping from normalized byte offsets back to offsets in s:
// mapping[i] is the offset in s of normalized byte i, and mapping[len(normalized)]
// is len(s) so that end-of-match offsets resolve as well.
func normalizeEOLWithMapping(s string) (string, []int) {
	if !strings.Contains(s, "\r") {
		mapping := make([]int, len(s)+1)
		for i := range mapping {
			mapping[i] = i
		}
		return s, mapping
	}

	var b strings.Builder
	b.Grow(len(s))
	mapping := make([]int, 0, len(s)+1)
	for i := 0; i < len(s); i++ {
		if s[i] == '\r' {
			b.WriteByte('\n')
			mapping = append(mapping, i)
			if i+1 < len(s) && s[i+1] == '\n' {
				i++ // consume the LF of a CRLF pair
			}
			continue
		}
		b.WriteByte(s[i])
		mapping = append(mapping, i)
	}
	mapping = append(mapping, len(s))
	return b.String(), mapping
}

// detectEOLStyle returns the dominant line terminator used by s: "\r\n", "\r",
// or "\n". Content without any terminator reports "\n".
func detectEOLStyle(s string) string {
	crlf := strings.Count(s, "\r\n")
	lf := strings.Count(s, "\n") - crlf
	cr := strings.Count(s, "\r") - crlf
	switch {
	case crlf > 0 && crlf >= lf && crlf >= cr:
		return "\r\n"
	case cr > lf && cr > 0:
		return "\r"
	default:
		return "\n"
	}
}

// adaptEOLToStyle normalizes s to LF and re-terminates every line with style.
func adaptEOLToStyle(s, style string) string {
	return strings.ReplaceAll(normalizeEOLToLF(s), "\n", style)
}

// replaceEditContent finds and replaces a single occurrence of oldText.
//
// Matching is tiered:
//  1. exact byte match (legacy semantics, BOM included);
//  2. exact match after stripping a UTF-8 BOM (the BOM is preserved in the
//     result);
//  3. line-ending-insensitive match: both sides are normalized to LF before
//     comparison, so an old_text written with "\n" matches a file whose disk
//     form uses "\r\n" (typical for Windows files).
//
// The replacement text is re-terminated with the file's dominant line-ending
// style so an edit never introduces mixed line endings. Files detected as LF
// keep legacy byte-for-byte replacement semantics.
func replaceEditContent(content []byte, oldText, newText string) ([]byte, error) {
	raw := string(content)

	// 1) Exact byte match.
	if strings.Contains(raw, oldText) {
		return replaceExact(raw, oldText, newText)
	}

	// 2) Exact match ignoring a UTF-8 BOM.
	text := raw
	bom := ""
	if b, rest := splitUTF8BOM(raw); b != "" {
		if strings.Contains(rest, oldText) {
			replaced, err := replaceExact(rest, oldText, newText)
			if err != nil {
				return nil, err
			}
			return []byte(b + string(replaced)), nil
		}
		bom, text = b, rest
	}

	// 3) Line-ending-insensitive match. Only multi-line needles (or needles
	// containing an explicit CR) can benefit from normalization; a single-line
	// needle must never match across a stripped CR (e.g. "abc" matching
	// "ab\rc").
	if !strings.Contains(oldText, "\n") && !strings.Contains(oldText, "\r") {
		return nil, errOldTextNotFound
	}

	normalized, mapping := normalizeEOLWithMapping(text)
	normOld := normalizeEOLToLF(oldText)
	switch count := strings.Count(normalized, normOld); {
	case count == 0:
		return nil, errOldTextNotFound
	case count > 1:
		return nil, fmt.Errorf("old_text appears %d times. Please provide more context to make it unique", count)
	}

	start := strings.Index(normalized, normOld)
	origStart, origEnd := mapping[start], mapping[start+len(normOld)]

	inserted := normalizeEOLToLF(newText)
	if style := detectEOLStyle(text); style != "\n" {
		inserted = adaptEOLToStyle(inserted, style)
	}

	return []byte(bom + text[:origStart] + inserted + text[origEnd:]), nil
}

// replaceExact performs the legacy exact-match replacement. When the file's
// dominant line-ending style is not LF and newText contains newlines, the
// replacement is re-terminated in the file's style to avoid mixed endings.
// LF files keep byte-for-byte semantics.
func replaceExact(content, oldText, newText string) ([]byte, error) {
	if count := strings.Count(content, oldText); count > 1 {
		return nil, fmt.Errorf("old_text appears %d times. Please provide more context to make it unique", count)
	}

	inserted := newText
	if style := detectEOLStyle(content); style != "\n" && strings.Contains(newText, "\n") {
		inserted = adaptEOLToStyle(newText, style)
	}

	return []byte(strings.Replace(content, oldText, inserted, 1)), nil
}
