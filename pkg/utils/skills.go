package utils

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ValidateSkillIdentifier validates that the given skill identifier (slug or registry name) is non-empty
// and does not contain path separators ("/", "\\") or ".." for security.
func ValidateSkillIdentifier(identifier string) error {
	trimmed := strings.TrimSpace(identifier)
	if trimmed == "" {
		return fmt.Errorf("identifier is required and must be a non-empty string")
	}
	if strings.ContainsAny(trimmed, "/\\") || strings.Contains(trimmed, "..") {
		return fmt.Errorf("identifier must not contain path separators or '..' to prevent directory traversal")
	}
	return nil
}

// NormalizeSkillName converts an arbitrary skill title into a valid skill name:
// it trims space, replaces whitespace/underscore runs with a single hyphen,
// drops control characters and path-unsafe punctuation, collapses consecutive
// hyphens, and trims leading/trailing hyphens. Unicode letters and digits
// (CJK, accents...) are preserved. Returns "" when nothing usable remains.
func NormalizeSkillName(raw string) string {
	cleaned := strings.TrimSpace(raw)
	if cleaned == "" {
		return ""
	}

	var b strings.Builder
	lastHyphen := true // suppress leading hyphens
	for _, r := range cleaned {
		switch {
		case r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == '_':
			if !lastHyphen {
				b.WriteByte('-')
				lastHyphen = true
			}
		case r == '-':
			if !lastHyphen {
				b.WriteByte('-')
				lastHyphen = true
			}
		case r < 0x20 || r == 0x7f:
			// control characters: drop
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
			lastHyphen = false
		default:
			// other punctuation/symbols: drop (keeps names path-safe)
		}
	}
	out := strings.Trim(b.String(), "-")
	if !utf8.ValidString(out) {
		return ""
	}
	return out
}
