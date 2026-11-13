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
// Nothing published that shape before the frame-shape fix ever shipped, so
// the tool gates now reject it (invalid createSurface key) — the model gets
// a retryable error instead of a card the frontend renders as a dead
// placeholder.
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

// TestRenderUIToolLegacyInlineBatchRejected: the inline shape (taught by the
// original buggy description) carries components inside createSurface — an
// invalid key the strict v0.9 schema rejects. The tool gates now reject it
// so the model gets a retryable error instead of shipping a card the
// frontend renders as a dead placeholder (no pre-fix batch ever shipped, so
// legacy tolerance would only mask model mistakes).
func TestRenderUIToolLegacyInlineBatchRejected(t *testing.T) {
	tool, msgBus := newTestRenderUITool(t)
	ctx := WithToolContext(context.Background(), "pico", "pico:sess-1")

	result := tool.Execute(ctx, map[string]any{"messages": renderUILegacyInlineMessages()})
	if !result.IsError {
		t.Fatalf("Execute() IsError = false, want inline-shape rejection, ForLLM: %s", result.ForLLM)
	}
	if !strings.Contains(result.ForLLM, "invalid key") {
		t.Errorf("ForLLM = %q, want invalid-key explanation", result.ForLLM)
	}
	assertNoOutboundMessage(t, msgBus)
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

// TestRenderUIToolNonStringSummaryFallsBackToTree: a non-string summary
// argument (the model emitting a number) misses the live string assertion
// and silently downgrades to the tree-derived fallback — the card still
// publishes. Pins the live side of the live/history parity locked in
// TestRenderUIHistoryMessages ("non-string summary falls back to tree
// derivation like live").
func TestRenderUIToolNonStringSummaryFallsBackToTree(t *testing.T) {
	tool, msgBus := newTestRenderUITool(t)
	ctx := WithToolContext(context.Background(), "pico", "pico:sess-1")

	result := tool.Execute(ctx, map[string]any{
		"messages": renderUITestMessages(),
		"summary":  123,
	})
	if result.IsError {
		t.Fatalf("Execute() IsError = true (non-string summary must downgrade, not reject), ForLLM: %s", result.ForLLM)
	}

	msg := waitOutboundMessage(t, msgBus)
	if msg.UI == nil {
		t.Fatal("published message UI = nil, want UIMeta (card still publishes)")
	}
	for _, wantText := range []string{"季度体检人数", "2026 Q1: 128 人"} {
		if !strings.Contains(msg.Content, wantText) {
			t.Errorf("Content = %q, want tree-derived fallback containing %q", msg.Content, wantText)
		}
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
				"version":       "v0.9",
				"createSurface": map[string]any{"surfaceId": "s1", "catalogId": "https://a2ui.org/specification/v0_9/catalogs/basic/catalog.json"},
				"updateComponents": map[string]any{"surfaceId": "s1", "components": []any{
					map[string]any{"id": "c1", "component": "Text", "text": "x"},
				}},
			}}},
			want: "exactly one of",
		},
		{
			name: "batch without any createSurface envelope",
			args: map[string]any{"messages": []any{map[string]any{
				"version":         "v0.9",
				"updateDataModel": map[string]any{"surfaceId": "s1", "path": "/", "value": map[string]any{}},
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
		{
			// The live incident (2026-11-13): the model added a title to
			// createSurface — an unknown key the strict schema rejects, which
			// used to publish a batch the frontend rendered as a dead
			// placeholder. The gate must turn it into a retryable error.
			name: "createSurface carries an extra title key",
			args: map[string]any{"messages": []any{map[string]any{
				"version": "v0.9",
				"createSurface": map[string]any{
					"surfaceId": "s1",
					"catalogId": "https://a2ui.org/specification/v0_9/catalogs/basic/catalog.json",
					"title":     "体检表",
				},
			}}},
			want: "invalid key",
		},
		{
			name: "updateComponents carries an unknown key",
			args: map[string]any{"messages": []any{
				map[string]any{
					"version":       "v0.9",
					"createSurface": map[string]any{"surfaceId": "s1", "catalogId": "https://a2ui.org/specification/v0_9/catalogs/basic/catalog.json"},
				},
				map[string]any{
					"version":          "v0.9",
					"updateComponents": map[string]any{"surfaceId": "s1", "components": []any{}, "title": "x"},
				},
			}},
			want: "invalid key",
		},
		{
			name: "unknown component name",
			args: map[string]any{"messages": []any{
				map[string]any{
					"version":       "v0.9",
					"createSurface": map[string]any{"surfaceId": "s1", "catalogId": "https://a2ui.org/specification/v0_9/catalogs/basic/catalog.json"},
				},
				map[string]any{
					"version": "v0.9",
					"updateComponents": map[string]any{"surfaceId": "s1", "components": []any{
						map[string]any{"id": "t1", "component": "Table", "children": []any{"x"}},
					}},
				},
			}},
			want: "unknown component",
		},
		{
			name: "component missing id",
			args: map[string]any{"messages": []any{
				map[string]any{
					"version":       "v0.9",
					"createSurface": map[string]any{"surfaceId": "s1", "catalogId": "https://a2ui.org/specification/v0_9/catalogs/basic/catalog.json"},
				},
				map[string]any{
					"version": "v0.9",
					"updateComponents": map[string]any{"surfaceId": "s1", "components": []any{
						map[string]any{"component": "Text", "text": "hello"},
					}},
				},
			}},
			want: `"id"`,
		},
		{
			name: "updateComponents with empty components array",
			args: map[string]any{"messages": []any{
				map[string]any{
					"version":       "v0.9",
					"createSurface": map[string]any{"surfaceId": "s1", "catalogId": "https://a2ui.org/specification/v0_9/catalogs/basic/catalog.json"},
				},
				map[string]any{
					"version":          "v0.9",
					"updateComponents": map[string]any{"surfaceId": "s1", "components": []any{}},
				},
			}},
			want: "non-empty array",
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
		// Strict-schema teaching locked after the live title-in-createSurface
		// incident: the envelope key subset, the per-component strict props,
		// and the no-styling warning.
		`createSurface allows ONLY surfaceId + catalogId`,
		`NO styling props`,
		`unknown prop, unknown component name, or missing id`,
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
	t.Run("non-string summary falls back to tree derivation like live", func(t *testing.T) {
		// Live: the args["summary"].(string) assertion miss silently
		// downgrades to the tree-derived fallback and the card still
		// publishes. History must agree — a numeric summary must not fail
		// the whole arguments decode and silently drop the card on replay.
		argsJSON := `{"messages":` + marshalForTest(t, renderUITestMessages()) + `,"summary":123}`
		messages, fallback, ok := RenderUIHistoryMessages(argsJSON)
		if !ok {
			t.Fatal("ok = false, want extraction to succeed (non-string summary downgrades, not rejects)")
		}
		want := marshalForTest(t, renderUITestMessages())
		if string(messages) != want {
			t.Errorf("messages = %s, want %s", messages, want)
		}
		for _, wantText := range []string{"季度体检人数", "2026 Q1: 128 人"} {
			if !strings.Contains(fallback, wantText) {
				t.Errorf("fallback = %q, want tree-derived fallback containing %q", fallback, wantText)
			}
		}
	})
	t.Run("legacy inline batch stays rejected like live", func(t *testing.T) {
		// The inline shape is rejected live (invalid createSurface key) and
		// never shipped, so history replay must reject it the same way —
		// the shared envelope gate enforces that for free.
		argsJSON := `{"messages":` + marshalForTest(t, renderUILegacyInlineMessages()) + `}`
		_, _, ok := RenderUIHistoryMessages(argsJSON)
		if ok {
			t.Fatal("ok = true, want legacy inline batch to stay rejected")
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
