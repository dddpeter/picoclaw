// PicoClaw - Ultra-lightweight personal AI agent

package tools

import (
	"fmt"
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
