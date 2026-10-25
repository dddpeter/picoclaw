package agent

import (
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
)

// singleServerCfg returns a fresh MCPConfig with exactly one server (maps are
// never shared between cases: config.MCPConfig carries reference-type fields).
func singleServerCfg(id, command string) config.MCPConfig {
	return config.MCPConfig{
		Servers: map[string]config.MCPServerConfig{
			id: {Enabled: true, Command: command, Args: []string{"-y", "demo"}},
		},
	}
}

// TestMCPConfigChanged pins the hot-reload fast path's predicate: only a real
// difference in the MCP section forces a manager teardown + rebuild. nil and
// empty Servers both mean "no servers" and must compare equal so that a
// model-only config touch keeps existing MCP connections alive.
func TestMCPConfigChanged(t *testing.T) {
	cases := []struct {
		name string
		old  config.MCPConfig
		new  config.MCPConfig
		want bool
	}{
		{"identical configs", singleServerCfg("ctx7", "npx"), singleServerCfg("ctx7", "npx"), false},
		{"nil servers vs empty map", config.MCPConfig{}, config.MCPConfig{Servers: map[string]config.MCPServerConfig{}}, false},
		{"both empty", config.MCPConfig{}, config.MCPConfig{}, false},
		{"spawn command changed", singleServerCfg("ctx7", "npx"), singleServerCfg("ctx7", "node"), true},
		{"server added", func() config.MCPConfig {
			c := singleServerCfg("ctx7", "npx")
			c.Servers["fetch"] = config.MCPServerConfig{Enabled: true, Command: "uvx", Args: []string{"fetch"}}
			return c
		}(), singleServerCfg("ctx7", "npx"), true},
		{"server removed", singleServerCfg("ctx7", "npx"), config.MCPConfig{Servers: map[string]config.MCPServerConfig{}}, true},
		{"threshold field changed", func() config.MCPConfig {
			c := singleServerCfg("ctx7", "npx")
			c.MaxInlineTextChars = 9999
			return c
		}(), singleServerCfg("ctx7", "npx"), true},
		{"env changed", func() config.MCPConfig {
			c := singleServerCfg("ctx7", "npx")
			c.Servers["ctx7"] = config.MCPServerConfig{
				Enabled: true, Command: "npx", Args: []string{"-y", "demo"},
				Env: map[string]string{"K": "V"},
			}
			return c
		}(), singleServerCfg("ctx7", "npx"), true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := mcpConfigChanged(tc.old, tc.new); got != tc.want {
				t.Fatalf("mcpConfigChanged = %v, want %v", got, tc.want)
			}
		})
	}
}
