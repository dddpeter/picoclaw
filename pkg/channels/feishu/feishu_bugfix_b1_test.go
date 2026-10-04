//go:build amd64 || arm64 || riscv64 || mips64 || ppc64

package feishu

// Regression tests for the 2026-10-04 bug review (docs/design/
// 2026-10-04-feishu-streaming-card-bug-review.zh.md): B1 oversized-final
// answer, B4 byte/字 mismatch, B5 paren URL truncation.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/sipeed/picoclaw/pkg/bus"
)

// --- B4: truncation suffix counts runes, not bytes ---

func TestTruncateReasoningCountsRunes(t *testing.T) {
	text := strings.Repeat("推", 1000) // 3000 bytes, 1000 runes
	got := truncateFeishuReasoning(text)
	if !strings.Contains(got, "共 1000 字") {
		t.Fatalf("suffix must count runes (1000), got %q", got[len(got)-40:])
	}
}

// --- B5: image URLs with balanced parens survive sanitization ---

func TestSanitizeImagesWithParenURL(t *testing.T) {
	in := "前 ![img](https://x.com/a_(b).png) 中 ![ok](img_v3_abc) 后"
	out := sanitizeFeishuMarkdownImages(in)
	if !strings.Contains(out, "[img](https://x.com/a_(b).png)") {
		t.Errorf("paren URL must degrade to a complete link, got %q", out)
	}
	if !strings.Contains(out, "![ok](img_v3_abc)") {
		t.Errorf("real image_key must stay an image, got %q", out)
	}
	// Two paren URLs on one line must not fuse into one match.
	two := "![a](u_(1).png) mid ![b](u_(2).png)"
	gotTwo := sanitizeFeishuMarkdownImages(two)
	if !strings.Contains(gotTwo, "[a](u_(1).png)") || !strings.Contains(gotTwo, "[b](u_(2).png)") {
		t.Errorf("adjacent paren URLs must match separately, got %q", gotTwo)
	}
}

// --- B1: oversized final answers seal the card and split the remainder ---

// sizeGuardAPIFake enforces Feishu's real 30KB card-JSON cap locally and
// records sealed card sizes, close calls and delivered remainder parts.
type sizeGuardAPIFake struct {
	degradeAPIFake

	mu          sync.Mutex
	updateSizes []int
	lastCard    map[string]any
	closeCalls  int
	delivered   []string
	rejectFirst error // scripted failure for the first updateCard call
}

func (f *sizeGuardAPIFake) updateCard(_ context.Context, _ string, card map[string]any, _ int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.rejectFirst != nil {
		err := f.rejectFirst
		f.rejectFirst = nil
		return err
	}
	data, _ := json.Marshal(card)
	f.updateSizes = append(f.updateSizes, len(data))
	f.lastCard = card
	if len(data) > 30000 {
		return fmt.Errorf("feishu cardkit update: card json %d bytes exceeds Feishu 30KB limit", len(data))
	}
	return nil
}

func (f *sizeGuardAPIFake) closeStreaming(context.Context, string, map[string]any, int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closeCalls++
	return nil
}

func newSizeGuardStreamer(t *testing.T, fake *sizeGuardAPIFake) *feishuCardStreamer {
	t.Helper()
	s := newDegradeTestStreamer(t, &fake.degradeAPIFake)
	s.updateCard = fake.updateCard
	s.closeStreaming = fake.closeStreaming
	s.deliverPart = func(_ context.Context, _, content string) error {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		fake.delivered = append(fake.delivered, content)
		return nil
	}
	return s
}

func sealedAnswerElementContent(t *testing.T, card map[string]any) string {
	t.Helper()
	body, _ := card["body"].(map[string]any)
	elements, _ := body["elements"].([]any)
	for _, elem := range elements {
		if m, ok := elem.(map[string]any); ok && m["element_id"] == feishuAnswerElementID {
			content, _ := m["content"].(string)
			return content
		}
	}
	return ""
}

