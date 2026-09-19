//go:build windows

package fstools

import (
	"fmt"
	"path/filepath"
	"strings"
)

// windowsReservedNames are device names reserved in every path element on
// Windows, with or without an extension ("CON" and "CON.txt" are both
// reserved). Writing them hangs or creates an unaddressable device; reading
// them (notably CON) can block forever waiting for console input.
var windowsReservedNames = map[string]bool{
	"CON": true, "PRN": true, "AUX": true, "NUL": true,
	"COM1": true, "COM2": true, "COM3": true, "COM4": true,
	"COM5": true, "COM6": true, "COM7": true, "COM8": true, "COM9": true,
	"LPT1": true, "LPT2": true, "LPT3": true, "LPT4": true, "LPT5": true,
	"LPT6": true, "LPT7": true, "LPT8": true, "LPT9": true,
}

// validateWritePath rejects paths that Win32 would silently transform instead
// of failing: reserved device names, trailing dots/spaces (stripped by
// CreateFile, so the file lands under a different name than the one the model
// asked for), invalid characters, and NTFS alternate data streams
// ("file.txt:stream"). Every path element is checked — Win32 applies these
// rules at each component. A clear error lets the model pick a valid name
// immediately instead of spiraling in retries.
func validateWritePath(path string) error {
	for _, elem := range pathElements(path) {
		if err := checkWindowsElementName(elem, true); err != nil {
			return err
		}
	}
	return nil
}

// validateReadPath rejects opening paths containing reserved device names in
// any element (opening CON can hang the tool; Win32 cannot address such
// components without the \\?\ literal prefix anyway).
func validateReadPath(path string) error {
	for _, elem := range pathElements(path) {
		if err := checkWindowsElementName(elem, false); err != nil {
			return err
		}
	}
	return nil
}

// pathElements returns every non-empty element of path (volume and
// separators removed; "." and ".." pass through untouched —
// checkWindowsElementName ignores them).
func pathElements(path string) []string {
	cleaned := filepath.Clean(path)
	if cleaned == "." {
		return nil
	}
	vol := filepath.VolumeName(cleaned)
	return strings.FieldsFunc(cleaned[len(vol):], func(r rune) bool {
		return r == '/' || r == '\\'
	})
}

// checkWindowsElementName validates one path element. When fullCheck is set it
// also rejects invalid characters, trailing dots/spaces, and NTFS alternate
// data stream syntax.
func checkWindowsElementName(elem string, fullCheck bool) error {
	if elem == "" || elem == "." || elem == ".." {
		return nil
	}
	if fullCheck {
		if strings.ContainsAny(elem, `<>:"|?*`) {
			return fmt.Errorf(
				"invalid Windows file name %q: the characters <>:\"|?* are not allowed",
				elem,
			)
		}
		if strings.HasSuffix(elem, ".") || strings.HasSuffix(elem, " ") {
			return fmt.Errorf(
				"invalid Windows file name %q: Windows strips trailing dots and spaces, so the file would be created under a different name; drop the trailing character",
				elem,
			)
		}
	}
	base := elem
	// Classic Win32 device-name parsing (RtlIsDosDeviceName) strips at the
	// FIRST dot: "nul.tar.gz" still addresses the NUL device on older
	// Windows, even though recent builds allow creating such literal names.
	if i := strings.IndexByte(base, '.'); i > 0 {
		base = base[:i]
	}
	if windowsReservedNames[strings.ToUpper(base)] {
		return fmt.Errorf(
			"invalid Windows file name %q: %s is a reserved device name on Windows",
			elem, strings.ToUpper(base),
		)
	}
	return nil
}
