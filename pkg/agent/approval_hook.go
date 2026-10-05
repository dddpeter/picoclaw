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
	"github.com/sipeed/picoclaw/pkg/commands"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/constants"
	"github.com/sipeed/picoclaw/pkg/logger"
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
	// TimeoutMS 等待用户回复的上限，超时按拒绝处理。默认 300000。
	// 挂载时 HookManager 的全局 approval 超时会自动抬高到不低于该值。
	TimeoutMS int `json:"timeout_ms"`
}

type approvalReply struct {
	approved bool
}

// pendingApproval is one session's in-flight approval waiter. channel/chatID
// pin WHERE the question was asked: replies arriving from a different
// channel/chat are not accepted as answers (M1 hardening) — they keep
// flowing to steering instead.
type pendingApproval struct {
	resolve chan approvalReply
	channel string
	chatID  string
}

// approvalHook implements ToolApprover for config-driven HITL approval.
type approvalHook struct {
	commandAsk []*regexp.Regexp // matched against the exec command string
	toolAsk    []*regexp.Regexp // matched against the tool name ("tool:" prefix)
	timeout    time.Duration
	pending    sync.Map // sessionKey -> chan approvalReply (cap 1)
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
	h := &approvalHook{timeout: defaultApprovalTimeout}
	if cfg.TimeoutMS > 0 {
		h.timeout = time.Duration(cfg.TimeoutMS) * time.Millisecond
	}
	for _, pattern := range cfg.AskPatterns {
		pattern = strings.TrimSpace(pattern)
		if pattern == "" {
			continue
		}
		if pattern == "tool:" {
			return nil, fmt.Errorf(`approval ask pattern "tool:" is missing the tool name after the prefix`)
		}
		if rest, ok := strings.CutPrefix(pattern, "tool:"); ok && strings.TrimSpace(rest) != "" {
			re, err := regexp.Compile(strings.TrimSpace(rest))
			if err != nil {
				return nil, fmt.Errorf("approval tool ask pattern %q: %w", pattern, err)
			}
			h.toolAsk = append(h.toolAsk, re)
			continue
		}
		re, err := regexp.Compile(pattern)
		if err != nil {
			return nil, fmt.Errorf("approval ask pattern %q: %w", pattern, err)
		}
		h.commandAsk = append(h.commandAsk, re)
	}
	return h, nil
}

// needsApproval reports whether the call matches an ask pattern.
func (h *approvalHook) needsApproval(tool string, args map[string]any) bool {
	for _, re := range h.toolAsk {
		if re.MatchString(tool) {
			return true
		}
	}
	if len(h.commandAsk) == 0 {
		return false
	}
	command, _ := args["command"].(string)
	if strings.TrimSpace(command) == "" {
		return false
	}
	for _, re := range h.commandAsk {
		if re.MatchString(command) {
			return true
		}
	}
	return false
}

// ApproveTool implements ToolApprover. Runs inside HookManager's
// approval-timeout goroutine wrapper; the hook's own timer is the real
// control, and loadConfiguredHooks raises the manager timeout to match.
func (h *approvalHook) ApproveTool(ctx context.Context, req *ToolApprovalRequest) (ApprovalDecision, error) {
	if req == nil {
		return ApprovalDecision{Approved: true}, nil
	}
	if !h.needsApproval(req.Tool, req.Arguments) {
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

	resolve := make(chan approvalReply, 1)
	waiter := &pendingApproval{resolve: resolve, channel: channel, chatID: chatID}
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

// publishAsk sends the approval question to the session's channel. Publish
// runs on a detached 5s-bounded context so a cancelled turn cannot also
// cancel the question the user still needs to see.
func (h *approvalHook) publishAsk(ctx context.Context, req *ToolApprovalRequest, channel, chatID string) {
	al := AgentLoopFromContext(ctx)
	if al == nil || al.bus == nil {
		logger.WarnCF("agent", "Approval hook has no bus; question not delivered", map[string]any{
			"session_key": req.Meta.SessionKey,
			"tool":        req.Tool,
		})
		return
	}
	preview := approvalCallPreview(req)
	content := fmt.Sprintf("⚠️ 需要批准：即将执行工具 %s", req.Tool)
	if preview != "" {
		content += "\n" + preview
	}
	content += fmt.Sprintf("\n\n回复 /approve 允许、/deny 拒绝（超过 %s 未回复将按拒绝处理）", h.timeout)

	publishCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = al.bus.PublishOutbound(publishCtx, bus.OutboundMessage{
		Channel:    channel,
		ChatID:     chatID,
		Context:    outboundContextFromInbound(req.Context.Inbound, channel, chatID, ""),
		AgentID:    req.Meta.AgentID,
		SessionKey: req.Meta.SessionKey,
		Content:    content,
	})
}

// parseApprovalReply recognizes explicit user answers only: /approve /deny
// (plus yes/no aliases and Chinese 同意/拒绝/批准). A command form only
// counts when it is the ENTIRE message — "/approve 请继续删库" carries extra
// intent and must not silently approve (fail-closed: better to ask again
// than to mis-approve). Anything else returns ok=false so the message keeps
// flowing to steering.
func parseApprovalReply(content string) (approved, ok bool) {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return false, false
	}
	if name, isCmd := commands.CommandName(trimmed); isCmd {
		if len(strings.Fields(trimmed)) != 1 {
			return false, false // command plus trailing content — not an answer
		}
		switch strings.ToLower(name) {
		case "approve", "yes":
			return true, true
		case "deny", "no":
			return false, true
		}
		return false, false
	}
	switch trimmed {
	case "同意", "批准":
		return true, true
	case "拒绝":
		return false, true
	}
	return false, false
}

// tryHandleApprovalReply runs in the inbound pump BEFORE steering enqueue:
// when the session has a pending approval and the message parses as an
// explicit answer FROM THE SAME CHANNEL/CHAT the question was asked in,
// resolve the waiter and consume the message (it must not enter the model
// context). Replies from other surfaces fall through to steering — a
// different chat must not be able to approve another chat's pending tool.
// Returns true when consumed.
func (al *AgentLoop) tryHandleApprovalReply(ctx context.Context, msg bus.InboundMessage, sessionKey string) bool {
	hook := al.approvalHook
	if hook == nil {
		return false
	}
	approved, ok := parseApprovalReply(msg.Content)
	if !ok {
		return false
	}
	loaded, exists := hook.pending.Load(sessionKey)
	if !exists {
		return false
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
	case p.resolve <- approvalReply{approved: approved}:
	default:
		return false // already resolved by another reply
	}

	receipt := "⛔ 已拒绝本次工具执行。"
	if approved {
		receipt = "✅ 已批准，继续执行。"
	}
	publishCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = al.bus.PublishOutbound(publishCtx, bus.OutboundMessage{
		Channel:    msg.Channel,
		ChatID:     msg.ChatID,
		Context:    outboundContextFromInbound(nil, msg.Channel, msg.ChatID, ""),
		SessionKey: sessionKey,
		Content:    receipt,
	})
	logger.InfoCF("agent", "Approval reply routed", map[string]any{
		"session_key": sessionKey,
		"approved":    approved,
	})
	return true
}
