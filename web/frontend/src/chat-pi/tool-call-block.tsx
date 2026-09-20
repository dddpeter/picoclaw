// 移植自 pi-web-ui（MIT License）web/src/components/ToolCallBlock.tsx。
// SPDX-License-Identifier: MIT
// 适配点（picoclaw 协议无服务端工具状态事件）：
//  - 状态机由 turn 聚合层（turns.ts）从消息时序推导：
//      running  = 无反馈且 turn 仍在流式；
//      waitingModel = 反馈已到（工具已结束）且 turn 仍在流式 —— 对应 pi-web-ui
//                    的 tool_status 阶段（表已出、模型还在消化），卡头显示耗时；
//      done     = 反馈已到且 turn 已结束；
//      idle     = turn 结束但没等到反馈（中断等异常）。
//  - 去掉 kill bash / liveOutput / delegate_task / present_files 分支。
import { memo, useState } from "react";
import {
	FiCheck,
	FiCheckCircle,
	FiChevronDown,
	FiChevronRight,
	FiClock,
	FiCopy,
	FiLoader,
	FiMinus,
	FiTerminal,
	FiX,
} from "react-icons/fi";
import { useTranslation } from "react-i18next";
import { shortenPath, toolArgHints } from "./tool-args";

/** turn 聚合层推导出的工具视图状态（对应 pi-web-ui 的 ToolView）。 */
export interface PiToolView {
	/** turn 是否仍在流式（决定 running / waitingModel / done）。 */
	streaming: boolean;
	/** 工具反馈（输出）文本；undefined = 尚无反馈。 */
	resultText?: string;
	/** 反馈是否判定为错误（启发式，见 turns.ts）。 */
	resultIsError?: boolean;
	/** 工具执行耗时（反馈时间戳 − 调用时间戳）。 */
	durationMs?: number;
}

const TOOL_ICONS: Record<string, string> = {
	bash: "$",
	read: "📄",
	write: "✍️",
	edit: "✏️",
	grep: "🔍",
	find: "🧭",
	ls: "📂",
};

function toolIcon(name: string): string {
	return TOOL_ICONS[name] ?? "🛠";
}

