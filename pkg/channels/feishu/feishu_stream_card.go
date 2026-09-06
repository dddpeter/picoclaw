// PicoClaw - Ultra-lightweight AI agent

package feishu

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/sipeed/picoclaw/pkg/bus"
)

// Feishu CardKit 2.0 element IDs used by the streaming card. Elements are
// addressed by these IDs when streaming content into the card.
const (
	feishuAnswerElementID  = "answer_content"
	feishuPanelElementID   = "agent_process_panel"
	feishuLoadingElementID = "loading_icon"
)

// Feishu Card 2.0 hard-caps a card at 200 elements; every JSON object with a
// "tag" key counts, at any nesting depth. Keep a safety margin.
const (
	feishuElementLimit       = 200
	feishuElementLimitMargin = 5
)

// feishuInvalidImageKeyRe matches markdown image refs whose key is not a valid
// Feishu image_key (must be img_v2_/img_v3_... uploaded via the image API).
// CardKit rejects the whole card with 200570 otherwise.
var feishuImageRefRe = regexp.MustCompile(`!\[[^\]]*\]\(([^)]+)\)`)

// sanitizeFeishuMarkdownImages rewrites image refs that use local filenames or
// URLs instead of real Feishu image_keys into plain-text file references so
// the card never fails validation (code=200570).
func sanitizeFeishuMarkdownImages(content string) string {
	return feishuImageRefRe.ReplaceAllStringFunc(content, func(m string) string {
		key := feishuImageRefRe.FindStringSubmatch(m)[1]
		if strings.HasPrefix(key, "img_v2_") || strings.HasPrefix(key, "img_v3_") {
			return m // real Feishu image_key, keep as image
		}
		return m[1:] // ![alt](key) -> [alt](key): degrade to link text
	})
}

// Display caps that keep the process panel comfortably under the element
// limit (each nested reasoning round costs ~3 elements, each tool step ~5
// compact / ~7 with a result block).
const (
	feishuMaxReasoningRounds    = 20
	feishuMaxToolSteps          = 20
	feishuReasoningDisplayLimit = 2000
)

// Feishu caps the whole card JSON at 30KB. Panel refreshes send the full
// card, so the panel body itself must stay well under that (the answer text
// and card scaffolding share the same budget). Oldest reasoning texts are
// trimmed first when over budget.
const feishuPanelTextBudget = 16000

// applyPanelTextBudget shrinks reasoning/tool display texts (oldest first)
// until the assembled panel body fits feishuPanelTextBudget bytes.
func applyPanelTextBudget(rounds []feishuReasoningRound, tools []bus.ToolStep, budget int) ([]feishuReasoningRound, []bus.ToolStep) {
	// Copy so the live stream state is never mutated by display trimming.
	rounds = append([]feishuReasoningRound(nil), rounds...)
	tools = append([]bus.ToolStep(nil), tools...)
	total := 0
	for _, r := range rounds {
		total += len(r.Text)
	}
	for _, t := range tools {
		total += len(t.Args) + len(t.Result)
	}
	if total <= budget {
		return rounds, tools
	}
	// Trim oldest reasoning texts to a stub first.
	for i := range rounds {
		if total <= budget {
			break
		}
		total -= len(rounds[i].Text)
		rounds[i].Text = "…（内容过长已省略）"
		total += len([]byte(rounds[i].Text))
	}
	// Still over: drop reasoning texts entirely, then trim tool previews.
	for i := range rounds {
		if total <= budget {
			break
		}
		total -= len(rounds[i].Text)
		rounds[i].Text = ""
	}
	for i := range tools {
		if total <= budget {
			break
		}
		total -= len(tools[i].Result)
		tools[i].Result = "…"
		total += len(tools[i].Result)
	}
	// Last resort: trim tool argument previews as well.
	for i := range tools {
		if total <= budget {
			break
		}
		total -= len(tools[i].Args)
		tools[i].Args = "…"
		total += len(tools[i].Args)
	}
	return rounds, tools
}

type feishuReasoningRound struct {
	Text     string
	Duration time.Duration
}

