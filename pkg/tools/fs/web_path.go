package fstools

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// ResolveWebPath validates a path for the web file endpoints (GET /pico/file,
// GET /pico/tree — design web-file-upload-preview §5.2). It reuses the file
// tools' restrict-mode semantics wholesale: workspace-relative resolution,
// system-directory veto, workspace boundary plus explicit extra roots, and
// symlink escape checks — so the web surface can never drift from the tool
// surface on the basics, while staying narrower by default (workspace-only
// unless extraRoots are configured).
//
// extraRoots entries may be absolute, ~-relative (home), or relative to the
// workspace root. A root grants access to itself and everything under it.
func ResolveWebPath(path, workspace string, extraRoots []string) (string, error) {
	// Windows: rooted-but-driveless paths ("/etc/passwd" → "\etc\passwd") are
	// absolute on the process drive, yet filepath.IsAbs reports false — left
	// alone they would silently re-anchor inside the workspace and answer 404
	// instead of the boundary denial Unix gets. Volume-ize before validation.
	if !filepath.IsAbs(path) && strings.HasPrefix(filepath.ToSlash(path), "/") {
		if abs, err := filepath.Abs(path); err == nil {
			path = abs
		}
	}
	var patterns []*regexp.Regexp
	for _, root := range extraRoots {
		trimmed := strings.TrimSpace(root)
		if trimmed == "" {
			continue
		}
		abs, err := resolveWebRoot(trimmed, workspace)
		if err != nil {
			continue
		}
		// Separator-agnostic: on Windows the remainder after the root is
		// "\child", so a "/"-only boundary would never match any child path.
		patterns = append(patterns, regexp.MustCompile("^"+regexp.QuoteMeta(abs)+`(?:[/\\]|$)`))
	}
	return validatePathWithAllowPaths(path, workspace, true, patterns)
}

func resolveWebRoot(root, workspace string) (string, error) {
	if strings.HasPrefix(root, "~") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("home dir unavailable: %w", err)
		}
		if root == "~" {
			return home, nil
		}
		if strings.HasPrefix(root[1:], "/") {
			return filepath.Clean(home + root[1:]), nil
		}
		return "", fmt.Errorf("invalid root %q", root)
	}
	if filepath.IsAbs(root) {
		return filepath.Clean(root), nil
	}
	if workspace == "" {
		return "", fmt.Errorf("relative root %q needs a workspace", root)
	}
	return filepath.Clean(filepath.Join(workspace, root)), nil
}
