//go:build amd64 || arm64 || riscv64 || mips64 || ppc64

package feishu

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/google/uuid"
	larkcardkit "github.com/larksuite/oapi-sdk-go/v3/service/cardkit/v1"

	"github.com/sipeed/picoclaw/pkg/logger"
)

// Approval callback commands carried by the approval card buttons.
const (
	feishuApproveCmd = "approve"
	feishuDenyCmd    = "deny"

	// approvalDenyReason is the model-facing explanation for a user denial.
	approvalDenyReason = "用户在审批卡上拒绝了该命令"
)

// feishuApprovalDecision is the outcome resolved by a button click (or the
// waiter on timeout/cancel).
type feishuApprovalDecision struct {
	approved bool
	reason   string
}

// feishuApprovalPending is one in-flight approval request. The waiter (the
// agent's tool pipeline goroutine) blocks on ch; the card action callback
// resolves it exactly once via resolve().
type feishuApprovalPending struct {
	id      string
	chatID  string
	command string
	cardID  string
	seq     int // next CardKit update sequence, guarded by mu

	mu      sync.Mutex
	decided bool
	ch      chan feishuApprovalDecision
}

func (p *feishuApprovalPending) resolve(d feishuApprovalDecision) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.decided {
		return false
	}
	p.decided = true
	// Buffered so the resolver never blocks even if the waiter already left.
	select {
	case p.ch <- d:
	default:
	}
	return true
}

func (p *feishuApprovalPending) nextSeq() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.seq++
	return p.seq
}

// RequestApproval implements channels.ApprovalCapable: sends an approve/deny
// card to the chat and blocks until the user clicks, ctx is canceled (turn
// abort), or the ctx deadline passes (timeout — denied). Fail-closed: any
// delivery failure denies the command.
func (c *FeishuChannel) RequestApproval(ctx context.Context, chatID, command string) (bool, string) {
	id := "apr_" + uuid.NewString()[:8]
	pending := &feishuApprovalPending{
		id:     id,
		chatID: chatID,
		// Truncate extremely long commands in the card; the model still holds
		// the full command and re-issues it after approval.
		command: truncateApprovalCommand(command),
		ch:      make(chan feishuApprovalDecision, 1),
	}
	if err := c.sendApprovalCard(ctx, chatID, pending); err != nil {
		return false, "approval card delivery failed: " + err.Error()
	}
	c.approvals.Store(id, pending)
	defer c.approvals.Delete(id)

	logger.InfoCF("feishu", "approval requested, waiting for decision", map[string]any{
		"chat_id":    chatID,
		"approval_id": id,
		"command":    pending.command,
	})

	select {
	case d := <-pending.ch:
		return d.approved, d.reason
	case <-ctx.Done():
		outcome := "请求已取消（回合中止），未执行"
		if ctx.Err() == context.DeadlineExceeded {
			outcome = "等待批准超时，未执行"
		}
		pending.resolve(feishuApprovalDecision{approved: false, reason: outcome})
		c.updateApprovalCardOutcome(pending, "⏱ "+outcome)
		return false, outcome
	}
}

// resolveApprovalButton handles an approve/deny button click arriving from
// handleCardAction. Returns the toast to show the clicker.
func (c *FeishuChannel) resolveApprovalButton(approved bool, id string) *feishuApprovalToast {
	v, ok := c.approvals.LoadAndDelete(id)
	if !ok {
		return &feishuApprovalToast{typ: "info", text: "该请求已处理或已过期"}
	}
	pending, ok := v.(*feishuApprovalPending)
	if !ok {
		return &feishuApprovalToast{typ: "info", text: "该请求已处理或已过期"}
	}
	decision := feishuApprovalDecision{approved: approved}
	if !approved {
		decision.reason = approvalDenyReason
	}
	if !pending.resolve(decision) {
		return &feishuApprovalToast{typ: "info", text: "该请求已处理"}
	}
	if approved {
		c.updateApprovalCardOutcome(pending, "✅ 已批准，命令将继续执行")
		logger.InfoCF("feishu", "approval granted", map[string]any{
			"chat_id":     pending.chatID,
			"approval_id": id,
			"command":     pending.command,
		})
		return &feishuApprovalToast{typ: "success", text: "已批准，命令将继续执行"}
	}
	c.updateApprovalCardOutcome(pending, "🛑 已拒绝，命令不会执行")
	logger.InfoCF("feishu", "approval denied", map[string]any{
		"chat_id":     pending.chatID,
		"approval_id": id,
		"command":     pending.command,
	})
	return &feishuApprovalToast{typ: "info", text: "已拒绝，命令不会执行"}
}

