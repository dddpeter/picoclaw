package pico

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/channels"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/media"
)

func newTestPicoChannel(t *testing.T) *PicoChannel {
	t.Helper()

	bc := &config.Channel{Type: config.ChannelPico, Enabled: true}
	cfg := &config.PicoSettings{}
	cfg.SetToken("test-token")
	ch, err := NewPicoChannel(bc, cfg, bus.NewMessageBus())
	if err != nil {
		t.Fatalf("NewPicoChannel: %v", err)
	}

	ch.ctx = context.Background()
	return ch
}

func TestHandleMessageSend_ForwardsMessageMetadata(t *testing.T) {
	msgBus := bus.NewMessageBus()
	bc := &config.Channel{Type: config.ChannelPico, Enabled: true}
	cfg := &config.PicoSettings{}
	cfg.SetToken("test-token")
	ch, err := NewPicoChannel(bc, cfg, msgBus)
	if err != nil {
		t.Fatalf("NewPicoChannel: %v", err)
	}
	ch.ctx = context.Background()

	ch.handleMessageSend(&picoConn{id: "conn-1", sessionID: "sess-1"}, PicoMessage{
		Type:      TypeMessageSend,
		ID:        "msg-1",
		SessionID: "sess-1",
		Payload: map[string]any{
			PayloadKeyContent: "hello",
		},
	})

	select {
	case inbound := <-msgBus.InboundChan():
		if inbound.Content != "hello" {
			t.Fatalf("content = %q, want hello", inbound.Content)
		}
		if got := inbound.Context.Raw["session_id"]; got != "sess-1" {
			t.Fatalf("session_id raw = %q, want sess-1", got)
		}
	case <-time.After(time.Second):
		t.Fatal("expected inbound pico message")
	}
}

func TestFinalizeTrackedToolFeedbackMessage_StopsTrackingBeforeEdit(t *testing.T) {
	ch := &PicoChannel{
		progress: channels.NewToolFeedbackAnimator(nil),
	}
	ch.RecordToolFeedbackMessage("pico:chat-1", "msg-1", "🔧 `read_file`")

	msgIDs, handled := ch.finalizeTrackedToolFeedbackMessage(
		context.Background(),
		"pico:chat-1",
		"final reply",
		func(_ context.Context, chatID, messageID string, payload map[string]any, contextUsage *bus.ContextUsage) error {
			if _, ok := ch.currentToolFeedbackMessage(chatID); ok {
				t.Fatal("expected tracked tool feedback to be stopped before edit")
			}
			if chatID != "pico:chat-1" || messageID != "msg-1" {
				t.Fatalf("unexpected edit args: %s %s", chatID, messageID)
			}
			if got := payload[PayloadKeyContent]; got != "final reply" {
				t.Fatalf("unexpected content payload: %#v", got)
			}
			if contextUsage != nil {
				t.Fatalf("unexpected context usage: %+v", contextUsage)
			}
			return nil
		},
		nil,
		nil,
	)
	if !handled {
		t.Fatal("expected finalizeTrackedToolFeedbackMessage to handle tracked message")
	}
	if len(msgIDs) != 1 || msgIDs[0] != "msg-1" {
		t.Fatalf("finalizeTrackedToolFeedbackMessage() ids = %v, want [msg-1]", msgIDs)
	}
}

func TestDismissTrackedToolFeedbackMessage_DeletesProgressMessage(t *testing.T) {
	ch := &PicoChannel{
		progress: channels.NewToolFeedbackAnimator(nil),
	}
	ch.RecordToolFeedbackMessage("pico:chat-1", "msg-1", "🔧 `read_file`")

	var deleted struct {
		chatID    string
		messageID string
	}
	ch.deleteMessageFn = func(_ context.Context, chatID string, messageID string) error {
		deleted.chatID = chatID
		deleted.messageID = messageID
		return nil
	}

	ch.DismissToolFeedbackMessage(context.Background(), "pico:chat-1")

	if deleted.chatID != "pico:chat-1" || deleted.messageID != "msg-1" {
		t.Fatalf("unexpected delete target: %+v", deleted)
	}
	if _, ok := ch.currentToolFeedbackMessage("pico:chat-1"); ok {
		t.Fatal("expected tracked tool feedback to be cleared after dismissal")
	}
}

func TestSend_ThoughtMessageDoesNotFinalizeTrackedToolFeedback(t *testing.T) {
	ch := newTestPicoChannel(t)

	if err := ch.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer ch.Stop(context.Background())

	clientConn, received, cleanup := newTestPicoWebSocket(t)
	defer cleanup()
	ch.addConnForTest(&picoConn{id: "conn-1", conn: clientConn, sessionID: "sess-1"})

	ch.RecordToolFeedbackMessage("pico:sess-1", "msg-progress", "🔧 `read_file`\nReading config")

	if _, err := ch.Send(context.Background(), bus.OutboundMessage{
		ChatID:  "pico:sess-1",
		Content: "thinking trace",
		Context: bus.InboundContext{
			Channel: "pico",
			ChatID:  "pico:sess-1",
			Raw: map[string]string{
				"message_kind":      MessageKindThought,
				PayloadKeyModelName: "gpt-5.4-mini",
			},
		},
	}); err != nil {
		t.Fatalf("Send(thought) error = %v", err)
	}

	select {
	case msg := <-received:
		if msg.Type != TypeMessageCreate {
			t.Fatalf("thought message type = %q, want %q", msg.Type, TypeMessageCreate)
		}
		payload := msg.Payload
		if got := payload[PayloadKeyContent]; got != "thinking trace" {
			t.Fatalf("thought content = %#v, want %q", got, "thinking trace")
		}
		if got := payload[PayloadKeyKind]; got != MessageKindThought {
			t.Fatalf("thought kind = %#v, want %q", got, MessageKindThought)
		}
		if got := payload[PayloadKeyModelName]; got != "gpt-5.4-mini" {
			t.Fatalf("thought model_name = %#v, want %q", got, "gpt-5.4-mini")
		}
		if got := payload["message_id"]; got == "msg-progress" || got == nil || got == "" {
			t.Fatalf("thought message_id = %#v, want new non-progress id", got)
		}
	case <-time.After(time.Second):
		t.Fatal("expected thought message to be delivered")
	}

	if msgID, ok := ch.currentToolFeedbackMessage("pico:sess-1"); !ok || msgID != "msg-progress" {
		t.Fatalf("tracked tool feedback = (%q, %v), want (msg-progress, true)", msgID, ok)
	}

	if _, err := ch.Send(context.Background(), bus.OutboundMessage{
		ChatID:  "pico:sess-1",
		Content: "final reply",
		Context: bus.InboundContext{
			Channel: "pico",
			ChatID:  "pico:sess-1",
			Raw: map[string]string{
				PayloadKeyModelName: "gpt-5.4",
			},
		},
		ContextUsage: &bus.ContextUsage{
			UsedTokens:       321,
			TotalTokens:      4096,
			CompressAtTokens: 3072,
			UsedPercent:      8,
		},
	}); err != nil {
		t.Fatalf("Send(final) error = %v", err)
	}

	select {
	case msg := <-received:
		if msg.Type != TypeMessageUpdate {
			t.Fatalf("final message type = %q, want %q", msg.Type, TypeMessageUpdate)
		}
		payload := msg.Payload
		if got := payload["message_id"]; got != "msg-progress" {
			t.Fatalf("final message_id = %#v, want %q", got, "msg-progress")
		}
		if got := payload[PayloadKeyContent]; got != "final reply" {
			t.Fatalf("final content = %#v, want %q", got, "final reply")
		}
		if got := payload[PayloadKeyModelName]; got != "gpt-5.4" {
			t.Fatalf("final model_name = %#v, want %q", got, "gpt-5.4")
		}
		rawUsage, ok := payload["context_usage"].(map[string]any)
		if !ok {
			t.Fatalf("final context_usage = %#v, want map payload", payload["context_usage"])
		}
		if got, ok := rawUsage["used_tokens"].(float64); !ok || got != 321 {
			t.Fatalf("used_tokens = %#v, want 321", rawUsage["used_tokens"])
		}
		if got, ok := rawUsage["total_tokens"].(float64); !ok || got != 4096 {
			t.Fatalf("total_tokens = %#v, want 4096", rawUsage["total_tokens"])
		}
	case <-time.After(time.Second):
		t.Fatal("expected final reply to finalize tracked tool feedback")
	}

	if _, ok := ch.currentToolFeedbackMessage("pico:sess-1"); ok {
		t.Fatal("expected tracked tool feedback to be cleared after final reply")
	}
}

