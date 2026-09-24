package openai_compat

import (
	"regexp"
	"strings"
)

// minimaxBoundaryToken is the encoded transport boundary MiniMax models emit
// around their native XML tool-call syntax. When an upstream tool-call parser
// fails, the raw markup — with this glitch-decoded token interleaved before
// each close tag — leaks into delta.content verbatim (e.g.
// `PYEOF]<]minimax[>[</command>]<]minimax[>[</invoke>`). Upstream trackers:
// MiniMax-AI/MiniMax-M3#31, vllm-project/vllm#51073, openclaw PR #126307.
const minimaxBoundaryToken = "]<]minimax[>["

// minimaxTailTags matches a trailing run of bare MiniMax tool-call XML close
// tags (whitespace between them allowed). Such a run at the end of the text
// is the evidence that a leaked pseudo tool-call sits there; without it the
// text is left structurally untouched.
var minimaxTailTags = regexp.MustCompile(`(?:</(?:tool_call|minimax:tool_call|invoke|command|parameter)>\s*)+$`)

// minimaxLeakStarts lists markers a leaked pseudo tool-call block may start
// with, ordered by preference: the outer block tags first (a complete leak
// begins there), then the inner markers models fall back to when the outer
// wrapper is absent — picoclaw's own seahorse history format the model
// parrots (`[tool_use: name, args: ...]`) among them.
var minimaxLeakStarts = [][]string{
	{"<minimax:tool_call", "<tool_call"},
	{"<invoke", "[tool_use: "},
}

// sanitizeMinimaxToolLeak strips MiniMax tool-call leakage from assistant
// text. Detection is evidence-based rather than model-name-gated: nothing
// happens unless the encoded boundary token is present, so other providers
// can never be affected. When it is present:
//
//  1. every boundary token is removed;
//  2. if the text then ends with a run of bare tool-call XML close tags, the
//     whole trailing pseudo tool-call block is cut from its right-most start
//     marker — the block body is model-printed tool syntax, not an answer,
//     and may carry secrets. Without a start marker only the tag run is
//     trimmed.
//
// Occurrences inside code fences are not protected: a reply quoting the leak
// verbatim is far rarer than the leak itself.
func sanitizeMinimaxToolLeak(s string) string {
	if !strings.Contains(s, minimaxBoundaryToken) {
		return s
	}
	cleaned := strings.ReplaceAll(s, minimaxBoundaryToken, "")
	tail := minimaxTailTags.FindString(cleaned)
	if tail == "" {
		return cleaned
	}
	cut := cleaned[:len(cleaned)-len(tail)]
	// Cut the whole block from its outermost start marker. Outer tags win
	// over inner ones: a complete leak nests <tool_call><invoke>…, and cutting
	// at the inner <invoke> would leave the outer tag dangling.
	for _, group := range minimaxLeakStarts {
		start := -1
		for _, marker := range group {
			if idx := strings.LastIndex(cut, marker); idx > start {
				start = idx
			}
		}
		if start >= 0 {
			cut = cut[:start]
			break
		}
	}
	return strings.TrimRight(cut, " \t\r\n")
}
