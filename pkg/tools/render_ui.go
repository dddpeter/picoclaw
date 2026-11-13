package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/bus"
)

const (
	// RenderUIToolName is the registered tool name (snake_case convention).
	RenderUIToolName = "render_ui"

	// renderUIMaxBytes caps the serialized A2UI batch (256KB): the model can
	// emit hundreds of KB of components, and the tool-side cap is the first
	// line of defense before the frontend zod layer.
	renderUIMaxBytes = 256 * 1024

	// renderUIPublishTimeout bounds the fire-and-forget bus publish.
	renderUIPublishTimeout = 5 * time.Second

	// renderUIVersionPrefix pins the supported A2UI envelope version family
	// (v0.9 and point releases like v0.9.1 share the adapter).
	renderUIVersionPrefix = "v0.9"
)

// RenderUITool publishes A2UI v0.9 messages into the chat stream (A2UI web
// render). The model calls render_ui mid-turn with a message batch; the tool
// applies cheap validation (json.Valid → per-message envelope check → size
// cap) and pushes one OutboundMessage whose UI.Messages carries the raw JSON
// verbatim — the Go side never interprets the A2UI structure. Channels that
// do not understand UI metadata fall back to the plain-text Content summary.
type RenderUITool struct {
	msgBus *bus.MessageBus
}

// NewRenderUITool creates a RenderUITool publishing through msgBus
// (CronTool-style direct bus injection — fire and forget).
func NewRenderUITool(msgBus *bus.MessageBus) *RenderUITool {
	return &RenderUITool{msgBus: msgBus}
}

// Name returns the tool name.
func (t *RenderUITool) Name() string {
	return RenderUIToolName
}

// Description returns the tool description.
func (t *RenderUITool) Description() string {
	return "Render an interactive structured UI (A2UI v0.9 surface: cards, columns, lists, text) " +
		"directly into the chat stream. Use it to present structured results (tables, grouped data, " +
		"summaries with labeled fields) instead of long plain-text dumps. Display-only in this " +
		"version: no form input comes back to you."
}

// Parameters returns the tool parameters schema. The messages description
// pins the strict v0.9 envelope so the model cannot smuggle extra top-level
// keys past the renderer's zod schema.
func (t *RenderUITool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"messages": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
				},
				"description": `Array of A2UI v0.9 messages. Each message MUST be exactly ` +
					`{"version":"v0.9","createSurface":{"surfaceId":"<unique-id>","catalogId":"https://a2ui.org/specification/v0_9/catalogs/basic/catalog.json","components":[{"id":"c1","component":"Card","label":"...","childIds":["c2"]},{"id":"c2","component":"Text","text":"..."}],"dataModel":{}}} — ` +
					`the top level allows ONLY "version" and "createSurface" (the v0.9 schema is strict: extra top-level keys are rejected by the renderer); "version" must start with "v0.9" and "createSurface" is required. ` +
					`components is a flat array of {id, component, ...props, childIds?}; available components: Text, Image, Icon, Video, AudioPlayer, Row, Column, List, Card, Tabs, Modal, Divider, Button, TextField, CheckBox, ChoicePicker, Slider, DateTimeInput.`,
			},
			"summary": map[string]any{
				"type":        "string",
				"description": "Optional plain-text fallback shown by channels that cannot render the interactive UI. Defaults to a text summary generated from the component tree.",
			},
		},
		"required": []string{"messages"},
	}
}

// Execute validates the A2UI batch and publishes it to the chat stream.
func (t *RenderUITool) Execute(ctx context.Context, args map[string]any) *ToolResult {
	raw, ok := args["messages"]
	if !ok {
		return &ToolResult{ForLLM: "messages is required", IsError: true}
	}
	items, ok := raw.([]any)
	if !ok || len(items) == 0 {
		return &ToolResult{ForLLM: "messages must be a non-empty array of A2UI message objects (not a JSON string)", IsError: true}
	}

	// Gate 1: the batch must be valid JSON.
	payload, err := json.Marshal(items)
	if err != nil {
		return &ToolResult{
			ForLLM:  fmt.Sprintf("messages failed to serialize as JSON: %v", err),
			IsError: true,
		}
	}
	if !json.Valid(payload) {
		return &ToolResult{ForLLM: "messages is not valid JSON", IsError: true}
	}

	// Gate 2: each message must be a v0.9 envelope carrying createSurface.
	if violation := renderUIEnvelopeViolation(items); violation != nil {
		return &ToolResult{ForLLM: violation.Error(), IsError: true}
	}

	// Gate 3: size cap on the serialized batch.
	if len(payload) > renderUIMaxBytes {
		return &ToolResult{
			ForLLM:  fmt.Sprintf("messages too large: %d bytes (max %d bytes); split the surface into smaller render_ui calls", len(payload), renderUIMaxBytes),
			IsError: true,
		}
	}

	channel := ToolChannel(ctx)
	chatID := ToolChatID(ctx)
	if channel == "" || chatID == "" {
		return &ToolResult{ForLLM: "No target channel/chat specified", IsError: true}
	}
	if t.msgBus == nil {
		return &ToolResult{ForLLM: "UI rendering not configured", IsError: true}
	}

	// Plain-text fallback Content: prefer the summary argument, else derive a
	// text summary from the components tree. Non-pico channels render this
	// instead of silently dropping the card.
	summary, _ := args["summary"].(string)
	fallback := strings.TrimSpace(summary)
	if fallback == "" {
		fallback = renderUITextSummary(items)
	}
	if fallback == "" {
		fallback = fmt.Sprintf("Interactive UI (%d A2UI message(s))", len(items))
	}

	pubCtx, pubCancel := context.WithTimeout(context.Background(), renderUIPublishTimeout)
	defer pubCancel()
	if err := t.msgBus.PublishOutbound(pubCtx, bus.OutboundMessage{
		Context:    bus.NewOutboundContext(channel, chatID, ""),
		AgentID:    ToolAgentID(ctx),
		SessionKey: ToolSessionKey(ctx),
		Content:    fallback,
		UI:         &bus.UIMeta{Messages: json.RawMessage(payload)},
	}); err != nil {
		return &ToolResult{
			ForLLM:  fmt.Sprintf("failed to publish UI message: %v", err),
			IsError: true,
			Err:     err,
		}
	}

	return &ToolResult{
		ForLLM: fmt.Sprintf("UI rendered: %d A2UI message(s) delivered to %s:%s", len(items), channel, chatID),
		Silent: true,
	}
}