// feishuStreamState is the mutable content of one streaming card. All card
// builders below are pure functions over this state.
type feishuStreamState struct {
	Rounds       []feishuReasoningRound
	CurReasoning string
	Tools        []bus.ToolStep
	ModelName    string
	InputTokens  int
	OutputTokens int

	// Steering notices: user messages queued for the active turn, surfaced on
	// the panel so the user sees their mid-turn message was heard.
	SteeringCount int
	SteeringLast  string
	LLMCalls      int // LLM API calls made this turn (one per iteration)

	// Context usage snapshot at finalize, for the footer.
	ContextUsed   int
	ContextTotal  int
	ContextOffset int // history tokens in the session at finalize
}

func (s *feishuStreamState) hasPanelContent() bool {
	return len(s.Rounds) > 0 || strings.TrimSpace(s.CurReasoning) != "" ||
		len(s.Tools) > 0 || s.SteeringCount > 0
}

func (s *feishuStreamState) reasoningTotal() time.Duration {
	total := time.Duration(0)
	for _, r := range s.Rounds {
		total += r.Duration
	}
	return total
}

// buildFeishuStreamingCard builds the initial CardKit v2 streaming card: a
// collapsible process panel placeholder, the answer element that receives
// streamed content, and a loading spinner.
func buildFeishuStreamingCard() map[string]any {
	elements := []any{
		buildFeishuPanelPlaceholder(),
		map[string]any{
			"tag":        "markdown",
			"content":    "",
			"text_align": "left",
			"text_size":  "normal_v2",
			"margin":     "0px 0px 0px 0px",
			"element_id": feishuAnswerElementID,
		},
		buildFeishuLoadingElement(feishuPhaseLoading),
	}
	return map[string]any{
		"schema": "2.0",
		"config": map[string]any{
			"streaming_mode": true,
			"streaming_config": map[string]any{
				"print_frequency_ms": map[string]any{"default": 70},
				"print_step":         map[string]any{"default": 4},
				"print_strategy":     "delay",
			},
			"locales": []string{"zh_cn", "en_us"},
			"summary": map[string]any{
				"content": "正在思考…",
				"i18n_content": map[string]any{
					"zh_cn": "正在思考…",
					"en_us": "Processing…",
				},
			},
		},
		"body": map[string]any{"elements": elements},
	}
}

func buildFeishuPanelPlaceholder() map[string]any {
	return map[string]any{
		"tag":              "collapsible_panel",
		"expanded":         true,
		"header":           feishuPanelHeader(0, false, 0, 0),
		"border":           map[string]any{"color": "grey", "corner_radius": "10px"},
		"vertical_spacing": "4px",
		"padding":          "12px 12px 8px 12px",
		"elements":         []any{map[string]any{"tag": "markdown", "content": " "}},
		"element_id":       feishuPanelElementID,
	}
}

// feishuStreamPhase values driving the streaming status line.
const (
	feishuPhaseLoading  = "" // before any activity in the turn
	feishuPhaseThinking = "thinking"
	feishuPhaseAnswer   = "answer"
)

// feishuLoadingText maps a stream phase to the status line shown next to the
// loading icon while the card is streaming.
func feishuLoadingText(phase string) (zh, en string) {
	switch phase {
	case feishuPhaseThinking:
		return "🧠 正在思考…", "Thinking…"
	case feishuPhaseAnswer:
		return "✍ 正在生成回答…", "Generating answer…"
	default:
		return "正在加载上下文…", "Loading context…"
	}
}

func buildFeishuLoadingElement(phase string) map[string]any {
	zh, en := feishuLoadingText(phase)
	return map[string]any{
		"tag": "div",
		"icon": map[string]any{
			"tag":   "standard_icon",
			"token": "time_outlined",
			"size":  "16px 16px",
		},
		"text": map[string]any{
			"tag":     "plain_text",
			"content": zh,
			"i18n_content": map[string]any{
				"zh_cn": zh,
				"en_us": en,
			},
		},
		"element_id": feishuLoadingElementID,
	}
}

