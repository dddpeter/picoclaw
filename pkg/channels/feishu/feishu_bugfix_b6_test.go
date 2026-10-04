//go:build amd64 || arm64 || riscv64 || mips64 || ppc64

package feishu

// Regression tests for B6 (docs/design/2026-10-04-feishu-streaming-card-
// bug-review.zh.md): SendMedia partial delivery must be classified permanent
// so the manager never retries the batch (retries would duplicate the parts
// already in the chat); and every part caption is delivered, not just the
// first.

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/channels"
	"github.com/sipeed/picoclaw/pkg/media"
)

func newMediaTestChannel(t *testing.T) *FeishuChannel {
	t.Helper()
	ch, _ := newCardActionTestChannel()
	ch.SetRunning(true)
	ch.SetMediaStore(media.NewFileMediaStore())
	ch.sendMediaPartFn = func(context.Context, string, bus.MediaPart, media.MediaStore) error { return nil }
	return ch
}

// TestSendMediaPartialFailureIsPermanent: with one part already delivered,
// a later part failure must be classified ErrSendFailed (no manager retry).
func TestSendMediaPartialFailureIsPermanent(t *testing.T) {
	ch := newMediaTestChannel(t)
	calls := 0
	ch.sendMediaPartFn = func(context.Context, string, bus.MediaPart, media.MediaStore) error {
		calls++
		if calls == 1 {
			return nil // first part delivered
		}
		return fmt.Errorf("feishu send media: %w", channels.ErrTemporary)
	}

	_, err := ch.SendMedia(context.Background(), bus.OutboundMediaMessage{
		ChatID: "chat-b6",
		Parts:  []bus.MediaPart{{Type: "image", Ref: "r1"}, {Type: "image", Ref: "r2"}},
	})
	if err == nil {
		t.Fatal("expected error from failed second part")
	}
	if !errors.Is(err, channels.ErrSendFailed) {
		t.Fatalf("partial delivery must be permanent (ErrSendFailed) so it is not retried, got %v", err)
	}
}

// TestSendMediaFirstPartFailureStaysRetryable: nothing delivered yet → the
// transient classification survives for a safe retry.
func TestSendMediaFirstPartFailureStaysRetryable(t *testing.T) {
	ch := newMediaTestChannel(t)
	ch.sendMediaPartFn = func(context.Context, string, bus.MediaPart, media.MediaStore) error {
		return fmt.Errorf("feishu send media: %w", channels.ErrTemporary)
	}
	_, err := ch.SendMedia(context.Background(), bus.OutboundMediaMessage{
		ChatID: "chat-b6",
		Parts:  []bus.MediaPart{{Type: "image", Ref: "r1"}},
	})
	if !errors.Is(err, channels.ErrTemporary) {
		t.Fatalf("first-part failure must stay retryable, got %v", err)
	}
	if errors.Is(err, channels.ErrSendFailed) {
		t.Fatalf("first-part failure must not be classified permanent, got %v", err)
	}
}

// TestSendMediaCaptionFailureAfterDeliveryIsPermanent: all media delivered,
// caption text fails → permanent, never retry the batch.
func TestSendMediaCaptionFailureAfterDeliveryIsPermanent(t *testing.T) {
	ch := newMediaTestChannel(t)
	ch.sendTextFn = func(context.Context, string, string) (string, error) {
		return "", fmt.Errorf("feishu send text: %w", channels.ErrTemporary)
	}
	_, err := ch.SendMedia(context.Background(), bus.OutboundMediaMessage{
		ChatID: "chat-b6",
		Parts:  []bus.MediaPart{{Type: "image", Ref: "r1", Caption: "图注"}},
	})
	if !errors.Is(err, channels.ErrSendFailed) {
		t.Fatalf("caption failure after delivery must be permanent, got %v", err)
	}
}

// TestSendMediaJoinsAllCaptions: parts 2+ captions are delivered too.
func TestSendMediaJoinsAllCaptions(t *testing.T) {
	ch := newMediaTestChannel(t)
	var sentCaption string
	ch.sendTextFn = func(_ context.Context, _, text string) (string, error) {
		sentCaption = text
		return "om_cap", nil
	}
	_, err := ch.SendMedia(context.Background(), bus.OutboundMediaMessage{
		ChatID: "chat-b6",
		Parts: []bus.MediaPart{
			{Type: "image", Ref: "r1", Caption: "第一张"},
			{Type: "image", Ref: "r2", Caption: "第二张"},
		},
	})
	if err != nil {
		t.Fatalf("SendMedia: %v", err)
	}
	if sentCaption != "第一张\n第二张" {
		t.Fatalf("captions must all be joined, got %q", sentCaption)
	}
}
