package pico

import (
	"context"
	"testing"

	"github.com/sipeed/picoclaw/pkg/providers"
)

func TestSetTurnUsagePayload(t *testing.T) {
	t.Run("populates usage block when counts present", func(t *testing.T) {
		payload := map[string]any{PayloadKeyContent: "hi"}
		setTurnUsagePayload(payload, providers.UsageInfo{PromptTokens: 1234, CompletionTokens: 567}, 3)

		raw, ok := payload[PayloadKeyUsage]
		if !ok {
			t.Fatalf("expected %q key in payload", PayloadKeyUsage)
		}
		usage, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("usage block is not a map: %T", raw)
		}
		if usage["input_tokens"] != 1234 {
			t.Errorf("input_tokens = %v, want 1234", usage["input_tokens"])
		}
		if usage["output_tokens"] != 567 {
			t.Errorf("output_tokens = %v, want 567", usage["output_tokens"])
		}
		if usage["total_tokens"] != 1801 {
			t.Errorf("total_tokens = %v, want 1801", usage["total_tokens"])
		}
		if usage["llm_calls"] != 3 {
			t.Errorf("llm_calls = %v, want 3", usage["llm_calls"])
		}
		if _, ok := usage["cached_tokens"]; ok {
			t.Errorf("cached_tokens should be omitted when upstream reports none, got %v", usage["cached_tokens"])
		}
	})

	t.Run("emits cached_tokens when the upstream reports cache hits", func(t *testing.T) {
		payload := map[string]any{PayloadKeyContent: "hi"}
		setTurnUsagePayload(payload, providers.UsageInfo{
			PromptTokens:        1200,
			CompletionTokens:    340,
			PromptTokensDetails: &providers.PromptTokensDetails{CachedTokens: 900},
		}, 1)

		usage := payload[PayloadKeyUsage].(map[string]any)
		if usage["cached_tokens"] != 900 {
			t.Errorf("cached_tokens = %v, want 900", usage["cached_tokens"])
		}
		// DeepSeek 平铺风格也应归一进同一字段。
		payload2 := map[string]any{PayloadKeyContent: "hi"}
		setTurnUsagePayload(payload2, providers.UsageInfo{
			PromptTokens:         1200,
			CompletionTokens:     340,
			PromptCacheHitTokens: 800,
		}, 1)
		usage2 := payload2[PayloadKeyUsage].(map[string]any)
		if usage2["cached_tokens"] != 800 {
			t.Errorf("cached_tokens (flat spelling) = %v, want 800", usage2["cached_tokens"])
		}
	})

	t.Run("omits llm_calls when zero", func(t *testing.T) {
		payload := map[string]any{PayloadKeyContent: "hi"}
		setTurnUsagePayload(payload, providers.UsageInfo{PromptTokens: 1234, CompletionTokens: 567}, 0)
		usage := payload[PayloadKeyUsage].(map[string]any)
		if _, ok := usage["llm_calls"]; ok {
			t.Errorf("llm_calls should be omitted when zero, got %v", usage["llm_calls"])
		}
	})

	t.Run("omits usage block when all counts zero", func(t *testing.T) {
		payload := map[string]any{PayloadKeyContent: "hi"}
		setTurnUsagePayload(payload, providers.UsageInfo{}, 0)
		if _, ok := payload[PayloadKeyUsage]; ok {
			t.Errorf("expected no %q key when counts are zero", PayloadKeyUsage)
		}
	})
}

// newCaptureStreamer returns a streamer whose broadcasts are captured into the
// returned map pointer, so tests need no live websocket.
func newCaptureStreamer() (*picoStreamer, *map[string]any) {
	var last map[string]any
	ch := &PicoChannel{}
	ch.broadcastFn = func(chatID string, msg PicoMessage) error {
		last = msg.Payload
		return nil
	}
	s := &picoStreamer{channel: ch, chatID: "c1"}
	return s, &last
}

func TestStreamerEmitsUsageOnFinalize(t *testing.T) {
	s, last := newCaptureStreamer()
	s.SetTurnUsage(providers.UsageInfo{PromptTokens: 100, CompletionTokens: 40})
	s.SetTurnLLMCalls(2)

	// sendLocked with empty messageID takes the create branch, which attaches
	// usage from the streamer's stored counts.
	s.mu.Lock()
	err := s.sendLocked(context.Background(), "answer", nil)
	s.mu.Unlock()
	if err != nil {
		t.Fatalf("sendLocked: %v", err)
	}
	usage, ok := (*last)[PayloadKeyUsage].(map[string]any)
	if !ok {
		t.Fatalf("expected usage in payload, got %+v", *last)
	}
	if usage["llm_calls"] != 2 {
		t.Errorf("llm_calls = %v, want 2", usage["llm_calls"])
	}

	// 缓存命中随封板 usage 透出（端到端：SetTurnUsage → payload.cached_tokens）。
	// 复用同 streamer 会走 edit 分支（其 broadcastToSession 无 mock 拦截、无连接），
	// 故用新 streamer 再走一次 create 分支。
	s2, last2 := newCaptureStreamer()
	s2.SetTurnUsage(providers.UsageInfo{
		PromptTokens:        1200,
		CompletionTokens:    340,
		PromptTokensDetails: &providers.PromptTokensDetails{CachedTokens: 900},
	})
	s2.mu.Lock()
	err = s2.sendLocked(context.Background(), "answer", nil)
	s2.mu.Unlock()
	if err != nil {
		t.Fatalf("sendLocked: %v", err)
	}
	usage2 := (*last2)[PayloadKeyUsage].(map[string]any)
	if usage2["cached_tokens"] != 900 {
		t.Errorf("cached_tokens = %v, want 900", usage2["cached_tokens"])
	}
}
