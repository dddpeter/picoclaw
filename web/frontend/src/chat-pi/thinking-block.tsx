// 移植自 pi-web-ui（MIT License）web/src/components/ThinkingBlock.tsx。
// SPDX-License-Identifier: MIT
// 适配点：i18n 由 pi-web-ui 的 useT() 换成 react-i18next 的 useTranslation()。
import { useState } from "react";
import { FiCheckCircle, FiChevronDown, FiChevronRight, FiCopy, FiCpu } from "react-icons/fi";
import { useTranslation } from "react-i18next";

/** 折叠预览纯函数：流式取实时尾巴，结束取开头一行。单测直引此处，禁止在测试里抄一份实现。 */
export function thinkingPreview(thinking: string, streaming?: boolean): string {
	return streaming ? thinking.trimEnd().slice(-80) : thinking.split("\n")[0].slice(0, 80);
}

interface ThinkingBlockProps {
	thinking: string;
	/** True while the assistant is still streaming this thinking block. */
	streaming?: boolean;
	/** 「完整显示思考」开关：true（开）→ 思考始终完整展开并自动换行
	 *  （流式推理过程也实时可见）；false（关）→ 折叠成一行摘要，流式中一行
	 *  实时显示最新文本。 */
	wrap?: boolean;
}

export function ThinkingBlock({ thinking, streaming, wrap = true }: ThinkingBlockProps) {
	const { t } = useTranslation();
	// null = 未手动点过 → 跟随开关：wrap=true（开）→ 完整展开；wrap=false（关）→ 折叠。
	const [open, setOpen] = useState<boolean | null>(null);
	const expanded = open ?? wrap;
	const shown = expanded;
	// 折叠预览：流式中取最新文本（实时尾巴），结束后取开头一行。
	const preview = thinkingPreview(thinking, streaming);
	const [copied, setCopied] = useState(false);
	const copyThinking = () => {
		void navigator.clipboard.writeText(thinking);
		setCopied(true);
		window.setTimeout(() => setCopied(false), 1200);
	};

	return (
		<div className={`thinking ${shown ? "open" : ""} ${streaming ? "live" : ""}`}>
			<div
				className="chead thinking-head"
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
					className="chead-toggle thinking-toggle"
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
				<span className="chead-icon thinking-icon">
					<FiCpu />
				</span>
				<span className="chead-title thinking-label">
					{streaming && shown ? (
						<span className="thinking-live-label">
							{t("piChat.thinkingNow")}
							<span className="dots" />
						</span>
					) : shown ? (
						t("piChat.thinking")
					) : (
						t("piChat.thinkingPreview", { preview })
					)}
				</span>
				<button
					type="button"
					className="chead-copy toolcall-copy thinking-copy"
					title={copied ? t("piChat.copied") : t("piChat.copyMessage")}
					aria-label={t("piChat.copyMessage")}
					onClick={(e) => {
						e.stopPropagation();
						copyThinking();
					}}
				>
					{copied ? <FiCheckCircle /> : <FiCopy />}
				</button>
			</div>
			{shown && <div className="thinking-body">{thinking}</div>}
		</div>
	);
}
