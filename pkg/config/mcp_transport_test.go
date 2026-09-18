package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestNormalizeMCPTransportType covers canonicalization of MCP transport
// names from config. "http" is the canonical streamable HTTP mode;
// "streamable-http" and its spelling variants are accepted aliases.
func TestNormalizeMCPTransportType(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		// canonical passthrough
		{name: "stdio passthrough", in: "stdio", want: "stdio"},
		{name: "sse passthrough", in: "sse", want: "sse"},
		{name: "http passthrough", in: "http", want: "http"},

		// aliases canonicalize to "http"
		{name: "streamable-http alias", in: "streamable-http", want: "http"},
		{name: "streamable_http alias", in: "streamable_http", want: "http"},
		{name: "streamablehttp alias", in: "streamablehttp", want: "http"},

		// case and whitespace normalization
		{name: "upper case", in: "SSE", want: "sse"},
		{name: "mixed case stdio", in: " StDiO ", want: "stdio"},
		{name: "mixed case alias", in: "Streamable-HTTP", want: "http"},

		// empty / unknown values pass through unchanged
		{name: "empty string", in: "", want: ""},
		{name: "unknown transport", in: "websocket", want: "websocket"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, NormalizeMCPTransportType(tt.in))
		})
	}
}

// TestEffectiveMCPTransportType covers inference of the effective transport
// when Type is unset or empty: URL implies "sse", Command implies "stdio",
// and an empty server config has no transport at all.
func TestEffectiveMCPTransportType(t *testing.T) {
	tests := []struct {
		name   string
		server MCPServerConfig
		want   string
	}{
		{
			name:   "explicit type wins",
			server: MCPServerConfig{Type: "streamable-http", URL: "http://x", Command: "npx"},
			want:   "http",
		},
		{
			name:   "empty type with url infers sse",
			server: MCPServerConfig{URL: "http://localhost:8080/mcp"},
			want:   "sse",
		},
		{
			name:   "empty type with command infers stdio",
			server: MCPServerConfig{Command: "npx"},
			want:   "stdio",
		},
		{
			name:   "url beats command when both set and type empty",
			server: MCPServerConfig{URL: "http://x", Command: "npx"},
			want:   "sse",
		},
		{
			name:   "empty server config yields empty",
			server: MCPServerConfig{},
			want:   "",
		},
		{
			name:   "whitespace-only type is treated as empty",
			server: MCPServerConfig{Type: "   ", URL: "http://x"},
			want:   "sse",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, EffectiveMCPTransportType(tt.server))
		})
	}
}
