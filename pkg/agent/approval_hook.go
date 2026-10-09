package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/channels"
	"github.com/sipeed/picoclaw/pkg/commands"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/constants"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/memory"
	"github.com/sipeed/picoclaw/pkg/session"
)

// HITL 工具审批钩子（fork，agentscope-go borrowing §一，
// docs/design/agentscope-go-borrowing-analysis.zh.md）。它是
// builtinHookRegistry 的第一个内置消费者：config
//
//	"hooks": {"builtins": {"approval": {"enabled": true, "config": {
//	    "ask_patterns": ["git push", "tool:write_file"],
//	    "timeout_ms": 300000
//	}}}}
//
// 设计要点：
//   - 只加 Ask 层：ask_patterns 默认空 = 钩子放行一切，行为与未启用时完
//     全一致（守住开放默认三件套）；deny patterns 仍在工具内部生效且永
//     远是最终裁决（审批通过后工具自身的 deny 检查照样拒绝）。
//   - fail-closed（借 agentscope 原则 "Do not silently approve a tool
//     because an execution path cannot request confirmation"）：无交互通
//     道的会话（cron/心跳，channel 为空或 internal）Ask 命中时立即拒绝；
//     等待超时同样拒绝。绝不静默放行。
//   - 审批回答的入站路由排在 steering 之前（agent.go 消息泵），
//     /approve /deny 不进模型上下文。
//   - 等待挂在 turnCtx 上：hard abort 立即返回拒绝，不留僵尸等待。
const (
	approvalHookName = "approval"

	// defaultApprovalTimeout is sized for IM turnarounds, not local tools.
	defaultApprovalTimeout = 5 * time.Minute

	// approvalAskPreviewRunes bounds the command/args preview in the ask
	// message so a huge tool call cannot blow up the chat bubble.
	approvalAskPreviewRunes = 200
)

// approvalHookConfig is the hooks.builtins.approval.config payload.
type approvalHookConfig struct {
	// AskPatterns 决定哪些调用需要用户批准，语义对齐
	// tools.exec.custom_deny_patterns（正则）：`tool:` 前缀匹配工具名，
	// 其余匹配 exec 的命令串（仅对 exec 工具生效）。默认空 = 放行一切。
	AskPatterns []string `json:"ask_patterns"`
	// InheritExecDenyPatterns pulls tools.exec.custom_deny_patterns into the
	// ask set at mount, so ONE pattern list powers approval gating: hits ask
	// the user, an /approve lets them run. Keep
	// tools.exec.enable_custom_deny_patterns false while this is on — with
	// exec enforcement active an approved command would still be
	// hard-rejected inside exec (mount logs a warning). This is the
	// single-list recipe; explicit ask_patterns is then unnecessary.
	InheritExecDenyPatterns bool `json:"inherit_exec_deny_patterns"`
	// HardDenyPatterns: hits are rejected outright — no ask, no session
	// rule bypass (optional irreducible floor; default empty = everything
	// matched is askable). Same syntax as ask patterns.
	HardDenyPatterns []string `json:"hard_deny_patterns"`
	// TimeoutMS 等待用户回复的上限，超时按拒绝处理。默认 300000。
	// 挂载时 HookManager 的全局 approval 超时会自动抬高到不低于该值。
	TimeoutMS int `json:"timeout_ms"`
}

// approvalReply carries the user's answer; always asks the waiter's owner
// to remember the matched rule for the rest of the session.
type approvalReply struct {
	approved bool
	always   bool
}

// pendingApproval is one session's in-flight approval waiter. channel/chatID
// pin WHERE the question was asked: replies arriving from a different
// channel/chat are not accepted as answers (M1 hardening) — they keep
// flowing to steering instead. pattern is the ask pattern that matched, so
// an "always" answer can promote exactly that rule to session scope.
type pendingApproval struct {
	resolve chan approvalReply
	channel string
	chatID  string
	pattern string
}

