package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/lsp"
	fstools "github.com/sipeed/picoclaw/pkg/tools/fs"
)

// LspServerError marks user-actionable LSP tool failures (bad server name,
// unsupported file, missing command).
type LspServerError struct{ Message string }

func (e *LspServerError) Error() string { return e.Message }

// lspDiagnosticsLineCap bounds the diagnostic lines in one tool result;
// beyond it the output truncates with a note (read outputs stay bounded,
// unlike pi-lsp's unbounded contract — design §3.5).
const lspDiagnosticsLineCap = 200

// sharedLspPool is the process-wide session pool: every agent instance
// shares sessions per (root, server), which dedups cold starts across
// reloads. The TTL reaper and Windows KILL_ON_CLOSE job handles bound its
// lifetime; there is no explicit shutdown.
var (
	sharedLspPoolOnce sync.Once
	sharedLspPoolVal  *lsp.Pool
)

func sharedLspPool(cfg config.LspToolsConfig) *lsp.Pool {
	sharedLspPoolOnce.Do(func() {
		sharedLspPoolVal = lsp.NewPool(
			time.Duration(cfg.EffectiveIdleTTLSeconds())*time.Second,
			time.Duration(cfg.EffectiveTimeoutSeconds())*time.Second,
		)
	})
	return sharedLspPoolVal
}

// LspDiagnosticsTool runs targeted diagnostics through configured language
// servers (fork feature, docs/design/lsp-support-design.zh.md).
type LspDiagnosticsTool struct {
	workspace string
	restrict  bool
	allow     []*regexp.Regexp
	servers   []lsp.ServerConfig
	pool      *lsp.Pool
	cfg       config.LspToolsConfig
}

// NewLspDiagnosticsTool builds the diagnostics tool on the process-shared
// session pool. Path validation reuses the fs tooling's read-side rules
// (workspace restriction, allow-read patterns, system-path protection).
func NewLspDiagnosticsTool(
	workspace string,
	restrict bool,
	allowPaths []*regexp.Regexp,
	cfg config.LspToolsConfig,
) *LspDiagnosticsTool {
	return NewLspDiagnosticsToolWithPool(workspace, restrict, allowPaths, cfg, nil)
}

// NewLspDiagnosticsToolWithPool is NewLspDiagnosticsTool with an injectable
// session pool (tests own the lifecycle; nil uses the shared pool).
func NewLspDiagnosticsToolWithPool(
	workspace string,
	restrict bool,
	allowPaths []*regexp.Regexp,
	cfg config.LspToolsConfig,
	pool *lsp.Pool,
) *LspDiagnosticsTool {
	if pool == nil {
		pool = sharedLspPool(cfg)
	}
	return &LspDiagnosticsTool{
		workspace: workspace,
		restrict:  restrict,
		allow:     allowPaths,
		servers:   resolveLspServers(cfg),
		pool:      pool,
		cfg:       cfg,
	}
}

func (t *LspDiagnosticsTool) Name() string { return "lsp_diagnostics" }

func (t *LspDiagnosticsTool) Description() string {
	return "Run targeted diagnostics on files through configured language servers (LSP). Intermediate feedback between edits — still run the project's authoritative build/test commands before declaring completion. Paths default to the workspace root; servers route by file extension. Configure servers under tools.lsp in config.json."
}

func (t *LspDiagnosticsTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "File or directory to check (defaults to the workspace root).",
			},
			"server": map[string]any{
				"type":        "string",
				"description": "Optional configured server name to use (defaults to all servers matching the files' extensions).",
			},
			"limit": map[string]any{
				"type":        "integer",
				"description": "Maximum number of files to open per server (default 50).",
			},
		},
		"required": []string{"path"},
	}
}

