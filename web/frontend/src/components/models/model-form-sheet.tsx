/**
 * Shared sheet body for Add/EditModelSheet.
 *
 * Renders the full model form (provider, model id, api key/base, default
 * toggle, advanced section, footer) from a `useModelFormState` instance.
 * Sheets inject only the differences: header, entry-only fields
 * (`headerFields`), error display, dialog instances (children).
 */
import type { ReactNode } from "react"
import {
  IconDownload,
  IconLoader2,
  IconPlugConnected,
} from "@tabler/icons-react"
import { useRef } from "react"
import { useTranslation } from "react-i18next"

import type { ModelProviderOption } from "@/api/models"
import { ConfigChangeNotice } from "@/components/config-change-notice"
import {
  AdvancedSection,
  Field,
  KeyInput,
  SwitchCardField,
} from "@/components/shared-form"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetFooter,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet"
import { Textarea } from "@/components/ui/textarea"

import { type ModelFormStateApi } from "./model-form-state"
import { ProviderCombobox } from "./provider-combobox"
import { providerSupportsFetch } from "./provider-registry"

interface ModelFormSheetProps {
  open: boolean
  onClose: () => void
  saving: boolean
  title: ReactNode
  description: ReactNode
  state: ModelFormStateApi
  providerOptions?: ModelProviderOption[]
  isDirty: boolean
  confirmLabel: string
  onSave: () => void
  /** Rendered above the provider field (e.g. the model display name field). */
  headerFields?: ReactNode
  providerFieldError?: string
  modelFieldError?: string
  showSelectProviderFirstHint?: boolean
  providerComboboxAllowCreate?: boolean
  testDisabled: boolean
  apiKeyPlaceholder: string
  apiKeyHint?: string
  defaultOnSaveHint: string
  /** Dialog instances (FetchModelsDialog / TestModelDialog). */
  children?: ReactNode
}

