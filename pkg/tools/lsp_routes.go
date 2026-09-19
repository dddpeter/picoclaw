package tools

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/lsp"
)

// builtinLspServers is the fork's curated catalog (design §3.2, review
// decision: 8 entries). Custom config merges on top of these by name;
// `disabled: true` removes an entry.
func builtinLspServers() []lsp.ServerConfig {
	return []lsp.ServerConfig{
		{
			Name:       "gopls",
			Command:    "gopls",
			Extensions: []string{".go"},
			// gopls can answer the first pull before its analysis publishes
			// the real diagnostics.
			PullDiagnosticsGraceMs: 5000,
		},
		{
			// Push-only: clean documents may never publish — treat
			// silence as clean after this window.
			PushDiagnosticsGraceMs: 5000,
			Name:                   "typescript-language-server",
			Command:                "typescript-language-server",
			Args:                   []string{"--stdio"},
			Extensions:             []string{".ts", ".tsx", ".js", ".jsx", ".mts", ".cts"},
		},
		{
			// Push-only: clean documents may never publish — treat
			// silence as clean after this window.
			PushDiagnosticsGraceMs: 5000,
			Name:                   "pyright",
			Command:                "pyright-langserver",
			Args:                   []string{"--stdio"},
			Extensions:             []string{".py", ".pyi"},
		},
		{
			Name:       "ruff",
			Command:    "ruff",
			Args:       []string{"server"},
			Extensions: []string{".py", ".pyi"},
		},
		{
			Name:                   "rust-analyzer",
			Command:                "rust-analyzer",
			Extensions:             []string{".rs"},
			PullDiagnosticsGraceMs: 5000,
		},
		{
			// Push-only: clean documents may never publish — treat
			// silence as clean after this window.
			PushDiagnosticsGraceMs: 5000,
			Name:                   "clangd",
			Command:                "clangd",
			Extensions:             []string{".c", ".h", ".cpp", ".hpp", ".cc", ".cxx", ".hh", ".hxx"},
		},
		{
			// Push-only: clean documents may never publish — treat
			// silence as clean after this window.
			PushDiagnosticsGraceMs: 8000,
			Name:                   "jdtls",
			Command:                "jdtls",
			Extensions:             []string{".java"},
			SkipDirectories:        []string{".gradle", "build"},
		},
		{
			// Push-only: clean documents may never publish — treat
			// silence as clean after this window.
			PushDiagnosticsGraceMs: 5000,
			Name:                   "vue-language-server",
			Command:                "vue-language-server",
			Args:                   []string{"--stdio"},
			Extensions:             []string{".vue"},
		},
	}
}

// commonLspSkipDirs is the shared directory-name skip set for file
// collection (pi-lsp's COMMON_SKIP_DIRECTORIES).
var commonLspSkipDirs = map[string]bool{
	".git": true, ".hg": true, ".mypy_cache": true, ".next": true,
	".nuxt": true, ".output": true, ".ruff_cache": true, ".svelte-kit": true,
	".tox": true, ".venv": true, "__pycache__": true, "coverage": true,
	"dist": true, "node_modules": true, "out": true, "target": true,
	"vendor": true, "venv": true,
}

// resolveLspServers merges the built-in catalog with user configuration
// (opencode semantics): same-name entries override fields, `disabled: true`
// removes the entry entirely, and brand-new names extend the catalog.
func resolveLspServers(cfg config.LspToolsConfig) []lsp.ServerConfig {
	byName := map[string]lsp.ServerConfig{}
	for _, s := range builtinLspServers() {
		byName[s.Name] = s
	}
	names := make([]string, 0, len(byName)+len(cfg.Servers))
	for name := range byName {
		names = append(names, name)
	}

	for name, user := range cfg.Servers {
		base, exists := byName[name]
		if !exists {
			base = lsp.ServerConfig{Name: name}
			names = append(names, name)
		}
		if user.EffectiveIsDisabled() {
			delete(byName, name)
			continue
		}
		if len(user.Command) > 0 {
			base.Command = user.Command[0]
			base.Args = append([]string{}, user.Command[1:]...)
		}
		if len(user.Extensions) > 0 {
			base.Extensions = normalizeExtensions(user.Extensions)
		}
		if user.Env != nil {
			base.Env = user.Env
		}
		if user.Initialization != nil {
			base.Initialization = user.Initialization
		}
		if len(user.SkipDirs) > 0 {
			base.SkipDirectories = user.SkipDirs
		}
		if user.PushDiagnosticsGraceMs > 0 {
			base.PushDiagnosticsGraceMs = user.PushDiagnosticsGraceMs
		}
		if user.PullDiagnosticsGraceMs > 0 {
			base.PullDiagnosticsGraceMs = user.PullDiagnosticsGraceMs
		}
		byName[name] = base
	}

	sort.Strings(names)
	servers := make([]lsp.ServerConfig, 0, len(byName))
	for _, name := range names {
		if s, ok := byName[name]; ok {
			servers = append(servers, s)
		}
	}
	return servers
}

