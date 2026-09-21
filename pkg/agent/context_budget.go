// PicoClaw - Ultra-lightweight personal AI agent
// License: MIT
//
// Copyright (c) 2026 PicoClaw contributors

package agent

import (
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/tokenizer"
)

// parseTurnBoundaries returns the starting index of each Turn in the history.
// A Turn is a complete "user input → LLM iterations → final response" cycle
// (as defined in #1316). Each Turn begins at a user message and extends
// through all subsequent assistant/tool messages until the next user message.
//
// Cutting at a Turn boundary guarantees that no tool-call sequence
// (assistant+ToolCalls → tool results) is split across the cut.
func parseTurnBoundaries(history []providers.Message) []int {
	var starts []int
	for i, msg := range history {
		if msg.Role == "user" {
			starts = append(starts, i)
		}
	}
	return starts
}

// isSafeBoundary reports whether index is a valid Turn boundary — i.e.,
// a position where the kept portion (history[index:]) begins at a user
// message, so no tool-call sequence is torn apart.
func isSafeBoundary(history []providers.Message, index int) bool {
	if index <= 0 || index >= len(history) {
		return true
	}
	return history[index].Role == "user"
}

// findSafeBoundary locates the nearest Turn boundary to targetIndex.
// It prefers the boundary at or before targetIndex (preserving more recent
// context). Falls back to the nearest boundary after targetIndex, and
// returns targetIndex unchanged only when no Turn boundary exists at all.
func findSafeBoundary(history []providers.Message, targetIndex int) int {
	if len(history) == 0 {
		return 0
	}
	if targetIndex <= 0 {
		return 0
	}
	if targetIndex >= len(history) {
		return len(history)
	}

	turns := parseTurnBoundaries(history)
	if len(turns) == 0 {
		return targetIndex
	}

	// Find the last Turn boundary at or before targetIndex.
	// Prefer backward: keeps more recent messages.
	backward := -1
	for _, t := range turns {
		if t <= targetIndex {
			backward = t
		}
	}
	if backward > 0 {
		return backward
	}

	// No valid Turn boundary before target (or only at index 0 which
	// would keep everything). Use the first Turn after targetIndex.
	for _, t := range turns {
		if t > targetIndex {
			return t
		}
	}

	// No Turn boundary after targetIndex either. The only boundary is at
	// index 0, meaning the entire history is a single Turn. Return 0 to
	// signal that safe compression is not possible — callers check for
	// mid <= 0 and skip compression in that case.
	return 0
}

// EstimateMessageTokens estimates the token count for a single message.
// Delegates to the shared tokenizer package for consistency across agent and seahorse.
func EstimateMessageTokens(msg providers.Message) int {
	return tokenizer.EstimateMessageTokens(msg)
}

// EstimateToolDefsTokens estimates the total token cost of tool definitions
// as they appear in the LLM request. Delegates to the shared tokenizer package.
func EstimateToolDefsTokens(defs []providers.ToolDefinition) int {
	return tokenizer.EstimateToolDefsTokens(defs)
}

// isOverContextBudget checks whether the assembled messages plus tool definitions
// and output reserve would exceed the model's context window. This enables
// proactive compression before calling the LLM, rather than reacting to 400 errors.
func isOverContextBudget(
	contextWindow int,
	messages []providers.Message,
	toolDefs []providers.ToolDefinition,
	maxTokens int,
) bool {
	return isOverContextBudgetWithToolTokens(
		contextWindow,
		messages,
		EstimateToolDefsTokens(toolDefs),
		maxTokens,
	)
}

// isOverContextBudgetWithToolTokens is isOverContextBudget with the tool
// definition cost supplied by the caller. Callers that test several candidate
// histories against one fixed tool set use this to avoid re-marshaling every
// tool schema once per candidate.
func isOverContextBudgetWithToolTokens(
	contextWindow int,
	messages []providers.Message,
	toolTokens int,
	maxTokens int,
) bool {
	msgTokens := 0
	for _, m := range messages {
		msgTokens += EstimateMessageTokens(m)
	}

	return msgTokens+toolTokens+maxTokens > contextWindow
}

// trimHistoryToFitContextWindow rebuilds the prompt from progressively newer
// history slices until it fits within the context window. Oldest complete turns
// are dropped first so tool-call sequences remain intact.
//
// The candidate cut points are enumerated once (cheap — no prompt rebuild) and
// then probed by binary search, because the prompt rebuild dominates the cost:
// the previous scan rebuilt the whole prompt once per dropped turn. The result
// is the first candidate that fits, exactly the cut the linear scan chose; the
// search relies on the token count being monotone in the cut index, which holds
// because dropping a whole turn only removes messages.
func trimHistoryToFitContextWindow(
	history []providers.Message,
	build func([]providers.Message) []providers.Message,
	contextWindow int,
	toolDefs []providers.ToolDefinition,
	maxTokens int,
) ([]providers.Message, []providers.Message, bool) {
	messages := build(history)
	toolTokens := EstimateToolDefsTokens(toolDefs)
	if !isOverContextBudgetWithToolTokens(contextWindow, messages, toolTokens, maxTokens) {
		return history, messages, true
	}

	candidates := trimCandidateStarts(history)

	best := -1
	var bestMessages []providers.Message

	for lo, hi := 0, len(candidates)-1; lo <= hi; {
		mid := lo + (hi-lo)/2
		candidateMessages := build(sliceFromStart(history, candidates[mid]))
		if isOverContextBudgetWithToolTokens(contextWindow, candidateMessages, toolTokens, maxTokens) {
			lo = mid + 1
			continue
		}
		best = mid
		bestMessages = candidateMessages
		hi = mid - 1
	}

	if best >= 0 {
		return sliceFromStart(history, candidates[best]), bestMessages, true
	}

	// Nothing fits, not even an empty history: report the empty prompt so the
	// caller can log what the model would have seen.
	return nil, build(nil), false
}

// trimCandidateStarts enumerates the successive "drop the oldest remaining
// turn" cut points in ascending order, always ending with a cut that drops
// everything. This matches the sequence the previous incremental loop walked
// with one deliberate difference: when a turn boundary cannot be found
// mid-history (nextHistoryTrimStart <= 0), the old loop dropped everything
// immediately, while this keeps the cuts enumerated so far and appends the
// "drop everything" fallback — strictly better, because an earlier cut may
// already fit.
func trimCandidateStarts(history []providers.Message) []int {
	starts := make([]int, 0, 8)
	remaining := history
	offset := 0
	for len(remaining) > 0 {
		dropUntil := nextHistoryTrimStart(remaining)
		if dropUntil <= 0 || dropUntil >= len(remaining) {
			return append(starts, len(history))
		}
		offset += dropUntil
		starts = append(starts, offset)
		remaining = remaining[dropUntil:]
	}
	return append(starts, len(history))
}

// sliceFromStart returns the copy of history[start:] that the trim hands back
// to callers, or nil once the cut drops everything.
func sliceFromStart(history []providers.Message, start int) []providers.Message {
	if start >= len(history) {
		return nil
	}
	return append([]providers.Message(nil), history[start:]...)
}

func nextHistoryTrimStart(history []providers.Message) int {
	if len(history) == 0 {
		return 0
	}

	turns := parseTurnBoundaries(history)
	if len(turns) >= 2 {
		return turns[1]
	}
	if len(turns) == 1 {
		if turns[0] > 0 {
			return turns[0]
		}
		return len(history)
	}

	return len(history)
}
