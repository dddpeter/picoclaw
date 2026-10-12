// PicoClaw - Ultra-lightweight personal AI agent
// License: MIT

package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/commands"
	"github.com/sipeed/picoclaw/pkg/constants"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/memory"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/session"
	"github.com/sipeed/picoclaw/pkg/tools"
)

// ask_user (octop borrowing §三): the agent asks the user structured
// questions mid-turn and blocks until they are answered. Rides the approval
// wait plumbing in shape (block on a channel, publish a question card, pin
// the answer to the asking channel/chat) but keeps fully separate state:
// answers never form approval rules or any allow policy — asks are UX, not
// gates. Timeout semantics are the OPPOSITE of approval: a timeout or a
// channel-less session is a SOFT failure ("user did not answer"), never a
// hard denial; the model decides how to continue.

const (
	askUserToolName = "ask_user"

	askUserMaxQuestions = 5
	askUserMaxOptions   = 6
	askMaxQuestionRunes = 400
	askMaxLabelRunes    = 120
	// askMaxAnswerRunes caps one free-text answer line (the /deny reason cap
	// is the precedent — a pasted log must not ride into the context whole).
	askMaxAnswerRunes = 4000

	// defaultAskUserTimeout bounds one ask wait (soft). Generous on purpose:
	// the user may be away; the stall watchdog is kept calm by touchActivity.
	defaultAskUserTimeout = 10 * time.Minute
)

// askKeepAliveInterval keeps the stall watchdog calm during the wait
// (octop borrowing §三-5: without this a multi-question wait would be
// mistaken for a stalled turn and hard-aborted). Var (not const) so tests
// can shrink it.
var askKeepAliveInterval = 30 * time.Second

// askOptionKeys maps "A".."F" / "1".."6" reply tokens to option indexes.
var askOptionKeys = []string{"A", "B", "C", "D", "E", "F"}

// askReply is the resolved answer set: one normalized answer per question,
// or skipped when the user chose /skip.
type askReply struct {
	answers []string
	skipped bool
}

// pendingAsk is one session's in-flight ask wait. channel/chatID pin WHERE
// the question was asked: replies from a different chat are not accepted.
type pendingAsk struct {
	resolve   chan askReply
	channel   string
	chatID    string
	questions []bus.AskQuestion
}

// askUserToolDef is the LLM-facing definition (kept out of the registry,
// injected per-LLM-call; execution is intercepted in the tool loop).
func askUserToolDef() providers.ToolDefinition {
	return providers.ToolDefinition{
		Type: "function",
		Function: providers.ToolFunctionDefinition{
			Name: askUserToolName,
			Description: "Ask the user structured questions and wait for their answers. " +
				"Use ONLY when genuinely blocked on a user decision you cannot infer. " +
				"Options become clickable; the user may also answer with free text " +
				"(letters A-F or numbers pick options; multi-select joins with \"; \").",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"questions": map[string]any{
						"type":        "array",
						"minItems":    1,
						"maxItems":    askUserMaxQuestions,
						"description": "1-5 questions; each with question text, optional header, up to 6 options",
						"items": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"question":     map[string]any{"type": "string"},
								"header":       map[string]any{"type": "string", "description": "short topic label"},
								"multi_select": map[string]any{"type": "boolean"},
								"options": map[string]any{
									"type":     "array",
									"maxItems": askUserMaxOptions,
									"items": map[string]any{
										"type": "object",
										"properties": map[string]any{
											"label":       map[string]any{"type": "string"},
											"description": map[string]any{"type": "string"},
										},
										"required": []string{"label"},
									},
								},
							},
							"required": []string{"question"},
						},
					},
				},
				"required": []string{"questions"},
			},
		},
	}
}

