import { memo } from "react"
import { useTranslation } from "react-i18next"

import type { ModelInfo } from "@/api/models"
import { LOBE_SCOPE_TOKENS } from "@/chat-pi/tokens"
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectLabel,
  SelectSeparator,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { cn } from "@/lib/utils"

interface ModelSelectorProps {
  /** The configured default model (config.json) — shown when the session
   *  has no model of its own. */
  defaultModelName: string
  /** The model bound to the current session ("" = follow the default). */
  sessionModel?: string
  apiKeyModels: ModelInfo[]
  oauthModels: ModelInfo[]
  localModels: ModelInfo[]
  disabled?: boolean
  onValueChange: (modelName: string) => void
}

export const ModelSelector = memo(function ModelSelector({
  defaultModelName,
  sessionModel,
  apiKeyModels,
  oauthModels,
  localModels,
  disabled = false,
  onValueChange,
}: ModelSelectorProps) {
  const { t } = useTranslation()
  // The session model wins when set; otherwise the configured default shows
  // (the session follows the config until the user picks another model).
  const selectedModel = sessionModel || defaultModelName
  const rawDefaultModel =
    defaultModelName &&
    ![...apiKeyModels, ...oauthModels, ...localModels].some(
      (model) => model.model_name === defaultModelName,
    )
      ? defaultModelName
      : ""
  // A session model that is no longer in the model list (removed from the
  // config) still shows, so the UI never silently lies about the session's
  // model.
  const rawSessionModel =
    sessionModel &&
    ![...apiKeyModels, ...oauthModels, ...localModels].some(
      (model) => model.model_name === sessionModel,
    )
      ? sessionModel
      : ""

  return (
    <Select
      value={selectedModel}
      onValueChange={onValueChange}
      disabled={disabled}
    >
      <SelectTrigger
        size="sm"
        title={
          sessionModel
            ? `${t("chat.sessionModel")} · ${t("chat.sessionModelTitle")}`
            : undefined
        }
        className={cn(
          LOBE_SCOPE_TOKENS,
          "focus-visible:ring-ring/30 h-8 max-w-[180px] min-w-[96px] rounded-lg border-0 bg-[var(--lobe-fill-2)] px-2.5 text-[13px] font-medium text-[var(--lobe-text-2)] shadow-none hover:bg-[var(--lobe-fill-3)] hover:text-[var(--lobe-text)] focus-visible:ring-2 sm:max-w-[240px]",
        )}
      >
        <SelectValue placeholder={t("chat.noModel")} />
      </SelectTrigger>
      <SelectContent
        position="popper"
        align="start"
        className={cn(
          LOBE_SCOPE_TOKENS,
          "rounded-lg border border-[var(--lobe-border-2)] bg-[var(--lobe-bg-elevated)] p-1 text-[var(--lobe-text)] shadow-lg ring-0",
        )}
      >
        {rawSessionModel && (
          <SelectItem
            value={rawSessionModel}
            className="rounded-md px-2 py-1.5 text-[13px] focus:bg-[var(--lobe-fill-2)] focus:text-[var(--lobe-text)]"
          >
            {rawSessionModel}
          </SelectItem>
        )}
        {rawDefaultModel &&
          rawDefaultModel !== rawSessionModel &&
          (apiKeyModels.length > 0 ||
            oauthModels.length > 0 ||
            localModels.length > 0) && (
            <SelectSeparator className="bg-[var(--lobe-border-2)]" />
          )}
        {rawDefaultModel && rawDefaultModel !== rawSessionModel && (
          <SelectItem
            value={rawDefaultModel}
            className="rounded-md px-2 py-1.5 text-[13px] focus:bg-[var(--lobe-fill-2)] focus:text-[var(--lobe-text)]"
          >
            {rawDefaultModel}
          </SelectItem>
        )}
        {rawDefaultModel &&
          rawDefaultModel !== rawSessionModel &&
          (apiKeyModels.length > 0 ||
            oauthModels.length > 0 ||
            localModels.length > 0) && (
            <SelectSeparator className="bg-[var(--lobe-border-2)]" />
          )}
        {apiKeyModels.length > 0 && (
          <SelectGroup className="p-0.5">
            <SelectLabel className="px-2 py-1 text-[11px] font-semibold tracking-wide text-[var(--lobe-text-desc)] uppercase">
              {t("chat.modelGroup.apikey")}
            </SelectLabel>
            {apiKeyModels.map((model) => (
              <SelectItem
                key={model.index}
                value={model.model_name}
                className="rounded-md px-2 py-1.5 text-[13px] focus:bg-[var(--lobe-fill-2)] focus:text-[var(--lobe-text)]"
              >
                {model.model_name}
              </SelectItem>
            ))}
          </SelectGroup>
        )}
        {apiKeyModels.length > 0 &&
          (oauthModels.length > 0 || localModels.length > 0) && (
            <SelectSeparator className="bg-[var(--lobe-border-2)]" />
          )}

        {oauthModels.length > 0 && (
          <SelectGroup className="p-0.5">
            <SelectLabel className="px-2 py-1 text-[11px] font-semibold tracking-wide text-[var(--lobe-text-desc)] uppercase">
              {t("chat.modelGroup.oauth")}
            </SelectLabel>
            {oauthModels.map((model) => (
              <SelectItem
                key={model.index}
                value={model.model_name}
                className="rounded-md px-2 py-1.5 text-[13px] focus:bg-[var(--lobe-fill-2)] focus:text-[var(--lobe-text)]"
              >
                {model.model_name}
              </SelectItem>
            ))}
          </SelectGroup>
        )}
        {oauthModels.length > 0 &&
          (localModels.length > 0 || apiKeyModels.length > 0) && (
            <SelectSeparator className="bg-[var(--lobe-border-2)]" />
          )}

        {localModels.length > 0 && (
          <SelectGroup className="p-0.5">
            <SelectLabel className="px-2 py-1 text-[11px] font-semibold tracking-wide text-[var(--lobe-text-desc)] uppercase">
              {t("chat.modelGroup.local")}
            </SelectLabel>
            {localModels.map((model) => (
              <SelectItem
                key={model.index}
                value={model.model_name}
                className="rounded-md px-2 py-1.5 text-[13px] focus:bg-[var(--lobe-fill-2)] focus:text-[var(--lobe-text)]"
              >
                {model.model_name}
              </SelectItem>
            ))}
          </SelectGroup>
        )}
      </SelectContent>
    </Select>
  )
})