// approvalSessionRuleTTL bounds how long an "always" answer keeps
// auto-approving its rule: session-scoped, in-memory, expired rather than
// persisted — a restart or a quiet half-day re-arms the question.
const approvalSessionRuleTTL = 6 * time.Hour

// sessionRuleSet holds one session's always-allow ask patterns.
type sessionRuleSet struct {
	mu    sync.Mutex
	rules map[string]time.Time // ask pattern -> expiry
}

// askRule pairs a compiled ask regex with its configured pattern text; the
// text is what session always-rules remember and report back to the user.
type askRule struct {
	pattern string
	re      *regexp.Regexp
}

// approvalHook implements ToolApprover for config-driven HITL approval.
type approvalHook struct {
	commandAsk      []askRule // matched against the exec command string
	toolAsk         []askRule // matched against the tool name ("tool:" prefix)
	hardDenyCommand []askRule // hard floor: matched against the command, rejected without asking
	hardDenyTool    []askRule // hard floor: matched against the tool name
	inheritExecDeny bool      // pull tools.exec.custom_deny_patterns at mount
	timeout         time.Duration
	pending         sync.Map // sessionKey -> *pendingApproval
	sessionRules    sync.Map // sessionKey -> *sessionRuleSet (always-allow, TTL-bounded)
}

func init() {
	if err := RegisterBuiltinHook(approvalHookName, func(ctx context.Context, spec config.BuiltinHookConfig) (any, error) {
		return newApprovalHookFromConfig(spec.Config)
	}); err != nil {
		// Only reachable on duplicate registration — a programmer error.
		panic(err)
	}
}

func newApprovalHookFromConfig(raw json.RawMessage) (*approvalHook, error) {
	var cfg approvalHookConfig
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return nil, fmt.Errorf("approval hook config: %w", err)
		}
	}
	h := &approvalHook{timeout: defaultApprovalTimeout, inheritExecDeny: cfg.InheritExecDenyPatterns}
	if cfg.TimeoutMS > 0 {
		h.timeout = time.Duration(cfg.TimeoutMS) * time.Millisecond
	}
	// compileRules turns pattern text into ask rules, splitting "tool:"
	// prefixed patterns (matched against the tool name) from command
	// patterns (matched against the exec command string).
	compileRules := func(patterns []string) ([]askRule, []askRule, error) {
		var command, tool []askRule
		for _, pattern := range patterns {
			pattern = strings.TrimSpace(pattern)
			if pattern == "" {
				continue
			}
			if pattern == "tool:" {
				return nil, nil, fmt.Errorf(`approval pattern "tool:" is missing the tool name after the prefix`)
			}
			if rest, ok := strings.CutPrefix(pattern, "tool:"); ok && strings.TrimSpace(rest) != "" {
				re, err := regexp.Compile(strings.TrimSpace(rest))
				if err != nil {
					return nil, nil, fmt.Errorf("approval tool pattern %q: %w", pattern, err)
				}
				tool = append(tool, askRule{pattern: pattern, re: re})
				continue
			}
			re, err := regexp.Compile(pattern)
			if err != nil {
				return nil, nil, fmt.Errorf("approval pattern %q: %w", pattern, err)
			}
			command = append(command, askRule{pattern: pattern, re: re})
		}
		return command, tool, nil
	}
	cmdRules, toolRules, err := compileRules(cfg.AskPatterns)
	if err != nil {
		return nil, err
	}
	h.commandAsk = cmdRules
	h.toolAsk = toolRules
	hardCmd, hardTool, err := compileRules(cfg.HardDenyPatterns)
	if err != nil {
		return nil, err
	}
	h.hardDenyCommand = hardCmd
	h.hardDenyTool = hardTool
	return h, nil
}

