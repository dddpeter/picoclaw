package seahorse

import "strings"

// The readable-content markers emitted by partsToReadableContent
// ("[tool_use: name, args: …]", "[tool_result for id: …]", "[media: …]") are
// storage plumbing, but they leak into two LLM-facing surfaces: summarizer
// input (formatMessagesForSummary / truncateSummary read the content column)
// and stored summaries replayed by FormatSummaryXML. Models that see enough
// of the bracket format start parroting it as answer text instead of using
// the tool-call channel (observed 2026-10-05, MiniMax-M3). sanitizeToolMarkers
// rewrites marker lines into plain prose at those boundaries so the format
// never reaches a context the model could imprint on. It is line-oriented
// like the markers themselves; fence handling is unnecessary because these
// lines originate from partsToReadableContent output, never from user intent.
func sanitizeToolMarkers(text string) string {
	if !strings.Contains(text, "[tool_use: ") &&
		!strings.Contains(text, "[tool_result") &&
		!strings.Contains(text, "[media: ") {
		return text
	}
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "[tool_use: ") && strings.HasSuffix(trimmed, "]"):
			lines[i] = "tool call " + strings.TrimSuffix(strings.TrimPrefix(trimmed, "[tool_use: "), "]")
		case strings.HasPrefix(trimmed, "[tool_result") && strings.HasSuffix(trimmed, "]"):
			// Drop the "for <call_id>" segment — call IDs are plumbing noise
			// for a summary; the payload after ": " is what matters.
			inner := trimmed
			if idx := strings.Index(inner, ": "); idx >= 0 {
				inner = inner[idx+len(": "):]
			}
			lines[i] = "tool result: " + strings.TrimSuffix(inner, "]")
		case strings.HasPrefix(trimmed, "[media: ") && strings.HasSuffix(trimmed, "]"):
			lines[i] = "media attachment " + strings.TrimSuffix(strings.TrimPrefix(trimmed, "[media: "), "]")
		}
	}
	return strings.Join(lines, "\n")
}
