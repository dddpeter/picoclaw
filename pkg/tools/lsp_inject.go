package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/lsp"
	toolshared "github.com/sipeed/picoclaw/pkg/tools/shared"
)

// editDiagnosticsMaxPerFile caps injected diagnostic lines (opencode
// semantics: errors only, 20 per file — design §3.4).
const editDiagnosticsMaxPerFile = 20

// editDiagnosticsTimeout bounds the wait for post-edit diagnostics: the edit
// result must never hang on a slow server.
const editDiagnosticsTimeout = 2 * time.Second

// lspInjectingTool wraps a file-editing tool and appends error-level LSP
// diagnostics for the edited file to successful results (fork feature,
// design §3.4 — the model gets an edit→diagnostics→fix loop without having
// to remember to call lsp_diagnostics).
type lspInjectingTool struct {
	inner toolshared.Tool
	inj   *editDiagnosticsInjector
}

type editDiagnosticsInjector struct {
	workspace string
	servers   []lsp.ServerConfig
	pool      *lsp.Pool
}

// WithEditDiagnostics wraps a file-editing tool with post-edit LSP
// diagnostics injection. Returns the tool unchanged when injection is off.
func WithEditDiagnostics(
	tool toolshared.Tool,
	cfg config.LspToolsConfig,
	workspace string,
	pool *lsp.Pool,
) toolshared.Tool {
	if !cfg.EffectiveEnabled() || !cfg.EffectiveInjectOnEdit() {
		return tool
	}
	if pool == nil {
		pool = sharedLspPool(cfg)
	}
	return &lspInjectingTool{
		inner: tool,
		inj: &editDiagnosticsInjector{
			workspace: workspace,
			servers:   resolveLspServers(cfg),
			pool:      pool,
		},
	}
}

func (w *lspInjectingTool) Name() string               { return w.inner.Name() }
func (w *lspInjectingTool) Description() string        { return w.inner.Description() }
func (w *lspInjectingTool) Parameters() map[string]any { return w.inner.Parameters() }

func (w *lspInjectingTool) Execute(ctx context.Context, args map[string]any) *ToolResult {
	result := w.inner.Execute(ctx, args)
	if result == nil || result.IsError {
		return result
	}
	path, _ := args["path"].(string)
	if strings.TrimSpace(path) == "" {
		return result
	}
	block := w.inj.diagnosticsBlock(path)
	if block == "" {
		return result
	}
	augmented := *result
	augmented.ForLLM = result.ForLLM + "\n\n" + block
	return &augmented
}

// diagnosticsBlock runs bounded diagnostics for one file and returns the
// error block, or "" when there is nothing to inject (no server, no
// command, broken session, timeout, no errors — silence costs nothing).
func (i *editDiagnosticsInjector) diagnosticsBlock(path string) string {
	abs, err := resolveInjectionPath(i.workspace, path)
	if err != nil {
		return ""
	}
	var server *lsp.ServerConfig
	for idx := range i.servers {
		if i.servers[idx].Supports(abs) && i.servers[idx].CommandAvailable() {
			server = &i.servers[idx]
			break
		}
	}
	if server == nil {
		return ""
	}
	if _, broken := i.pool.Broken(i.workspace, server.Name); broken {
		return ""
	}

	ctx, cancel := context.WithTimeout(context.Background(), editDiagnosticsTimeout)
	defer cancel()

	client, err := i.pool.Acquire(ctx, *server, i.workspace)
	if err != nil {
		return ""
	}
	defer i.pool.Touch(i.workspace, server.Name)

	text, err := os.ReadFile(abs)
	if err != nil {
		return ""
	}
	uri, err := client.TouchFile(ctx, abs, string(text), lsp.LanguageID(abs))
	if err != nil {
		return ""
	}
	diags, err := client.Diagnostics(ctx, uri)
	if err != nil {
		return ""
	}

	var lines []string
	for _, d := range diags {
		if d.Severity != 1 { // errors only
			continue
		}
		if len(lines) >= editDiagnosticsMaxPerFile {
			lines = append(lines, fmt.Sprintf("... and %d more", countErrors(diags)-editDiagnosticsMaxPerFile))
			break
		}
		lines = append(lines, fmt.Sprintf("ERROR [%d:%d] %s",
			d.Range.Start.Line+1, d.Range.Start.Character+1, firstLine(d.Message)))
	}
	if len(lines) == 0 {
		return ""
	}
	return fmt.Sprintf("LSP errors detected in this file, please fix:\n<diagnostics file=%q>\n%s\n</diagnostics>",
		relTo(i.workspace, abs), strings.Join(lines, "\n"))
}

func countErrors(diags []lsp.Diagnostic) int {
	n := 0
	for _, d := range diags {
		if d.Severity == 1 {
			n++
		}
	}
	return n
}

// resolveInjectionPath resolves the (already tool-validated) path against
// the workspace. The inner tool has already enforced access rules; this is
// only address resolution.
func resolveInjectionPath(workspace, path string) (string, error) {
	if !filepath.IsAbs(path) {
		path = filepath.Join(workspace, path)
	}
	return filepath.Clean(path), nil
}
