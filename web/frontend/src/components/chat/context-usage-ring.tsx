import { IconArrowRight } from "@tabler/icons-react"
import { useEffect, useRef, useState } from "react"
import { useTranslation } from "react-i18next"

import type { ContextUsage } from "@/store/chat"

interface ContextUsageRingProps {
  usage: ContextUsage
  onDetailClick?: () => void
}

function formatTokens(n: number): string {
  if (n >= 1000) return `${(n / 1000).toFixed(1)}k`
  return String(n)
}

/** 分段色（octop 借鉴 §五）：环与弹层共用同一套。 */
const SEGMENT_COLORS: Record<string, string> = {
  system_prompt: "bg-slate-400",
  skills: "bg-amber-400",
  tool_definitions: "bg-violet-400",
  mcp: "bg-fuchsia-400",
  subagents: "bg-blue-400",
  conversation: "bg-cyan-400",
}

const SEGMENT_LABELS: Record<string, string> = {
  system_prompt: "System",
  skills: "Skills",
  tool_definitions: "Tools",
  mcp: "MCP",
  subagents: "Subagents",
  conversation: "History",
}

function segmentColor(key: string): string {
  return SEGMENT_COLORS[key] ?? "bg-muted-foreground/50"
}

function segmentLabel(key: string): string {
  return SEGMENT_LABELS[key] ?? key
}

