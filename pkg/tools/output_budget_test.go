// PicoClaw - Ultra-lightweight personal AI agent

package tools

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestApplyOutputBudget_Passthrough(t *testing.T) {
	cases := []struct {
		name    string
		content string
		max     int
	}{
		{"under budget", "short output", 1024},
		{"disabled zero", strings.Repeat("x", 10000), 0},
		{"disabled negative", strings.Repeat("x", 10000), -1},
		{"empty", "", 10},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ApplyOutputBudget("tool_x", tc.content, tc.max); got != tc.content {
				t.Fatalf("expected unchanged content, got %d bytes (was %d)", len(got), len(tc.content))
			}
		})
	}
}

func TestApplyOutputBudget_TruncatesKeepingTail(t *testing.T) {
	content := "HEAD-" + strings.Repeat("m", 4096) + "-TAIL"
	got := ApplyOutputBudget("tool_x", content, 2048)

	if !strings.HasPrefix(got, "[output truncated: kept the last") {
		t.Fatalf("expected truncation notice prefix, got %q", got[:60])
	}
	if !strings.HasSuffix(got, "-TAIL") {
		t.Fatal("expected the tail to be kept, not the head")
	}
	if !strings.Contains(got[:strings.IndexByte(got, '\n')], "of 4106 bytes]") {
		t.Fatalf("expected original size in notice, got %q", got[:80])
	}
}

func TestApplyOutputBudget_UTF8BoundarySafe(t *testing.T) {
	// 3-byte runes; force the cut to land mid-rune.
	runes := strings.Repeat("世", 3000)              // 9000 bytes
	got := ApplyOutputBudget("tool_x", runes, 4097) // 4097 = 1365*3 + 2 → mid-rune

	body := got[strings.IndexByte(got, '\n')+1:]
	if !utf8.ValidString(body) {
		t.Fatalf("truncated body must stay valid UTF-8, got invalid sequence")
	}
	if len(body) > 4097 {
		t.Fatalf("kept body larger than budget: %d", len(body))
	}
	if n := utf8.RuneCountInString(body); n != 1365 {
		t.Fatalf("expected 1365 whole runes kept, got %d", n)
	}
}

func TestApplyOutputBudget_DefaultsAboveBuiltInBudgets(t *testing.T) {
	// The default must sit above every built-in tool's own budget so they
	// never hit the backstop and never grow a double truncation notice.
	if DefaultToolOutputBytes <= 64<<10 { // read_file budget
		t.Fatalf("default budget %d must exceed read_file's 64KB", DefaultToolOutputBytes)
	}
	if DefaultToolOutputBytes <= 50<<10 { // shell budget
		t.Fatalf("default budget %d must exceed shell's 50KB", DefaultToolOutputBytes)
	}
	if effectiveOutputBudget(0) != DefaultToolOutputBytes {
		t.Fatal("0 must resolve to the default")
	}
	if effectiveOutputBudget(-5) != -5 {
		t.Fatal("negative must stay negative (unlimited)")
	}
	if effectiveOutputBudget(777) != 777 {
		t.Fatal("positive value must pass through")
	}
}

func TestExecuteWithContext_AppliesOutputBudget(t *testing.T) {
	r := NewToolRegistry()
	tool := newMockTool("big", "returns a huge result")
	tool.result = SilentResult(strings.Repeat("some ordinary command output line with words\n", 160)) // 7.2KB
	r.Register(tool)

	r.SetMaxOutputBytes(1000)
	result := r.Execute(context.Background(), "big", map[string]any{})

	if result.ForLLM == "" {
		t.Fatal("expected non-empty result")
	}
	if !strings.HasPrefix(result.ForLLM, "[output truncated: kept the last") {
		t.Fatalf("expected budget truncation at the registry exit, got %q", result.ForLLM[:min(60, len(result.ForLLM))])
	}
	if !strings.Contains(result.ForLLM, "words") {
		t.Fatal("expected tail content to survive")
	}

	// Unlimited must pass the full content through untouched.
	r.SetMaxOutputBytes(-1)
	tool.result = SilentResult(strings.Repeat("some ordinary command output line with words\n", 160))
	result = r.Execute(context.Background(), "big", map[string]any{})
	if len(result.ForLLM) != 160*45 {
		t.Fatalf("expected full %d bytes with unlimited budget, got %d", 160*45, len(result.ForLLM))
	}
}