// inheritExecDenyPatterns compiles tools.exec.custom_deny_patterns into the
// ask set (deduped against existing rules by pattern text), powering the
// single-list recipe: one pattern list, approval-gated. Invalid regexes are
// skipped with a loud warning (fail-soft per pattern — a typo must not
// disable the whole hook) and reported in the returned count.
func (h *approvalHook) inheritExecDenyPatterns(cfg *config.Config) int {
	if cfg == nil || len(cfg.Tools.Exec.CustomDenyPatterns) == 0 {
		return 0
	}
	existing := make(map[string]bool, len(h.commandAsk)+len(h.toolAsk))
	for _, r := range h.toolAsk {
		existing[r.pattern] = true
	}
	for _, r := range h.commandAsk {
		existing[r.pattern] = true
	}
	added, skipped := 0, 0
	for _, pattern := range cfg.Tools.Exec.CustomDenyPatterns {
		pattern = strings.TrimSpace(pattern)
		if pattern == "" || existing[pattern] {
			continue
		}
		if pattern == "tool:" {
			logger.WarnCF("agent", "Skipping invalid inherited approval pattern", map[string]any{
				"pattern": pattern, "error": `"tool:" is missing the tool name`,
			})
			skipped++
			continue
		}
		if rest, ok := strings.CutPrefix(pattern, "tool:"); ok && strings.TrimSpace(rest) != "" {
			re, err := regexp.Compile(strings.TrimSpace(rest))
			if err != nil {
				logger.WarnCF("agent", "Skipping invalid inherited approval pattern", map[string]any{
					"pattern": pattern, "error": err.Error(),
				})
				skipped++
				continue
			}
			h.toolAsk = append(h.toolAsk, askRule{pattern: pattern, re: re})
			existing[pattern] = true
			added++
			continue
		}
		re, err := regexp.Compile(pattern)
		if err != nil {
			logger.WarnCF("agent", "Skipping invalid inherited approval pattern", map[string]any{
				"pattern": pattern, "error": err.Error(),
			})
			skipped++
			continue
		}
		h.commandAsk = append(h.commandAsk, askRule{pattern: pattern, re: re})
		existing[pattern] = true
		added++
	}
	if added > 0 {
		logger.InfoCF("agent", "Inherited exec deny patterns as approval ask rules", map[string]any{
			"inherited": added, "skipped_invalid": skipped,
			"exec_custom_deny_enabled": cfg.Tools.Exec.EnableCustomDenyPatterns,
		})
	}
	if cfg.Tools.Exec.EnableCustomDenyPatterns && added > 0 {
		logger.WarnCF("agent", "Exec deny enforcement is ALSO active for the inherited patterns; approved commands will still be hard-rejected inside exec — set tools.exec.enable_custom_deny_patterns=false for the single-list approval recipe", map[string]any{})
	}
	return added
}

// matchRules returns the first matching rule's pattern, or "". Phases stay
// separate: tool rules only match the tool name, command rules only match
// the exec command string — a command whose text merely names a tool must
// not trip a "tool:" rule (and vice versa).
func matchRules(toolRules, commandRules []askRule, tool string, args map[string]any) string {
	for _, rule := range toolRules {
		if rule.re.MatchString(tool) {
			return rule.pattern
		}
	}
	command, _ := args["command"].(string)
	if strings.TrimSpace(command) == "" {
		return ""
	}
	for _, rule := range commandRules {
		if rule.re.MatchString(command) {
			return rule.pattern
		}
	}
	return ""
}

// matchedAskPattern returns the ask pattern that requires approval for this
// call, or "" when the call needs none.
func (h *approvalHook) matchedAskPattern(tool string, args map[string]any) string {
	return matchRules(h.toolAsk, h.commandAsk, tool, args)
}

// matchedHardDeny returns the hard-deny pattern that forbids this call, or
// "". Hard-deny rules checked before everything else: no ask, no session
// rule bypass.
func (h *approvalHook) matchedHardDeny(tool string, args map[string]any) string {
	if len(h.hardDenyCommand) == 0 && len(h.hardDenyTool) == 0 {
		return ""
	}
	return matchRules(h.hardDenyTool, h.hardDenyCommand, tool, args)
}

