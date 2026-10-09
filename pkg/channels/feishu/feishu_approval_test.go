package feishu

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"

	"github.com/sipeed/picoclaw/pkg/channels"
)

// This file pins the native approval card (agentscope-go borrowing §一
// interaction upgrade) and its click routing.

func approvalClickEvent(chatID, cmd string) *callback.CardActionTriggerEvent {
	return &callback.CardActionTriggerEvent{
		Event: &callback.CardActionTriggerRequest{
			Operator: &callback.Operator{OpenID: "ou_test_operator"},
			Action: &callback.CallBackAction{
				Value: map[string]interface{}{"cmd": cmd, "chat_id": chatID},
			},
			Context: &callback.Context{OpenMessageID: "om_seal_me", OpenChatID: chatID},
		},
	}
}

// approvalClickEventNoQIDContext reproduces the production frame shape that
// broke sealing on 2026-10-09: button value carries qid but the callback
// event has no context at all (no message id).
func approvalClickEventNoQIDContext(chatID, qid, cmd string) *callback.CardActionTriggerEvent {
	return &callback.CardActionTriggerEvent{
		Event: &callback.CardActionTriggerRequest{
			Operator: &callback.Operator{OpenID: "ou_test_operator"},
			Action: &callback.CallBackAction{
				Value: map[string]interface{}{"cmd": cmd, "chat_id": chatID, "qid": qid},
			},
		},
	}
}

// TestBuildApprovalCardButtons pins the prompt card shape: three callback
// buttons with chat_id embedded (card.action.trigger carries no chat
// context), no streaming_mode, and the bounded preview in the body.
func TestBuildApprovalCardButtons(t *testing.T) {
	card := buildFeishuApprovalCard("chat-1", "qid-test", channels.ApprovalPrompt{
		SessionKey: "sess",
		Tool:       "exec",
		Preview:    "git push origin main",
		Timeout:    5 * time.Minute,
	})
	raw := string(mustJSON(card))

	if strings.Contains(raw, "streaming_mode") {
		t.Error("approval card must not carry streaming_mode")
	}
	if !strings.Contains(raw, `"schema":"2.0"`) {
		t.Error("approval card must be schema 2.0")
	}

	buttons := 0
	elements := card["body"].(map[string]any)["elements"].([]any)
	for _, el := range elements {
		m, ok := el.(map[string]any)
		if !ok || m["tag"] != "button" {
			continue
		}
		buttons++
		behaviors, _ := m["behaviors"].([]any)
		if len(behaviors) != 1 {
			t.Fatalf("button must carry exactly one callback behavior: %v", m)
		}
		value := behaviors[0].(map[string]any)["value"].(map[string]any)
		if value["chat_id"] != "chat-1" {
			t.Fatalf("button value must embed chat_id, got %v", value)
		}
		if value["qid"] != "qid-test" {
			t.Fatalf("button value must embed the card qid, got %v", value)
		}
	}
	if buttons != 3 {
		t.Fatalf("expected exactly 3 buttons (approve/always/deny), got %d", buttons)
	}

	// The sealed card has no buttons at all.
	sealed := buildFeishuApprovalSealedCard(true, "")
	for _, el := range sealed["body"].(map[string]any)["elements"].([]any) {
		if m, ok := el.(map[string]any); ok && m["tag"] == "button" {
			t.Fatal("sealed card must not carry buttons")
		}
	}
}

// TestFeishuApprovalSynthContent pins the button→protocol-text mapping.
func TestFeishuApprovalSynthContent(t *testing.T) {
	cases := map[string]string{
		feishuApprovalApproveCmd: "/approve",
		feishuApprovalAlwaysCmd:  "/approve always",
		feishuApprovalDenyCmd:    "/deny",
	}
	for cmd, want := range cases {
		got, ok := feishuApprovalSynthContent(cmd)
		if !ok || got != want {
			t.Fatalf("synth(%q) = (%q,%v), want %q", cmd, got, ok, want)
		}
	}
	if _, ok := feishuApprovalSynthContent("stop"); ok {
		t.Fatal("stop must not map through the approval synth")
	}
}

// TestHandleCardActionApprovalRoutes pins the click path: allowlisted clicks
// synthesize the approval protocol text through the normal inbound pipeline
// (reusing tryHandleApprovalReply downstream) and answer with a toast.
func TestHandleCardActionApprovalRoutes(t *testing.T) {
	cases := []struct {
		cmd      string
		wantText string
	}{
		{feishuApprovalApproveCmd, "/approve"},
		{feishuApprovalAlwaysCmd, "/approve always"},
		{feishuApprovalDenyCmd, "/deny"},
	}
	for _, tc := range cases {
		ch, mb := newCardActionTestChannel()
		resp, err := ch.handleCardAction(context.Background(), approvalClickEvent("chat-1", tc.cmd))
		if err != nil {
			t.Fatalf("handleCardAction(%s): %v", tc.cmd, err)
		}
		if resp == nil || resp.Toast == nil || resp.Toast.Type != "success" {
			t.Fatalf("%s: expected success toast, got %+v", tc.cmd, resp)
		}
		select {
		case msg := <-mb.InboundChan():
			if msg.Content != tc.wantText {
				t.Fatalf("%s: expected %q inbound, got %q", tc.cmd, tc.wantText, msg.Content)
			}
			if msg.ChatID != "chat-1" {
				t.Fatalf("%s: expected chat-1, got %q", tc.cmd, msg.ChatID)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("%s: no inbound message published", tc.cmd)
		}
	}
}

// TestHandleCardActionApprovalRequiresChat pins the missing-chat_id refusal
// (value must embed it at render time).
func TestHandleCardActionApprovalRequiresChat(t *testing.T) {
	ch, mb := newCardActionTestChannel()
	event := approvalClickEvent("", feishuApprovalApproveCmd)
	resp, err := ch.handleCardAction(context.Background(), event)
	if err != nil {
		t.Fatalf("handleCardAction: %v", err)
	}
	if resp == nil || resp.Toast == nil || resp.Toast.Type != "error" {
		t.Fatalf("expected error toast for missing chat_id, got %+v", resp)
	}
	select {
	case msg := <-mb.InboundChan():
		t.Fatalf("missing chat_id must not publish inbound, got %+v", msg)
	case <-time.After(200 * time.Millisecond):
	}
}

// sealRecorder captures seal calls through the seam used by the click handler
// (cardID, sealedJSON, sequence).
type sealRecorder struct {
	mu    sync.Mutex
	calls []struct {
		cardID string
		card   string
		seq    int
	}
}

func (r *sealRecorder) fn(ctx context.Context, cardID, cardJSON string, seq int) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, struct {
		cardID string
		card   string
		seq    int
	}{cardID, cardJSON, seq})
	return nil
}

