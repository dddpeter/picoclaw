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
      const previous = sessionModel
      setIsSetting(true)
      // Optimistic: the selector shows the new model immediately; a failed
      // request rolls it back.
      setSessionModelState(modelName)
      try {
        const response = await setSessionModel(activeSessionId, modelName)
        applyResponse(response)
      } catch (err) {
        console.error("Failed to set session model:", err)
        setSessionModelState(previous)
        toast.error(
          err instanceof Error ? err.message : t("chat.sessionModelSetFailed"),
        )
      } finally {
        setIsSetting(false)
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