// ruleActive reports whether the session carries a live always-allow rule
// for the pattern, pruning it lazily on expiry.
func (h *approvalHook) ruleActive(sessionKey, pattern string) bool {
	loaded, ok := h.sessionRules.Load(sessionKey)
	if !ok {
		return false
	}
	set, ok := loaded.(*sessionRuleSet)
	if !ok {
		return false
	}
	set.mu.Lock()
	defer set.mu.Unlock()
	expiry, ok := set.rules[pattern]
	if !ok {
		return false
	}
	if time.Now().After(expiry) {
		delete(set.rules, pattern)
		return false
	}
	return true
}

// addSessionRule promotes an ask pattern to session-scope auto-approve.
func (h *approvalHook) addSessionRule(sessionKey, pattern string) {
	loaded, _ := h.sessionRules.LoadOrStore(sessionKey, &sessionRuleSet{rules: make(map[string]time.Time)})
	set, ok := loaded.(*sessionRuleSet)
	if !ok {
		return
	}
	set.mu.Lock()
	defer set.mu.Unlock()
	set.rules[pattern] = time.Now().Add(approvalSessionRuleTTL)
}

// ApproveTool implements ToolApprover. Runs inside HookManager's
// approval-timeout goroutine wrapper; the hook's own timer is the real
// control, and loadConfiguredHooks raises the manager timeout to match.
func (h *approvalHook) ApproveTool(ctx context.Context, req *ToolApprovalRequest) (ApprovalDecision, error) {
	if req == nil {
		return ApprovalDecision{Approved: true}, nil
	}
	// Hard floor first: matches are rejected outright — no ask, no session
	// rule bypass, interactive or not.
	if hard := h.matchedHardDeny(req.Tool, req.Arguments); hard != "" {
		return ApprovalDecision{
			Approved: false,
			Reason: fmt.Sprintf("tool %q matches hard-deny rule %q; this call is not approvable and must not be retried as-is.",
				req.Tool, hard),
		}, nil
	}
	pattern := h.matchedAskPattern(req.Tool, req.Arguments)
	if pattern == "" {
		return ApprovalDecision{Approved: true}, nil
	}

	// Fail-closed for sessions with no interactive channel (cron/heartbeat
	// turns arrive on cli/system/subagent): never wait, never approve.
	channel, chatID := approvalRequestTarget(req)
	if channel == "" || constants.IsInternalChannel(channel) {
		return ApprovalDecision{
			Approved: false,
			Reason: fmt.Sprintf("tool %q matched an approval rule, but this session has no interactive channel (background/cron turn); denied fail-closed. Use a different approach, or let the user run it in an interactive session.",
				req.Tool),
		}, nil
	}

	sessionKey := req.Meta.SessionKey
	if sessionKey == "" {
		return ApprovalDecision{
			Approved: false,
			Reason:   "approval request has no session key; denied fail-closed",
		}, nil
	}

	// Session-scoped always-allow (from a previous "/approve always"):
	// auto-approve without asking. TTL-bounded in-memory only.
	if h.ruleActive(sessionKey, pattern) {
		logger.DebugCF("agent", "Approval auto-allowed by session rule", map[string]any{
			"session_key": sessionKey,
			"pattern":     pattern,
			"tool":        req.Tool,
		})
		return ApprovalDecision{Approved: true}, nil
	}

	resolve := make(chan approvalReply, 1)
	waiter := &pendingApproval{resolve: resolve, channel: channel, chatID: chatID, pattern: pattern}
	if prev, loaded := h.pending.Swap(sessionKey, waiter); loaded {
		// The tool loop is serial per session, so this only happens with a
		// misbehaving parallel path: deny the stale waiter instead of
		// leaving it dangling.
		if p, ok := prev.(*pendingApproval); ok {
			select {
			case p.resolve <- approvalReply{approved: false}:
			default:
			}
		}
	}
	defer h.pending.CompareAndDelete(sessionKey, waiter)

	// Pending-approval marker (web approval card): the ask itself is
	// outbound-only and never enters the transcript, so a web page reload
	// mid-approval would lose the card. The marker restores it from the
	// session API; cleared on every exit path below (reply / abort / timeout).
	if al := AgentLoopFromContext(ctx); al != nil {
		if ms := approvalMarkerStoreFor(al, req.Meta.AgentID); ms != nil {
			ms.MarkApprovalPending(memory.ApprovalMarker{
				SessionKey: sessionKey,
				AgentID:    req.Meta.AgentID,
				Tool:       req.Tool,
				Preview:    approvalCallPreview(req),
				TimeoutMs:  h.timeout.Milliseconds(),
			})
			defer ms.ClearApprovalPending(sessionKey)
		}
	}

	h.publishAsk(ctx, req, channel, chatID)

	timer := time.NewTimer(h.timeout)
	defer timer.Stop()
	select {
	case reply := <-resolve:
		if !reply.approved {
			return ApprovalDecision{
				Approved: false,
				Reason:   fmt.Sprintf("user denied %q via approval reply", req.Tool),
			}, nil
		}
		return ApprovalDecision{Approved: true}, nil
	case <-ctx.Done():
		return ApprovalDecision{
			Approved: false,
			Reason:   fmt.Sprintf("approval wait for %q was cancelled (turn aborted); denied fail-closed", req.Tool),
		}, nil
	case <-timer.C:
		// fail-closed 超时对用户可见：向提问的渠道发拒绝回执（此前超时只
		// 写进 deny reason，用户端的审批提示永远悬着）。web 审批卡靠这条
		// 回执封存；飞书交互卡不识别该文本、仍需点按钮才会封卡（超时封卡
		// 为后续工作，见设计文档 §6）。
		h.publishTimeoutReceipt(ctx, req, channel, chatID)
		return ApprovalDecision{
			Approved: false,
			Reason:   fmt.Sprintf("approval for %q timed out after %s without a user reply; denied fail-closed", req.Tool, h.timeout),
		}, nil
	}
}

