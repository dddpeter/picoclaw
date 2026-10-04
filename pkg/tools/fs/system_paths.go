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
		add(systemRootDir())
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

func systemRootDir() string {
	if sr := os.Getenv("SystemRoot"); sr != "" {
		return sr
	}
	return `C:\Windows`
}

// stripExtendedPathPrefix removes the Windows extended-path prefixes
// (`\\?\`, `\\?\UNC\`, `\\.\`) so prefix matching sees the plain Win32 path.
// filepath.Clean, GetLongPathName and EvalSymlinks all preserve the prefix,
// which makes every hasPathPrefix check miss — the `\\?\C:\Windows\...`
// alias would sail straight past the guard (exec's guardCommand strips the
// same alias, so the two layers must agree). The prefixes are matched
// case-insensitively: the filesystem tolerates `\\?\unc\...` too.
func stripExtendedPathPrefix(p string) string {
	if !isWindowsPlatform() {
		return p
	}
	if hasFoldPrefix(p, `\\?\UNC\`) {
		return `\\` + p[len(`\\?\UNC\`):]
	}
	if hasFoldPrefix(p, `\\.\UNC\`) {
		return `\\` + p[len(`\\.\UNC\`):]
	}
	if hasFoldPrefix(p, `\\?\`) {
		return p[len(`\\?\`):]
	}
	if hasFoldPrefix(p, `\\.\`) {
		return p[len(`\\.\`):]
	}
	return p
}

func hasFoldPrefix(p, prefix string) bool {
	return len(p) >= len(prefix) && strings.EqualFold(p[:len(prefix)], prefix)
}

func isDriveLetter(b byte) bool {
	return (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z')
}

// isLocalServerName reports whether the UNC server component targets this
// machine rather than a remote host (case-insensitive on every form).
func isLocalServerName(server string) bool {
	switch {
	case server == "", server == ".":
		return false
	case strings.EqualFold(server, "localhost"),
		strings.EqualFold(server, "127.0.0.1"),
		strings.EqualFold(server, "::1"),
		strings.EqualFold(server, "[::1]"):
		return true
	}
	if host, err := os.Hostname(); err == nil && host != "" {
		// Accept the machine's short name and fully-qualified form in
		// either direction (host "pc" vs server "pc.example.com" etc.).
		lh, ls := strings.ToLower(host), strings.ToLower(server)
		if lh == ls || strings.HasPrefix(lh, ls+".") || strings.HasPrefix(ls, lh+".") {
			return true
		}
	}
	return false
}

// normalizeLocalUNC maps a UNC path that reaches this machine through an
// administrative share back to its local drive form so prefix matching sees
// the real location: `\\localhost\C$\Windows\win.ini` is C:\Windows\win.ini
// by another name, and the bare UNC form would otherwise miss every drive-
// letter prefix (the T2 strip alone does not help — a UNC path never starts
// with a drive letter). ADMIN$ is the SystemRoot share. Remote hosts are
// left untouched: they are not this machine's system directories.
func normalizeLocalUNC(p string) string {
	if !isWindowsPlatform() || !strings.HasPrefix(p, `\\`) {
		return p
	}
	rest := p[2:] // server\share[\path...]
	serverEnd := strings.IndexByte(rest, '\\')
	if serverEnd <= 0 {
		return p
	}
	server := rest[:serverEnd]
	shareRest := rest[serverEnd+1:]
	shareEnd := strings.IndexByte(shareRest, '\\')
	share, tail := shareRest, ""
	if shareEnd >= 0 {
		share, tail = shareRest[:shareEnd], shareRest[shareEnd+1:]
	}
	if !isLocalServerName(server) {
		return p
	}
	if strings.EqualFold(share, "ADMIN$") {
		local := systemRootDir()
		if tail != "" {
			local += `\` + tail
		}
		return local
	}
	if len(share) == 2 && share[1] == '$' && isDriveLetter(share[0]) {
		local := string(share[0]) + `:\`
		if tail != "" {
			local += tail
		}
		return local
	}
	return p
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
	// Extended-path aliases are normalized first: every candidate below
	// derives from this cleaned form, so `\\?\`-prefixed inputs are checked
	// against the same prefixes as their plain equivalents. Local-machine
	// UNC admin shares (\\localhost\C$\...) are folded back to their drive
	// form for the same reason — the alias would otherwise match no prefix.
	cleaned := filepath.Clean(normalizeLocalUNC(stripExtendedPathPrefix(p)))
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
