package agentplugins

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRegistryRoundtrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "registry.json")

	// Missing file → empty registry, nil error.
	r, err := LoadRegistry(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Entries) != 0 {
		t.Fatalf("empty registry expected, got %v", r.Entries)
	}

	r.Entries["golden"] = RegistryEntry{
		Name: "golden", Version: "1.0.0", Source: "local",
		InstalledAt: time.Now().Truncate(time.Second), Enabled: true,
	}
	if err := r.Save(); err != nil {
		t.Fatal(err)
	}

	r2, err := LoadRegistry(path)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := r2.Entries["golden"]
	if !ok || got.Version != "1.0.0" || got.Source != "local" || !got.Enabled {
		t.Fatalf("roundtrip mismatch: %+v", got)
	}
}

func TestInstallFromLocal(t *testing.T) {
	installRoot := t.TempDir()
	src := t.TempDir()
	makeGoldenPlugin(t, src)

	target, err := InstallFromLocal(src, installRoot)
	if err != nil {
		t.Fatal(err)
	}
	if target != filepath.Join(installRoot, "golden") {
		t.Fatalf("target = %q", target)
	}
	if _, err := os.Stat(filepath.Join(target, "plugin.json")); err != nil {
		t.Fatalf("plugin.json not copied: %v", err)
	}
	p, err := LoadPlugin(target, filepath.Join(installRoot, "data", "golden"), true)
	if err != nil {
		t.Fatalf("installed plugin must load: %v", err)
	}
	if p.Name != "golden" || len(p.Skills) != 1 || len(p.MCPServers) != 1 {
		t.Fatalf("loaded plugin = %+v", p)
	}
}

func TestInstallFromLocal_TargetExists(t *testing.T) {
	installRoot := t.TempDir()
	src := t.TempDir()
	makeGoldenPlugin(t, src)

	if _, err := InstallFromLocal(src, installRoot); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallFromLocal(src, installRoot); err == nil {
		t.Fatal("second install must fail (no auto-overwrite)")
	}
}

func TestInstallFromLocal_NoManifest(t *testing.T) {
	installRoot := t.TempDir()
	src := t.TempDir()
	if _, err := InstallFromLocal(src, installRoot); err == nil {
		t.Fatal("source without plugin.json must fail")
	}
}

func TestInstallReservedNames(t *testing.T) {
	installRoot := t.TempDir()
	for _, name := range []string{"data", "registry.json"} {
		src := t.TempDir()
		if err := os.WriteFile(filepath.Join(src, "plugin.json"),
			[]byte(`{"$schema":"`+ManifestSchemaURL+`","name":"`+name+`"}`), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := InstallFromLocal(src, installRoot); err == nil {
			t.Errorf("reserved plugin name %q must be rejected", name)
		}
	}
}

func TestRemove(t *testing.T) {
	installRoot := t.TempDir()
	src := t.TempDir()
	makeGoldenPlugin(t, src)
	if _, err := InstallFromLocal(src, installRoot); err != nil {
		t.Fatal(err)
	}

	// Data dir exists.
	dataDir := filepath.Join(installRoot, "data", "golden")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := Remove("golden", installRoot, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(installRoot, "golden")); !os.IsNotExist(err) {
		t.Fatal("plugin dir must be gone")
	}
	if _, err := os.Stat(dataDir); err != nil {
		t.Fatal("data dir must survive without purgeData")
	}

	// Reinstall, then purge data too.
	if _, err := InstallFromLocal(src, installRoot); err != nil {
		t.Fatal(err)
	}
	if err := Remove("golden", installRoot, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dataDir); !os.IsNotExist(err) {
		t.Fatal("data dir must be purged with purgeData")
	}
}

func TestRemoveReservedNames(t *testing.T) {
	installRoot := t.TempDir()
	for _, name := range []string{"data", "registry.json"} {
		if err := Remove(name, installRoot, false); err == nil {
			t.Errorf("Remove(%q) must be rejected", name)
		}
	}
}

func TestSetEnabled(t *testing.T) {
	path := filepath.Join(t.TempDir(), "registry.json")
	r, err := LoadRegistry(path)
	if err != nil {
		t.Fatal(err)
	}
	r.Entries["golden"] = RegistryEntry{Name: "golden", Enabled: true}
	if err := r.SetEnabled("golden", false); err != nil {
		t.Fatal(err)
	}
	if err := r.Save(); err != nil {
		t.Fatal(err)
	}
	r2, err := LoadRegistry(path)
	if err != nil {
		t.Fatal(err)
	}
	if r2.Entries["golden"].Enabled {
		t.Fatal("enabled must roundtrip as false")
	}
	if err := r.SetEnabled("missing", true); err == nil {
		t.Fatal("SetEnabled for unknown plugin must fail")
	}
}

func TestInstallFromGit(t *testing.T) {
	if _, err := InstallFromGit("https://example.invalid/nope.git", "", t.TempDir()); err == nil {
		t.Fatal("clone of unreachable repo must fail")
	}
}