// feishuPanelExpanded keeps the process panel expanded until the answer
// starts flowing — then the panel folds so the growing answer stays in view.
func feishuPanelExpanded(answer string) bool {
	return strings.TrimSpace(answer) == ""
}

func feishuPanelHeader(rounds int, hasCur bool, tools int, elapsedMs int64) map[string]any {
	parts := []string{"Agent 过程"}
	n := rounds
	if hasCur {
		n++
	}
	if n > 0 {
		parts = append(parts, fmt.Sprintf("%d 轮推理", n))
	}
	if tools > 0 {
		parts = append(parts, fmt.Sprintf("%d 次工具", tools))
	}
	if elapsedMs > 0 {
		parts = append(parts, formatFeishuElapsed(time.Duration(elapsedMs)*time.Millisecond))
	}
	title := "🧠 " + strings.Join(parts, " · ")
	return map[string]any{
		"title": map[string]any{
			"tag":          "plain_text",
			"content":      title,
			"i18n_content": map[string]any{"zh_cn": title, "en_us": title},
			"text_color":   "grey",
			"text_size":    "notation",
		},
		"vertical_align":      "center",
		"icon":                map[string]any{"tag": "standard_icon", "token": "down-small-ccm_outlined", "size": "16px 16px", "color": "grey"},
		"icon_position":       "right",
		"icon_expanded_angle": -180,
	}
}

// buildFeishuPanel builds the unified process panel with the default text
// budget. See buildFeishuPanelBudget.
func buildFeishuPanel(state *feishuStreamState, expanded bool) map[string]any {
	return buildFeishuPanelBudget(state, expanded, feishuPanelTextBudget)
}

// buildFeishuCardWithinSize builds a card through successive, smaller panel
// text budgets until the marshaled JSON fits Feishu's 30KB card limit. The
// JSON scaffolding of ~40 panel elements alone costs ~14KB, so the text
// budget must shrink adaptively when the answer is long.
func buildFeishuCardWithinSize(build func(panelBudget int) map[string]any) map[string]any {
	for _, budget := range []int{feishuPanelTextBudget, 8000, 3000, 1000, 0} {
		card := build(budget)
		data, err := json.Marshal(card)
		if err == nil && len(data) <= 29500 {
			return card
		}
	}
	return build(0)
}