// approvalRequestTarget extracts the interactive channel/chat from the
// request's turn context. Empty channel = no interactive surface.
func approvalRequestTarget(req *ToolApprovalRequest) (channel, chatID string) {
	if req.Context == nil || req.Context.Inbound == nil {
		return "", ""
	}
	return req.Context.Inbound.Channel, req.Context.Inbound.ChatID
}

// approvalCallPreview renders what is about to run: the exec command when
// present, otherwise the JSON arguments.
func approvalCallPreview(req *ToolApprovalRequest) string {
	if cmd, _ := req.Arguments["command"].(string); strings.TrimSpace(cmd) != "" {
		return firstNRunes(cmd, approvalAskPreviewRunes)
	}
	if len(req.Arguments) > 0 {
		if b, err := json.Marshal(req.Arguments); err == nil {
			return firstNRunes(string(b), approvalAskPreviewRunes)
		}
	}
	return ""
}

// publishAsk asks the session's channel to render the approval question.
// Channels implementing ApprovalPromptCapable (feishu) get an interactive
// card with buttons; the clicks synthesize /approve /deny through the normal
// inbound path. Anything else — other channels, or a card-render failure —
// falls back to the plain text prompt. Publish runs on a detached 5s-bounded
// context so a cancelled turn cannot also cancel the question the user still
// needs to see.
func (h *approvalHook) publishAsk(ctx context.Context, req *ToolApprovalRequest, channel, chatID string) {
	al := AgentLoopFromContext(ctx)
	if al == nil || al.bus == nil {
		logger.WarnCF("agent", "Approval hook has no bus; question not delivered", map[string]any{
			"session_key": req.Meta.SessionKey,
			"tool":        req.Tool,
		})
		return
	}
	prompt := channels.ApprovalPrompt{
		SessionKey: req.Meta.SessionKey,
		AgentID:    req.Meta.AgentID,
		Tool:       req.Tool,
		Preview:    approvalCallPreview(req),
		Timeout:    h.timeout,
	}

	publishCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if al.channelManager != nil {
		if ch, ok := al.channelManager.GetChannel(channel); ok {
			if pc, ok := ch.(channels.ApprovalPromptCapable); ok {
				if err := pc.ShowApprovalPrompt(publishCtx, chatID, prompt); err == nil {
					return
				} else {
					logger.WarnCF("agent", "Approval prompt card failed; falling back to text", map[string]any{
						"channel": channel,
						"session": req.Meta.SessionKey,
						"error":   err.Error(),
					})
				}
			}
		}
	}

	content := fmt.Sprintf("⚠️ 需要批准：即将执行工具 %s", req.Tool)
	if prompt.Preview != "" {
		content += "\n" + prompt.Preview
	}
	content += fmt.Sprintf("\n\n回复 /approve 允许、/approve always 本会话内不再询问、/deny 拒绝（超过 %s 未回复将按拒绝处理）", h.timeout)

	_ = al.bus.PublishOutbound(publishCtx, bus.OutboundMessage{
		Channel:    channel,
		ChatID:     chatID,
		Context:    outboundContextFromInbound(req.Context.Inbound, channel, chatID, ""),
		AgentID:    req.Meta.AgentID,
		SessionKey: req.Meta.SessionKey,
		Content:    content,
		// v1 协议元数据：web 审批卡用它拿到精确 tool/preview/timeout
		//（兜底文本通道仍在，供旧前端/历史回放使用）。
		Approval: &bus.ApprovalRequestMeta{
			Tool:      req.Tool,
			Preview:   prompt.Preview,
			TimeoutMs: h.timeout.Milliseconds(),
		},
	})
}