func TestSend_ToolCallsMessageIncludesModelName(t *testing.T) {
	ch := newTestPicoChannel(t)

	if err := ch.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer ch.Stop(context.Background())

	clientConn, received, cleanup := newTestPicoWebSocket(t)
	defer cleanup()
	ch.addConnForTest(&picoConn{id: "conn-1", conn: clientConn, sessionID: "sess-1"})

	if _, err := ch.Send(context.Background(), bus.OutboundMessage{
		ChatID:  "pico:sess-1",
		Content: "",
		Context: bus.InboundContext{
			Channel: "pico",
			ChatID:  "pico:sess-1",
			Raw: map[string]string{
				"message_kind":      MessageKindToolCalls,
				PayloadKeyModelName: "gpt-5.4",
				PayloadKeyToolCalls: `[{"id":"call_1","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"README.md\"}"}}]`,
			},
		},
	}); err != nil {
		t.Fatalf("Send(tool_calls) error = %v", err)
	}

	select {
	case msg := <-received:
		if msg.Type != TypeMessageCreate {
			t.Fatalf("tool_calls message type = %q, want %q", msg.Type, TypeMessageCreate)
		}
		payload := msg.Payload
		if got := payload[PayloadKeyKind]; got != MessageKindToolCalls {
			t.Fatalf("tool_calls kind = %#v, want %q", got, MessageKindToolCalls)
		}
		if got := payload[PayloadKeyModelName]; got != "gpt-5.4" {
			t.Fatalf("tool_calls model_name = %#v, want %q", got, "gpt-5.4")
		}
		if _, ok := payload[PayloadKeyToolCalls].([]any); !ok {
			t.Fatalf("tool_calls payload = %#v, want parsed array", payload[PayloadKeyToolCalls])
		}
	case <-time.After(time.Second):
		t.Fatal("expected tool_calls message to be delivered")
	}
}

func TestSendPlaceholder_EmitsNormalMessageWithoutKind(t *testing.T) {
	ch := newTestPicoChannel(t)
	ch.bc.Placeholder.Enabled = true

	if err := ch.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer ch.Stop(context.Background())

	clientConn, received, cleanup := newTestPicoWebSocket(t)
	defer cleanup()
	ch.addConnForTest(&picoConn{id: "conn-1", conn: clientConn, sessionID: "sess-1"})

	msgID, err := ch.SendPlaceholder(context.Background(), "pico:sess-1")
	if err != nil {
		t.Fatalf("SendPlaceholder() error = %v", err)
	}
	if msgID == "" {
		t.Fatal("expected placeholder message id")
	}

	select {
	case msg := <-received:
		if msg.Type != TypeMessageCreate {
			t.Fatalf("placeholder message type = %q, want %q", msg.Type, TypeMessageCreate)
		}
		payload := msg.Payload
		if got := payload["message_id"]; got != msgID {
			t.Fatalf("placeholder message_id = %#v, want %q", got, msgID)
		}
		if got := payload[PayloadKeyContent]; got != "Thinking..." {
			t.Fatalf("placeholder content = %#v, want %q", got, "Thinking...")
		}
		if got := payload[PayloadKeyPlaceholder]; got != true {
			t.Fatalf("placeholder marker = %#v, want true", got)
		}
		if got, ok := payload[PayloadKeyKind]; ok {
			t.Fatalf("placeholder kind = %#v, want absent", got)
		}
	case <-time.After(time.Second):
		t.Fatal("expected placeholder message to be delivered")
	}
}

func TestBeginStream_CreatesAndUpdatesSameMessage(t *testing.T) {
	ch := newTestPicoChannel(t)
	ch.config.Streaming = config.StreamingConfig{
		Enabled:         true,
		ThrottleSeconds: 1,
		MinGrowthChars:  1,
	}
	if err := ch.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer ch.Stop(context.Background())

	clientConn, received, cleanup := newTestPicoWebSocket(t)
	defer cleanup()
	ch.addConnForTest(&picoConn{id: "conn-1", conn: clientConn, sessionID: "sess-1"})

	streamer, err := ch.BeginStream(context.Background(), "pico:sess-1")
	if err != nil {
		t.Fatalf("BeginStream() error = %v", err)
	}
	if setter, ok := streamer.(interface{ SetModelName(modelName string) }); ok {
		setter.SetModelName("gpt-5.4")
	}
	if err := streamer.Update(context.Background(), "hello"); err != nil {
		t.Fatalf("Update(first) error = %v", err)
	}
	first := mustReceivePicoMessage(t, received)
	if first.Type != TypeMessageCreate {
		t.Fatalf("first type = %q, want %q", first.Type, TypeMessageCreate)
	}
	msgID, _ := first.Payload["message_id"].(string)
	if msgID == "" {
		t.Fatalf("first message_id = %#v, want non-empty", first.Payload["message_id"])
	}
	if got := first.Payload[PayloadKeyContent]; got != "hello" {
		t.Fatalf("first content = %#v, want hello", got)
	}
	if got := first.Payload[PayloadKeyModelName]; got != "gpt-5.4" {
		t.Fatalf("first model_name = %#v, want %q", got, "gpt-5.4")
	}

	rawStreamer := streamer.(*picoStreamer)
	rawStreamer.mu.Lock()
	rawStreamer.lastAt = time.Now().Add(-2 * time.Second)
	rawStreamer.mu.Unlock()
	secondContent := "hello world with enough growth to pass the default streaming threshold"
	if err := streamer.Update(context.Background(), secondContent); err != nil {
		t.Fatalf("Update(second) error = %v", err)
	}
	second := mustReceivePicoMessage(t, received)
	if second.Type != TypeMessageUpdate {
		t.Fatalf("second type = %q, want %q", second.Type, TypeMessageUpdate)
	}
	if got := second.Payload["message_id"]; got != msgID {
		t.Fatalf("second message_id = %#v, want %q", got, msgID)
	}
	if got := second.Payload[PayloadKeyContent]; got != secondContent {
		t.Fatalf("second content = %#v, want %q", got, secondContent)
	}
	if got := second.Payload[PayloadKeyModelName]; got != "gpt-5.4" {
		t.Fatalf("second model_name = %#v, want %q", got, "gpt-5.4")
	}
}

func TestBeginStream_DefaultStreamingShowsSmallIncrements(t *testing.T) {
	ch := newTestPicoChannel(t)
	ch.config.Streaming = config.StreamingConfig{Enabled: true}
	if err := ch.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer ch.Stop(context.Background())

	clientConn, received, cleanup := newTestPicoWebSocket(t)
	defer cleanup()
	ch.addConnForTest(&picoConn{id: "conn-1", conn: clientConn, sessionID: "sess-1"})

	streamer, err := ch.BeginStream(context.Background(), "pico:sess-1")
	if err != nil {
		t.Fatalf("BeginStream() error = %v", err)
	}
	if err := streamer.Update(context.Background(), "h"); err != nil {
		t.Fatalf("Update(first) error = %v", err)
	}
	first := mustReceivePicoMessage(t, received)
	if first.Type != TypeMessageCreate {
		t.Fatalf("first type = %q, want %q", first.Type, TypeMessageCreate)
	}
	msgID, _ := first.Payload["message_id"].(string)
	if msgID == "" {
		t.Fatalf("first message_id = %#v, want non-empty", first.Payload["message_id"])
	}

	if err := streamer.Update(context.Background(), "he"); err != nil {
		t.Fatalf("Update(second) error = %v", err)
	}
	second := mustReceivePicoMessage(t, received)
	if second.Type != TypeMessageUpdate {
		t.Fatalf("second type = %q, want %q", second.Type, TypeMessageUpdate)
	}
	if got := second.Payload["message_id"]; got != msgID {
		t.Fatalf("second message_id = %#v, want %q", got, msgID)
	}
	if got := second.Payload[PayloadKeyContent]; got != "he" {
		t.Fatalf("second content = %#v, want he", got)
	}
}