// buildFeishuPanelBudget builds the unified process panel: collapse hint for
// trimmed early items, finalized reasoning rounds, in-progress reasoning, then
// tool steps — all in chronological order (reasoning rounds of a turn precede
// the tool calls that followed them).
func buildFeishuPanelBudget(state *feishuStreamState, expanded bool, textBudget int) map[string]any {
	rounds := state.Rounds
	tools := state.Tools
	trimmedRounds := 0
	trimmedTools := 0
	if len(rounds) > feishuMaxReasoningRounds {
		trimmedRounds = len(rounds) - feishuMaxReasoningRounds
		rounds = rounds[len(rounds)-feishuMaxReasoningRounds:]
	}
	if len(tools) > feishuMaxToolSteps {
		trimmedTools = len(tools) - feishuMaxToolSteps
		tools = tools[len(tools)-feishuMaxToolSteps:]
	}
	rounds, tools = applyPanelTextBudget(rounds, tools, textBudget)

	// Skill activation entries are context preparation, not tool executions:
	// render them before the reasoning rounds and keep them out of the
	// header's tool count.
	skillSteps := make([]bus.ToolStep, 0, len(tools))
	execSteps := make([]bus.ToolStep, 0, len(tools))
	for _, step := range tools {
		if step.Kind == bus.ToolStepKindSkill {
			skillSteps = append(skillSteps, step)
		} else {
			execSteps = append(execSteps, step)
		}
	}

	children := []any{}
	if state.SteeringCount > 0 {
		notice := fmt.Sprintf("📥 收到 %d 条追加指令", state.SteeringCount)
		if last := strings.TrimSpace(state.SteeringLast); last != "" {
			if len(last) > 60 {
				last = cutOnRuneBoundary(last, 60) + "…"
			}
			notice += " · 最新：" + last
		}
		children = append(children, map[string]any{
			"tag": "markdown", "content": notice, "text_size": "notation",
		})
	}
	if trimmedRounds > 0 || trimmedTools > 0 {
		var hintParts []string
		if trimmedRounds > 0 {
			hintParts = append(hintParts, fmt.Sprintf("%d 轮早期推理", trimmedRounds))
		}
		if trimmedTools > 0 {
			hintParts = append(hintParts, fmt.Sprintf("%d 步早期操作", trimmedTools))
		}
		hint := "⚡ 已折叠：" + strings.Join(hintParts, "、")
		children = append(children, map[string]any{
			"tag": "markdown", "content": hint, "text_size": "notation",
		})
	}

	for _, step := range skillSteps {
		children = append(children, feishuToolStepTitle(step))
	}
	for i, r := range rounds {
		children = append(children, feishuReasoningRoundPanel(i+1, r))
	}
	if cur := strings.TrimSpace(state.CurReasoning); cur != "" {
		children = append(children, feishuReasoningTitle(len(rounds)+1, 0, false))
		children = append(children, feishuIndentedLarkMD(truncateFeishuReasoning(cur)))
	}
	for _, step := range execSteps {
		children = append(children, feishuToolStepElements(step)...)
	}
	if len(children) == 0 {
		children = append(children, map[string]any{"tag": "markdown", "content": " "})
	}

	header := feishuPanelHeader(len(rounds), strings.TrimSpace(state.CurReasoning) != "", len(execSteps),
		int64((state.reasoningTotal()).Milliseconds()))
	return map[string]any{
		"tag":              "collapsible_panel",
		"expanded":         expanded,
		"header":           header,
		"border":           map[string]any{"color": "grey", "corner_radius": "10px"},
		"vertical_spacing": "4px",
		"padding":          "12px 12px 8px 12px",
		"elements":         children,
		"element_id":       feishuPanelElementID,
	}
}

func feishuReasoningTitle(index int, elapsed time.Duration, finalized bool) map[string]any {
	color, symbol := "orange-300", "▸"
	if finalized {
		color, symbol = "green", "✓"
	}
	text := fmt.Sprintf("第 %d 轮推理", index)
	if elapsed > 0 {
		text += " · " + formatFeishuElapsed(elapsed)
	}
	content := fmt.Sprintf("<font color='%s'>**%s %s**</font>", color, symbol, text)
	return map[string]any{
		"tag": "div",
		"icon": map[string]any{
			"tag":   "standard_icon",
			"token": "robot-add_outlined",
			"size":  "16px 16px",
			"color": "grey",
		},
		"text": map[string]any{
			"tag": "lark_md", "content": content, "text_size": "notation",
		},
	}
}

func feishuToolStepElements(step bus.ToolStep) []any {
	title := feishuToolStepTitle(step)
	result := strings.TrimSpace(step.Result)

	// Compact form: short single-line results merge with the args preview
	// into one indented line — no labeled block. Long or multi-line results
	// keep the block form below.
	if result != "" && !strings.Contains(result, "\n") && len([]rune(result)) <= feishuCompactResultRunes {
		if line := feishuCompactToolLine(step.Args, result); line != "" {
			return []any{title, map[string]any{
				"tag":    "div",
				"margin": "0px 0px 0px 22px",
				"text": map[string]any{
					"tag": "plain_text", "content": line, "text_color": "grey", "text_size": "notation",
				},
			}}
		}
		return []any{title}
	}

	elements := []any{title}
	if detail := strings.TrimSpace(step.Args); detail != "" {
		elements = append(elements, map[string]any{
			"tag":    "div",
			"margin": "0px 0px 0px 22px",
			"text": map[string]any{
				"tag": "plain_text", "content": detail, "text_color": "grey", "text_size": "notation",
			},
		})
	}
	if result != "" {
		label := "结果"
		if step.IsError {
			label = "错误"
		}
		// No fenced code blocks here: Feishu renders fences at a fixed large
		// font and ignores text_size on them (built-in or custom). Per-line
		// inline code is character-level styling, so it follows notation size.
		content := "**" + label + "**\n" + feishuInlineCodeBlock(truncateFeishuCodeResult(result))
		elements = append(elements, map[string]any{
			"tag":    "div",
			"margin": "0px 0px 0px 22px",
			"text": map[string]any{
				"tag": "lark_md", "content": content, "text_size": "notation",
			},
		})
	}
	return elements
}

