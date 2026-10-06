package feishu

import (
	"context"
	"strings"
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

// TestBuildApprovalCardButtons pins the prompt card shape: three callback
// buttons with chat_id embedded (card.action.trigger carries no chat
// context), no streaming_mode, and the bounded preview in the body.
func TestBuildApprovalCardButtons(t *testing.T) {
	card := buildFeishuApprovalCard("chat-1", channels.ApprovalPrompt{
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
