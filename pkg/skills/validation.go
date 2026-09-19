package skills

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/sipeed/picoclaw/pkg/utils"
)

// maxSkillNameRunes caps skill names; they double as directory names and
// prompt-catalog identifiers.
const maxSkillNameRunes = 64

func ValidateSkillName(name string) error {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return fmt.Errorf("skill name is required")
	}
	if filepath.IsAbs(trimmed) {
		return fmt.Errorf("skill name must not be an absolute path")
	}
	if err := utils.ValidateSkillIdentifier(trimmed); err != nil {
		return fmt.Errorf("skill name is invalid: %w", err)
	}
	if len(trimmed) > MaxNameLength {
		return fmt.Errorf("skill name exceeds %d characters", MaxNameLength)
	}
	if runeLen(trimmed) > maxSkillNameRunes {
		return fmt.Errorf("skill name exceeds %d characters", maxSkillNameRunes)
	}
	// Path separators and traversal are rejected above. Names double as
	// directory names, so Windows-reserved filename runes are denied
	// explicitly: < > : " / \ | ? * plus control characters. Everything
	// printable goes: Unicode letters/digits (CJK, accents), spaces, dots,
	// parens, etc. — titles like "SVG绘图工作台" or "Proactivity (Proactive
	// Agent)" are accepted as-is.
	if err := validateNameRunes(trimmed); err != nil {
		return err
	}
	if isWindowsReservedName(trimmed) {
		return fmt.Errorf("skill name is a reserved device name: %s", trimmed)
	}
	return nil
}

// validateNameRunes rejects control characters and Windows-forbidden filename
// runes. Everything printable goes.
func validateNameRunes(name string) error {
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("skill name must not contain control characters")
		}
		if strings.ContainsRune(`<>:"/\|?*`, r) {
			return fmt.Errorf("skill name must not contain %q", string(r))
		}
	}
	if strings.HasPrefix(name, ".") {
		return fmt.Errorf("skill name must not start with a dot")
	}
	if strings.HasSuffix(name, ".") || strings.HasSuffix(name, " ") {
		return fmt.Errorf("skill name must not end with a dot or space")
	}
	return nil
}

// isWindowsReservedName reports whether name collides with legacy Windows
// device names (CON, PRN, AUX, NUL, COM1..COM9, LPT1..LPT9).
func isWindowsReservedName(name string) bool {
	base := strings.SplitN(name, ".", 2)[0]
	base = strings.ToUpper(strings.TrimSpace(base))
	switch base {
	case "CON", "PRN", "AUX", "NUL":
		return true
	}
	if len(base) == 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) &&
		base[3] >= '1' && base[3] <= '9' {
		return true
	}
	return false
}

func runeLen(s string) int {
	n := 0
	for range s {
		n++
	}
	return n
}
