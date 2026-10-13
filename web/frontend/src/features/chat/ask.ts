// ask_user 问询卡（octop 借鉴 §三）：payload.ask → AskInfo，回执配对封存。
// 与审批卡（approval.ts）同构但完全独立——问询永不形成审批策略。
import type { AskInfo, AskOption, AskQuestion, AskSeal, ChatMessage } from "@/store/chat"

/** 问询回执前缀（pkg/agent/ask_user.go publishAskReceipt* 锁定）。 */
const ASK_RECEIVED_PREFIX = "📝 已收到回答"
const ASK_SKIPPED_PREFIX = "⏭️ 已跳过问询"
const ASK_TIMEOUT_PREFIX = "⏰ 问询超过"
/** 超时回执的第二特征段（Go 端 publishAskTimeoutReceipt 固定文案）：
 *  双特征匹配把误报面收窄到真实回执——普通聊天恰好以 "⏰ 问询超过"
 *  开头的概率已低，再要求包含完整处理语义则几乎为零（评审 P4）。 */
const ASK_TIMEOUT_CONFIRM = "未作答，按未作答处理"
/** 孤儿 /skip 的回执（consumeOrphanSkip）：封存幽灵卡而非落成普通文本。 */
const ASK_ORPHAN_PREFIX = "❓ 当前没有待回答的问题"

export type AskReceiptVerdict = AskSeal["verdict"]

/**
 * 原始题目 JSON（payload.ask.questions 或恢复 marker 的同构 JSON）→
 * AskQuestion[]：multi_select → multiSelect 归一、空 label 过滤、空选项降级。
 * 恢复路径复用同一归一（评审 P1-2：裸 as-cast 会把多选题退化成单选）。
 */
export function parseAskQuestions(rawQuestions: unknown): AskQuestion[] {
	if (!Array.isArray(rawQuestions)) return []
	return rawQuestions
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
}

/** v1：payload.ask 字段 → AskInfo。 */
export function parseAskFromPayload(
	payload: Record<string, unknown>,
): AskInfo | undefined {
	const raw = payload.ask
	if (!raw || typeof raw !== "object") return undefined
	const obj = raw as Record<string, unknown>
	const questions = parseAskQuestions(obj.questions)
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
  if (trimmed.startsWith(ASK_SKIPPED_PREFIX)) {
    return "skipped"
  }
  if (trimmed.startsWith(ASK_TIMEOUT_PREFIX) && trimmed.includes(ASK_TIMEOUT_CONFIRM)) {
  	// 超时软失败与 stale 恢复同档：低权重"已结束"，不渲染可点卡。
    return "done"
  }
  if (trimmed.startsWith(ASK_ORPHAN_PREFIX)) {
    // 孤儿 /skip（无 pending）：把残留的幽灵卡封掉（审批同语义）。
    return "done"
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
