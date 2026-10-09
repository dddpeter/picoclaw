import { IconAlertTriangle, IconLoader2 } from "@tabler/icons-react"
import { useEffect, useState } from "react"
import { useTranslation } from "react-i18next"

import { sendChatMessage } from "@/features/chat/controller"
import { cn } from "@/lib/utils"
import type { ApprovalInfo, ApprovalSeal } from "@/store/chat"

/** 紧凑剩余时长：3m12s / 45s / 1h02m（与回合状态栏同格式）。 */
function formatRemaining(ms: number): string {
	const s = Math.max(0, Math.floor(ms / 1000))
	if (s < 60) return `${s}s`
	const m = Math.floor(s / 60)
	if (m < 60) return `${m}m${String(s % 60).padStart(2, "0")}s`
	const h = Math.floor(m / 60)
	return `${h}h${String(m % 60).padStart(2, "0")}m`
}

function sealLabel(
	t: (key: string) => string,
	verdict: ApprovalSeal["verdict"],
): { text: string; icon: string; tone: string } {
	switch (verdict) {
		case "approved":
			return { text: t("chat.approvalSealApproved"), icon: "✅", tone: "text-emerald-700 dark:text-emerald-400" }
		case "always":
			return { text: t("chat.approvalSealAlways"), icon: "✅", tone: "text-emerald-700 dark:text-emerald-400" }
		case "denied":
			return { text: t("chat.approvalSealDenied"), icon: "⛔", tone: "text-red-600 dark:text-red-400" }
		case "timeout":
			return { text: t("chat.approvalSealTimeout"), icon: "⛔", tone: "text-red-600 dark:text-red-400" }
		default:
			return { text: t("chat.approvalSealDone"), icon: "•", tone: "text-muted-foreground" }
	}
}

/**
 * 消息流内嵌审批卡（方案 A）。三态：pending（三按钮 + 倒计时）→
 * submitting（按钮置灰防连点）→ sealed（结论行 + 低视觉权重留痕）。
 * 操作复用 /approve 文字协议（与用户打字同路径），倒计时锚点是询问
 * 消息的服务器时间戳；v0 兜底无 timeout 时降级为不显示倒计时。
 */
