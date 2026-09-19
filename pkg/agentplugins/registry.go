package agentplugins

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// RegistryEntry is one installed plugin's state in registry.json.
type RegistryEntry struct {
	Name        string    `json:"name"`
	Version     string    `json:"version,omitempty"`
	Source      string    `json:"source,omitempty"`
	Ref         string    `json:"ref,omitempty"`
	InstalledAt time.Time `json:"installedAt"`
	Enabled     bool      `json:"enabled"`
}

// Registry is the install-root state file. The on-disk shape is a plain
// object keyed by plugin name — the same shape readEnabledMap consumes at
// load time (missing entry ⇒ enabled).
type Registry struct {
	Path    string
	Entries map[string]RegistryEntry
}

// LoadRegistry reads registry.json at path. A missing file yields an empty
// registry with a nil error (nothing installed yet).
func LoadRegistry(path string) (*Registry, error) {
	r := &Registry{Path: path, Entries: map[string]RegistryEntry{}}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return r, nil
		}
		return nil, fmt.Errorf("read registry: %w", err)
	}
	if err := json.Unmarshal(data, &r.Entries); err != nil {
		return nil, fmt.Errorf("registry is not valid JSON: %w", err)
	}
	return r, nil
}

// Save writes the registry back to its path (atomic-ish: temp file + rename).
func (r *Registry) Save() error {
	data, err := json.MarshalIndent(r.Entries, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(r.Path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp := r.Path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, r.Path)
}

// SetEnabled toggles the enabled flag of an installed plugin.
func (r *Registry) SetEnabled(name string, enabled bool) error {
	entry, ok := r.Entries[name]
	if !ok {
		return fmt.Errorf("plugin %q is not registered", name)
	}
	entry.Enabled = enabled
	r.Entries[name] = entry
	return nil
}

// DefaultInstallRoot returns ~/.agents/plugins — the spec's example install
// root the user pinned for PicoClaw.
func DefaultInstallRoot() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}
	return filepath.Join(home, ".agents", "plugins"), nil
}

// DefaultDataRoot returns <install root>/data — the base of the
// client-managed PLUGIN_DATA directories.
func DefaultDataRoot() (string, error) {
	root, err := DefaultInstallRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "data"), nil
}
