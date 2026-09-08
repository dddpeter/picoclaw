import { IconHistory, IconTrash } from "@tabler/icons-react"
import dayjs from "dayjs"
import { useEffect, useRef, type RefObject } from "react"
import { useTranslation } from "react-i18next"

import type { SessionSummary } from "@/api/sessions"
import { Button } from "@/components/ui/button"
import { ScrollArea } from "@/components/ui/scroll-area"
import { cn } from "@/lib/utils"

interface SessionHistorySidebarProps {
  sessions: SessionSummary[]
  activeSessionId: string
  hasMore: boolean
  isLoading: boolean
  loadError: boolean
  loadErrorMessage: string
  observerRef: RefObject<HTMLDivElement | null>
  onSwitchSession: (sessionId: string) => void
  onDeleteSession: (sessionId: string) => void
  onRefresh: () => void
}

export function SessionHistorySidebar({
  sessions,
  activeSessionId,
  hasMore,
  isLoading,
  loadError,
  loadErrorMessage,
  observerRef,
  onSwitchSession,
  onDeleteSession,
  onRefresh,
}: SessionHistorySidebarProps) {
  const { t } = useTranslation()
  const loadedRef = useRef(false)

  useEffect(() => {
    if (loadedRef.current) {
      return
    }
    loadedRef.current = true
    onRefresh()
  }, [onRefresh])

  return (
    <aside className="border-border/60 bg-gradient-to-b from-amber-500/[0.09] via-transparent to-teal-500/[0.05] flex w-64 shrink-0 flex-col border-r dark:from-amber-400/[0.10] dark:via-transparent dark:to-teal-400/[0.05]">
      <div className="flex items-center gap-2 px-4 pt-4 pb-3">
        <span className="bg-primary/10 text-primary flex h-7 w-7 items-center justify-center rounded-lg">
          <IconHistory className="size-4" />
        </span>
        <span className="text-foreground text-sm font-semibold tracking-wide">
          {t("chat.history")}
        </span>
        <span className="bg-secondary text-secondary-foreground ml-auto rounded-full px-2 py-0.5 text-[11px] font-medium tabular-nums">
          {sessions.length}
        </span>
      </div>

      <ScrollArea className="min-h-0 flex-1">
        <div className="flex flex-col gap-1 px-3 pb-4">
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
            <div className="text-muted-foreground rounded-lg px-3 py-6 text-center text-xs">
              {t("chat.loadingMore")}
            </div>
          )}

          {!loadError && !isLoading && sessions.length === 0 && (
            <div className="text-muted-foreground rounded-lg px-3 py-6 text-center text-xs">
              {t("chat.noHistory")}
            </div>
          )}

          {sessions.map((session) => {
            const active = session.id === activeSessionId

            return (
              <div
                key={session.id}
                role="button"
                tabIndex={0}
                aria-current={active ? "true" : undefined}
                onClick={() => onSwitchSession(session.id)}
                onKeyDown={(event) => {
                  if (event.key === "Enter" || event.key === " ") {
                    event.preventDefault()
                    onSwitchSession(session.id)
                  }
                }}
                className={cn(
                  "group relative cursor-pointer rounded-xl border px-3 py-2 transition-all select-none",
                  active
                    ? "border-primary/40 from-primary/20 to-primary/[0.05] bg-gradient-to-r shadow-sm"
                    : "hover:bg-accent border-border/40 hover:border-border",
                )}
              >
                <div className="flex items-center gap-2 pr-6">
                  <span
                    className={cn(
                      "h-1.5 w-1.5 shrink-0 rounded-full transition-colors",
                      active ? "bg-primary" : "bg-muted-foreground/40",
                    )}
                  />
                  <span
                    className={cn(
                      "line-clamp-1 text-[13px] font-medium",
                      active
                        ? "text-foreground"
                        : "text-foreground/85 group-hover:text-foreground",
                    )}
                  >
                    {session.title}
                  </span>
                </div>
                <div className="mt-1 flex items-center gap-1 pl-3.5 text-[11px]">
                  <span className="text-primary/90 font-semibold tabular-nums">
                    {t("chat.messagesCount", {
                      count: session.message_count,
                    })}
                  </span>
                  <span className="text-muted-foreground/50">·</span>
                  <span className="text-muted-foreground">{dayjs(session.updated).fromNow()}</span>
                </div>

                <Button
                  variant="ghost"
                  size="icon"
                  aria-label={t("chat.deleteSession")}
                  className="text-muted-foreground hover:bg-destructive/10 hover:text-destructive absolute top-1/2 right-1.5 h-6 w-6 -translate-y-1/2 opacity-0 transition-opacity group-hover:opacity-100 focus-visible:opacity-100"
                  onClick={(event) => {
                    event.stopPropagation()
                    onDeleteSession(session.id)
                  }}
                >
                  <IconTrash className="h-3.5 w-3.5" />
                </Button>
              </div>
            )
          })}

          {hasMore && sessions.length > 0 && (
            <div ref={observerRef} className="py-2 text-center">
              <span className="text-muted-foreground animate-pulse text-xs">
                {t("chat.loadingMore")}
              </span>
            </div>
          )}
        </div>
      </ScrollArea>
    </aside>
  )
}
