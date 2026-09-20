import { useEffect, useState } from "react"
import { useTranslation } from "react-i18next"

import {
  type ModelInfo,
  type ModelProviderOption,
  type ModelReferenceRename,
  setDefaultModel,
  updateModel,
} from "@/api/models"
import { maskedSecretPlaceholder } from "@/components/secret-placeholder"
import { showSaveSuccessOrRestartToast } from "@/lib/restart-required"
import { refreshGatewayState } from "@/store/gateway"

import { FetchModelsDialog } from "./fetch-models-dialog"
import { ModelFormSheet } from "./model-form-sheet"
import {
  buildModelFormFromModel,
  useModelFormState,
} from "./model-form-state"
import { TestModelDialog } from "./test-model-dialog"

interface EditModelSheetProps {
  model: ModelInfo | null
  open: boolean
  onClose: () => void
  onUpdated: (referenceRename?: ModelReferenceRename) => void
  onSaved: (referenceRename?: ModelReferenceRename) => void
  onUpdateStarted: () => void
  onUpdateSettled: () => void
  providerOptions?: ModelProviderOption[]
}

export function EditModelSheet({
  model,
  open,
  onClose,
  onUpdated,
  onSaved,
  onUpdateStarted,
  onUpdateSettled,
  providerOptions,
}: EditModelSheetProps) {
  const { t } = useTranslation()
  const state = useModelFormState({ providerOptions })
  const { reset } = state
  const [saving, setSaving] = useState(false)

  const initialForm = model ? buildModelFormFromModel(model) : null
  const isDirty =
    model != null &&
    (JSON.stringify(state.form) !== JSON.stringify(initialForm) ||
      state.setAsDefault !== model.is_default)

  useEffect(() => {
    if (model) {
      reset(buildModelFormFromModel(model), model.is_default)
    }
  }, [model, providerOptions, reset])

  const hasSavedAPIKey = Boolean(model?.api_key)
  const apiKeyPlaceholder = hasSavedAPIKey
    ? maskedSecretPlaceholder(
        model?.api_key ?? "",
        t("models.field.apiKeyPlaceholderSet"),
      )
    : t("models.field.apiKeyPlaceholder")
  const apiKeyHint = hasSavedAPIKey ? t("models.edit.apiKeyHint") : undefined

  const handleSave = async () => {
    if (!model) return
    if (!state.validateCommon()) return

    const parsed = state.parseJsonFields()
    if (!parsed) return

    onUpdateStarted()
    setSaving(true)
    state.setServerError("")
    try {
      const modelId = state.form.modelId.trim()
      const streaming =
        model.streaming?.enabled === true || state.form.streamingEnabled
          ? { enabled: state.form.streamingEnabled }
          : undefined
      const response = await updateModel(model.index, {
        model_name: model.model_name,
        provider: state.canonicalProvider,
        model: modelId,
        api_base: state.submittedApiBase,
        api_key: state.form.apiKey.trim() || undefined,
        proxy: state.form.proxy.trim() || undefined,
        auth_method: state.authMethodLocked
          ? state.defaultAuthMethod || undefined
          : state.form.authMethod.trim() || undefined,
        connect_mode: state.form.connectMode.trim() || undefined,
        workspace: state.form.workspace.trim() || undefined,
        rpm: state.form.rpm ? Number(state.form.rpm) : undefined,
        max_tokens_field: state.form.maxTokensField.trim() || undefined,
        request_timeout: state.form.requestTimeout
          ? Number(state.form.requestTimeout)
          : undefined,
        thinking_level: state.form.thinkingLevel.trim() || undefined,
        tool_schema_transform: state.form.toolSchemaTransform.trim() || undefined,
        streaming,
        extra_body: parsed.extraBody,
        custom_headers: parsed.customHeaders,
      })
      onUpdated(response.reference_rename)
      if (state.setAsDefault && !model.is_default) {
        await setDefaultModel(model.model_name)
      }
      const gateway = await refreshGatewayState({ force: true })
      showSaveSuccessOrRestartToast(
        t,
        t("models.edit.saveSuccess"),
        model.model_name,
        gateway?.restartRequired === true,
      )
      onSaved()
      onClose()
    } catch (e) {
      state.setServerError(
        e instanceof Error ? e.message : t("models.edit.saveError"),
      )
    } finally {
      onUpdateSettled()
      setSaving(false)
    }
  }

  const providerFieldError =
    !state.providerDef && state.form.provider
      ? t("models.field.providerInvalid")
      : state.fieldErrors.provider

  return (
    <ModelFormSheet
      open={open}
      onClose={onClose}
      saving={saving}
      title={t("models.edit.title", { name: model?.model_name })}
      description={<span className="font-mono">{model?.model}</span>}
      state={state}
      providerOptions={providerOptions}
      isDirty={isDirty}
      confirmLabel={t("common.save")}
      onSave={handleSave}
      providerFieldError={providerFieldError}
      modelFieldError={state.fieldErrors.model}
      testDisabled={!model}
      apiKeyPlaceholder={apiKeyPlaceholder}
      apiKeyHint={apiKeyHint}
      defaultOnSaveHint={
        !state.defaultModelAllowed
          ? t("models.defaultOnSave.unsupportedProvider")
          : t("models.defaultOnSave.description")
      }
    >
      <TestModelDialog
        model={model}
        open={state.testOpen}
        onClose={() => state.setTestOpen(false)}
        inlineParams={{
          provider: state.canonicalProvider,
          model: state.form.modelId,
          apiBase: state.effectiveApiBase,
          apiKey: state.form.apiKey,
          authMethod: state.effectiveAuthMethod,
          modelIndex: model?.index,
        }}
      />

      <FetchModelsDialog
        open={state.fetchOpen}
        onClose={() => state.setFetchOpen(false)}
        onFill={state.handleFetchFill}
        provider={state.canonicalProvider}
        apiKey={state.form.apiKey}
        apiBase={state.effectiveApiBase}
        modelIndex={model?.index}
        backendOptions={providerOptions}
      />
    </ModelFormSheet>
  )
}
