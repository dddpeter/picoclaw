//go:build windows

package fstools

import (
	"strings"
	"testing"
)

func TestValidateWritePath_RejectsReservedNames(t *testing.T) {
	for _, path := range []string{
		`C:\tmp\NUL`,
		`C:\tmp\con`,
		`C:\tmp\CON.txt`,
		`C:\tmp\nul.tar.gz`, // device parsing strips at the FIRST dot
		`C:\tmp\sub\COM1.log`,
		`C:\tmp\LPT9.prn`,
		`nul`,
	} {
		if err := validateWritePath(path); err == nil {
			t.Errorf("validateWritePath(%q) = nil, want reserved-name error", path)
		} else if !strings.Contains(err.Error(), "reserved device name") {
			t.Errorf("validateWritePath(%q) error = %v, want reserved-name message", path, err)
		}
	}
}

func TestValidateWritePath_RejectsTrailingDotsAndSpaces(t *testing.T) {
	for _, path := range []string{
		`C:\tmp\notes.txt.`,
		`C:\tmp\notes.txt `,
		`C:\tmp\folder.\file.txt`,
	} {
		if err := validateWritePath(path); err == nil {
			t.Errorf("validateWritePath(%q) = nil, want trailing dot/space error", path)
		}
	}
}

func TestValidateWritePath_RejectsInvalidCharsAndADS(t *testing.T) {
	for _, path := range []string{
		`C:\tmp\a|b.txt`,
		`C:\tmp\a<b.txt`,
		`C:\tmp\q?.txt`,
		`C:\tmp\file.txt:hidden`, // NTFS alternate data stream
	} {
		if err := validateWritePath(path); err == nil {
			t.Errorf("validateWritePath(%q) = nil, want invalid-char error", path)
		}
	}
}

func TestValidateWritePath_AcceptsLegitNames(t *testing.T) {
	for _, path := range []string{
		`C:\tmp\notes.txt`,
		`C:\tmp\.gitignore`,
		`C:\tmp\aux-files\data.txt`, // intermediate element may resemble a reserved name
		`C:\tmp\com10\log.txt`,      // COM10+ are not reserved
		`C:\tmp\file.name.txt`,      // first-dot base "file" is not reserved
		`C:\tmp\null\x.txt`,         // "null" ≠ "nul"
		`D:\code\con as prefix.txt`,
		`relative\path\file.md`,
	} {
		if err := validateWritePath(path); err != nil {
			t.Errorf("validateWritePath(%q) = %v, want nil", path, err)
		}
	}
}

func TestValidateReadPath_RejectsReservedElements(t *testing.T) {
	if err := validateReadPath(`C:\dev\CON`); err == nil {
		t.Error("validateReadPath on reserved final element = nil, want error")
	}
	// Win32 applies reserved-name parsing at every component, so intermediate
	// elements are checked too (they are unreachable without the \\?\ prefix).
	if err := validateReadPath(`C:\code\aux\file.txt`); err == nil {
		t.Error("validateReadPath(intermediate aux) = nil, want error")
	}
	if err := validateReadPath(`C:\tmp\file.txt:notes`); err != nil {
		t.Errorf("validateReadPath(ADS) = %v, want nil (reads are not name-checked)", err)
	}
}
