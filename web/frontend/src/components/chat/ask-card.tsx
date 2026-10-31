import { IconHelpCircle, IconLoader2, IconPencil } from "@tabler/icons-react"
import { useEffect, useState } from "react"
import { useTranslation } from "react-i18next"
import TextareaAutosize from "react-textarea-autosize"

import {
  type AskAnswer,
  OPTION_KEYS,
  OTHER,
  answerText,
  autoAdvance,
  buildSubmitBody,
  emptyAnswers,
} from "@/features/chat/ask-flow"
import { sendChatMessage } from "@/features/chat/controller"
import { cn } from "@/lib/utils"
import type { AskInfo, AskQuestion, AskSeal } from "@/store/chat"

/**
 * ask_user 问询卡（octop 借鉴 §三，步进化改造，设计文档
 * ask-user-octop-redesign）：
 * - 步进式：一次只显示当前一题（单选选完自动前进），末题进"回顾"模式，
 *   逐题确认后一次提交；提交体 = 行 i = 题 i（与 IM 逐行回复同协议）。
 * - 纯逻辑（answerText / autoAdvance / buildSubmitBody / flattenLine）在
 *   @/features/chat/ask-flow，带单测。
 * - 保留机制：latest 防错位、submitting 30s 恢复、seal 三态。
 * - 完成态：details 摘要条；无本地答案（刷新 / IM 端作答）只显示问题（§2.4）。
 */
