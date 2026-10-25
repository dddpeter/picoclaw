package tokenizer

import (
	"encoding/json"
	"math"
	"strings"

	"github.com/sipeed/picoclaw/pkg/providers"
)

// Token cost model (borrowed from octop-harness context_usage.py, see
// docs/design/octop-borrowing-analysis.zh.md §七):
//
// BPE vocabularies spend roughly one token per 1-2 CJK characters against
// ~4 latin characters — a flat chars/2.5 conversion under-counts a Chinese
// prompt by more than a third, delaying every threshold built on these
// estimates (compaction watermarks, tool-trigger ratios, usage display).
const (
	cjkCharsPerToken   = 1.6
	latinCharsPerToken = 4.0
	jsonCharsPerToken  = 3.0
)

// messageFramingTokens is the role marker + JSON structure + separators the
// chat template adds around every message.
const messageFramingTokens = 4

// perSystemPartOverheadTokens / perToolDefOverheadTokens account for the JSON
// structure (type, text, cache_control) around each block/tool definition.
const (
	perSystemPartOverheadTokens = 8
	perToolDefOverheadTokens    = 8
)

// Inline media cost more than any placeholder text suggests: images are
// billed as a tile grid (provider- and resolution-specific, ~1600 mid-range),
// other media blocks ~400.
const (
	imageMediaTokens   = 1600
	otherMediaTokens   = 400
	imageExtHintLenMax = 8 // extension lookback bound for media refs
)

// isCJK reports whether r falls in the token-expensive East Asian ranges:
// kana, CJK extension A, unified ideographs, compatibility ideographs,
// halfwidth katakana, and hangul syllables.
func isCJK(r rune) bool {
	switch {
	case r >= 0x3040 && r <= 0x30FF: // hiragana + katakana
		return true
	case r >= 0x3000 && r <= 0x303F: // CJK symbols & punctuation (、。「」etc.)
		return true
	case r >= 0xFF01 && r <= 0xFF5E: // fullwidth forms （，！？（）等）
		return true
	case r >= 0x3400 && r <= 0x4DBF: // CJK extension A
		return true
	case r >= 0x4E00 && r <= 0x9FFF: // CJK unified ideographs
		return true
	case r >= 0xF900 && r <= 0xFAFF: // CJK compatibility ideographs
		return true
	case r >= 0xFF66 && r <= 0xFF9F: // halfwidth katakana
		return true
	case r >= 0xAC00 && r <= 0xD7AF: // hangul syllables
		return true
	default:
		return false
	}
}

// estimateTextTokens classifies runes by script: CJK ≈ 1.6 chars/token,
// everything else ≈ 4 chars/token. Non-empty input yields at least 1.
func estimateTextTokens(s string) int {
	if s == "" {
		return 0
	}
	cjk, total := 0, 0
	for _, r := range s {
		total++
		if isCJK(r) {
			cjk++
		}
	}
	est := float64(cjk)/cjkCharsPerToken + float64(total-cjk)/latinCharsPerToken
	return max(1, int(math.Round(est)))
}

// EstimateTextTokens is the exported script-aware text estimator, for
// callers outside the tokenizer (context.go's system-prompt segments) so
// both use one CJK-aware cost model.
func EstimateTextTokens(s string) int {
	return estimateTextTokens(s)
}

// estimateJSONTokens estimates serialized JSON, which packs denser than
// prose: braces, quotes, colons, and snake_case keys each split off (~3
// chars/token). Script-aware like estimateTextTokens (review P2): the old
// byte-length conversion charged CJK inside tool arguments ~1 token/char
// and Cyrillic/Greek/Thai 2-3x their real cost, tripping the compression
// gate early — the same text is cheaper when it rides as message content
// than as tool-call JSON, which is incoherent. CJK classifies through
// isCJK (1/1.6 chars/token), everything else keeps the dense /3.
func estimateJSONTokens(s string) int {
	if s == "" {
		return 0
	}
	cjk, total := 0, 0
	for _, r := range s {
		total++
		if isCJK(r) {
			cjk++
		}
	}
	est := float64(cjk)/cjkCharsPerToken + float64(total-cjk)/jsonCharsPerToken
	return max(1, int(math.Round(est)))
}

