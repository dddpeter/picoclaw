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

// renderUIBranchKeys lists the v0.9 message union branches (from the
// @a2ui/web_core v0_9 adapter): every message is "version" plus exactly one
// of these payloads. components/dataModel live in their own frames — they
// are NEVER inlined in createSurface (the strict zod schema rejects that).
var renderUIBranchKeys = []string{"createSurface", "updateComponents", "updateDataModel", "deleteSurface"}

// renderUIBranchKeysDoc is the quoted, slash-joined branch list used in
// error messages.
var renderUIBranchKeysDoc = func() string {
	quoted := make([]string, len(renderUIBranchKeys))
	for i, key := range renderUIBranchKeys {
		quoted[i] = fmt.Sprintf("%q", key)
	}
	return strings.Join(quoted, "/")
}()

// renderUIBranchAllowedKeys mirrors the v0.9 strict per-branch schemas
// (@a2ui/web_core v0_9 adapter): every branch payload is a strict zod
// object, so an extra key — a "title" inside createSurface being the
// observed live failure — makes the frontend reject the whole batch with
// "Unrecognized key(s)". Enforcing the subsets here turns that dead card
// into a retryable tool error. Legacy inline batches (components/dataModel
// inside createSurface) are rejected on purpose: nothing published those
// before the frame-shape fix (5bfe894d) ever shipped, so there is no
// history to replay and tolerance would only mask model mistakes.
var renderUIBranchAllowedKeys = map[string]map[string]bool{
	"createSurface":    {"surfaceId": true, "catalogId": true, "theme": true, "sendDataModel": true},
	"updateComponents": {"surfaceId": true, "components": true},
	"updateDataModel":  {"surfaceId": true, "path": true, "value": true},
	"deleteSurface":    {"surfaceId": true},
}

// renderUIBasicCatalogID is the exact catalogId the frontend registers (the
// v0.9 basicCatalog). The strict schema requires catalogId on createSurface,
// and an id that does not resolve to a registered catalog leaves the surface
// without a catalog to render against — dead card. Enforcing the exact match
// (the only catalog the web frontend ships) turns a typo into a retryable
// error.
const renderUIBasicCatalogID = "https://a2ui.org/specification/v0_9/catalogs/basic/catalog.json"

// renderUIBasicCatalogComponents is the closed component-name whitelist of
// the v0.9 basicCatalog. The renderer fails a batch whose component falls
// outside this set (catalog-level lookup); catching it here is a cheap
// string-set check that converts an unrenderable card into a retryable
// error naming the valid components.
var renderUIBasicCatalogComponents = map[string]bool{
	"Text": true, "Image": true, "Icon": true, "Video": true, "AudioPlayer": true,
	"Row": true, "Column": true, "List": true, "Card": true, "Tabs": true,
	"Modal": true, "Divider": true, "Button": true, "TextField": true,
	"CheckBox": true, "ChoicePicker": true, "Slider": true, "DateTimeInput": true,
}

// RenderUITool publishes A2UI v0.9 messages into the chat stream (A2UI web
// render). The model calls render_ui mid-turn with a message batch; the tool
// applies cheap validation (json.Valid → frame check → size cap) and pushes
// one OutboundMessage whose UI.Messages carries the raw JSON verbatim — the
// Go side never interprets the A2UI structure. Channels that do not
// understand UI metadata fall back to the plain-text Content summary.
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
	return "Render an interactive structured UI (A2UI v0.9 surface: cards, columns, lists, text, " +
		"buttons, form inputs) directly into the chat stream. Use it to present structured results " +
		"(tables, grouped data, summaries with labeled fields) instead of long plain-text dumps. " +
		"Interactive components work: when the user clicks a button or submits a form, the action " +
		"and its form values reach you as a user message prefixed with \"[A2UI 交互]\"."
}

