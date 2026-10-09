// 审批卡识别与封存（docs/design/2026-10-09-web-approval-card-design.md §3.1/§3.3）。
// 双通道：v1 协议 payload.approval（精确 tool/preview/timeout_ms）优先；
// v0 兜底匹配 publishAsk 的固定文本特征（旧网关 / 历史回放），超时解析
// 失败时降级为不显示倒计时。回执（✅/⛔ 前缀）与最近未封存审批卡配对，
// 配对成功则封存卡片并抑制回执文本本身。
import type { ApprovalInfo, ApprovalSeal, ChatMessage } from "@/store/chat"

const ASK_PREFIX = "⚠️ 需要批准：即将执行工具 "
const ASK_REPLY_PREFIX = "回复 /approve"

/** v1：pico payload 的 approval 字段 → ApprovalInfo。 */
export function parseApprovalFromPayload(
  payload: Record<string, unknown>,
): ApprovalInfo | undefined {
  const raw = payload.approval
  if (!raw || typeof raw !== "object") {
    return undefined
  }
  const obj = raw as Record<string, unknown>
  const tool = typeof obj.tool === "string" ? obj.tool.trim() : ""
  if (!tool) {
    return undefined
  }
  const timeoutMs = Number(obj.timeout_ms)
  const preview = typeof obj.preview === "string" ? obj.preview.trim() : ""
  return {
    tool,
    ...(preview ? { preview } : {}),
    ...(Number.isFinite(timeoutMs) && timeoutMs > 0 ? { timeoutMs } : {}),
  }
}

/** Go duration（5m0s / 30s / 1h0m0s）→ ms。 */
export function parseGoDuration(text: string): number | undefined {
  let ms = 0
  let matched = false
  const unitMs: Record<string, number> = { h: 3_600_000, m: 60_000, s: 1_000 }
  for (const match of text.matchAll(/(\d+(?:\.\d+)?)([hms])/g)) {
    matched = true
    ms += Number(match[1]) * (unitMs[match[2]] ?? 0)
  }
  return matched && ms > 0 ? Math.round(ms) : undefined
}

/** v0：publishAsk 兜底文本 → ApprovalInfo。 */
export function parseApprovalFromText(content: string): ApprovalInfo | undefined {
  const lines = content.split("\n")
  const first = (lines[0] ?? "").trim()
  if (!first.startsWith(ASK_PREFIX)) {
    return undefined
  }
  const tool = first.slice(ASK_PREFIX.length).trim()
  if (!tool) {
    return undefined
  }
  const replyIdx = lines.findIndex((line) => line.trim().startsWith(ASK_REPLY_PREFIX))
  const replyLine = replyIdx >= 0 ? lines[replyIdx].trim() : ""
  const preview =
    (replyIdx > 0 ? lines.slice(1, replyIdx) : lines.slice(1)).join("\n").trim()
  const timeoutMatch = /超过\s*([0-9hms.]+)\s*未回复/.exec(replyLine)
  const timeoutMs = timeoutMatch ? parseGoDuration(timeoutMatch[1]) : undefined
  return {
    tool,
    ...(preview ? { preview } : {}),
    ...(timeoutMs ? { timeoutMs } : {}),
  }
}

export type ApprovalReceiptVerdict = ApprovalSeal["verdict"]

/** 回执文本特征 → 结论；非回执返回 undefined（孤儿回执也返回 undefined，按普通文本展示）。 */
export function matchApprovalReceipt(content: string): ApprovalReceiptVerdict | undefined {
  const trimmed = content.trim()
  if (trimmed.startsWith("✅ 已批准；本会话内命中规则")) {
    return "always"
  }
  if (trimmed.startsWith("✅ 已批准，继续执行")) {
    return "approved"
  }
  if (trimmed.startsWith("⛔ 已拒绝本次工具执行")) {
    return "denied"
  }
  if (trimmed.startsWith("⛔ 审批超时")) {
    return "timeout"
  }
  return undefined
}

/** 把回执折进最近一张未封存审批卡（不可变更新）。命中返回新数组，否则 undefined。 */
export function sealLastApproval(
  messages: ChatMessage[],
  verdict: ApprovalReceiptVerdict,
  at: number,
): ChatMessage[] | undefined {
  for (let i = messages.length - 1; i >= 0; i--) {
    const message = messages[i]
    if (message.role === "assistant" && message.approval && !message.approvalSeal) {
      const copy = [...messages]
      copy[i] = { ...message, approvalSeal: { verdict, at } }
      return copy
    }
  }
  return undefined
}

/**
 * 历史回放配对：v0 文本识别审批消息 → 回执与最近的未封卡配对（抑制回执
 * 文本）→ 未配对的遗留审批卡封存为 done（无法回溯结论）。
 */
export function pairApprovalHistory(messages: ChatMessage[]): ChatMessage[] {
  const out: ChatMessage[] = []
  for (const message of messages) {
    if (message.role === "assistant") {
      const verdict = matchApprovalReceipt(message.content)
      if (verdict) {
        const sealed = sealLastApproval(out, verdict, typeof message.timestamp === "number" ? message.timestamp : 0)
        if (sealed) {
          out.length = 0
          out.push(...sealed)
          continue
        }
        // 未配对的回执（孤儿）按普通文本保留
      } else if (!message.approval) {
        const approval = parseApprovalFromText(message.content)
        if (approval) {
          out.push({ ...message, approval })
          continue
        }
      }
    }
    out.push(message)
  }
  return out.map((message) =>
    message.role === "assistant" && message.approval && !message.approvalSeal
      ? { ...message, approvalSeal: { verdict: "done" as const, at: 0 } }
      : message,
  )
}