export function ModelFormSheet({
  open,
  onClose,
  saving,
  title,
  description,
  state,
  providerOptions,
  isDirty,
  confirmLabel,
  onSave,
  headerFields,
  providerFieldError,
  modelFieldError,
  showSelectProviderFirstHint,
  providerComboboxAllowCreate,
  testDisabled,
  apiKeyPlaceholder,
  apiKeyHint,
  defaultOnSaveHint,
  children,
}: ModelFormSheetProps) {
  const { t } = useTranslation()
  const scrollContainerRef = useRef<HTMLDivElement>(null)

  const {
    form,
    setForm,
    setAsDefault,
    setSetAsDefault,
    serverError,
    modelValidation,
    setFetchOpen,
    setTestOpen,
    fetchedModels,
    catalogModels,
    setField,
    handleModelChange,
    handleProviderChange,
    applyFix,
    handleCommonModel,
    commonModels,
    authMethodLocked,
    defaultAuthMethod,
    isOAuth,
    apiBasePlaceholder,
  } = state

  return (
    <Sheet open={open} onOpenChange={(v) => !v && !saving && onClose()}>
      <SheetContent
        side="right"
        className="flex flex-col gap-0 p-0 data-[side=right]:!w-full data-[side=right]:sm:!w-[560px] data-[side=right]:sm:!max-w-[560px]"
      >
        <SheetHeader className="border-b-muted border-b px-6 py-5">
          <SheetTitle className="text-base">{title}</SheetTitle>
          <SheetDescription className="text-xs">{description}</SheetDescription>
        </SheetHeader>

        <div
          className="min-h-0 flex-1 overflow-y-auto"
          ref={scrollContainerRef}
        >
          <div className="space-y-5 px-6 py-5">
            {headerFields}

            <Field
              label={t("models.field.provider")}
              hint={t("models.field.providerHint")}
              error={providerFieldError}
              required
            >
              <ProviderCombobox
                value={form.provider}
                onChange={handleProviderChange}
                placeholder={t("models.field.providerPlaceholder")}
                backendOptions={providerOptions}
                filterCreateAllowed={providerComboboxAllowCreate}
                containerRef={scrollContainerRef}
              />
            </Field>

            <Field
              label={t("models.add.modelId")}
              hint={t("models.add.modelIdHint")}
            >
              <Input
                value={form.modelId}
                onChange={handleModelChange}
                placeholder={
                  state.providerDef
                    ? `${commonModels[0] || "model-name"}`
                    : t("models.add.modelIdPlaceholder")
                }
                className="font-mono text-sm"
                aria-invalid={
                  !!modelFieldError || modelValidation?.level === "error"
                }
              />
              {modelValidation && modelValidation.messageKey && (
                <div
                  className={`flex items-center gap-2 text-xs ${
                    modelValidation.level === "error"
                      ? "text-destructive"
                      : modelValidation.level === "warning"
                        ? "text-yellow-600 dark:text-yellow-500"
                        : "text-green-600 dark:text-green-500"
                  }`}
                >
                  <span>
                    {t(
                      modelValidation.messageKey,
                      modelValidation.messageParams,
                    )}
                  </span>
                  {modelValidation.fix && (
                    <button
                      type="button"
                      onClick={applyFix}
                      className="text-primary underline hover:no-underline"
                    >
                      {t("common.fix")}
                    </button>
                  )}
                </div>
              )}
              {modelFieldError && !modelValidation && (
                <p className="text-destructive text-xs">{modelFieldError}</p>
              )}
              {commonModels.length > 0 && (
                <div className="flex flex-wrap gap-1.5">
                  {commonModels.map((m) => (
                    <Badge
                      key={m}
                      variant="secondary"
                      className="hover:bg-secondary/80 cursor-pointer font-mono text-xs"
                      onClick={() => handleCommonModel(m)}
                    >
                      {m}
                    </Badge>
                  ))}
                </div>
              )}
              {catalogModels.length > 0 && (
                <div className="flex flex-wrap gap-1.5">
                  {catalogModels.map((m) => (
                    <Badge
                      key={m}
                      variant={form.modelId === m ? "default" : "outline"}
                      className="cursor-pointer font-mono text-xs"
                      onClick={() => handleCommonModel(m)}
                    >
                      {m}
                    </Badge>
                  ))}
                </div>
              )}
              {fetchedModels.length > 0 && (
                <div className="flex flex-wrap gap-1.5">
                  {fetchedModels.map((m) => (
                    <Badge
                      key={m}
                      variant={form.modelId === m ? "default" : "outline"}
                      className="cursor-pointer font-mono text-xs"
                      onClick={() => handleCommonModel(m)}
                    >
                      {m}
                    </Badge>
                  ))}
                </div>
              )}
              <div className="flex items-center gap-2">
                {providerSupportsFetch(form.provider, providerOptions) && (
                  <Button
                    variant="outline"
                    size="sm"
                    className="h-7 text-xs"
                    onClick={() => setFetchOpen(true)}
                  >
                    <IconDownload className="size-3" />
                    {t("models.fetch.title")}
                  </Button>
                )}
                {showSelectProviderFirstHint && !form.provider && (
                  <span className="text-muted-foreground text-xs">
                    {t("models.field.selectProviderFirst")}
                  </span>
                )}
              </div>
            </Field>

            {!isOAuth && (
              <Field label={t("models.field.apiKey")} hint={apiKeyHint}>
                <KeyInput
                  value={form.apiKey}
                  onChange={(v) => setForm((f) => ({ ...f, apiKey: v }))}
                  placeholder={apiKeyPlaceholder}
                />
              </Field>
            )}

            <Field
              label={t("models.field.apiBase")}
              hint={isOAuth ? t("models.edit.oauthNote") : undefined}
            >
              <Input
                value={form.apiBase}
                onChange={setField("apiBase")}
                placeholder={apiBasePlaceholder}
                disabled={isOAuth}
              />
            </Field>

            <div className="flex items-center gap-2">
              <Button
                variant="outline"
                size="sm"
                onClick={() => setTestOpen(true)}
                disabled={testDisabled}
              >
                <IconPlugConnected className="size-4" />
                {t("models.test.testConnection")}
              </Button>
            </div>

            <SwitchCardField
              label={t("models.defaultOnSave.label")}
              hint={defaultOnSaveHint}
              checked={setAsDefault}
              onCheckedChange={setSetAsDefault}
              disabled={!state.defaultModelAllowed}
            />

            <AdvancedSection>
              <Field
                label={t("models.field.proxy")}
                hint={t("models.field.proxyHint")}
              >
                <Input
                  value={form.proxy}
                  onChange={setField("proxy")}
                  placeholder="http://127.0.0.1:7890"
                />
              </Field>

              <Field
                label={t("models.field.authMethod")}
                hint={
                  authMethodLocked
                    ? t("models.field.authMethodManagedHint")
                    : t("models.field.authMethodHint")
                }
              >
                <Input
                  value={
                    authMethodLocked ? defaultAuthMethod : form.authMethod
                  }
                  onChange={setField("authMethod")}
                  placeholder="oauth"
                  disabled={authMethodLocked}
                />
              </Field>

              <Field
                label={t("models.field.connectMode")}
                hint={t("models.field.connectModeHint")}
              >
                <Input
                  value={form.connectMode}
                  onChange={setField("connectMode")}
                  placeholder="stdio"
                />
              </Field>

              <Field
                label={t("models.field.workspace")}
                hint={t("models.field.workspaceHint")}
              >
                <Input
                  value={form.workspace}
                  onChange={setField("workspace")}
                  placeholder="/path/to/workspace"
                />
              </Field>

              <Field
                label={t("models.field.requestTimeout")}
                hint={t("models.field.requestTimeoutHint")}
              >
                <Input
                  value={form.requestTimeout}
                  onChange={setField("requestTimeout")}
                  placeholder="60"
                  type="number"
                  min={0}
                />
              </Field>

              <Field
                label={t("models.field.rpm")}
                hint={t("models.field.rpmHint")}
              >
                <Input
                  value={form.rpm}
                  onChange={setField("rpm")}
                  placeholder="60"
                  type="number"
                  min={0}
                />
              </Field>

              <Field
                label={t("models.field.thinkingLevel")}
                hint={t("models.field.thinkingLevelHint")}
              >
                <Input
                  value={form.thinkingLevel}
                  onChange={setField("thinkingLevel")}
                  placeholder={t("models.field.providerDefault")}
                />
              </Field>

              <Field
                label={t("models.field.maxTokensField")}
                hint={t("models.field.maxTokensFieldHint")}
              >
                <Input
                  value={form.maxTokensField}
                  onChange={setField("maxTokensField")}
                  placeholder="max_completion_tokens"
                />
              </Field>

              <Field
                label={t("models.field.toolSchemaTransform")}
                hint={t("models.field.toolSchemaTransformHint")}
              >
                <Input
                  value={form.toolSchemaTransform}
                  onChange={setField("toolSchemaTransform")}
                  placeholder="google"
                />
              </Field>

              <SwitchCardField
                label={t("models.field.streamingEnabled")}
                hint={t("models.field.streamingEnabledHint")}
                checked={form.streamingEnabled}
                onCheckedChange={(checked) =>
                  setForm((f) => ({ ...f, streamingEnabled: checked }))
                }
                ariaLabel={t("models.field.streamingEnabled")}
              />

              <Field
                label={t("models.field.extraBody")}
                hint={t("models.field.extraBodyHint")}
              >
                <Textarea
                  value={form.extraBody}
                  onChange={setField("extraBody")}
                  placeholder='{"key": "value"}'
                  rows={3}
                />
              </Field>

              <Field
                label={t("models.field.customHeaders")}
                hint={t("models.field.customHeadersHint")}
              >
                <Textarea
                  value={form.customHeaders}
                  onChange={setField("customHeaders")}
                  placeholder='{"X-Source": "coding-plan"}'
                  rows={3}
                />
              </Field>
            </AdvancedSection>

            {serverError && (
              <p className="text-destructive bg-destructive/10 rounded-md px-3 py-2 text-sm">
                {serverError}
              </p>
            )}
          </div>
        </div>

        <SheetFooter className="border-t-muted border-t px-6 py-4">
          {isDirty && (
            <ConfigChangeNotice
              kind="save"
              title={t("common.saveChangesTitle")}
              description={t("models.unsavedPrompt")}
            />
          )}
          <Button variant="ghost" onClick={onClose} disabled={saving}>
            {t("common.cancel")}
          </Button>
          <Button
            onClick={onSave}
            disabled={
              !isDirty || saving || modelValidation?.level === "error"
            }
          >
            {saving && <IconLoader2 className="size-4 animate-spin" />}
            {confirmLabel}
          </Button>
        </SheetFooter>
      </SheetContent>

      {children}
    </Sheet>
  )
}
