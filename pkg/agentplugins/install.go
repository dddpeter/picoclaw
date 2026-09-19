package agentplugins

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// reservedInstallNames are spec-legal plugin names that would collide with
// install-root special paths: the data root (<root>/data) and the registry
// file (registry.json). Installing under these names is rejected.
var reservedInstallNames = map[string]bool{
	"data":           true,
	RegistryFileName: true,
}

// validateForInstall loads the manifest of the source tree, enforcing the
// reserved-name guard. It returns the manifest.
func validateForInstall(src string) (*Manifest, error) {
	var rep Report
	m, err := LoadManifest(src, &rep)
	if err != nil {
		return nil, fmt.Errorf("source is not a valid plugin: %w", err)
	}
	if reservedInstallNames[m.Name] {
		return nil, fmt.Errorf("plugin name %q collides with a reserved install-root path (data, %s)", m.Name, RegistryFileName)
	}
	return m, nil
}

// copyTree copies the whole source directory tree into dst (dst must not
// exist). os.CopyFS preserves regular files, directories and symlinks;
// symlink escapes are not an install-time concern — every package path is
// containment-checked again at load time (spec §4.1, design D6).
func copyTree(dst, src string) error {
	return os.CopyFS(dst, os.DirFS(src))
}

// installValidated copies a validated plugin source tree to its target dir.
func installValidated(src, installRoot string) (string, error) {
	m, err := validateForInstall(src)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(installRoot, 0o755); err != nil {
		return "", fmt.Errorf("create install root: %w", err)
	}
	target := filepath.Join(installRoot, m.Name)
	if _, err := os.Lstat(target); err == nil {
		return "", fmt.Errorf("plugin %q already installed at %s (run `picoclaw plugin remove %s` first)", m.Name, target, m.Name)
	} else if !os.IsNotExist(err) {
		return "", err
	}
	if err := copyTree(target, src); err != nil {
		// Best-effort cleanup of the partial copy.
		os.RemoveAll(target)
		return "", fmt.Errorf("copy plugin files: %w", err)
	}
	return target, nil
}

// InstallFromLocal copies a local plugin directory into the install root.
// The source is validated first (manifest + reserved-name guard); the target
// is never overwritten.
func InstallFromLocal(src string, installRoot string) (string, error) {
	st, err := os.Stat(src)
	if err != nil {
		return "", fmt.Errorf("source directory: %w", err)
	}
	if !st.IsDir() {
		return "", fmt.Errorf("source %s is not a directory", src)
	}
	return installValidated(src, installRoot)
}

// InstallFromGit clones gitURL (shallow, optionally pinned to a branch or tag
// via ref — commit SHAs are NOT supported) to a temp dir, validates the
// manifest and copies the plugin into the install root.
func InstallFromGit(gitURL string, ref string, installRoot string) (string, error) {
	if gitURL == "" {
		return "", fmt.Errorf("git URL is required")
	}
	tmp, err := os.MkdirTemp("", "picoclaw-plugin-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)

	args := []string{"clone", "--depth", "1"}
	if ref != "" {
		args = append(args, "--branch", ref)
	}
	args = append(args, gitURL, filepath.Join(tmp, "clone"))

	cmd := exec.Command("git", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git clone failed: %w: %s", err, strings.TrimSpace(string(out)))
	}
	target, err := installValidated(filepath.Join(tmp, "clone"), installRoot)
	if err != nil {
		return "", err
	}
	return target, nil
}

// Remove deletes the installed plugin directory and, when purgeData is set,
// the client-managed PLUGIN_DATA directory for it.
func Remove(name string, installRoot string, purgeData bool) error {
	if reservedInstallNames[name] {
		return fmt.Errorf("refusing to remove reserved install-root path %q", name)
	}
	dir := filepath.Join(installRoot, name)
	if _, err := os.Stat(dir); err != nil {
		return fmt.Errorf("plugin %q is not installed", name)
	}
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("remove plugin directory: %w", err)
	}
	if purgeData {
		dataDir := filepath.Join(installRoot, "data", name)
		if err := os.RemoveAll(dataDir); err != nil {
			return fmt.Errorf("remove plugin data directory: %w", err)
		}
	}
	return nil
}

// RegisterIn adds an entry for a freshly installed plugin. (Helper kept
// beside the installers so CLI code stays thin.)
func RegisterIn(r *Registry, name, version, source, ref string) {
	r.Entries[name] = RegistryEntry{
		Name:        name,
		Version:     version,
		Source:      source,
		Ref:         ref,
		InstalledAt: time.Now(),
		Enabled:     true,
	}
}

// IsReservedInstallName reports whether name collides with a reserved
// install-root path (the data root or the registry file). Such names are
// spec-legal plugin names but must never be install/remove targets.
func IsReservedInstallName(name string) bool {
	return reservedInstallNames[name]
}
