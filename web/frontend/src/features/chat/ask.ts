// ask_user 问询卡（octop 借鉴 §三）：payload.ask → AskInfo，回执配对封存。
// 与审批卡（approval.ts）同构但完全独立——问询永不形成审批策略。
import type { AskInfo, AskOption, AskQuestion, AskSeal, ChatMessage } from "@/store/chat"

/** 问询回执前缀（pkg/agent/ask_user.go publishAskReceipt 锁定）。 */
const ASK_RECEIVED_PREFIX = "📝 已收到回答"

export type AskReceiptVerdict = AskSeal["verdict"]

/** v1：payload.ask 字段 → AskInfo。 */
export function parseAskFromPayload(
  payload: Record<string, unknown>,
): AskInfo | undefined {
  const raw = payload.ask
  if (!raw || typeof raw !== "object") return undefined
  const obj = raw as Record<string, unknown>
  if (!Array.isArray(obj.questions)) return undefined
  const questions = obj.questions
    .map((q): AskQuestion | undefined => {
      if (!q || typeof q !== "object") return undefined
      const qo = q as Record<string, unknown>
      const question = typeof qo.question === "string" ? qo.question.trim() : ""
      if (!question) return undefined
      const options = Array.isArray(qo.options)
        ? qo.options
            .map((o): AskOption | undefined => {
              if (!o || typeof o !== "object") return undefined
              const oo = o as Record<string, unknown>
              const label = typeof oo.label === "string" ? oo.label.trim() : ""
              if (!label) return undefined
              return {
                label,
                ...(typeof oo.description === "string" && oo.description.trim()
                  ? { description: oo.description.trim() }
                  : {}),
              }
            })
            .filter((o): o is AskOption => o !== undefined)
        : []
      return {
        question,
        ...(typeof qo.header === "string" && qo.header.trim()
          ? { header: qo.header.trim() }
          : {}),
        ...(options.length > 0 ? { options } : {}),
        ...(qo.multi_select === true ? { multiSelect: true } : {}),
      }
    })
    .filter((q): q is AskQuestion => q !== undefined)
  if (questions.length === 0) return undefined
  const timeoutMs = Number(obj.timeout_ms)
  return {
    questions,
    ...(Number.isFinite(timeoutMs) && timeoutMs > 0 ? { timeoutMs } : {}),
  }
}

/** 回执文本 → 结论；非回执返回 undefined。 */
export function matchAskReceipt(content: string): AskReceiptVerdict | undefined {
  const trimmed = content.trim()
  if (trimmed.startsWith(ASK_RECEIVED_PREFIX)) {
    return "answered"
  }
  return undefined
}

/** 把回执折进最近一张未封 ask 卡（不可变更新）。命中返回新数组。 */
export function sealLastAsk(
  messages: ChatMessage[],
  verdict: AskReceiptVerdict,
  at: number,
): ChatMessage[] | undefined {
  for (let i = messages.length - 1; i >= 0; i--) {
    const message = messages[i]
    if (message.role === "assistant" && message.ask && !message.askSeal) {
      const copy = [...messages]
      copy[i] = { ...message, askSeal: { verdict, at } }
      return copy
    }
  }
  return undefined
}