// parseAskQuestions validates and normalizes the tool arguments.
func parseAskQuestions(args map[string]any) ([]bus.AskQuestion, error) {
	rawQuestions, ok := args["questions"].([]any)
	if !ok || len(rawQuestions) == 0 {
		return nil, fmt.Errorf("questions must be a non-empty array")
	}
	if len(rawQuestions) > askUserMaxQuestions {
		return nil, fmt.Errorf("at most %d questions per call", askUserMaxQuestions)
	}
	out := make([]bus.AskQuestion, 0, len(rawQuestions))
	for _, rawQ := range rawQuestions {
		qMap, ok := rawQ.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("each question must be an object")
		}
		q := bus.AskQuestion{
			Question:    firstNRunes(strings.TrimSpace(strArg(qMap["question"])), askMaxQuestionRunes),
			Header:      firstNRunes(strings.TrimSpace(strArg(qMap["header"])), askMaxLabelRunes),
			MultiSelect: qMap["multi_select"] == true,
		}
		if q.Question == "" {
			return nil, fmt.Errorf("question text is required")
		}
		if rawOpts, ok := qMap["options"].([]any); ok && len(rawOpts) > 0 {
			if len(rawOpts) > askUserMaxOptions {
				return nil, fmt.Errorf("question %q has more than %d options", q.Question, askUserMaxOptions)
			}
			for _, rawO := range rawOpts {
				oMap, ok := rawO.(map[string]any)
				if !ok {
					return nil, fmt.Errorf("each option must be an object")
				}
				opt := bus.AskOption{
					Label:       firstNRunes(strings.TrimSpace(strArg(oMap["label"])), askMaxLabelRunes),
					Description: firstNRunes(strings.TrimSpace(strArg(oMap["description"])), askMaxLabelRunes),
				}
				if opt.Label == "" {
					return nil, fmt.Errorf("option label is required")
				}
				q.Options = append(q.Options, opt)
			}
		}
		out = append(out, q)
	}
	return out, nil
}

func strArg(v any) string {
	s, _ := v.(string)
	return s
}

