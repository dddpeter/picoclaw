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

// findStopButton walks a card (any nesting depth — the button lives inside a
// column set) and returns the stop button element, or nil.
func findStopButton(node any) map[string]any {
	switch v := node.(type) {
	case map[string]any:
		if v["tag"] == "button" && strings.Contains(string(mustJSON(v)), "⏹ 停止") {
			return v
		}
		for _, child := range v {
			if found := findStopButton(child); found != nil {
				return found
			}
		}
	case []any:
		for _, child := range v {
			if found := findStopButton(child); found != nil {
				return found
			}
		}
	}
	return nil
}

func mustJSON(v any) []byte {
	data, _ := json.Marshal(v)
	return data
}

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
	btn := findStopButton(card)
	if btn == nil {
		return nil
	}
	for _, b := range btn["behaviors"].([]any) {
		bm, _ := b.(map[string]any)
		if bm["type"] == "callback" {
			vm, _ := bm["value"].(map[string]any)
			return vm
		}
	}
	return nil
}

// assertNarrowStopButton verifies the stop button is wrapped in a 1:2
// column set (~1/3 row width) instead of spanning the full card width.
func assertNarrowStopButton(t *testing.T, card map[string]any) {
	t.Helper()
	body, _ := card["body"].(map[string]any)
	elements, _ := body["elements"].([]any)
	for _, elem := range elements {
		m, ok := elem.(map[string]any)
		if !ok || m["tag"] != "column_set" {
			continue
		}
		columns, _ := m["columns"].([]any)
		if len(columns) != 2 {
			continue
		}
		first, _ := columns[0].(map[string]any)
		second, _ := columns[1].(map[string]any)
		if first["weight"] == 1 && second["weight"] == 2 && findStopButton(first) != nil {
			return
		}
	}
	t.Errorf("stop button should sit in a 1:2 column set, got: %.400s", string(mustJSON(card)))
}

func TestStreamingCardsCarryStopButtonWithChatContext(t *testing.T) {
	const chatID = "oc_stop_button_test"
	value := stopButtonJSON(t, buildFeishuStreamingCard(chatID))
	if value == nil || value["cmd"] != feishuStopCmd || value["chat_id"] != chatID {
		t.Fatalf("initial card stop button value wrong: %v", value)
	}
	assertNarrowStopButton(t, buildFeishuStreamingCard(chatID))

	state := &feishuStreamState{Tools: nil}
	refresh := buildFeishuRefreshCard(state, "", feishuPhaseThinking, feishuPanelTextBudget, "", chatID)
	value = stopButtonJSON(t, refresh)
	if value == nil || value["cmd"] != feishuStopCmd || value["chat_id"] != chatID {
		t.Fatalf("refresh card stop button value wrong: %v", value)
	}
	assertNarrowStopButton(t, refresh)

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
