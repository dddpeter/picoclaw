import { useEffect, useState } from "react"
import { useTranslation } from "react-i18next"

interface TypingIndicatorProps {
  /** When the current turn started (ms epoch); defaults to mount time. */
  startedAt?: number
}

const STALL_WARN_SECONDS = 30

function formatElapsed(ms: number): string {
  const totalSeconds = Math.max(0, Math.floor(ms / 1000))
  const minutes = Math.floor(totalSeconds / 60)
  const seconds = totalSeconds % 60
  if (minutes <= 0) return `${seconds}s`
  return `${minutes}m${String(seconds).padStart(2, "0")}s`
}

export function TypingIndicator({ startedAt }: TypingIndicatorProps) {
  const { t } = useTranslation()
  const thinkingSteps = [
    t("chat.thinking.step1"),
    t("chat.thinking.step2"),
    t("chat.thinking.step3"),
    t("chat.thinking.step4"),
  ]
  const [stepIndex, setStepIndex] = useState(0)
  const [elapsed, setElapsed] = useState(0)
  // Stable fallback: a plain `startedAt ?? Date.now()` would re-evaluate on
  // every render, resetting the elapsed clock to ~0 each second tick.
  const [turnStartedAt] = useState(() => startedAt ?? Date.now())

  useEffect(() => {
    const stepsCount = thinkingSteps.length
    const interval = setInterval(() => {
      setStepIndex((prev) => (prev + 1) % stepsCount)
    }, 3000)
    return () => clearInterval(interval)
  }, [thinkingSteps.length])

  useEffect(() => {
    const tick = setInterval(() => {
      setElapsed(Date.now() - turnStartedAt)
    }, 1000)
    return () => clearInterval(tick)
  }, [turnStartedAt])

  const stalled = elapsed >= STALL_WARN_SECONDS * 1000
  const elapsedLabel = t("chat.thinking.elapsed", {
    defaultValue: "{{seconds}} elapsed",
    seconds: formatElapsed(elapsed),
  })
  const stalledLabel = t("chat.thinking.stalled", {
    defaultValue: "Still working — no new output for a while",
  })

  return (
    <div className="flex w-full flex-col gap-1.5">
      <div className="bg-card border-border/50 inline-flex w-fit max-w-xs flex-col gap-3 rounded-xl border px-5 py-4">
        <div className="flex items-center gap-1.5">
          <span className="size-2 animate-bounce rounded-full bg-orange-400/80 [animation-delay:-0.3s]" />
          <span className="size-2 animate-bounce rounded-full bg-orange-400/80 [animation-delay:-0.15s]" />
          <span className="size-2 animate-bounce rounded-full bg-orange-400/80" />
        </div>

        <div className="bg-muted relative h-1 w-36 overflow-hidden rounded-full">
          <div className="absolute inset-0 animate-[shimmer_2s_infinite] rounded-full bg-gradient-to-r from-orange-500/60 via-amber-400/90 to-orange-500/60 bg-[length:200%_100%]" />
        </div>

        <p
          key={stepIndex}
          className="text-muted-foreground animate-[fadeSlideIn_0.4s_ease-out] text-xs"
        >
          {thinkingSteps[stepIndex]}
        </p>

        <div className="flex flex-col gap-0.5">
          <p className="font-mono text-[11px] text-zinc-400 tabular-nums">
            {elapsedLabel}
          </p>
          {stalled && (
            <p className="text-[11px] text-amber-600 dark:text-amber-400">
              ⏳ {stalledLabel}
            </p>
          )}
        </div>
      </div>
    </div>
  )
}
