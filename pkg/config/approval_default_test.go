package config

import (
	"encoding/json"
	"testing"
)

// Fork: approval builtin ships enabled by default (fail-closed HITL).
func TestApprovalBuiltinEnabledByDefault(t *testing.T) {
	cfg := DefaultConfig()
	if !cfg.Hooks.Builtins["approval"].Enabled {
		t.Fatal("approval builtin must default to enabled")
	}
	var hc map[string]any
	if err := json.Unmarshal(cfg.Hooks.Builtins["approval"].Config, &hc); err != nil {
		t.Fatalf("default approval config JSON invalid: %v", err)
	}
	if len(hc["ask_patterns"].([]any)) == 0 {
		t.Fatal("default ask_patterns must be non-empty")
	}
}

// Configs that predate the default keep their explicit opt-out: loading
// user JSON over defaults is map-merge, and a full "approval" entry from
// the user replaces the default wholesale.
func TestApprovalBuiltinUserEntryWins(t *testing.T) {
	data := []byte(`{
		"version": 1,
		"hooks": {"enabled": true, "builtins": {"approval": {"enabled": false}}}
	}`)
	var tmp Config
	if err := json.Unmarshal(data, &tmp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	cfg := DefaultConfig()
	if tmp.Hooks.Builtins["approval"].Enabled {
		t.Fatal("user explicit enabled:false must be honored (precondition)")
	}
	// simulate loadConfigLenient's decode-over-defaults
	if err := json.Unmarshal(data, cfg); err != nil {
		t.Fatalf("decode over defaults: %v", err)
	}
	if cfg.Hooks.Builtins["approval"].Enabled {
		t.Fatal("explicit user enabled:false must override the enabled default")
	}
}
