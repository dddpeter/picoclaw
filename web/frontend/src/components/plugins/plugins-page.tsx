import { useTranslation } from "react-i18next"

import type { PluginItem, PluginReportResponse } from "@/api/plugins"
import { PageHeader } from "@/components/page-header"
import { usePluginsPage } from "@/components/plugins/use-plugins-page"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from "@/components/ui/collapsible"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Switch } from "@/components/ui/switch"

function PluginReportBlock({ report }: { report: PluginReportResponse }) {
  const { t } = useTranslation()
  return (
    <div className="text-muted-foreground space-y-1 text-sm">
      {report.error ? (
        <div className="text-destructive">{report.error}</div>
      ) : null}
      {!report.error && report.ok ? (
        <div>
          {t("pages.plugins.report_summary", {
            defaultValue: "{{name}}: {{skills}} skills, {{mcp}} MCP servers",
            name: report.name,
            skills: report.skills,
            mcp: report.mcpServers,
          })}
        </div>
      ) : null}
      {report.warnings?.length ? (
        <div className="space-y-0.5 text-amber-600 dark:text-amber-400">
          {report.warnings.map((w, i) => (
            <div key={i} className="break-all">
              ⚠ {w}
            </div>
          ))}
        </div>
      ) : null}
    </div>
  )
}

function PluginCard({
  plugin,
  busy,
  onToggle,
  onRemove,
}: {
  plugin: PluginItem
  busy: boolean
  onToggle: (enabled: boolean) => void
  onRemove: () => void
}) {
  const { t } = useTranslation()
  const broken = Boolean(plugin.loadError)
  return (
    <Card size="sm" className={broken ? "opacity-70" : undefined}>
      <CardHeader className="border-border border-b py-3">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <div className="flex min-w-0 flex-wrap items-center gap-2">
            <CardTitle className="text-base font-medium">
              {plugin.name}
            </CardTitle>
            {plugin.version ? (
              <Badge variant="outline">v{plugin.version}</Badge>
            ) : null}
            {!plugin.registered ? (
              <Badge variant="secondary">
                {t("pages.plugins.unregistered", "unregistered")}
              </Badge>
            ) : null}
            {broken ? (
              <Badge variant="destructive">
                {t("pages.plugins.broken", "load failed")}
              </Badge>
            ) : (
              <Badge variant="secondary">
                {t("pages.plugins.counts", {
                  defaultValue: "{{skills}} skills · {{mcp}} MCP",
                  skills: plugin.skills,
                  mcp: plugin.mcpServers,
                })}
              </Badge>
            )}
          </div>
          <div className="flex items-center gap-3">
            {!broken ? (
              <Switch
                checked={plugin.enabled}
                disabled={busy}
                onCheckedChange={onToggle}
                aria-label={t("pages.plugins.enabled_label", "Enabled")}
              />
            ) : null}
            <Button
              variant="ghost"
              size="sm"
              className="text-destructive"
              disabled={busy}
              onClick={onRemove}
            >
              {t("pages.plugins.remove", "Remove")}
            </Button>
          </div>
        </div>
      </CardHeader>
      <CardContent className="space-y-2 pt-3 text-sm">
        {broken ? (
          <div className="text-destructive break-all">{plugin.loadError}</div>
        ) : null}
        {plugin.source ? (
          <div className="text-muted-foreground truncate">
            {t("pages.plugins.source", "Source")}: {plugin.source}
            {plugin.ref ? ` @${plugin.ref}` : ""}
          </div>
        ) : null}
        {plugin.installedAt ? (
          <div className="text-muted-foreground">
            {t("pages.plugins.installed_at", "Installed at")}:{" "}
            {new Date(plugin.installedAt).toLocaleString()}
          </div>
        ) : null}
        {plugin.warnings?.length ? (
          <Collapsible>
            <CollapsibleTrigger className="text-amber-600 underline-offset-2 hover:underline dark:text-amber-400">
              {t("pages.plugins.warnings", {
                defaultValue: "{{count}} warnings",
                count: plugin.warnings.length,
              })}
            </CollapsibleTrigger>
            <CollapsibleContent className="text-muted-foreground space-y-0.5 pt-1">
              {plugin.warnings.map((w, i) => (
                <div key={i} className="break-all">
                  ⚠ {w}
                </div>
              ))}
            </CollapsibleContent>
          </Collapsible>
        ) : null}
      </CardContent>
    </Card>
  )
}

