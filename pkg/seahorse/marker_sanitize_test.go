package seahorse

import (
	"fmt"
	"strings"
	"testing"
)

// TestSanitizeToolMarkers (fork, 2026-10-05): readable-content marker lines
// are rewritten into prose so summarizer inputs and stored summaries never
// teach the LLM the bracket format it later parrots as answers.
func TestSanitizeToolMarkers(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "tool_use line rewritten",
			in:   "[tool_use: bash, args: {\"command\":\"ls\"}]",
			want: `tool call bash, args: {"command":"ls"}`,
		},
		{
			name: "tool_result line drops call id keeps payload",
			in:   "[tool_result for call-abc-1: file-a file-b]",
			want: "tool result: file-a file-b",
		},
		{
			name: "media line rewritten",
			in:   "[media: /tmp/a.png (image/png)]",
			want: "media attachment /tmp/a.png (image/png)",
		},
		{
			name: "mixed prose keeps prose",
			in:   "ran the build\n[tool_use: bash, args: {}]\nall green",
			want: "ran the build\ntool call bash, args: {}\nall green",
		},
		{
			name: "no markers unchanged",
			in:   "plain summary text\n[other bracket] fine",
			want: "plain summary text\n[other bracket] fine",
		},
		{
			name: "unclosed marker left alone",
			in:   "[tool_use: bash, args: {truncated",
			want: "[tool_use: bash, args: {truncated",
		},
		{
			// tool_result text with real newlines splits the marker across
			// physical lines; only the closed single-line form rewrites
			// (accepted noise, same policy as the display filter).
			name: "multiline tool_result payload left as-is",
			in:   "[tool_result for c1: line1\nline2]",
			want: "[tool_result for c1: line1\nline2]",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sanitizeToolMarkers(tc.in); got != tc.want {
				t.Fatalf("sanitizeToolMarkers(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestSanitizeToolMarkers_CRLF: CRLF line endings are normalized to LF, the
// same normalization the display-side filter applies.
func TestSanitizeToolMarkers_CRLF(t *testing.T) {
	in := "prose\r\n[tool_use: x, args: {}]\r\nmore"
	want := "prose\ntool call x, args: {}\nmore"
	if got := sanitizeToolMarkers(in); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// TestFormatMessagesForSummary_SanitizesMarkers: the summarizer input never
// contains raw marker lines, whether content came from the readable column
// or the parts fallback.
func TestFormatMessagesForSummary_SanitizesMarkers(t *testing.T) {
	msgs := []Message{
		{
			Role: "assistant",
			Parts: []MessagePart{{
				Type: "tool_use", Name: "bash",
				Arguments:  `{"command":"ls"}`,
				ToolCallID: "call-1",
			}},
		},
		{Role: "user", Content: "继续"},
	}
	out := formatMessagesForSummary(msgs)
	if strings.Contains(out, "[tool_use: ") {
		t.Fatalf("marker leaked into summarizer input: %q", out)
	}
	if !strings.Contains(out, "tool call bash") {
		t.Fatalf("prose rewrite missing: %q", out)
	}
}

// TestTruncateSummary_SanitizesMarkers: the no-LLM fallback must not store
// marker lines verbatim into summaries.
func TestTruncateSummary_SanitizesMarkers(t *testing.T) {
	msgs := []Message{
		{Role: "assistant", Content: "[tool_use: bash, args: {}]\n[tool_result for c1: done]"},
	}
	out := truncateSummary(msgs)
	if strings.Contains(out, "[tool_use: ") || strings.Contains(out, "[tool_result") {
		t.Fatalf("markers survived fallback: %q", out)
	}
	if !strings.Contains(out, "tool call bash") || !strings.Contains(out, "tool result: done") {
		t.Fatalf("prose rewrite missing: %q", out)
	}
}

// TestFormatSummaryXML_SanitizesMarkers: legacy summaries that already carry
// marker lines are healed at assembly time, no DB migration needed.
func TestFormatSummaryXML_SanitizesMarkers(t *testing.T) {
	sum := Summary{
		SummaryID: "s-1",
		Kind:      SummaryKindLeaf,
		Content:   "did work\n[tool_use: bash, args: {}]\ndone",
	}
	out := FormatSummaryXML(&sum, nil)
	if strings.Contains(out, "[tool_use: ") {
		t.Fatalf("marker leaked into summary XML: %q", out)
	}
	if !strings.Contains(out, "tool call bash") {
		t.Fatalf("prose rewrite missing: %q", out)
	}
}

// TestSummaryPromptsForbidMarkers: all three summary prompt builders carry
// the anti-marker rule so summarizer output never reproduces the format.
func TestSummaryPromptsForbidMarkers(t *testing.T) {
	builders := map[string]func() string{
		"leaf": func() string { return buildLeafSummaryPrompt("src", "", 100) },
		"aggressive": func() string {
			return buildAggressiveLeafSummaryPrompt("src", "", 100)
		},
		"condensed": func() string { return buildCondensedSummaryPrompt("src", 100) },
	}
	for name, build := range builders {
		t.Run(name, func(t *testing.T) {
			prompt := build()
			if !strings.Contains(prompt, "Never reproduce bracketed tool-call markers") {
				t.Fatalf("prompt missing anti-marker rule:\n%s", prompt)
			}
			if !strings.Contains(prompt, fmt.Sprintf("%d", 100)) {
				t.Fatalf("prompt missing token target")
			}
		})
	}
}
