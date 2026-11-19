// pi 渲染消息列表：扁平 ChatMessage[] → buildTurns → PiTurnView 逐卡渲染。
// 移植自 pi-web-ui（MIT License）web/src/components/MessageList.tsx 的消息区
// 骨架（列容器由 chat-page 提供，这里只负责 turns 映射与 memo）。
//
// 尾部窗口裁剪（F3）：超长会话的历史回放曾一次性挂载全部 turn 的 DOM
// （markdown/工具块/思考块），配合流式期间 reconcile 是长会话卡顿的另一
// 主源。这里只挂载最近 INITIAL_TURN_WINDOW 个 turn，更早的以顶部按钮按
// TURN_WINDOW_STEP 步进展开；窗口从尾部计——流式追加只发生在末 turn，
// 窗口永不漂移。展开时以首个可见 turn 为锚做 scroll 偏移补偿（DOM 在
// 视口上方插入，浏览器不自动保位）。
import { memo, useCallback, useLayoutEffect, useMemo, useRef, useState } from "react"
import { useTranslation } from "react-i18next"

import type { AssistantDetailVisibility } from "@/features/chat/detail-visibility"
import type { ChatMessage } from "@/store/chat"

import { PiTurnView } from "./pi-message"
import { type PiTurn, buildTurns } from "./turns"

const INITIAL_TURN_WINDOW = 50
const TURN_WINDOW_STEP = 50

interface PiMessageListProps {
  messages: ChatMessage[]
  /** chatAtom.isTyping：turn 是否仍在流式。 */
  isTyping: boolean
  /** 「思考与工具调用」展示级别（block 级过滤，见 turns.ts）。 */
  detail: AssistantDetailVisibility
}

const TurnItem = memo(function TurnItem({ turn }: { turn: PiTurn }) {
  return <PiTurnView turn={turn} />
})

export function PiMessageList({
  messages,
  isTyping,
  detail,
}: PiMessageListProps) {
  const { t } = useTranslation()
  const turns = useMemo(
    () => buildTurns(messages, { isTyping, detail }),
    [messages, isTyping, detail],
  )

  const [windowSize, setWindowSize] = useState(INITIAL_TURN_WINDOW)
  const sessionKeyRef = useRef("")
  const firstMessageId = messages[0]?.id ?? ""
  // 换会话（首消息 id 变化）重置窗口——渲染期调整派生 state 的官方模式。
  if (sessionKeyRef.current !== firstMessageId) {
    sessionKeyRef.current = firstMessageId
    if (windowSize !== INITIAL_TURN_WINDOW) {
      setWindowSize(INITIAL_TURN_WINDOW)
    }
  }

  const visibleFrom = Math.max(0, turns.length - windowSize)
  const hiddenCount = visibleFrom

  // 展开锚定：展开前记录首个可见 turn 的视口位置，展开后把 scroll 容器
  // 补偿同样的偏移，视口停留在原内容上而不是跳到新插入的 DOM。
  const firstVisibleRef = useRef<HTMLDivElement | null>(null)
  const anchorTop = useRef<number | null>(null)
  const expandWindow = useCallback(() => {
    anchorTop.current =
      firstVisibleRef.current?.getBoundingClientRect().top ?? null
    setWindowSize((size) => size + TURN_WINDOW_STEP)
  }, [])
  useLayoutEffect(() => {
    if (anchorTop.current === null) return
    const node = firstVisibleRef.current
    const scroller = node?.closest(".pi-chat")?.parentElement
    if (node && scroller) {
      scroller.scrollTop +=
        node.getBoundingClientRect().top - anchorTop.current
    }
    anchorTop.current = null
  })

  const visibleTurns = hiddenCount > 0 ? turns.slice(visibleFrom) : turns

  return (
    <>
      {hiddenCount > 0 && (
        <div className="mb-3 flex justify-center">
          <button
            type="button"
            data-expand-earlier=""
            onClick={expandWindow}
            className="text-muted-foreground hover:bg-muted hover:text-foreground rounded-full border border-border/60 px-3.5 py-1.5 text-xs transition-colors"
          >
            {t("piChat.showEarlierTurns", { count: hiddenCount })}
          </button>
        </div>
      )}
      {visibleTurns.map((turn, index) => (
        <div
          key={turn.id}
          data-turn-window=""
          ref={index === 0 ? firstVisibleRef : undefined}
        >
          <TurnItem turn={turn} />
        </div>
      ))}
    </>
  )
}
