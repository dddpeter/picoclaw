package agent

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/providers"
)

func TestMapTurnErrorToUserMessage(t *testing.T) {
	exhaustedNoCooldown := &providers.FallbackExhaustedError{Attempts: []providers.FallbackAttempt{
		{Provider: "p1", Model: "m1", Skipped: true, Reason: providers.FailoverRateLimit},
	}}
	exhaustedWithCooldown := &providers.FallbackExhaustedError{Attempts: []providers.FallbackAttempt{
		{Provider: "p1", Model: "m1", Skipped: true, Reason: providers.FailoverRateLimit, CooldownRemaining: 30 * time.Second},
		{Provider: "p2", Model: "m2", Skipped: true, Reason: providers.FailoverRateLimit, CooldownRemaining: 90 * time.Second},
	}}

	cases := []struct {
		name      string
		err       error
		wantFirst string // complete first line (prefix + optional recovery suffix)
	}{
		{"fallback exhausted", exhaustedNoCooldown,
			"⚠ 模型暂时不可用：所有候选模型均失败或限流"},
		{"fallback exhausted with cooldown rounds up minutes", exhaustedWithCooldown,
			"⚠ 模型暂时不可用：所有候选模型均失败或限流，最早约 2 分钟后可恢复"},
		{"wrapped fallback exhausted still classified", fmt.Errorf("call llm: %w", exhaustedWithCooldown),
			"⚠ 模型暂时不可用：所有候选模型均失败或限流，最早约 2 分钟后可恢复"},
		{"context window", errors.New("API Error: maximum Context Window exceeded"),
			"⚠ 上下文超限且自动压缩失败，建议发送 /new 开启新会话"},
		{"auth 401", errors.New("API request failed: Status: 401 Body: invalid token"),
			"⚠ 模型鉴权失败，请检查 API Key 配置"},
		{"auth unauthorized", errors.New("403 Forbidden: unauthorized"),
			"⚠ 模型鉴权失败，请检查 API Key 配置"},
		{"auth invalid api key", errors.New("Invalid API key provided"),
			"⚠ 模型鉴权失败，请检查 API Key 配置"},
		{"rate limit 429", errors.New("API request failed: Status: 429 Body: too many requests"),
			"⚠ 模型限流中，已稍后自动恢复，请稍后重试"},
		{"rate limit text", errors.New("Rate limit exceeded, retry later"),
			"⚠ 模型限流中，已稍后自动恢复，请稍后重试"},
		{"bad request 400", errors.New("API request failed: Status: 400 Body: bad request"),
			"⚠ 模型返回异常请求，请稍后重试或切换模型"},
		{"bad request text", errors.New("400 Bad Request: invalid parameter"),
			"⚠ 模型返回异常请求，请稍后重试或切换模型"},
		{"generic fallback", errors.New("read tcp 1.2.3.4:443: connection reset by peer"),
			"⚠ 模型调用失败，本轮已中止"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := mapTurnErrorToUserMessage(tc.err)
			if !strings.HasPrefix(got, tc.wantFirst) {
				t.Fatalf("first line = %q, want prefix %q", got, tc.wantFirst)
			}
			if !strings.Contains(got, "\n> 原始错误：") {
				t.Fatalf("missing original-error line: %q", got)
			}
			if !strings.Contains(got, tc.err.Error()) {
				t.Fatalf("original error text not embedded: %q", got)
			}
		})
	}
}

func TestMapTurnErrorToUserMessageTruncatesLongErrors(t *testing.T) {
	long := strings.Repeat("x", 500)
	got := mapTurnErrorToUserMessage(errors.New(long))
	if !strings.Contains(got, strings.Repeat("x", 100)) {
		t.Fatalf("expected a large prefix of the original error to survive")
	}
	if strings.Contains(got, strings.Repeat("x", 200)) {
		t.Fatalf("original error not truncated: %d chars total", len(got))
	}
	if !strings.HasSuffix(got, "...") {
		t.Fatalf("expected Truncate's ellipsis marker at the end: %q", got[len(got)-20:])
	}
}
