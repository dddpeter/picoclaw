package tui

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
)

func TestGatewayURLFromConfig(t *testing.T) {
	cases := []struct {
		host string
		port int
		want string
	}{
		// Loopback aliases normalize to 127.0.0.1: dialing "localhost" can
		// die on LAN DNS ("lookup localhost … no such host") even though the
		// gateway is bound to loopback.
		{"localhost", 18790, "ws://127.0.0.1:18790/pico/ws"},
		{"LocalHost", 18790, "ws://127.0.0.1:18790/pico/ws"},
		{"::1", 18790, "ws://127.0.0.1:18790/pico/ws"},
		{"[::1]", 18790, "ws://127.0.0.1:18790/pico/ws"},
		{"0.0.0.0", 18790, "ws://127.0.0.1:18790/pico/ws"},
		{"", 18790, "ws://127.0.0.1:18790/pico/ws"},
		// Explicit non-loopback hosts pass through untouched.
		{"192.168.1.10", 2000, "ws://192.168.1.10:2000/pico/ws"},
		{"gateway.example.com", 2000, "ws://gateway.example.com:2000/pico/ws"},
		// Port falls back to the config default when unset.
		{"", 0, "ws://127.0.0.1:18790/pico/ws"},
	}
	for _, tc := range cases {
		got := gatewayURLFromConfig(&config.Config{Gateway: config.GatewayConfig{Host: tc.host, Port: tc.port}})
		if got != tc.want {
			t.Errorf("host=%q port=%d: got %q, want %q", tc.host, tc.port, got, tc.want)
		}
	}
}

func TestIsLocalhostURL(t *testing.T) {
	cases := map[string]bool{
		"ws://127.0.0.1:18790/pico/ws": true,
		"ws://localhost:18790/pico/ws": true,
		"ws://[::1]:1/x":               true,
		"wss://example.com/pico/ws":    false,
		"ws://192.168.1.5:1/x":         false,
	}
	for raw, want := range cases {
		if got := isLocalhostURL(raw); got != want {
			t.Errorf("isLocalhostURL(%q) = %v, want %v", raw, got, want)
		}
	}
}

func TestRunTUIPersistsSelectedSession(t *testing.T) {
	for _, mode := range []string{"first", "new", "explicit"} {
		t.Run(mode, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("PICOCLAW_HOME", home)
			t.Setenv("PICOCLAW_CONFIG", filepath.Join(home, "config.json"))
			if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(`{"version":3,"agents":{}}`), 0o600); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(home, "tui_session")
			opts := &tuiOptions{GatewayURL: "ws://127.0.0.1:1/pico/ws", Token: "test"}
			if mode != "first" {
				if err := os.WriteFile(path, []byte("previous-session"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "new" {
				opts.NewSession = true
			}
			if mode == "explicit" {
				opts.SessionID = "explicit-session"
			}
			ctx, cancel := context.WithCancel(t.Context())
			cancel() // No real gateway or interactive terminal is needed.
			_ = runTUI(ctx, NewTUICommand(), opts)
			got := loadStoredSession(path)
			if got == "" || got == "previous-session" {
				t.Fatalf("selected session was not persisted: %q", got)
			}
			if mode == "explicit" && got != opts.SessionID {
				t.Fatalf("stored %q, want explicit selection %q", got, opts.SessionID)
			}
		})
	}
}

func TestPersistSessionCreatesParentAndReportsErrors(t *testing.T) {
	m := newTestApp(t)
	m.sessionFile = filepath.Join(t.TempDir(), "nested", "tui_session")
	m.persistSession("saved-session")
	if got := loadStoredSession(m.sessionFile); got != "saved-session" {
		t.Fatalf("nested session store = %q", got)
	}
	// A directory at the target path makes atomic replacement fail on all OSes.
	m.sessionFile = t.TempDir()
	m.persistSession("cannot-save")
	if len(m.state.Items) == 0 || m.state.Items[0].Kind != ItemError {
		t.Fatal("session persistence failure must be visible, not silently discarded")
	}
}

func TestLoadTUIConfigWithExplicitEndpoint(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "config.json")
	t.Setenv("PICOCLAW_HOME", home)
	t.Setenv("PICOCLAW_CONFIG", path)
	workspace := filepath.Join(home, "workspace")
	data, err := json.Marshal(map[string]any{
		"version": 3,
		"agents":  map[string]any{"defaults": map[string]any{"workspace": workspace}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	opts := &tuiOptions{GatewayURL: "ws://127.0.0.1:1/pico/ws", Token: "test"}
	cfg, err := loadTUIConfig(opts)
	if err != nil || cfg == nil || cfg.Agents.Defaults.Workspace != workspace {
		t.Fatalf("explicit connection must still load local history config: cfg=%v err=%v", cfg != nil, err)
	}

	t.Setenv("PICOCLAW_CONFIG", home) // A directory is not a readable config file.
	cfg, err = loadTUIConfig(opts)
	if err != nil || cfg != nil {
		t.Fatalf("explicit endpoint/token should tolerate unavailable config: cfg=%v err=%v", cfg != nil, err)
	}
	opts.Token = ""
	if _, err := loadTUIConfig(opts); err == nil {
		t.Fatal("missing connection settings still require valid config")
	}
}
