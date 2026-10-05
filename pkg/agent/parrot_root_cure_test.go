package agent

import (
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/providers/protocoltypes"
	"github.com/sipeed/picoclaw/pkg/seahorse"
)

// TestProviderToSeahorseMessage_TextPart (fork, 2026-10-05): a message that
// carries text alongside tool calls or media must store the text as a real
// part — the DB content column is derived from Parts alone, so without the
// text part the prose was lost and replay fed the derived marker lines back
// to the LLM (tool-call parrot root cause). Tool-result messages keep their
// text in the tool_result part; pure-text messages stay on the no-parts path
// so bootstrap parts-matching against pre-fork rows stays stable.
func TestProviderToSeahorseMessage_TextPart(t *testing.T) {
	t.Run("text with tool calls becomes text part plus tool_use parts", func(t *testing.T) {
		msg := providerToSeahorseMessage(protocoltypes.Message{
			Role: "assistant",
			ToolCalls: []protocoltypes.ToolCall{{
				ID: "call-1", Type: "function",
				Function: &protocoltypes.FunctionCall{Name: "bash", Arguments: `{"command":"ls"}`},
			}},
			Content: "I'll list the directory first.",
		})
		if len(msg.Parts) != 2 {
			t.Fatalf("parts = %d, want 2 (text + tool_use): %+v", len(msg.Parts), msg.Parts)
		}
		if msg.Parts[0].Type != "text" || msg.Parts[0].Text != "I'll list the directory first." {
			t.Fatalf("text part missing or wrong: %+v", msg.Parts[0])
		}
		if msg.Parts[1].Type != "tool_use" || msg.Parts[1].Name != "bash" {
			t.Fatalf("tool_use part wrong: %+v", msg.Parts[1])
		}
	})

	t.Run("text with media becomes text part plus media part", func(t *testing.T) {
		msg := providerToSeahorseMessage(protocoltypes.Message{
			Role:    "user",
			Content: "what is in this picture",
			Media:   []string{"data:image/png;base64,xxx"},
		})
		if len(msg.Parts) != 2 || msg.Parts[0].Type != "text" || msg.Parts[1].Type != "media" {
			t.Fatalf("unexpected parts: %+v", msg.Parts)
		}
	})

	t.Run("pure text message keeps no-parts shape", func(t *testing.T) {
		msg := providerToSeahorseMessage(protocoltypes.Message{
			Role:    "assistant",
			Content: "plain answer",
		})
		if len(msg.Parts) != 0 {
			t.Fatalf("pure text message gained parts: %+v", msg.Parts)
		}
	})

	t.Run("tool result message carries text in tool_result part only", func(t *testing.T) {
		msg := providerToSeahorseMessage(protocoltypes.Message{
			Role:       "tool",
			ToolCallID: "call-1",
			Content:    "file-a\nfile-b",
		})
		if len(msg.Parts) != 1 || msg.Parts[0].Type != "tool_result" || msg.Parts[0].Text != "file-a\nfile-b" {
			t.Fatalf("unexpected parts: %+v", msg.Parts)
		}
	})
}

// TestSeahorseToProviderMessages_ReplayContentClean (fork, 2026-10-05): the
// replay path must derive message content from text parts and never ship the
// storage marker lines ("[tool_use: ...]" etc.) as LLM-visible prose — for
// new rows via the text part, for legacy marker-content rows via the strip
// fallback. Tool result text still comes from its part verbatim, user text
// is never stripped, and assistant prose that itself parroted the format is
// cleaned so the self-reinforcement loop breaks.
func TestSeahorseToProviderMessages_ReplayContentClean(t *testing.T) {
	build := func(msgs ...seahorse.Message) []protocoltypes.Message {
		return seahorseToProviderMessages(&seahorse.AssembleResult{Messages: msgs})
	}

	t.Run("legacy marker-only row replays without marker prose", func(t *testing.T) {
		got := build(seahorse.Message{
			Role:    "assistant",
			Content: `[tool_use: bash, args: {"command":"ls"}]`,
			Parts: []seahorse.MessagePart{{
				Type: "tool_use", Name: "bash",
				Arguments:  `{"command":"ls"}`,
				ToolCallID: "call-1",
			}},
		})
		if len(got) != 1 {
			t.Fatalf("messages = %d", len(got))
		}
		if got[0].Content != "" {
			t.Fatalf("legacy marker content leaked into replay: %q", got[0].Content)
		}
		if len(got[0].ToolCalls) != 1 || got[0].ToolCalls[0].Function.Name != "bash" {
			t.Fatalf("tool calls not reconstructed: %+v", got[0].ToolCalls)
		}
	})

	t.Run("new row replays text part and keeps tool calls", func(t *testing.T) {
		got := build(seahorse.Message{
			Role:    "assistant",
			Content: "listing now\n[tool_use: bash, args: {\"command\":\"ls\"}]",
			Parts: []seahorse.MessagePart{
				{Type: "text", Text: "listing now"},
				{Type: "tool_use", Name: "bash", Arguments: `{}`, ToolCallID: "call-1"},
			},
		})
		if got[0].Content != "listing now" {
			t.Fatalf("replay content = %q, want %q", got[0].Content, "listing now")
		}
		if len(got[0].ToolCalls) != 1 {
			t.Fatalf("tool calls lost: %+v", got[0].ToolCalls)
		}
	})

	t.Run("legacy mixed row keeps prose, drops markers", func(t *testing.T) {
		got := build(seahorse.Message{
			Role:    "user",
			Content: "look at this image\n[media: /tmp/a.png (image/png)]",
			Parts: []seahorse.MessagePart{
				{Type: "media", MediaURI: "/tmp/a.png", MimeType: "image/png"},
			},
		})
		if got[0].Content != "look at this image" {
			t.Fatalf("legacy mixed content = %q", got[0].Content)
		}
	})

	t.Run("tool result fills content verbatim", func(t *testing.T) {
		got := build(seahorse.Message{
			Role:    "tool",
			Content: "[tool_result for call-1: raw output with [brackets] inside]",
			Parts: []seahorse.MessagePart{{
				Type: "tool_result", ToolCallID: "call-1",
				Text: "raw output with [brackets] inside",
			}},
		})
		if got[0].Content != "raw output with [brackets] inside" || got[0].ToolCallID != "call-1" {
			t.Fatalf("tool result replay wrong: content=%q id=%q", got[0].Content, got[0].ToolCallID)
		}
	})

	t.Run("user quoting markers verbatim is preserved", func(t *testing.T) {
		quoted := "what does this mean\n[tool_use: bash, args: {}]"
		got := build(seahorse.Message{Role: "user", Content: quoted})
		if got[0].Content != quoted {
			t.Fatalf("user content mutated: %q", got[0].Content)
		}
	})

	t.Run("assistant parroted prose is stripped on replay", func(t *testing.T) {
		got := build(seahorse.Message{
			Role:    "assistant",
			Content: "partial answer\n[tool_use: bash, args: {}]\nmore answer",
		})
		if got[0].Content != "partial answer\nmore answer" {
			t.Fatalf("assistant parrot prose not stripped: %q", got[0].Content)
		}
	})
}

