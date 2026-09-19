// Package lsp implements a minimal LSP client over stdio for targeted
// diagnostics and source fixes. Design: docs/design/lsp-support-design.zh.md.
//
// The client speaks a deliberately small subset of LSP: initialize,
// didOpen/didChange/didClose, diagnostics (push and pull), code actions and
// their resolution. Capabilities are advertised statically (no dynamic
// registration) because sessions are pooled and reused, not editor-grade.
package lsp

import "time"

// ServerConfig describes one language server: how to start it and which
// files it handles. Mirrors pi-lsp/opencode routing semantics (see design
// doc §2.1/§2.7).
type ServerConfig struct {
	Name    string
	Command string   // executable
	Args    []string // argv tail
	// Extensions routes files to this server (with leading dot, e.g. ".go").
	Extensions []string
	// Env overrides the inherited environment.
	Env map[string]string
	// Initialization is merged into initializationOptions and sent as
	// workspace/didChangeConfiguration settings after init.
	Initialization map[string]any
	// SkipDirectories are directory names (not paths) excluded from file
	// collection, added to the common skip set.
	SkipDirectories []string
	// PushDiagnosticsGraceMs: for push-only servers that stay silent on
	// clean documents, treat "no publish within this window" as clean.
	PushDiagnosticsGraceMs int
	// PullDiagnosticsGraceMs: after an empty pull result, keep waiting for
	// a push this long before reporting clean (rust-analyzer-style servers
	// answer the first pull before analysis finishes).
	PullDiagnosticsGraceMs int
	// Disabled removes the server (merge semantics, config only).
	Disabled bool
}

// RequestTimeout is the per-request cap for JSON-RPC calls. Default.
const DefaultRequestTimeout = 20 * time.Second

// DiagnosticsSettle is the quiet period after a publish before push
// diagnostics are considered final (pi-lsp uses 800ms; bounded servers
// republish quickly when more is coming).
const DiagnosticsSettle = 800 * time.Millisecond

// Position is an LSP position: line and UTF-16 character offset, 0-based.
type Position struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

// Range is an LSP range.
type Range struct {
	Start Position `json:"start"`
	End   Position `json:"end"`
}

// Diagnostic is one LSP diagnostic (subset of fields we consume).
type Diagnostic struct {
	Range    Range  `json:"range"`
	Severity int    `json:"severity,omitempty"`
	Code     any    `json:"code,omitempty"`
	Source   string `json:"source,omitempty"`
	Message  string `json:"message"`
}

// SeverityName maps LSP severity numbers to display names.
func SeverityName(severity int) string {
	switch severity {
	case 1:
		return "error"
	case 2:
		return "warning"
	case 3:
		return "info"
	case 4:
		return "hint"
	default:
		return "diagnostic"
	}
}
