package lsp

import (
	"fmt"
	"sort"
)

// TextEdit is an LSP text edit (range in UTF-16 positions, replacement
// text).
type TextEdit struct {
	Range   Range  `json:"range"`
	NewText string `json:"newText"`
}

// WorkspaceEdit is the subset of workspace edits we consume: either
// documentChanges entries or a changes map.
type WorkspaceEdit struct {
	DocumentChanges []struct {
		TextDocument struct {
			URI string `json:"uri"`
		} `json:"textDocument"`
		Edits []TextEdit `json:"edits"`
	} `json:"documentChanges"`
	Changes map[string][]TextEdit `json:"changes"`
}

// CodeAction is an LSP code action with an optional edit payload.
type CodeAction struct {
	Title string        `json:"title"`
	Kind  string        `json:"kind"`
	Edit  WorkspaceEdit `json:"edit"`
	Diag  []Diagnostic  `json:"diagnostics"`
}

// CollectEdits returns the text edits in a workspace edit that target the
// given document URI (both documentChanges and legacy changes forms).
func CollectEdits(edit WorkspaceEdit, uri string) []TextEdit {
	var out []TextEdit
	for _, change := range edit.DocumentChanges {
		if change.TextDocument.URI == uri || DocumentKey(change.TextDocument.URI) == DocumentKey(uri) {
			out = append(out, change.Edits...)
		}
	}
	if len(out) == 0 {
		out = append(out, edit.Changes[uri]...)
	}
	if len(out) == 0 {
		// Servers may key the legacy changes map with an equivalent but
		// differently-encoded URI; fall back to normalized matching.
		want := DocumentKey(uri)
		for k, v := range edit.Changes {
			if k != uri && DocumentKey(k) == want {
				out = append(out, v...)
			}
		}
	}
	return out
}

// positionedEdit is a byte-offset-resolved edit.
type positionedEdit struct {
	start, end int
	index      int // original position, tiebreak for deterministic order
	edit       TextEdit
}

// ApplyEdits applies text edits to the original text. Edits are applied
// from the end of the document backwards so earlier offsets stay valid.
// Overlapping replacements are rejected (pure inserts never conflict with
// each other; a pure insert strictly inside a replacement does — pi-lsp
// semantics, design §2.4).
func ApplyEdits(text string, edits []TextEdit) (string, error) {
	if len(edits) == 0 {
		return text, nil
	}
	positioned := make([]positionedEdit, 0, len(edits))
	for i, e := range edits {
		start := PositionToOffset(text, e.Range.Start.Line, e.Range.Start.Character)
		end := PositionToOffset(text, e.Range.End.Line, e.Range.End.Character)
		if end < start {
			start, end = end, start
		}
		positioned = append(positioned, positionedEdit{start: start, end: end, index: i, edit: e})
	}

	for i := 0; i < len(positioned); i++ {
		for j := i + 1; j < len(positioned); j++ {
			if editsConflict(positioned[i], positioned[j]) {
				return "", fmt.Errorf(
					"overlapping code-action edits (%d-%d vs %d-%d); use a narrower action kind",
					positioned[i].start, positioned[i].end, positioned[j].start, positioned[j].end)
			}
		}
	}

	// Descending by start so earlier offsets stay valid; index tiebreak
	// keeps same-position edits deterministic (sort.Slice is not stable).
	sort.Slice(positioned, func(a, b int) bool {
		if positioned[a].start != positioned[b].start {
			return positioned[a].start > positioned[b].start
		}
		if positioned[a].end != positioned[b].end {
			return positioned[a].end > positioned[b].end
		}
		return positioned[a].index < positioned[b].index
	})

	out := text
	for _, p := range positioned {
		out = out[:p.start] + p.edit.NewText + out[p.end:]
	}
	return out, nil
}

func editsConflict(a, b positionedEdit) bool {
	if a.start == a.end && b.start == b.end {
		return false // two pure inserts never conflict
	}
	if a.start == a.end {
		return b.start < a.start && a.start < b.end
	}
	if b.start == b.end {
		return a.start < b.start && b.start < a.end
	}
	return max(a.start, b.start) < min(a.end, b.end)
}