func (t *LspDiagnosticsTool) Execute(ctx context.Context, args map[string]any) *ToolResult {
	rawPath, _ := args["path"].(string)
	if strings.TrimSpace(rawPath) == "" {
		return ErrorResult("path is required")
	}
	var explicit []string
	if server, ok := args["server"].(string); ok && strings.TrimSpace(server) != "" {
		explicit = strings.FieldsFunc(server, func(r rune) bool { return r == ',' || r == ' ' })
	}
	limit := t.cfg.EffectiveMaxFiles()
	if v, ok := args["limit"].(float64); ok && int(v) > 0 {
		limit = int(v)
	}

	root := t.workspace
	var inputs []string
	// Path validation: resolve relative to the workspace and apply the
	// read-side rules (workspace restriction, allow patterns, protected
	// system paths) — one per path; the fs validator returns abs paths.
	for _, p := range strings.FieldsFunc(rawPath, func(r rune) bool { return r == ';' }) {
		abs, err := lspValidateReadPath(t, p)
		if err != nil {
			return ErrorResult(err.Error())
		}
		inputs = append(inputs, abs)
	}
	if len(inputs) == 0 {
		return ErrorResult("path is required")
	}

	routes, skipped, err := selectLspRoutes(t.servers, root, inputs, explicit, limit)
	if err != nil {
		return ErrorResult(err.Error())
	}
	if len(routes) == 0 {
		if len(skipped) > 0 && len(t.servers) == len(skipped) {
			return NewToolResult("No runnable LSP servers. Skipped: " + strings.Join(skipped, "; "))
		}
		msg := "No supported files found in the requested paths for any configured server."
		if len(skipped) > 0 {
			msg += " Skipped servers: " + strings.Join(skipped, "; ")
		}
		return NewToolResult(msg)
	}

	var (
		mu                     sync.Mutex
		wg                     sync.WaitGroup
		sections               []string
		totalDiags, totalFiles int
		firstErr               string
	)
	for _, route := range routes {
		wg.Add(1)
		go func(route lspRoute) {
			defer wg.Done()
			section, files, diags, err := t.runRoute(ctx, route, root)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				if firstErr == "" {
					firstErr = fmt.Sprintf("%s: %v", route.server.Name, err)
				}
				return
			}
			sections = append(sections, section)
			totalDiags += diags
			totalFiles += files
		}(route)
	}
	wg.Wait()

	sort.Strings(sections)
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("LSP diagnostics: %d diagnostic(s) across %d file(s).\n", totalDiags, totalFiles))
	if len(skipped) > 0 {
		sb.WriteString("Skipped servers: " + strings.Join(skipped, "; ") + "\n")
	}
	if firstErr != "" {
		sb.WriteString("Failed: " + firstErr + "\n")
	}
	for _, s := range sections {
		sb.WriteString("\n" + s)
	}
	out := sb.String()
	if lines := strings.Count(out, "\n"); lines > lspDiagnosticsLineCap {
		out = truncateLines(out, lspDiagnosticsLineCap) +
			fmt.Sprintf("\n... output truncated (%d lines shown; narrow the path for full results)", lspDiagnosticsLineCap)
	}
	if firstErr != "" && totalFiles == 0 {
		return ErrorResult(out)
	}
	return NewToolResult(out)
}

// runRoute opens the route's files in one server session and collects
// diagnostics.
func (t *LspDiagnosticsTool) runRoute(ctx context.Context, route lspRoute, root string) (string, int, int, error) {
	client, err := t.pool.Acquire(ctx, route.server, root)
	if err != nil {
		return "", 0, 0, err
	}
	defer t.pool.Touch(root, route.server.Name)

	var lines []string
	diagCount := 0
	for _, file := range route.files {
		if ctx.Err() != nil {
			break
		}
		text, err := os.ReadFile(file)
		if err != nil {
			lines = append(lines, fmt.Sprintf("%s: read failed: %v", relTo(root, file), err))
			continue
		}
		uri, err := client.TouchFile(ctx, file, string(text), lsp.LanguageID(file))
		if err != nil {
			return "", 0, 0, err
		}
		diags, err := client.Diagnostics(ctx, uri)
		if err != nil {
			return "", 0, 0, err
		}
		rel := relTo(root, file)
		if len(diags) == 0 {
			lines = append(lines, rel+": no diagnostics")
			continue
		}
		for _, d := range diags {
			diagCount++
			code := ""
			if d.Code != nil {
				code = " " + fmt.Sprint(d.Code)
			}
			source := d.Source
			if source == "" {
				source = route.server.Name
			}
			lines = append(lines, fmt.Sprintf("%s:%d:%d: %s %s%s: %s",
				rel, d.Range.Start.Line+1, d.Range.Start.Character+1,
				lsp.SeverityName(d.Severity), source, code, firstLine(d.Message)))
		}
	}
	header := fmt.Sprintf("%s: %d diagnostic(s) in %d file(s)", route.server.Name, diagCount, len(route.files))
	return header + "\n" + strings.Join(lines, "\n"), len(route.files), diagCount, nil
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func relTo(root, path string) string {
	if rel, err := filepath.Rel(root, path); err == nil && rel != "." && !strings.HasPrefix(rel, "..") {
		return filepath.ToSlash(rel)
	}
	return filepath.ToSlash(path)
}

func truncateLines(s string, max int) string {
	lines := strings.Split(s, "\n")
	if len(lines) <= max {
		return s
	}
	return strings.Join(lines[:max], "\n")
}

// lspValidateReadPath applies the fs read-side path rules for the LSP tool.
func lspValidateReadPath(t *LspDiagnosticsTool, path string) (string, error) {
	return fstools.ValidatePathWithAllowPaths(path, t.workspace, t.restrict, t.allow)
}
