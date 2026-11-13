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

// renderUITestMessages returns a valid frame-shaped A2UI v0.9 batch (the
// shape the strict zod adapter accepts — same as the verified frontend
// fixture in a2ui-card.test.tsx): one createSurface envelope, one
// updateComponents frame (literal + path-bound Text), one updateDataModel
// frame. components/dataModel are NEVER inlined in createSurface.
func renderUITestMessages() []any {
	return []any{
		map[string]any{
			"version": "v0.9",
			"createSurface": map[string]any{
				"surfaceId": "surface-1",
				"catalogId": "https://a2ui.org/specification/v0_9/catalogs/basic/catalog.json",
			},
		},
		map[string]any{
			"version": "v0.9",
			"updateComponents": map[string]any{
				"surfaceId": "surface-1",
				"components": []any{
					map[string]any{"id": "c1", "component": "Card", "child": "c2"},
					map[string]any{"id": "c2", "component": "Text", "text": "季度体检人数"},
					map[string]any{"id": "c3", "component": "Text", "text": map[string]any{"path": "/rows/0"}},
				},
			},
		},
		map[string]any{
			"version": "v0.9",
			"updateDataModel": map[string]any{
				"surfaceId": "surface-1",
				"path":      "/",
				"value":     map[string]any{"rows": []any{"2026 Q1: 128 人", "2026 Q2: 157 人"}},
			},
		},
	}
}

// renderUILegacyInlineMessages returns the legacy inline shape the original
// (buggy) tool description taught: components embedded inside createSurface.
// The strict zod adapter rejects it (Unrecognized key(s)); the tool itself
// must NOT pre-reject — the frontend renders its error placeholder instead.
func renderUILegacyInlineMessages() []any {
	return []any{
		map[string]any{
			"version": "v0.9",
			"createSurface": map[string]any{
				"surfaceId": "legacy-1",
				"catalogId": "https://a2ui.org/specification/v0_9/catalogs/basic/catalog.json",
				"components": []any{
					map[string]any{"id": "c1", "component": "Text", "text": "旧内联批次"},
				},
			},
		},
	}
}

// TestRenderUIToolValidBatchPublishesUI pins the happy path: a valid framed
// v0.9 batch publishes one OutboundMessage whose UI.Messages carries the
// input verbatim and whose Content is a non-empty plain-text fallback built
// from the updateComponents frames and the updateDataModel strings; the
// returned ToolResult is silent (success stays out of the tool feedback
// stream — locked for the design doc §2.1 claim).
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
	if !result.Silent {
		t.Error("Silent = false, want silent success (no success noise in tool feedback)")
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
	// Generated fallback must surface literal component text AND data-model
	// strings so non-pico channels stay readable.
	for _, wantText := range []string{"季度体检人数", "2026 Q1: 128 人", "surface surface-1"} {
		if !strings.Contains(msg.Content, wantText) {
			t.Errorf("Content = %q, want it to contain %q", msg.Content, wantText)
		}
	}
	if msg.Channel != "pico" || msg.ChatID != "pico:sess-1" {
		t.Errorf("published to %s:%s, want pico:pico:sess-1", msg.Channel, msg.ChatID)
	}
	if msg.AgentID != "agent-1" || msg.SessionKey != "session-key-1" {
		t.Errorf("AgentID/SessionKey = %q/%q, want agent-1/session-key-1", msg.AgentID, msg.SessionKey)
	}
}

