import { IconTrash } from "@tabler/icons-react"
import { useTranslation } from "react-i18next"

import type { MCPServerStatusEntry, MCPServerType } from "@/api/mcp"
import { Field, SwitchCardField } from "@/components/shared-form"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from "@/components/ui/collapsible"
import { Input } from "@/components/ui/input"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { Switch } from "@/components/ui/switch"
import { Textarea } from "@/components/ui/textarea"
import { cn } from "@/lib/utils"

import type { DeferredMode, MCPServerDraft } from "./types"
import type { ServerRuntimeStatus, ServerTestState } from "./use-mcp-page"

const STATUS_DOT: Record<ServerRuntimeStatus, string> = {
  offline: "bg-muted-foreground/40",
  not_initialized: "bg-muted-foreground/40",
  not_loaded: "bg-muted-foreground/40",
  connected: "bg-emerald-500",
  error: "bg-red-500",
}

interface ServerCardProps {
  draft: MCPServerDraft
  runtime: { status: ServerRuntimeStatus; entry?: MCPServerStatusEntry }
  testState?: ServerTestState
  confirmRemove: boolean
  onRequestConfirmRemove: () => void
  onCancelConfirmRemove: () => void
  onConfirmRemove: () => void
  onChange: (patch: Partial<MCPServerDraft>) => void
  onTest: () => void
}

