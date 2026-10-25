package agent

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/utils"
)

// mapTurnErrorToUserMessage translates a failed turn's error into a
// user-facing Chinese message with actionable guidance. Raw provider error
// strings are unreadable for chat users; errors.As on the fallback aggregate
// plus string matching pick the hint that says what (if anything) to do.
// The original error is always appended (truncated) for diagnosis.
func mapTurnErrorToUserMessage(err error) string {
	msg := err.Error()
	lower := strings.ToLower(msg)

	var text string
	var exhausted *providers.FallbackExhaustedError
	switch {
	case errors.As(err, &exhausted):
		// Must run before string matching: the aggregate's own message
		// embeds per-attempt errors that contain "429"/"rate limit" etc.
		text = "⚠ 模型暂时不可用：所有候选模型均失败或限流"
		if remaining := maxCooldownRemaining(exhausted.Attempts); remaining > 0 {
			text += fmt.Sprintf("，最早约 %d 分钟后可恢复", ceilMinutes(remaining))
		}
	case strings.Contains(lower, "context window"):
		text = "⚠ 上下文超限且自动压缩失败，建议发送 /new 开启新会话"
	case containsAny(lower, "401", "unauthorized", "invalid api key"):
		text = "⚠ 模型鉴权失败，请检查 API Key 配置"
	case containsAny(lower, "429", "rate limit"):
		text = "⚠ 模型限流中，已稍后自动恢复，请稍后重试"
	case containsAny(lower, "400", "bad request"):
		text = "⚠ 模型返回异常请求，请稍后重试或切换模型"
	default:
		text = "⚠ 模型调用失败，本轮已中止"
	}
	return text + "\n> 原始错误：" + utils.Truncate(msg, 160)
}

// maxCooldownRemaining returns the longest remaining cooldown window across
// attempts, 0 when none is cooling down.
func maxCooldownRemaining(attempts []providers.FallbackAttempt) time.Duration {
	var longest time.Duration
	for _, a := range attempts {
		longest = max(longest, a.CooldownRemaining)
	}
	return longest
}

// ceilMinutes rounds a duration up to whole minutes (90s → 2).
func ceilMinutes(d time.Duration) int {
	return int((d + time.Minute - 1) / time.Minute)
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