// TestRenderUIToolLegacyInlineBatchStillPublishes: the inline shape (taught
// by the original buggy description) is not pre-rejected by the tool gates —
// the frontend zod layer is the schema authority and renders its error
// placeholder. Keeps publishing verbatim so the fallback Content stays
// readable (also the shape of pre-fix persisted history batches).
func TestRenderUIToolLegacyInlineBatchStillPublishes(t *testing.T) {
	tool, msgBus := newTestRenderUITool(t)
	ctx := WithToolContext(context.Background(), "pico", "pico:sess-1")

	messages := renderUILegacyInlineMessages()
	result := tool.Execute(ctx, map[string]any{"messages": messages})
	if result.IsError {
		t.Fatalf("Execute() IsError = true (inline shape must not be pre-rejected), ForLLM: %s", result.ForLLM)
	}

	msg := waitOutboundMessage(t, msgBus)
	want, err := json.Marshal(messages)
	if err != nil {
		t.Fatalf("marshal test messages: %v", err)
	}
	if string(msg.UI.Messages) != string(want) {
		t.Errorf("UI.Messages = %s, want %s", msg.UI.Messages, want)
	}
	if !strings.Contains(msg.Content, "旧内联批次") {
		t.Errorf("Content = %q, want legacy inline component text in fallback", msg.Content)
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
			name: "message is not an object",
			args: map[string]any{"messages": []any{"x"}},
			want: "must be an object",
		},
		{
			name: "message carries no branch payload",
			args: map[string]any{"messages": []any{map[string]any{"version": "v0.9"}}},
			want: "exactly one of",
		},
		{
			name: "message carries two branch payloads",
			args: map[string]any{"messages": []any{map[string]any{
				"version":          "v0.9",
				"createSurface":    map[string]any{"surfaceId": "s1"},
				"updateComponents": map[string]any{"surfaceId": "s1"},
			}}},
			want: "exactly one of",
		},
		{
			name: "batch without any createSurface envelope",
			args: map[string]any{"messages": []any{map[string]any{
				"version":          "v0.9",
				"updateComponents": map[string]any{"surfaceId": "s1", "components": []any{}},
			}}},
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

// TestRenderUIToolOversizedBatchRejected: frame-shaped batches beyond the
// 256KB cap fail with an explanatory ForLLM and nothing is published.
func TestRenderUIToolOversizedBatchRejected(t *testing.T) {
	tool, msgBus := newTestRenderUITool(t)
	ctx := WithToolContext(context.Background(), "pico", "pico:sess-1")

	big := []any{
		map[string]any{
			"version": "v0.9",
			"createSurface": map[string]any{
				"surfaceId": "s1",
				"catalogId": "https://a2ui.org/specification/v0_9/catalogs/basic/catalog.json",
			},
		},
		map[string]any{
			"version": "v0.9",
			"updateComponents": map[string]any{
				"surfaceId": "s1",
				"components": []any{map[string]any{
					"id": "c1", "component": "Text", "text": strings.Repeat("x", 256*1024),
				}},
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

// TestRenderUIToolSchemaContract pins the tool signature — and, critically,
// that the messages description teaches the FRAME shape (envelope +
// updateComponents [+ updateDataModel]) and no longer the inline
// components-in-createSurface shape the strict zod adapter rejects.
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
		`{"version":"v0.9","createSurface":{"surfaceId":`,
		`"updateComponents"`,
		`"updateDataModel"`,
		"https://a2ui.org/specification/v0_9/catalogs/basic/catalog.json",
	} {
		if !strings.Contains(desc, want) {
			t.Errorf("messages description missing %q: %s", want, desc)
		}
	}
	// The old description taught components/dataModel inlined after
	// catalogId inside createSurface — that exact inline example must be gone.
	for _, banned := range []string{
		`catalog.json","components"`,
		`catalog.json","dataModel"`,
	} {
		if strings.Contains(desc, banned) {
			t.Errorf("messages description still teaches the inline shape (%q): %s", banned, desc)
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
	t.Run("valid frame batch with summary param", func(t *testing.T) {
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
	t.Run("valid frame batch without summary derives frame-aware fallback", func(t *testing.T) {
		argsJSON := `{"messages":` + marshalForTest(t, renderUITestMessages()) + `}`
		messages, fallback, ok := RenderUIHistoryMessages(argsJSON)
		if !ok {
			t.Fatal("ok = false, want extraction to succeed")
		}
		if len(messages) == 0 {
			t.Error("messages is empty")
		}
		for _, wantText := range []string{"季度体检人数", "2026 Q1: 128 人"} {
			if !strings.Contains(fallback, wantText) {
				t.Errorf("fallback = %q, want it to contain %q", fallback, wantText)
			}
		}
	})
	t.Run("legacy inline batch still extracts with readable fallback", func(t *testing.T) {
		// Pre-fix persisted batches used the inline shape; replay keeps
		// deriving a readable fallback from the inline components.
		argsJSON := `{"messages":` + marshalForTest(t, renderUILegacyInlineMessages()) + `}`
		messages, fallback, ok := RenderUIHistoryMessages(argsJSON)
		if !ok {
			t.Fatal("ok = false, want legacy inline extraction to succeed")
		}
		if len(messages) == 0 {
			t.Error("messages is empty")
		}
		if !strings.Contains(fallback, "旧内联批次") {
			t.Errorf("fallback = %q, want legacy inline component text", fallback)
		}
	})
	t.Run("rejected live stays rejected", func(t *testing.T) {
		for name, argsJSON := range map[string]string{
			"invalid json":       `{not json`,
			"empty messages":     `{"messages":[]}`,
			"messages not array": `{"messages":"nope"}`,
			"missing messages":   `{"summary":"x"}`,
			"bad version":        `{"messages":[{"version":"v1.0","createSurface":{"surfaceId":"s"}}]}`,
			"no branch payload":  `{"messages":[{"version":"v0.9"}]}`,
			"no createSurface":   `{"messages":[{"version":"v0.9","updateComponents":{"surfaceId":"s","components":[]}}]}`,
			"message not object": `{"messages":["x"]}`,
			"oversized frame batch": func() string {
				big := []any{
					map[string]any{"version": "v0.9", "createSurface": map[string]any{"surfaceId": "s"}},
					map[string]any{"version": "v0.9", "updateComponents": map[string]any{
						"surfaceId": "s",
						"components": []any{map[string]any{
							"id": "c", "component": "Text", "text": strings.Repeat("x", 256*1024),
						}},
					}},
				}
				return `{"messages":` + marshalForTest(t, big) + `}`
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