// feishuCompactResultRunes bounds the compact one-line form; longer results
// fall back to the labeled block.
const feishuCompactResultRunes = 120

// feishuCompactToolLine joins the args preview and a short result into one
// grey notation line: `{"q":"x"} → 3 results`.
func feishuCompactToolLine(args, result string) string {
	args = strings.TrimSpace(args)
	if args == "" {
		return result
	}
	if runes := []rune(args); len(runes) > 60 {
		args = string(runes[:60]) + "…"
	}
	return args + " → " + result
}

// feishuInlineCodeBlock renders multi-line tool output as per-line inline
// code so every line inherits the element's small text size.
func feishuInlineCodeBlock(result string) string {
	lines := strings.Split(strings.ReplaceAll(result, "\r\n", "\n"), "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			out = append(out, "")
			continue
		}
		out = append(out, feishuInlineCodeLine(line))
	}
	return strings.Join(out, "\n")
}

// feishuInlineCodeLine wraps one line in inline code, using a backtick run
// longer than any run inside the line (markdown inline-code rule).
func feishuInlineCodeLine(line string) string {
	longest := 0
	for _, m := range feishuBacktickRunRe.FindAllString(line, -1) {
		if len(m) > longest {
			longest = len(m)
		}
	}
	fence := strings.Repeat("`", longest+1)
	if strings.HasPrefix(line, "`") || strings.HasSuffix(line, "`") {
		line = " " + line + " "
	}
	return fence + line + fence
}

// truncateFeishuCodeResult keeps tool results readable inside the panel: cap
// at ~6 lines and 600 chars, appending an ellipsis marker when trimmed.
func truncateFeishuCodeResult(result string) string {
	normalized := strings.ReplaceAll(strings.TrimSpace(result), "\r\n", "\n")
	if len(normalized) <= 600 && strings.Count(normalized, "\n") < 6 {
		return normalized
	}
	lines := strings.Split(normalized, "\n")
	if len(lines) > 6 {
		lines = lines[:6]
	}
	out := strings.Join(lines, "\n")
	if len(out) > 600 {
		out = out[:1200]
	}
	return out + "\n…"
}

func feishuToolStepTitle(step bus.ToolStep) map[string]any {
	color, symbol := "green", "✓"
	if step.IsError {
		color, symbol = "red", "✕"
	}
	var title string
	switch step.Kind {
	case bus.ToolStepKindSkill:
		// Context activation, not an execution: show the skill names without
		// an elapsed suffix.
		title = "📚 已加载技能：" + step.Tool
	case bus.ToolStepKindMCP:
		// MCP names carry an "mcp_<server>_<tool>" prefix; strip it and tag
		// the step so peripheral calls stand out from built-in tools.
		title = fmt.Sprintf("🔌 MCP %s（%s）",
			strings.TrimPrefix(step.Tool, "mcp_"), formatFeishuElapsed(step.Duration))
	default:
		title = fmt.Sprintf("%s（%s）", step.Tool, formatFeishuElapsed(step.Duration))
	}
	content := fmt.Sprintf("<font color='%s'>**%s %s**</font>", color, symbol, escapeFeishuMD(title))
	iconColor := "grey"
	switch step.Kind {
	case bus.ToolStepKindMCP:
		iconColor = "blue"
	case bus.ToolStepKindSkill:
		iconColor = "violet"
	}
	return map[string]any{
		"tag": "div",
		"icon": map[string]any{
			"tag":   "standard_icon",
			"token": "tool_02",
			"color": iconColor,
		},
		"text": map[string]any{
			"tag": "lark_md", "content": content, "text_size": "notation",
		},
	}
}

