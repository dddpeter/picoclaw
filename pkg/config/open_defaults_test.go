package config

import "testing"

// Fork open-by-default sandbox: unrestricted filesystem access (minus the
// system-path guard) and general-command exec. Upstream defaults to a
// workspace sandbox; these pins keep the fork default from regressing.
func TestOpenByDefaultSandboxDefaults(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Agents.Defaults.RestrictToWorkspace {
		t.Fatal("restrict_to_workspace must default to false (fork)")
	}
	if !cfg.Tools.EffectiveProtectSystemPaths() {
		t.Fatal("tools.protect_system_paths must default to enabled (nil)")
	}
	explicitFalse := boolPtr(false)
	cfg.Tools.ProtectSystemPaths = explicitFalse
	if cfg.Tools.EffectiveProtectSystemPaths() {
		t.Fatal("explicit protect_system_paths=false must disable the guard")
	}
}
