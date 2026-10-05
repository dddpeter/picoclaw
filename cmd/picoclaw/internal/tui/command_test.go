package tui

import (
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
