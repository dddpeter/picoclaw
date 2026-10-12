import { IconHelpCircle, IconLoader2 } from "@tabler/icons-react"
import { useEffect, useState } from "react"
import { useTranslation } from "react-i18next"

import { sendChatMessage } from "@/features/chat/controller"
import { cn } from "@/lib/utils"
import type { AskInfo, AskSeal } from "@/store/chat"

const OPTION_KEYS = ["A", "B", "C", "D", "E", "F"] as const

/**
 * ask_user 问询卡（octop 借鉴 §三）：agent 被用户决策卡住时弹出的结构化
 * 问题卡。交互模型（v1.6）：
 * - 多题 = 一卡全问一次提交（octop 语义）：选项只入草稿，自由文本题在卡上
 *   内联输入，全部作答后"提交作答"按行序统一发送；
 * - 单题 = 点击选项即发；自由文本题提示走输入框；
 * - 也可全程在输入框自由作答（多题按行序）；/skip 跳过。
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
	const [submitting, setSubmitting] = useState(false)
	// 草稿：question index -> 已选 labels（选项题）。
	const [draft, setDraft] = useState<Record<number, string[]>>({})
	// 自由文本题的内联作答（多题卡自包含：不逼用户去输入框拼行序）。
	const [textDraft, setTextDraft] = useState<Record<number, string>>({})

	const pending = seal === undefined
	const actionable = pending && latest
	const multiQ = info.questions.length > 1

	// 一题已答 = 选项题有选中，或自由文本题有非空内联输入。
	const questionAnswered = (qi: number, hasOptions: boolean): boolean => {
		if (hasOptions) return (draft[qi] ?? []).length > 0
		return Boolean((textDraft[qi] ?? "").trim())
	}
	const answeredCount = info.questions.filter((q, qi) =>
		questionAnswered(qi, Boolean(q.options && q.options.length > 0)),
	).length
	const allAnswered = multiQ && answeredCount === info.questions.length

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

	const toggle = (qi: number, label: string, multi: boolean | undefined) => {
		if (!actionable) return
		setDraft((prev) => {
			if (!multi) {
				return { ...prev, [qi]: [label] }
			}
			const cur = prev[qi] ?? []
			return {
				...prev,
				[qi]: cur.includes(label) ? cur.filter((v) => v !== label) : [...cur, label],
			}
		})
	}

	// 多题：选项只入草稿（修复 v1.5"点一题即发、后题封死"）；单题：即发。
	const onOptionClick = (qi: number, label: string, multi: boolean | undefined) => {
		if (multiQ) {
			toggle(qi, label, multi)
			return
		}
		send(label)
	}

	// 统一提交：行 i = 题 i（选项题 join "; "，自由文本题取内联输入）。
	const submitAll = () => {
		const lines = info.questions.map((q, qi) => {
			if (q.options && q.options.length > 0) {
				return (draft[qi] ?? []).join("; ")
			}
			return (textDraft[qi] ?? "").trim()
		})
		send(lines.join("\n"))
	}

	const sealInfo = seal
		? seal.verdict === "answered"
			? { text: t("chat.askSealAnswered"), tone: "text-emerald-700 dark:text-emerald-400" }
			: seal.verdict === "skipped"
				? { text: t("chat.askSealSkipped"), tone: "text-muted-foreground" }
				: { text: t("chat.askSealDone"), tone: "text-muted-foreground" }
		: undefined

	return (
		<div
			className={cn(
				"my-2 w-full overflow-hidden rounded-xl border shadow-sm",
				pending
					? "border-l-4 border-sky-500/50 bg-sky-500/[0.05]"
					: "border-border/60 border-l-4 border-l-border/60 opacity-75",
			)}
		>
			<div className="flex items-center gap-2 px-3.5 pt-3 pb-1">
				<IconHelpCircle
					className={cn("size-4 shrink-0", pending ? "text-sky-500" : "text-muted-foreground")}
				/>
				<span className={cn("text-[13px] font-semibold", pending ? "text-foreground" : "text-muted-foreground")}>
					{t("chat.askTitle")}
				</span>
				{multiQ && (
					<span className="bg-secondary text-secondary-foreground ml-auto rounded px-1.5 py-px font-mono text-[10px]">
						{info.questions.length} Q
					</span>
				)}
			</div>

			<div className="space-y-2.5 px-3.5 pt-1.5">
				{info.questions.map((q, qi) => {
					const picked = draft[qi] ?? []
					const hasOptions = Boolean(q.options && q.options.length > 0)
					return (
						<div key={qi}>
							<div className="text-[13px] leading-relaxed">
								{multiQ && <span className="text-muted-foreground mr-1">{qi + 1}.</span>}
								{q.header && <span className="text-muted-foreground">【{q.header}】</span>}
								{q.question}
								{q.multiSelect && <span className="text-muted-foreground/70 ml-1 text-[11px]">（多选）</span>}
							</div>
							{hasOptions ? (
								<div className="mt-1.5 flex flex-wrap gap-1.5">
									{(q.options ?? []).map((opt, oi) => {
										const selected = picked.includes(opt.label)
										return (
											<button
												key={opt.label}
												type="button"
												disabled={submitting || !actionable}
												title={opt.description}
												onClick={() => onOptionClick(qi, opt.label, q.multiSelect)}
												className={cn(
													"inline-flex h-7 cursor-pointer items-center gap-1 rounded-lg border px-2.5 text-[12px] font-medium transition-colors disabled:cursor-not-allowed disabled:opacity-50",
													selected
														? "border-sky-500/60 bg-sky-500/15 text-sky-700 dark:text-sky-300"
														: "border-border text-foreground hover:bg-accent bg-transparent",
												)}
											>
												{oi < OPTION_KEYS.length && (
													<span className="text-muted-foreground/70 font-mono text-[10px]">
														{OPTION_KEYS[oi]}
													</span>
												)}
												{opt.label}
											</button>
										)
									})}
								</div>
								) : multiQ ? (
									<div className="mt-1.5">
										<input
											type="text"
											value={textDraft[qi] ?? ""}
											disabled={submitting || !actionable}
											onChange={(e) =>
												setTextDraft((prev) => ({ ...prev, [qi]: e.target.value }))
											}
											onKeyDown={(e) => {
												// Enter 提交（对齐审批卡拒绝理由输入的先例）；输入法
												// 组合态的 Enter 不算。
												if (e.key === "Enter" && !e.nativeEvent.isComposing && allAnswered) {
													e.preventDefault()
													submitAll()
												}
											}}
											placeholder={t("chat.askTextPlaceholder")}
											maxLength={2000}
											className="border-border focus-visible:ring-sky-500/30 h-8 w-full max-w-md rounded-lg border bg-transparent px-2.5 text-[13px] outline-none focus-visible:ring-2 disabled:cursor-not-allowed disabled:opacity-50"
										/>
									</div>
								) : (
								<div className="text-muted-foreground/70 mt-1 text-[11px]">
									{t("chat.askFreeTextHint")}
								</div>
							)}
						</div>
					)
				})}
			</div>

			{pending ? (
				<div className="flex flex-wrap items-center gap-2 px-3.5 pt-2.5 pb-3">
					{multiQ && (
						<button
							type="button"
							disabled={submitting || !actionable || !allAnswered}
							onClick={submitAll}
							className="bg-primary text-primary-foreground hover:bg-primary/90 inline-flex h-7 cursor-pointer items-center gap-1.5 rounded-lg px-3 text-[12px] font-medium shadow-sm transition-colors disabled:cursor-not-allowed disabled:opacity-50"
						>
							{submitting ? <IconLoader2 className="size-3.5 animate-spin" /> : <span aria-hidden>✓</span>}
							{t("chat.askSubmit")}
						</button>
					)}
					<button
						type="button"
						disabled={submitting || !actionable}
						onClick={() => send("/skip")}
						className="text-muted-foreground hover:text-foreground hover:bg-accent inline-flex h-7 cursor-pointer items-center rounded-lg border border-transparent bg-transparent px-2 text-[12px] transition-colors disabled:cursor-not-allowed disabled:opacity-50"
					>
						{t("chat.askSkip")}
					</button>
					{multiQ ? (
						<span className="text-muted-foreground/70 text-[11px]">
							{t("chat.askDraftHint", { answered: answeredCount, total: info.questions.length })}
						</span>
					) : (
						<span className="text-muted-foreground/70 ml-auto text-[11px]">
							{t("chat.askInputHint")}
						</span>
					)}
				</div>
			) : (
				sealInfo && (
					<div className="flex items-center gap-2 px-3.5 pt-2 pb-3">
						<span className={cn("text-[13px] font-medium", sealInfo.tone)}>{sealInfo.text}</span>
						{seal && seal.at > 0 && (
							<span className="text-muted-foreground/60 ml-auto font-mono text-[11px] tabular-nums">
								{new Date(seal.at).toLocaleTimeString()}
							</span>
						)}
					</div>
				)
			)}
		</div>
	)
}