// Parameters returns the tool parameters schema. The messages description
// teaches the FRAME shape the strict v0.9 zod adapter accepts: a
// createSurface envelope first, then one message per update
// (updateComponents / updateDataModel). Inlining components or dataModel
// inside createSurface is rejected by the renderer — the original inline
// wording here was a verified defect and is deliberately gone. It also
// teaches the interaction uplink contract: Button event.context {path}
// bindings collect form values from the dataModel at click time, and the
// user's interaction arrives as a "[A2UI 交互]"-prefixed user message.
func (t *RenderUITool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"messages": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
				},
				"description": `Array of A2UI v0.9 messages in FRAME shape (the renderer schema is strict). ` +
					`First message creates the surface: {"version":"v0.9","createSurface":{"surfaceId":"<unique-id>","catalogId":"https://a2ui.org/specification/v0_9/catalogs/basic/catalog.json"}} ` +
					`— createSurface allows ONLY surfaceId + catalogId (plus optional theme/sendDataModel); do NOT add "title" or any other key (unknown keys reject the whole batch); ` +
					`catalogId must be EXACTLY "https://a2ui.org/specification/v0_9/catalogs/basic/catalog.json" (the only registered catalog — no other id resolves). ` +
					`Then one message per update, reusing the SAME surfaceId: ` +
					`{"version":"v0.9","updateComponents":{"surfaceId":"<id>","components":[...]}}, and optionally ` +
					`{"version":"v0.9","updateDataModel":{"surfaceId":"<id>","path":"/","value":{...}}}. ` +
					`Example: [{"version":"v0.9","createSurface":{"surfaceId":"s1","catalogId":"https://a2ui.org/specification/v0_9/catalogs/basic/catalog.json"}},` +
					`{"version":"v0.9","updateComponents":{"surfaceId":"s1","components":[{"id":"root","component":"Card","child":"c2"},{"id":"c2","component":"Text","text":"hello"}]}},` +
					`{"version":"v0.9","updateDataModel":{"surfaceId":"s1","path":"/","value":{"title":"hello"}}}] — ` +
					`the TOP-LEVEL component's id MUST be "root": the renderer always renders the tree rooted at the component named "root" (any other root id renders an empty card), ` +
					`every component must be reachable from "root" via child/children references (unreachable components never render), ` +
					`components form a TREE — each id may appear in at most ONE parent's child/children list (listing an id in two parents renders it twice), ` +
					`and component ids must be UNIQUE across the whole components array (a duplicate id makes the renderer reject the batch); ` +
					`every message top level allows ONLY "version" plus exactly ONE of ` + renderUIBranchKeysDoc + `; ` +
					`do NOT inline components or dataModel inside createSurface (the strict schema rejects them with Unrecognized key(s)). ` +
					`components entries are {id, component, ...props}; props are STRICT and per-component: ` +
					`Card{child}, Row/Column{children, justify?: start|center|end|spaceAround|spaceBetween, align?: start|center|end|stretch}, ` +
					`List{children, direction?: vertical|horizontal}, Text{text, variant?: h1|h2|h3|h4|h5|caption|body, weight?: number}; ` +
					`text values accept a plain string or a data binding {"path":"/field"}; ` +
					`there are NO styling props — never send style/colors/fontSize/title on components (basicCatalog has no per-component styling; a heading is a Text with variant h1-h5); ` +
					`any unknown prop, unknown component name, or missing id makes the WHOLE batch fail to render. ` +
					`interactive components bind their value into the surface dataModel with a {"path"} binding on the value prop ` +
					`(TextField/CheckBox/ChoicePicker/Slider/DateTimeInput, e.g. {"id":"f","component":"TextField","label":"Name","value":{"path":"/form/name"}} — user edits are written to that dataModel path immediately); ` +
					`Button fires an action: {"id":"b","component":"Button","child":"bl","action":{"event":{"name":"submit","context":{"name":{"path":"/form/name"}}}}} — ` +
					`at click time each event.context entry holding a {"path"} binding is resolved from the CURRENT dataModel value (this is how form values ride along; plain string entries pass through verbatim); ` +
					`the user's interaction reaches you as a plain user message prefixed "[A2UI 交互]" in the exact shape ` +
					`[A2UI 交互] surface="<surfaceId>" action="<name>" source="<componentId>" context=<json> — the context= segment is omitted when the resolved context is empty — parse it to read the action name and the collected form values; ` +
					`complete returnable form example (TextField bound to /form/name plus a submit Button whose event.context collects it): ` +
					`[{"version":"v0.9","createSurface":{"surfaceId":"s1","catalogId":"https://a2ui.org/specification/v0_9/catalogs/basic/catalog.json"}},` +
					`{"version":"v0.9","updateComponents":{"surfaceId":"s1","components":[{"id":"root","component":"Card","child":"col"},{"id":"col","component":"Column","children":["nameField","submitBtn"]},{"id":"nameField","component":"TextField","label":"Name","value":{"path":"/form/name"}},{"id":"submitBtn","component":"Button","child":"btnLabel","action":{"event":{"name":"submit","context":{"name":{"path":"/form/name"}}}}},{"id":"btnLabel","component":"Text","text":"Submit"}]}},` +
					`{"version":"v0.9","updateDataModel":{"surfaceId":"s1","path":"/","value":{"form":{"name":""}}}}] ` +
					`(the button click sends you: [A2UI 交互] surface="s1" action="submit" source="submitBtn" context={"name":"<user input>"}). ` +
					`available components: Text, Image, Icon, Video, AudioPlayer, Row, Column, List, Card, Tabs, Modal, Divider, Button, TextField, CheckBox, ChoicePicker, Slider, DateTimeInput.`,
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

	// Gate 2: frame check — per-message version + exactly one union branch,
	// and at least one createSurface envelope in the batch.
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
	// text summary from the batch frames. Non-pico channels render this
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