func normalizeExtensions(exts []string) []string {
	out := make([]string, 0, len(exts))
	for _, e := range exts {
		e = strings.ToLower(strings.TrimSpace(e))
		if e == "" {
			continue
		}
		if !strings.HasPrefix(e, ".") {
			e = "." + e
		}
		out = append(out, e)
	}
	return out
}

// lspRoute pairs a resolved server with the files it should process.
type lspRoute struct {
	server lsp.ServerConfig
	files  []string
}

func serverSkipsDirs(s lsp.ServerConfig) map[string]bool {
	skips := make(map[string]bool, len(commonLspSkipDirs)+len(s.SkipDirectories))
	for k := range commonLspSkipDirs {
		skips[k] = true
	}
	for _, d := range s.SkipDirectories {
		skips[d] = true
	}
	return skips
}

// selectLspRoutes picks runnable servers for the requested paths and the
// files each should handle. Explicit server names must exist and be
// runnable; implicit selection silently skips servers whose commands are
// missing from PATH (design §2.1).
func selectLspRoutes(
	servers []lsp.ServerConfig,
	root string,
	requestedPaths []string,
	explicitServers []string,
	limit int,
) (routes []lspRoute, skipped []string, err error) {
	if len(explicitServers) > 0 {
		byName := map[string]lsp.ServerConfig{}
		for _, s := range servers {
			byName[s.Name] = s
		}
		filtered := make([]lsp.ServerConfig, 0, len(explicitServers))
		var missing []string
		for _, name := range explicitServers {
			s, ok := byName[name]
			if !ok {
				missing = append(missing, name)
				continue
			}
			filtered = append(filtered, s)
		}
		if len(missing) > 0 {
			return nil, nil, &LspServerError{Message: "unknown LSP server(s): " + strings.Join(missing, ", ") +
				". Configured: " + strings.Join(serverNames(servers), ", ")}
		}
		servers = filtered
	}

	inputs := requestedPaths
	if len(inputs) == 0 {
		inputs = []string{root}
	}

	// Cache file collection by (extensions, skips) policy so complementary
	// servers (pyright + ruff) walk the tree once.
	policyFiles := map[string][]string{}
	for _, s := range servers {
		if len(explicitServers) == 0 {
			if !s.CommandAvailable() {
				skipped = append(skipped, s.Name+" (command not found: "+s.Command+")")
				continue
			}
		} else if !s.CommandAvailable() {
			return nil, nil, &LspServerError{Message: s.Name + " command not found: " + s.Command}
		}
		key := strings.Join(s.Extensions, ",") + "|" + strings.Join(s.SkipDirectories, ",")
		files, ok := policyFiles[key]
		if !ok {
			files = collectLspFiles(s, root, inputs, limit)
			policyFiles[key] = files
		}
		if len(files) == 0 {
			continue
		}
		routes = append(routes, lspRoute{server: s, files: files})
	}
	return routes, skipped, nil
}

func serverNames(servers []lsp.ServerConfig) []string {
	names := make([]string, 0, len(servers))
	for _, s := range servers {
		names = append(names, s.Name)
	}
	return names
}

// collectLspFiles walks the requested inputs collecting files the server
// supports, capped at limit, honoring the skip directories.
func collectLspFiles(s lsp.ServerConfig, root string, inputs []string, limit int) []string {
	if limit < 1 {
		limit = 1
	}
	skips := serverSkipsDirs(s)
	var files []string
	seen := map[string]bool{}

	for _, input := range inputs {
		if len(files) >= limit {
			break
		}
		abs := input
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(root, abs)
		}
		info, err := os.Stat(abs)
		if err != nil {
			continue
		}
		if !info.IsDir() {
			if s.Supports(abs) && !seen[abs] {
				seen[abs] = true
				files = append(files, abs)
			}
			continue
		}
		filepath.WalkDir(abs, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				if path != abs && skips[d.Name()] {
					return filepath.SkipDir
				}
				return nil
			}
			if len(files) >= limit {
				return filepath.SkipAll
			}
			if s.Supports(path) && !seen[path] {
				seen[path] = true
				files = append(files, path)
			}
			return nil
		})
	}
	return files
}
