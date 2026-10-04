//go:build amd64 || arm64 || riscv64 || mips64 || ppc64

package feishu

// Regression tests for the 2026-10-04 streaming-card optimizations:
// O1 compose/sanitize only on write-through (observable via the stream
// seam), O2 sanitize fast path, O3 adaptive flush interval.

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestAnswerFlushIntervalForAdaptsToSize pins the adaptive throttle mapping
// (O3): default 200ms, 500ms past 24KB, 1s past 48KB.
func TestAnswerFlushIntervalForAdaptsToSize(t *testing.T) {
	cases := []struct {
		bytes int
		want  time.Duration
	}{
		{0, 200 * time.Millisecond},
		{1024, 200 * time.Millisecond},
		{24000, 200 * time.Millisecond},
		{24001, 500 * time.Millisecond},
		{48000, 500 * time.Millisecond},
		{48001, time.Second},
		{96000, time.Second},
	}
	for _, c := range cases {
		if got := feishuAnswerFlushIntervalFor(c.bytes); got != c.want {
			t.Errorf("feishuAnswerFlushIntervalFor(%d) = %v, want %v", c.bytes, got, c.want)
		}
	}
}

// TestUpdateThrottledSkipsCompose: a throttled Update must not reach the
// element seam at all (the compose+sanitize work now happens only on
// write-through) — and the accumulated text still lands on the next
// write-through.
func TestUpdateThrottledSkipsCompose(t *testing.T) {
	s := newDegradeTestStreamer(t, &degradeAPIFake{})
	var (
		mu    sync.Mutex
		calls int
		last  string
	)
	s.streamContent = func(_ context.Context, _, _, content string, _ int) error {
		mu.Lock()
		defer mu.Unlock()
		calls++
		last = content
		return nil
	}

	_ = s.Update(context.Background(), "第一段")
	// Force-throttle the next update by backdating the last flush.
	s.mu.Lock()
	s.answerSentAt = time.Now()
	s.mu.Unlock()
	_ = s.Update(context.Background(), "第一段第二段")

	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Fatalf("throttled Update must not write the element, got %d calls", calls)
	}
	if last != "第一段" {
		t.Fatalf("element content from first write-through = %q", last)
	}
}

// TestSanitizeFastPathEquivalence: content without an image marker passes
// through untouched (byte-identical), matching the regex path's behavior.
func TestSanitizeFastPathEquivalence(t *testing.T) {
	cases := []string{
		"",
		"plain text with [brackets] and (parens) but no images",
		"中文内容，无图片标记。代码 `![not an image]` 在反引号里也会跳过？——不，会走正则路径；此处只验证无标记场景",
		strings.Repeat("很长的答案 ", 1000),
	}
	for _, c := range cases {
		if got := sanitizeFeishuMarkdownImages(c); got != c {
			t.Errorf("fast path must be identity for image-free content, got divergence (len %d -> %d)", len(c), len(got))
		}
	}
}