func (r *sealRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

// TestHandleCardActionApprovalSealsViaQID pins the production behavior after
// the 2026-10-09 sealing rework: the click seals exactly the card recorded
// under the button's qid via one CardKit full-card update (sequence 1), with
// no dependency on the callback context at all.
func TestHandleCardActionApprovalSealsViaQID(t *testing.T) {
	for _, tc := range []struct {
		cmd     string
		verdict string
	}{
		{feishuApprovalApproveCmd, "已批准"},
		{feishuApprovalAlwaysCmd, "已批准"},
		{feishuApprovalDenyCmd, "已拒绝"},
	} {
		ch, mb := newCardActionTestChannel()
		rec := &sealRecorder{}
		ch.sealCardFn = rec.fn
		ch.recordApprovalCard("qid-live", "chat-1", "om_recorded", "card_rec_1")

		resp, err := ch.handleCardAction(context.Background(),
			approvalClickEventNoQIDContext("chat-1", "qid-live", tc.cmd))
		if err != nil {
			t.Fatalf("%s: handleCardAction: %v", tc.cmd, err)
		}
		if rec.count() != 1 {
			t.Fatalf("%s: expected exactly one seal call, got %d", tc.cmd, rec.count())
		}
		call := rec.calls[0]
		if call.cardID != "card_rec_1" || call.seq != 1 {
			t.Fatalf("%s: seal called with (card=%q seq=%d), want (card_rec_1 seq=1)", tc.cmd, call.cardID, call.seq)
		}
		if !strings.Contains(call.card, tc.verdict) {
			t.Fatalf("%s: sealed card must carry the %q verdict, got: %.200s", tc.cmd, tc.verdict, call.card)
		}
		if strings.Contains(call.card, "behaviors") {
			t.Fatalf("%s: sealed card must not carry buttons, got: %.200s", tc.cmd, call.card)
		}
		if resp.Card != nil {
			t.Fatalf("%s: response must be toast-only (a response card merge fights the CardKit replace)", tc.cmd)
		}
		select {
		case msg := <-mb.InboundChan():
			if msg.ChatID != "chat-1" {
				t.Fatalf("%s: expected chat-1 inbound, got %q", tc.cmd, msg.ChatID)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("%s: no inbound message published", tc.cmd)
		}
	}
}

// TestHandleCardActionApprovalSealConsumesQID pins that the registry entry is
// consumed by the first click: a double-click race must not seal twice.
func TestHandleCardActionApprovalSealConsumesQID(t *testing.T) {
	ch, _ := newCardActionTestChannel()
	rec := &sealRecorder{}
	ch.sealCardFn = rec.fn
	ch.recordApprovalCard("qid-race", "chat-1", "om_recorded", "card_rec_1")

	if _, err := ch.handleCardAction(context.Background(),
		approvalClickEventNoQIDContext("chat-1", "qid-race", feishuApprovalApproveCmd)); err != nil {
		t.Fatalf("first click: %v", err)
	}
	if ref := ch.takeApprovalCardRef("qid-race"); ref != nil {
		t.Fatalf("qid entry must be consumed by the first click, got %+v", ref)
	}
	if rec.count() != 1 {
		t.Fatalf("expected exactly one seal call, got %d", rec.count())
	}
}

// TestHandleCardActionApprovalSealSkipsInlineCard pins the fallback shape: a
// prompt whose CardKit create failed is sent inline (no card_id recorded) —
// its clicks still route the reply but never attempt a seal.
func TestHandleCardActionApprovalSealSkipsInlineCard(t *testing.T) {
	ch, mb := newCardActionTestChannel()
	rec := &sealRecorder{}
	ch.sealCardFn = rec.fn
	ch.recordApprovalCard("qid-inline", "chat-1", "om_inline", "") // inline fallback: no card_id

	resp, err := ch.handleCardAction(context.Background(),
		approvalClickEventNoQIDContext("chat-1", "qid-inline", feishuApprovalApproveCmd))
	if err != nil {
		t.Fatalf("handleCardAction: %v", err)
	}
	if rec.count() != 0 {
		t.Fatalf("no seal call expected for an inline card, got %d", rec.count())
	}
	if resp == nil || resp.Toast == nil || resp.Toast.Type != "success" {
		t.Fatalf("expected success toast, got %+v", resp)
	}
	select {
	case <-mb.InboundChan():
	case <-time.After(2 * time.Second):
		t.Fatal("no inbound message published")
	}
}
