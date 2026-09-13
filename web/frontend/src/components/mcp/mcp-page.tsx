import { useState } from "react"
import { useTranslation } from "react-i18next"

import { PageHeader } from "@/components/page-header"
import { ServerCard } from "@/components/mcp/server-card"
import { useMCPPage, type MCPPageTab } from "@/components/mcp/use-mcp-page"
import type { MCPConfigForm } from "@/components/mcp/types"
import { Field, SwitchCardField } from "@/components/shared-form"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Switch } from "@/components/ui/switch"
import { cn } from "@/lib/utils"

export function MCPPage() {
  const { t } = useTranslation()
  const {
    tab, setTab, form, dirty, saving, loading, status, testStates,
    updateGlobal, updateServer, addServer, removeServer, save, runTest,
    fetchStatus, reload, serverStatus,
  } = useMCPPage()
  const [confirmRemove, setConfirmRemove] = useState<string | null>(null)
  const [confirmDiscard, setConfirmDiscard] = useState(false)

  const tabs: Array<{ key: MCPPageTab; label: string }> = [
    { key: "servers", label: t("pages.mcp.tabs.servers") },
    { key: "settings", label: t("pages.mcp.tabs.settings") },
  ]

  const offline = !status || status.gateway === "offline"
  const summary = (() => {
    if (offline) return t("pages.mcp.banner.offline")
    if (!status?.initialized) return t("pages.mcp.banner.not_initialized")
    const entries = Object.values(status.servers ?? {})
    const connected = entries.filter((e) => e.connected).length
    const tools = entries.reduce((acc, e) => acc + e.toolCount, 0)
    return t("pages.mcp.banner.summary", { connected, total: entries.length, tools })
  })()

  return (
    <div className="bg-background flex h-full flex-col">
      <PageHeader title={t("navigation.mcp", "MCP")} />

      <div className="border-border/60 flex flex-wrap items-center gap-x-6 gap-y-3 border-b px-6 py-3">
        <div className="flex items-center gap-2">
          <Switch
            checked={form?.enabled ?? false}
            disabled={!form}
            onCheckedChange={(checked) => updateGlobal({ enabled: checked })}
            aria-label={t("pages.mcp.global.enabled")}
          />
          <span className="text-sm font-medium">{t("pages.mcp.global.enabled")}</span>
        </div>
        <span className={cn("text-muted-foreground text-sm", offline && "opacity-70")}>
          {summary}
        </span>
        <Button variant="outline" size="sm" onClick={() => void fetchStatus()}>
          {t("pages.mcp.banner.refresh")}
        </Button>
      </div>

      <div className="border-border/60 border-b px-6 pt-2">
        <div className="flex gap-8">
          {tabs.map((item) => (
            <button
              key={item.key}
              type="button"
              onClick={() => setTab(item.key)}
              className={cn(
                "hover:text-foreground relative cursor-pointer pb-4 text-[14px] font-medium transition-colors outline-none",
                tab === item.key ? "text-foreground" : "text-muted-foreground",
              )}
            >
              {item.label}
              {tab === item.key && (
                <span className="bg-primary absolute inset-x-0 bottom-0 h-[2px] rounded-t-full" />
              )}
            </button>
          ))}
        </div>
      </div>

      <div className="flex-1 overflow-y-auto px-6 pt-4">
        {loading || !form ? (
          <p className="text-muted-foreground text-sm">…</p>
        ) : tab === "servers" ? (
          <div className="mx-auto flex max-w-3xl flex-col gap-3">
            {form.servers.length === 0 && (
              <p className="text-muted-foreground text-sm">{t("pages.mcp.servers.empty")}</p>
            )}
            {form.servers.map((server) => (
              <ServerCard
                key={server.id}
                draft={server}
                runtime={serverStatus(server.name.trim())}
                testState={testStates[server.id]}
                confirmRemove={confirmRemove === server.id}
                onRequestConfirmRemove={() => setConfirmRemove(server.id)}
                onCancelConfirmRemove={() => setConfirmRemove(null)}
                onConfirmRemove={() => {
                  removeServer(server.id)
                  setConfirmRemove(null)
                }}
                onChange={(patch) => updateServer(server.id, patch)}
                onTest={() => void runTest(server)}
              />
            ))}
            <div>
              <Button variant="outline" size="sm" onClick={addServer}>
                + {t("pages.mcp.servers.add")}
              </Button>
            </div>
          </div>
        ) : (
          <DiscoverySettings form={form} onChange={updateGlobal} />
        )}

        {dirty && (
          <div className="border-border/60 bg-background/95 sticky bottom-0 mt-4 flex items-center justify-end gap-3 rounded-md border px-4 py-3 backdrop-blur">
            <span className="text-muted-foreground text-sm">{t("pages.mcp.discard_hint")}</span>
            {confirmDiscard ? (
              <Button
                variant="destructive"
                size="sm"
                onClick={() => {
                  setConfirmDiscard(false)
                  void reload()
                }}
              >
                {t("pages.mcp.discard_confirm")}
              </Button>
            ) : (
              <Button variant="ghost" size="sm" onClick={() => setConfirmDiscard(true)}>
                {t("pages.mcp.discard")}
              </Button>
            )}
            <Button
              onClick={() => {
                setConfirmDiscard(false)
                void save()
              }}
              disabled={saving}
            >
              {t("pages.mcp.save")}
            </Button>
          </div>
        )}
      </div>
    </div>
  )
}