export function ServerCard({
  draft,
  runtime,
  testState,
  confirmRemove,
  onRequestConfirmRemove,
  onCancelConfirmRemove,
  onConfirmRemove,
  onChange,
  onTest,
}: ServerCardProps) {
  const { t } = useTranslation()

  return (
    <Collapsible>
      <div className="border-border rounded-md border">
        <CollapsibleTrigger asChild>
          <button
            type="button"
            className="hover:bg-muted/40 flex w-full cursor-pointer items-center gap-3 px-3 py-2.5 text-left transition-colors"
          >
            <span
              className={cn("size-2 shrink-0 rounded-full", STATUS_DOT[runtime.status])}
              title={t(`pages.mcp.server.status_${runtime.status}`)}
            />
            <span className="text-sm font-medium">
              {draft.name.trim() || t("pages.mcp.server.name_placeholder")}
            </span>
            <Badge variant="outline">{draft.type}</Badge>
            {runtime.status === "connected" && runtime.entry && (
              <span className="text-muted-foreground text-xs">
                {t("pages.mcp.server.tools_count", { count: runtime.entry.toolCount })}
              </span>
            )}
            <span
              className="ml-auto flex items-center gap-2"
              onClick={(e) => e.stopPropagation()}
            >
              <Switch
                checked={draft.enabled}
                onCheckedChange={(checked) => onChange({ enabled: checked })}
                aria-label={t("pages.mcp.server.enabled")}
              />
              {confirmRemove ? (
                <span className="flex items-center gap-1">
                  <Button
                    variant="ghost"
                    size="sm"
                    className="text-muted-foreground h-8 px-2"
                    onClick={onCancelConfirmRemove}
                  >
                    ×
                  </Button>
                  <Button variant="destructive" size="sm" onClick={onConfirmRemove}>
                    {t("pages.mcp.servers.remove_confirm", { name: draft.name.trim() })}
                  </Button>
                </span>
              ) : (
                <Button
                  variant="ghost"
                  size="icon"
                  className="text-muted-foreground hover:text-destructive size-8"
                  onClick={onRequestConfirmRemove}
                  aria-label={t("pages.mcp.servers.remove")}
                >
                  <IconTrash className="size-4" />
                </Button>
              )}
            </span>
          </button>
        </CollapsibleTrigger>

        <CollapsibleContent>
          <div className="border-border/60 flex flex-col gap-1 border-t px-3 py-3">
            {runtime.status === "error" && runtime.entry?.error && (
              <p className="text-destructive mb-2 text-xs">
                {t("pages.mcp.server.error_label")}: {runtime.entry.error}
              </p>
            )}

            <div className="grid gap-3 md:grid-cols-2">
              <Field label={t("pages.mcp.server.name")} layout="setting-row">
                <Input
                  value={draft.name}
                  placeholder={t("pages.mcp.server.name_placeholder")}
                  onChange={(e) => onChange({ name: e.target.value })}
                />
              </Field>
              <Field label={t("pages.mcp.server.type")} layout="setting-row">
                <Select
                  value={draft.type}
                  onValueChange={(value) => onChange({ type: value as MCPServerType })}
                >
                  <SelectTrigger aria-label={t("pages.mcp.server.type")}>
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="stdio">stdio</SelectItem>
                    <SelectItem value="sse">sse</SelectItem>
                    <SelectItem value="http">http</SelectItem>
                  </SelectContent>
                </Select>
              </Field>
            </div>

            <SwitchCardField
              label={t("pages.mcp.server.enabled")}
              layout="setting-row"
              checked={draft.enabled}
              onCheckedChange={(checked) => onChange({ enabled: checked })}
            />

            <Field label={t("pages.mcp.server.deferred")} layout="setting-row">
              <Select
                value={draft.deferred}
                onValueChange={(value) => onChange({ deferred: value as DeferredMode })}
              >
                <SelectTrigger aria-label={t("pages.mcp.server.deferred")}>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="inherit">
                    {t("pages.mcp.server.deferred_inherit")}
                  </SelectItem>
                  <SelectItem value="deferred">
                    {t("pages.mcp.server.deferred_deferred")}
                  </SelectItem>
                  <SelectItem value="eager">
                    {t("pages.mcp.server.deferred_eager")}
                  </SelectItem>
                </SelectContent>
              </Select>
            </Field>

            {draft.type !== "stdio" ? (
              <>
                <Field label={t("pages.mcp.server.url")} layout="setting-row">
                  <Input
                    value={draft.url}
                    placeholder={t("pages.mcp.server.url_placeholder")}
                    onChange={(e) => onChange({ url: e.target.value })}
                  />
                </Field>
                <Field
                  label={t("pages.mcp.server.headers")}
                  hint={t("pages.mcp.server.headers_hint")}
                  layout="setting-row"
                >
                  <Textarea
                    value={draft.headersText}
                    className="min-h-[88px] font-mono text-xs"
                    onChange={(e) => onChange({ headersText: e.target.value })}
                  />
                </Field>
              </>
            ) : (
              <>
                <Field label={t("pages.mcp.server.command")} layout="setting-row">
                  <Input
                    value={draft.command}
                    placeholder={t("pages.mcp.server.command_placeholder")}
                    onChange={(e) => onChange({ command: e.target.value })}
                  />
                </Field>
                <Field label={t("pages.mcp.server.args")} layout="setting-row">
                  <Textarea
                    value={draft.argsText}
                    className="min-h-[72px] font-mono text-xs"
                    onChange={(e) => onChange({ argsText: e.target.value })}
                  />
                </Field>
                <Field label={t("pages.mcp.server.env")} layout="setting-row">
                  <Textarea
                    value={draft.envText}
                    className="min-h-[72px] font-mono text-xs"
                    onChange={(e) => onChange({ envText: e.target.value })}
                  />
                </Field>
                <Field label={t("pages.mcp.server.env_file")} layout="setting-row">
                  <Input
                    value={draft.envFile}
                    placeholder={t("pages.mcp.server.env_file_placeholder")}
                    onChange={(e) => onChange({ envFile: e.target.value })}
                  />
                </Field>
              </>
            )}

            {runtime.status === "connected" &&
              runtime.entry &&
              runtime.entry.tools.length > 0 && (
                <div className="mt-1">
                  <p className="text-muted-foreground mb-1.5 text-xs font-medium">
                    {t("pages.mcp.server.tools_title")}
                  </p>
                  <div className="flex flex-wrap gap-1.5">
                    {runtime.entry.tools.map((tool) => (
                      <Badge
                        key={tool.name}
                        variant="secondary"
                        title={tool.description}
                        className="max-w-[22rem] truncate font-normal"
                      >
                        {tool.name}
                      </Badge>
                    ))}
                  </div>
                </div>
              )}

            <div className="border-border/60 mt-2 flex items-center gap-3 border-t pt-3">
              <Button
                type="button"
                variant="outline"
                size="sm"
                onClick={onTest}
                disabled={testState?.running}
              >
                {testState?.running ? t("pages.mcp.test.running") : t("pages.mcp.test.run")}
              </Button>
              {testState && !testState.running && (
                testState.ok ? (
                  <span className="text-sm text-emerald-600 dark:text-emerald-400">
                    {t("pages.mcp.test.success", {
                      latency: testState.latencyMs,
                      count: testState.toolCount,
                    })}
                  </span>
                ) : (
                  <span className="text-destructive max-w-[36rem] truncate text-sm">
                    {t("pages.mcp.test.failed")}
                    {testState.error ? `: ${testState.error}` : ""}
                  </span>
                )
              )}
            </div>
            {testState?.ok && (testState.tools?.length ?? 0) > 0 && (
              <div className="mt-1 flex flex-col gap-1">
                {(testState.tools ?? []).map((tool) => (
                  <div key={tool.name} className="text-muted-foreground truncate text-xs">
                    <span className="text-foreground font-mono">{tool.name}</span>
                    {tool.description ? ` — ${tool.description}` : ""}
                  </div>
                ))}
              </div>
            )}
          </div>
        </CollapsibleContent>
      </div>
    </Collapsible>
  )
}
