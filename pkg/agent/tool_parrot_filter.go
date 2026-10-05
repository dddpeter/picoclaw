// PicoClaw - Ultra-lightweight personal AI agent
// License: MIT
//
// Copyright (c) 2026 PicoClaw contributors

package agent

import "strings"

// Seahorse stores tool calls in its history/summaries via
// seahorse.partsToReadableContent as "[tool_use: name, args: ...]" lines
// (plus "[tool_result ...]" / "[media: ...]"). Models that see enough of
// this history start PARROTING the format in their visible answer content —
// replaying past (or planned) tool calls as plain text instead of keeping
// them in the tool-call channel. On streaming cards the parroted lines then
// render as raw payload dumps above the real answer (observed 2026-10-05,
// deepseek via an aggregator gateway).
//
// stripToolCallParrot drops those lines from user-facing display text.
// Lines inside code fences are protected: a reply legitimately quoting the
// history format puts it in a fence, while the parrot emits bare lines.
// The filter is line-oriented: the observed parrot embeds JSON-escaped \n
// sequences inside args, so each block is one line. A model emitting raw
// newlines inside parroted args would leave non-marker fragment lines
// behind — accepted noise for now. Only display paths use this — session
// history and LLM context keep the model's original content verbatim.
func stripToolCallParrot(text string) string {
	if !strings.Contains(text, "[tool_use: ") &&
		!strings.Contains(text, "[tool_result") &&
		!strings.Contains(text, "[media: ") {
		return text
	}
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	inFence := false
	dropped := 0
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			inFence = !inFence
			out = append(out, line)
			continue
		}
		if !inFence && isToolParrotLine(trimmed) {
			dropped++
			continue
		}
		out = append(out, line)
	}
	if dropped == 0 {
		return text
	}
	cleaned := strings.Join(out, "\n")
	// Collapse the blank runs dropped blocks leave behind (3+ newlines → 2,
	// i.e. at most one empty line), then trim the edges.
	for strings.Contains(cleaned, "\n\n\n") {
		cleaned = strings.ReplaceAll(cleaned, "\n\n\n", "\n\n")
	}
	return strings.TrimSpace(cleaned)
}

// isToolParrotLine reports whether a trimmed line is a seahorse
// readable-content marker line rather than prose.
func isToolParrotLine(line string) bool {
	return strings.HasPrefix(line, "[tool_use: ") ||
		strings.HasPrefix(line, "[tool_result") ||
		strings.HasPrefix(line, "[media: ")
}
