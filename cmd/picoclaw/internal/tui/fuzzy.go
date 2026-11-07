package tui

import (
	"strings"
	"unicode"
)

// fuzzyMatch is the pi fuzzy.ts port (subsequence matching with consecutive
// and word-boundary bonuses). Matches: all query runes appear in order (not
// necessarily adjacent); lower score ranks better.
type fuzzyMatch struct {
	Matches bool
	Score   float64
}

// isWordBoundaryRune reports whether r may precede a word boundary for
// scoring purposes (pi fuzzy.ts counts space, -, _, ., /, : and any
// non-alphanumeric as a boundary).
func isWordBoundaryRune(r rune) bool {
	switch r {
	case ' ', '-', '_', '.', '/', ':':
		return true
	}
	return !unicode.IsLetter(r) && !unicode.IsNumber(r)
}

// fuzzyScore scores query against text (both lowercased by the caller).
// Empty query matches everything with score 0.
func fuzzyScore(query, textLower string, textRunes []rune) fuzzyMatch {
	if query == "" {
		return fuzzyMatch{Matches: true}
	}
	qRunes := []rune(query)
	if len(qRunes) > len(textRunes) {
		return fuzzyMatch{}
	}

	// Greedy alignment: each query rune at its first occurrence after the
	// previous one (pi fuzzy.ts matchQuery).
	var positions []int
	last := -1
	for _, q := range qRunes {
		found := -1
		for i := last + 1; i < len(textRunes); i++ {
			if unicode.ToLower(textRunes[i]) == q {
				found = i
				break
			}
		}
		if found < 0 {
			return fuzzyMatch{}
		}
		positions = append(positions, found)
		last = found
	}

	score := 0.0
	lastMatch := -1
	consecutive := 0
	for _, i := range positions {
		if lastMatch == i-1 {
			consecutive++
			score -= float64(consecutive) * 5
		} else {
			consecutive = 0
			if lastMatch >= 0 {
				score += float64(i-lastMatch-1) * 2
			}
		}
		if i == 0 || isWordBoundaryRune(textRunes[i-1]) {
			score -= 10
		}
		score += float64(i) * 0.1
		lastMatch = i
	}
	return fuzzyMatch{Matches: true, Score: score}
}

// fuzzyFilter ranks candidates by fuzzyScore and keeps the top `limit` in
// ascending score order. Candidates keep their original index so callers can
// map back to source data.
type fuzzyCandidate struct {
	Index int
	Score float64
}

func fuzzyFilter(query string, texts []string, limit int) []fuzzyCandidate {
	q := strings.ToLower(query)
	cands := make([]fuzzyCandidate, 0, len(texts))
	for i, text := range texts {
		runes := []rune(strings.ToLower(text))
		m := fuzzyScore(q, strings.ToLower(text), runes)
		if !m.Matches {
			continue
		}
		cands = append(cands, fuzzyCandidate{Index: i, Score: m.Score})
	}
	// Stable insertion into a bounded ascending list (small n; keeps ties in
	// original order without a full sort).
	out := make([]fuzzyCandidate, 0, limit+1)
	for _, c := range cands {
		pos := len(out)
		for j := len(out) - 1; j >= 0; j-- {
			if out[j].Score <= c.Score {
				pos = j + 1
				break
			}
			pos = j
		}
		if pos >= limit {
			continue
		}
		out = append(out, fuzzyCandidate{})
		copy(out[pos+1:], out[pos:])
		out[pos] = c
		if len(out) > limit {
			out = out[:limit]
		}
	}
	return out
}
