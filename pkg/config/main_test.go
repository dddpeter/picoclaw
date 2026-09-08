package config

import (
	"os"
	"strings"
	"testing"
)

// TestMain isolates the test binary from the ambient picoclaw environment.
//
// LoadConfig applies env overrides (caarlos0/env) with higher precedence than
// config.json/.security.yml values. When tests run inside the picoclaw
// service's process tree (e.g. `go test` from a picoclaw-managed exec), the
// gateway exports live credentials such as PICOCLAW_CHANNELS_FEISHU_APP_SECRET
// and those would silently override fixture values — asserting the wrong data
// and even leaking real secrets into test logs (seen 2026-09-08:
// TestAllSecurityKeysAccessible failed with a production Feishu app_secret).
//
// Scrubbing here covers every test in the package; tests that need a specific
// variable set it explicitly via t.Setenv (the existing convention, see
// gateway_host_env_test.go / config_channel_test.go).
//
// Also scrubbed: bare names without the PICOCLAW_ prefix that struct tags use
// for wecom (BOT_ID/SECRET/WEBSOCKET_URL/SEND_THINKING_MESSAGE) and tool
// toggles (ENABLED) — they are generic names that may exist in ambient
// environments for unrelated reasons.
func TestMain(m *testing.M) {
	const prefix = "PICOCLAW_"
	bareNames := []string{
		"BOT_ID", "SECRET", "WEBSOCKET_URL", "SEND_THINKING_MESSAGE",
		"ENABLED",
	}

	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(name, prefix) {
			if os.Unsetenv(name) != nil {
				os.Exit(1)
			}
			continue
		}
		for _, bare := range bareNames {
			if name == bare {
				if os.Unsetenv(name) != nil {
					os.Exit(1)
				}
				break
			}
		}
	}

	os.Exit(m.Run())
}