func TestBeginStream_StreamsReasoningAsThoughtUpdates(t *testing.T) {
	ch := newTestPicoChannel(t)
	ch.config.Streaming = config.StreamingConfig{Enabled: true}
	if err := ch.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer ch.Stop(context.Background())

	clientConn, received, cleanup := newTestPicoWebSocket(t)
	defer cleanup()
	ch.addConnForTest(&picoConn{id: "conn-1", conn: clientConn, sessionID: "sess-1"})

	streamer, err := ch.BeginStream(context.Background(), "pico:sess-1")
	if err != nil {
		t.Fatalf("BeginStream() error = %v", err)
	}
	reasoningStreamer, ok := streamer.(bus.ReasoningStreamer)
	if !ok {
		t.Fatal("pico stream should support reasoning updates")
	}
	if setter, ok := streamer.(interface{ SetModelName(modelName string) }); ok {
		setter.SetModelName("gpt-5.4-mini")
	}
	if err := reasoningStreamer.UpdateReasoning(context.Background(), "thinking"); err != nil {
		t.Fatalf("UpdateReasoning(first) error = %v", err)
	}
	first := mustReceivePicoMessage(t, received)
	if first.Type != TypeMessageCreate {
		t.Fatalf("first type = %q, want %q", first.Type, TypeMessageCreate)
	}
	msgID, _ := first.Payload["message_id"].(string)
	if msgID == "" {
		t.Fatalf("first message_id = %#v, want non-empty", first.Payload["message_id"])
	}
	if got := first.Payload[PayloadKeyKind]; got != MessageKindThought {
		t.Fatalf("first kind = %#v, want %q", got, MessageKindThought)
	}
	if got := first.Payload[PayloadKeyContent]; got != "thinking" {
		t.Fatalf("first content = %#v, want thinking", got)
	}
	if got := first.Payload[PayloadKeyModelName]; got != "gpt-5.4-mini" {
		t.Fatalf("first model_name = %#v, want %q", got, "gpt-5.4-mini")
	}

	if err := reasoningStreamer.UpdateReasoning(context.Background(), "thinking more"); err != nil {
		t.Fatalf("UpdateReasoning(second) error = %v", err)
	}
	second := mustReceivePicoMessage(t, received)
	if second.Type != TypeMessageUpdate {
		t.Fatalf("second type = %q, want %q", second.Type, TypeMessageUpdate)
	}
	if got := second.Payload["message_id"]; got != msgID {
		t.Fatalf("second message_id = %#v, want %q", got, msgID)
	}
	if got := second.Payload[PayloadKeyKind]; got != MessageKindThought {
		t.Fatalf("second kind = %#v, want %q", got, MessageKindThought)
	}
	if got := second.Payload[PayloadKeyContent]; got != "thinking more" {
		t.Fatalf("second content = %#v, want thinking more", got)
	}
	if got := second.Payload[PayloadKeyModelName]; got != "gpt-5.4-mini" {
		t.Fatalf("second model_name = %#v, want %q", got, "gpt-5.4-mini")
	}
}

func TestBeginStream_ThrottlesIntermediateUpdatesAndFinalFlushes(t *testing.T) {
	ch := newTestPicoChannel(t)
	ch.config.Streaming = config.StreamingConfig{
		Enabled:         true,
		ThrottleSeconds: 60,
		MinGrowthChars:  100,
	}
	if err := ch.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer ch.Stop(context.Background())

	clientConn, received, cleanup := newTestPicoWebSocket(t)
	defer cleanup()
	ch.addConnForTest(&picoConn{id: "conn-1", conn: clientConn, sessionID: "sess-1"})

	streamer, err := ch.BeginStream(context.Background(), "pico:sess-1")
	if err != nil {
		t.Fatalf("BeginStream() error = %v", err)
	}
	if err := streamer.Update(context.Background(), "first"); err != nil {
		t.Fatalf("Update(first) error = %v", err)
	}
	if err := streamer.Update(context.Background(), "first plus short growth"); err != nil {
		t.Fatalf("Update(throttled) error = %v", err)
	}
	if err := streamer.Update(context.Background(), "first"+strings.Repeat("x", 120)); err != nil {
		t.Fatalf("Update(enough growth too soon) error = %v", err)
	}

	first := mustReceivePicoMessage(t, received)
	if first.Type != TypeMessageCreate {
		t.Fatalf("first type = %q, want %q", first.Type, TypeMessageCreate)
	}
	msgID, _ := first.Payload["message_id"].(string)
	assertNoPicoMessage(t, received)

	rawStreamer := streamer.(*picoStreamer)
	rawStreamer.mu.Lock()
	rawStreamer.lastAt = time.Now().Add(-61 * time.Second)
	rawStreamer.mu.Unlock()
	if err := streamer.Update(context.Background(), "first plus small growth"); err != nil {
		t.Fatalf("Update(enough time too little growth) error = %v", err)
	}
	assertNoPicoMessage(t, received)

	if err := streamer.Finalize(context.Background(), "first plus final text"); err != nil {
		t.Fatalf("Finalize() error = %v", err)
	}
	final := mustReceivePicoMessage(t, received)
	if final.Type != TypeMessageUpdate {
		t.Fatalf("final type = %q, want %q", final.Type, TypeMessageUpdate)
	}
	if got := final.Payload["message_id"]; got != msgID {
		t.Fatalf("final message_id = %#v, want %q", got, msgID)
	}
	if got := final.Payload[PayloadKeyContent]; got != "first plus final text" {
		t.Fatalf("final content = %#v, want final text", got)
	}
	assertNoPicoMessage(t, received)
}

func TestBeginStream_FinalizeIncludesContextUsage(t *testing.T) {
	ch := newTestPicoChannel(t)
	ch.config.Streaming = config.StreamingConfig{
		Enabled:         true,
		ThrottleSeconds: 0,
		MinGrowthChars:  0,
	}
	if err := ch.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer ch.Stop(context.Background())

	clientConn, received, cleanup := newTestPicoWebSocket(t)
	defer cleanup()
	ch.addConnForTest(&picoConn{id: "conn-1", conn: clientConn, sessionID: "sess-1"})

	streamer, err := ch.BeginStream(context.Background(), "pico:sess-1")
	if err != nil {
		t.Fatalf("BeginStream() error = %v", err)
	}
	if setter, ok := streamer.(interface{ SetModelName(modelName string) }); ok {
		setter.SetModelName("gpt-5.4")
	}
	if err := streamer.Update(context.Background(), "partial"); err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	first := mustReceivePicoMessage(t, received)
	msgID, _ := first.Payload["message_id"].(string)

	contextStreamer, ok := streamer.(interface {
		FinalizeWithContext(ctx context.Context, content string, usage *bus.ContextUsage) error
	})
	if !ok {
		t.Fatal("streamer should support FinalizeWithContext")
	}
	if err := contextStreamer.FinalizeWithContext(context.Background(), "final", &bus.ContextUsage{
		UsedTokens:        10,
		TotalTokens:       100,
		HistoryTokens:     5,
		CompressAtTokens:  80,
		SummarizeAtTokens: 60,
		UsedPercent:       10,
	}); err != nil {
		t.Fatalf("FinalizeWithContext() error = %v", err)
	}

	final := mustReceivePicoMessage(t, received)
	if final.Type != TypeMessageUpdate {
		t.Fatalf("final type = %q, want %q", final.Type, TypeMessageUpdate)
	}
	if got := final.Payload["message_id"]; got != msgID {
		t.Fatalf("final message_id = %#v, want %q", got, msgID)
	}
	if got := final.Payload[PayloadKeyModelName]; got != "gpt-5.4" {
		t.Fatalf("final model_name = %#v, want %q", got, "gpt-5.4")
	}
	rawUsage, ok := final.Payload["context_usage"].(map[string]any)
	if !ok {
		t.Fatalf("final context_usage = %#v, want map", final.Payload["context_usage"])
	}
	if got := rawUsage["used_tokens"]; got != float64(10) {
		t.Fatalf("used_tokens = %#v, want 10", got)
	}
	if got := rawUsage["history_tokens"]; got != float64(5) {
		t.Fatalf("history_tokens = %#v, want 5", got)
	}
	if got := rawUsage["summarize_at_tokens"]; got != float64(60) {
		t.Fatalf("summarize_at_tokens = %#v, want 60", got)
	}
}