// TestParrotOnlyAnswerAutoRetry (fork, 2026-10-05): a direct answer that is
// nothing but replayed tool-call marker lines is auto-retried once with a
// corrective directive instead of ending the turn on the interception note.
// The parroted draft and the directive stay request-view only.
func TestParrotOnlyAnswerAutoRetry(t *testing.T) {
	t.Run("retry recovers a real answer", func(t *testing.T) {
		provider := &scriptedAnswerProvider{responses: []providers.LLMResponse{
			{Content: "[tool_use: bash, args: {\"command\":\"ls\"}]", FinishReason: "stop"},
			{Content: "目录里有三个文件", FinishReason: "stop"},
		}}
		response, al := runLengthNoteTurn(t, provider, "session-parrot-retry")
		if response != "目录里有三个文件" {
			t.Fatalf("response = %q", response)
		}
		if len(provider.calls) != 2 {
			t.Fatalf("LLM calls = %d, want 2", len(provider.calls))
		}
		second := provider.calls[1]
		if len(second) < 3 {
			t.Fatalf("retry request too short: %+v", second)
		}
		if draft := second[len(second)-2]; draft.Role != "assistant" ||
			!strings.HasPrefix(draft.Content, "[tool_use: ") {
			t.Fatalf("parrot draft missing from retry view: %+v", draft)
		}
		if directive := second[len(second)-1]; directive.Role != "user" ||
			!strings.Contains(directive.Content, "复述") {
			t.Fatalf("corrective directive missing: %+v", directive)
		}
		history := al.registry.GetDefaultAgent().Sessions.GetHistory("session-parrot-retry")
		for _, m := range history {
			if strings.Contains(m.Content, "[tool_use: ") {
				t.Fatalf("parrot draft or marker text leaked into history: %+v", m)
			}
			if m.Role == "user" && strings.Contains(m.Content, "复述") {
				t.Fatalf("corrective directive leaked into history: %+v", m)
			}
		}
	})

	t.Run("retry happens at most once", func(t *testing.T) {
		provider := &scriptedAnswerProvider{responses: []providers.LLMResponse{
			{Content: "[tool_use: bash, args: {}]", FinishReason: "stop"},
			{Content: "[tool_result for call-1: nothing]", FinishReason: "stop"},
		}}
		response, _ := runLengthNoteTurn(t, provider, "session-parrot-exhaust")
		if len(provider.calls) != 2 {
			t.Fatalf("LLM calls = %d, want 2 (no second retry)", len(provider.calls))
		}
		// The un-recovered turn keeps the raw final content in history
		// (display filtering is the card/outbound layer's job).
		if response != "[tool_result for call-1: nothing]" {
			t.Fatalf("response = %q", response)
		}
	})

	t.Run("normal answer is not retried", func(t *testing.T) {
		provider := &scriptedAnswerProvider{responses: []providers.LLMResponse{
			{Content: "普通回答", FinishReason: "stop"},
		}}
		response, _ := runLengthNoteTurn(t, provider, "session-parrot-clean")
		if response != "普通回答" || len(provider.calls) != 1 {
			t.Fatalf("response = %q, calls = %d", response, len(provider.calls))
		}
	})
}
