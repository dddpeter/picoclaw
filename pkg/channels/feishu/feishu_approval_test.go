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

// sealRecorder captures seal calls through the seam used by the click handler.
type sealRecorder struct {
	mu     sync.Mutex
	calls  [][3]any // chatID, messageID, approved
}

func (r *sealRecorder) fn(ctx context.Context, chatID, messageID string, approved bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, [3]any{chatID, messageID, approved})
	return nil
}

func (r *sealRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

// TestHandleCardActionApprovalSealsViaQID pins the production regression from
// 2026-10-09: with no callback context at all, the click still seals exactly
// the card recorded under the button's qid (approve → success verdict, deny →
// danger verdict), and the response carries the sealed card so the buttons
// disappear atomically even if the PATCH cannot run.
func TestHandleCardActionApprovalSealsViaQID(t *testing.T) {
	for _, tc := range []struct {
		cmd      string
		approved bool
	}{
		{feishuApprovalApproveCmd, true},
		{feishuApprovalAlwaysCmd, true},
		{feishuApprovalDenyCmd, false},
	} {
		ch, mb := newCardActionTestChannel()
		rec := &sealRecorder{}
		ch.sealCardFn = rec.fn
		ch.recordApprovalCard("qid-live", "chat-1", "om_recorded")

		resp, err := ch.handleCardAction(context.Background(),
			approvalClickEventNoQIDContext("chat-1", "qid-live", tc.cmd))
		if err != nil {
			t.Fatalf("%s: handleCardAction: %v", tc.cmd, err)
		}
		if rec.count() != 1 {
			t.Fatalf("%s: expected exactly one seal call, got %d", tc.cmd, rec.count())
		}
		call := rec.calls[0]
		if call[0] != "chat-1" || call[1] != "om_recorded" || call[2] != tc.approved {
			t.Fatalf("%s: seal called with (%v), want (chat-1 om_recorded %v)", tc.cmd, call, tc.approved)
		}
		if resp.Card == nil || resp.Card.Type != "raw" {
			t.Fatalf("%s: response must carry the raw sealed card, got %+v", tc.cmd, resp.Card)
		}
		sealed, ok := resp.Card.Data.(map[string]any)
		if !ok {
			t.Fatalf("%s: sealed card data must be a card object, got %T", tc.cmd, resp.Card.Data)
		}
		for _, el := range sealed["body"].(map[string]any)["elements"].([]any) {
			if m, ok := el.(map[string]any); ok && m["tag"] == "button" {
				t.Fatalf("%s: response sealed card must not carry buttons", tc.cmd)
			}
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
// consumed by the first click: a double-click race must not seal twice via
// the registry (the second click falls back to context, then the response
// card).
func TestHandleCardActionApprovalSealConsumesQID(t *testing.T) {
	ch, _ := newCardActionTestChannel()
	rec := &sealRecorder{}
	ch.sealCardFn = rec.fn
	ch.recordApprovalCard("qid-race", "chat-1", "om_recorded")

	if _, err := ch.handleCardAction(context.Background(),
		approvalClickEventNoQIDContext("chat-1", "qid-race", feishuApprovalApproveCmd)); err != nil {
		t.Fatalf("first click: %v", err)
	}
	// Second click with no context and the now-consumed qid: no registry hit.
	chatID, msgID := ch.lookupApprovalCard("qid-race")
	if chatID != "" || msgID != "" {
		t.Fatalf("qid entry must be consumed by the first click, got (%q,%q)", chatID, msgID)
	}
	if rec.count() != 1 {
		t.Fatalf("expected exactly one seal call, got %d", rec.count())
	}
}

// TestHandleCardActionApprovalCardInResponseWithoutTarget pins the floor of
// the fix: even when the card cannot be located at all (no qid match, no
// context), the callback response still carries the sealed card so the
// buttons disappear for the clicker.
func TestHandleCardActionApprovalCardInResponseWithoutTarget(t *testing.T) {
	ch, mb := newCardActionTestChannel()
	rec := &sealRecorder{}
	ch.sealCardFn = rec.fn

	resp, err := ch.handleCardAction(context.Background(),
		approvalClickEventNoQIDContext("chat-1", "qid-unknown", feishuApprovalApproveCmd))
	if err != nil {
		t.Fatalf("handleCardAction: %v", err)
	}
	if rec.count() != 0 {
		t.Fatalf("no seal PATCH expected without a located card, got %d calls", rec.count())
	}
	if resp.Card == nil {
		t.Fatal("response must still carry the sealed card")
	}
	select {
	case <-mb.InboundChan():
	case <-time.After(2 * time.Second):
		t.Fatal("no inbound message published")
	}
}
