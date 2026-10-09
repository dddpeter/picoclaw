package feishu

import (
	"context"
	"crypto/rand"
	"encoding/hex"
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
// and the card's qid ride in the value because card.action.trigger carries no
// chat context — and, as observed in production, event.context may carry no
// message id either, so the qid is what locates the card to seal.
func feishuApprovalButtonElement(chatID, qid, cmd, label, btnType string) map[string]any {
	return map[string]any{
		"tag":  "button",
		"text": map[string]any{"tag": "plain_text", "content": label},
		"type": btnType,
		"behaviors": []any{map[string]any{
			"type":  "callback",
			"value": map[string]any{"cmd": cmd, "chat_id": chatID, "qid": qid},
		}},
	}
}

// buildFeishuApprovalCard renders the interactive approval prompt: header,
// the tool + preview body, and the three answer buttons as standalone
// elements. No streaming_mode in config — this card never streams, it only
// gets patched once (seal on click).
func buildFeishuApprovalCard(chatID, qid string, prompt channels.ApprovalPrompt) map[string]any {
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
			feishuApprovalButtonElement(chatID, qid, feishuApprovalApproveCmd, "✅ 批准", "primary"),
			feishuApprovalButtonElement(chatID, qid, feishuApprovalAlwaysCmd, "🔁 批准（本会话免问）", "default"),
			feishuApprovalButtonElement(chatID, qid, feishuApprovalDenyCmd, "⛔ 拒绝", "danger"),
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
// approval question as an interactive card. The card's message id is recorded
// under its qid so the click handler can seal exactly the card that was
// clicked (late clicks on expired cards included).
func (c *FeishuChannel) ShowApprovalPrompt(ctx context.Context, chatID string, prompt channels.ApprovalPrompt) error {
	qid := newFeishuApprovalQID()
	card, err := json.Marshal(buildFeishuApprovalCard(chatID, qid, prompt))
	if err != nil {
		return fmt.Errorf("feishu approval card build: %w", err)
	}
	messageID, err := c.sendCard(ctx, chatID, string(card))
	if err != nil {
		return err
	}
	c.recordApprovalCard(qid, chatID, messageID)
	logger.InfoCF("feishu", "Approval prompt card sent", map[string]any{
		"chat_id":    chatID,
		"session":    prompt.SessionKey,
		"tool":       prompt.Tool,
		"message_id": messageID,
		"qid":        qid,
	})
	return nil
}

// feishuApprovalCardTTL bounds how long a sent prompt card stays sealable via
// its qid: far beyond any approval timeout, but not forever.
const feishuApprovalCardTTL = 24 * time.Hour

// feishuApprovalCardRef is one recorded prompt card (see FeishuChannel.
// approvalCards).
type feishuApprovalCardRef struct {
	chatID    string
	messageID string
	at        time.Time
}

// newFeishuApprovalQID mints an opaque per-card id embedded in every button
// value; 16 random bytes are enough to be collision-free in practice.
func newFeishuApprovalQID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand never fails on supported platforms; fall back to a
		// time-derived id rather than not sealing.
		return fmt.Sprintf("q-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

// recordApprovalCard remembers qid -> card message id and sweeps entries past
// their TTL. Approvals are rare, so a full scan per record is cheap.
func (c *FeishuChannel) recordApprovalCard(qid, chatID, messageID string) {
	if qid == "" || messageID == "" {
		return
	}
	c.approvalCards.Store(qid, &feishuApprovalCardRef{chatID: chatID, messageID: messageID, at: time.Now()})
	c.approvalCards.Range(func(k, v any) bool {
		if ref, ok := v.(*feishuApprovalCardRef); ok && time.Since(ref.at) > feishuApprovalCardTTL {
			c.approvalCards.Delete(k)
		}
		return true
	})
}

// lookupApprovalCard resolves the card recorded for a qid. The entry is
// consumed: repeated clicks (double-click races) fall through to the callback
// context, and the response-card seal still covers them.
func (c *FeishuChannel) lookupApprovalCard(qid string) (chatID, messageID string) {
	if qid == "" {
		return "", ""
	}
	v, ok := c.approvalCards.LoadAndDelete(qid)
	if !ok {
		return "", ""
	}
	ref, ok := v.(*feishuApprovalCardRef)
	if !ok || time.Since(ref.at) > feishuApprovalCardTTL {
		return "", ""
	}
	return ref.chatID, ref.messageID
}

// sealApprovalCard best-effort patches the approval card into a verdict-only
// state (buttons removed) so a resolved question cannot be clicked again.
func (c *FeishuChannel) sealApprovalCard(ctx context.Context, chatID, messageID string, approved bool) {
	if messageID == "" {
		logger.DebugCF("feishu", "approval card seal skipped: no message id", map[string]any{
			"chat_id": chatID,
		})
		return
	}
	seal := c.sealCardFn
	if seal == nil {
		seal = c.sealApprovalCardAPI
	}
	if err := seal(ctx, chatID, messageID, approved); err != nil {
		logger.WarnCF("feishu", "Failed to seal approval card", map[string]any{
			"chat_id": chatID, "message_id": messageID, "error": err.Error(),
		})
		return
	}
	logger.InfoCF("feishu", "Approval card sealed", map[string]any{
		"chat_id": chatID, "message_id": messageID,
	})
}

// sealApprovalCardAPI patches the prompt card into its sealed shape over the
// message PATCH API.
func (c *FeishuChannel) sealApprovalCardAPI(ctx context.Context, chatID, messageID string, approved bool) error {
	if c.client == nil {
		return fmt.Errorf("feishu client unavailable")
	}
	card, err := json.Marshal(buildFeishuApprovalSealedCard(approved, ""))
	if err != nil {
		return fmt.Errorf("feishu approval seal card build: %w", err)
	}
	return patchCardContent(ctx, c, chatID, messageID, string(card))
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
