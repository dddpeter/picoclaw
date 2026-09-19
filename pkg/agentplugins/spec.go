// Package agentplugins implements an Agent Plugins Specification 1.0.0
// compatible client: loading, validating, installing plugin packages and
// bridging their skills and MCP servers into the host.
//
// The package is pure logic with zero gateway dependencies — it only imports
// the standard library so hosts (pkg/agent, pkg/skills, cmd/picoclaw) can
// bridge its outputs without import cycles.
package agentplugins

import "fmt"

// SpecVersion is the Agent Plugins specification version this client supports.
const SpecVersion = "1.0.0"

// Canonical $schema identifiers (spec §5.2 / §7.2.1, verbatim).
const (
	ManifestSchemaURL  = "https://agent-plugins.org/schemas/1.0.0/plugin.schema.json"
	MCPConfigSchemaURL = "https://agent-plugins.org/schemas/1.0.0/mcp.schema.json"
)

// SupportedManifestSchema reports whether the manifest $schema URL is a
// locally supported version. Only the exact 1.0.0 canonical identifier is
// supported; unknown/draft versions (e.g. 1.1.0) are rejected (design D5).
func SupportedManifestSchema(u string) bool { return u == ManifestSchemaURL }

// Report collects structured diagnostics (spec SHOULD-report exits).
type Report struct {
	Warnings []string
	Fatals   []string
}

func (r *Report) Warnf(format string, a ...any) {
	r.Warnings = append(r.Warnings, fmt.Sprintf(format, a...))
}

func (r *Report) Fatalf(format string, a ...any) {
	r.Fatals = append(r.Fatals, fmt.Sprintf(format, a...))
}

// OK reports whether no fatal diagnostic has been recorded.
func (r *Report) OK() bool { return len(r.Fatals) == 0 }
