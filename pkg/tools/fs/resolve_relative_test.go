package fstools

import (
	"path/filepath"
	"runtime"
	"testing"
)

// T5 regression: open-mode file tools anchor relative paths at the
// workspace root. Before the fix an unresolved relative path silently hit
// the gateway process CWD (HOME under systemd), so write_file("config.json")
// could clobber an unrelated file.
func TestResolveRelativeAnchorsAtWorkspace(t *testing.T) {
	ws := t.TempDir()
	h := &hostFs{workspace: ws}

	cases := []struct {
		in   string
		want string
	}{
		{"config.json", filepath.Join(ws, "config.json")},
		{filepath.Join("sub", "a.txt"), filepath.Join(ws, "sub", "a.txt")},
	}
	for _, c := range cases {
		if got := h.resolveRelative(c.in); got != c.want {
			t.Errorf("resolveRelative(%q) = %q, want %q", c.in, got, c.want)
		}
	}

	// Absolute paths pass through untouched.
	abs := filepath.Join(ws, "x.txt")
	if got := h.resolveRelative(abs); got != abs {
		t.Errorf("absolute path rewritten: %q -> %q", abs, got)
	}
	if runtime.GOOS == "windows" {
		// Drive-prefixed and rooted-relative forms must not be joined onto
		// the workspace (a leading backslash is volume-relative, not
		// workspace-relative).
		for _, p := range []string{`C:\temp\f.txt`, `C:rel.txt`, `\rooted.txt`} {
			if got := h.resolveRelative(p); got != p {
				t.Errorf("Windows path %q rewritten to %q", p, got)
			}
		}
	}

	// Empty workspace keeps the legacy passthrough behavior.
	legacy := &hostFs{}
	if got := legacy.resolveRelative("config.json"); got != "config.json" {
		t.Errorf("empty workspace must pass through, got %q", got)
	}
}
