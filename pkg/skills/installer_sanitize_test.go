package skills

import "testing"

// F3 regression: names returned by remote listing APIs (GitHub installer)
// must be plain single path components before being joined into local
// paths — a hostile repo listing "../escape" or "C:\x" must not traverse.
func TestSanitizeRemoteFileName(t *testing.T) {
	ok := []struct{ in, want string }{
		{"SKILL.md", "SKILL.md"},
		{"scripts", "scripts"},
		{"my tool notes.txt", "my tool notes.txt"}, // spaces are legal
		{"a..b.md", "a..b.md"},                    // not a dot-dot component
		{"..hidden", "..hidden"},
	}
	for _, c := range ok {
		got, err := sanitizeRemoteFileName(c.in)
		if err != nil {
			t.Errorf("sanitizeRemoteFileName(%q) unexpected error: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("sanitizeRemoteFileName(%q) = %q, want %q", c.in, got, c.want)
		}
	}

	bad := []string{
		"",
		".",
		"..",
		"../escape",
		"a/b",
		`a\b`,   // backslash is a separator on Windows even though git stores it
		`\root`, // rooted component
		`C:\x`,
		`C:x`,
		`\\srv\share`,
	}
	for _, in := range bad {
		if got, err := sanitizeRemoteFileName(in); err == nil {
			t.Errorf("sanitizeRemoteFileName(%q) = %q, want rejection", in, got)
		}
	}
}
