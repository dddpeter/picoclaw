import type { AskQuestion } from "@/store/chat"

/** ask_user 步进卡纯逻辑（设计文档 ask-user-octop-redesign §3）：
 * 状态机的判定与消息拼装全部抽为纯函数，供 ask-card.tsx 与单测共用。 */

/** Sentinel choice that reveals the free-text input for a question（octop 同款）. */
export const OTHER = "__other__"

export const OPTION_KEYS = ["A", "B", "C", "D", "E", "F"] as const

/** What the user picked for one question. */
export interface AskAnswer {
  picked: string[]
  other: string
}

export function emptyAnswers(count: number): AskAnswer[] {
  return Array.from({ length: count }, () => ({ picked: [], other: "" }))
}

/**
 * Resolve one question's answer to display text, or `null` when unanswered.
 * "该题是否已答"的唯一真相源（评审修订，替代旧稿 isAnswered）：
 * 只点"其他…"未填文本 = 未答（picked.length > 0 不能作判据，
 * 否则会放行空答案行，后端渲染成 "(unanswered)"）。
 */
export function answerText(
  question: AskQuestion,
  answer: AskAnswer | undefined,
): string | null {
  if (!answer) return null
  const labels = answer.picked.filter((v) => v !== OTHER)
  const wantsOther = answer.picked.includes(OTHER) || !question.options?.length
  const other = answer.other.trim()
  if (wantsOther && other) labels.push(other)
  return labels.length ? labels.join("; ") : null
}

/**
 * 多题卡行内换行压平（评审 P1，§8-1）：后端 collectAskReply 对多题按
 * \n 切行、行 i = 题 i、超出行数折叠进末题——中间题的内嵌换行会让该题
 * 之后的所有答案错位。单题卡不压平（整条消息就是答案，换行是合法内容）。
 */
export function flattenLine(text: string): string {
  return text
    .split(/\r?\n/)
    .map((s) => s.trim())
    .filter(Boolean)
    .join("；")
}

/**
 * Build the message body to send: one line per question, in question order
 * （与 IM 逐行回复同协议）。任一题未答 → null（调用方不得发送）。
 */
export function buildSubmitBody(
  questions: AskQuestion[],
  answers: AskAnswer[],
): string | null {
  const resolved = questions.map((q, i) => answerText(q, answers[i]))
  if (resolved.some((text) => text === null)) return null
  const lines =
    questions.length > 1
      ? resolved.map((text) => flattenLine(text as string))
      : resolved
  return lines.join("\n")
}

export type AskFlowState = { mode: "step" | "review"; current: number }

/**
 * 单选自动跳题（§3）：普通选项选完即前进（末题进回顾）；"其他…"与
 * 多选不自动跳——"其他"要等自由文本填完，多选要手动"下一步"确认结束。
 * 返回 null = 保持原地。
 */
export function autoAdvance(
  index: number,
  total: number,
  multiSelect: boolean,
  changed: string,
): AskFlowState | null {
  if (multiSelect || changed === OTHER) return null
  if (index === total - 1) return { mode: "review", current: index }
  return { mode: "step", current: index + 1 }
}