export function AskCard({
  info,
  seal,
  latest = false,
}: {
  info: AskInfo
  seal?: AskSeal
  /** false = 存在更新的未封存问询卡，本卡按钮禁用（防提交错位）。
   * 默认 false：buildTurns 只显式标记最近一张未封存卡。 */
  latest?: boolean
}) {
  const { t } = useTranslation()
  const total = info.questions.length
  const [submitting, setSubmitting] = useState(false)
  // 步进状态机（§3）：step 显示当前题，review 显示全部已答摘要。
  const [mode, setMode] = useState<"step" | "review">("step")
  const [current, setCurrent] = useState(0)
  const [answers, setAnswers] = useState<AskAnswer[]>(() => emptyAnswers(total))

  const pending = seal === undefined
  const actionable = pending && latest

  // 提交恢复路径：回执丢失（断线窗口）时 30s 后恢复可点，用户重试会得到
  // 孤儿回执封存（与审批卡同语义）。
  useEffect(() => {
    if (!submitting) return
    const timer = setTimeout(() => setSubmitting(false), 30_000)
    return () => clearTimeout(timer)
  }, [submitting])

  const send = (content: string) => {
    if (submitting || !actionable) return
    if (!sendChatMessage({ content, attachments: [] })) {
      return
    }
    setSubmitting(true)
  }

  const resolved = info.questions.map((q, i) => answerText(q, answers[i]))
  const allAnswered = resolved.every((text) => text !== null)
  const isLast = current === total - 1

  const setAnswerAt = (index: number, answer: AskAnswer, changed: string) => {
    if (!actionable) return
    setAnswers((prev) => prev.map((a, i) => (i === index ? answer : a)))
    // 单选选完普通选项即前进（末题进回顾）；"其他…"与多选等手动确认。
    const next = autoAdvance(
      index,
      total,
      Boolean(info.questions[index]?.multiSelect),
      changed,
    )
    if (next) {
      setMode(next.mode)
      setCurrent(next.current)
    }
  }

  const submit = () => {
    const body = buildSubmitBody(info.questions, answers)
    if (body === null) return
    send(body)
  }

  if (total === 0) return null

  // 完成态：details 摘要条（§2.3/§2.4）。答案只在组件 state（AskSeal 不含
  // 答案）；页面刷新或 IM 端作答后无数据，降级为只显示问题行。
  if (!pending) {
    const status =
      seal?.verdict === "answered"
        ? {
            text: t("chat.askAnsweredSummary", { count: total }),
            tone: "text-emerald-700 dark:text-emerald-400",
          }
        : seal?.verdict === "skipped"
          ? {
              text: t("chat.askDismissedSummary"),
              tone: "text-muted-foreground",
            }
          : { text: t("chat.askDoneSummary"), tone: "text-muted-foreground" }
    return (
      <details className="group/ask my-2 w-full">
        <summary className="border-border/60 bg-muted/30 flex cursor-pointer list-none items-center gap-2 rounded-lg border px-3 py-2 text-[13px] [&::-webkit-details-marker]:hidden">
          <span
            aria-hidden
            className="text-muted-foreground/70 transition-transform duration-150 group-open/ask:rotate-90"
          >
            ›
          </span>
          <span
            className={cn("min-w-0 flex-1 truncate font-medium", status.tone)}
          >
            {status.text}
          </span>
          <span className="text-muted-foreground/70 shrink-0 text-xs">
            {t("chat.askExpand")}
          </span>
        </summary>
        <div className="border-border/60 mt-1.5 ml-1 border-l-2 pl-3">
          {info.questions.map((q, i) => (
            <div key={i} className="py-1 text-[13px] leading-relaxed">
              {total > 1 && (
                <span className="text-muted-foreground mr-1 tabular-nums">
                  {i + 1}.
                </span>
              )}
              {q.header && (
                <span className="text-muted-foreground">【{q.header}】</span>
              )}
              {q.question}
              {resolved[i] !== null && (
                <span className="text-muted-foreground"> → {resolved[i]}</span>
              )}
            </div>
          ))}
        </div>
      </details>
    )
  }

  return (
    <div className="my-2 w-full overflow-hidden rounded-xl border border-l-4 border-sky-500/50 bg-sky-500/[0.05] shadow-sm">
      {/* 标题行：图标 + 标题 + 步进进度徽标（回顾时显示"回顾"） */}
      <div className="flex items-center gap-2 px-3.5 pt-3 pb-1">
        <IconHelpCircle className="size-4 shrink-0 text-sky-500" />
        <span className="text-[13px] font-semibold">{t("chat.askTitle")}</span>
        <span className="bg-secondary text-secondary-foreground ml-auto rounded-full border px-2 py-px text-[11px] tabular-nums">
          {mode === "review"
            ? t("chat.askReviewTag")
            : `${current + 1} / ${total}`}
        </span>
      </div>

      {/* 步骤区（§2.3：max-h min(420px,42vh) 内部滚动） */}
      <div className="max-h-[min(420px,42vh)] space-y-1 overflow-y-auto px-3.5 pt-1.5">
        {mode === "review" ? (
          info.questions.map((q, i) => (
            <ReviewRow
              key={i}
              question={q}
              index={i}
              answer={answers[i]}
              disabled={submitting || !actionable}
              onEdit={() => {
                setMode("step")
                setCurrent(i)
              }}
            />
          ))
        ) : (
          <QuestionRow
            question={info.questions[current]}
            index={current}
            answer={answers[current]}
            disabled={submitting || !actionable}
            onAnswer={(answer, changed) =>
              setAnswerAt(current, answer, changed)
            }
          />
        )}
      </div>

      {/* 动作区：顶部分隔线；回顾模式 = 提交/返回，步进模式 = 下一步/返回/你决定 */}
      <div className="border-border/60 mt-1.5 flex flex-wrap items-center gap-2 border-t px-3.5 pt-2.5 pb-3">
        {mode === "review" ? (
          <>
            <button
              type="button"
              disabled={submitting || !actionable || !allAnswered}
              onClick={submit}
              className="bg-primary text-primary-foreground hover:bg-primary/90 inline-flex h-7 cursor-pointer items-center gap-1.5 rounded-lg px-3 text-[12px] font-medium shadow-sm transition-colors disabled:cursor-not-allowed disabled:opacity-50"
            >
              {submitting ? (
                <IconLoader2 className="size-3.5 animate-spin" />
              ) : (
                <span aria-hidden>✓</span>
              )}
              {t("chat.askSubmit")}
            </button>
            <button
              type="button"
              disabled={submitting || !actionable}
              onClick={() => setMode("step")}
              className="text-muted-foreground hover:text-foreground hover:bg-accent border-border inline-flex h-7 cursor-pointer items-center rounded-lg border bg-transparent px-3 text-[12px] font-medium transition-colors disabled:cursor-not-allowed disabled:opacity-50"
            >
              {t("chat.askBack")}
            </button>
          </>
        ) : (
          <>
            <button
              type="button"
              disabled={submitting || !actionable || resolved[current] === null}
              onClick={() =>
                isLast ? setMode("review") : setCurrent(current + 1)
              }
              className="bg-primary text-primary-foreground hover:bg-primary/90 inline-flex h-7 cursor-pointer items-center gap-1.5 rounded-lg px-3 text-[12px] font-medium shadow-sm transition-colors disabled:cursor-not-allowed disabled:opacity-50"
            >
              {isLast ? t("chat.askReview") : t("chat.askNext")}
            </button>
            {current > 0 && (
              <button
                type="button"
                disabled={submitting || !actionable}
                onClick={() => setCurrent(current - 1)}
                className="text-muted-foreground hover:text-foreground hover:bg-accent border-border inline-flex h-7 cursor-pointer items-center rounded-lg border bg-transparent px-3 text-[12px] font-medium transition-colors disabled:cursor-not-allowed disabled:opacity-50"
              >
                {t("chat.askBack")}
              </button>
            )}
            {/* "你决定"：发语义化消息而非字面 /skip（§8-2，IM 通道 /skip 不受影响） */}
            <button
              type="button"
              disabled={submitting || !actionable}
              onClick={() => send(t("chat.askSkipMessage"))}
              className="text-muted-foreground hover:text-foreground hover:bg-accent ml-auto inline-flex h-7 cursor-pointer items-center rounded-lg border border-transparent bg-transparent px-2 text-[12px] transition-colors disabled:cursor-not-allowed disabled:opacity-50"
            >
              {t("chat.askYouDecide")}
            </button>
          </>
        )}
      </div>
    </div>
  )
}

