import { useCallback, useEffect, useMemo, useRef, useState } from "react"
import { useTranslation } from "react-i18next"
import { toast } from "sonner"

import {
  getMCPConfig,
  getMCPStatus,
  putMCPConfig,
  testMCPServer,
  type MCPServerPayload,
  type MCPServerStatusEntry,
  type MCPStatusResponse,
} from "@/api/mcp"
import { refreshGatewayState } from "@/store/gateway"
import { showSaveSuccessOrRestartToast } from "@/lib/restart-required"

import {
  draftToServerPayload,
  emptyServerDraft,
  formToPayload,
  serverPayloadToDraft,
  type MCPServerDraft,
  type MCPConfigForm,
} from "./types"

export type MCPPageTab = "servers" | "settings"

export type ServerRuntimeStatus =
  | "offline"
  | "not_initialized"
  | "connected"
  | "error"
  | "not_loaded"

export interface ServerTestState {
  running: boolean
  ok?: boolean
  latencyMs?: number
  toolCount?: number
  tools?: Array<{ name: string; description: string }>
  error?: string
}

function formFromResponse(payload: Awaited<ReturnType<typeof getMCPConfig>>): MCPConfigForm {
  return {
    enabled: payload.enabled,
    maxInlineTextChars: String(payload.maxInlineTextChars || 16384),
    discoveryEnabled: payload.discovery.enabled,
    discoveryTTL: String(payload.discovery.ttlSeconds || 5),
    discoveryMaxResults: String(payload.discovery.maxSearchResults || 5),
    discoveryUseBM25: payload.discovery.useBM25,
    discoveryUseRegex: payload.discovery.useRegex,
    servers: payload.servers.map(serverPayloadToDraft),
    pluginServers: payload.pluginServers ?? [],
  }
}

export function useMCPPage() {
  const { t } = useTranslation()
  const [tab, setTab] = useState<MCPPageTab>("servers")
  const [form, setForm] = useState<MCPConfigForm | null>(null)
  const baselineRef = useRef<string>("")
  const [status, setStatus] = useState<MCPStatusResponse | null>(null)
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [testStates, setTestStates] = useState<Record<string, ServerTestState>>({})

  const fetchStatus = useCallback(async () => {
    try {
      setStatus(await getMCPStatus())
    } catch {
      setStatus({ gateway: "offline" })
    }
  }, [])

  const load = useCallback(async () => {
    setLoading(true)
    try {
      const payload = await getMCPConfig()
      const nextForm = formFromResponse(payload)
      baselineRef.current = JSON.stringify(nextForm)
      setForm(nextForm)
    } catch (err) {
      toast.error(err instanceof Error ? err.message : String(err))
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    void load()
    void fetchStatus()
  }, [load, fetchStatus])

  const dirty = useMemo(
    () => form !== null && JSON.stringify(form) !== baselineRef.current,
    [form],
  )

  const updateGlobal = useCallback(
    (patch: Partial<Omit<MCPConfigForm, "servers">>) => {
      setForm((prev) => (prev ? { ...prev, ...patch } : prev))
    },
    [],
  )

  const updateServer = useCallback((id: string, patch: Partial<MCPServerDraft>) => {
    setForm(
      (prev) =>
        prev && {
          ...prev,
          servers: prev.servers.map((s) => (s.id === id ? { ...s, ...patch } : s)),
        },
    )
  }, [])

  const addServer = useCallback(() => {
    setForm((prev) => prev && { ...prev, servers: [...prev.servers, emptyServerDraft()] })
  }, [])

  const removeServer = useCallback((id: string) => {
    setForm((prev) => prev && { ...prev, servers: prev.servers.filter((s) => s.id !== id) })
  }, [])

  const save = useCallback(async () => {
    if (!form) return
    setSaving(true)
    try {
      const payload = formToPayload(
        form,
        t("pages.mcp.invalid_json", { field: "headers" }),
        (field) => t("pages.mcp.invalid_number", { field }),
      )
      // 至少一个发现方式
      if (
        payload.enabled &&
        payload.discovery.enabled &&
        !payload.discovery.useBM25 &&
        !payload.discovery.useRegex
      ) {
        toast.error(t("pages.mcp.discovery.use_bm25_hint"))
        return
      }
      await putMCPConfig(payload)
      const fresh = await getMCPConfig()
      const nextForm = formFromResponse(fresh)
      baselineRef.current = JSON.stringify(nextForm)
      setForm(nextForm)
      const gateway = await refreshGatewayState({ force: true })
      showSaveSuccessOrRestartToast(
        t,
        t("pages.mcp.save_success"),
        t("navigation.mcp"),
        gateway?.restartRequired === true,
      )
      void fetchStatus()
    } catch (err) {
      const message = err instanceof Error ? err.message : String(err)
      if (message.startsWith("INVALID_JSON:")) {
        toast.error(t("pages.mcp.invalid_json", { field: message.slice("INVALID_JSON:".length) }))
      } else if (message.startsWith("INVALID_NUMBER:")) {
        toast.error(
          t("pages.mcp.invalid_number", { field: message.slice("INVALID_NUMBER:".length) }),
        )
      } else {
        toast.error(t("pages.mcp.save_failed", { message }))
      }
    } finally {
      setSaving(false)
    }
  }, [form, t, fetchStatus])

  const runTest = useCallback(
    async (draft: MCPServerDraft) => {
      setTestStates((prev) => ({ ...prev, [draft.id]: { running: true } }))
      try {
        let payload: MCPServerPayload
        try {
          payload = draftToServerPayload(draft, t("pages.mcp.invalid_json", { field: "headers" }))
        } catch {
          setTestStates((prev) => ({
            ...prev,
            [draft.id]: { running: false, error: t("pages.mcp.invalid_json", { field: "headers" }) },
          }))
          return
        }
        const res = await testMCPServer(payload)
        setTestStates((prev) => ({ ...prev, [draft.id]: { running: false, ...res } }))
      } catch (err) {
        setTestStates((prev) => ({
          ...prev,
          [draft.id]: { running: false, error: err instanceof Error ? err.message : String(err) },
        }))
      }
    },
    [t],
  )

  const serverStatus = useCallback(
    (name: string): { status: ServerRuntimeStatus; entry?: MCPServerStatusEntry } => {
      if (!status || status.gateway === "offline") return { status: "offline" }
      if (!status.initialized) return { status: "not_initialized" }
      const entry = status.servers?.[name]
      if (!entry) return { status: "not_loaded" }
      return entry.connected ? { status: "connected", entry } : { status: "error", entry }
    },
    [status],
  )

  return {
    tab, setTab, form, dirty, saving, loading, status, testStates,
    updateGlobal, updateServer, addServer, removeServer, save, runTest,
    fetchStatus, reload: load, serverStatus, t,
  }
}