// runAskUser publishes the question card and blocks for the answer. Runs on
// the turn goroutine (intercepted before registry execution); the wait hangs
// on turnCtx so a hard abort unblocks it immediately, and a keepalive ticker
// keeps the stall watchdog from mistaking the wait for a dead turn.
func (al *AgentLoop) runAskUser(ctx context.Context, ts *turnState, args map[string]any) *tools.ToolResult {
	questions, err := parseAskQuestions(args)
	if err != nil {
		return tools.ErrorResult(fmt.Sprintf("ask_user: %v", err))
	}

	// Soft fail: sessions without an interactive channel (cron/heartbeat)
	// can never be answered — tell the model and let it proceed.
	if ts.channel == "" || constants.IsInternalChannel(ts.channel) {
		return tools.NewToolResult(
			"The user cannot be asked here: this session has no interactive channel. " +
				"Proceed autonomously with a safe default, or finish and explain what you would have asked.")
	}

	timeout := al.askTimeout
	if timeout <= 0 {
		timeout = defaultAskUserTimeout
	}

	resolve := make(chan askReply, 1)
	waiter := &pendingAsk{
		resolve:   resolve,
		channel:   ts.channel,
		chatID:    ts.chatID,
		questions: questions,
	}
	if prev, loaded := al.pendingAsks.Swap(ts.sessionKey, waiter); loaded {
		// Stale ask from an aborted path: answer it as skipped so its waiter
		// (if any) unblocks instead of dangling.
		if p, ok := prev.(*pendingAsk); ok {
			select {
			case p.resolve <- askReply{skipped: true}:
			default:
			}
		}
	}
	defer al.pendingAsks.CompareAndDelete(ts.sessionKey, waiter)

	al.publishAskQuestion(ts, questions, timeout)

	// Pending-ask marker (web ask card — octop borrowing §三): the question
	// itself is outbound-only and never enters the transcript; the marker
	// restores the card after a page reload. Cleared on every exit path
	// below (answer / skip / timeout / abort) via the defer.
	if ms := askMarkerStoreFor(al, ts.agent.ID); ms != nil {
		if questionsJSON, jerr := json.Marshal(questions); jerr == nil {
			ms.MarkAskPending(memory.AskMarker{
				SessionKey: ts.sessionKey,
				AgentID:    ts.agent.ID,
				Questions:  questionsJSON,
				TimeoutMs:  timeout.Milliseconds(),
			})
			defer ms.ClearAskPending(ts.sessionKey)
		}
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	// Keepalive cadence must sit well under the watchdog's stall threshold:
	// the heartbeat loop clamps stallAfter to >= 2×interval, so an
	// aggressively configured heartbeat (10s beats → 20s threshold) would
	// outrun a fixed 30s keepalive (review P1-4).
	keepAliveInterval := askKeepAliveInterval
	if stall := al.askStallThreshold(ts); stall > 0 {
		if third := stall / 3; third < keepAliveInterval {
			keepAliveInterval = third
		}
	}
	if keepAliveInterval < 50*time.Millisecond {
		keepAliveInterval = 50 * time.Millisecond
	}
	keepAlive := time.NewTicker(keepAliveInterval)
	defer keepAlive.Stop()

	for {
		select {
		case reply := <-resolve:
			return askResultFor(questions, reply)
		case <-ctx.Done():
			// Close the late-reply race before returning: without this, a
			// reply arriving in the return-to-defer window would be consumed
			// and acknowledged while the turn proceeds as unanswered
			// (review P2-3; the defers stay as safety nets).
			al.pendingAsks.CompareAndDelete(ts.sessionKey, waiter)
			return tools.NewToolResult("The turn was aborted before the user answered; treat the questions as unanswered.")
		case <-timer.C:
			al.pendingAsks.CompareAndDelete(ts.sessionKey, waiter)
			al.publishAskTimeoutReceipt(ts, timeout)
			return tools.NewToolResult(fmt.Sprintf(
				"The user did not answer within %s. Treat the questions as unanswered and decide how to continue.",
				timeout.Round(time.Second)))
		case <-keepAlive.C:
			// Octop borrowing §三-5: the wait itself counts as progress —
			// without these beats the stall watchdog hard-aborts the turn
			// on the second question of a multi-question ask.
			ts.touchActivity()
		}
	}
}

// askResultFor renders the deterministic answer list the model receives.
func askResultFor(questions []bus.AskQuestion, reply askReply) *tools.ToolResult {
	if reply.skipped {
		return tools.NewToolResult("The user skipped the questions. Decide how to proceed, or finish with what you have.")
	}
	var b strings.Builder
	b.WriteString("The user answered:")
	for i, q := range questions {
		answer := ""
		if i < len(reply.answers) {
			answer = reply.answers[i]
		}
		if answer == "" {
			answer = "(unanswered)"
		}
		label := q.Header
		if label == "" {
			label = q.Question
		}
		fmt.Fprintf(&b, "\n- %s: %s", label, answer)
	}
	return tools.NewToolResult(b.String())
}

// askMarkerStoreFor resolves the session-layer ask marker store for the
// asking agent (durable on the JSONL backend; nil when unavailable).
func askMarkerStoreFor(al *AgentLoop, agentID string) session.AskMarkerStore {
	if al == nil || al.registry == nil || agentID == "" {
		return nil
	}
	agent, ok := al.registry.GetAgent(agentID)
	if !ok || agent.Sessions == nil {
		return nil
	}
	ms, _ := agent.Sessions.(session.AskMarkerStore)
	return ms
}

// publishAskQuestion sends the question card: structured payload for
// channels that render it (pico "ask" payload → web card) with a plain-text
// fallback every channel can display. Detached context so a cancelled turn
// cannot also cancel the question the user still needs to see.
func (al *AgentLoop) publishAskQuestion(ts *turnState, questions []bus.AskQuestion, timeout time.Duration) {
	if al.bus == nil {
		return
	}
	msg := outboundMessageForTurn(ts, formatAskCardText(questions, timeout))
	msg.Ask = &bus.AskRequestMeta{
		Questions: questions,
		TimeoutMs: timeout.Milliseconds(),
	}
	// The pico channel marks ask/approval cards as progress_note by payload
	// presence — no generic message_kind here (see markPicoProgressNote).
	pubCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = al.bus.PublishOutbound(pubCtx, msg)
}

// formatAskCardText renders the plain-text form: numbered questions with
// lettered options (one message; the user replies line-by-line, or /skip).
func formatAskCardText(questions []bus.AskQuestion, timeout time.Duration) string {
	var b strings.Builder
	b.WriteString("❓ 需要你的输入：")
	for i, q := range questions {
		if len(questions) > 1 {
			fmt.Fprintf(&b, "\n%d. ", i+1)
		} else {
			b.WriteString("\n")
		}
		if q.Header != "" {
			fmt.Fprintf(&b, "【%s】", q.Header)
		}
		b.WriteString(q.Question)
		for j, opt := range q.Options {
			key := askOptionKeys[j]
			if opt.Description != "" {
				fmt.Fprintf(&b, "\n  %s. %s — %s", key, opt.Label, opt.Description)
			} else {
				fmt.Fprintf(&b, "\n  %s. %s", key, opt.Label)
			}
		}
	}
	if len(questions) > 1 {
		b.WriteString("\n（多题请按行序回复，每题一行）")
	}
	fmt.Fprintf(&b, "\n回复作答；回复 /skip 跳过（超过 %s 未回复将按未作答处理）。", timeout.Round(time.Minute))
	return b.String()
}

// tryHandleAskReply runs in the inbound pump's busy branch (octop borrowing
// §三-3), AFTER the approval interceptor: an explicit /skip — or any plain
// text — FROM THE SAME CHANNEL/CHAT the question was asked in resolves the
// waiter and is consumed (never enters the model context). Slash commands
// other than /skip fall through to the command path; a bare orphan /skip
// (no pending ask) is consumed with a receipt instead of leaking the
// protocol word into the model context (the /stop precedent). Returns true
// when consumed.
func (al *AgentLoop) tryHandleAskReply(ctx context.Context, msg bus.InboundMessage, sessionKey string) bool {
	loaded, ok := al.pendingAsks.Load(sessionKey)
	if !ok {
		return al.consumeOrphanSkip(ctx, msg)
	}
	p, ok := loaded.(*pendingAsk)
	if !ok {
		return false
	}
	if p.channel != msg.Channel || p.chatID != msg.ChatID {
		return false // asked elsewhere; keep flowing (steering/commands)
	}
	if commands.HasCommandPrefix(msg.Content) {
		if name, isCmd := commands.CommandName(msg.Content); !isCmd || name != "skip" {
			return false // other commands still route normally
		}
	}
	select {
	case p.resolve <- collectAskReply(p.questions, msg.Content):
	default:
		return false // already resolved by another reply
	}
	al.publishAskReceipt(ctx, msg, commands.HasCommandPrefix(msg.Content))
	logger.InfoCF("agent", "Ask reply routed", map[string]any{
		"session_key": sessionKey,
		"skipped":     commands.HasCommandPrefix(msg.Content),
	})
	return true
}

// consumeOrphanSkip answers a bare /skip that has no pending ask with a
// receipt, so the protocol word never reaches the model context (mirrors the
// approval orphan handling in tryHandleApprovalReply).
func (al *AgentLoop) consumeOrphanSkip(ctx context.Context, msg bus.InboundMessage) bool {
	if !commands.HasCommandPrefix(msg.Content) {
		return false
	}
	if name, isCmd := commands.CommandName(msg.Content); !isCmd || name != "skip" {
		return false
	}
	if fields := lenFields(msg.Content); fields != 1 {
		return false // trailing content — not a bare /skip
	}
	al.publishAskReceiptText(ctx, msg, "❓ 当前没有待回答的问题。")
	return true
}

// collectAskReply parses one inbound message into the per-question answer
// set: /skip → skipped; otherwise split lines (fewer lines than questions
// leave the rest empty) with option-token normalization. Lines are capped
// (review P2-2: a pasted log would otherwise ride into the model context
// verbatim; the /deny reason cap is the precedent).
func collectAskReply(questions []bus.AskQuestion, text string) askReply {
	trimmed := strings.TrimSpace(text)
	if name, isCmd := commands.CommandName(trimmed); isCmd && name == "skip" {
		return askReply{skipped: true}
	}
	lines := strings.Split(trimmed, "\n")
	answers := make([]string, len(questions))
	for i := range questions {
		if i >= len(lines) {
			break
		}
		answers[i] = normalizeAskAnswer(questions[i], firstNRunes(strings.TrimSpace(lines[i]), askMaxAnswerRunes))
	}
	return askReply{answers: answers}
}

// normalizeAskAnswer maps reply tokens to canonical option labels: an exact
// (case-insensitive) label match, or a letter A-F / number 1-6 selecting the
// option index; multi-select splits on ; ， , 、 and joins labels with "; ".
// Free text rides through unchanged.
func normalizeAskAnswer(q bus.AskQuestion, raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(q.Options) == 0 {
		return raw
	}
	tokens := []string{raw}
	if q.MultiSelect {
		tokens = strings.FieldsFunc(raw, func(r rune) bool {
			return r == ';' || r == '；' || r == ',' || r == '，' || r == '、'
		})
	}
	labels := make([]string, 0, len(tokens))
	for _, tok := range tokens {
		tok = strings.TrimSpace(tok)
		if tok == "" {
			continue
		}
		labels = append(labels, askOptionLabel(q, tok))
	}
	return strings.Join(labels, "; ")
}

// askOptionLabel resolves one token to its canonical option label, or the
// token itself when it matches nothing.
func askOptionLabel(q bus.AskQuestion, tok string) string {
	lower := strings.ToLower(tok)
	for i, opt := range q.Options {
		if lower == strings.ToLower(opt.Label) {
			return opt.Label
		}
		if i < len(askOptionKeys) {
			if tok == askOptionKeys[i] || strings.EqualFold(tok, askOptionKeys[i]) {
				return opt.Label
			}
		}
		if n, ok := atoiSmall(tok); ok && n >= 1 && n <= len(q.Options) && q.Options[n-1].Label != "" {
			return q.Options[n-1].Label
		}
	}
	return tok
}

// atoiSmall parses 1-2 digit integers without pulling strconv into hot paths.
func atoiSmall(s string) (int, bool) {
	if len(s) == 0 || len(s) > 2 {
		return 0, false
	}
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, false
		}
		n = n*10 + int(r-'0')
	}
	return n, true
}

