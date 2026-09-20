// 移植自 pi-web-ui（MIT License）web/src/components/Message.tsx 的消息卡结构。
// SPDX-License-Identifier: MIT
// 适配点：
//  - 输入由 pi 的 UiMessage（服务端已聚合 blocks）换成 picoclaw 的 PiTurn
//    （turns.ts 前端聚合）：一张用户卡 + 一张助手卡（blocks 分发）；
//  - 编辑重问 / 右键菜单 / 问题导航 / 折叠行 / 消息操作条等外围功能不移植
//    （设计文档 P0+P1 范围）；
//  - i18n 走 react-i18next（piChat 命名空间），文案取 pi-web-ui 原文。
import { memo, useState } from "react";
import { FiCheckCircle, FiCopy } from "react-icons/fi";
import { useTranslation } from "react-i18next";

import { Markdown } from "./markdown";
import { StreamMarkdown } from "./stream-markdown";
import { ThinkingBlock } from "./thinking-block";
import { ToolCallBlock, type PiToolView } from "./tool-call-block";
import type { PiBlock, PiTurn, PiUserPart } from "./turns";
import "./pi-chat.css";

/** pi-web-ui Message.tsx formatTime：本地时间 HH:MM。 */
function formatTime(ts: number): string {
	const d = new Date(ts);
	const hh = String(d.getHours()).padStart(2, "0");
	const mm = String(d.getMinutes()).padStart(2, "0");
	return `${hh}:${mm}`;
}

/** 单条 turn = 一张用户卡（可选）+ 一张助手卡（可选）。 */
export const PiTurnView = memo(function PiTurnView({ turn }: { turn: PiTurn }) {
	const { t } = useTranslation();
	return (
		<>
			{turn.user && <UserCard user={turn.user} label={t("piChat.roleUser")} />}
			{turn.assistant && <AssistantCard turn={turn} label={t("piChat.roleAssistant")} />}
		</>
	);
});

function UserCard({ user, label }: { user: PiUserPart; label: string }) {
	const { t } = useTranslation();
	return (
		<div className="msg msg-user" data-role="user">
			<div className="msg-meta">
				<span className="msg-role">{label}</span>
				{user.steering && <span className="msg-model">{t("piChat.steeringTag")}</span>}
				<span className="msg-time">{formatTime(user.timestamp)}</span>
			</div>
			<div className="msg-body">
				{user.text && (
					<TextBlock
						id={`${user.id}#text`}
						text={user.text}
						live={false}
						role="user"
					/>
				)}
				{(user.attachments ?? []).map((attachment, index) => (
					<AttachmentView
						key={`${user.id}#att${index}`}
						url={attachment.url}
						filename={attachment.filename}
						media={attachment.type === "image"}
					/>
				))}
			</div>
		</div>
	);
}

function AssistantCard({ turn, label }: { turn: PiTurn; label: string }) {
	const { t } = useTranslation();
	const assistant = turn.assistant;
	if (!assistant) return null;
	// 空流式占位：turn 活跃但一个可见块都没有（首 token 未到）——
	// pi-web-ui 的 isEmptyStreaming：thinking-wait 而非看不见的空气泡。
	const isEmptyStreaming = turn.active && assistant.blocks.length === 0;
	return (
		<div className="msg msg-assistant" data-role="assistant">
			<div className="msg-meta">
				<span className="msg-role">{label}</span>
				{assistant.modelName && (
					<span className="msg-model">{assistant.modelName}</span>
				)}
				<span className="msg-time">{formatTime(assistant.timestamp)}</span>
			</div>
			<div className="msg-body">
				{assistant.blocks.map((block) => (
					<PiBlockView key={block.id} block={block} active={turn.active} />
				))}
				{isEmptyStreaming && (
					<div className="thinking-wait">
						{t("piChat.thinkingWait")}
						<span className="dot" />
					</div>
				)}
				{turn.active && !isEmptyStreaming && <span className="stream-cursor" />}
			</div>
		</div>
	);
}

/** pi-web-ui Block 的 text 分支：用户气泡保留单个换行；助手流式走
 *  StreamMarkdown；单行消息复制键行内摆放（oneLiner）。 */
const TextBlock = memo(function TextBlock({
	text,
	live,
	role,
}: {
	id: string;
	text: string;
	live: boolean;
	role: "user" | "assistant";
}) {
	const { t } = useTranslation();
	const [copied, setCopied] = useState(false);
	const oneLiner = !text.includes("\n");
	const body =
		role === "user" ? (
			<Markdown text={text} hardBreaks />
		) : live ? (
			<StreamMarkdown text={text} />
		) : (
			<Markdown text={text} />
		);
	return (
		<div className={`msg-text${oneLiner ? " single" : ""}`}>
			{oneLiner ? <div className="msg-text-main">{body}</div> : body}
			<button
				type="button"
				className="msg-text-copy"
				title={copied ? t("piChat.copied") : t("piChat.copyMessage")}
				aria-label={t("piChat.copyMessage")}
				onClick={() => {
					void navigator.clipboard.writeText(text);
					setCopied(true);
					window.setTimeout(() => setCopied(false), 1200);
				}}
			>
				{copied ? <FiCheckCircle /> : <FiCopy />}
			</button>
		</div>
	);
});

function AttachmentView({
	url,
	filename,
	media,
}: {
	url: string;
	filename?: string;
	media: boolean;
}) {
	if (media) {
		return (
			<div className="msg-image">
				<img src={url} alt={filename ?? "attachment"} />
			</div>
		);
	}
	return (
		<a className="msg-attach-chip" href={url} target="_blank" rel="noreferrer">
			📎 {filename ?? url.split("/").pop() ?? "attachment"}
		</a>
	);
}

function PiBlockView({ block, active }: { block: PiBlock; active: boolean }) {
	switch (block.type) {
		case "text":
			return <TextBlock id={block.id} text={block.text} live={block.live && active} role="assistant" />;
		case "thinking":
			return (
				<ThinkingBlock thinking={block.thinking} streaming={block.live && active} />
			);
		case "toolCall": {
			const feedback = block.feedback;
			const view: PiToolView = {
				streaming: active,
				...(feedback
					? {
							resultText: feedback.text,
							resultIsError: feedback.isError,
							...(block.startedAt !== undefined &&
							feedback.endedAt !== undefined
								? { durationMs: feedback.endedAt - block.startedAt }
								: {}),
					  }
					: {}),
			};
			return (
				<ToolCallBlock
					name={block.name}
					argumentsText={block.argumentsText}
					view={view}
				/>
			);
		}
		case "image":
			return (
				<AttachmentView url={block.url} filename={block.filename} media={block.media} />
			);
		default:
			return null;
	}
}
