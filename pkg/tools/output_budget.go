// PicoClaw - Ultra-lightweight personal AI agent

package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/sipeed/picoclaw/pkg/logger"
)

// DefaultToolOutputBytes is the last-resort per-result cap on tool output
// injected into the model context. It deliberately sits above every
// built-in tool's own budget (shell: 2000 lines / 50KB, read_file: 64KB), so
// only unbounded producers — MCP servers, third-party tools — ever hit it,
// and built-in results never carry a double truncation notice.
const DefaultToolOutputBytes = 128 << 10

// effectiveOutputBudget resolves the configured value: 0 = default,
// negative = unlimited, positive = the value itself.
func effectiveOutputBudget(configured int) int {
	if configured == 0 {
		return DefaultToolOutputBytes
	}
	return configured
}

// ApplyOutputBudget enforces the per-result byte cap on tool output destined
// for the LLM. It keeps the tail — the end of command output is where errors
// and conclusions usually live, matching TruncateTail's semantics — and
// prefixes a notice so the model knows content was cut. The cut is advanced
// to a UTF-8 boundary so no multi-byte rune is split. maxBytes <= 0 disables
// the cap entirely.
func ApplyOutputBudget(toolName, content string, maxBytes int) string {
	if maxBytes <= 0 || len(content) <= maxBytes {
		return content
	}

	cut := len(content) - maxBytes
	for cut < len(content) && !utf8.RuneStart(content[cut]) {
		cut++ // never split a multi-byte rune
	}
	kept := content[cut:]

	logger.WarnCF("tool", "Tool output exceeded context budget; truncated",
		map[string]any{
			"tool":           toolName,
			"original_bytes": len(content),
			"kept_bytes":     len(kept),
			"budget_bytes":   maxBytes,
		})

	return fmt.Sprintf("[output truncated: kept the last %d of %d bytes]\n%s",
		len(kept), len(content), kept)
}

// ApplyOutputBudgetWithOffload behaves like ApplyOutputBudget and, when the
// content was actually truncated and workspaceDir is non-empty, additionally
// persists the untruncated original under <workspaceDir>/tmp and appends a
// "Full output:" reference so the model can read the original back. An
// offload failure falls back to plain truncation (never-worse: a failed
// offload must not fail the tool result).
func ApplyOutputBudgetWithOffload(toolName, content string, maxBytes int, workspaceDir string) string {
	truncated := ApplyOutputBudget(toolName, content, maxBytes)
	if truncated == content || workspaceDir == "" {
		return truncated
	}
	if path := OffloadTruncatedOutput(workspaceDir, toolName, content); path != "" {
		truncated += "\nFull output: " + path
	}
	return truncated
}

// OffloadTruncatedOutput writes the untruncated tool output to
// <baseDir>/tmp/tool-output-<tool>-*.log and returns the path, or "" on
// failure. This generalizes the exec tool's long-standing persistFullOutput
// to every tool that hits the output budget (MCP servers, third-party tools).
func OffloadTruncatedOutput(baseDir, toolName, output string) string {
	safe := sanitizeFileToken(toolName)
	if safe == "" {
		safe = "tool"
	}
	return PersistOutputFile(baseDir, fmt.Sprintf("tool-output-%s-*.log", safe), output)
}

// PersistOutputFile is the shared "save oversized output so the model can
// read it back" helper. baseDir is the workspace root (empty falls back to
// os.TempDir); the file lands in <baseDir>/tmp so read_file can reach it even
// under workspace sandboxing. Returns the path, or "" when persistence
// failed — callers treat "" as "no reference, keep going".
func PersistOutputFile(baseDir, filePattern, output string) string {
	base := strings.TrimSpace(baseDir)
	if base == "" {
		base = os.TempDir()
	}
	dir := filepath.Join(base, "tmp")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		logger.WarnCF("tool", "Failed to create dir for truncated tool output",
			map[string]any{"dir": dir, "error": err.Error()})
		return ""
	}

	tmpFile, err := os.CreateTemp(dir, filePattern)
	if err != nil {
		logger.WarnCF("tool", "Failed to create file for truncated tool output",
			map[string]any{"dir": dir, "error": err.Error()})
		return ""
	}
	path := tmpFile.Name()
	if _, err := tmpFile.WriteString(output); err != nil {
		_ = tmpFile.Close()
		_ = os.Remove(path)
		logger.WarnCF("tool", "Failed to write truncated tool output",
			map[string]any{"path": path, "error": err.Error()})
		return ""
	}
	if err := tmpFile.Close(); err != nil {
		_ = os.Remove(path)
		return ""
	}

	logger.InfoCF("tool", "Preserved full tool output after truncation",
		map[string]any{"path": path, "bytes": len(output)})
	return path
}

// sanitizeFileToken reduces a tool name to a filename-safe token.
func sanitizeFileToken(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return strings.Trim(b.String(), "_")
}

// SweepStaleOffloadFiles removes offload files (tool-output-* and exec's
// legacy shell-output-*) older than maxAge from <workspaceDir>/tmp. Offload
// files are never referenced twice — the model reads them back or they rot —
// so a startup sweep bounds their growth (review M3: the generalization to
// all tools would otherwise multiply the volume exec alone produced).
// Best effort: unreadable dirs are skipped silently.
func SweepStaleOffloadFiles(workspaceDir string, maxAge time.Duration) int {
	if strings.TrimSpace(workspaceDir) == "" || maxAge <= 0 {
		return 0
	}
	dir := filepath.Join(workspaceDir, "tmp")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	cutoff := time.Now().Add(-maxAge)
	removed := 0
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasPrefix(name, "tool-output-") && !strings.HasPrefix(name, "shell-output-") {
			continue
		}
		info, err := entry.Info()
		if err != nil || !info.ModTime().Before(cutoff) {
			continue
		}
		if os.Remove(filepath.Join(dir, name)) == nil {
			removed++
		}
	}
	if removed > 0 {
		logger.InfoCF("tool", "Swept stale offload files", map[string]any{
			"dir": dir, "removed": removed, "max_age": maxAge.String(),
		})
	}
	return removed
}