export function ContextUsageRing({
  usage,
  onDetailClick,
}: ContextUsageRingProps) {
  const { t } = useTranslation()
  const [intent, setIntent] = useState(false) // user wants open
  const [visible, setVisible] = useState(false) // DOM mounted
  const [animated, setAnimated] = useState(false) // CSS target state
  const [cooldown, setCooldown] = useState(false)
  const containerRef = useRef<HTMLDivElement>(null)
  const timerRef = useRef<ReturnType<typeof setTimeout>>(null)
  const hoverIntent = useRef<ReturnType<typeof setTimeout>>(null)
  const closeTimer = useRef<ReturnType<typeof setTimeout>>(null)

  useEffect(() => {
    if (intent) {
      // Mount first, animate in on next frame
      if (closeTimer.current) clearTimeout(closeTimer.current)
      setVisible(true)
      requestAnimationFrame(() => {
        requestAnimationFrame(() => setAnimated(true))
      })
    } else if (visible) {
      // Animate out, then unmount
      setAnimated(false)
      closeTimer.current = setTimeout(() => setVisible(false), 150)
    }
  }, [intent, visible])

  useEffect(() => {
    return () => {
      if (timerRef.current) clearTimeout(timerRef.current)
      if (hoverIntent.current) clearTimeout(hoverIntent.current)
      if (closeTimer.current) clearTimeout(closeTimer.current)
    }
  }, [])

  const percent = Math.min(usage.used_percent, 100)
  const radius = 8
  const circumference = 2 * Math.PI * radius
  const offset = circumference - (percent / 100) * circumference
  const barPercent = Math.min(percent, 100)

  const handleDetail = () => {
    if (cooldown || !onDetailClick) return
    setCooldown(true)
    onDetailClick()
    setIntent(false)
    timerRef.current = setTimeout(() => setCooldown(false), 1000)
  }

  // Desktop: hover to open, mouse leave to close (with small delay)
  const handleMouseEnter = () => {
    if (hoverIntent.current) clearTimeout(hoverIntent.current)
    setIntent(true)
  }

  const handleMouseLeave = () => {
    hoverIntent.current = setTimeout(() => setIntent(false), 150)
  }

  // Mobile: tap to toggle (preventDefault suppresses synthetic mouseenter)
  const handleTouchStart = (e: React.TouchEvent) => {
    e.preventDefault()
    setIntent((v) => !v)
  }

  return (
    <div
      ref={containerRef}
      className="relative"
      onMouseEnter={handleMouseEnter}
      onMouseLeave={handleMouseLeave}
    >
      <button
        type="button"
        onTouchStart={handleTouchStart}
        className="relative flex h-6 w-6 cursor-pointer items-center justify-center transition-opacity hover:opacity-70"
      >
        <svg className="h-6 w-6 -rotate-90" viewBox="0 0 20 20">
          <circle
            cx="10"
            cy="10"
            r={radius}
            fill="none"
            className="stroke-muted-foreground/30"
            strokeWidth="2"
          />
          <circle
            cx="10"
            cy="10"
            r={radius}
            fill="none"
            className="stroke-muted-foreground"
            strokeWidth="2"
            strokeLinecap="round"
            strokeDasharray={circumference}
            strokeDashoffset={offset}
          />
        </svg>
        <span className="text-muted-foreground absolute text-[8px] font-medium tabular-nums">
          {percent}
        </span>
      </button>

      {visible && (
        <div
          className={`bg-popover text-popover-foreground absolute right-0 bottom-full z-50 mb-3 w-[220px] rounded-xl border p-4 shadow-lg transition-all duration-150 ${
            animated
              ? "scale-100 opacity-100"
              : "pointer-events-none scale-95 opacity-0"
          }`}
        >
          <div className="bg-popover absolute right-3 -bottom-1.5 h-3 w-3 rotate-45 border-r border-b" />

          <div className="flex items-center justify-between">
            <span className="text-muted-foreground text-xs">
              {t("chat.contextTitle")}
            </span>
            <span className="text-xs font-medium">
              {formatTokens(usage.used_tokens)} /{" "}
              {formatTokens(usage.compress_at_tokens)}
            </span>
          </div>
          <div className="bg-muted mt-1.5 h-1.5 w-full overflow-hidden rounded-full">
            {usage.segments && usage.segments.length > 0 ? (
              // 分段堆叠条：一段一色，宽度按占 used 比例（octop 借鉴 §五）。
              usage.segments
                .filter((s) => s.tokens > 0)
                .map((s) => (
                  <div
                    key={s.key}
                    className={`${segmentColor(s.key)} h-full transition-all first:rounded-l-full last:rounded-r-full`}
                    style={{ width: `${Math.min(100, (s.tokens / Math.max(1, usage.compress_at_tokens)) * 100)}%` }}
                  />
                ))
            ) : (
              <div
                className="h-full rounded-full bg-orange-500 transition-all"
                style={{ width: `${barPercent}%` }}
              />
            )}
          </div>

          {usage.segments && usage.segments.some((s) => s.tokens > 0) && (
            <div className="mt-2 space-y-0.5">
              {usage.segments
                .filter((s) => s.tokens > 0)
                .map((s) => (
                  <div key={s.key} className="flex items-center justify-between text-[10px]">
                    <span className="text-muted-foreground flex items-center gap-1.5">
                      <span className={`inline-block h-1.5 w-1.5 rounded-full ${segmentColor(s.key)}`} />
                      {segmentLabel(s.key)}
                    </span>
                    <span className="tabular-nums">{formatTokens(s.tokens)}</span>
                  </div>
                ))}
            </div>
          )}

          <div className="mt-2 space-y-0.5">
            {usage.history_tokens != null && usage.history_tokens > 0 && (
              <div className="flex items-center justify-between text-[10px]">
                <span className="text-muted-foreground">History</span>
                <span className="tabular-nums">
                  {formatTokens(usage.history_tokens)}
                </span>
              </div>
            )}
            <div className="flex items-center justify-between text-[10px]">
              <span className="text-muted-foreground">{t("chat.contextCompressAt")}</span>
              <span className="tabular-nums">
                {formatTokens(usage.compress_at_tokens)}
              </span>
            </div>
            {usage.summarize_at_tokens != null && usage.summarize_at_tokens > 0 && (
              <div className="flex items-center justify-between text-[10px]">
                <span className="text-muted-foreground">{t("chat.contextSummarizeAt")}</span>
                <span className="tabular-nums">
                  {formatTokens(usage.summarize_at_tokens)}
                </span>
              </div>
            )}
          </div>

          <button
            type="button"
            onClick={handleDetail}
            disabled={cooldown}
            className="mt-3 inline-flex items-center gap-1 text-xs font-medium text-orange-600 transition-opacity hover:opacity-70 disabled:opacity-40 dark:text-amber-400"
          >
            {t("chat.contextDetail")}
            <IconArrowRight className="h-3 w-3" />
          </button>
        </div>
      )}
    </div>
  )
}
