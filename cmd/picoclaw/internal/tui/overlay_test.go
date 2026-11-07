package tui

import (
	"testing"

	"github.com/sipeed/picoclaw/pkg/picoclient"
)

// 评审回归 1：内容型浮层（help/usage）滚动越过末尾不得 panic
// （move() 无内容高度可循，越界偏移须在渲染层收敛）。
func TestOverlayScrollPastEndClamps(t *testing.T) {
	var o overlayState
	o.open(overlayHelp)
	for i := 0; i < 30; i++ {
		o.move(+10, 10)
	}
	// 不 panic 即通过；且窗口应停在内容末尾而非空白。
	body := renderOverlayBox(&o, overlayRenderCtx{}, 80, 10)
	if len(body) == 0 {
		t.Fatal("body should not be empty")
	}
	if o.scroll < 0 {
		t.Fatalf("scroll clamped non-negative, got %d", o.scroll)
	}
	// 回滚到顶后再拉一格仍正常。
	o.move(-10, 10)
	_ = renderOverlayBox(&o, overlayRenderCtx{}, 80, 10)
}

// 评审回归 2：usage 浮层同样收敛。
func TestOverlayUsageScrollClamps(t *testing.T) {
	var o overlayState
	o.open(overlayUsage)
	rc := overlayRenderCtx{usage: &picoclient.ContextUsage{UsedTokens: 1000, TotalTokens: 4000, UsedPercent: 25}}
	for i := 0; i < 10; i++ {
		o.move(+50, 8)
	}
	_ = renderOverlayBox(&o, rc, 80, 8)
}
