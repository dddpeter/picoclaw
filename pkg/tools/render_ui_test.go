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
					map[string]any{"id": "root", "component": "Card", "child": "col"},
					map[string]any{"id": "col", "component": "Column", "children": []any{"t1", "t3"}},
					map[string]any{"id": "t1", "component": "Text", "text": "季度体检人数"},
					map[string]any{"id": "t3", "component": "Text", "text": map[string]any{"path": "/rows/0"}},
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
	// Streamed-update merge (phase 2): the pico layer keys its (session,
	// surfaceId) registry on UIMeta.SurfaceID — it must carry the batch's
	// single surface id.
	if msg.UI.SurfaceID != "surface-1" {
		t.Errorf("UI.SurfaceID = %q, want %q (the batch's single surface id)", msg.UI.SurfaceID, "surface-1")
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

// renderUIMixedSurfaceMessages returns an otherwise-valid batch whose frames
// target two different surfaces (createSurface on surface-a,
// updateComponents on surface-b): streamed-update merge (phase 2) attributes
// a batch to one card by (session, surfaceId), so a mixed batch cannot be
// attributed and must be rejected.
func renderUIMixedSurfaceMessages() []any {
	return []any{
		map[string]any{
			"version":       "v0.9",
			"createSurface": map[string]any{"surfaceId": "surface-a", "catalogId": "https://a2ui.org/specification/v0_9/catalogs/basic/catalog.json"},
		},
		map[string]any{
			"version": "v0.9",
			"updateComponents": map[string]any{
				"surfaceId": "surface-b",
				"components": []any{
					map[string]any{"id": "root", "component": "Card", "child": "t1"},
					map[string]any{"id": "t1", "component": "Text", "text": "x"},
				},
			},
		},
	}
}

// TestRenderUIToolMixedSurfaceIDRejected: every branch payload in the batch
// must carry the same non-empty surfaceId — the pico merge registry keys on
// it, so a batch mixing two ids fails as a retryable tool error listing the
// conflicting ids, and nothing publishes.
func TestRenderUIToolMixedSurfaceIDRejected(t *testing.T) {
	tool, msgBus := newTestRenderUITool(t)
	ctx := WithToolContext(context.Background(), "pico", "pico:sess-1")

	result := tool.Execute(ctx, map[string]any{"messages": renderUIMixedSurfaceMessages()})
	if !result.IsError {
		t.Fatalf("Execute() IsError = false, want mixed-surfaceId rejection, ForLLM: %s", result.ForLLM)
	}
	for _, want := range []string{"surface-a", "surface-b"} {
		if !strings.Contains(result.ForLLM, want) {
			t.Errorf("ForLLM = %q, want it to list the conflicting id %q", result.ForLLM, want)
		}
	}
	assertNoOutboundMessage(t, msgBus)
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
		{
			// The live empty-card incident (2026-11-14): the renderer mounts
			// the tree rooted at the component with id "root" — a batch whose
			// root is named anything else delivers components that never
			// render.
			name: "no component named root",
			args: map[string]any{"messages": []any{
				map[string]any{
					"version":       "v0.9",
					"createSurface": map[string]any{"surfaceId": "s1", "catalogId": "https://a2ui.org/specification/v0_9/catalogs/basic/catalog.json"},
				},
				map[string]any{
					"version": "v0.9",
					"updateComponents": map[string]any{"surfaceId": "s1", "components": []any{
						map[string]any{"id": "c1", "component": "Card", "child": "c2"},
						map[string]any{"id": "c2", "component": "Text", "text": "hello"},
					}},
				},
			}},
			want: `id "root"`,
		},
		{
			// Same incident, second layer: an empty Row left four Text
			// components orphaned — they would never render even with a
			// proper root.
			name: "components unreachable from root",
			args: map[string]any{"messages": []any{
				map[string]any{
					"version":       "v0.9",
					"createSurface": map[string]any{"surfaceId": "s1", "catalogId": "https://a2ui.org/specification/v0_9/catalogs/basic/catalog.json"},
				},
				map[string]any{
					"version": "v0.9",
					"updateComponents": map[string]any{"surfaceId": "s1", "components": []any{
						map[string]any{"id": "root", "component": "Card", "child": "c2"},
						map[string]any{"id": "c2", "component": "Text", "text": "hello"},
						map[string]any{"id": "orphan", "component": "Text", "text": "stranded"},
					}},
				},
			}},
			want: "not reachable",
		},
		{
			// Third live incident (2026-11-14, second retest): the model used
			// the same id for a Column container and the Row inside it — the
			// renderer throws "Duplicate component ID" and the batch dies.
			name: "duplicate component id",
			args: map[string]any{"messages": []any{
				map[string]any{
					"version":       "v0.9",
					"createSurface": map[string]any{"surfaceId": "s1", "catalogId": "https://a2ui.org/specification/v0_9/catalogs/basic/catalog.json"},
				},
				map[string]any{
					"version": "v0.9",
					"updateComponents": map[string]any{"surfaceId": "s1", "components": []any{
						map[string]any{"id": "root", "component": "Card", "child": "c2"},
						map[string]any{"id": "c2", "component": "Column", "children": []any{"r1"}},
						map[string]any{"id": "r1", "component": "Column", "children": []any{}},
						map[string]any{"id": "r1", "component": "Row", "children": []any{"x"}},
						map[string]any{"id": "x", "component": "Text", "text": "dup"},
					}},
				},
			}},
			want: "duplicate component id",
		},
		{
			name: "catalogId not the registered basic catalog",
			args: map[string]any{"messages": []any{map[string]any{
				"version":       "v0.9",
				"createSurface": map[string]any{"surfaceId": "s1", "catalogId": "https://example.com/catalog.json"},
			}}},
			want: "registered catalog",
		},
		{
			// Streamed-update merge (phase 2) attributes each batch to one
			// (session, surfaceId) card: a frame whose surfaceId is missing
			// or empty cannot be attributed and is rejected.
			name: "branch payload with empty surfaceId",
			args: map[string]any{"messages": []any{
				map[string]any{
					"version":       "v0.9",
					"createSurface": map[string]any{"surfaceId": "s1", "catalogId": "https://a2ui.org/specification/v0_9/catalogs/basic/catalog.json"},
				},
				map[string]any{
					"version": "v0.9",
					"updateComponents": map[string]any{"surfaceId": "", "components": []any{
						map[string]any{"id": "root", "component": "Text", "text": "x"},
					}},
				},
			}},
			want: "non-empty",
		},
		{
			name: "createSurface without surfaceId",
			args: map[string]any{"messages": []any{map[string]any{
				"version":       "v0.9",
				"createSurface": map[string]any{"catalogId": "https://a2ui.org/specification/v0_9/catalogs/basic/catalog.json"},
			}}},
			want: "non-empty",
		},
		{
			// Fourth live incident (2026-11-14, third retest): the model
			// listed every Text in BOTH the root Column and its owning Row —
			// each value rendered twice (row layout + stacked duplicate).
			name: "component referenced by multiple parents",
			args: map[string]any{"messages": []any{
				map[string]any{
					"version":       "v0.9",
					"createSurface": map[string]any{"surfaceId": "s1", "catalogId": "https://a2ui.org/specification/v0_9/catalogs/basic/catalog.json"},
				},
				map[string]any{
					"version": "v0.9",
					"updateComponents": map[string]any{"surfaceId": "s1", "components": []any{
						map[string]any{"id": "root", "component": "Card", "child": "col"},
						map[string]any{"id": "col", "component": "Column", "children": []any{"row", "t1", "t2"}},
						map[string]any{"id": "row", "component": "Row", "children": []any{"t1", "t2"}},
						map[string]any{"id": "t1", "component": "Text", "text": "第一季度"},
						map[string]any{"id": "t2", "component": "Text", "text": "1,280"},
					}},
				},
			}},
			want: "more than one parent",
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
					"id": "root", "component": "Text", "text": strings.Repeat("x", 256*1024),
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
		// Root-rule teaching locked after the live empty-card incident: the
		// renderer mounts the tree at the component with id "root".
		`id MUST be "root"`,
		`reachable from "root"`,
		// Uniqueness + exact-catalogId teaching locked after the third live
		// incident (duplicate year ids killed the batch); tree-invariant
		// teaching locked after the fourth (values rendered twice).
		`UNIQUE across the whole components array`,
		`catalogId must be EXACTLY`,
		`at most ONE parent's child/children list`,
		// Interaction teaching locked with the action-uplink task: Button
		// event.context path bindings collect form values at click time and
		// the user's interaction arrives as a prefixed user message.
		`event.context`,
		`A2UI 交互`,
		// Update-only teaching locked with task 4 (streamed increments): the
		// model can call render_ui again with delta frames only — same
		// surfaceId, no createSurface — to patch an existing card.
		`updateComponents/updateDataModel frames only`,
		`same surfaceId, no createSurface`,
		`update an existing surface in place`,
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
	t.Run("mixed surfaceId batch stays rejected like live", func(t *testing.T) {
		// The single-surfaceId gate rides the shared envelope gate, so
		// history replay rejects a batch the live tool would refuse — an
		// otherwise-valid mixed batch must be the ONLY reason it fails.
		argsJSON := `{"messages":` + marshalForTest(t, renderUIMixedSurfaceMessages()) + `}`
		if _, _, ok := RenderUIHistoryMessages(argsJSON); ok {
			t.Fatal("ok = true, want mixed-surfaceId batch to stay rejected")
		}
	})
	t.Run("update-only delta returns the delta batch", func(t *testing.T) {
		// Task 4 (streamed increments): a delta batch the live tool now
		// accepts must extract from history too — ok=true with the verbatim
		// delta frames.
		argsJSON := `{"messages":` + marshalForTest(t, renderUIUpdateOnlyMessages()) + `,"summary":"追加行"}`
		messages, fallback, ok := RenderUIHistoryMessages(argsJSON)
		if !ok {
			t.Fatal("ok = false, want update-only delta extraction to succeed")
		}
		want := marshalForTest(t, renderUIUpdateOnlyMessages())
		if string(messages) != want {
			t.Errorf("messages = %s, want %s", messages, want)
		}
		if fallback != "追加行" {
			t.Errorf("fallback = %q, want summary param", fallback)
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
			// Empty components stay a delta-local rejection — "no
			// createSurface" alone is no longer one (task 4).
			"update-only delta with empty components": `{"messages":[{"version":"v0.9","updateComponents":{"surfaceId":"s","components":[]}}]}`,
			"update-only delta with duplicate id": `{"messages":[{"version":"v0.9","updateComponents":{"surfaceId":"s","components":[` +
				`{"id":"t","component":"Text","text":"a"},{"id":"t","component":"Text","text":"b"}]}}]}`,
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

// renderUIUpdateOnlyMessages returns a valid update-only delta batch (task 4,
// streamed increments): updateComponents + updateDataModel frames reusing an
// existing surfaceId, NO createSurface envelope. The delta patches a card
// that already rendered live, so only delta-local checks apply — root
// existence, reachability and the multi-parent invariant are skipped (the
// delta components hang off the tree that already lives on the frontend; the
// component id here is deliberately NOT "root").
func renderUIUpdateOnlyMessages() []any {
	return []any{
		map[string]any{
			"version": "v0.9",
			"updateComponents": map[string]any{
				"surfaceId": "surface-1",
				"components": []any{
					map[string]any{"id": "t9", "component": "Text", "text": "追加行"},
				},
			},
		},
		map[string]any{
			"version": "v0.9",
			"updateDataModel": map[string]any{
				"surfaceId": "surface-1",
				"path":      "/",
				"value":     map[string]any{"rows": []any{"2026 Q3: 172 人"}},
			},
		},
	}
}

// TestRenderUIToolUpdateOnlyBatchPublishesUI pins the task-4 gate opening:
// a batch of delta frames only (no createSurface envelope) publishes — the
// pico layer (phase 2) merges it into the live card keyed by
// UIMeta.SurfaceID. Delta-local gates apply; the tree gates
// (root/reachability/multi-parent) do not.
func TestRenderUIToolUpdateOnlyBatchPublishesUI(t *testing.T) {
	tool, msgBus := newTestRenderUITool(t)
	ctx := WithToolSessionContext(
		WithToolContext(context.Background(), "pico", "pico:sess-1"),
		"agent-1", "session-key-1", nil,
	)

	messages := renderUIUpdateOnlyMessages()
	result := tool.Execute(ctx, map[string]any{"messages": messages})
	if result.IsError {
		t.Fatalf("Execute() IsError = true, ForLLM: %s", result.ForLLM)
	}
	if !result.Silent {
		t.Error("Silent = false, want silent success like the create path")
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
	if msg.UI.SurfaceID != "surface-1" {
		t.Errorf("UI.SurfaceID = %q, want %q (the pico merge registry key)", msg.UI.SurfaceID, "surface-1")
	}
	// The delta text must surface in the plain-text fallback so non-pico
	// channels stay readable.
	for _, wantText := range []string{"追加行", "2026 Q3: 172 人"} {
		if !strings.Contains(msg.Content, wantText) {
			t.Errorf("Content = %q, want it to contain %q", msg.Content, wantText)
		}
	}
}

// TestRenderUIToolUpdateOnlyDataModelOnlyBatchPublishesUI: a dataModel-only
// delta (no components frame, no createSurface) is a valid streamed update
// too — form values can change without touching the tree.
func TestRenderUIToolUpdateOnlyDataModelOnlyBatchPublishesUI(t *testing.T) {
	tool, msgBus := newTestRenderUITool(t)
	ctx := WithToolContext(context.Background(), "pico", "pico:sess-1")

	messages := []any{map[string]any{
		"version":         "v0.9",
		"updateDataModel": map[string]any{"surfaceId": "surface-1", "path": "/", "value": map[string]any{"count": 3}},
	}}
	result := tool.Execute(ctx, map[string]any{"messages": messages})
	if result.IsError {
		t.Fatalf("Execute() IsError = true, ForLLM: %s", result.ForLLM)
	}
	msg := waitOutboundMessage(t, msgBus)
	if msg.UI == nil || msg.UI.SurfaceID != "surface-1" {
		t.Fatalf("UI = %+v, want UIMeta with SurfaceID surface-1", msg.UI)
	}
}

// TestRenderUIToolUpdateOnlyDeltaStillGated: the delta-local checks still
// apply to update-only batches — version / branch count / key subsets /
// component names / component ids / intra-batch duplicate ids / batch-
// internal multi-parent references / single surfaceId / size cap. Only root
// existence and reachability are skipped (and the cross-delta side of
// multi-parent references — see TestRenderUIToolUpdateOnlyDeltaSkipsTreeGates).
func TestRenderUIToolUpdateOnlyDeltaStillGated(t *testing.T) {
	comp := func(id string) map[string]any {
		return map[string]any{"id": id, "component": "Text", "text": "x"}
	}
	cases := []struct {
		name     string
		messages []any
		want     string
	}{
		{
			name:     "delta frame without branch payload",
			messages: []any{map[string]any{"version": "v0.9"}},
			want:     "exactly one of",
		},
		{
			name: "delta frame carries two branch payloads",
			messages: []any{map[string]any{
				"version":          "v0.9",
				"updateComponents": map[string]any{"surfaceId": "s1", "components": []any{comp("t1")}},
				"updateDataModel":  map[string]any{"surfaceId": "s1", "path": "/", "value": map[string]any{}},
			}},
			want: "exactly one of",
		},
		{
			name: "bad version in delta frame",
			messages: []any{map[string]any{
				"version":          "v0.8",
				"updateComponents": map[string]any{"surfaceId": "s1", "components": []any{comp("t1")}},
			}},
			want: "v0.9",
		},
		{
			name: "unknown key in delta frame",
			messages: []any{map[string]any{
				"version":          "v0.9",
				"updateComponents": map[string]any{"surfaceId": "s1", "components": []any{comp("t1")}, "title": "x"},
			}},
			want: "invalid key",
		},
		{
			name: "unknown component name in delta",
			messages: []any{map[string]any{
				"version":          "v0.9",
				"updateComponents": map[string]any{"surfaceId": "s1", "components": []any{map[string]any{"id": "t1", "component": "Table", "text": "x"}}},
			}},
			want: "unknown component",
		},
		{
			name: "delta component missing id",
			messages: []any{map[string]any{
				"version":          "v0.9",
				"updateComponents": map[string]any{"surfaceId": "s1", "components": []any{map[string]any{"component": "Text", "text": "x"}}},
			}},
			want: `"id"`,
		},
		{
			name: "duplicate component id within delta",
			messages: []any{map[string]any{
				"version": "v0.9",
				"updateComponents": map[string]any{"surfaceId": "s1", "components": []any{
					map[string]any{"id": "t1", "component": "Text", "text": "a"},
					map[string]any{"id": "t1", "component": "Text", "text": "b"},
				}},
			}},
			want: "duplicate component id",
		},
		{
			// Fix round 1 (controller ruling): batch-internal multi-parent
			// references are delta-local checkable — two parents INSIDE the
			// delta referencing the same id render it once per reference
			// (the fourth live incident's pattern) — and must be rejected as
			// a retryable error. Only references crossing the delta boundary
			// are invisible to the gate.
			name: "delta component referenced by two parents inside the delta",
			messages: []any{map[string]any{
				"version": "v0.9",
				"updateComponents": map[string]any{"surfaceId": "s1", "components": []any{
					map[string]any{"id": "row", "component": "Row", "children": []any{"t1", "t2"}},
					map[string]any{"id": "col", "component": "Column", "children": []any{"t1"}},
					map[string]any{"id": "t1", "component": "Text", "text": "shared"},
					map[string]any{"id": "t2", "component": "Text", "text": "x"},
				}},
			}},
			want: "more than one parent",
		},
		{
			name: "delta mixes two surfaceIds",
			messages: []any{
				map[string]any{
					"version":          "v0.9",
					"updateComponents": map[string]any{"surfaceId": "s-a", "components": []any{comp("t1")}},
				},
				map[string]any{
					"version":         "v0.9",
					"updateDataModel": map[string]any{"surfaceId": "s-b", "path": "/", "value": map[string]any{}},
				},
			},
			want: "s-a",
		},
		{
			name: "delta frame with empty surfaceId",
			messages: []any{map[string]any{
				"version":          "v0.9",
				"updateComponents": map[string]any{"surfaceId": "", "components": []any{comp("t1")}},
			}},
			want: "non-empty",
		},
		{
			name: "delta components array empty",
			messages: []any{map[string]any{
				"version":          "v0.9",
				"updateComponents": map[string]any{"surfaceId": "s1", "components": []any{}},
			}},
			want: "non-empty array",
		},
		{
			name: "oversized delta",
			messages: []any{map[string]any{
				"version":          "v0.9",
				"updateComponents": map[string]any{"surfaceId": "s1", "components": []any{map[string]any{"id": "t1", "component": "Text", "text": strings.Repeat("x", 256*1024)}}},
			}},
			want: "large",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tool, msgBus := newTestRenderUITool(t)
			ctx := WithToolContext(context.Background(), "pico", "pico:sess-1")

			result := tool.Execute(ctx, map[string]any{"messages": tc.messages})
			if !result.IsError {
				t.Fatalf("Execute() IsError = false, want delta-local rejection; ForLLM: %s", result.ForLLM)
			}
			if !strings.Contains(result.ForLLM, tc.want) {
				t.Errorf("ForLLM = %q, want it to mention %q", result.ForLLM, tc.want)
			}
			assertNoOutboundMessage(t, msgBus)
		})
	}
}

// TestRenderUIHistoryFold pins the call-sequence folding (task 4, A2UI web
// render history replay): read render_ui calls in order, fold by surfaceId —
// a createSurface batch establishes the card, later update-only batches
// merge their frames into it (components frames appended, updateDataModel
// frames overwritten per path), a second createSurface for the same
// surfaceId keeps the LAST card, an orphan update-only batch (no matching
// card) stays text-only, and calls the live gates reject contribute nothing.
func TestRenderUIHistoryFold(t *testing.T) {
	createJSON := `{"messages":` + marshalForTest(t, renderUITestMessages()) + `,"summary":"季度体检人数表"}`
	updateJSON := `{"messages":` + marshalForTest(t, renderUIUpdateOnlyMessages()) + `}`

	t.Run("create then update folds into one card with final frames", func(t *testing.T) {
		items := RenderUIHistoryFold([]string{createJSON, updateJSON})
		if len(items) != 2 {
			t.Fatalf("items length = %d, want 2", len(items))
		}
		// The update call merged into the card: nothing resurfaces at its
		// own position.
		if len(items[1].A2UI) != 0 || items[1].Fallback != "" {
			t.Errorf("update item = {a2ui:%s fallback:%q}, want empty (frames ride the card)", items[1].A2UI, items[1].Fallback)
		}
		if len(items[0].A2UI) == 0 {
			t.Fatal("create item carries no folded card")
		}
		var frames []map[string]any
		if err := json.Unmarshal(items[0].A2UI, &frames); err != nil {
			t.Fatalf("folded card is not a frame array: %v (%s)", err, items[0].A2UI)
		}
		if len(frames) != 4 {
			t.Fatalf("folded frames = %d, want 4 (create + components + overwritten dataModel + appended delta components): %s", len(frames), items[0].A2UI)
		}
		if _, ok := frames[0]["createSurface"]; !ok {
			t.Errorf("frames[0] = %s, want the createSurface envelope", frames[0])
		}
		uc, ok := frames[1]["updateComponents"].(map[string]any)
		if !ok {
			t.Fatalf("frames[1] = %s, want the original updateComponents frame", frames[1])
		}
		comps, _ := uc["components"].([]any)
		if len(comps) != 4 {
			t.Errorf("frames[1] components = %d, want 4 (the original tree)", len(comps))
		}
		dm, ok := frames[2]["updateDataModel"].(map[string]any)
		if !ok {
			t.Fatalf("frames[2] = %s, want the updateDataModel frame", frames[2])
		}
		if dm["path"] != "/" {
			t.Errorf("dataModel path = %v, want /", dm["path"])
		}
		value, _ := dm["value"].(map[string]any)
		rows, _ := value["rows"].([]any)
		if len(rows) != 1 || rows[0] != "2026 Q3: 172 人" {
			t.Errorf("dataModel rows = %v, want the last update's value (frame overwritten per path)", rows)
		}
		uc, ok = frames[3]["updateComponents"].(map[string]any)
		if !ok {
			t.Fatalf("frames[3] = %s, want the appended delta components frame", frames[3])
		}
		comps, _ = uc["components"].([]any)
		if len(comps) != 1 {
			t.Errorf("frames[3] components = %d, want 1 (the delta frame)", len(comps))
		}
		// The card caption stays the create call's summary param.
		if items[0].Fallback != "季度体检人数表" {
			t.Errorf("card fallback = %q, want the create call's summary", items[0].Fallback)
		}
	})

	t.Run("second create for the same surfaceId keeps the last card", func(t *testing.T) {
		secondCreate := []any{
			map[string]any{
				"version":       "v0.9",
				"createSurface": map[string]any{"surfaceId": "surface-1", "catalogId": "https://a2ui.org/specification/v0_9/catalogs/basic/catalog.json"},
			},
			map[string]any{
				"version":          "v0.9",
				"updateComponents": map[string]any{"surfaceId": "surface-1", "components": []any{map[string]any{"id": "root", "component": "Text", "text": "重画"}}},
			},
		}
		secondJSON := `{"messages":` + marshalForTest(t, secondCreate) + `,"summary":"第二张"}`
		items := RenderUIHistoryFold([]string{createJSON, updateJSON, secondJSON})
		if len(items) != 3 {
			t.Fatalf("items length = %d, want 3", len(items))
		}
		if len(items[0].A2UI) != 0 || items[0].Fallback != "" {
			t.Errorf("first create item = {a2ui:%s fallback:%q}, want empty (superseded by the last create)", items[0].A2UI, items[0].Fallback)
		}
		if len(items[1].A2UI) != 0 || items[1].Fallback != "" {
			t.Errorf("update item = {a2ui:%s fallback:%q}, want empty (merged into the discarded card)", items[1].A2UI, items[1].Fallback)
		}
		if len(items[2].A2UI) == 0 {
			t.Fatal("second create item carries no card")
		}
		var frames []map[string]any
		if err := json.Unmarshal(items[2].A2UI, &frames); err != nil {
			t.Fatalf("card is not a frame array: %v (%s)", err, items[2].A2UI)
		}
		if len(frames) != 2 {
			t.Errorf("card frames = %d, want 2 (the second create's batch)", len(frames))
		}
		if items[2].Fallback != "第二张" {
			t.Errorf("card fallback = %q, want the surviving create's summary", items[2].Fallback)
		}
	})

	t.Run("orphan update-only batch surfaces text only", func(t *testing.T) {
		items := RenderUIHistoryFold([]string{updateJSON})
		if len(items) != 1 {
			t.Fatalf("items length = %d, want 1", len(items))
		}
		if len(items[0].A2UI) != 0 {
			t.Errorf("orphan update produced a card: %s", items[0].A2UI)
		}
		// No summary param: the fallback is derived from the delta frames.
		for _, wantText := range []string{"追加行", "2026 Q3: 172 人"} {
			if !strings.Contains(items[0].Fallback, wantText) {
				t.Errorf("fallback = %q, want tree-derived text containing %q", items[0].Fallback, wantText)
			}
		}
	})

	t.Run("calls the live gates reject contribute nothing", func(t *testing.T) {
		dupDelta := []any{map[string]any{
			"version": "v0.9",
			"updateComponents": map[string]any{"surfaceId": "s", "components": []any{
				map[string]any{"id": "t", "component": "Text", "text": "a"},
				map[string]any{"id": "t", "component": "Text", "text": "b"},
			}},
		}}
		oversized := []any{map[string]any{
			"version":          "v0.9",
			"updateComponents": map[string]any{"surfaceId": "s", "components": []any{map[string]any{"id": "t", "component": "Text", "text": strings.Repeat("x", 256*1024)}}},
		}}
		items := RenderUIHistoryFold([]string{
			`{not json`,
			`{"messages":[]}`,
			`{"messages":[{"version":"v1.0","createSurface":{"surfaceId":"s"}}]}`,
			`{"messages":` + marshalForTest(t, dupDelta) + `}`,
			`{"messages":` + marshalForTest(t, oversized) + `}`,
		})
		for i, item := range items {
			if len(item.A2UI) != 0 || item.Fallback != "" {
				t.Errorf("items[%d] = {a2ui:%s fallback:%q}, want empty (rejected live)", i, item.A2UI, item.Fallback)
			}
		}
	})

	t.Run("single create matches RenderUIHistoryMessages", func(t *testing.T) {
		items := RenderUIHistoryFold([]string{createJSON})
		messages, fallback, ok := RenderUIHistoryMessages(createJSON)
		if !ok {
			t.Fatal("RenderUIHistoryMessages ok = false")
		}
		if string(items[0].A2UI) != string(messages) {
			t.Errorf("fold a2ui = %s, want %s", items[0].A2UI, messages)
		}
		if items[0].Fallback != fallback {
			t.Errorf("fold fallback = %q, want %q", items[0].Fallback, fallback)
		}
	})
}

// TestRenderUIToolUpdateOnlyDeltaSkipsTreeGates pins the controller
// correction: root existence and reachability are meaningless inside a delta
// (the delta components hang off the tree that already lives on the frontend
// — a BFS inside the delta would flag every component "orphan"), so deltas
// carrying unrooted or unattached components still publish. Cross-delta
// references do not trigger the multi-parent check either: a delta component
// pointing at an id that lives in the already-rendered tree is invisible to
// the gate (that id is not in the delta's byID). Batch-INTERNAL multi-parent
// references are a different matter — they are delta-local checkable and
// rejected (TestRenderUIToolUpdateOnlyDeltaStillGated, fix round 1). The
// frontend processor re-validates the merged tree when applying the delta
// (accepted limitation).
func TestRenderUIToolUpdateOnlyDeltaSkipsTreeGates(t *testing.T) {
	cases := []struct {
		name       string
		components []any
	}{
		{
			name:       "delta components without a root id",
			components: []any{map[string]any{"id": "t1", "component": "Text", "text": "orphan"}},
		},
		{
			name: "delta components all unreachable from the live tree",
			components: []any{
				map[string]any{"id": "a", "component": "Text", "text": "a"},
				map[string]any{"id": "b", "component": "Text", "text": "b"},
			},
		},
		{
			// Fix round 1 (controller ruling): the multi-parent check runs on
			// the delta's own byID, so a child/children reference to an id
			// outside the delta cannot double-render inside the delta and
			// must not be flagged.
			name: "delta component referencing an id outside the delta",
			components: []any{
				map[string]any{"id": "wrap", "component": "Column", "children": []any{"existing-node", "t2"}},
				map[string]any{"id": "t2", "component": "Text", "text": "new"},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tool, msgBus := newTestRenderUITool(t)
			ctx := WithToolContext(context.Background(), "pico", "pico:sess-1")

			messages := []any{map[string]any{
				"version":          "v0.9",
				"updateComponents": map[string]any{"surfaceId": "surface-1", "components": tc.components},
			}}
			result := tool.Execute(ctx, map[string]any{"messages": messages})
			if result.IsError {
				t.Fatalf("Execute() IsError = true, want tree gates skipped for delta; ForLLM: %s", result.ForLLM)
			}
			msg := waitOutboundMessage(t, msgBus)
			if msg.UI == nil || msg.UI.SurfaceID != "surface-1" {
				t.Fatalf("UI = %+v, want UIMeta with SurfaceID surface-1", msg.UI)
			}
		})
	}
}
