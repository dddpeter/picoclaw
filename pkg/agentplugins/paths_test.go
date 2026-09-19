package agentplugins

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func TestContains(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "sub", "file.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Run("inside file", func(t *testing.T) {
		if !Contains(root, filepath.Join(root, "sub", "file.txt")) {
			t.Fatal("file inside root must be contained")
		}
	})
	t.Run("parent escape", func(t *testing.T) {
		if Contains(root, filepath.Join(root, "..", "outside.txt")) {
			t.Fatal("../ must escape")
		}
	})
	t.Run("absolute outside", func(t *testing.T) {
		if Contains(root, `C:\Windows\notepad.exe`) {
			t.Fatal("absolute path outside root must not be contained")
		}
	})
	t.Run("root itself", func(t *testing.T) {
		if !Contains(root, root) {
			t.Fatal("root itself must be contained")
		}
	})
	t.Run("missing file within existing ancestor", func(t *testing.T) {
		if !Contains(root, filepath.Join(root, "sub", "missing.txt")) {
			t.Fatal("nonexistent file with nearest existing ancestor inside root must be contained")
		}
	})
	t.Run("missing file under escaping ancestor", func(t *testing.T) {
		outside := filepath.Join(root, "..", "elsewhere")
		if Contains(root, filepath.Join(outside, "missing.txt")) {
			t.Fatal("nearest existing ancestor outside root must not be contained")
		}
	})
	t.Run("symlink escape", func(t *testing.T) {
		outside := t.TempDir()
		if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(root, "leak")
		err := os.Symlink(outside, link)
		if err != nil {
			t.Skipf("symlink unavailable on this platform/user: %v", err)
		}
		if Contains(root, filepath.Join(link, "secret.txt")) {
			t.Fatal("symlink resolving outside root must not be contained")
		}
		// The link path itself also resolves outside the root, so per spec
		// §4.1 it must be rejected as well.
		if Contains(root, link) {
			t.Fatal("reparse point resolving outside root must not be contained")
		}
	})
	t.Run("symlink to inside is contained", func(t *testing.T) {
		link := filepath.Join(root, "inside-link")
		err := os.Symlink(filepath.Join(root, "sub"), link)
		if err != nil {
			t.Skipf("symlink unavailable on this platform/user: %v", err)
		}
		if !Contains(root, filepath.Join(link, "file.txt")) {
			t.Fatal("symlink resolving inside root must be contained")
		}
	})
	t.Run("junction escape", func(t *testing.T) {
		if runtime.GOOS != "windows" {
			t.Skip("junctions are a Windows reparse mechanism")
		}
		outside := t.TempDir()
		if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(root, "junction")
		// Junctions do not require elevated privileges; created via mklink.
		out, err := exec.Command("cmd", "/c", "mklink", "/J", link, outside).CombinedOutput()
		if err != nil {
			t.Skipf("junction creation unavailable: %v: %s", err, string(out))
		}
		defer os.Remove(link)
		if Contains(root, filepath.Join(link, "secret.txt")) {
			t.Fatal("junction resolving outside root must not be contained")
		}
	})
}

func TestIsPluginRelative(t *testing.T) {
	cases := []struct {
		p    string
		want bool
	}{
		{"./bin/server", true},
		{"./data", true},
		{"../bin", false},
		{"data", false},
		{"", false},
		{".", false},
		{"./a/../b", false},
		{"./a/..", false},
	}
	for _, c := range cases {
		if got := IsPluginRelative(c.p); got != c.want {
			t.Errorf("IsPluginRelative(%q) = %v, want %v", c.p, got, c.want)
		}
	}
}
