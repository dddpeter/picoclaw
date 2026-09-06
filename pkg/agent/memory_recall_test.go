package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
)

func TestNormalizeMemoryRecallConfig(t *testing.T) {
	got := normalizeMemoryRecallConfig(config.MemoryRecallConfig{Enabled: true, Server: "openviking"})
	if got.Tool != "search" {
		t.Errorf("default tool = %q, want search", got.Tool)
	}
	if got.MaxChars != defaultMemoryRecallMaxChars {
		t.Errorf("default max chars = %d, want %d", got.MaxChars, defaultMemoryRecallMaxChars)
	}
	if got.TimeoutMs != defaultMemoryRecallTimeoutMs {
		t.Errorf("default timeout = %d, want %d", got.TimeoutMs, defaultMemoryRecallTimeoutMs)
	}
}

func TestMemoryRecallContributorInjectsResults(t *testing.T) {
	c := &memoryRecallContributor{
		cfg: normalizeMemoryRecallConfig(config.MemoryRecallConfig{
			Enabled: true,
			Server:  "openviking",
		}),
		recall: func(_ context.Context, query string) (string, error) {
			if query != "帮我画一只柴犬骑士" {
				t.Errorf("unexpected query: %q", query)
			}
			return "用户喜欢柴犬主题的插图，偏好全甲战斗风格。", nil
		},
	}

	parts, err := c.ContributePrompt(context.Background(), PromptBuildRequest{
		CurrentMessage: "帮我画一只柴犬骑士",
	})
	if err != nil {
		t.Fatalf("ContributePrompt failed: %v", err)
	}
	if len(parts) != 1 {
		t.Fatalf("expected 1 part, got %d", len(parts))
	}
	part := parts[0]
	if part.Layer != PromptLayerContext || part.Slot != PromptSlotMemory {
		t.Fatalf("unexpected placement: layer=%s slot=%s", part.Layer, part.Slot)
	}
	if part.Stable {
		t.Error("recall must not be marked stable — it varies per turn")
	}
	if !strings.Contains(part.Content, "柴犬") {
		t.Fatalf("recall text missing from content:\n%s", part.Content)
	}
	if !strings.Contains(part.Content, "defer to explicit user instructions") {
		t.Fatalf("recall should carry the approximation disclaimer:\n%s", part.Content)
	}
}

func TestMemoryRecallContributorTruncates(t *testing.T) {
	c := &memoryRecallContributor{
		cfg: normalizeMemoryRecallConfig(config.MemoryRecallConfig{
			Enabled:  true,
			Server:   "openviking",
			MaxChars: 50,
		}),
		recall: func(_ context.Context, _ string) (string, error) {
			return strings.Repeat("记", 200), nil
		},
	}
	parts, _ := c.ContributePrompt(context.Background(), PromptBuildRequest{CurrentMessage: "hi"})
	if len(parts) != 1 {
		t.Fatalf("expected 1 part, got %d", len(parts))
	}
	runes := []rune(parts[0].Content)
	if len(runes) > 50+300 { // cap + scaffolding/disclaimer slack
		t.Fatalf("recall content not truncated near the cap: %d runes", len(runes))
	}
	if !strings.Contains(parts[0].Content, "recall truncated") {
		t.Fatalf("truncated recall should carry a marker:\n%s", parts[0].Content)
	}
}

func TestMemoryRecallContributorDegradesSilently(t *testing.T) {
	cfg := normalizeMemoryRecallConfig(config.MemoryRecallConfig{Enabled: true, Server: "openviking"})

	cases := map[string]*memoryRecallContributor{
		"recall error": {
			cfg: cfg,
			recall: func(_ context.Context, _ string) (string, error) {
				return "", errors.New("server unreachable")
			},
		},
		"empty result": {
			cfg: cfg,
			recall: func(_ context.Context, _ string) (string, error) {
				return "   ", nil
			},
		},
		"no recall fn": {cfg: cfg},
	}
	for name, c := range cases {
		parts, err := c.ContributePrompt(context.Background(), PromptBuildRequest{CurrentMessage: "hello"})
		if err != nil {
			t.Errorf("%s: expected silent degradation, got error: %v", name, err)
		}
		if len(parts) != 0 {
			t.Errorf("%s: expected no parts, got %d", name, len(parts))
		}
	}

	// Empty user message never triggers a recall call.
	called := false
	c := &memoryRecallContributor{
		cfg: cfg,
		recall: func(_ context.Context, _ string) (string, error) {
			called = true
			return "x", nil
		},
	}
	parts, err := c.ContributePrompt(context.Background(), PromptBuildRequest{})
	if err != nil || len(parts) != 0 || called {
		t.Errorf("empty message should skip recall: parts=%d err=%v called=%v", len(parts), err, called)
	}
}

func TestMemoryRecallContributorHonorsTimeout(t *testing.T) {
	c := &memoryRecallContributor{
		cfg: normalizeMemoryRecallConfig(config.MemoryRecallConfig{
			Enabled:   true,
			Server:    "openviking",
			TimeoutMs: 20,
		}),
		recall: func(ctx context.Context, _ string) (string, error) {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(time.Second):
				return "late", nil
			}
		},
	}
	parts, err := c.ContributePrompt(context.Background(), PromptBuildRequest{CurrentMessage: "hello"})
	if err != nil || len(parts) != 0 {
		t.Fatalf("timed-out recall should degrade silently: parts=%d err=%v", len(parts), err)
	}
}
