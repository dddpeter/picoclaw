import { useCallback, useState } from "react"
import { useTranslation } from "react-i18next"

import { type SessionSummary, deleteSession, getSessions } from "@/api/sessions"

const LIMIT = 20

interface UseSessionHistoryOptions {
  activeSessionId: string
  onDeletedActiveSession: () => void
}

export function useSessionHistory({
  activeSessionId,
  onDeletedActiveSession,
}: UseSessionHistoryOptions) {
  const { t } = useTranslation()
  const [sessions, setSessions] = useState<SessionSummary[]>([])
  const [offset, setOffset] = useState(0)
  const [hasMore, setHasMore] = useState(true)
  const [isLoadingMore, setIsLoadingMore] = useState(false)
  const [loadError, setLoadError] = useState(false)
  const [isLoading, setIsLoading] = useState(true)

  const loadSessions = useCallback(
    async (reset = true) => {
      try {
        const currentOffset = reset ? 0 : offset
        if (reset) {
          setLoadError(false)
          setHasMore(true)
          setOffset(0)
        }

        const data = await getSessions(currentOffset, LIMIT)
        setLoadError(false)

        if (data.length < LIMIT) {
          setHasMore(false)
        }

        if (reset) {
          setSessions(data)
        } else {
          setSessions((prev) => {
            const existingIds = new Set(prev.map((s) => s.id))
            const newItems = data.filter((s) => !existingIds.has(s.id))
            return [...prev, ...newItems]
          })
        }

        setOffset(currentOffset + data.length)
      } catch (err) {
        console.error("Failed to fetch session history:", err)
        setLoadError(true)
        if (!reset) {
          setHasMore(false)
        }
      } finally {
        setIsLoading(false)
        setIsLoadingMore(false)
      }
    },
    [offset],
  )

  // Merge-style refresh for polling/focus: fetch the first page and merge it
  // into the loaded list without collapsing pagination progress.
  const refreshSessions = useCallback(async () => {
    try {
      const data = await getSessions(0, LIMIT)
      setLoadError(false)
      setSessions((prev) => {
        const byId = new Map(prev.map((session) => [session.id, session]))
        for (const item of data) {
          byId.set(item.id, item)
        }
        return [...byId.values()].sort((a, b) => (a.updated < b.updated ? 1 : -1))
      })
    } catch (err) {
      console.error("Failed to refresh session history:", err)
    }
  }, [])

  const handleDeleteSession = useCallback(
    async (id: string, channel?: string) => {
      try {
        const deletedLoadedSession = sessions.some(
          (session) => session.id === id,
        )
        await deleteSession(id, channel)
        setSessions((prev) => prev.filter((s) => s.id !== id))
        if (deletedLoadedSession) {
          setOffset((prev) => Math.max(prev - 1, 0))
        }
        if (id === activeSessionId) {
          onDeletedActiveSession()
        }
      } catch (err) {
        console.error("Failed to delete session:", err)
      }
    },
    [activeSessionId, onDeletedActiveSession, sessions],
  )

  // M-3: the IntersectionObserver moved into SessionHistorySidebar — the
  // shared external ref broke when the sidebar rendered twice (desktop inline
  // + mobile drawer): the effect bound whichever sentinel was mounted first
  // and never rebound for the other instance.
  const loadMore = useCallback(() => {
    void loadSessions(false)
  }, [loadSessions])

  return {
    sessions,
    hasMore,
    isLoading,
    isLoadingMore,
    loadError,
    loadErrorMessage: t("chat.historyLoadFailed"),
    loadSessions,
    loadMore,
    refreshSessions,
    handleDeleteSession,
  }
}
