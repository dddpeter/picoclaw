package fstools

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"
)

// grepMaxFiles caps how many files are scanned, grepMaxMatches caps the
// emitted hits, and grepMaxFileSize skips oversized files before reading:
// content search must stay cheap even over a whole workspace (2026-10-31
// review C3/m-3: content search previously required a platform shell via
// exec, and a full ReadFile of a huge log could blow up memory).
const (
	grepMaxFiles        = 4000
	grepMaxMatches      = 200
	grepBinaryProbeSize = 8 * 1024
)

// grepMaxFileSize skips oversized files before reading (var for tests).
var grepMaxFileSize = int64(8 << 20) // 8 MiB

// GrepTool searches file contents with a regular expression, mirroring the
// classic `grep` contract on top of the shared fileSystem abstraction
// (restrict mode and allow-read patterns apply unchanged).
type GrepTool struct {
	fs fileSystem
}

func NewGrepTool(workspace string, restrict bool, allowPaths ...[]*regexp.Regexp) *GrepTool {
	var patterns []*regexp.Regexp
	if len(allowPaths) > 0 {
		patterns = allowPaths[0]
	}
	return &GrepTool{fs: buildFs(workspace, restrict, patterns)}
}

func (t *GrepTool) Name() string {
	return "grep"
}

func (t *GrepTool) Description() string {
	return "Search file contents with a regular expression and report path:line matches. Supports an include glob to filter filenames (e.g. '*.go')."
}

func (t *GrepTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"pattern": map[string]any{
				"type":        "string",
				"description": "Regular expression (Go regexp syntax) to search for",
			},
			"path": map[string]any{
				"type":        "string",
				"description": "File or directory to search (default: workspace root)",
			},
			"include": map[string]any{
				"type":        "string",
				"description": "Optional filename glob filter, e.g. '*.go' or '*_test.go'",
			},
			"case_insensitive": map[string]any{
				"type":        "boolean",
				"description": "Ignore case (default: false)",
			},
		},
		"required": []string{"pattern"},
	}
}

func looksBinary(chunk []byte) bool {
	return bytes.IndexByte(chunk, 0) >= 0
}

// grepIncludeMatcher translates an include glob into a filename matcher.
func grepIncludeMatcher(include string) (func(string) bool, error) {
	if strings.TrimSpace(include) == "" {
		return func(string) bool { return true }, nil
	}
	re, err := globToRegexp(include)
	if err != nil {
		return nil, err
	}
	return func(name string) bool { return re.MatchString(name) }, nil
}

func (t *GrepTool) searchFile(ctx context.Context, path string, re *regexp.Regexp, out *strings.Builder, count *int) error {
	data, err := t.fs.ReadFile(path)
	if err != nil {
		return nil // unreadable files are skipped, not fatal
	}
	return searchBytes(ctx, path, re, data, out, count)
}

func searchBytes(ctx context.Context, path string, re *regexp.Regexp, data []byte, out *strings.Builder, count *int) error {
	if looksBinary(data[:min(len(data), grepBinaryProbeSize)]) {
		return nil
	}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := scanner.Text()
		if !re.MatchString(line) {
			continue
		}
		if *count >= grepMaxMatches {
			return fmt.Errorf("match cap")
		}
		*count++
		display := line
		if runes := utf8.RuneCountInString(display); runes > 300 {
			display = string([]rune(display)[:300]) + "…"
		}
		fmt.Fprintf(out, "%s:%d: %s\n", path, lineNo, display)
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
	}
	return nil
}

func (t *GrepTool) Execute(ctx context.Context, args map[string]any) *ToolResult {
	pattern, _ := args["pattern"].(string)
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return ErrorResult("pattern is required")
	}
	if caseInsensitive, _ := args["case_insensitive"].(bool); caseInsensitive {
		pattern = "(?i)" + pattern
	}
	searchPath, _ := args["path"].(string)
	if strings.TrimSpace(searchPath) == "" {
		searchPath = "."
	}
	include, _ := args["include"].(string)
	includeMatch, err := grepIncludeMatcher(include)
	if err != nil {
		return ErrorResult(fmt.Sprintf("invalid include glob: %v", err))
	}

	re, err := regexp.Compile(pattern)
	if err != nil {
		return ErrorResult(fmt.Sprintf("invalid pattern: %v", err))
	}

	out := &strings.Builder{}
	count := 0
	filesScanned := 0
	skipped := 0
	truncated := false
	canceled := false

	var walk func(dir string, depth int)
	walk = func(dir string, depth int) {
		if canceled || truncated || depth > globWalkDepth || filesScanned >= grepMaxFiles {
			return
		}
		select {
		case <-ctx.Done():
			canceled = true
			return
		default:
		}
		entries, readErr := t.fs.ReadDir(dir)
		if readErr != nil {
			return
		}
		for _, entry := range entries {
			name := entry.Name()
			rel := name
			if dir != "." && dir != "" {
				rel = dir + "/" + name
			}
			if entry.IsDir() {
				if name == ".git" || name == "node_modules" {
					continue
				}
				walk(rel, depth+1)
				continue
			}
			if !includeMatch(filepath.Base(name)) {
				continue
			}
			if filesScanned >= grepMaxFiles || count >= grepMaxMatches {
				truncated = count >= grepMaxMatches
				return
			}
			// m-3: skip oversized files before reading them into memory.
			if info, infoErr := entry.Info(); infoErr == nil && info.Size() > grepMaxFileSize {
				skipped++
				continue
			}
			filesScanned++
			if searchErr := t.searchFile(ctx, rel, re, out, &count); searchErr != nil {
				truncated = true
				return
			}
		}
	}

	// A single-file search path skips the walk. The probe read doubles as
	// the search input — no second ReadFile (nit: previously read twice).
	if data, readErr := t.fs.ReadFile(searchPath); readErr == nil && data != nil {
		buf := &strings.Builder{}
		n := 0
		if searchErr := searchBytes(ctx, searchPath, re, data, buf, &n); searchErr != nil {
			truncated = true
		}
		out.WriteString(buf.String())
		count = n
	} else {
		walk(strings.TrimPrefix(strings.ReplaceAll(searchPath, "\\", "/"), "./"), 0)
	}

	if canceled {
		return ErrorResult("search interrupted: context canceled")
	}

	if count == 0 && skipped == 0 {
		return NewToolResult(fmt.Sprintf("No matches for %q under %s.", pattern, searchPath))
	}
	result := fmt.Sprintf("%d match(es):\n%s", count, out.String())
	if skipped > 0 {
		result += fmt.Sprintf("\n[%d file(s) skipped over %d MiB size cap]", skipped, grepMaxFileSize>>20)
	}
	if truncated {
		result += fmt.Sprintf("\n[results truncated at %d matches — narrow the search]", grepMaxMatches)
	}
	return NewToolResult(result)
}