// renderUIEnvelopeViolation returns the first v0.9 envelope violation in the
// batch (Execute gate 2), or nil when every item is a message object with a
// compatible version and a createSurface payload. Shared with
// RenderUIHistoryMessages so history replay applies the exact live gates.
func renderUIEnvelopeViolation(items []any) error {
	for i, item := range items {
		msg, ok := item.(map[string]any)
		if !ok {
			return fmt.Errorf("messages[%d] must be an object", i)
		}
		version, _ := msg["version"].(string)
		if !strings.HasPrefix(version, renderUIVersionPrefix) {
			return fmt.Errorf("messages[%d]: \"version\" must be a string starting with %q", i, renderUIVersionPrefix)
		}
		surface, ok := msg["createSurface"].(map[string]any)
		if !ok || len(surface) == 0 {
			return fmt.Errorf("messages[%d]: \"createSurface\" object is required", i)
		}
	}
	return nil
}

// RenderUIHistoryMessages extracts the validated A2UI batch and its
// plain-text fallback from a persisted render_ui tool-call arguments JSON
// (the full arguments string the pipeline stores verbatim in
// ToolCalls[].Function.Arguments — A2UI web render history replay). It
// applies the same gates as Execute (envelope check, size cap) so history
// only resurfaces batches that actually rendered live; ok=false otherwise.
// fallback mirrors the live OutboundMessage Content derivation (summary
// argument, else the generated components-tree text, else a count line).
func RenderUIHistoryMessages(argsJSON string) (messages json.RawMessage, fallback string, ok bool) {
	var args struct {
		Messages []any  `json:"messages"`
		Summary  string `json:"summary"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return nil, "", false
	}
	if len(args.Messages) == 0 {
		return nil, "", false
	}
	if renderUIEnvelopeViolation(args.Messages) != nil {
		return nil, "", false
	}
	payload, err := json.Marshal(args.Messages)
	if err != nil || len(payload) > renderUIMaxBytes {
		return nil, "", false
	}
	fallback = strings.TrimSpace(args.Summary)
	if fallback == "" {
		fallback = renderUITextSummary(args.Messages)
	}
	if fallback == "" {
		fallback = fmt.Sprintf("Interactive UI (%d A2UI message(s))", len(args.Messages))
	}
	return payload, fallback, true
}

// renderUITextSummary derives a plain-text fallback from the components
// tree: one line per component listing its type name and text-valued props.
// Component keys are walked in sorted order so the output is deterministic.
func renderUITextSummary(messages []any) string {
	var lines []string
	for _, raw := range messages {
		msg, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		surface, _ := msg["createSurface"].(map[string]any)
		if surface == nil {
			continue
		}
		if id, _ := surface["surfaceId"].(string); id != "" {
			lines = append(lines, fmt.Sprintf("surface %s", id))
		}
		components, _ := surface["components"].([]any)
		for _, c := range components {
			comp, ok := c.(map[string]any)
			if !ok {
				continue
			}
			name, _ := comp["component"].(string)
			if name == "" {
				name = "component"
			}
			keys := make([]string, 0, len(comp))
			for key := range comp {
				switch key {
				case "id", "component", "childIds":
					continue
				}
				keys = append(keys, key)
			}
			sort.Strings(keys)
			texts := make([]string, 0, len(keys))
			for _, key := range keys {
				if text, ok := comp[key].(string); ok && strings.TrimSpace(text) != "" {
					texts = append(texts, text)
				}
			}
			if len(texts) == 0 {
				lines = append(lines, "- "+name)
				continue
			}
			lines = append(lines, fmt.Sprintf("- %s: %s", name, strings.Join(texts, " | ")))
		}
	}
	return strings.Join(lines, "\n")
}