func TestCreateAndAddConnection_RespectsMaxConnectionsConcurrently(t *testing.T) {
	ch := newTestPicoChannel(t)

	const (
		maxConns   = 5
		goroutines = 64
		sessionID  = "session-a"
	)

	var wg sync.WaitGroup
	var mu sync.Mutex
	successCount := 0
	errCount := 0

	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()

			pc, err := ch.createAndAddConnection(nil, sessionID, maxConns)
			mu.Lock()
			defer mu.Unlock()

			if err == nil {
				successCount++
				if pc == nil {
					t.Errorf("pc is nil on success")
				}
				return
			}
			if !errors.Is(err, channels.ErrTemporary) {
				t.Errorf("unexpected error: %v", err)
				return
			}
			errCount++
		}()
	}
	wg.Wait()

	if successCount > maxConns {
		t.Fatalf("successCount=%d > maxConns=%d", successCount, maxConns)
	}
	if successCount+errCount != goroutines {
		t.Fatalf("success=%d err=%d total=%d want=%d", successCount, errCount, successCount+errCount, goroutines)
	}
	if got := ch.currentConnCount(); got != maxConns {
		t.Fatalf("currentConnCount=%d want=%d", got, maxConns)
	}
}

func TestRemoveConnection_CleansBothIndexes(t *testing.T) {
	ch := newTestPicoChannel(t)

	pc, err := ch.createAndAddConnection(nil, "session-cleanup", 10)
	if err != nil {
		t.Fatalf("createAndAddConnection: %v", err)
	}

	removed := ch.removeConnection(pc.id)
	if removed == nil {
		t.Fatal("removeConnection returned nil")
	}

	ch.connsMu.RLock()
	defer ch.connsMu.RUnlock()

	if _, ok := ch.connections[pc.id]; ok {
		t.Fatalf("connID %s still exists in connections", pc.id)
	}
	if _, ok := ch.sessionConnections[pc.sessionID]; ok {
		t.Fatalf("session %s still exists in sessionConnections", pc.sessionID)
	}
	if got := len(ch.connections); got != 0 {
		t.Fatalf("len(connections)=%d want=0", got)
	}
}

func TestBroadcastToSession_TargetsOnlyRequestedSession(t *testing.T) {
	ch := newTestPicoChannel(t)

	target := &picoConn{id: "target", sessionID: "s-target"}
	target.closed.Store(true)
	ch.addConnForTest(target)

	other := &picoConn{id: "other", sessionID: "s-other"}
	ch.addConnForTest(other)

	err := ch.broadcastToSession("pico:s-target", newMessage(TypeMessageCreate, map[string]any{"content": "hello"}))
	if err == nil {
		t.Fatal("expected send failure due to closed target connection")
	}
	if !errors.Is(err, channels.ErrSendFailed) {
		t.Fatalf("expected ErrSendFailed, got %v", err)
	}
}

