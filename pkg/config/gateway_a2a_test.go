package config

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestGatewayA2ADefaultsOff pins the iron rule: A2A is disabled unless the
// config explicitly turns it on (default config AND absent config section).
func TestGatewayA2ADefaultsOff(t *testing.T) {
	def := DefaultConfig()
	if def.Gateway.A2A.Enabled {
		t.Error("DefaultConfig().Gateway.A2A.Enabled = true, want false")
	}

	// A config file without an a2a section parses to disabled.
	var cfg struct {
		Gateway GatewayConfig `json:"gateway"`
	}
	if err := json.Unmarshal([]byte(`{"gateway":{"port":18790}}`), &cfg); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if cfg.Gateway.A2A.Enabled {
		t.Error("absent a2a section parsed as enabled")
	}
}

// TestGatewayA2AParse pins the config shape: gateway.a2a{enabled,token}.
func TestGatewayA2AParse(t *testing.T) {
	var cfg struct {
		Gateway GatewayConfig `json:"gateway"`
	}
	input := `{"gateway":{"port":18790,"a2a":{"enabled":true,"token":"t0k3n"}}}`
	if err := json.Unmarshal([]byte(input), &cfg); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !cfg.Gateway.A2A.Enabled {
		t.Error("a2a.enabled = false, want true")
	}
	if cfg.Gateway.A2A.Token != "t0k3n" {
		t.Errorf("a2a.token = %q, want t0k3n", cfg.Gateway.A2A.Token)
	}
}

// TestGatewayA2AJSONTag pins the exact wire key so config files are stable.
func TestGatewayA2AJSONTag(t *testing.T) {
	raw, err := json.Marshal(GatewayA2AConfig{Enabled: true, Token: "x"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(raw)
	if !strings.Contains(s, `"enabled"`) || !strings.Contains(s, `"token"`) {
		t.Errorf("marshaled keys wrong: %s", s)
	}
}