export const ToolCallBlock = memo(function ToolCallBlock({
	name,
	argumentsText,
	view,
	wrap = true,
}: {
	name: string;
	argumentsText?: string;
	view: PiToolView;
	/** 「完整显示工具」开关：true（开）→ 工具始终完整展开；false（关）→ 默认折叠。 */
	wrap?: boolean;
}) {
	const { t } = useTranslation();
	// null = 未手动点过 → 跟随开关：wrap=true（开）→ 全部展开；wrap=false（关）→ 全部折叠。
	const [open, setOpen] = useState<boolean | null>(null);
	const expanded = open ?? wrap;
	const shown = expanded;
	const [copied, setCopied] = useState(false);

	const resultLanded = view.resultText !== undefined;
	const running = !resultLanded && view.streaming;
	const done = resultLanded && !view.streaming;
	/** 工具已出结果、模型还在消化（对应 pi-web-ui 的 tool_status 阶段）。 */
	const waitingModel = resultLanded && view.streaming;
	const isError = view.resultIsError ?? false;

	const output = resultLanded ? (view.resultText ?? "") : "";

	const statusClass = isError ? "err" : done ? "ok" : running || waitingModel ? "run" : "idle";
	let statusLabel = isError
		? t("piChat.error")
		: done
			? t("piChat.done")
			: running
				? t("piChat.running")
				: waitingModel
					? t("piChat.toolDoneWaitingModel")
					: t("piChat.toolQueued");
	const duration = waitingModel && view.durationMs !== undefined ? formatDuration(view.durationMs) : "";
	if (waitingModel && duration) statusLabel = `${statusLabel} · ${duration}`;

	// 卡头右侧提示：任何工具都从参数里安全取路径/超时（AI 填错也只是不显示）；
	// bash 类的命令行给正文的终端行，折叠时卡头跟一小段预览。
	const hints = toolArgHints(argumentsText);
	const bashCommand = name === "bash" ? hints.command : undefined;
	// 折叠预览：只取首行（空白压成单空格），80 字截断；多行折成 +N 后缀。
	const collapsedCmd = !shown && bashCommand ? collapsedBashPreview(bashCommand) : undefined;

	const copyArgs = () => {
		if (argumentsText) {
			void navigator.clipboard.writeText(argumentsText);
			setCopied(true);
			setTimeout(() => setCopied(false), 1200);
		}
	};

	return (
		<div className={`toolcall ${statusClass}`}>
			<div
				className="chead toolcall-head"
				role="button"
				tabIndex={0}
				aria-expanded={shown}
				title={shown ? t("piChat.collapseMsg") : t("piChat.expandMsg")}
				onClick={() => setOpen(!expanded)}
				onKeyDown={(e) => {
					if (e.target !== e.currentTarget) return;
					if (e.key === "Enter" || e.key === " ") {
						e.preventDefault();
						setOpen(!expanded);
					}
				}}
			>
				<button
					type="button"
					className="chead-toggle toolcall-toggle"
					title={shown ? t("piChat.collapseMsg") : t("piChat.expandMsg")}
					aria-label={shown ? t("piChat.collapseMsg") : t("piChat.expandMsg")}
					aria-expanded={shown}
					onClick={(e) => {
						e.stopPropagation();
						setOpen(!expanded);
					}}
				>
					{shown ? <FiChevronDown /> : <FiChevronRight />}
				</button>
				<span className="chead-icon toolcall-icon">{toolIcon(name)}</span>
				<span className="chead-title toolcall-name">{name}</span>
				<span className="toolcall-status" title={statusLabel} aria-label={statusLabel}>
					{isError ? <FiX /> : done ? <FiCheck /> : running ? <FiLoader /> : waitingModel ? <FiClock /> : <FiMinus />}
				</span>
				{collapsedCmd && (
					<span className="toolcall-cmd" title={bashCommand}>
						$ {collapsedCmd}
					</span>
				)}
				{hints.path && (
					<span className="toolcall-path" title={hints.path}>
						{shortenPath(hints.path)}
					</span>
				)}
				{hints.timeout && <span className="toolcall-timeout">⏱ {hints.timeout}</span>}
				<span className="toolcall-spacer" />
				<button
					type="button"
					className="chead-copy toolcall-copy"
					title={t("piChat.copyArgs")}
					onClick={(e) => {
						e.stopPropagation();
						copyArgs();
					}}
				>
					{copied ? <FiCheckCircle /> : <FiCopy />}
				</button>
			</div>
			{shown && (
				<div className="toolcall-body">
					{argumentsText && (
						<div className="toolcall-args">
							{bashCommand ? <TerminalCommand command={bashCommand} /> : <pre>{argumentsText}</pre>}
						</div>
					)}
					{output.length > 0 && (
						<div className="toolcall-output">
							<div className="toolcall-output-label">
								{isError ? t("piChat.errorOutput") : t("piChat.output")}
								{(running || waitingModel) && <span className="cursor" />}
							</div>
							<pre>{output}</pre>
						</div>
					)}
					{running && output.length === 0 && (
						<div className="toolcall-waiting">
							<span className="cursor" /> {t("piChat.waitingOutput")}
						</div>
					)}
					{waitingModel && output.length === 0 && (
						<div className="toolcall-waiting">
							<span className="cursor" /> {t("piChat.waitingModel")}
						</div>
					)}
				</div>
			)}
		</div>
	);
});

/** Pretty-print a bash tool call's command line as a terminal row. */
function TerminalCommand({ command }: { command: string }) {
	return (
		<div className="termline">
			<FiTerminal className="termline-icon" />
			<code>{command}</code>
		</div>
	);
}

/** 折叠态 bash 命令预览：首行空白归一后取 80 字，多行追加 `+N` 后缀。
 *  输入脏（空串/全空白）返回 undefined——卡头不显示。 */
export function collapsedBashPreview(command: string): string | undefined {
	const lines = command.split("\n");
	const first = lines[0].replace(/\s+/g, " ").trim();
	if (!first) return undefined;
	const rest = lines.length - 1;
	const short = first.length > 80 ? `${first.slice(0, 80)}…` : first;
	return rest > 0 ? `${short} +${rest}` : short;
}

/** "0.3s" / "12.0s" / "1m 05s" — for the duration hint. */
function formatDuration(ms?: number): string {
	if (ms === undefined) return "";
	const totalSec = ms / 1000;
	if (totalSec < 60) return `${totalSec.toFixed(1)}s`;
	const m = Math.floor(totalSec / 60);
	const s = Math.round(totalSec % 60);
	return `${m}m ${String(s).padStart(2, "0")}s`;
}
