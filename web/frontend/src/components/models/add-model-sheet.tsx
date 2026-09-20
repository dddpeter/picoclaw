import { useEffect, useState } from "react"
import { useTranslation } from "react-i18next"

import {
  type ModelProviderOption,
  addModel,
  setDefaultModel,
} from "@/api/models"
import { maskedSecretPlaceholder } from "@/components/secret-placeholder"
import { Field } from "@/components/shared-form"
import { Input } from "@/components/ui/input"
import { showSaveSuccessOrRestartToast } from "@/lib/restart-required"
import { refreshGatewayState } from "@/store/gateway"

import { FetchModelsDialog } from "./fetch-models-dialog"
import { ModelFormSheet } from "./model-form-sheet"
import {
  EMPTY_MODEL_FORM,
  useModelFormState,
} from "./model-form-state"
import { TestModelDialog } from "./test-model-dialog"

interface AddModelSheetProps {
  open: boolean
  onClose: () => void
  onSaved: () => void
  onMutationStarted?: () => void
  onMutationSettled?: () => void
  existingModelNames: string[]
  providerOptions?: ModelProviderOption[]
}

export function AddModelSheet({
  open,
  onClose,
  onSaved,
  onMutationStarted,
  onMutationSettled,
  existingModelNames,
  providerOptions,
}: AddModelSheetProps) {
  const { t } = useTranslation()
  const state = useModelFormState({ providerOptions })
  const { reset } = state
  const [modelName, setModelName] = useState("")
  const [modelNameError, setModelNameError] = useState("")
  const [saving, setSaving] = useState(false)

  const isDirty =
    modelName !== "" ||
    state.setAsDefault ||
    JSON.stringify(state.form) !== JSON.stringify(EMPTY_MODEL_FORM)

  useEffect(() => {
    if (open) {
      reset(EMPTY_MODEL_FORM, false)
      setModelName("")
      setModelNameError("")
    }
  }, [open, reset])

  const apiKeyPlaceholder = maskedSecretPlaceholder(
    state.form.apiKey,
    t("models.field.apiKeyPlaceholder"),
  )

  const validate = (): boolean => {
    let ok = state.validateCommon()
    const trimmed = modelName.trim()
    if (!trimmed) {
      setModelNameError(t("models.add.errorRequired"))
      ok = false
    } else if (existingModelNames.some((name) => name.trim() === trimmed)) {
      setModelNameError(t("models.add.errorDuplicateModelName"))
      ok = false
    }
    return ok
  }

  const handleSave = async () => {
    if (!validate()) return

    const parsed = state.parseJsonFields()
    if (!parsed) return

    onMutationStarted?.()
    setSaving(true)
    state.setServerError("")
    try {
      const newModelName = modelName.trim()
      await addModel({
        model_name: newModelName,
        provider: state.canonicalProvider || undefined,
        model: state.form.modelId.trim(),
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
        streaming: state.form.streamingEnabled ? { enabled: true } : undefined,
        extra_body: parsed.extraBody,
        custom_headers: parsed.customHeaders,
      })
      if (state.setAsDefault) {
        await setDefaultModel(newModelName)
      }
      const gateway = await refreshGatewayState({ force: true })
      showSaveSuccessOrRestartToast(
        t,
        t("models.add.saveSuccess"),
        newModelName,
        gateway?.restartRequired === true,
      )
      onSaved()
      onClose()
    } catch (e) {
      state.setServerError(
        e instanceof Error ? e.message : t("models.add.saveError"),
      )
    } finally {
      setSaving(false)
      onMutationSettled?.()
    }
  }

  return (
    <ModelFormSheet
      open={open}
      onClose={onClose}
      saving={saving}
      title={t("models.add.title")}
      description={t("models.add.description")}
      state={state}
      providerOptions={providerOptions}
      isDirty={isDirty}
      confirmLabel={t("models.add.confirm")}
      onSave={handleSave}
      providerFieldError={state.fieldErrors.provider}
      modelFieldError={state.fieldErrors.model}
      showSelectProviderFirstHint
      providerComboboxAllowCreate
      testDisabled={!state.form.provider || !state.form.modelId}
      apiKeyPlaceholder={apiKeyPlaceholder}
      defaultOnSaveHint={
        !state.defaultModelAllowed && state.form.provider
          ? t("models.defaultOnSave.unsupportedProvider")
          : t("models.defaultOnSave.description")
      }
      headerFields={
        <Field
          label={t("models.add.modelName")}
          hint={t("models.add.modelNameHint")}
        >
          <Input
            value={modelName}
            onChange={(e) => {
              setModelName(e.target.value)
              if (modelNameError) setModelNameError("")
            }}
            placeholder={t("models.add.modelNamePlaceholder")}
            aria-invalid={!!modelNameError}
          />
          {modelNameError && (
            <p className="text-destructive text-xs">{modelNameError}</p>
          )}
        </Field>
      }
    >
      <FetchModelsDialog
        open={state.fetchOpen}
        onClose={() => state.setFetchOpen(false)}
        onFill={state.handleFetchFill}
        provider={state.canonicalProvider}
        apiKey={state.form.apiKey}
        apiBase={state.effectiveApiBase}
        backendOptions={providerOptions}
      />

      <TestModelDialog
        model={null}
        open={state.testOpen}
        onClose={() => state.setTestOpen(false)}
        inlineParams={{
          provider: state.canonicalProvider,
          model: state.form.modelId,
          apiBase: state.effectiveApiBase,
          apiKey: state.form.apiKey,
          authMethod: state.effectiveAuthMethod,
        }}
      />
    </ModelFormSheet>
  )
}
