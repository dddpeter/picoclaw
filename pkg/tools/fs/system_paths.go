package fstools

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
)

// System-path protection (fork default-on): file tools may roam the whole
// filesystem now that restrict_to_workspace defaults to false, but OS system
// directories stay off-limits for reads and writes alike. Exec keeps running
// general commands against system paths; only destructive denies apply there
// (see pkg/tools/shell.go defaultDenyPatterns).
//
// protectSystemPaths is tri-state via config (tools.protect_system_paths,
// nil = enabled, same nil=on convention as loop_detection). It is set once per
// agent construction from the effective config.

var protectSystemPaths atomic.Bool

func init() {
	protectSystemPaths.Store(true)
}

// SetSystemPathProtection enables/disables the guard process-wide.
func SetSystemPathProtection(enabled bool) {
	protectSystemPaths.Store(enabled)
}

// SystemPathProtectionEnabled reports the current guard state.
func SystemPathProtectionEnabled() bool {
	return protectSystemPaths.Load()
}

// systemPathPrefixes returns the protected OS system directory prefixes on
// the current platform. /opt, /var, /tmp and user homes are deliberately NOT
// protected: they hold user-managed software and data.
func systemPathPrefixes() []string {
	if isWindowsPlatform() {
		var prefixes []string
		add := func(dir string) {
			if dir != "" {
				prefixes = append(prefixes, filepath.Clean(dir))
			}
		}
		// Resolve real locations via env; fall back to the conventional root.
		if sr := os.Getenv("SystemRoot"); sr != "" {
			add(sr)
		} else {
			add(`C:\Windows`)
		}
		add(os.Getenv("ProgramFiles"))
		add(os.Getenv("ProgramFiles(x86)"))
		add(os.Getenv("ProgramW6432"))
		if pd := os.Getenv("ProgramData"); pd != "" {
			add(pd)
		} else {
			add(`C:\ProgramData`)
		}
		return prefixes
	}
	// Shared Unix bases; macOS additionally gets its sealed system volumes.
	prefixes := []string{
		"/bin", "/sbin", "/usr", "/etc", "/boot", "/dev", "/proc", "/sys", "/run",
		"/lib", "/lib64", "/lib32", "/libx32",
	}
	if isDarwinPlatform() {
		prefixes = append(prefixes, "/System", "/Library", "/private/etc", "/private/var/db")
	}
	return prefixes
}

func isWindowsPlatform() bool {
	return runtime.GOOS == "windows"
}

func isDarwinPlatform() bool {
	return runtime.GOOS == "darwin"
}

// IsProtectedSystemPath reports whether p is at or inside a protected OS
// system directory. Both the literal path and its symlink resolution are
// checked, so links pointing into e.g. C:\Windows are refused too; for
// not-yet-existing targets the deepest existing ancestor is resolved so
// symlinked parents still get caught.
func IsProtectedSystemPath(p string) bool {
	if p == "" {
		return false
	}
	cleaned := filepath.Clean(p)
	candidates := []string{cleaned}
	// 8.3 short names (C:\PROGRA~1) are a Windows path alias EvalSymlinks
	// does not expand; resolve them before matching prefixes.
	if long, ok := longPathForm(cleaned); ok {
		candidates = append(candidates, filepath.Clean(long))
	}
	if resolved, err := filepath.EvalSymlinks(cleaned); err == nil {
		candidates = append(candidates, filepath.Clean(resolved))
	} else if existing, err := resolveExistingAncestor(filepath.Dir(cleaned)); err == nil {
		// Protect everything beneath the deepest existing ancestor: a
		// symlinked parent is caught, while an unrelated new file under a
		// non-protected ancestor stays allowed.
		candidates = append(candidates, filepath.Join(filepath.Clean(existing), "x"))
	}
	prefixes := systemPathPrefixes()
	for _, candidate := range candidates {
		for _, prefix := range prefixes {
			if hasPathPrefix(candidate, prefix) {
				return true
			}
		}
	}
	return false
}

func hasPathPrefix(p, prefix string) bool {
	// Windows and macOS filesystems are case-insensitive: a model writing
	// C:\WindOWS would sail past a case-sensitive prefix match.
	if isWindowsPlatform() || isDarwinPlatform() {
		if len(p) < len(prefix) || !strings.EqualFold(p[:len(prefix)], prefix) {
			return false
		}
		rest := p[len(prefix):]
		return rest == "" || strings.EqualFold(rest[:1], string(os.PathSeparator))
	}
	if !strings.HasPrefix(p, prefix) {
		return false
	}
	rest := p[len(prefix):]
	return rest == "" || strings.HasPrefix(rest, string(os.PathSeparator))
}
