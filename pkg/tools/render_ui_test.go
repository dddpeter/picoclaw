package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/bus"
)

func newTestRenderUITool(t *testing.T) (*RenderUITool, *bus.MessageBus) {
	t.Helper()
	msgBus := bus.NewMessageBus()
	return NewRenderUITool(msgBus), msgBus
}

func waitOutboundMessage(t *testing.T, msgBus *bus.MessageBus) bus.OutboundMessage {
	t.Helper()
	select {
	case msg := <-msgBus.OutboundChan():
		return msg
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for outbound message")
		return bus.OutboundMessage{}
	}
}

func assertNoOutboundMessage(t *testing.T, msgBus *bus.MessageBus) {
	t.Helper()
	select {
	case msg := <-msgBus.OutboundChan():
		t.Fatalf("expected no outbound message, got: %+v", msg)
	case <-time.After(150 * time.Millisecond):
	}
}

// renderUITestMessages returns a valid A2UI v0.9 batch argument: one
// createSurface message whose components carry text props.
func renderUITestMessages() []any {
	return []any{
		map[string]any{
			"version": "v0.9",
			"createSurface": map[string]any{
				"surfaceId": "surface-1",
				"catalogId": "https://a2ui.org/specification/v0_9/catalogs/basic/catalog.json",
				"components": []any{
					map[string]any{"id": "c1", "component": "Card", "label": "季度体检人数"},
					map[string]any{"id": "c2", "component": "Text", "text": "2026 Q1: 128 人"},
				},
			},
		},
	}
}

// TestRenderUIToolValidBatchPublishesUI pins the happy path: a valid v0.9
// batch publishes one OutboundMessage whose UI.Messages carries the input
// verbatim and whose Content is a non-empty plain-text fallback.
func TestRenderUIToolValidBatchPublishesUI(t *testing.T) {
	tool, msgBus := newTestRenderUITool(t)
	ctx := WithToolSessionContext(
		WithToolContext(context.Background(), "pico", "pico:sess-1"),
		"agent-1", "session-key-1", nil,
	)

	messages := renderUITestMessages()
	result := tool.Execute(ctx, map[string]any{"messages": messages})
	if result.IsError {
		t.Fatalf("Execute() IsError = true, ForLLM: %s", result.ForLLM)
	}
	if !strings.Contains(strings.ToLower(result.ForLLM), "render") {
		t.Errorf("ForLLM = %q, want a render success hint", result.ForLLM)
	}

	msg := waitOutboundMessage(t, msgBus)
	if msg.UI == nil {
		t.Fatalf("published message UI = nil, want UIMeta")
	}
	want, err := json.Marshal(messages)
	if err != nil {
		t.Fatalf("marshal test messages: %v", err)
	}
	if string(msg.UI.Messages) != string(want) {
		t.Errorf("UI.Messages = %s, want %s", msg.UI.Messages, want)
	}
	if strings.TrimSpace(msg.Content) == "" {
		t.Error("published Content is empty, want plain-text fallback summary")
	}
	// Generated fallback must surface component text so non-pico channels
	// stay readable instead of silently dropping the card.
	if !strings.Contains(msg.Content, "2026 Q1: 128 人") {
		t.Errorf("Content = %q, want component text in fallback", msg.Content)
	}
	if msg.Channel != "pico" || msg.ChatID != "pico:sess-1" {
		t.Errorf("published to %s:%s, want pico:pico:sess-1", msg.Channel, msg.ChatID)
	}
	if msg.AgentID != "agent-1" || msg.SessionKey != "session-key-1" {
		t.Errorf("AgentID/SessionKey = %q/%q, want agent-1/session-key-1", msg.AgentID, msg.SessionKey)
	}
}

// TestRenderUIToolSummaryParamWinsAsFallbackContent: the optional summary
// param overrides the generated components-tree text as fallback Content.
func TestRenderUIToolSummaryParamWinsAsFallbackContent(t *testing.T) {
	tool, msgBus := newTestRenderUITool(t)
	ctx := WithToolContext(context.Background(), "pico", "pico:sess-1")

	result := tool.Execute(ctx, map[string]any{
		"messages": renderUITestMessages(),
		"summary":  "季度体检人数表",
	})
	if result.IsError {
		t.Fatalf("Execute() IsError = true, ForLLM: %s", result.ForLLM)
	}

	msg := waitOutboundMessage(t, msgBus)
	if msg.Content != "季度体检人数表" {
		t.Errorf("Content = %q, want summary param as fallback", msg.Content)
	}
}

// TestRenderUIToolInvalidBatchRejected: malformed batches fail with an
// explanatory ForLLM and nothing is published.
func TestRenderUIToolInvalidBatchRejected(t *testing.T) {
	cases := []struct {
		name string
		args map[string]any
		want string
	}{
		{
			name: "messages missing",
			args: map[string]any{},
			want: "messages",
		},
		{
			name: "messages is a JSON string, not an array",
			args: map[string]any{"messages": `{"version":"v0.9"}`},
			want: "array",
		},
		{
			name: "message missing createSurface",
			args: map[string]any{"messages": []any{map[string]any{"version": "v0.9"}}},
			want: "createSurface",
		},
		{
			name: "message version not v0.9",
			args: map[string]any{"messages": []any{map[string]any{
				"version":       "v0.8",
				"createSurface": map[string]any{"surfaceId": "s1"},
			}}},
			want: "v0.9",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tool, msgBus := newTestRenderUITool(t)
			ctx := WithToolContext(context.Background(), "pico", "pico:sess-1")

			result := tool.Execute(ctx, tc.args)
			if !result.IsError {
				t.Fatalf("Execute() IsError = false, want rejection; ForLLM: %s", result.ForLLM)
			}
			if !strings.Contains(result.ForLLM, tc.want) {
				t.Errorf("ForLLM = %q, want it to mention %q", result.ForLLM, tc.want)
			}
			assertNoOutboundMessage(t, msgBus)
		})
	}
}