// TestFinalizeOversizedAnswerSealsAndSplits: a >30KB answer must seal the
// card (clamped, within the cap), close streaming mode, and deliver the
// remainder as follow-up messages — never strand the card in streaming mode.
func TestFinalizeOversizedAnswerSealsAndSplits(t *testing.T) {
	fake := &sizeGuardAPIFake{}
	s := newSizeGuardStreamer(t, fake)

	huge := strings.Repeat("字", 12000) // 36KB composed content
	if err := s.Finalize(context.Background(), huge); err != nil {
		t.Fatalf("Finalize on oversized answer must seal via clamp+split, got %v", err)
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.updateSizes) == 0 {
		t.Fatal("no card update was sent")
	}
	for i, size := range fake.updateSizes {
		if size > 30000 {
			t.Errorf("sealed card #%d json = %d bytes, exceeds Feishu 30KB cap", i, size)
		}
	}
	if content := sealedAnswerElementContent(t, fake.lastCard); !strings.Contains(content, "已截断") {
		t.Errorf("clamped card must carry a truncation note, answer element: %.120s", content)
	}
	if fake.closeCalls == 0 {
		t.Error("closeStreaming never called — card would stay in streaming mode")
	}
	if len(fake.delivered) == 0 {
		t.Fatal("oversized remainder was not delivered as follow-up messages")
	}
	joined := strings.Join(fake.delivered, "")
	trimmed := strings.TrimSpace(joined)
	if !strings.HasSuffix(huge, trimmed[len(trimmed)-30:]) {
		t.Errorf("delivered remainder must cover the answer tail, got %d chars", len([]rune(joined)))
	}
}

// TestFinalizeFallsBackToMinimalSealWhenCardRejected: when the regular seal
// card is rejected, Finalize retries once with a minimal card so the card is
// still sealed instead of being stranded.
func TestFinalizeFallsBackToMinimalSealWhenCardRejected(t *testing.T) {
	fake := &sizeGuardAPIFake{rejectFirst: fmt.Errorf("card rejected (scripted)")}
	s := newSizeGuardStreamer(t, fake)

	if err := s.Finalize(context.Background(), "正常答案"); err != nil {
		t.Fatalf("Finalize must survive a rejected seal card via minimal seal, got %v", err)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.updateSizes) != 1 {
		t.Fatalf("sealed card updates = %d, want 1 (first rejected, minimal retry sealed)", len(fake.updateSizes))
	}
	if fake.updateSizes[0] > 30000 {
		t.Errorf("minimal seal card json = %d bytes, exceeds cap", fake.updateSizes[0])
	}
	if fake.closeCalls == 0 {
		t.Error("closeStreaming never called after minimal seal")
	}
}

// TestCancelOversizedAnswerClampsInCard: cancel clamps the answer in-card
// (with a note that mentions no follow-ups) and never split-delivers.
func TestCancelOversizedAnswerClampsInCard(t *testing.T) {
	fake := &sizeGuardAPIFake{}
	s := newSizeGuardStreamer(t, fake)
	s.answer = strings.Repeat("字", 12000) // 36KB partial answer
	s.state.Tools = append(s.state.Tools, bus.ToolStep{Tool: "demo", Result: "done"}) // panel → not an empty card

	s.CancelWithReason(context.Background(), "stop_command")
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.updateSizes) == 0 {
		t.Fatal("cancel did not seal the card")
	}
	for i, size := range fake.updateSizes {
		if size > 30000 {
			t.Errorf("cancel card #%d json = %d bytes, exceeds cap", i, size)
		}
	}
	if content := sealedAnswerElementContent(t, fake.lastCard); !strings.Contains(content, "已截断") {
		t.Errorf("clamped cancel card must carry a truncation note, got %.120s", content)
	}
	if len(fake.delivered) != 0 {
		t.Errorf("cancel must not split-deliver, got %d parts", len(fake.delivered))
	}
}

// TestRefreshAnswerSnapshotClamped: mid-stream full-card refreshes clamp the
// answer snapshot so the panel never freezes on Feishu's 30KB cap.
func TestRefreshAnswerSnapshotClamped(t *testing.T) {
	state := &feishuStreamState{}
	card := buildFeishuRefreshCard(state, strings.Repeat("字", 12000), feishuPhaseAnswer, feishuPanelTextBudget, "")
	data, _ := json.Marshal(card)
	if len(data) > 30000 {
		t.Fatalf("refresh card json = %d bytes, exceeds Feishu 30KB cap", len(data))
	}
	content := sealedAnswerElementContent(t, card)
	if !strings.Contains(content, "以封卡为准") {
		t.Errorf("clamped refresh snapshot must note where the full text lives, got tail %.80s", content[len(content)-80:])
	}
}