// feishuReasoningRoundPanel nests a finalized reasoning round in its own
// collapsed panel: the panel list stays scannable (one header line per round)
// and the full text is one click away. Only the live in-progress round stays
// expanded inline.
func feishuReasoningRoundPanel(index int, r feishuReasoningRound) map[string]any {
	text := fmt.Sprintf("第 %d 轮推理", index)
	if r.Duration > 0 {
		text += " · " + formatFeishuElapsed(r.Duration)
	}
	elements := []any{}
	if t := strings.TrimSpace(r.Text); t != "" {
		elements = append(elements, map[string]any{
			"tag": "markdown", "content": truncateFeishuReasoning(t), "text_size": "notation",
		})
	}
	if len(elements) == 0 {
		elements = []any{map[string]any{"tag": "markdown", "content": " "}}
	}
	return map[string]any{
		"tag":      "collapsible_panel",
		"expanded": false,
		"header": map[string]any{"title": map[string]any{
			"tag": "plain_text", "content": "✓ " + text, "text_color": "green", "text_size": "notation",
		}},
		"border":           map[string]any{"color": "grey", "corner_radius": "8px"},
		"vertical_spacing": "2px",
		"padding":          "8px 8px 4px 8px",
		"elements":         elements,
	}
}

func feishuIndentedLarkMD(content string) map[string]any {
	return map[string]any{
		"tag":    "div",
		"margin": "0px 0px 0px 22px",
		"text": map[string]any{
			"tag": "lark_md", "content": content, "text_size": "notation",
		},
	}
}

// buildFeishuFinalCard builds the sealed card: collapsed process panel, the
// full answer, and a footer with turn statistics. The loading element is gone.
func buildFeishuFinalCard(state *feishuStreamState, answer string, aborted bool, elapsed time.Duration, cancelReason string) map[string]any {
	return buildFeishuCardWithinSize(func(panelBudget int) map[string]any {
		return buildFeishuFinalCardBudget(state, answer, aborted, elapsed, cancelReason, panelBudget)
	})
}

// feishuCancelReasonText maps stable cancellation codes to display text.
// Unknown non-empty codes render verbatim so new reasons never degrade to a
// generic "interrupted".
func feishuCancelReasonText(reason string) string {
	switch reason {
	case "stop_command":
		return "用户停止"
	case "stream_error":
		return "流式更新失败"
	case "session_save_failed":
		return "会话保存失败"
	case "hook_abort":
		return "钩子中止"
	case "hard_abort":
		return "硬中断"
	case "turn_aborted":
		return "回合中止"
	case "":
		return ""
	default:
		return reason
	}
}

func buildFeishuFinalCardBudget(state *feishuStreamState, answer string, aborted bool, elapsed time.Duration, cancelReason string, panelBudget int) map[string]any {
	elements := []any{}
	if state.hasPanelContent() {
		elements = append(elements, buildFeishuPanelBudget(state, false, panelBudget))
	}
	if strings.TrimSpace(answer) != "" {
		elements = append(elements, map[string]any{
			"tag":        "markdown",
			"content":    answer,
			"text_align": "left",
			"text_size":  "normal_v2",
			"element_id": feishuAnswerElementID,
		})
	} else if !state.hasPanelContent() {
		content := "已完成"
		if aborted {
			content = "已中断"
			if text := feishuCancelReasonText(cancelReason); text != "" {
				content += " · " + text
			}
		}
		elements = append(elements, map[string]any{"tag": "markdown", "content": content})
	}
	elements = append(elements, buildFeishuFooter(state, aborted, elapsed, cancelReason)...)

	card := map[string]any{
		"schema": "2.0",
		"config": map[string]any{
			"streaming_mode": false,
			"locales":        []string{"zh_cn", "en_us"},
			"summary":        feishuCardSummary(answer),
		},
		"body": map[string]any{"elements": elements},
	}
	enforceFeishuElementLimit(card)
	return card
}