function DiscoverySettings({
  form,
  onChange,
}: {
  form: MCPConfigForm
  onChange: (patch: Partial<Omit<MCPConfigForm, "servers">>) => void
}) {
  const { t } = useTranslation()

  return (
    <div className="mx-auto flex max-w-3xl flex-col gap-1">
      <SwitchCardField
        label={t("pages.mcp.discovery.enabled")}
        hint={t("pages.mcp.discovery.enabled_hint")}
        layout="setting-row"
        checked={form.discoveryEnabled}
        onCheckedChange={(checked) => onChange({ discoveryEnabled: checked })}
      />

      {form.discoveryEnabled && (
        <>
          <Field
            label={t("pages.mcp.discovery.ttl")}
            hint={t("pages.mcp.discovery.ttl_hint")}
            layout="setting-row"
          >
            <Input
              type="number"
              min={1}
              value={form.discoveryTTL}
              onChange={(e) => onChange({ discoveryTTL: e.target.value })}
            />
          </Field>
          <Field
            label={t("pages.mcp.discovery.max_results")}
            hint={t("pages.mcp.discovery.max_results_hint")}
            layout="setting-row"
          >
            <Input
              type="number"
              min={1}
              value={form.discoveryMaxResults}
              onChange={(e) => onChange({ discoveryMaxResults: e.target.value })}
            />
          </Field>
          <SwitchCardField
            label={t("pages.mcp.discovery.use_bm25")}
            hint={t("pages.mcp.discovery.use_bm25_hint")}
            layout="setting-row"
            checked={form.discoveryUseBM25}
            disabled={form.discoveryUseBM25 && !form.discoveryUseRegex}
            onCheckedChange={(checked) => onChange({ discoveryUseBM25: checked })}
          />
          <SwitchCardField
            label={t("pages.mcp.discovery.use_regex")}
            hint={t("pages.mcp.discovery.use_regex_hint")}
            layout="setting-row"
            checked={form.discoveryUseRegex}
            disabled={form.discoveryUseRegex && !form.discoveryUseBM25}
            onCheckedChange={(checked) => onChange({ discoveryUseRegex: checked })}
          />
        </>
      )}

      <Field
        label={t("pages.mcp.advanced.max_inline")}
        hint={t("pages.mcp.advanced.max_inline_hint")}
        layout="setting-row"
      >
        <Input
          type="number"
          min={1}
          value={form.maxInlineTextChars}
          onChange={(e) => onChange({ maxInlineTextChars: e.target.value })}
        />
      </Field>
    </div>
  )
}