export function ApprovalCard({
	info,
	seal,
	timestamp,
	latest = true,
}: {
	info: ApprovalInfo
	seal?: ApprovalSeal
	timestamp: number
	/** false = 存在更新的待审批问题，本卡按钮禁用（防批准对象错位）。 */
	latest?: boolean
}) {
	const { t } = useTranslation()
	const [submitting, setSubmitting] = useState<null | "approve" | "always" | "deny">(null)
	const [now, setNow] = useState(Date.now())

	const pending = seal === undefined
	const actionable = pending && latest
	const deadline = info.timeoutMs ? timestamp + info.timeoutMs : undefined

	useEffect(() => {
		if (!pending || !deadline) return
		const timer = setInterval(() => setNow(Date.now()), 1000)
		return () => clearInterval(timer)
	}, [pending, deadline])

	// submitting 恢复路径：回执丢失（断线窗口）或已被另一端答复却只收到
	// 不可配对的反馈时，卡片不能永久置灰——30s 后恢复可点，用户重试会
	// 得到孤儿回执并把卡封存为 done。
	useEffect(() => {
		if (!submitting) return
		const timer = setTimeout(() => setSubmitting(null), 30_000)
		return () => clearTimeout(timer)
	}, [submitting])

	const remaining = deadline ? Math.max(0, deadline - now) : undefined
	const urgent = remaining !== undefined && remaining <= 60_000
	const progress =
		remaining !== undefined && info.timeoutMs
			? Math.min(100, (remaining / info.timeoutMs) * 100)
			: undefined

	const send = (command: string, which: "approve" | "always" | "deny") => {
		if (submitting || !actionable) return
		if (!sendChatMessage({ content: command, attachments: [] })) {
			return // 发送失败保持可点（连接恢复后重试）
		}
		setSubmitting(which)
	}

	const sealInfo = seal ? sealLabel(t, seal.verdict) : undefined

	return (
		<div
			className={cn(
				"my-2 w-full overflow-hidden rounded-xl border shadow-sm transition-all",
				pending
					? "border-l-4 border-amber-500/50 bg-amber-500/[0.05]"
					: "border-border/60 border-l-4 border-l-border/60 opacity-75",
			)}
		>
			<div className="flex items-center gap-2 px-3.5 pt-3 pb-1">
				<IconAlertTriangle
					className={cn("size-4 shrink-0", pending ? "text-amber-500" : "text-muted-foreground")}
				/>
				<span className={cn("text-[13px] font-semibold", pending ? "text-foreground" : "text-muted-foreground")}>
					{t("chat.approvalTitle")}
				</span>
				<span className="bg-secondary text-secondary-foreground ml-auto rounded px-1.5 py-px font-mono text-[10px]">
					{info.tool}
				</span>
			</div>

			{info.preview && (
				<pre className="bg-muted/60 mx-3.5 mt-2 overflow-x-auto rounded-lg border p-2.5 font-mono text-[12px] leading-relaxed whitespace-pre-wrap">
					{info.preview}
				</pre>
			)}

			{pending && (
				<div className="mt-2.5 px-3.5">
					{remaining !== undefined && progress !== undefined ? (
						<div className="flex items-center gap-2.5">
							<div className="bg-muted-foreground/20 h-1 flex-1 overflow-hidden rounded-full">
								<div
									className={cn(
										"h-full rounded-full transition-[width] duration-1000 ease-linear",
										urgent ? "bg-red-500" : "bg-amber-500",
									)}
									style={{ width: `${progress}%` }}
								/>
							</div>
							<span
								className={cn(
									"w-12 shrink-0 text-right font-mono text-[11px] tabular-nums",
									urgent ? "font-semibold text-red-500" : "text-muted-foreground",
								)}
							>
								{formatRemaining(remaining)}
							</span>
						</div>
					) : (
						<div className="text-muted-foreground/80 text-[11px]">—</div>
					)}
					<div className="text-muted-foreground/70 mt-1 text-[11px]">
						{t("chat.approvalTimeoutNote")}
					</div>
				</div>
			)}

			{pending ? (
				<div className="flex flex-wrap items-center gap-2 px-3.5 pt-2.5 pb-3">
					<button
						type="button"
						disabled={submitting !== null || !actionable}
						onClick={() => send("/approve", "approve")}
						className="bg-primary text-primary-foreground hover:bg-primary/90 inline-flex h-8 cursor-pointer items-center gap-1.5 rounded-lg px-3.5 text-[13px] font-medium shadow-sm transition-colors disabled:cursor-not-allowed disabled:opacity-50"
					>
						{submitting === "approve" ? (
							<IconLoader2 className="size-3.5 animate-spin" />
						) : (
							<span aria-hidden>✓</span>
						)}
						{t("chat.approvalApprove")}
					</button>
					<button
						type="button"
						disabled={submitting !== null || !actionable}
						onClick={() => send("/approve always", "always")}
						className="border-border text-foreground hover:bg-accent inline-flex h-8 cursor-pointer items-center gap-1.5 rounded-lg border bg-transparent px-3.5 text-[13px] font-medium transition-colors disabled:cursor-not-allowed disabled:opacity-50"
					>
						{submitting === "always" ? (
							<IconLoader2 className="size-3.5 animate-spin" />
						) : (
							<span aria-hidden>⚡</span>
						)}
						{t("chat.approvalAlways")}
					</button>
					<button
						type="button"
						disabled={submitting !== null || !actionable}
						onClick={() => send("/deny", "deny")}
						className="text-red-600 dark:text-red-400 border-red-500/40 hover:bg-red-500/10 inline-flex h-8 cursor-pointer items-center gap-1.5 rounded-lg border bg-transparent px-3.5 text-[13px] font-medium transition-colors disabled:cursor-not-allowed disabled:opacity-50"
					>
						{submitting === "deny" ? (
							<IconLoader2 className="size-3.5 animate-spin" />
						) : (
							<span aria-hidden>✕</span>
						)}
						{t("chat.approvalDeny")}
					</button>
				</div>
			) : (
				sealInfo && (
					<div className="flex items-center gap-2 px-3.5 pt-2 pb-3">
						<span aria-hidden>{sealInfo.icon}</span>
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
