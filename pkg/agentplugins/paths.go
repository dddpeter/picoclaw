package agentplugins

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Contains reports whether candidate stays within the filesystem-resolved
// plugin root (spec §4.1). The candidate is resolved by following symlinks of
// its nearest existing ancestor and re-joining the not-yet-existing suffix, so
// escapes via symlinks/junctions/reparse points are rejected.
func Contains(root, candidate string) bool {
	rootReal, err := resolveExisting(root)
	if err != nil {
		// Root does not exist yet (e.g. PLUGIN_DATA created just before
		// launch): fall back to lexical containment — no symlink can hide
		// inside a nonexistent root.
		rootAbs, aerr := filepath.Abs(root)
		if aerr != nil {
			return false
		}
		rootReal = rootAbs
	}
	rootNorm := normalizePath(rootReal)

	candAbs, err := filepath.Abs(candidate)
	if err != nil {
		return false
	}

	// Walk up to the deepest existing ancestor; remember the missing suffix.
	dir := candAbs
	var rest []string
	for {
		if _, err := os.Lstat(dir); err == nil {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return false
		}
		rest = append(rest, filepath.Base(dir))
		dir = parent
	}
	dirReal, err := resolveExisting(dir)
	if err != nil {
		return false
	}

	// Rebuild: reversed rest sits under the resolved ancestor.
	resolved := dirReal
	for i := len(rest) - 1; i >= 0; i-- {
		resolved = filepath.Join(resolved, rest[i])
	}
	resolved = normalizePath(resolved)

	if resolved == rootNorm {
		return true
	}
	sep := string(os.PathSeparator)
	if runtime.GOOS == "windows" {
		return hasPrefixFold(resolved, rootNorm+sep)
	}
	return strings.HasPrefix(resolved, rootNorm+sep)
}

// resolveExisting returns the absolute, symlink-resolved path of a path that
// must exist.
func resolveExisting(p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(abs)
}

// normalizePath makes paths comparable across separators.
func normalizePath(p string) string {
	return filepath.Clean(filepath.ToSlash(p))
}

// hasPrefixFold is a case-insensitive prefix check for Windows paths.
func hasPrefixFold(s, prefix string) bool {
	return len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix)
}

// IsPluginRelative reports whether p is a plugin-relative path per spec §4.1:
// it must begin with "./" and contain no ".." segments.
func IsPluginRelative(p string) bool {
	if !strings.HasPrefix(p, "./") {
		return false
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." {
			return false
		}
	}
	return true
}
