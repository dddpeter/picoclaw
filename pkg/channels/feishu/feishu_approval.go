package feishu

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"

	"github.com/sipeed/picoclaw/pkg/channels"
	"github.com/sipeed/picoclaw/pkg/logger"
)

// 飞书原生审批卡（fork，agentscope-go borrowing §一 的交互升级）：审批
// 提问渲染为 CardKit v2 交互卡（三按钮），点击经 card.action.trigger（ws
// 长连接）回来，合成审批协议词（/approve /approve always /deny）走普通入
// 站管线——复用 tryHandleApprovalReply 的 steering 前路由、M1 同 chat 校验、
// 回执与会话规则注册，与停止按钮（§7，fc18f452）完全同构。
//
// schema 硬约束（§7 实测）：schema 2.0 拒绝旧 action 包装（错误码 200861），
// 按钮必须是独立 button 元素 + behaviors 回调；回调事件不含会话上下文，
// value 必须在渲染时嵌入 chat_id。

const (
	feishuApprovalApproveCmd = "approval_approve"
	feishuApprovalAlwaysCmd  = "approval_always"
	feishuApprovalDenyCmd    = "approval_deny"
)

// feishuApprovalSynthContent maps a button callback command to the approval
// protocol text synthesized into the inbound pipeline.
func feishuApprovalSynthContent(cmd string) (string, bool) {
	switch cmd {
	case feishuApprovalApproveCmd:
		return "/approve", true
	case feishuApprovalAlwaysCmd:
		return "/approve always", true
	case feishuApprovalDenyCmd:
		return "/deny", true
	}
	return "", false
}

// feishuApprovalButtonElement is one schema-2.0 callback button; the chat_id
// rides in the value because card.action.trigger carries no chat context.
func feishuApprovalButtonElement(chatID, cmd, label, btnType string) map[string]any {
	return map[string]any{
		"tag":  "button",
		"text": map[string]any{"tag": "plain_text", "content": label},
		"type": btnType,
		"behaviors": []any{map[string]any{
			"type":  "callback",
			"value": map[string]any{"cmd": cmd, "chat_id": chatID},
		}},
	}
}

// buildFeishuApprovalCard renders the interactive approval prompt: header,
// the tool + preview body, and the three answer buttons as standalone
// elements. No streaming_mode in config — this card never streams, it only
// gets patched once (seal on click).
func buildFeishuApprovalCard(chatID string, prompt channels.ApprovalPrompt) map[string]any {
	preview := strings.ReplaceAll(prompt.Preview, "`", "'")
	var body strings.Builder
	fmt.Fprintf(&body, "**工具**：%s\n", prompt.Tool)
	if preview != "" {
		fmt.Fprintf(&body, "**内容**：\n`%s`\n", preview)
	}
	timeout := prompt.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	fmt.Fprintf(&body, "\n超过 %s 未处理将按**拒绝**处理（fail-closed）。", timeout)

	return map[string]any{
		"schema": "2.0",
		"config": map[string]any{"update_multi": true},
		"header": map[string]any{
			"title":    map[string]any{"tag": "plain_text", "content": "⚠️ 工具审批"},
			"template": "yellow",
		},
		"body": map[string]any{"elements": []any{
			map[string]any{"tag": "markdown", "content": body.String()},
			feishuApprovalButtonElement(chatID, feishuApprovalApproveCmd, "✅ 批准", "primary"),
			feishuApprovalButtonElement(chatID, feishuApprovalAlwaysCmd, "🔁 批准（本会话免问）", "default"),
			feishuApprovalButtonElement(chatID, feishuApprovalDenyCmd, "⛔ 拒绝", "danger"),
		}},
	}
}

// buildFeishuApprovalSealedCard renders the verdict-only replacement card
// (no buttons) patched over the prompt after a click.
func buildFeishuApprovalSealedCard(approved bool, detail string) map[string]any {
	template := "default"
	verdict := "⏰ 审批已结束"
	switch {
	case approved:
		template = "success"
		verdict = "✅ 已批准，继续执行。"
	default:
		template = "danger"
		verdict = "⛔ 已拒绝。"
	}
	content := verdict
	if detail != "" {
		content += "\n" + detail
	}
	return map[string]any{
		"schema": "2.0",
		"config": map[string]any{"update_multi": true},
		"header": map[string]any{
			"title":    map[string]any{"tag": "plain_text", "content": "工具审批"},
			"template": template,
		},
		"body": map[string]any{"elements": []any{
			map[string]any{"tag": "markdown", "content": content},
		}},
	}
}

// ShowApprovalPrompt implements channels.ApprovalPromptCapable: render the
// approval question as an interactive card. The message id is logged for
// traceability; sealing happens from the click handler (the callback event
// carries the card's message id), so no channel-side state is kept.
func (c *FeishuChannel) ShowApprovalPrompt(ctx context.Context, chatID string, prompt channels.ApprovalPrompt) error {
	card, err := json.Marshal(buildFeishuApprovalCard(chatID, prompt))
	if err != nil {
		return fmt.Errorf("feishu approval card build: %w", err)
	}
	messageID, err := c.sendCard(ctx, chatID, string(card))
	if err != nil {
		return err
	}
	logger.InfoCF("feishu", "Approval prompt card sent", map[string]any{
		"chat_id":    chatID,
		"session":    prompt.SessionKey,
		"tool":       prompt.Tool,
		"message_id": messageID,
	})
	return nil
}

// sealApprovalCard best-effort patches the approval card into a verdict-only
// state (buttons removed) so a resolved question cannot be clicked again.
func (c *FeishuChannel) sealApprovalCard(ctx context.Context, chatID, messageID string, approved bool) {
	if messageID == "" || c.client == nil {
		return
	}
	card, err := json.Marshal(buildFeishuApprovalSealedCard(approved, ""))
	if err != nil {
		return
	}
	if err := patchCardContent(ctx, c, chatID, messageID, string(card)); err != nil {
		logger.WarnCF("feishu", "Failed to seal approval card", map[string]any{
			"chat_id": chatID, "message_id": messageID, "error": err.Error(),
		})
	}
}

// patchCardContent patches an interactive card message with raw card JSON
// (EditMessage wraps content in a markdown card — wrong shape here).
func patchCardContent(ctx context.Context, c *FeishuChannel, chatID, messageID, cardContent string) error {
	req := larkim.NewPatchMessageReqBuilder().
		MessageId(messageID).
		Body(larkim.NewPatchMessageReqBodyBuilder().Content(cardContent).Build()).
		Build()
	resp, err := c.client.Im.V1.Message.Patch(ctx, req)
	if err != nil {
		return fmt.Errorf("feishu approval seal: %w", err)
	}
	if !resp.Success() {
		c.invalidateTokenOnAuthError(resp.Code)
		return fmt.Errorf("feishu approval seal api error (code=%d msg=%s)", resp.Code, resp.Msg)
	}
	return nil
}