// mediaRefTokens bills one media reference by type: image references get
// the tile-grid cost, everything else the generic media block cost. Two
// production shapes are recognized: inline data URLs (the current-turn
// image path — `data:image/...;base64`) and file-style refs whose extension
// names a raster/vector format. Plain `media://<uuid>` store refs carry no
// type information in the ref itself (the mime lives in the store's
// metadata, unreachable from this package) and bill as "other" — a known
// under-count for stored images, noted in fork-overview §25.1.
func mediaRefTokens(ref string) int {
	if isImageMediaRef(ref) {
		return imageMediaTokens
	}
	return otherMediaTokens
}

// isImageMediaRef reports whether a media reference names an image.
func isImageMediaRef(ref string) bool {
	if strings.HasPrefix(ref, "data:image/") {
		return true
	}
	return isImageExt(mediaRefExt(ref))
}

// mediaRefExt extracts the lowercase extension of a media ref (may be empty).
func mediaRefExt(ref string) string {
	if len(ref) > imageExtHintLenMax {
		ref = ref[len(ref)-imageExtHintLenMax:]
	}
	if i := strings.LastIndexByte(ref, '.'); i >= 0 {
		return strings.ToLower(ref[i+1:])
	}
	return ""
}

// isImageExt reports whether ext names a raster/vector image format.
func isImageExt(ext string) bool {
	switch ext {
	case "png", "jpg", "jpeg", "gif", "webp", "bmp", "tiff", "svg":
		return true
	default:
		return false
	}
}

// EstimateMessageTokens estimates the token count for a single message,
// including Content, ReasoningContent, ToolCalls arguments, ToolCallID
// metadata, and Media items. Script-aware: CJK content costs ~1.6 chars per
// token against ~4 for latin; JSON arguments ~3 chars per token; media items
// are billed per type (image tile grid vs generic block).
func EstimateMessageTokens(msg providers.Message) int {
	// SystemParts are structured system blocks used for cache-aware adapters.
	// They carry the same content as Content, but in multiple blocks. We
	// estimate them as an alternative representation, not additive.
	systemPartsTokens := 0
	if len(msg.SystemParts) > 0 {
		for _, part := range msg.SystemParts {
			systemPartsTokens += estimateTextTokens(part.Text) + perSystemPartOverheadTokens
		}
	}

	// Use the larger of the two representations to stay conservative.
	tokens := estimateTextTokens(msg.Content)
	if systemPartsTokens > tokens {
		tokens = systemPartsTokens
	}

	tokens += estimateTextTokens(msg.ReasoningContent)

	for _, tc := range msg.ToolCalls {
		// IDs and type markers are ASCII wire-format overhead.
		tokens += estimateTextTokens(tc.ID) + estimateTextTokens(tc.Type)
		if tc.Function != nil {
			// Name is a short ASCII identifier; arguments are wire JSON.
			tokens += estimateTextTokens(tc.Function.Name) + estimateJSONTokens(tc.Function.Arguments)
		} else {
			tokens += estimateTextTokens(tc.Name)
		}
	}

	tokens += estimateTextTokens(msg.ToolCallID)

	tokens += messageFramingTokens

	for _, ref := range msg.Media {
		tokens += mediaRefTokens(ref)
	}

	return tokens
}

// EstimateToolDefsTokens estimates the total token cost of tool definitions
// as they appear in the LLM request: names and descriptions are
// prose-classified (CJK-aware), parameter schemas are JSON-density adjusted.
func EstimateToolDefsTokens(defs []providers.ToolDefinition) int {
	if len(defs) == 0 {
		return 0
	}

	total := 0
	for _, d := range defs {
		total += estimateTextTokens(d.Function.Name) + estimateTextTokens(d.Function.Description)

		if d.Function.Parameters != nil {
			if paramJSON, err := json.Marshal(d.Function.Parameters); err == nil {
				total += estimateJSONTokens(string(paramJSON))
			}
		}

		total += perToolDefOverheadTokens
	}

	return total
}