/** 选项行左侧的字母 chip（A–F；选中变品牌色填充反白）。 */
function LetterChip({
  letter,
  selected,
}: {
  letter: string | undefined
  selected: boolean
}) {
  return (
    <span
      aria-hidden
      className={cn(
        "mt-px flex size-[18px] shrink-0 items-center justify-center rounded border text-[11px] font-semibold",
        selected
          ? "border-sky-500 bg-sky-500 text-white"
          : "border-border text-muted-foreground",
      )}
    >
      {letter}
    </span>
  )
}

/** 步进模式的当前题：全宽选项行（主文本 + 描述次行 + 字母 chip）+
 * 自动追加的"其他…"行 + 自由文本 TextArea。 */
function QuestionRow({
  question,
  index,
  answer,
  disabled,
  onAnswer,
}: {
  question: AskQuestion
  index: number
  answer: AskAnswer
  disabled: boolean
  onAnswer: (answer: AskAnswer, changed: string) => void
}) {
  const { t } = useTranslation()
  const options = question.options ?? []
  const openEnded = options.length === 0
  const hasOther = openEnded || answer.picked.includes(OTHER)

  const toggle = (label: string) => {
    if (disabled) return
    if (question.multiSelect) {
      const picked = answer.picked.includes(label)
        ? answer.picked.filter((v) => v !== label)
        : [...answer.picked, label]
      onAnswer({ ...answer, picked }, label)
    } else {
      onAnswer({ ...answer, picked: [label] }, label)
    }
  }

  const optionClasses = (selected: boolean) =>
    cn(
      // hover 边框用品牌色全值（对齐 Octop：选中项 hover 不变淡，特异度高于选中态）
      "enabled:hover:border-sky-500 enabled:hover:bg-accent enabled:hover:-translate-y-px enabled:hover:shadow-sm enabled:active:scale-[0.99]",
      "focus-visible:ring-sky-500/30 min-h-[42px] max-md:min-h-11 flex w-full items-start gap-2.5 rounded-lg border px-[11px] py-[9px] text-left transition-all outline-none focus-visible:ring-2",
      "disabled:cursor-not-allowed disabled:opacity-50",
      selected
        ? "border-sky-500 bg-sky-500/15"
        : "border-border bg-transparent",
    )

  return (
    <div className="pt-1 pb-2">
      <div className="mb-2 flex items-baseline gap-2">
        <span className="text-muted-foreground shrink-0 text-[13px] tabular-nums">
          {index + 1}.
        </span>
        <span className="text-[13px] leading-relaxed font-medium">
          {question.header && (
            <span className="text-muted-foreground font-normal">
              {question.header} ·{" "}
            </span>
          )}
          {question.question}
          {question.multiSelect && (
            <span className="text-muted-foreground/70 ml-1 text-[11px] font-normal">
              （多选）
            </span>
          )}
        </span>
      </div>
      {/* 选项区（§2.3：纵向全宽行；移动端去左缩进） */}
      <div className="flex flex-col gap-1.5 pl-5 max-md:pl-0">
        {options.map((opt, oi) => {
          const selected = answer.picked.includes(opt.label)
          return (
            <button
              key={opt.label}
              type="button"
              disabled={disabled}
              aria-pressed={selected}
              onClick={() => toggle(opt.label)}
              className={optionClasses(selected)}
            >
              <LetterChip
                letter={oi < OPTION_KEYS.length ? OPTION_KEYS[oi] : undefined}
                selected={selected}
              />
              <span className="flex min-w-0 flex-col">
                <span className="text-[13px] leading-snug font-medium">
                  {opt.label}
                </span>
                {opt.description && (
                  <span className="text-muted-foreground mt-0.5 text-xs leading-relaxed">
                    {opt.description}
                  </span>
                )}
              </span>
            </button>
          )
        })}
        {!openEnded && (
          <button
            type="button"
            disabled={disabled}
            aria-pressed={answer.picked.includes(OTHER)}
            onClick={() => toggle(OTHER)}
            className={optionClasses(answer.picked.includes(OTHER))}
          >
            <LetterChip
              letter={
                options.length < OPTION_KEYS.length
                  ? OPTION_KEYS[options.length]
                  : undefined
              }
              selected={answer.picked.includes(OTHER)}
            />
            <span className="flex min-w-0 flex-col">
              <span className="text-[13px] leading-snug font-medium">
                {t("chat.askOther")}
              </span>
            </span>
          </button>
        )}
        {hasOther && (
          <TextareaAutosize
            value={answer.other}
            minRows={1}
            maxRows={4}
            maxLength={2000}
            disabled={disabled}
            onChange={(e) =>
              onAnswer({ ...answer, other: e.target.value }, OTHER)
            }
            placeholder={t("chat.askTextPlaceholder")}
            className="border-border mt-1.5 block w-full max-w-lg rounded-lg border bg-transparent px-2.5 py-2 text-[13px] outline-none focus-visible:border-sky-500/60 focus-visible:ring-2 focus-visible:ring-sky-500/30 disabled:cursor-not-allowed disabled:opacity-50"
          />
        )}
      </div>
    </div>
  )
}

