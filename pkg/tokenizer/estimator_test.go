package tokenizer

import (
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/providers"
)

// CJK runes must cost visibly more tokens than latin runes of the same count
// (BPE spends roughly one token per 1-2 CJK chars against ~4 latin chars).
// 100 CJK runes ≈ 63 tokens + framing; 100 latin ≈ 25 + framing.
func TestEstimator_CJKVsLatin(t *testing.T) {
	cjkTokens := EstimateMessageTokens(providers.Message{Content: strings.Repeat("中", 100)})
	latinTokens := EstimateMessageTokens(providers.Message{Content: strings.Repeat("a", 100)})

	if cjkTokens < latinTokens*2 {
		t.Fatalf("CJK should cost >= 2x latin: cjk=%d latin=%d", cjkTokens, latinTokens)
	}
	if cjkTokens < 60 || cjkTokens > 70 {
		t.Fatalf("100 CJK runes should estimate ~63+framing tokens, got %d", cjkTokens)
	}
	if latinTokens < 22 || latinTokens > 32 {
		t.Fatalf("100 latin chars should estimate ~25+framing tokens, got %d", latinTokens)
	}
}

// Mixed content is the weighted sum: 80 CJK (~50) + 120 latin (~30) ≈ 80 + framing.
func TestEstimator_MixedContentWeighted(t *testing.T) {
	content := strings.Repeat("中", 80) + strings.Repeat("a", 120)
	got := EstimateMessageTokens(providers.Message{Content: content})
	if got < 78 || got > 90 {
		t.Fatalf("80 CJK + 120 latin should estimate ~80+framing tokens, got %d", got)
	}
}

// JSON (tool-call arguments, tool schemas) tokenizes denser than prose:
// ~3 chars/token against ~4 for plain latin.
func TestEstimator_JSONDenser(t *testing.T) {
	args := `{"path":"/works/workspace/ai/picoclaw/pkg/agent/context.go","offset":120,"limit":40,"expand":true}`
	jsonMsg := providers.Message{
		ToolCalls: []providers.ToolCall{{
			ID:   "call_1",
			Type: "function",
			Function: &providers.FunctionCall{
				Name:      "read_file",
				Arguments: args,
			},
		}},
	}
	plainMsg := providers.Message{Content: args}

	jsonTokens := EstimateMessageTokens(jsonMsg)
	plainTokens := EstimateMessageTokens(plainMsg)

	if jsonTokens <= plainTokens {
		t.Fatalf("JSON-wrapped args should estimate denser than the same bytes as prose: json=%d plain=%d", jsonTokens, plainTokens)
	}
	// len(args) ≈ 95 ASCII bytes → 95/3 ≈ 32 tokens; + name/id/type/framing ≈ 10.
	if jsonTokens < 35 || jsonTokens > 50 {
		t.Fatalf("JSON args estimate out of expected range, got %d", jsonTokens)
	}
}

// Media refs are billed by type: inline images cost far more than other media
// blocks (image ≈ 1600 tokens against ≈ 400 for the rest).
func TestEstimator_ImageVsOtherMedia(t *testing.T) {
	imgMsg := providers.Message{Media: []string{"media://store/shot.png"}}
	audioMsg := providers.Message{Media: []string{"media://store/voice.mp3"}}

	imgTokens := EstimateMessageTokens(imgMsg)
	audioTokens := EstimateMessageTokens(audioMsg)

	if imgTokens < 1200 {
		t.Fatalf("image media should estimate >= 1200 tokens, got %d", imgTokens)
	}
	if audioTokens > 600 {
		t.Fatalf("non-image media should estimate <= 600 tokens, got %d", audioTokens)
	}
	if imgTokens < audioTokens*2 {
		t.Fatalf("image should cost far more than other media: img=%d audio=%d", imgTokens, audioTokens)
	}
}

// Empty message keeps a small framing-only estimate (unchanged behavior).
func TestEstimator_EmptyMessage(t *testing.T) {
	got := EstimateMessageTokens(providers.Message{})
	if got <= 0 || got > 12 {
		t.Fatalf("empty message should keep a small framing-only estimate, got %d", got)
	}
}

// Tool definitions mix prose descriptions with JSON schemas: description is
// prose-classified (CJK-aware), parameters JSON is density-adjusted.
func TestEstimator_ToolDefs_CJKDescription(t *testing.T) {
	defs := []providers.ToolDefinition{{
		Type: "function",
		Function: providers.ToolFunctionDefinition{
			Name:        "search_web",
			Description: strings.Repeat("搜索", 50), // 100 CJK runes
			Parameters:  nil,
		},
	}}
	got := EstimateToolDefsTokens(defs)
	// 100 CJK runes ≈ 63 tokens + name (~3) + per-tool overhead (8).
	if got < 60 || got > 85 {
		t.Fatalf("CJK tool description should estimate ~63+overhead tokens, got %d", got)
	}
}

// TestEstimator_DataURLImageTyping pins the production media shapes (review
// P1-7): current-turn inline images ride data URLs — the image tile cost
// must fire on the `data:image/` prefix, while extension-less media:// store
// refs (mime lives in store metadata, unreachable here) stay at the generic
// block cost as a documented under-count.
func TestEstimator_DataURLImageTyping(t *testing.T) {
	dataURLMsg := providers.Message{Media: []string{"data:image/png;base64,iVBORw0KGgo="}}
	storeRefMsg := providers.Message{Media: []string{"media://3f06b1a2-0000-4000-8000-1c2d3e4f5a6b"}}

	dataURLTokens := EstimateMessageTokens(dataURLMsg)
	storeRefTokens := EstimateMessageTokens(storeRefMsg)

	if dataURLTokens < 1200 {
		t.Fatalf("data:image/ URL must bill as an image (>= 1200), got %d", dataURLTokens)
	}
	if storeRefTokens > 600 {
		t.Fatalf("extension-less media:// ref must bill at the generic block cost (<= 600), got %d", storeRefTokens)
	}
	if dataURLTokens < storeRefTokens*2 {
		t.Fatalf("image data URL must far out-cost a plain media ref: %d vs %d", dataURLTokens, storeRefTokens)
	}
}
