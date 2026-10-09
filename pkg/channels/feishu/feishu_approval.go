package feishu

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	larkcardkit "github.com/larksuite/oapi-sdk-go/v3/service/cardkit/v1"

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

// feishuApprovalButtonRow lays the three answer buttons out in one row of
// equal thirds (standalone buttons are block-level and stack vertically).
// Labels are kept near-equal in length so the thirds render evenly.
func feishuApprovalButtonRow(chatID, qid string) map[string]any {
	column := func(cmd, label, btnType string) map[string]any {
		return map[string]any{
			"tag":      "column",
			"width":    "weighted",
			"weight":   1,
			"elements": []any{feishuApprovalButtonElement(chatID, qid, cmd, label, btnType)},
		}
	}
	return map[string]any{
		"tag":       "column_set",
		"flex_mode": "none",
		"columns": []any{
			column(feishuApprovalApproveCmd, "✅ 批准", "primary"),
			column(feishuApprovalAlwaysCmd, "🔁 本会话", "default"),
			column(feishuApprovalDenyCmd, "⛔ 拒绝", "danger"),
		},
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

	// The card is born in streaming_mode: CardKit updates only propagate to
	// the delivered message view while the card is inside a streaming window
	// (a static card's PUT updates the entity but never reaches clients;
	// observed 2026-10-09). The seal closes the window with streaming_mode
	// false — the same lifecycle as the streaming card's final card.
	return map[string]any{
		"schema": "2.0",
		"config": map[string]any{
			"update_multi":   true,
			"streaming_mode": true,
			"locales":        []string{"zh_cn", "en_us"},
		},
		"header": map[string]any{
			"title":    map[string]any{"tag": "plain_text", "content": "⚠️ 工具审批"},
			"template": "yellow",
		},
		"body": map[string]any{"elements": []any{
			map[string]any{"tag": "markdown", "content": body.String()},
			feishuApprovalButtonRow(chatID, qid),
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
	// config mirrors the streaming card's final card: streaming_mode false
	// closes the streaming window this card was born with, which is what
	// makes the replacement propagate to clients.
	return map[string]any{
		"schema": "2.0",
		"config": map[string]any{
			"update_multi":   true,
			"streaming_mode": false,
			"locales":        []string{"zh_cn", "en_us"},
			"summary":        feishuCardSummary(content),
		},
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
// approval question as an interactive card. The card goes out through the
// CardKit create-and-reference flow (same as the streaming card) so its
// card_id is known channel-side and the click handler can replace the card
// wholesale — Im.Message.Patch on a schema-2.0 card only partially applies
// (observed 2026-10-09: header replaced, body survived). If the CardKit
// create fails the card is still sent inline (unsealable, but the question
// must reach the user).
func (c *FeishuChannel) ShowApprovalPrompt(ctx context.Context, chatID string, prompt channels.ApprovalPrompt) error {
	qid := newFeishuApprovalQID()
	cardJSON, err := json.Marshal(buildFeishuApprovalCard(chatID, qid, prompt))
	if err != nil {
		return fmt.Errorf("feishu approval card build: %w", err)
	}

	cardID, createErr := c.createCardkitCard(ctx, cardJSON)
	if createErr != nil {
		logger.WarnCF("feishu", "Approval card CardKit create failed; sending inline (card will not seal)", map[string]any{
			"chat_id": chatID,
			"error":   createErr.Error(),
		})
	}

	var content string
	if cardID != "" {
		msgContent, _ := json.Marshal(map[string]any{
			"type": "card",
			"data": map[string]any{"card_id": cardID},
		})
		content = string(msgContent)
	} else {
		content = string(cardJSON)
	}
	messageID, err := c.sendCard(ctx, chatID, content)
	if err != nil {
		return err
	}
	c.recordApprovalCard(qid, chatID, messageID, cardID)
	logger.InfoCF("feishu", "Approval prompt card sent", map[string]any{
		"chat_id":    chatID,
		"session":    prompt.SessionKey,
		"tool":       prompt.Tool,
		"message_id": messageID,
		"card_id":    cardID,
		"qid":        qid,
	})
	return nil
}

// createCardkitCard registers card JSON as a CardKit card entity and returns
// its card_id.
func (c *FeishuChannel) createCardkitCard(ctx context.Context, cardJSON []byte) (string, error) {
	if c.client == nil {
		return "", fmt.Errorf("feishu client unavailable")
	}
	req := larkcardkit.NewCreateCardReqBuilder().
		Body(larkcardkit.NewCreateCardReqBodyBuilder().
			Type("card_json").
			Data(string(cardJSON)).
			Build()).
		Build()
	resp, err := c.client.Cardkit.V1.Card.Create(ctx, req)
	if err != nil || !resp.Success() {
		code, msg := 0, ""
		if resp != nil {
			code, msg = resp.Code, resp.Msg
		}
		return "", fmt.Errorf("feishu approval cardkit create failed (code=%d msg=%s err=%v)", code, msg, err)
	}
	if resp.Data == nil || resp.Data.CardId == nil {
		return "", fmt.Errorf("feishu approval cardkit create returned no card_id")
	}
	return *resp.Data.CardId, nil
}

// feishuApprovalCardTTL bounds how long a sent prompt card stays sealable via
// its qid: far beyond any approval timeout, but not forever.
const feishuApprovalCardTTL = 24 * time.Hour

// feishuApprovalCardRef is one recorded prompt card (see FeishuChannel.
// approvalCards).
type feishuApprovalCardRef struct {
	chatID    string
	messageID string
	// cardID is the CardKit card entity behind the message; empty when the
	// prompt fell back to an inline send (unsealable).
	cardID string
	at     time.Time
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

// recordApprovalCard remembers qid -> card and sweeps entries past their TTL.
// Approvals are rare, so a full scan per record is cheap.
func (c *FeishuChannel) recordApprovalCard(qid, chatID, messageID, cardID string) {
	if qid == "" || messageID == "" {
		return
	}
	c.approvalCards.Store(qid, &feishuApprovalCardRef{
		chatID:    chatID,
		messageID: messageID,
		cardID:    cardID,
		at:        time.Now(),
	})
	c.approvalCards.Range(func(k, v any) bool {
		if ref, ok := v.(*feishuApprovalCardRef); ok && time.Since(ref.at) > feishuApprovalCardTTL {
			c.approvalCards.Delete(k)
		}
		return true
	})
}

// takeApprovalCardRef consumes the card recorded for a qid. Consumption makes
// repeated clicks (double-click races) fall through, and the first click is
// the one that seals.
func (c *FeishuChannel) takeApprovalCardRef(qid string) *feishuApprovalCardRef {
	if qid == "" {
		return nil
	}
	v, ok := c.approvalCards.LoadAndDelete(qid)
	if !ok {
		return nil
	}
	ref, ok := v.(*feishuApprovalCardRef)
	if !ok || time.Since(ref.at) > feishuApprovalCardTTL {
		return nil
	}
	return ref
}

// sealApprovalCard best-effort replaces the prompt card with its verdict-only
// shape (buttons removed) so a resolved question cannot be clicked again.
// The CardKit full-card update is the only path with whole-card replace
// semantics here; sequence is 1 (the first update issued for this card).
func (c *FeishuChannel) sealApprovalCard(ctx context.Context, ref *feishuApprovalCardRef, approved bool) {
	if ref == nil {
		return
	}
	if ref.cardID == "" {
		logger.DebugCF("feishu", "approval card seal skipped: inline card without card_id", map[string]any{
			"chat_id": ref.chatID, "message_id": ref.messageID,
		})
		return
	}
	sealed, err := json.Marshal(buildFeishuApprovalSealedCard(approved, ""))
	if err != nil {
		logger.WarnCF("feishu", "Failed to build sealed approval card", map[string]any{
			"error": err.Error(),
		})
		return
	}
	seal := c.sealCardFn
	if seal == nil {
		seal = c.cardkitUpdateCardJSON
	}
	if err := seal(ctx, ref.cardID, string(sealed), 1); err != nil {
		logger.WarnCF("feishu", "Failed to seal approval card", map[string]any{
			"chat_id": ref.chatID, "message_id": ref.messageID, "card_id": ref.cardID, "error": err.Error(),
		})
		return
	}
	logger.InfoCF("feishu", "Approval card sealed", map[string]any{
		"chat_id": ref.chatID, "message_id": ref.messageID, "card_id": ref.cardID,
	})
}

// The PATCH-based seal (Im.Message.Patch) was removed on 2026-10-09: it
// reports success on schema-2.0 cards but only partially applies — header
// replaced, body elements survived. Sealing goes through the CardKit card_id
// full-card update (sealApprovalCard) instead.