// renderUIEnvelopeViolation returns the first v0.9 frame violation in the
// batch (Execute gate 2), or nil when every message is an object with a
// compatible version carrying exactly one non-empty union branch payload,
// every branch payload stays within its strict key subset, updateComponents
// entries reference known basicCatalog components with ids, and the batch
// contains at least one createSurface envelope. The legacy inline shape
// (components inside createSurface) is rejected by the key-subset check:
// nothing published that shape before the frame-shape fix, so tolerance
// would only mask model mistakes the model could otherwise retry out of.
// Shared with RenderUIHistoryMessages so history replay applies the exact
// live gates.
func renderUIEnvelopeViolation(items []any) error {
	hasCreateSurface := false
	for i, item := range items {
		msg, ok := item.(map[string]any)
		if !ok {
			return fmt.Errorf("messages[%d] must be an object", i)
		}
		version, _ := msg["version"].(string)
		if !strings.HasPrefix(version, renderUIVersionPrefix) {
			return fmt.Errorf("messages[%d]: \"version\" must be a string starting with %q", i, renderUIVersionPrefix)
		}
		branches := 0
		for _, key := range renderUIBranchKeys {
			payload, ok := msg[key].(map[string]any)
			if !ok {
				continue
			}
			if len(payload) == 0 {
				return fmt.Errorf("messages[%d]: exactly one of %s is required (non-empty object)", i, renderUIBranchKeysDoc)
			}
			for k := range payload {
				if !renderUIBranchAllowedKeys[key][k] {
					return fmt.Errorf("messages[%d]: %s has invalid key %q (allowed: %s) — unknown keys make the renderer reject the whole batch",
						i, key, k, renderUIAllowedKeysDoc(key))
				}
			}
			if key == "updateComponents" {
				if err := renderUIComponentsViolation(i, payload["components"]); err != nil {
					return err
				}
			}
			branches++
			if key == "createSurface" {
				hasCreateSurface = true
				if cid, _ := payload["catalogId"].(string); cid != renderUIBasicCatalogID {
					return fmt.Errorf("messages[%d]: createSurface.catalogId %q does not name a registered catalog — use exactly %q", i, cid, renderUIBasicCatalogID)
				}
			}
		}
		if branches != 1 {
			return fmt.Errorf("messages[%d]: exactly one of %s is required (non-empty object)", i, renderUIBranchKeysDoc)
		}
	}
	if !hasCreateSurface {
		return fmt.Errorf("batch must contain at least one \"createSurface\" envelope message")
	}
	return renderUIRootViolation(items)
}

