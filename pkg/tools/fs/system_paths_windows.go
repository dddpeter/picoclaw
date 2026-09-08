//go:build windows

package fstools

import (
	"golang.org/x/sys/windows"
)

// longPathForm expands 8.3 short-name path components (e.g. C:\PROGRA~1)
// to their long forms via GetLongPathName. Windows' EvalSymlinks does NOT
// expand short names, so the system-path guard needs this separate pass to
// keep C:\PROGRA~1\evil from sailing past the C:\Program Files prefix.
func longPathForm(p string) (string, bool) {
	if p == "" {
		return "", false
	}
	ptr, err := windows.UTF16PtrFromString(p)
	if err != nil {
		return "", false
	}
	n, err := windows.GetLongPathName(ptr, nil, 0)
	if err != nil || n == 0 {
		return "", false
	}
	buf := make([]uint16, n)
	if _, err := windows.GetLongPathName(ptr, &buf[0], uint32(len(buf))); err != nil {
		return "", false
	}
	return windows.UTF16ToString(buf), true
}
