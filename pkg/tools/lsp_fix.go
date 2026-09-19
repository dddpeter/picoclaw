package tools

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/lsp"
	fstools "github.com/sipeed/picoclaw/pkg/tools/fs"
)

// LspFixTool applies server-supported source actions (source.fixAll,
// source.organizeImports, ...) to one file (fork feature, design §3.5).
type LspFixTool struct {
	workspace string
	restrict  bool
	allowR    []*regexp.Regexp
	allowW    []*regexp.Regexp
	servers   []lsp.ServerConfig
	pool      *lsp.Pool
	cfg       config.LspToolsConfig
	writer    *WriteFileTool
}

// NewLspFixTool builds the fix tool; writes go through the standard
// WriteFileTool path (validation + atomic write + Windows name rules).
func NewLspFixTool(
	workspace string,
	restrict bool,
	allowRead, allowWrite []*regexp.Regexp,
	cfg config.LspToolsConfig,
) *LspFixTool {
	return NewLspFixToolWithPool(workspace, restrict, allowRead, allowWrite, cfg, nil)
}

// NewLspFixToolWithPool is NewLspFixTool with an injectable pool for tests.
func NewLspFixToolWithPool(
	workspace string,
	restrict bool,
	allowRead, allowWrite []*regexp.Regexp,
	cfg config.LspToolsConfig,
	pool *lsp.Pool,
) *LspFixTool {
	if pool == nil {
		pool = sharedLspPool(cfg)
	}
	return &LspFixTool{
		workspace: workspace,
		restrict:  restrict,
		allowR:    allowRead,
		allowW:    allowWrite,
		servers:   resolveLspServers(cfg),
		pool:      pool,
		cfg:       cfg,
		writer:    NewWriteFileTool(workspace, restrict, allowWrite),
	}
}

func (t *LspFixTool) Name() string { return "lsp_fix" }

func (t *LspFixTool) Description() string {
	return "Apply a language-server source action (default source.fixAll; also source.organizeImports etc.) to a file. Without write=true returns the fixed text for preview; with write=true writes it back atomically. If several servers match the file, pass server explicitly."
}

func (t *LspFixTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "File to fix",
			},
			"kind": map[string]any{
				"type":        "string",
				"description": "Source action kind (default source.fixAll).",
			},
			"write": map[string]any{
				"type":        "boolean",
				"description": "Write the fixed text back (default false: preview only).",
			},
			"server": map[string]any{
				"type":        "string",
				"description": "Optional server name when several match the file.",
			},
		},
		"required": []string{"path"},
	}
}

func (t *LspFixTool) Execute(ctx context.Context, args map[string]any) *ToolResult {
	rawPath, _ := args["path"].(string)
	if strings.TrimSpace(rawPath) == "" {
		return ErrorResult("path is required")
	}
	kind, _ := args["kind"].(string)
	if strings.TrimSpace(kind) == "" {
		kind = "source.fixAll"
	}
	write, _ := args["write"].(bool)
	server, _ := args["server"].(string)

	abs, err := fstools.ValidatePathWithAllowPaths(rawPath, t.workspace, t.restrict, t.allowR)
	if err != nil {
		return ErrorResult(err.Error())
	}

	// Route: exactly one server must handle the file (explicit or unique).
	var candidates []lsp.ServerConfig
	for _, s := range t.servers {
		if server != "" && s.Name != server {
			continue
		}
		if s.Supports(abs) {
			candidates = append(candidates, s)
		}
	}
	if len(candidates) == 0 {
		return ErrorResult(fmt.Sprintf("no configured LSP server supports %s (routing is by file extension; servers: %s)",
			rawPath, strings.Join(serverNames(t.servers), ", ")))
	}
	if len(candidates) > 1 {
		return ErrorResult("multiple servers support " + rawPath + ": " +
			strings.Join(serverNames(candidates), ", ") + "; pass the server parameter")
	}
	srv := candidates[0]
	if !srv.CommandAvailable() {
		return ErrorResult(srv.Name + " command not found: " + srv.Command)
	}

	client, err := t.pool.Acquire(ctx, srv, t.workspace)
	if err != nil {
		return ErrorResult(err.Error())
	}
	defer t.pool.Touch(t.workspace, srv.Name)

	textBytes, err := os.ReadFile(abs)
	if err != nil {
		return ErrorResult(fmt.Sprintf("read %s: %v", rawPath, err))
	}
	text := string(textBytes)
	uri, err := client.TouchFile(ctx, abs, text, lsp.LanguageID(abs))
	if err != nil {
		return ErrorResult(err.Error())
	}
	diags, err := client.Diagnostics(ctx, uri)
	if err != nil {
		return ErrorResult(err.Error())
	}
	actions, err := client.CodeActions(ctx, uri, text, diags, kind)
	if err != nil {
		return ErrorResult(err.Error())
	}
	resolved, err := client.ResolveActions(ctx, actions)
	if err != nil {
		return ErrorResult(err.Error())
	}

	var edits []lsp.TextEdit
	var appliedTitles []string
	for _, a := range resolved {
		if a.Kind == kind || strings.HasPrefix(a.Kind, kind+".") {
			edits = append(edits, lsp.CollectEdits(a.Edit, uri)...)
			appliedTitles = append(appliedTitles, a.Title)
		}
	}
	newText, err := lsp.ApplyEdits(text, edits)
	if err != nil {
		return ErrorResult(fmt.Sprintf("%s: %v", srv.Name, err))
	}
	changed := newText != text
	rel := relTo(t.workspace, abs)

	if changed && write {
		// Reuse the standard write path: validation, atomic write,
		// Windows filename rules, transient-retry.
		if res := t.writer.Execute(ctx, map[string]any{
			"path": abs, "content": newText, "overwrite": true,
		}); res.IsError {
			return res
		}
	}

	switch {
	case !changed:
		return NewToolResult(fmt.Sprintf("%s LSP fix left %s unchanged (kind=%s, %d action(s) found).",
			srv.Name, rel, kind, len(resolved)))
	case write:
		return NewToolResult(fmt.Sprintf("%s LSP fix updated %s (kind=%s, actions: %s).",
			srv.Name, rel, kind, strings.Join(appliedTitles, "; ")))
	default:
		return NewToolResult(fmt.Sprintf("%s LSP fix computed changes for %s (kind=%s, actions: %s):\n\n%s",
			srv.Name, rel, kind, strings.Join(appliedTitles, "; "), newText))
	}
}