// publishAskReceipt acknowledges the answer on the surface it came from.
// Skip and answer get distinct receipts so the web card can seal with the
// right verdict (review P2-1: both used to say "已收到回答").
func (al *AgentLoop) publishAskReceipt(ctx context.Context, msg bus.InboundMessage, skipped bool) {
	if skipped {
		al.publishAskReceiptText(ctx, msg, "⏭️ 已跳过问询，继续。")
		return
	}
	al.publishAskReceiptText(ctx, msg, "📝 已收到回答，正在继续。")
}

// publishAskTimeoutReceipt tells the asking surface the wait is over, so
// interactive cards seal instead of hanging clickable after the soft timeout
// (review P2-5; the approval hook's publishTimeoutReceipt precedent).
func (al *AgentLoop) publishAskTimeoutReceipt(ts *turnState, timeout time.Duration) {
	if al.bus == nil {
		return
	}
	msg := outboundMessageForTurn(ts, fmt.Sprintf("⏰ 问询超过 %s 未作答，按未作答处理，继续执行。", timeout.Round(time.Minute)))
	markPicoProgressNote(&msg)
	pubCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = al.bus.PublishOutbound(pubCtx, msg)
}

// publishAskReceiptText delivers a raw receipt text as a non-final beat
// (review P1-3: receipts must not read as the turn's final answer — the
// model keeps generating after the answer arrives).
func (al *AgentLoop) publishAskReceiptText(ctx context.Context, msg bus.InboundMessage, text string) {
	if al.bus == nil {
		return
	}
	out := bus.OutboundMessage{
		Channel: msg.Channel,
		ChatID:  msg.ChatID,
		Context: outboundContextFromInbound(nil, msg.Channel, msg.ChatID, ""),
		Content: text,
	}
	markPicoProgressNote(&out)
	pubCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = al.bus.PublishOutbound(pubCtx, out)
}

// askStallThreshold mirrors the heartbeat loop's stall-threshold derivation
// (ts override → config, clamped to >= 2× the heartbeat interval) so the
// ask keepalive can pace itself under it. 0 = watchdog disabled.
func (al *AgentLoop) askStallThreshold(ts *turnState) time.Duration {
	if ts == nil {
		return 0
	}
	stall := ts.stallInterruptAfter
	if stall <= 0 && al != nil && al.GetConfig() != nil {
		stall = time.Duration(al.GetConfig().Agents.Defaults.GetProgressStallInterruptSeconds()) * time.Second
	}
	if stall <= 0 {
		return 0
	}
	if al != nil && al.GetConfig() != nil {
		if interval := time.Duration(al.GetConfig().Agents.Defaults.GetProgressHeartbeatSeconds()) * time.Second; interval > 0 && stall < 2*interval {
			stall = 2 * interval
		}
	}
	return stall
}