func buildFeishuFooter(state *feishuStreamState, aborted bool, elapsed time.Duration, cancelReason string) []any {
	status := "✓ 已完成"
	if aborted {
		status = "⚠ 已中断"
		if text := feishuCancelReasonText(cancelReason); text != "" {
			status += " · " + text
		}
	}
	line1 := []string{status}
	if elapsed > 0 {
		line1 = append(line1, "⏱ "+formatFeishuElapsed(elapsed))
	}
	if state.ModelName != "" {
		line1 = append(line1, state.ModelName)
	}
	if state.LLMCalls > 1 {
		line1 = append(line1, fmt.Sprintf("API %d", state.LLMCalls))
	}
	line2 := []string{}
	if state.InputTokens > 0 || state.OutputTokens > 0 {
		line2 = append(line2, fmt.Sprintf("↑ %s ↓ %s", formatFeishuTokens(state.InputTokens), formatFeishuTokens(state.OutputTokens)))
	}
	if state.ContextTotal > 0 && state.ContextUsed > 0 {
		pct := state.ContextUsed * 100 / state.ContextTotal
		ctxVal := fmt.Sprintf("%s/%s (%d%%)",
			formatFeishuTokens(state.ContextUsed), formatFeishuTokens(state.ContextTotal), pct)
		// Warn colors near the window limit, same scheme as hermes-lark-streaming.
		switch {
		case pct > 95:
			ctxVal = fmt.Sprintf("<font color='red'>%s</font>", ctxVal)
		case pct > 80:
			ctxVal = fmt.Sprintf("<font color='orange-300'>%s</font>", ctxVal)
		}
		ctxPart := "📦 " + ctxVal
		if state.ContextOffset > 0 {
			ctxPart += fmt.Sprintf(" · ↪ %d", state.ContextOffset)
		}
		line2 = append(line2, ctxPart)
	}
	parts := append(append([]string{}, line1...), line2...)
	if len(parts) == 0 {
		return nil
	}
	color := "grey"
	if aborted {
		color = "orange"
	}
	content := fmt.Sprintf("<font color='%s'>%s</font>", color, strings.Join(line1, " · "))
	if len(line2) > 0 {
		content += fmt.Sprintf("\n<font color='%s'>%s</font>", color, strings.Join(line2, " · "))
	}
	return []any{
		map[string]any{"tag": "hr"},
		map[string]any{"tag": "markdown", "content": content, "text_size": "notation"},
	}
}

// formatFeishuTokens renders token counts compactly: 8.1K / 40.4K / 1.2M.
func formatFeishuTokens(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 1_000:
		return fmt.Sprintf("%.1fK", float64(n)/1_000)
	default:
		return fmt.Sprintf("%d", n)
	}
}

func feishuCardSummary(answer string) map[string]any {
	summary := strings.TrimSpace(answer)
	if idx := strings.IndexAny(summary, "\n"); idx >= 0 {
		summary = summary[:idx]
	}
	summary = strings.ReplaceAll(summary, "```", "")
	if len(summary) > 120 {
		summary = summary[:120]
	}
	if summary == "" {
		summary = "已完成"
	}
	return map[string]any{
		"content": summary,
		"i18n_content": map[string]any{
			"zh_cn": summary,
			"en_us": summary,
		},
	}
}

// countFeishuTagObjects recursively counts JSON objects with a "tag" key —
// Feishu's definition of card element count.
func countFeishuTagObjects(obj any) int {
	switch v := obj.(type) {
	case map[string]any:
		count := 0
		if _, ok := v["tag"]; ok {
			count++
		}
		for _, child := range v {
			count += countFeishuTagObjects(child)
		}
		return count
	case []any:
		count := 0
		for _, child := range v {
			count += countFeishuTagObjects(child)
		}
		return count
	default:
		return 0
	}
}