export function PluginsPage() {
  const { t } = useTranslation()
  const {
    pluginsQuery,
    plugins,
    installRoot,
    registryError,
    toggleMutation,
    removeMutation,
    installMutation,
    validateMutation,
    installOpen,
    setInstallOpen,
    sourceText,
    setSourceText,
    refText,
    setRefText,
    openInstall,
    confirmRemove,
    setConfirmRemove,
    purgeData,
    setPurgeData,
  } = usePluginsPage()

  const loading = pluginsQuery.isLoading
  const busy =
    toggleMutation.isPending ||
    removeMutation.isPending ||
    installMutation.isPending
  const isGitSource =
    sourceText.startsWith("https://") ||
    sourceText.startsWith("git@") ||
    sourceText.startsWith("file://")
  const validateResult = validateMutation.data ?? null

  return (
    <div className="bg-background flex h-full flex-col">
      <PageHeader
        title={t("navigation.plugins", "Plugins")}
        titleExtra={
          <Button size="sm" onClick={openInstall}>
            {t("pages.plugins.install", "Install plugin")}
          </Button>
        }
      />

      <div className="flex-1 space-y-3 overflow-y-auto px-6 py-4">
        {registryError ? (
          <div className="text-sm text-amber-600 dark:text-amber-400">
            {t("pages.plugins.registry_error", "Registry problem")}:{" "}
            {registryError}
          </div>
        ) : null}

        {loading ? (
          <div className="text-muted-foreground text-sm">
            {t("common.loading", "Loading…")}
          </div>
        ) : plugins.length === 0 ? (
          <div className="text-muted-foreground py-8 text-center text-sm">
            {t("pages.plugins.empty", "No plugins installed.")}
            <div className="mt-1">
              {t("pages.plugins.empty_hint", {
                defaultValue:
                  "Install one from a local directory or a git URL under {{root}}",
                root: installRoot,
              })}
            </div>
          </div>
        ) : (
          plugins.map((p) => (
            <PluginCard
              key={p.name}
              plugin={p}
              busy={busy}
              onToggle={(enabled) =>
                toggleMutation.mutate({ name: p.name, enabled })
              }
              onRemove={() => setConfirmRemove(p)}
            />
          ))
        )}
      </div>

      {/* Install / validate dialog */}
      <Dialog open={installOpen} onOpenChange={setInstallOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>
              {t("pages.plugins.install", "Install plugin")}
            </DialogTitle>
            <DialogDescription>
              {t("pages.plugins.install_hint", {
                defaultValue:
                  "A local plugin directory or a git URL (branch/tag ref only)",
              })}
            </DialogDescription>
          </DialogHeader>
          <div className="space-y-3">
            <div className="space-y-1.5">
              <Label htmlFor="plugin-source">
                {t("pages.plugins.source", "Source")}
              </Label>
              <Input
                id="plugin-source"
                value={sourceText}
                onChange={(e) => setSourceText(e.target.value)}
                placeholder="D:\plugins\my-plugin  ·  https://github.com/example/my-plugin"
              />
            </div>
            {isGitSource ? (
              <div className="space-y-1.5">
                <Label htmlFor="plugin-ref">
                  {t("pages.plugins.ref", "Ref (branch or tag)")}
                </Label>
                <Input
                  id="plugin-ref"
                  value={refText}
                  onChange={(e) => setRefText(e.target.value)}
                  placeholder="main · v1"
                />
              </div>
            ) : null}
            {validateResult ? (
              <PluginReportBlock report={validateResult} />
            ) : null}
          </div>
          <DialogFooter className="gap-2">
            <Button
              variant="outline"
              disabled={
                !sourceText.trim() || isGitSource || validateMutation.isPending
              }
              title={
                isGitSource
                  ? t("pages.plugins.validate_local_only", {
                      defaultValue: "Validate works on local directories only",
                    })
                  : undefined
              }
              onClick={() =>
                validateMutation.mutate({ path: sourceText.trim() })
              }
            >
              {validateMutation.isPending
                ? t("common.loading", "Loading…")
                : t("pages.plugins.validate", "Validate only")}
            </Button>
            <Button
              disabled={!sourceText.trim() || installMutation.isPending}
              onClick={() => {
                validateMutation.reset()
                installMutation.mutate({
                  source: sourceText.trim(),
                  ref: refText.trim(),
                })
                setInstallOpen(false)
              }}
            >
              {t("pages.plugins.install", "Install plugin")}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* Remove confirmation dialog */}
      <Dialog
        open={confirmRemove !== null}
        onOpenChange={(open) => {
          if (!open) setConfirmRemove(null)
        }}
      >
        <DialogContent>
          <DialogHeader>
            <DialogTitle>
              {t("pages.plugins.remove_confirm_title", {
                defaultValue: "Remove {{name}}?",
                name: confirmRemove?.name ?? "",
              })}
            </DialogTitle>
            <DialogDescription>
              {t("pages.plugins.remove_confirm_desc", {
                defaultValue:
                  "The plugin directory is deleted. Its data directory can be kept or purged.",
              })}
            </DialogDescription>
          </DialogHeader>
          <label className="flex cursor-pointer items-center gap-2 text-sm">
            <Switch
              checked={purgeData}
              onCheckedChange={setPurgeData}
              aria-label={t("pages.plugins.purge_data", "Purge data directory")}
            />
            {t("pages.plugins.purge_data", "Purge data directory")}
          </label>
          <DialogFooter>
            <Button variant="outline" onClick={() => setConfirmRemove(null)}>
              {t("common.cancel", "Cancel")}
            </Button>
            <Button
              variant="destructive"
              disabled={removeMutation.isPending}
              onClick={() => {
                if (confirmRemove) {
                  removeMutation.mutate({
                    name: confirmRemove.name,
                    purgeData,
                  })
                  setConfirmRemove(null)
                  setPurgeData(false)
                }
              }}
            >
              {t("pages.plugins.remove", "Remove")}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}
