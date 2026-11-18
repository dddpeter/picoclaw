import { useCallback, useEffect, useRef, useState } from "react"
import { useTranslation } from "react-i18next"
import { toast } from "sonner"

import { getSessionModel, setSessionModel } from "@/api/session-model"

interface UseSessionModelOptions {
  /** The active chat session (the pico session UUID). */
  activeSessionId: string
  /** Whether the gateway is reachable (the endpoint needs a running gateway). */
  isConnected: boolean
}

/**
 * Session-scoped model (fork: session-scoped model selection).
 *
 * The model chosen here applies to THIS conversation only: the config file
 * keeps its own default model, and the config's default-model setting is
 * untouched. Switching conversations reloads the bound model.
 */
export function useSessionModel({
  activeSessionId,
  isConnected,
}: UseSessionModelOptions) {
  const { t } = useTranslation()
  // "" = the session follows the configured default model.
  const [sessionModel, setSessionModelState] = useState("")
  // The session model's context window (0 = unknown / follow the config
  // default's window).
  const [sessionContextWindow, setSessionContextWindow] = useState(0)
  const [isSetting, setIsSetting] = useState(false)
  const loadedSessionRef = useRef<string>("")
  // The session the async callbacks belong to. The state is shared across
  // sessions (one hook instance serves the selector), so a response that
  // settles after the user switched conversations must NOT be applied —
  // otherwise session B's selector would flash session A's model.
  const activeSessionRef = useRef(activeSessionId)
  useEffect(() => {
    activeSessionRef.current = activeSessionId
  }, [activeSessionId])

  const applyResponse = useCallback(
    (response: { model: string; context_window?: number }) => {
      setSessionModelState(response.model ?? "")
      setSessionContextWindow(response.context_window ?? 0)
    },
    [],
  )

  // Load the session's bound model whenever the conversation changes. A
  // failed load leaves the session on the configured default — the selector
  // then shows the config default, which is honest.
  useEffect(() => {
    if (!isConnected || !activeSessionId) {
      // Dropping the loaded marker on disconnect makes the reconnect refetch:
      // a gateway restart wipes the in-memory overrides, so the stale value
      // from before the restart would otherwise stick forever.
      loadedSessionRef.current = ""
      return
    }
    if (loadedSessionRef.current === activeSessionId) {
      return
    }
    loadedSessionRef.current = activeSessionId

    let cancelled = false
    void getSessionModel(activeSessionId)
      .then((response) => {
        if (!cancelled) {
          applyResponse(response)
        }
      })
      .catch((err) => {
        console.error("Failed to load session model:", err)
        if (!cancelled) {
          applyResponse({ model: "" })
        }
      })

    return () => {
      cancelled = true
    }
  }, [activeSessionId, applyResponse, isConnected])

  const handleSetSessionModel = useCallback(
    async (modelName: string) => {
      if (!activeSessionId) {
        return
      }
      // Pin the session this write belongs to. If the user switches
      // conversations while the request is in flight, neither the response
      // nor the rollback may touch the (already reloaded) other session's
      // display.
      const targetSession = activeSessionId
      const previous = sessionModel
      setIsSetting(true)
      // Optimistic: the selector shows the new model immediately; a failed
      // request rolls it back.
      setSessionModelState(modelName)
      try {
        const response = await setSessionModel(targetSession, modelName)
        if (activeSessionRef.current === targetSession) {
          applyResponse(response)
        }
      } catch (err) {
        console.error("Failed to set session model:", err)
        if (activeSessionRef.current === targetSession) {
          setSessionModelState(previous)
          toast.error(
            err instanceof Error
              ? err.message
              : t("chat.sessionModelSetFailed"),
          )
        }
      } finally {
        // Only the write's own session clears the in-flight flag — an
        // overlapping write on the session the user has switched to must not
        // be marked done by this one settling first.
        if (activeSessionRef.current === targetSession) {
          setIsSetting(false)
        }
      }
    },
    [activeSessionId, applyResponse, sessionModel, t],
  )

  return {
    /** The model bound to the active session ("" = configured default). */
    sessionModel,
    /** The session model's context window (0 = follow the config default). */
    sessionContextWindow,
    /** True while a session-model change is in flight. */
    isSettingSessionModel: isSetting,
    handleSetSessionModel,
  }
}
