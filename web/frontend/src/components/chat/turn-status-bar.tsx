import { useAtomValue } from "jotai"
import { useEffect, useState } from "react"
import { useTranslation } from "react-i18next"

import { cn } from "@/lib/utils"
import { chatAtom } from "@/store/chat"

/** 紧凑 token 数：8.1K / 40.4K / 1.2M（与飞书卡片 footer 同格式）。 */
function formatTokens(n: number): string {
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(1)}M`
  if (n >= 1_000) return `${(n / 1_000).toFixed(1)}K`
  return String(n)
}

/** 紧凑耗时：45s / 1m17s / 1h02m。 */
function formatDuration(ms: number): string {
  const s = Math.max(0, Math.floor(ms / 1000))
  if (s < 60) return `${s}s`
  const m = Math.floor(s / 60)
  if (m < 60) return `${m}m${String(s % 60).padStart(2, "0")}s`
  const h = Math.floor(m / 60)
  return `${h}h${String(m % 60).padStart(2, "0")}m`
}

function Sep() {
	return <span className="text-muted-foreground/35 select-none">·</span>
}

/**
 * 输入框上方的回合状态栏：状态/耗时/模型/API 次数/进出 token/上下文用量/历史
 * 偏移。生成中实时跳动耗时，回合结束冻结为末次快照；数据来自封板消息的
 * usage（input/output/llm_calls）与 context_usage 事件（后端 pico 渠道）。
 * 回合进行中不显示上一回合的 token/API（陈旧），只保留实时项。
 */
export function TurnStatusBar() {
	const { t } = useTranslation()
	const { isTyping, turnStartedAt, turnElapsedMs, turnStats, turnStatus, contextUsage } =
		useAtomValue(chatAtom)
	const [now, setNow] = useState(Date.now())

	useEffect(() => {
		if (!isTyping) return
		const timer = setInterval(() => setNow(Date.now()), 1000)
		return () => clearInterval(timer)
	}, [isTyping])

	const elapsed =
		isTyping && turnStartedAt !== undefined
			? now - turnStartedAt
			: turnElapsedMs
	// 首回合进行中、或已有任一已结算数据（耗时/用量）时显示。
	const visible = isTyping || turnStats !== undefined || turnElapsedMs !== undefined
	if (!visible) return null

	const status = isTyping ? "running" : (turnStatus ?? "done")
	const statusLabel =
		status === "running"
			? t("chat.turnRunning")
			: status === "error"
				? t("chat.turnFailed")
				: t("chat.turnDone")

	const showUsage = !isTyping && turnStats !== undefined
	const usage = showUsage ? turnStats : undefined

	const ctx = contextUsage
	const ctxPct =
		ctx && ctx.total_tokens > 0
			? Math.min(100, Math.round((ctx.used_tokens / ctx.total_tokens) * 100))
			: 0
	const ctxGaugeColor =
		ctxPct > 95 ? "bg-red-500" : ctxPct > 80 ? "bg-amber-500" : "bg-primary"

	return (
		<div
			role="status"
			aria-label={statusLabel}
			className="border-border/50 bg-muted/40 text-muted-foreground mb-1.5 inline-flex max-w-full items-center gap-2 self-end overflow-hidden rounded-full border px-3 py-[3px] text-[11px] tabular-nums shadow-sm backdrop-blur-sm"
		>
			{status === "running" ? (
				<span className="relative flex size-1.5 shrink-0">
					<span className="bg-primary absolute inline-flex h-full w-full animate-ping rounded-full opacity-60" />
					<span className="bg-primary relative inline-flex size-1.5 rounded-full" />
				</span>
			) : (
				<span
					className={cn(
						"size-1.5 shrink-0 rounded-full",
						status === "error" ? "bg-red-500" : "bg-emerald-500 dark:bg-emerald-400",
					)}
				/>
			)}
			<span
				className={cn(
					"shrink-0",
					status === "error" && "text-red-600 dark:text-red-400",
					status === "done" && "text-emerald-700 dark:text-emerald-400",
				)}
			>
				{statusLabel}
			</span>
			{elapsed !== undefined && (
				<>
					<Sep />
					<span className="shrink-0">⏱ {formatDuration(elapsed)}</span>
				</>
			)}
			{usage?.modelName && (
				<>
					<Sep />
					<span className="max-w-45 truncate font-mono text-[10.5px]">{usage.modelName}</span>
				</>
			)}
			{(usage?.llmCalls ?? 0) > 1 && (
				<>
					<Sep />
					<span className="shrink-0">API ×{usage?.llmCalls}</span>
				</>
			)}
			{usage && (usage.inputTokens > 0 || usage.outputTokens > 0) && (
				<>
					<Sep />
					<span className="shrink-0">
						↑ {formatTokens(usage.inputTokens)} ↓ {formatTokens(usage.outputTokens)}
					</span>
				</>
			)}
			{ctx && ctx.total_tokens > 0 && ctx.used_tokens > 0 && (
				<>
					<Sep />
					<span className="inline-flex shrink-0 items-center gap-1.5">
						<span className="bg-muted-foreground/20 h-1 w-10 overflow-hidden rounded-full">
							<span
								className={cn("h-full rounded-full transition-all duration-300", ctxGaugeColor)}
								style={{ width: `${ctxPct}%` }}
							/>
						</span>
						{formatTokens(ctx.used_tokens)}/{formatTokens(ctx.total_tokens)} ({ctxPct}%)
					</span>
					{(ctx.history_tokens ?? 0) > 0 && (
						<span className="shrink-0">↪ {formatTokens(ctx.history_tokens ?? 0)}</span>
					)}
				</>
			)}
		</div>
	)
}
