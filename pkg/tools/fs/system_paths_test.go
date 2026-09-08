package fstools

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The guard floor under the fork's open-by-default sandbox: file tools may
// roam the filesystem, but OS system directories stay off-limits for reads
// and writes unless tools.protect_system_paths explicitly disables it.
func TestSystemPathProtection(t *testing.T) {
	t.Cleanup(func() { SetSystemPathProtection(true) })

	var protected, allowed string
	if runtime.GOOS == "windows" {
		root := os.Getenv("SystemRoot")
		if root == "" {
			t.Skip("SystemRoot not set")
		}
		protected = filepath.Join(root, "System32", "drivers", "etc", "hosts")
		allowed = filepath.Join(os.TempDir(), "picoclaw-test", "file.txt")
	} else {
		protected = "/etc/passwd"
		allowed = "/tmp/picoclaw-test/file.txt"
	}

	if !IsProtectedSystemPath(protected) {
		t.Fatalf("%s should be protected", protected)
	}
	if IsProtectedSystemPath(allowed) {
		t.Fatalf("%s should not be protected", allowed)
	}

	// Prefix must match on a directory boundary: /etcz is NOT /etc (Unix).
	if runtime.GOOS != "windows" && IsProtectedSystemPath("/etcz/file") {
		t.Fatal("/etcz must not be treated as /etc")
	}

	ws := t.TempDir()
	_, err := ValidatePathWithAllowPaths(protected, ws, false, nil)
	if err == nil || !strings.Contains(err.Error(), "protected system directory") {
		t.Fatalf("read of %s under unrestricted mode should hit the system guard, got err=%v", protected, err)
	}

	// The guard applies in restrict mode too (it runs before the workspace
	// check, so allow-patterns cannot carve into system dirs either).
	_, err = ValidatePathWithAllowPaths(protected, ws, true, nil)
	if err == nil || !strings.Contains(err.Error(), "protected system directory") {
		t.Fatalf("restrict mode should also hit the system guard first, got err=%v", err)
	}

	// Explicit opt-out disables the guard.
	SetSystemPathProtection(false)
	if _, err := ValidatePathWithAllowPaths(protected, ws, false, nil); err != nil {
		t.Fatalf("guard disabled: expected pass-through, got err=%v", err)
	}
}

func TestSystemPathProtectionSymlinkEscape(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation on Windows needs privileges")
	}
	link := filepath.Join(t.TempDir(), "etc-link")
	if err := os.Symlink("/etc", link); err != nil {
		t.Skip("symlink unavailable:", err)
	}
	if !IsProtectedSystemPath(filepath.Join(link, "passwd")) {
		t.Fatal("path through a symlink into /etc must be protected")
	}
}
