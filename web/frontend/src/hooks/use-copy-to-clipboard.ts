import { useEffect, useRef, useState } from "react"

import { copyText } from "@/lib/clipboard"

const DEFAULT_RESET_DELAY_MS = 2000

export function useCopyToClipboard(
  resetDelayMs: number = DEFAULT_RESET_DELAY_MS,
) {
  const [isCopied, setIsCopied] = useState(false)
  const resetTimerRef = useRef<number | null>(null)
  /** 卸载标志（评审 P1-2）：await 之后组件可能已卸载，此时不得再 setState。 */
  const mountedRef = useRef(true)

  useEffect(() => {
    mountedRef.current = true
    return () => {
      mountedRef.current = false
      if (resetTimerRef.current !== null) {
        window.clearTimeout(resetTimerRef.current)
      }
    }
  }, [])

  const markCopied = () => {
    if (!mountedRef.current) return

    if (resetTimerRef.current !== null) {
      window.clearTimeout(resetTimerRef.current)
    }

    setIsCopied(true)
    resetTimerRef.current = window.setTimeout(() => {
      if (!mountedRef.current) return
      setIsCopied(false)
      resetTimerRef.current = null
    }, resetDelayMs)
  }

  const copy = async (text: string) => {
    // copyText 契约上不 reject（库层已 try/catch）；此处再兜一层（评审 P1-1），
    // 防止实现漂移把 rejection 外泄给 `void copy(...).then(...)` 调用方
    // （它们没有 `.catch`，rejection 会变成 unhandled rejection）。
    const didCopy = await copyText(text).catch(() => false)
    if (didCopy) {
      markCopied()
    }
    return didCopy
  }

  return {
    copy,
    isCopied,
  }
}