// approvalMarkerStoreFor resolves the session-layer approval marker store
// for the asking agent (durable on the JSONL backend; nil when unavailable).
func approvalMarkerStoreFor(al *AgentLoop, agentID string) session.ApprovalMarkerStore {
	if al == nil || al.registry == nil || agentID == "" {
		return nil
	}
	agent, ok := al.registry.GetAgent(agentID)
	if !ok || agent.Sessions == nil {
		return nil
	}
	ms, _ := agent.Sessions.(session.ApprovalMarkerStore)
	return ms
}

// publishTimeoutReceipt delivers the fail-closed timeout outcome to the
// surface the question was asked on, so interactive prompts (feishu/web
// approval cards) can seal instead of hanging open forever. Runs detached
// from the turn context, mirroring publishAsk.
func (h *approvalHook) publishTimeoutReceipt(ctx context.Context, req *ToolApprovalRequest, channel, chatID string) {
	al := AgentLoopFromContext(ctx)
	if al == nil || al.bus == nil {
		return
	}
	publishCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = al.bus.PublishOutbound(publishCtx, bus.OutboundMessage{
		Channel:    channel,
		ChatID:     chatID,
		Context:    outboundContextFromInbound(req.Context.Inbound, channel, chatID, ""),
		AgentID:    req.Meta.AgentID,
		SessionKey: req.Meta.SessionKey,
		Content:    fmt.Sprintf("⛔ 审批超时（%s），已按拒绝处理（fail-closed）。", h.timeout),
	})
}

// parseApprovalReply recognizes explicit user answers only: /approve /deny
// (plus yes/no aliases and Chinese 同意/拒绝/批准), and the always-allow
// forms "/approve always" / "总是允许" which additionally promote the matched
// rule to session scope. A command form only counts when it is the ENTIRE
// message (or exactly command+always) — "/approve 请继续删库" carries extra
// intent and must not silently approve (fail-closed: better to ask again
// than to mis-approve). Deny has no always form: refusals stay per-call.
// Anything else returns ok=false so the message keeps flowing to steering.
func parseApprovalReply(content string) (approved, always, ok bool) {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return false, false, false
	}
	fields := strings.Fields(trimmed)
	if name, isCmd := commands.CommandName(trimmed); isCmd {
		if len(fields) == 2 && strings.EqualFold(fields[1], "always") {
			if strings.ToLower(name) == "approve" {
				return true, true, true
			}
			return false, false, false
		}
		if len(fields) != 1 {
			return false, false, false // command plus trailing content — not an answer
		}
		switch strings.ToLower(name) {
		case "approve", "yes":
			return true, false, true
		case "deny", "no":
			return false, false, true
		}
		return false, false, false
	}
	if len(fields) == 1 {
		switch trimmed {
		case "同意", "批准":
			return true, false, true
		case "拒绝":
			return false, false, true
		case "总是允许":
			return true, true, true
		}
	}
	return false, false, false
}

