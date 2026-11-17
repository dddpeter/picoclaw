import {
  IconHistory,
  IconLoader2,
  IconPencil,
  IconTrash,
} from "@tabler/icons-react"
import dayjs from "dayjs"
import { useEffect, useRef, useState } from "react"
import { useTranslation } from "react-i18next"

import type { SessionSummary } from "@/api/sessions"
import { LOBE_SCOPE_TOKENS } from "@/chat-pi/tokens"
import { Button } from "@/components/ui/button"
import { ScrollArea } from "@/components/ui/scroll-area"
import { cn } from "@/lib/utils"

interface SessionHistorySidebarProps {
  sessions: SessionSummary[]
  activeSessionId: string
  activeSessionBusy: boolean
  hasMore: boolean
  isLoading: boolean
  isLoadingMore: boolean
  loadError: boolean
  loadErrorMessage: string
  onLoadMore: () => void
  onSwitchSession: (sessionId: string, channel?: string) => void
  onDeleteSession: (sessionId: string, channel?: string) => void
  onRenameSession: (sessionId: string, title: string) => Promise<void>
  onRefresh: () => void
  onAutoRefresh: () => void
}

export function SessionHistorySidebar({
  sessions,
  activeSessionId,
  activeSessionBusy,
  hasMore,
  isLoading,
  isLoadingMore,
  loadError,
  loadErrorMessage,
  onLoadMore,
  onSwitchSession,
  onDeleteSession,
  onRenameSession,
  onRefresh,
  onAutoRefresh,
}: SessionHistorySidebarProps) {
  const { t } = useTranslation()
  const [renamingId, setRenamingId] = useState<string | null>(null)
  const [renameDraft, setRenameDraft] = useState("")
  const loadedRef = useRef(false)
  const onRefreshRef = useRef(onRefresh)
  const onAutoRefreshRef = useRef(onAutoRefresh)
  const sentinelRef = useRef<HTMLDivElement | null>(null)
  useEffect(() => {
    onRefreshRef.current = onRefresh
    onAutoRefreshRef.current = onAutoRefresh
  })

  useEffect(() => {
    if (loadedRef.current) {
      return
    }
    loadedRef.current = true
    onRefresh()
  }, [onRefresh])

  // The gateway writes session files for other channels (feishu, telegram,
  // ...) without any websocket event reaching this page, so poll and refresh
  // on focus to keep the list current without a manual reload. While any
  // listed session is mid-turn, poll faster so busy spinners settle promptly.
  const anyBusy = sessions.some((s) => s.busy)
  useEffect(() => {
    const refreshIfVisible = () => {
      if (document.visibilityState === "visible") {
        onAutoRefreshRef.current()
      }
    }
    const interval = window.setInterval(
      refreshIfVisible,
      anyBusy ? 5_000 : 30_000,
    )
    window.addEventListener("focus", refreshIfVisible)
    document.addEventListener("visibilitychange", refreshIfVisible)
    return () => {
      window.clearInterval(interval)
      window.removeEventListener("focus", refreshIfVisible)
      document.removeEventListener("visibilitychange", refreshIfVisible)
    }
  }, [anyBusy])

  // Infinite-scroll trigger. Owned per-instance (M-3): when this sidebar is
  // rendered twice (desktop inline + mobile drawer), each copy observes its
  // own sentinel instead of sharing one ref whose binding went stale.
  useEffect(() => {
    const el = sentinelRef.current
    if (!el || !hasMore || isLoadingMore || loadError) return

    const observer = new IntersectionObserver(
      (entries) => {
        if (
          entries[0].isIntersecting &&
          hasMore &&
          !isLoadingMore &&
          !loadError
        ) {
          onLoadMore()
        }
      },
      { threshold: 0.1 },
    )
    observer.observe(el)
    return () => observer.disconnect()
  }, [hasMore, isLoadingMore, loadError, onLoadMore])

  return (
    <aside
      className={cn(
        LOBE_SCOPE_TOKENS,
        "flex w-80 shrink-0 flex-col border-r border-[var(--lobe-border-2)] bg-[var(--lobe-bg-elevated)]",
      )}
    >
      <div className="flex items-center gap-2 px-3 pt-3 pb-2">
        <span className="flex size-7 items-center justify-center rounded-md bg-[var(--lobe-fill-2)] text-[var(--lobe-text-desc)]">
          <IconHistory className="size-3.5" />
        </span>
        <span className="text-[13px] font-semibold tracking-[0.01em] text-[var(--lobe-text)]">
          {t("chat.history")}
        </span>
        <span className="ml-auto rounded-full bg-[var(--lobe-fill-2)] px-2 py-0.5 text-[11px] font-medium text-[var(--lobe-text-desc)] tabular-nums">
          {sessions.length}
        </span>
      </div>

      <ScrollArea className="min-h-0 flex-1">
        <div className="flex flex-col gap-0.5 px-2 pb-3">
          {loadError && (
            <div className="text-destructive bg-destructive/10 flex flex-col gap-2 rounded-lg px-3 py-2 text-xs">
              <span>{loadErrorMessage}</span>
              <Button
                variant="ghost"
                size="sm"
                className="h-6 w-full text-xs"
                onClick={() => onRefresh()}
              >
                {t("common.retry")}
              </Button>
            </div>
          )}

          {isLoading && !loadError && sessions.length === 0 && (
            <div className="flex items-center justify-center gap-2 rounded-lg px-3 py-6 text-xs text-[var(--lobe-text-desc)]">
              <IconLoader2 className="size-3.5 animate-spin" />
              <span>{t("chat.loadingMore")}</span>
            </div>
          )}

          {!loadError && !isLoading && sessions.length === 0 && (
            <div className="rounded-lg px-3 py-6 text-center text-xs text-[var(--lobe-text-desc)]">
              {t("chat.noHistory")}
            </div>
          )}

          {sessions.map((session) => {
            const active = session.id === activeSessionId
            const busy = (active && activeSessionBusy) || session.busy === true

            return (
              <div
                key={session.id}
                role="button"
                tabIndex={0}
                aria-current={active ? "true" : undefined}
                data-session-item
                onClick={() => onSwitchSession(session.id, session.channel)}
                onKeyDown={(event) => {
                  if (event.key === "Enter" || event.key === " ") {
                    event.preventDefault()
                    onSwitchSession(session.id, session.channel)
                  }
                }}
                className={cn(
                  "group relative min-w-0 cursor-pointer rounded-lg px-2.5 py-2 transition-colors select-none",
                  // 三态可见性分级（2026-11-17 反馈）：原先选中用 fill-3(6%)、hover 用
                  // fill-2(9%)，选中底色反而比 hover 淡，暗色下几乎不可辨。
                  // 现改为：hover = 9% 底 + 淡色左条；选中 = 14% 底 + 主色左条。
                  active
                    ? "bg-[var(--lobe-fill)] text-[var(--lobe-text)] shadow-[inset_2px_0_0_0_var(--lobe-info)]"
                    : "text-[var(--lobe-text-2)] hover:bg-[var(--lobe-fill-2)] hover:text-[var(--lobe-text)] hover:shadow-[inset_2px_0_0_0_var(--lobe-border)]",
                )}
              >
                <div className="flex items-center gap-2 pr-6">
                  {busy ? (
                    <IconLoader2
                      className="size-4 shrink-0 animate-spin text-[var(--lobe-info)]"
                      aria-label={t("chat.turnRunning")}
                    />
                  ) : (
                    <span
                      className={cn(
                        "shrink-0 rounded-full transition-all",
                        active
                          ? "h-2 w-2 bg-[var(--lobe-info)]"
                          : "h-1.5 w-1.5 bg-[var(--lobe-text-disabled)]",
                      )}
                    />
                  )}
                  <span
                    className={cn(
                      "line-clamp-1 text-[13px] font-medium [overflow-wrap:anywhere]",
                      // 活跃（运行中）会话即使未被选中也提亮标题，配合转圈图标形成第二辨认通道。
                      active || busy
                        ? "text-[var(--lobe-text)]"
                        : "text-[var(--lobe-text-2)] group-hover:text-[var(--lobe-text)]",
                    )}
                  >
                    {session.title}
                  </span>
                </div>
                {session.preview &&
                  session.preview !== "(empty)" &&
                  session.preview !== session.title && (
                    <div className="mt-0.5 line-clamp-1 pl-3.5 text-[11px] [overflow-wrap:anywhere] text-[var(--lobe-text-desc)]">
                      {session.preview}
                    </div>
                  )}
                <div className="mt-1 flex items-center gap-1 pl-3.5 text-[11px]">
                  {session.channel && session.channel !== "pico" && (
                    <span className="rounded bg-[var(--lobe-fill-2)] px-1 py-px text-[10px] font-semibold tracking-wide text-[var(--lobe-text-desc)] uppercase">
                      {session.channel}
                    </span>
                  )}
                  <span className="font-medium text-[var(--lobe-text-2)] tabular-nums">
                    {t("chat.messagesCount", {
                      count: session.message_count,
                    })}
                  </span>
                  <span className="text-[var(--lobe-text-disabled)]">·</span>
                  <span className="text-[var(--lobe-text-desc)]">
                    {dayjs(session.updated).fromNow()}
                  </span>
                </div>

                {renamingId === session.id ? (
                  <input
                    autoFocus
                    className="absolute top-1/2 right-1.5 z-10 h-6 w-40 -translate-y-1/2 rounded border border-[var(--lobe-border)] bg-[var(--lobe-bg-elevated)] px-1.5 text-xs text-[var(--lobe-text)] outline-none focus:border-[var(--lobe-info)]"
                    value={renameDraft}
                    onClick={(event) => event.stopPropagation()}
                    onChange={(event) => setRenameDraft(event.target.value)}
                    onKeyDown={(event) => {
                      if (event.key === "Enter") {
                        const title = renameDraft.trim()
                        if (title) {
                          void onRenameSession(session.id, title)
                        }
                        setRenamingId(null)
                      } else if (event.key === "Escape") {
                        setRenamingId(null)
                      }
                    }}
                    onBlur={() => setRenamingId(null)}
                  />
                ) : (
                  <Button
                    variant="ghost"
                    size="icon"
                    aria-label={t("chat.renameSession")}
                    className="absolute top-1/2 right-8 h-6 w-6 -translate-y-1/2 text-[var(--lobe-text-desc)] opacity-0 transition-opacity group-hover:opacity-100 hover:bg-[var(--lobe-fill-3)] hover:text-[var(--lobe-text)] focus-visible:opacity-100"
                    onClick={(event) => {
                      event.stopPropagation()
                      setRenameDraft(session.title || "")
                      setRenamingId(session.id)
                    }}
                  >
                    <IconPencil className="h-3.5 w-3.5" />
                  </Button>
                )}
                <Button
                  variant="ghost"
                  size="icon"
                  aria-label={t("chat.deleteSession")}
                  className="hover:bg-destructive/10 hover:text-destructive absolute top-1/2 right-1.5 h-6 w-6 -translate-y-1/2 text-[var(--lobe-text-desc)] opacity-0 transition-opacity group-hover:opacity-100 focus-visible:opacity-100"
                  onClick={(event) => {
                    event.stopPropagation()
                    onDeleteSession(session.id, session.channel)
                  }}
                >
                  <IconTrash className="h-3.5 w-3.5" />
                </Button>
              </div>
            )
          })}

          {hasMore && sessions.length > 0 && (
            <div
              ref={sentinelRef}
              className="flex items-center justify-center gap-2 py-2 text-xs text-[var(--lobe-text-desc)]"
            >
              <IconLoader2 className="size-3.5 animate-spin" />
              <span>{t("chat.loadingMore")}</span>
            </div>
          )}
        </div>
      </ScrollArea>
    </aside>
  )
}
