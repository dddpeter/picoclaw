// pi 渲染消息列表：扁平 ChatMessage[] → buildTurns → PiTurnView 逐卡渲染。
// 移植自 pi-web-ui（MIT License）web/src/components/MessageList.tsx 的消息区
// 骨架（列容器由 chat-page 提供，这里只负责 turns 映射与 memo）。
import { memo, useMemo } from "react";

import { PiTurnView } from "./pi-message";
import { buildTurns, type PiTurn } from "./turns";
import type { AssistantDetailVisibility } from "@/features/chat/detail-visibility";
import type { ChatMessage } from "@/store/chat";

interface PiMessageListProps {
	messages: ChatMessage[];
	/** chatAtom.isTyping：turn 是否仍在流式。 */
	isTyping: boolean;
	/** 「思考与工具调用」展示级别（block 级过滤，见 turns.ts）。 */
	detail: AssistantDetailVisibility;
}

const TurnItem = memo(function TurnItem({ turn }: { turn: PiTurn }) {
	return <PiTurnView turn={turn} />;
});

export function PiMessageList({ messages, isTyping, detail }: PiMessageListProps) {
	const turns = useMemo(
		() => buildTurns(messages, { isTyping, detail }),
		[messages, isTyping, detail],
	);
	return (
		<>
			{turns.map((turn) => (
				<TurnItem key={turn.id} turn={turn} />
			))}
		</>
	);
}