// TestRenderUIToolOversizedBatchRejected: batches beyond the 256KB cap fail
// with an explanatory ForLLM and nothing is published.
func TestRenderUIToolOversizedBatchRejected(t *testing.T) {
	tool, msgBus := newTestRenderUITool(t)
	ctx := WithToolContext(context.Background(), "pico", "pico:sess-1")

	big := []any{
		map[string]any{
			"version": "v0.9",
			"createSurface": map[string]any{
				"surfaceId":  "s1",
				"components": []any{map[string]any{"id": "c1", "component": "Text", "text": strings.Repeat("x", 256*1024)}},
			},
		},
	}
	result := tool.Execute(ctx, map[string]any{"messages": big})
	if !result.IsError {
		t.Fatal("Execute() IsError = false, want size-cap rejection")
	}
	if !strings.Contains(result.ForLLM, "large") {
		t.Errorf("ForLLM = %q, want size-limit explanation", result.ForLLM)
	}
	assertNoOutboundMessage(t, msgBus)
}

// TestRenderUIToolSchemaContract pins the tool signature: messages is a
// required array of objects whose description spells out the strict v0.9
// envelope, summary is an optional plain-text fallback string.
func TestRenderUIToolSchemaContract(t *testing.T) {
	tool, _ := newTestRenderUITool(t)

	if tool.Name() != "render_ui" {
		t.Fatalf("Name() = %q, want render_ui", tool.Name())
	}
	if strings.TrimSpace(tool.Description()) == "" {
		t.Error("Description() is empty")
	}

	params := tool.Parameters()
	props, ok := params["properties"].(map[string]any)
	if !ok {
		t.Fatalf("Parameters() properties = %#v, want map", params["properties"])
	}
	messages, ok := props["messages"].(map[string]any)
	if !ok {
		t.Fatalf("properties[messages] = %#v, want schema map", props["messages"])
	}
	if messages["type"] != "array" {
		t.Errorf("messages.type = %#v, want array", messages["type"])
	}
	if items, ok := messages["items"].(map[string]any); !ok || items["type"] != "object" {
		t.Errorf("messages.items = %#v, want {type: object}", messages["items"])
	}
	desc, _ := messages["description"].(string)
	for _, want := range []string{
		`{"version":"v0.9","createSurface":`,
		"createSurface",
		"https://a2ui.org/specification/v0_9/catalogs/basic/catalog.json",
	} {
		if !strings.Contains(desc, want) {
			t.Errorf("messages description missing %q: %s", want, desc)
		}
	}
	summary, ok := props["summary"].(map[string]any)
	if !ok || summary["type"] != "string" {
		t.Errorf("properties[summary] = %#v, want optional string schema", props["summary"])
	}
	req, ok := params["required"].([]string)
	if !ok || len(req) != 1 || req[0] != "messages" {
		t.Errorf("required = %#v, want [messages]", params["required"])
	}
}

// TestRenderUIHistoryMessages pins the history-replay extraction (A2UI web
// render task 5): valid persisted arguments yield the verbatim batch plus
// the same fallback derivation as the live publish; anything the tool would
// have rejected live yields ok=false so history never resurfaces cards that
// never rendered.
func TestRenderUIHistoryMessages(t *testing.T) {
	t.Run("valid batch with summary param", func(t *testing.T) {
		argsJSON := `{"messages":` + marshalForTest(t, renderUITestMessages()) + `,"summary":"季度体检人数表"}`
		messages, fallback, ok := RenderUIHistoryMessages(argsJSON)
		if !ok {
			t.Fatal("ok = false, want extraction to succeed")
		}
		want := marshalForTest(t, renderUITestMessages())
		if string(messages) != want {
			t.Errorf("messages = %s, want %s", messages, want)
		}
		if fallback != "季度体检人数表" {
			t.Errorf("fallback = %q, want summary param", fallback)
		}
	})
	t.Run("valid batch without summary derives components-tree fallback", func(t *testing.T) {
		argsJSON := `{"messages":` + marshalForTest(t, renderUITestMessages()) + `}`
		messages, fallback, ok := RenderUIHistoryMessages(argsJSON)
		if !ok {
			t.Fatal("ok = false, want extraction to succeed")
		}
		if len(messages) == 0 {
			t.Error("messages is empty")
		}
		if !strings.Contains(fallback, "2026 Q1: 128 人") {
			t.Errorf("fallback = %q, want derived components text", fallback)
		}
	})
	t.Run("rejected live stays rejected", func(t *testing.T) {
		for name, argsJSON := range map[string]string{
			"invalid json":        `{not json`,
			"empty messages":      `{"messages":[]}`,
			"messages not array":  `{"messages":"nope"}`,
			"missing messages":    `{"summary":"x"}`,
			"bad version":         `{"messages":[{"version":"v1.0","createSurface":{"surfaceId":"s"}}]}`,
			"missing surface":     `{"messages":[{"version":"v0.9"}]}`,
			"oversized batch": func() string {
				big := map[string]any{
					"version":      "v0.9",
					"createSurface": map[string]any{"surfaceId": "s", "components": []any{map[string]any{"id": "c", "component": "Text", "text": strings.Repeat("x", 256*1024)}}},
				}
				return `{"messages":` + marshalForTest(t, []any{big}) + `}`
			}(),
		} {
			if _, _, ok := RenderUIHistoryMessages(argsJSON); ok {
				t.Errorf("%s: ok = true, want rejection", name)
			}
		}
	})
}

func marshalForTest(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(encoded)
}