// tryHandleApprovalReply runs in the inbound pump BEFORE the turn-state
// claim (so it covers both an active approval wait and stale tokens arriving
// after the turn ended): an explicit answer FROM THE SAME CHANNEL/CHAT the
// question was asked in resolves the waiter and is consumed (never enters
// the model context). Stale tokens — a late button click after the question
// expired or was answered — are dropped with a receipt for the same reason.
// Replies from other surfaces fall through to steering — a different chat
// must not be able to approve another chat's pending tool (M1).
// Returns true when the message was consumed.
func (al *AgentLoop) tryHandleApprovalReply(ctx context.Context, msg bus.InboundMessage, sessionKey string) bool {
	hook := al.approvalHook
	if hook == nil {
		return false
	}
	approved, always, ok := parseApprovalReply(msg.Content)
	if !ok {
		return false
	}
	loaded, exists := hook.pending.Load(sessionKey)
	if !exists {
		// Orphan approval token (late click / typed reply after the question
		// expired or was already answered): drop with a receipt instead of
		// leaking the protocol word into steering or a fresh turn.
		al.publishApprovalReceipt(ctx, msg, "当前没有待批准的操作，本次回复已忽略。")
		logger.InfoCF("agent", "Orphan approval reply dropped", map[string]any{
			"session_key": sessionKey,
			"content":     msg.Content,
		})
		return true
	}
	p, ok := loaded.(*pendingApproval)
	if !ok {
		return false
	}
	if p.channel != msg.Channel || p.chatID != msg.ChatID {
		logger.DebugCF("agent", "Approval reply from a different chat ignored", map[string]any{
			"session_key":  sessionKey,
			"asked_on":     p.channel + "/" + p.chatID,
			"replied_from": msg.Channel + "/" + msg.ChatID,
		})
		return false
	}
	select {
	case p.resolve <- approvalReply{approved: approved, always: always}:
	default:
		return false // already resolved by another reply
	}

	receipt := "⛔ 已拒绝本次工具执行。"
	if approved {
		receipt = "✅ 已批准，继续执行。"
		if always && p.pattern != "" {
			hook.addSessionRule(sessionKey, p.pattern)
			receipt = fmt.Sprintf("✅ 已批准；本会话内命中规则 %q 的调用将自动放行（约 %d 小时后或重启失效）。",
				p.pattern, int(approvalSessionRuleTTL.Hours()))
		}
	}
	al.publishApprovalReceipt(ctx, msg, receipt)
	logger.InfoCF("agent", "Approval reply routed", map[string]any{
		"session_key": sessionKey,
		"approved":    approved,
		"always":      always,
	})
	return true
}

// publishApprovalReceipt sends a short user-facing confirmation for an
// approval answer (or its drop) on the surface the reply came from.
func (al *AgentLoop) publishApprovalReceipt(ctx context.Context, msg bus.InboundMessage, content string) {
	if al.bus == nil {
		return
	}
	publishCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = al.bus.PublishOutbound(publishCtx, bus.OutboundMessage{
		Channel:    msg.Channel,
		ChatID:     msg.ChatID,
		Context:    outboundContextFromInbound(nil, msg.Channel, msg.ChatID, ""),
		SessionKey: msg.SessionKey,
		Content:    content,
	})
}
