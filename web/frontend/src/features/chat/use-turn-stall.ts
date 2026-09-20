import { useEffect, useState } from "react"
import { useAtomValue } from "jotai"

import { chatAtom } from "@/store/chat"

const STALL_CHECK_INTERVAL_MS = 5_000

/**
 * 与后端分层：后端进度心跳（默认 3 分钟一拍）也算服务端活动；前端阈值须
 * 覆盖心跳间隔，只有"完全无事件"才视为疑似卡死。后端 stall watchdog
 * （默认 10 分钟）会尝试收尾/中止，前端提示只负责"看得见"。
 */
export const TURN_STALL_THRESHOLD_MS = 5 * 60 * 1000

export interface TurnStallState {
  /** 本轮已超过阈值无任何服务端事件，疑似卡死。 */
  stalled: boolean
  /** 距最后一次服务端活动（或轮次开始）的毫秒数；非生成中时 undefined。 */
  idleMs?: number
}

export function useTurnStall(): TurnStallState {
  const { isTyping, turnStartedAt, lastTurnActivityAt } =
    useAtomValue(chatAtom)
  const [now, setNow] = useState(() => Date.now())

  useEffect(() => {
    if (!isTyping) {
      return
    }
    const timer = setInterval(() => setNow(Date.now()), STALL_CHECK_INTERVAL_MS)
    return () => clearInterval(timer)
  }, [isTyping])

  if (!isTyping) {
    return { stalled: false }
  }
  const since = lastTurnActivityAt ?? turnStartedAt
  if (since === undefined) {
    return { stalled: false }
  }
  const idleMs = now - since
  return { stalled: idleMs >= TURN_STALL_THRESHOLD_MS, idleMs }
}