func TestSendMedia_ResolvesMediaBeforeDelivery(t *testing.T) {
	ch := newTestPicoChannel(t)
	store := media.NewFileMediaStore()
	ch.SetMediaStore(store)

	if err := ch.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer ch.Stop(context.Background())

	localPath := filepath.Join(t.TempDir(), "report.txt")
	if err := os.WriteFile(localPath, []byte("attachment body"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	ref, err := store.Store(localPath, media.MediaMeta{
		Filename:    "report.txt",
		ContentType: "text/plain",
	}, "test-scope")
	if err != nil {
		t.Fatalf("Store() error = %v", err)
	}

	closedConn := &picoConn{id: "closed", sessionID: "sess-1"}
	closedConn.closed.Store(true)
	ch.addConnForTest(closedConn)

	_, err = ch.SendMedia(context.Background(), bus.OutboundMediaMessage{
		ChatID: "pico:sess-1",
		Parts: []bus.MediaPart{{
			Ref:         ref,
			Type:        "file",
			Filename:    "report.txt",
			ContentType: "text/plain",
		}},
	})
	if !errors.Is(err, channels.ErrSendFailed) {
		t.Fatalf("SendMedia() error = %v, want ErrSendFailed", err)
	}
}

func TestSendMedia_DismissesTrackedToolFeedbackMessage(t *testing.T) {
	ch := newTestPicoChannel(t)
	store := media.NewFileMediaStore()
	ch.SetMediaStore(store)

	if err := ch.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer ch.Stop(context.Background())

	clientConn, received, cleanup := newTestPicoWebSocket(t)
	defer cleanup()
	ch.addConnForTest(&picoConn{id: "conn-1", conn: clientConn, sessionID: "sess-1"})

	localPath := filepath.Join(t.TempDir(), "report.txt")
	if err := os.WriteFile(localPath, []byte("attachment body"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	ref, err := store.Store(localPath, media.MediaMeta{
		Filename:    "report.txt",
		ContentType: "text/plain",
	}, "test-scope")
	if err != nil {
		t.Fatalf("Store() error = %v", err)
	}

	ch.RecordToolFeedbackMessage("pico:sess-1", "msg-progress", "🔧 `read_file`")

	var deleted struct {
		chatID    string
		messageID string
	}
	ch.deleteMessageFn = func(_ context.Context, chatID string, messageID string) error {
		deleted.chatID = chatID
		deleted.messageID = messageID
		return nil
	}

	_, err = ch.SendMedia(context.Background(), bus.OutboundMediaMessage{
		ChatID: "pico:sess-1",
		Parts: []bus.MediaPart{{
			Ref:         ref,
			Type:        "file",
			Filename:    "report.txt",
			ContentType: "text/plain",
		}},
	})
	if err != nil {
		t.Fatalf("SendMedia() error = %v", err)
	}

	select {
	case msg := <-received:
		if msg.Type != TypeMessageCreate {
			t.Fatalf("message type = %q, want %q", msg.Type, TypeMessageCreate)
		}
	case <-time.After(time.Second):
		t.Fatal("expected media message to be delivered")
	}

	if deleted.chatID != "pico:sess-1" || deleted.messageID != "msg-progress" {
		t.Fatalf("unexpected delete target: %+v", deleted)
	}
	if _, ok := ch.currentToolFeedbackMessage("pico:sess-1"); ok {
		t.Fatal("expected tracked tool feedback to be cleared after media delivery")
	}
}

func TestSendMedia_IncludesCaptionAndAttachmentsInSinglePayload(t *testing.T) {
	ch := newTestPicoChannel(t)
	store := media.NewFileMediaStore()
	ch.SetMediaStore(store)

	if err := ch.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer ch.Stop(context.Background())

	clientConn, received, cleanup := newTestPicoWebSocket(t)
	defer cleanup()
	ch.addConnForTest(&picoConn{id: "conn-1", conn: clientConn, sessionID: "sess-1"})

	localPath := filepath.Join(t.TempDir(), "photo.png")
	if err := os.WriteFile(localPath, []byte("png-body"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	ref, err := store.Store(localPath, media.MediaMeta{
		Filename:    "photo.png",
		ContentType: "image/png",
	}, "test-scope")
	if err != nil {
		t.Fatalf("Store() error = %v", err)
	}

	_, err = ch.SendMedia(context.Background(), bus.OutboundMediaMessage{
		ChatID: "pico:sess-1",
		Parts: []bus.MediaPart{{
			Ref:         ref,
			Type:        "image",
			Filename:    "photo.png",
			ContentType: "image/png",
			Caption:     "recipe translation",
		}},
	})
	if err != nil {
		t.Fatalf("SendMedia() error = %v", err)
	}

	select {
	case msg := <-received:
		if msg.Type != TypeMessageCreate {
			t.Fatalf("message type = %q, want %q", msg.Type, TypeMessageCreate)
		}
		payload := msg.Payload
		if got := payload[PayloadKeyContent]; got != "recipe translation" {
			t.Fatalf("content = %#v, want %q", got, "recipe translation")
		}
		rawAttachments, ok := payload["attachments"].([]any)
		if !ok || len(rawAttachments) != 1 {
			t.Fatalf("attachments = %#v, want 1 attachment", payload["attachments"])
		}
		attachment, ok := rawAttachments[0].(map[string]any)
		if !ok {
			t.Fatalf("attachment = %#v, want map", rawAttachments[0])
		}
		if got := attachment["type"]; got != "image" {
			t.Fatalf("attachment type = %#v, want image", got)
		}
		if got := attachment["filename"]; got != "photo.png" {
			t.Fatalf("attachment filename = %#v, want photo.png", got)
		}
	case <-time.After(time.Second):
		t.Fatal("expected media payload to be delivered")
	}
}

func TestPicoDownloadURLForRef(t *testing.T) {
	got, err := picoDownloadURLForRef("media://attachment-1")
	if err != nil {
		t.Fatalf("picoDownloadURLForRef() error = %v", err)
	}
	if got != "/pico/media/attachment-1" {
		t.Fatalf("picoDownloadURLForRef() = %q, want %q", got, "/pico/media/attachment-1")
	}
}

func TestHandleMediaDownload_ServesStoredFile(t *testing.T) {
	ch := newTestPicoChannel(t)
	store := media.NewFileMediaStore()
	ch.SetMediaStore(store)

	if err := ch.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer ch.Stop(context.Background())

	localPath := filepath.Join(t.TempDir(), "report.txt")
	if err := os.WriteFile(localPath, []byte("downloadable"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	ref, err := store.Store(localPath, media.MediaMeta{
		Filename:    "report.txt",
		ContentType: "text/plain",
	}, "test-scope")
	if err != nil {
		t.Fatalf("Store() error = %v", err)
	}

	refID := strings.TrimPrefix(ref, "media://")
	req := httptest.NewRequest("GET", "/pico/media/"+refID, nil)
	req.Header.Set("Authorization", "Bearer test-token")
	rec := httptest.NewRecorder()

	ch.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if body := rec.Body.String(); body != "downloadable" {
		t.Fatalf("body = %q, want %q", body, "downloadable")
	}
	if got := rec.Header().Get("Content-Type"); got != "text/plain" {
		t.Fatalf("Content-Type = %q, want %q", got, "text/plain")
	}
}

func mustReceivePicoMessage(t *testing.T, received <-chan PicoMessage) PicoMessage {
	t.Helper()
	select {
	case msg := <-received:
		return msg
	case <-time.After(time.Second):
		t.Fatal("expected pico message")
	}
	return PicoMessage{}
}

func assertNoPicoMessage(t *testing.T, received <-chan PicoMessage) {
	t.Helper()
	select {
	case msg := <-received:
		t.Fatalf("unexpected pico message: %+v", msg)
	case <-time.After(150 * time.Millisecond):
	}
}

func (c *PicoChannel) addConnForTest(pc *picoConn) {
	c.connsMu.Lock()
	defer c.connsMu.Unlock()
	if c.connections == nil {
		c.connections = make(map[string]*picoConn)
	}
	if c.sessionConnections == nil {
		c.sessionConnections = make(map[string]map[string]*picoConn)
	}
	if _, exists := c.connections[pc.id]; exists {
		panic(fmt.Sprintf("duplicate conn id in test: %s", pc.id))
	}
	c.connections[pc.id] = pc
	bySession, ok := c.sessionConnections[pc.sessionID]
	if !ok {
		bySession = make(map[string]*picoConn)
		c.sessionConnections[pc.sessionID] = bySession
	}
	bySession[pc.id] = pc
}

func newTestPicoWebSocket(t *testing.T) (*websocket.Conn, <-chan PicoMessage, func()) {
	t.Helper()

	received := make(chan PicoMessage, 4)
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("Upgrade() error = %v", err)
			return
		}
		defer conn.Close()
		for {
			var msg PicoMessage
			if err := conn.ReadJSON(&msg); err != nil {
				return
			}
			received <- msg
		}
	}))

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	clientConn, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		server.Close()
		t.Fatalf("Dial() error = %v", err)
	}

	cleanup := func() {
		clientConn.Close()
		server.Close()
	}
	defer resp.Body.Close()
	return clientConn, received, cleanup
}

func TestSend_ApprovalMessageCarriesApprovalPayload(t *testing.T) {
	ch := newTestPicoChannel(t)

	if err := ch.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer ch.Stop(context.Background())

	clientConn, received, cleanup := newTestPicoWebSocket(t)
	defer cleanup()
	ch.addConnForTest(&picoConn{id: "conn-1", conn: clientConn, sessionID: "sess-1"})

	if _, err := ch.Send(context.Background(), bus.OutboundMessage{
		ChatID:  "pico:sess-1",
		Content: "⚠️ 需要批准：即将执行工具 exec",
		Context: bus.InboundContext{Channel: "pico", ChatID: "pico:sess-1"},
		Approval: &bus.ApprovalRequestMeta{
			Tool:      "exec",
			Preview:   "git push origin main",
			TimeoutMs: 300000,
		},
	}); err != nil {
		t.Fatalf("Send(approval) error = %v", err)
	}

	select {
	case msg := <-received:
		if msg.Type != TypeMessageCreate {
			t.Fatalf("message type = %q, want %q", msg.Type, TypeMessageCreate)
		}
		raw, ok := msg.Payload[PayloadKeyApproval].(map[string]any)
		if !ok {
			t.Fatalf("payload[%q] = %#v, want object", PayloadKeyApproval, msg.Payload[PayloadKeyApproval])
		}
		if raw["tool"] != "exec" {
			t.Errorf("approval tool = %#v, want exec", raw["tool"])
		}
		if raw["preview"] != "git push origin main" {
			t.Errorf("approval preview = %#v", raw["preview"])
		}
		if raw["timeout_ms"] != int64(300000) && raw["timeout_ms"] != float64(300000) {
			t.Errorf("approval timeout_ms = %#v (%T)", raw["timeout_ms"], raw["timeout_ms"])
		}
	case <-time.After(time.Second):
		t.Fatal("expected approval message to be delivered")
	}
}

// TestSend_AskMessageCarriesAskPayload pins the ask_user question payload
// (octop borrowing §三): the web card consumes payload.ask for precise
// questions/options; the plain-text fallback rides the same message.
func TestSend_AskMessageCarriesAskPayload(t *testing.T) {
	ch := newTestPicoChannel(t)

	if err := ch.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer ch.Stop(context.Background())

	clientConn, received, cleanup := newTestPicoWebSocket(t)
	defer cleanup()
	ch.addConnForTest(&picoConn{id: "conn-1", conn: clientConn, sessionID: "sess-1"})

	if _, err := ch.Send(context.Background(), bus.OutboundMessage{
		ChatID:  "pico:sess-1",
		Content: "❓ 需要你的输入：\n部署到哪个环境？",
		Context: bus.InboundContext{Channel: "pico", ChatID: "pico:sess-1"},
		Ask: &bus.AskRequestMeta{
			Questions: []bus.AskQuestion{{
				Question: "部署到哪个环境？",
				Header:   "环境",
				Options:  []bus.AskOption{{Label: "staging"}, {Label: "production"}},
			}},
			TimeoutMs: 600000,
		},
	}); err != nil {
		t.Fatalf("Send() error = %v", err)
	}

	select {
	case msg := <-received:
		if msg.Type != TypeMessageCreate {
			t.Fatalf("message type = %q, want message.create", msg.Type)
		}
		raw, _ := msg.Payload["ask"].(map[string]any)
		if raw == nil {
			t.Fatalf("payload.ask missing: %+v", msg.Payload)
		}
		questions, _ := raw["questions"].([]any)
		if len(questions) != 1 {
			t.Fatalf("payload.ask.questions len = %d, want 1", len(questions))
		}
		q := questions[0].(map[string]any)
		if q["header"] != "环境" {
			t.Fatalf("question header = %v", q["header"])
		}
		opts, _ := q["options"].([]any)
		if len(opts) != 2 {
			t.Fatalf("options len = %d, want 2", len(opts))
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no message received")
	}
}

// TestPicoA2UI_SendCarriesA2UIPayload pins the A2UI transport contract
// (A2UI web render): bus.UIMeta.Messages rides message.create raw as
// payload["a2ui"] — the Go side never interprets the structure; the
// frontend zod layer absorbs schema evolution.
func TestPicoA2UI_SendCarriesA2UIPayload(t *testing.T) {
	ch := newTestPicoChannel(t)

	if err := ch.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer ch.Stop(context.Background())

	clientConn, received, cleanup := newTestPicoWebSocket(t)
	defer cleanup()
	ch.addConnForTest(&picoConn{id: "conn-1", conn: clientConn, sessionID: "sess-1"})

	if _, err := ch.Send(context.Background(), bus.OutboundMessage{
		ChatID:  "pico:sess-1",
		Content: "🖥️ 已生成交互界面",
		Context: bus.InboundContext{Channel: "pico", ChatID: "pico:sess-1"},
		UI: &bus.UIMeta{
			Messages: json.RawMessage(`[{"version":"v0.9","createSurface":{"surfaceId":"s1","catalogId":"basic","components":[]}}]`),
		},
	}); err != nil {
		t.Fatalf("Send() error = %v", err)
	}

	select {
	case msg := <-received:
		if msg.Type != TypeMessageCreate {
			t.Fatalf("message type = %q, want %q", msg.Type, TypeMessageCreate)
		}
		messages, ok := msg.Payload[PayloadKeyA2UI].([]any)
		if !ok {
			t.Fatalf("payload[%q] = %#v, want array", PayloadKeyA2UI, msg.Payload[PayloadKeyA2UI])
		}
		if len(messages) != 1 {
			t.Fatalf("payload[%q] len = %d, want 1", PayloadKeyA2UI, len(messages))
		}
		entry, ok := messages[0].(map[string]any)
		if !ok {
			t.Fatalf("payload[%q][0] = %#v, want object", PayloadKeyA2UI, messages[0])
		}
		surface, ok := entry["createSurface"].(map[string]any)
		if !ok {
			t.Fatalf("createSurface = %#v, want object", entry["createSurface"])
		}
		if surface["surfaceId"] != "s1" {
			t.Errorf("createSurface.surfaceId = %#v, want s1", surface["surfaceId"])
		}
	case <-time.After(time.Second):
		t.Fatal("expected a2ui message to be delivered")
	}
}

// a2uiBatchFramesOf extracts payload["a2ui"] as an ordered list of frame
// objects (test helper for the A2UI merge contract).
func a2uiBatchFramesOf(t *testing.T, msg PicoMessage) []map[string]any {
	t.Helper()
	raw, ok := msg.Payload[PayloadKeyA2UI].([]any)
	if !ok {
		t.Fatalf("payload[%q] = %#v, want array", PayloadKeyA2UI, msg.Payload[PayloadKeyA2UI])
	}
	frames := make([]map[string]any, 0, len(raw))
	for i, frame := range raw {
		obj, ok := frame.(map[string]any)
		if !ok {
			t.Fatalf("payload[%q][%d] = %#v, want object", PayloadKeyA2UI, i, frame)
		}
		frames = append(frames, obj)
	}
	return frames
}

// frameBranchOf returns the single union-branch key of an A2UI frame
// ("createSurface" / "updateComponents" / "updateDataModel" / ...).
func frameBranchOf(t *testing.T, frame map[string]any) string {
	t.Helper()
	for _, key := range []string{"createSurface", "updateComponents", "updateDataModel", "deleteSurface"} {
		if _, ok := frame[key]; ok {
			return key
		}
	}
	t.Fatalf("frame %#v carries no known union branch", frame)
	return ""
}

const testA2UICreateBatch = `[{"version":"v0.9","createSurface":{"surfaceId":"s1","catalogId":"basic"}}]`

const testA2UIUpdateBatch1 = `[` +
	`{"version":"v0.9","updateComponents":{"surfaceId":"s1","components":[{"id":"t1","component":"Text","text":"hello"}]}},` +
	`{"version":"v0.9","updateDataModel":{"surfaceId":"s1","path":"/a","value":"1"}}` +
	`]`

const testA2UIUpdateBatch2 = `[{"version":"v0.9","updateDataModel":{"surfaceId":"s1","path":"/a","value":"2"}}]`

// TestPicoA2UI_CreateRegistersSurfaceAndUpdateMerges pins the streamed
// surface merge contract (A2UI phase 2, task 5): the create batch broadcasts
// message.create and registers (chatID, surfaceId) → {messageID, frames};
// follow-up update-only batches for the same surface hot-update that exact
// message via message.update carrying the merged batch — create frames
// first, update frames appended, same-path updateDataModel overwritten
// last-value-wins (mirrors the RenderUIHistoryFold replay semantics so live
// and replayed cards converge on the same final state).
func TestPicoA2UI_CreateRegistersSurfaceAndUpdateMerges(t *testing.T) {
	ch := newTestPicoChannel(t)

	if err := ch.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer ch.Stop(context.Background())

	clientConn, received, cleanup := newTestPicoWebSocket(t)
	defer cleanup()
	ch.addConnForTest(&picoConn{id: "conn-1", conn: clientConn, sessionID: "sess-1"})

	chatID := "pico:sess-1"

	// ① Create batch: broadcast as message.create and register the surface.
	if _, err := ch.Send(context.Background(), bus.OutboundMessage{
		ChatID:  chatID,
		Content: "🖥️ card",
		Context: bus.InboundContext{Channel: "pico", ChatID: chatID},
		UI: &bus.UIMeta{
			Messages:  json.RawMessage(testA2UICreateBatch),
			SurfaceID: "s1",
		},
	}); err != nil {
		t.Fatalf("Send(create) error = %v", err)
	}

	var createMsgID string
	select {
	case msg := <-received:
		if msg.Type != TypeMessageCreate {
			t.Fatalf("first message type = %q, want %q", msg.Type, TypeMessageCreate)
		}
		createMsgID, _ = msg.Payload["message_id"].(string)
		if createMsgID == "" {
			t.Fatalf("create payload missing message_id: %#v", msg.Payload)
		}
		frames := a2uiBatchFramesOf(t, msg)
		if len(frames) != 1 || frameBranchOf(t, frames[0]) != "createSurface" {
			t.Fatalf("create batch = %#v, want a single createSurface frame", frames)
		}
	case <-time.After(time.Second):
		t.Fatal("expected create batch to be delivered")
	}

	// The create registered (chatID, surfaceId) → {messageID, frames}.
	state, ok := ch.a2uiSurfaces.lookup(chatID, "s1")
	if !ok {
		t.Fatal("create batch did not register (chatID, surfaceId)")
	}
	if state.messageID != createMsgID {
		t.Fatalf("registry messageID = %q, want %q", state.messageID, createMsgID)
	}
	if len(state.frames) != 1 {
		t.Fatalf("registry frames len = %d, want 1", len(state.frames))
	}

	// ② Update-only batch: merge into the registered card, broadcast as
	// message.update against the create's message id.
	if _, err := ch.Send(context.Background(), bus.OutboundMessage{
		ChatID:  chatID,
		Content: "🧩 update",
		Context: bus.InboundContext{Channel: "pico", ChatID: chatID},
		UI: &bus.UIMeta{
			Messages:  json.RawMessage(testA2UIUpdateBatch1),
			SurfaceID: "s1",
		},
	}); err != nil {
		t.Fatalf("Send(update 1) error = %v", err)
	}

	select {
	case msg := <-received:
		if msg.Type != TypeMessageUpdate {
			t.Fatalf("second message type = %q, want %q", msg.Type, TypeMessageUpdate)
		}
		if got := msg.Payload["message_id"]; got != createMsgID {
			t.Fatalf("update message_id = %#v, want the create's %q", got, createMsgID)
		}
		// The card caption stays the create's content (matches the history
		// fold pinning the caption on the establishing call).
		if got, _ := msg.Payload[PayloadKeyContent].(string); got != "🖥️ card" {
			t.Fatalf("update content = %#v, want the create caption", got)
		}
		frames := a2uiBatchFramesOf(t, msg)
		if len(frames) != 3 {
			t.Fatalf("merged batch len = %d, want 3 frames: %#v", len(frames), frames)
		}
		wantBranches := []string{"createSurface", "updateComponents", "updateDataModel"}
		for i, want := range wantBranches {
			if got := frameBranchOf(t, frames[i]); got != want {
				t.Fatalf("merged frame %d branch = %q, want %q", i, got, want)
			}
		}
		dm := frames[2]["updateDataModel"].(map[string]any)
		if dm["path"] != "/a" || dm["value"] != "1" {
			t.Fatalf("updateDataModel = %#v, want path /a value 1", dm)
		}
	case <-time.After(time.Second):
		t.Fatal("expected merged update to be delivered")
	}

	// A second same-path updateDataModel frame overwrites in place
	// (last value wins) instead of appending a duplicate.
	if _, err := ch.Send(context.Background(), bus.OutboundMessage{
		ChatID:  chatID,
		Context: bus.InboundContext{Channel: "pico", ChatID: chatID},
		UI: &bus.UIMeta{
			Messages:  json.RawMessage(testA2UIUpdateBatch2),
			SurfaceID: "s1",
		},
	}); err != nil {
		t.Fatalf("Send(update 2) error = %v", err)
	}

	select {
	case msg := <-received:
		if msg.Type != TypeMessageUpdate {
			t.Fatalf("third message type = %q, want %q", msg.Type, TypeMessageUpdate)
		}
		frames := a2uiBatchFramesOf(t, msg)
		if len(frames) != 3 {
			t.Fatalf("re-merged batch len = %d, want 3 frames (overwrite, not append): %#v", len(frames), frames)
		}
		dm := frames[2]["updateDataModel"].(map[string]any)
		if dm["path"] != "/a" || dm["value"] != "2" {
			t.Fatalf("updateDataModel = %#v, want path /a value 2 (last value wins)", dm)
		}
	case <-time.After(time.Second):
		t.Fatal("expected second merged update to be delivered")
	}

	// The registry now holds the fully merged frames.
	state, ok = ch.a2uiSurfaces.lookup(chatID, "s1")
	if !ok {
		t.Fatal("registry entry vanished after updates")
	}
	if len(state.frames) != 3 {
		t.Fatalf("registry merged frames len = %d, want 3", len(state.frames))
	}
}

// TestPicoA2UI_UpdateOnlyBatch_SessionIsolation pins review P2: the merge
// registry keys on (chatID, surfaceId) — session B's update for the same
// surfaceId string must not touch session A's card. B degrades to a fresh
// message.create (which registers B's own surface), and only after that do
// B's updates merge against B's registration.
func TestPicoA2UI_UpdateOnlyBatch_SessionIsolation(t *testing.T) {
	ch := newTestPicoChannel(t)

	if err := ch.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer ch.Stop(context.Background())

	connA, receivedA, cleanupA := newTestPicoWebSocket(t)
	defer cleanupA()
	ch.addConnForTest(&picoConn{id: "conn-a", conn: connA, sessionID: "sess-a"})

	connB, receivedB, cleanupB := newTestPicoWebSocket(t)
	defer cleanupB()
	ch.addConnForTest(&picoConn{id: "conn-b", conn: connB, sessionID: "sess-b"})

	// Session A creates surface s1.
	if _, err := ch.Send(context.Background(), bus.OutboundMessage{
		ChatID:  "pico:sess-a",
		Content: "🖥️ card A",
		Context: bus.InboundContext{Channel: "pico", ChatID: "pico:sess-a"},
		UI: &bus.UIMeta{
			Messages:  json.RawMessage(testA2UICreateBatch),
			SurfaceID: "s1",
		},
	}); err != nil {
		t.Fatalf("Send(create on A) error = %v", err)
	}

	var aMsgID string
	select {
	case msg := <-receivedA:
		if msg.Type != TypeMessageCreate {
			t.Fatalf("A first message type = %q, want %q", msg.Type, TypeMessageCreate)
		}
		aMsgID, _ = msg.Payload["message_id"].(string)
	case <-time.After(time.Second):
		t.Fatal("expected A create to be delivered")
	}

	// Session B sends an update-only batch for the SAME surfaceId string
	// without having created it: it must NOT hot-update A's card — it
	// degrades to a fresh message.create on B.
	if _, err := ch.Send(context.Background(), bus.OutboundMessage{
		ChatID:  "pico:sess-b",
		Content: "🧩 orphan update",
		Context: bus.InboundContext{Channel: "pico", ChatID: "pico:sess-b"},
		UI: &bus.UIMeta{
			Messages:  json.RawMessage(testA2UIUpdateBatch1),
			SurfaceID: "s1",
		},
	}); err != nil {
		t.Fatalf("Send(orphan update on B) error = %v", err)
	}

	select {
	case msg := <-receivedB:
		if msg.Type != TypeMessageCreate {
			t.Fatalf("B orphan update broadcast as %q, want degraded %q", msg.Type, TypeMessageCreate)
		}
		if got := msg.Payload["message_id"]; got == aMsgID {
			t.Fatal("B orphan update must not reuse A's message id")
		}
	case <-time.After(time.Second):
		t.Fatal("expected degraded create on B")
	}

	// The degraded create registered B's own surface: B's next update merges
	// against B's card.
	if _, err := ch.Send(context.Background(), bus.OutboundMessage{
		ChatID:  "pico:sess-b",
		Context: bus.InboundContext{Channel: "pico", ChatID: "pico:sess-b"},
		UI: &bus.UIMeta{
			Messages:  json.RawMessage(testA2UIUpdateBatch2),
			SurfaceID: "s1",
		},
	}); err != nil {
		t.Fatalf("Send(update on B) error = %v", err)
	}

	select {
	case msg := <-receivedB:
		if msg.Type != TypeMessageUpdate {
			t.Fatalf("B second update type = %q, want %q", msg.Type, TypeMessageUpdate)
		}
		if got := msg.Payload["message_id"]; got == aMsgID {
			t.Fatal("B update must not hot-update A's card")
		}
	case <-time.After(time.Second):
		t.Fatal("expected B merged update")
	}

	// A's card must not have received any extra frame (no cross-session
	// update traffic at all).
	select {
	case msg := <-receivedA:
		t.Fatalf("session A received unexpected frame: %+v", msg)
	case <-time.After(150 * time.Millisecond):
	}
}

// TestPicoA2UI_UnknownSurfaceUpdateDegradesToCreate pins the degradation
// rule: an update-only batch whose (chatID, surfaceId) has no registered
// create broadcasts a message.create new card instead of an orphan update
// that no client could attach to (e.g. after a gateway restart cleared the
// registry).
func TestPicoA2UI_UnknownSurfaceUpdateDegradesToCreate(t *testing.T) {
	ch := newTestPicoChannel(t)

	if err := ch.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer ch.Stop(context.Background())

	clientConn, received, cleanup := newTestPicoWebSocket(t)
	defer cleanup()
	ch.addConnForTest(&picoConn{id: "conn-1", conn: clientConn, sessionID: "sess-1"})

	if _, err := ch.Send(context.Background(), bus.OutboundMessage{
		ChatID:  "pico:sess-1",
		Content: "🧩 orphan",
		Context: bus.InboundContext{Channel: "pico", ChatID: "pico:sess-1"},
		UI: &bus.UIMeta{
			Messages:  json.RawMessage(testA2UIUpdateBatch1),
			SurfaceID: "never-created",
		},
	}); err != nil {
		t.Fatalf("Send() error = %v", err)
	}

	select {
	case msg := <-received:
		if msg.Type != TypeMessageCreate {
			t.Fatalf("message type = %q, want degraded %q", msg.Type, TypeMessageCreate)
		}
		frames := a2uiBatchFramesOf(t, msg)
		if len(frames) != len(jsonAnchoredFrames(t, testA2UIUpdateBatch1)) {
			t.Fatalf("degraded card batch len = %d, want the update batch frames", len(frames))
		}
	case <-time.After(time.Second):
		t.Fatal("expected degraded create to be delivered")
	}

	// The degraded create registered the surface, so a follow-up update
	// merges instead of creating yet another card.
	if _, err := ch.Send(context.Background(), bus.OutboundMessage{
		ChatID:  "pico:sess-1",
		Context: bus.InboundContext{Channel: "pico", ChatID: "pico:sess-1"},
		UI: &bus.UIMeta{
			Messages:  json.RawMessage(testA2UIUpdateBatch2),
			SurfaceID: "never-created",
		},
	}); err != nil {
		t.Fatalf("Send() error = %v", err)
	}

	select {
	case msg := <-received:
		if msg.Type != TypeMessageUpdate {
			t.Fatalf("follow-up type = %q, want %q", msg.Type, TypeMessageUpdate)
		}
	case <-time.After(time.Second):
		t.Fatal("expected follow-up merge after degraded registration")
	}
}

// jsonAnchoredFrames decodes an A2UI batch JSON into frame objects (test
// helper to compare expected batch lengths).
func jsonAnchoredFrames(t *testing.T, batch string) []map[string]any {
	t.Helper()
	var raw []any
	if err := json.Unmarshal([]byte(batch), &raw); err != nil {
		t.Fatalf("json.Unmarshal(test batch) error = %v", err)
	}
	frames := make([]map[string]any, 0, len(raw))
	for _, frame := range raw {
		obj, ok := frame.(map[string]any)
		if !ok {
			t.Fatalf("frame = %#v, want object", frame)
		}
		frames = append(frames, obj)
	}
	return frames
}

// TestA2uiSurfaceRegistry_LRUCap pins the registry bound: at most
// a2uiSurfaceCap entries survive, eviction drops the least recently used
// entry (record or lookup both count as use), and re-recording a known key
// replaces the entry instead of duplicating it.
func TestA2uiSurfaceRegistry_LRUCap(t *testing.T) {
	r := newA2uiSurfaceRegistry()

	for i := 0; i < a2uiSurfaceCap; i++ {
		chatID := fmt.Sprintf("chat-%d", i)
		r.record(chatID, "s1", fmt.Sprintf("m-%d", i), "", []any{i})
	}
	if len(r.entries) != a2uiSurfaceCap {
		t.Fatalf("entries = %d, want %d", len(r.entries), a2uiSurfaceCap)
	}

	// Touching the oldest entry (chat-0) makes it the most recently used.
	if _, ok := r.lookup("chat-0", "s1"); !ok {
		t.Fatal("chat-0 should be registered")
	}

	// One more record over the cap evicts the LRU entry (chat-1, since
	// chat-0 was just touched).
	r.record("chat-new", "s1", "m-new", "", nil)
	if _, ok := r.lookup("chat-1", "s1"); ok {
		t.Fatal("chat-1 should have been evicted as LRU")
	}
	if _, ok := r.lookup("chat-0", "s1"); !ok {
		t.Fatal("touched chat-0 must survive eviction")
	}
	if _, ok := r.lookup("chat-new", "s1"); !ok {
		t.Fatal("chat-new should be registered")
	}

	// Re-recording a known key replaces the entry (a second create for the
	// same surface takes over) without growing the map.
	r.record("chat-new", "s1", "m-new-2", "caption", []any{"replaced"})
	state, ok := r.lookup("chat-new", "s1")
	if !ok {
		t.Fatal("chat-new should still be registered")
	}
	if state.messageID != "m-new-2" || state.content != "caption" || len(state.frames) != 1 {
		t.Fatalf("re-recorded state = %+v, want replaced entry", state)
	}
	if len(r.entries) != a2uiSurfaceCap {
		t.Fatalf("entries after re-record = %d, want %d", len(r.entries), a2uiSurfaceCap)
	}

	// Session scoping lives in the key: same surfaceId under another chatID
	// never collides.
	r.record("chat-other", "s1", "m-other", "", nil)
	if state, ok := r.lookup("chat-new", "s1"); !ok || state.messageID != "m-new-2" {
		t.Fatalf("chat-new entry = %+v ok=%v, want untouched by chat-other", state, ok)
	}
}

// TestSetContextUsagePayloadCarriesSegments pins the wire contract (review
// P1-6): context_usage.segments must survive the pico payload serialization
// — dropping them left the web ring's stacked breakdown dead on the wire.
func TestSetContextUsagePayloadCarriesSegments(t *testing.T) {
	payload := map[string]any{}
	setContextUsagePayload(payload, &bus.ContextUsage{
		UsedTokens:  1000,
		TotalTokens: 8000,
		Segments: []bus.ContextUsageSegment{
			{Key: "conversation", Tokens: 700},
			{Key: "mcp", Tokens: 300},
		},
	})
	raw, ok := payload["context_usage"].(map[string]any)
	if !ok {
		t.Fatalf("context_usage payload missing: %+v", payload)
	}
	segs, ok := raw["segments"].([]map[string]any)
	if !ok || len(segs) != 2 {
		t.Fatalf("segments must serialize as a list of {key,tokens}, got: %#v", raw["segments"])
	}
	if segs[0]["key"] != "conversation" || segs[1]["tokens"] != 300 {
		t.Fatalf("segments content mismatch: %#v", segs)
	}

	// No segments → field omitted (single-color fallback on the web ring).
	empty := map[string]any{}
	setContextUsagePayload(empty, &bus.ContextUsage{UsedTokens: 10, TotalTokens: 100})
	if raw, _ := empty["context_usage"].(map[string]any); raw != nil {
		if _, exists := raw["segments"]; exists {
			t.Fatal("segments must be omitted when empty")
		}
	}
}

// TestOutboundMessageIsProgressNote_CardsAndPicoKind pins the P1-3 wire
// decision: the pico-scoped receipt marker (pico_kind) and the generic
// message_kind path count as non-final beats, while approval/ask CARDS are
// deliberately NOT marked on the wire — older pico clients (the TUI) render
// progress_note as a status-line-only beat and would drop the card from the
// timeline (review P1-2). The web frontend derives non-final semantics from
// the ask/approval payload instead.
func TestOutboundMessageIsProgressNote_CardsAndPicoKind(t *testing.T) {
	askCard := bus.OutboundMessage{Content: "❓", Ask: &bus.AskRequestMeta{}}
	if outboundMessageIsProgressNote(askCard) {
		t.Fatal("ask cards stay unmarked on the wire (TUI timeline visibility)")
	}
	approvalCard := bus.OutboundMessage{Content: "⚠️", Approval: &bus.ApprovalRequestMeta{}}
	if outboundMessageIsProgressNote(approvalCard) {
		t.Fatal("approval cards stay unmarked on the wire (TUI timeline visibility)")
	}
	receipt := bus.OutboundMessage{Content: "📝 已收到回答", Context: bus.InboundContext{Raw: map[string]string{"pico_kind": "progress_note"}}}
	if !outboundMessageIsProgressNote(receipt) {
		t.Fatal("pico_kind=progress_note receipts must read as non-final beats")
	}
	heartbeat := bus.OutboundMessage{Content: "⏳", Context: bus.InboundContext{Raw: map[string]string{"message_kind": "progress_note"}}}
	if !outboundMessageIsProgressNote(heartbeat) {
		t.Fatal("message_kind=progress_note beats keep their existing meaning")
	}
	final := bus.OutboundMessage{Content: "done, here is the answer"}
	if outboundMessageIsProgressNote(final) {
		t.Fatal("plain final answers must stay final")
	}
}
