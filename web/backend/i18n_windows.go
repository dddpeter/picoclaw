//go:build windows

package main

import "syscall"

// osUILanguage returns the Windows user UI language as a language code
// ("zh" for any Chinese locale), or "" when it cannot be determined. The
// tray menu is the launcher's own face on Windows, so it must follow the OS
// locale that the LANG environment variable never carries there.
func osUILanguage() string {
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	proc := kernel32.NewProc("GetUserDefaultUILanguage")
	if err := proc.Find(); err != nil {
		return ""
	}
	langID, _, _ := proc.Call()
	const primaryLangChinese = 0x04
	if langID != 0 && langID&0x3FF == primaryLangChinese {
		return "zh"
	}
	return ""
}
