package config

// LspToolsConfig configures the LSP tools (fork feature; see
// docs/design/lsp-support-design.zh.md §3.3). All optional fields default
// sensibly: enabled, injection on, 20s request timeout, 50-file limit,
// 5-minute idle session TTL, and the built-in server catalog.
type LspToolsConfig struct {
	// Enabled is nil = enabled (fork convention). Explicit false removes
	// both lsp tools.
	Enabled *bool `json:"enabled,omitempty" env:"PICOCLAW_TOOLS_LSP_ENABLED"`
	// InjectOnEdit is nil = enabled: successful edit_file/write_file/
	// append_file calls append error-level diagnostics for the edited file.
	InjectOnEdit *bool `json:"inject_on_edit,omitempty" env:"PICOCLAW_TOOLS_LSP_INJECT_ON_EDIT"`
	// TimeoutSeconds caps a single LSP request (default 20).
	TimeoutSeconds int `json:"timeout_seconds,omitempty" env:"PICOCLAW_TOOLS_LSP_TIMEOUT_SECONDS"`
	// MaxFiles caps files opened per diagnostics call (default 50).
	MaxFiles int `json:"max_files,omitempty" env:"PICOCLAW_TOOLS_LSP_MAX_FILES"`
	// IdleTTLSeconds reaps idle language-server sessions (default 300).
	IdleTTLSeconds int `json:"idle_ttl_seconds,omitempty" env:"PICOCLAW_TOOLS_LSP_IDLE_TTL_SECONDS"`
	// Servers merges with the built-in catalog by name; `disabled: true`
	// removes an entry (opencode-style merge semantics, design §2.7).
	Servers map[string]LspServerConfig `json:"servers,omitempty"`
}

// LspServerConfig is one user-configured language server.
type LspServerConfig struct {
	// Command is argv (["gopls"] or ["typescript-language-server","--stdio"]).
	Command []string `json:"command,omitempty"`
	// Extensions routes files to this server (leading dot optional).
	Extensions []string `json:"extensions,omitempty"`
	// Disabled removes this server (including built-ins).
	Disabled *bool `json:"disabled,omitempty"`
	// Env overrides the inherited environment for the server process.
	Env map[string]string `json:"env,omitempty"`
	// Initialization is sent as initializationOptions and as
	// workspace/didChangeConfiguration settings.
	Initialization map[string]any `json:"initialization,omitempty"`
	// SkipDirs adds directory names to the common skip set.
	SkipDirs []string `json:"skip_dirs,omitempty"`
	// PushDiagnosticsGraceMs treats "no publish within this window" as a
	// clean document for push-only servers that stay silent when clean.
	PushDiagnosticsGraceMs int `json:"push_diagnostics_grace_ms,omitempty"`
	// PullDiagnosticsGraceMs keeps waiting for a push after an empty pull
	// result (servers that answer before analysis finishes).
	PullDiagnosticsGraceMs int `json:"pull_diagnostics_grace_ms,omitempty"`
}

// EffectiveEnabled reports whether LSP tooling is on (nil = enabled).
func (c LspToolsConfig) EffectiveEnabled() bool {
	return c.Enabled == nil || *c.Enabled
}

// EffectiveInjectOnEdit reports whether diagnostics are appended to edit
// results (nil = enabled).
func (c LspToolsConfig) EffectiveInjectOnEdit() bool {
	return c.InjectOnEdit == nil || *c.InjectOnEdit
}

// EffectiveTimeoutSeconds returns the per-request LSP timeout.
func (c LspToolsConfig) EffectiveTimeoutSeconds() int {
	if c.TimeoutSeconds > 0 {
		return c.TimeoutSeconds
	}
	return 20
}

// EffectiveMaxFiles returns the per-call file limit.
func (c LspToolsConfig) EffectiveMaxFiles() int {
	if c.MaxFiles > 0 {
		return c.MaxFiles
	}
	return 50
}

// EffectiveIdleTTLSeconds returns the idle session TTL.
func (c LspToolsConfig) EffectiveIdleTTLSeconds() int {
	if c.IdleTTLSeconds > 0 {
		return c.IdleTTLSeconds
	}
	return 300
}

// EffectiveIsDisabled interprets the nil-able Disabled flag.
func (s LspServerConfig) EffectiveIsDisabled() bool {
	return s.Disabled != nil && *s.Disabled
}