// enforceFeishuElementLimit is the card-level safety net applied when sealing:
// if the assembled card still exceeds the 200-element cap (despite the
// per-kind display caps), the oldest process-panel children are trimmed and
// summarized in a collapse hint. Answer and footer are never trimmed.
func enforceFeishuElementLimit(card map[string]any) {
	threshold := feishuElementLimit - feishuElementLimitMargin
	total := countFeishuTagObjects(card)
	if total <= threshold {
		return
	}

	body, _ := card["body"].(map[string]any)
	elements, _ := body["elements"].([]any)
	panelIdx := -1
	for i, elem := range elements {
		if m, ok := elem.(map[string]any); ok && m["element_id"] == feishuPanelElementID && m["tag"] == "collapsible_panel" {
			panelIdx = i
			break
		}
	}
	if panelIdx < 0 {
		return
	}
	panel := elements[panelIdx].(map[string]any)
	children, _ := panel["elements"].([]any)

	// Reserve space for the collapse hint unless it already exists.
	hintIdx := -1
	for i, child := range children {
		if m, ok := child.(map[string]any); ok {
			if content, _ := m["content"].(string); strings.HasPrefix(content, "⚡") && strings.Contains(content, "已折叠") {
				hintIdx = i
				break
			}
		}
	}
	if hintIdx < 0 {
		total++
	}

	trimmed := 0
	for total > threshold && len(children) > 1 {
		removeIdx := 0
		if hintIdx == 0 {
			removeIdx = 1
		}
		total -= countFeishuTagObjects(children[removeIdx])
		children = append(children[:removeIdx], children[removeIdx+1:]...)
		if hintIdx == 0 {
			hintIdx = 0 // hint stays first
		} else if hintIdx > 0 {
			hintIdx--
		}
		trimmed++
	}
	if trimmed == 0 {
		return
	}

	if hintIdx >= 0 && hintIdx < len(children) {
		if m, ok := children[hintIdx].(map[string]any); ok {
			if content, _ := m["content"].(string); strings.Contains(content, "已折叠") {
				m["content"] = appendFeishuFoldCount(content, trimmed)
				panel["elements"] = children
				return
			}
		}
	}
	hint := fmt.Sprintf("⚡ 已折叠：%d 项早期过程", trimmed)
	children = append([]any{map[string]any{
		"tag": "markdown", "content": hint, "text_size": "notation",
	}}, children...)
	panel["elements"] = children
}

var feishuFoldCountRe = regexp.MustCompile(`(\d+) 项早期过程`)

func appendFeishuFoldCount(hint string, extra int) string {
	match := feishuFoldCountRe.FindStringSubmatch(hint)
	if match == nil {
		return fmt.Sprintf("⚡ 已折叠：%d 项早期过程", extra)
	}
	var existing int
	fmt.Sscanf(match[1], "%d", &existing)
	return feishuFoldCountRe.ReplaceAllString(hint, fmt.Sprintf("%d 项早期过程", existing+extra))
}

func formatFeishuElapsed(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	if d < time.Minute {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	return fmt.Sprintf("%dm%ds", int(d.Minutes()), int(d.Seconds())%60)
}

var feishuBacktickRunRe = regexp.MustCompile("`+")

var feishuMDSpecialRe = regexp.MustCompile("([`*_{}\\[\\]<>])")

func escapeFeishuMD(value string) string {
	value = strings.ReplaceAll(value, "\\", "\\\\")
	return feishuMDSpecialRe.ReplaceAllString(value, `\$1`)
}

func truncateFeishuReasoning(text string) string {
	if len(text) <= feishuReasoningDisplayLimit {
		return text
	}
	suffix := fmt.Sprintf("…（已截断，共 %d 字）", len(text))
	return cutOnRuneBoundary(text, feishuReasoningDisplayLimit-len(suffix)) + suffix
}

// cutOnRuneBoundary trims up to 3 trailing bytes so the cut does not split a
// multi-byte UTF-8 rune (which would surface as U+FFFD in Feishu).
func cutOnRuneBoundary(s string, max int) string {
	if max >= len(s) {
		return s
	}
	for n := 0; n < 3 && max > 0; n++ {
		if utf8.RuneStart(s[max]) {
			break
		}
		max--
	}
	return s[:max]
}
