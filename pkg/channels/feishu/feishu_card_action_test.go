package feishu

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/channels"
)

// stopButtonJSON walks a card for the stop button and returns its callback
// value map (nil when absent).
func stopButtonJSON(t *testing.T, card map[string]any) map[string]any {
	t.Helper()
	data, err := json.Marshal(card)
	if err != nil {
		t.Fatalf("marshal card: %v", err)
	}
	if !strings.Contains(string(data), "⏹ 停止") {
		t.Errorf("card should carry the stop button, got: %.300s", string(data))
	}
	body, _ := card["body"].(map[string]any)
	elements, _ := body["elements"].([]any)
	for _, elem := range elements {
		m, ok := elem.(map[string]any)
		if !ok || m["tag"] != "button" {
			continue
		}
		for _, b := range m["behaviors"].([]any) {
			bm := b.(map[string]any)
			if bm["type"] == "callback" {
				vm, _ := bm["value"].(map[string]any)
				return vm
			}
		}
	}
	return nil
}

func TestStreamingCardsCarryStopButtonWithChatContext(t *testing.T) {
	const chatID = "oc_stop_button_test"
	value := stopButtonJSON(t, buildFeishuStreamingCard(chatID))
	if value == nil || value["cmd"] != feishuStopCmd || value["chat_id"] != chatID {
		t.Fatalf("initial card stop button value wrong: %v", value)
	}

	state := &feishuStreamState{Tools: nil}
	value = stopButtonJSON(t, buildFeishuRefreshCard(state, "", feishuPhaseThinking, feishuPanelTextBudget, "", chatID))
	if value == nil || value["cmd"] != feishuStopCmd || value["chat_id"] != chatID {
		t.Fatalf("refresh card stop button value wrong: %v", value)
	}

	// The sealed card must NOT offer a stop button: the turn is over.
	final := buildFeishuFinalCard(&feishuStreamState{}, "done", false, time.Second, "")
	finalJSON, _ := json.Marshal(final)
	if strings.Contains(string(finalJSON), "⏹ 停止") {
		t.Error("final card should not carry the stop button")
	}
}

func newCardActionTestChannel() (*FeishuChannel, *bus.MessageBus) {
	mb := bus.NewMessageBus()
	ch := &FeishuChannel{
		BaseChannel: channels.NewBaseChannel("feishu", nil, mb, []string{"*"}),
	}
	return ch, mb
}

func stopClickEvent(chatID string) *callback.CardActionTriggerEvent {
	return &callback.CardActionTriggerEvent{
		Event: &callback.CardActionTriggerRequest{
			Operator: &callback.Operator{OpenID: "ou_test_operator"},
			Action: &callback.CallBackAction{
				Value: map[string]interface{}{"cmd": feishuStopCmd, "chat_id": chatID},
			},
		},
	}
}

func TestHandleCardActionStopEnqueuesStopCommand(t *testing.T) {
	ch, mb := newCardActionTestChannel()
	s := &feishuCardStreamer{ch: ch, chatID: "chat-live", lastAt: time.Now()}
	ch.streams.Store("chat-live", s)

	resp, err := ch.handleCardAction(context.Background(), stopClickEvent("chat-live"))
	if err != nil {
		t.Fatalf("handleCardAction returned error: %v", err)
	}
	if resp == nil || resp.Toast == nil || resp.Toast.Type != "success" {
		t.Fatalf("expected success toast, got: %+v", resp)
	}

	select {
	case msg := <-mb.InboundChan():
		if msg.Content != "/stop" {
			t.Fatalf("expected /stop inbound, got %q", msg.Content)
		}
		if msg.ChatID != "chat-live" {
			t.Fatalf("expected chat-live, got %q", msg.ChatID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no inbound message published within 2s")
	}
}

func TestHandleCardActionStopWithoutActiveTurn(t *testing.T) {
	ch, mb := newCardActionTestChannel()

	resp, err := ch.handleCardAction(context.Background(), stopClickEvent("chat-idle"))
	if err != nil {
		t.Fatalf("handleCardAction returned error: %v", err)
	}
	if resp == nil || resp.Toast == nil || resp.Toast.Type != "info" {
		t.Fatalf("expected info toast for idle chat, got: %+v", resp)
	}
	select {
	case msg := <-mb.InboundChan():
		t.Fatalf("no /stop should be published for an idle chat, got %+v", msg)
	case <-time.After(200 * time.Millisecond):
	}
}

func TestHandleCardActionUnknownCommand(t *testing.T) {
	ch, mb := newCardActionTestChannel()
	event := &callback.CardActionTriggerEvent{
		Event: &callback.CardActionTriggerRequest{
			Operator: &callback.Operator{OpenID: "ou_test_operator"},
			Action:   &callback.CallBackAction{Value: map[string]interface{}{"cmd": "spike_ping"}},
		},
	}
	resp, err := ch.handleCardAction(context.Background(), event)
	if err != nil {
		t.Fatalf("handleCardAction returned error: %v", err)
	}
	if resp == nil || resp.Toast == nil {
		t.Fatalf("expected toast response, got: %+v", resp)
	}
	select {
	case msg := <-mb.InboundChan():
		t.Fatalf("unknown command must not publish inbound, got %+v", msg)
	case <-time.After(200 * time.Millisecond):
	}
}

func TestHandleCardActionSealedStreamerRefusesStop(t *testing.T) {
	ch, mb := newCardActionTestChannel()
	s := &feishuCardStreamer{ch: ch, chatID: "chat-done", done: true, lastAt: time.Now()}
	ch.streams.Store("chat-done", s)

	resp, _ := ch.handleCardAction(context.Background(), stopClickEvent("chat-done"))
	if resp == nil || resp.Toast == nil || resp.Toast.Type != "info" {
		t.Fatalf("expected info toast for sealed card click, got: %+v", resp)
	}
	select {
	case msg := <-mb.InboundChan():
		t.Fatalf("sealed card click must not publish /stop, got %+v", msg)
	case <-time.After(200 * time.Millisecond):
	}
}