type feishuApprovalToast struct {
	typ  string
	text string
}

// sendApprovalCard creates the approval card and delivers it to the chat.
func (c *FeishuChannel) sendApprovalCard(ctx context.Context, chatID string, pending *feishuApprovalPending) error {
	card := buildFeishuApprovalCard(pending.command, pending.id)
	cardJSON, err := json.Marshal(card)
	if err != nil {
		return fmt.Errorf("marshal approval card: %w", err)
	}
	// Test seam: a channel without a client (unit tests) skips delivery; the
	// approval stays resolvable through the button-callback paths.
	if c.client == nil {
		return nil
	}
	createResp, err := c.client.Cardkit.V1.Card.Create(ctx,
		larkcardkit.NewCreateCardReqBuilder().
			Body(larkcardkit.NewCreateCardReqBodyBuilder().
				Type("card_json").Data(string(cardJSON)).Build()).
			Build())
	if err != nil || !createResp.Success() {
		code, msg := 0, ""
		if createResp != nil {
			code, msg = createResp.Code, createResp.Msg
		}
		return fmt.Errorf("cardkit create failed (code=%d msg=%s err=%v)", code, msg, err)
	}
	if createResp.Data == nil || createResp.Data.CardId == nil {
		return fmt.Errorf("cardkit create returned no card_id")
	}
	pending.cardID = *createResp.Data.CardId

	msgContent, _ := json.Marshal(map[string]any{
		"type": "card",
		"data": map[string]any{"card_id": pending.cardID},
	})
	if _, err := c.sendCard(ctx, chatID, string(msgContent)); err != nil {
		return fmt.Errorf("send approval card: %w", err)
	}
	return nil
}

// updateApprovalCardOutcome replaces the buttons with the final verdict so
// the card cannot be clicked again. Best effort: a failed update leaves a
// dead button whose click resolves to "已处理或已过期".
func (c *FeishuChannel) updateApprovalCardOutcome(pending *feishuApprovalPending, text string) {
	if c.client == nil || pending.cardID == "" {
		return
	}
	card := map[string]any{
		"schema": "2.0",
		"config": map[string]any{"update_multi": true},
		"body": map[string]any{"elements": []any{
			map[string]any{
				"tag":     "markdown",
				"content": fmt.Sprintf("🔐 **命令审批**\n%s\n\n%s", feishuInlineCodeBlock(pending.command), text),
			},
		}},
	}
	ctx, cancel := context.WithTimeout(context.Background(), feishuCallTimeout)
	defer cancel()
	if err := c.cardkitUpdateCard(ctx, pending.cardID, card, pending.nextSeq()); err != nil {
		logger.WarnCF("feishu", "approval card outcome update failed", map[string]any{
			"chat_id":     pending.chatID,
			"approval_id": pending.id,
			"error":       err.Error(),
		})
	}
}

// buildFeishuApprovalCard builds the interactive approval card. Schema V2
// buttons are standalone elements (the classic action wrapper is rejected,
// code 200861); the approval id is embedded because card callbacks carry no
// other context.
func buildFeishuApprovalCard(command, id string) map[string]any {
	return map[string]any{
		"schema": "2.0",
		"config": map[string]any{"update_multi": true},
		"body": map[string]any{"elements": []any{
			map[string]any{
				"tag":     "markdown",
				"content": "🔐 **命令审批请求**\n模型请求执行以下命令，请确认：\n" + feishuInlineCodeBlock(command),
			},
			map[string]any{
				"tag":  "button",
				"text": map[string]any{"tag": "plain_text", "content": "✅ 批准"},
				"type": "primary",
				"behaviors": []any{map[string]any{
					"type":  "callback",
					"value": map[string]any{"cmd": feishuApproveCmd, "id": id},
				}},
			},
			map[string]any{
				"tag":  "button",
				"text": map[string]any{"tag": "plain_text", "content": "🛑 拒绝"},
				"type": "danger",
				"behaviors": []any{map[string]any{
					"type":  "callback",
					"value": map[string]any{"cmd": feishuDenyCmd, "id": id},
				}},
			},
		}},
	}
}

// truncateApprovalCommand keeps the card readable: head + tail with an
// ellipsis marker for very long commands (rune-safe via cutOnRuneBoundary).
func truncateApprovalCommand(command string) string {
	command = strings.TrimSpace(command)
	const max = 800
	if len(command) <= max {
		return command
	}
	half := max / 2
	return cutOnRuneBoundary(command, half) + "\n…（已截断）…\n" + command[len(command)-half:]
}
