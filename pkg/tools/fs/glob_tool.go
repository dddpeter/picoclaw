package fstools

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// globWalkDepth caps recursion, globMaxMatches caps result count: the tool
// must stay cheap even when pointed at a workspace root with node_modules.
const (
	globWalkDepth  = 24
	globMaxMatches = 200
)

// GlobTool finds files by glob pattern (supports ** for recursive matching).
// It walks through the same fileSystem abstraction as the other file tools,
// so restrict mode and allow-read patterns apply unchanged (2026-10-31
// review C3: 找文件此前只能依赖 exec 里的平台 shell).
type GlobTool struct {
	fs      fileSystem
	pattern string
}

func NewGlobTool(workspace string, restrict bool, allowPaths ...[]*regexp.Regexp) *GlobTool {
	var patterns []*regexp.Regexp
	if len(allowPaths) > 0 {
		patterns = allowPaths[0]
	}
	return &GlobTool{fs: buildFs(workspace, restrict, patterns)}
}

func (t *GlobTool) Name() string {
	return "glob"
}

func (t *GlobTool) Description() string {
	return "Find files matching a glob pattern (supports ** for recursion), e.g. '**/*.go' or 'src/**/*.test.tsx'. More efficient than shelling out to find."
}

func (t *GlobTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"pattern": map[string]any{
				"type":        "string",
				"description": "Glob pattern relative to the search path; ** matches any number of directories",
			},
			"path": map[string]any{
				"type":        "string",
				"description": "Directory to search in (default: workspace root)",
			},
		},
		"required": []string{"pattern"},
	}
}

// globToRegexp translates a glob into a regexp. ** matches across separators,
// * matches within one segment, ? matches one non-separator char.
func globToRegexp(pattern string) (*regexp.Regexp, error) {
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(pattern); i++ {
		switch c := pattern[i]; c {
		case '*':
			if i+1 < len(pattern) && pattern[i+1] == '*' {
				// Consume any following separator so "**/" matches zero dirs too.
				if i+2 < len(pattern) && (pattern[i+2] == '/' || pattern[i+2] == '\\') {
					i += 2
					b.WriteString("(?:.*/)?")
				} else {
					b.WriteString(".*")
				}
			} else {
				b.WriteString("[^/\\\\]*")
			}
		case '?':
			b.WriteString("[^/\\\\]")
		case '.', '+', '(', ')', '|', '^', '$', '[', ']', '{', '}', '\\':
			b.WriteByte('\\')
			b.WriteByte(c)
		default:
			b.WriteByte(c)
		}
	}
	b.WriteString("$")
	return regexp.Compile(b.String())
}

// normalizeSearchRoot canonicalizes the caller-supplied search directory into
// the workspace-relative walk root. Absolute paths (leading "/" or a drive
// letter) are preserved — the previous two-sided strings.Trim stripped the
// leading slash and silently re-rooted "/tmp" into <workspace>/tmp (m-2).
func normalizeSearchRoot(searchDir string) string {
	root := strings.ReplaceAll(searchDir, "\\", "/")
	root = strings.TrimPrefix(root, "./")
	root = strings.TrimRight(root, "/")
	if root == "." {
		return ""
	}
	return root
}

func (t *GlobTool) Execute(ctx context.Context, args map[string]any) *ToolResult {
	pattern, _ := args["pattern"].(string)
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return ErrorResult("pattern is required")
	}
	searchDir, _ := args["path"].(string)
	if strings.TrimSpace(searchDir) == "" {
		searchDir = "."
	}

	re, err := globToRegexp(pattern)
	if err != nil {
		return ErrorResult(fmt.Sprintf("invalid glob pattern: %v", err))
	}

	matches := make([]string, 0, 16)
	truncated := false
	depthCapped := false
	var walkErr error
	// relBase strips the search dir prefix so patterns anchor at the search
	// root; display paths stay workspace-relative like the other file tools.
	searchRoot := normalizeSearchRoot(searchDir)
	walkRoot := searchRoot
	if walkRoot == "" {
		walkRoot = "."
	}
	var walk func(dir string, depth int)
	walk = func(dir string, depth int) {
		if walkErr != nil || truncated || len(matches) >= globMaxMatches {
			return
		}
		if depth > globWalkDepth {
			depthCapped = true
			return
		}
		select {
		case <-ctx.Done():
			walkErr = ctx.Err()
			return
		default:
		}
		entries, err := t.fs.ReadDir(dir)
		if err != nil {
			return // unreadable dirs are skipped, not fatal
		}
		for _, entry := range entries {
			name := entry.Name()
			display := name
			if dir != "." && dir != "" {
				display = dir + "/" + name
			}
			relFromSearch := strings.TrimPrefix(strings.TrimPrefix(display, searchRoot), "/")
			if entry.IsDir() {
				if name == ".git" || name == "node_modules" {
					continue
				}
				walk(display, depth+1)
				continue
			}
			if re.MatchString(filepath.ToSlash(relFromSearch)) {
				if len(matches) >= globMaxMatches {
					truncated = true
					return
				}
				matches = append(matches, display)
			}
		}
	}
	walk(walkRoot, 0)
	if walkErr != nil {
		return ErrorResult(fmt.Sprintf("search interrupted: %v", walkErr))
	}

	if len(matches) == 0 {
		return NewToolResult(fmt.Sprintf("No files match %q under %s.", pattern, searchDir))
	}
	sort.Strings(matches)
	out := strings.Join(matches, "\n")
	if truncated {
		out += fmt.Sprintf("\n[%d+ matches truncated — refine the pattern]", globMaxMatches)
	}
	if depthCapped {
		out += fmt.Sprintf("\n[directory tree deeper than %d levels was not searched]", globWalkDepth)
	}
	return NewToolResult(fmt.Sprintf("%d match(es):\n%s", len(matches), out))
}