// renderUIRootViolation checks the batch's component forest: component ids
// must be unique (the renderer throws "Duplicate component ID"), at least
// one component with id "root" must exist, and every component must be
// reachable from it via child/children references. The renderer always
// mounts SurfaceModel.rootId — which the v0.9 adapter pins to the literal
// "root" (createSurface carries no root field) — so any other root id
// renders an empty card no matter how many components were delivered
// (observed live 2026-11-14: 24 components, root named "c1", empty card),
// components orphaned from the tree never render at all, and a duplicated
// id aborts the whole batch (observed live 2026-11-14 second batch: year
// ids used for both a Column and its inner Row). Path-bound children
// ({"path":...}) are opaque here and skipped, not traversed.
func renderUIRootViolation(items []any) error {
	byID := map[string]map[string]any{}
	var order []string
	for _, item := range items {
		msg, ok := item.(map[string]any)
		if !ok {
			continue
		}
		uc, ok := msg["updateComponents"].(map[string]any)
		if !ok {
			continue
		}
		comps, ok := uc["components"].([]any)
		if !ok {
			continue
		}
		for _, c := range comps {
			comp, ok := c.(map[string]any)
			if !ok {
				continue
			}
			id, _ := comp["id"].(string)
			if id == "" {
				continue // 缺 id 已由 renderUIComponentsViolation 拦截
			}
			if _, exists := byID[id]; exists {
				return fmt.Errorf("duplicate component id %q — every component must have a unique id across the whole batch (the renderer rejects duplicates with \"Duplicate component ID\")", id)
			}
			order = append(order, id)
			byID[id] = comp
		}
	}
	if _, ok := byID["root"]; !ok {
		return fmt.Errorf(`batch must include a component with id "root" — the renderer renders the tree rooted at the component named "root"; rename the top-level component's id to "root"`)
	}
	visited := map[string]bool{"root": true}
	queue := []string{"root"}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		comp := byID[id]
		if comp == nil {
			continue
		}
		if child, ok := comp["child"].(string); ok && child != "" && !visited[child] {
			visited[child] = true
			queue = append(queue, child)
		}
		if children, ok := comp["children"].([]any); ok {
			for _, ch := range children {
				if ref, ok := ch.(string); ok && ref != "" && !visited[ref] {
					visited[ref] = true
					queue = append(queue, ref)
				}
			}
		}
	}
	var unreachable []string
	for _, id := range order {
		if !visited[id] {
			unreachable = append(unreachable, id)
		}
	}
	if len(unreachable) > 0 {
		if len(unreachable) > 5 {
			unreachable = append(unreachable[:5], fmt.Sprintf("… (+%d more)", len(unreachable)-5))
		}
		return fmt.Errorf("components not reachable from \"root\" never render: %s — wire them into a parent's child/children or remove them", strings.Join(unreachable, ", "))
	}
	return renderUIRefCountViolation(byID)
}

// renderUIRefCountViolation enforces the tree invariant: every component id
// may be referenced by at most one parent (each reference renders the
// component again). Observed live 2026-11-14: the model listed every Text in
// BOTH the root Column's children AND its owning Row's children, so each
// value rendered twice — once inside the row, once stacked below it.
func renderUIRefCountViolation(byID map[string]map[string]any) error {
	refCount := map[string]int{}
	for _, comp := range byID {
		if child, ok := comp["child"].(string); ok && child != "" {
			refCount[child]++
		}
		if children, ok := comp["children"].([]any); ok {
			for _, ch := range children {
				if ref, ok := ch.(string); ok && ref != "" {
					refCount[ref]++
				}
			}
		}
	}
	var shared []string
	for id, n := range refCount {
		if n > 1 {
			shared = append(shared, fmt.Sprintf("%q(x%d)", id, n))
		}
	}
	if len(shared) > 0 {
		sort.Strings(shared)
		return fmt.Errorf("components referenced by more than one parent render once per reference: %s — A2UI components form a tree, each id may appear in at most one child/children list", strings.Join(shared, ", "))
	}
	return nil
}

// renderUIAllowedKeysDoc renders the allowed key set of one branch payload
// for error messages.
func renderUIAllowedKeysDoc(branch string) string {
	allowed := renderUIBranchAllowedKeys[branch]
	quoted := make([]string, 0, len(allowed))
	for k := range allowed {
		quoted = append(quoted, fmt.Sprintf("%q", k))
	}
	sort.Strings(quoted)
	return strings.Join(quoted, ", ")
}

// renderUIComponentsViolation checks one updateComponents payload's
// components array: entries must be objects carrying a string id and a
// component name from the basicCatalog whitelist.
func renderUIComponentsViolation(msgIdx int, components any) error {
	comps, ok := components.([]any)
	if !ok || len(comps) == 0 {
		return fmt.Errorf("messages[%d]: updateComponents.components must be a non-empty array", msgIdx)
	}
	for j, c := range comps {
		comp, ok := c.(map[string]any)
		if !ok {
			return fmt.Errorf("messages[%d].components[%d] must be an object", msgIdx, j)
		}
		if id, _ := comp["id"].(string); id == "" {
			return fmt.Errorf("messages[%d].components[%d]: \"id\" (non-empty string) is required", msgIdx, j)
		}
		name, _ := comp["component"].(string)
		if !renderUIBasicCatalogComponents[name] {
			return fmt.Errorf("messages[%d].components[%d]: unknown component %q — basicCatalog components are: %s",
				msgIdx, j, name, renderUIComponentNamesDoc())
		}
	}
	return nil
}