/** 回顾模式的已答摘要行：问题 + 答案一行，点击回到该题修改。 */
function ReviewRow({
  question,
  index,
  answer,
  disabled,
  onEdit,
}: {
  question: AskQuestion
  index: number
  answer: AskAnswer
  disabled: boolean
  onEdit: () => void
}) {
  const confirmed = answerText(question, answer)
  return (
    <button
      type="button"
      disabled={disabled}
      onClick={onEdit}
      className="group hover:bg-accent flex w-full items-start gap-2 rounded-lg px-2 py-1.5 text-left transition-colors disabled:cursor-not-allowed disabled:opacity-50 disabled:hover:bg-transparent"
    >
      <span className="text-muted-foreground shrink-0 pt-px text-[13px] tabular-nums">
        {index + 1}.
      </span>
      <span className="flex min-w-0 flex-1 flex-col">
        <span className="text-muted-foreground text-xs leading-relaxed">
          {question.header ? `${question.header} · ` : ""}
          {question.question}
        </span>
        <span className="text-[13px] leading-snug font-medium break-words">
          {confirmed ?? "—"}
        </span>
      </span>
      <IconPencil
        aria-hidden
        className="text-muted-foreground/50 mt-1 size-3.5 shrink-0 opacity-0 transition-opacity group-hover:opacity-100"
      />
    </button>
  )
}