// renderUIComponentNamesDoc renders the sorted basicCatalog component name
// list for error messages.
func renderUIComponentNamesDoc() string {
	names := make([]string, 0, len(renderUIBasicCatalogComponents))
	for name := range renderUIBasicCatalogComponents {
		names = append(names, name)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// RenderUIHistoryMessages extracts the validated A2UI batch and its
// plain-text fallback from a persisted render_ui tool-call arguments JSON
// (the full arguments string the pipeline stores verbatim in
// ToolCalls[].Function.Arguments — A2UI web render history replay). It
// applies the same gates as Execute (frame check, size cap) so history
// only resurfaces batches that actually rendered live; ok=false otherwise.
// fallback mirrors the live OutboundMessage Content derivation (summary
// argument, else the generated batch text, else a count line). The
// arguments are decoded key-by-key so a non-string summary (e.g. a
// number) downgrades to the tree-derived fallback exactly like the live
// path's args["summary"].(string) assertion miss, instead of failing the
// whole Unmarshal and silently dropping the card on replay.
func RenderUIHistoryMessages(argsJSON string) (messages json.RawMessage, fallback string, ok bool) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(argsJSON), &raw); err != nil {
		return nil, "", false
	}
	var items []any
	if msgRaw, exists := raw["messages"]; exists {
		if err := json.Unmarshal(msgRaw, &items); err != nil {
			return nil, "", false
		}
	}
	if len(items) == 0 {
		return nil, "", false
	}
	if renderUIEnvelopeViolation(items) != nil {
		return nil, "", false
	}
	payload, err := json.Marshal(items)
	if err != nil || len(payload) > renderUIMaxBytes {
		return nil, "", false
	}
	// summary 二次按字符串解码：类型不对（数字/布尔/对象）按空处理，
	// 与 live 路径的静默降级对齐。
	var summary string
	if summaryRaw, exists := raw["summary"]; exists {
		_ = json.Unmarshal(summaryRaw, &summary)
	}
	fallback = strings.TrimSpace(summary)
	if fallback == "" {
		fallback = renderUITextSummary(items)
	}
	if fallback == "" {
		fallback = fmt.Sprintf("Interactive UI (%d A2UI message(s))", len(items))
	}
	return payload, fallback, true
}

// renderUITextSummary derives a plain-text fallback from the batch: a
// "surface <id>" header per createSurface envelope, one line per component
// from updateComponents frames, plus the string leaves of updateDataModel
// values so path-bound component text still surfaces. Keys are walked in
// sorted order so the output is deterministic.
func renderUITextSummary(messages []any) string {
	var lines []string
	for _, raw := range messages {
		msg, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if surface, ok := msg["createSurface"].(map[string]any); ok {
			if id, _ := surface["surfaceId"].(string); id != "" {
				lines = append(lines, fmt.Sprintf("surface %s", id))
			}
		}
		if update, ok := msg["updateComponents"].(map[string]any); ok {
			appendComponentLines(&lines, update["components"])
		}
		if update, ok := msg["updateDataModel"].(map[string]any); ok {
			appendDataModelStrings(&lines, update["value"])
		}
	}
	return strings.Join(lines, "\n")
}

// appendComponentLines appends one "- <Type>: <texts>" line per component.
// Structural reference props (id/component/child/children/childIds) are
// skipped so only display text shows up.
func appendComponentLines(lines *[]string, raw any) {
	components, ok := raw.([]any)
	if !ok {
		return
	}
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
			case "id", "component", "child", "children", "childIds":
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
			*lines = append(*lines, "- "+name)
			continue
		}
		*lines = append(*lines, fmt.Sprintf("- %s: %s", name, strings.Join(texts, " | ")))
	}
}

// appendDataModelStrings appends the string leaves of an updateDataModel
// value (sorted object keys, array order preserved) so path-bound component
// text still appears in the fallback.
func appendDataModelStrings(lines *[]string, value any) {
	switch v := value.(type) {
	case string:
		if strings.TrimSpace(v) != "" {
			*lines = append(*lines, "- "+v)
		}
	case []any:
		for _, item := range v {
			appendDataModelStrings(lines, item)
		}
	case map[string]any:
		keys := make([]string, 0, len(v))
		for key := range v {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			appendDataModelStrings(lines, v[key])
		}
	}
}
