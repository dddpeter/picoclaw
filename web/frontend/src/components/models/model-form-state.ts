/**
 * Shared model form state for Add/EditModelSheet.
 *
 * Owns everything the two sheets have in common: the provider/model form
 * fields, debounced model-id validation, catalog model badges, fetched model
 * badges, set-as-default toggle and per-field validation errors.
 * Sheets keep only what genuinely differs: entry fields (add has a model
 * display name), save payload/endpoint and dirty tracking.
 */
import { useCallback, useEffect, useRef, useState } from "react"
import { useTranslation } from "react-i18next"

import {
  type ModelInfo,
  type ModelProviderOption,
  getCatalogs,
} from "@/api/models"

import {
  getEffectiveAPIBase,
  getSubmittedAPIBase,
  normalizeApiBase,
} from "./model-provider-form-shared"
import { type FieldValidation, validateModelField } from "./model-validation"
import {
  getCanonicalProviderKey,
  getProviderCatalogEntry,
  getProviderCatalogMap,
  getProviderDefaultAPIBase,
  getProviderDefaultAuthMethod,
  isProviderAuthMethodLocked,
} from "./provider-registry"

export interface ModelFormState {
  provider: string
  modelId: string
  apiKey: string
  apiBase: string
  proxy: string
  authMethod: string
  connectMode: string
  workspace: string
  rpm: string
  maxTokensField: string
  requestTimeout: string
  thinkingLevel: string
  toolSchemaTransform: string
  streamingEnabled: boolean
  extraBody: string
  customHeaders: string
}

export const EMPTY_MODEL_FORM: ModelFormState = {
  provider: "",
  modelId: "",
  apiKey: "",
  apiBase: "",
  proxy: "",
  authMethod: "",
  connectMode: "",
  workspace: "",
  rpm: "",
  maxTokensField: "",
  requestTimeout: "",
  thinkingLevel: "",
  toolSchemaTransform: "",
  streamingEnabled: false,
  extraBody: "",
  customHeaders: "",
}

export function buildModelFormFromModel(model: ModelInfo): ModelFormState {
  return {
    provider: getCanonicalProviderKey(model.provider),
    modelId: model.model,
    apiKey: "",
    apiBase: model.api_base ?? "",
    proxy: model.proxy ?? "",
    authMethod: model.auth_method ?? "",
    connectMode: model.connect_mode ?? "",
    workspace: model.workspace ?? "",
    rpm: model.rpm ? String(model.rpm) : "",
    maxTokensField: model.max_tokens_field ?? "",
    requestTimeout: model.request_timeout ? String(model.request_timeout) : "",
    thinkingLevel: model.thinking_level ?? "",
    toolSchemaTransform: model.tool_schema_transform ?? "",
    streamingEnabled: model.streaming?.enabled === true,
    extraBody: model.extra_body
      ? JSON.stringify(model.extra_body, null, 2)
      : "",
    customHeaders: model.custom_headers
      ? JSON.stringify(model.custom_headers, null, 2)
      : "",
  }
}

export interface ModelFieldErrors {
  provider?: string
  model?: string
}

export function useModelFormState(opts: {
  providerOptions?: ModelProviderOption[]
}) {
  const { providerOptions } = opts
  const { t } = useTranslation()
  const [form, setForm] = useState<ModelFormState>(EMPTY_MODEL_FORM)
  const [setAsDefault, setSetAsDefault] = useState(false)
  const [fieldErrors, setFieldErrors] = useState<ModelFieldErrors>({})
  const [serverError, setServerError] = useState("")
  const [modelValidation, setModelValidation] =
    useState<FieldValidation | null>(null)
  const [fetchOpen, setFetchOpen] = useState(false)
  const [testOpen, setTestOpen] = useState(false)
  const [fetchedModels, setFetchedModels] = useState<string[]>([])
  const [catalogModels, setCatalogModels] = useState<string[]>([])
  const debounceRef = useRef<ReturnType<typeof setTimeout>>(undefined)

  const providerMap = getProviderCatalogMap(providerOptions)

  const canonicalProvider = getCanonicalProviderKey(
    form.provider,
    providerOptions,
  )
  const providerDef = canonicalProvider
    ? providerMap.get(canonicalProvider)
    : undefined
  const commonModels = providerDef?.commonModels || []
  const authMethodLocked = isProviderAuthMethodLocked(
    form.provider,
    providerOptions,
  )
  const defaultAuthMethod = getProviderDefaultAuthMethod(
    form.provider,
    providerOptions,
  )
  const effectiveAuthMethod = (
    authMethodLocked ? defaultAuthMethod : form.authMethod
  )
    .trim()
    .toLowerCase()
  const isOAuth = effectiveAuthMethod === "oauth"
  const defaultModelAllowed = providerDef?.defaultModelAllowed === true
  const apiBasePlaceholder =
    getProviderDefaultAPIBase(form.provider, providerOptions) ||
    "https://api.example.com/v1"
  const effectiveApiBase = getEffectiveAPIBase(
    form.provider,
    form.apiBase,
    providerOptions,
  )
  const submittedApiBase = getSubmittedAPIBase(form.apiBase)

  const reset = useCallback(
    (initial: ModelFormState, initialSetAsDefault: boolean) => {
      setForm(initial)
      setSetAsDefault(initialSetAsDefault)
      setFieldErrors({})
      setServerError("")
      setModelValidation(null)
      setFetchedModels([])
      setCatalogModels([])
    },
    [],
  )

  // Load catalog models matching the current provider + api base
  useEffect(() => {
    const providerKey = getCanonicalProviderKey(form.provider, providerOptions)
    const apiBase = getEffectiveAPIBase(
      form.provider,
      form.apiBase,
      providerOptions,
    )
    if (!form.provider.trim()) {
      setCatalogModels([])
      return
    }
    let cancelled = false
    getCatalogs()
      .then((res) => {
        if (cancelled) return
        const matched = (res.entries || []).filter((e) => {
          const ep = getCanonicalProviderKey(e.provider, providerOptions)
          const eb = (e.api_base ?? "").trim().replace(/\/+$/, "")
          return ep === providerKey && eb === apiBase
        })
        const ids = matched.flatMap((e) => e.models.map((m) => m.id))
        setCatalogModels([...new Set(ids)])
      })
      .catch(() => {})
    return () => {
      cancelled = true
    }
  }, [form.provider, form.apiBase, providerOptions])

  const debouncedValidateModel = useCallback(
    (value: string, provider: string) => {
      if (debounceRef.current) clearTimeout(debounceRef.current)
      debounceRef.current = setTimeout(() => {
        const result = validateModelField(
          value,
          provider || undefined,
          providerOptions,
        )
        setModelValidation(result)
      }, 300)
    },
    [providerOptions],
  )

  const setField =
    (key: keyof ModelFormState) =>
    (
      e: React.ChangeEvent<HTMLInputElement | HTMLTextAreaElement>,
    ): void => {
      setForm((f) => ({ ...f, [key]: e.target.value }))
      if (fieldErrors[key as keyof ModelFieldErrors]) {
        setFieldErrors((prev) => ({ ...prev, [key]: undefined }))
      }
    }

  const handleModelChange = (e: React.ChangeEvent<HTMLInputElement>) => {
    const value = e.target.value
    setForm((f) => ({ ...f, modelId: value }))
    if (fieldErrors.model) {
      setFieldErrors((prev) => ({ ...prev, model: undefined }))
    }
    debouncedValidateModel(value, form.provider)
  }

  const handleProviderChange = (provider: string) => {
    setForm((f) => {
      const previousOption = getProviderCatalogEntry(
        f.provider,
        providerOptions,
      )
      const nextOption = getProviderCatalogEntry(provider, providerOptions)
      const previousDefaultBase = normalizeApiBase(
        getProviderDefaultAPIBase(f.provider, providerOptions),
      )
      const nextDefaultBase = normalizeApiBase(
        getProviderDefaultAPIBase(provider, providerOptions),
      )
      const currentApiBase = normalizeApiBase(f.apiBase)
      let authMethod = f.authMethod
      let apiBase = f.apiBase
      if (nextOption?.authMethodLocked) {
        authMethod = nextOption.defaultAuthMethod ?? ""
      } else if (
        previousOption?.authMethodLocked &&
        f.authMethod === (previousOption.defaultAuthMethod ?? "")
      ) {
        authMethod = ""
      }
      if (
        currentApiBase &&
        previousDefaultBase &&
        currentApiBase === previousDefaultBase &&
        currentApiBase !== nextDefaultBase
      ) {
        apiBase = ""
      }
      return {
        ...f,
        provider: getCanonicalProviderKey(provider, providerOptions),
        apiBase,
        authMethod,
      }
    })
    // Re-validate model with new provider context
    if (form.modelId) {
      debouncedValidateModel(form.modelId, provider)
    }
    // Clear setAsDefault if the new provider doesn't support being default
    const allowed =
      getProviderCatalogEntry(provider, providerOptions)?.defaultModelAllowed ??
      false
    if (!allowed) {
      setSetAsDefault(false)
    }
    if (fieldErrors.provider) {
      setFieldErrors((prev) => ({ ...prev, provider: undefined }))
    }
  }

  const applyFix = () => {
    if (modelValidation?.fix) {
      setForm((f) => ({ ...f, modelId: modelValidation.fix! }))
      setModelValidation(null)
    }
  }

  const handleCommonModel = (modelId: string) => {
    setForm((f) => ({ ...f, modelId }))
    setModelValidation(null)
    if (fieldErrors.model) {
      setFieldErrors((prev) => ({ ...prev, model: undefined }))
    }
  }

  const handleFetchFill = (models: string[]) => {
    setFetchedModels(models)
    if (models.length >= 1) {
      setForm((f) => ({ ...f, modelId: models[0] }))
      setModelValidation(null)
      if (fieldErrors.model) {
        setFieldErrors((prev) => ({ ...prev, model: undefined }))
      }
    }
  }

  /**
   * Parse the extraBody / customHeaders JSON textareas.
   * Returns null (and surfaces a server error) when either is invalid JSON.
   */
  const parseJsonFields = (): {
    extraBody: Record<string, unknown>
    customHeaders: Record<string, string>
  } | null => {
    let extraBody: Record<string, unknown>
    let customHeaders: Record<string, string>
    try {
      extraBody = form.extraBody.trim()
        ? (JSON.parse(form.extraBody.trim()) as Record<string, unknown>)
        : {}
    } catch {
      setServerError(
        t("models.field.extraBody") + ": " + t("models.field.invalidJson"),
      )
      return null
    }
    try {
      customHeaders = form.customHeaders.trim()
        ? (JSON.parse(form.customHeaders.trim()) as Record<string, string>)
        : {}
    } catch {
      setServerError(
        t("models.field.customHeaders") +
          ": " +
          t("models.field.invalidJson"),
      )
      return null
    }
    return { extraBody, customHeaders }
  }

  /** Validate provider + model-id fields; sets fieldErrors and returns ok. */
  const validateCommon = (): boolean => {
    const errors: ModelFieldErrors = {}
    if (!providerDef) {
      errors.provider = t("models.field.providerInvalid")
    }
    if (!form.modelId.trim()) errors.model = t("models.add.errorRequired")
    if (modelValidation?.level === "error") {
      errors.model = t(
        modelValidation.messageKey,
        modelValidation.messageParams,
      )
    }
    setFieldErrors(errors)
    return !errors.provider && !errors.model
  }

  return {
    form,
    setForm,
    setAsDefault,
    setSetAsDefault,
    fieldErrors,
    serverError,
    setServerError,
    modelValidation,
    fetchOpen,
    setFetchOpen,
    testOpen,
    setTestOpen,
    fetchedModels,
    catalogModels,
    reset,
    setField,
    handleModelChange,
    handleProviderChange,
    applyFix,
    handleCommonModel,
    handleFetchFill,
    validateCommon,
    parseJsonFields,
    // derived values
    canonicalProvider,
    providerDef,
    commonModels,
    authMethodLocked,
    defaultAuthMethod,
    effectiveAuthMethod,
    isOAuth,
    defaultModelAllowed,
    apiBasePlaceholder,
    effectiveApiBase,
    submittedApiBase,
  }
}

export type ModelFormStateApi = ReturnType<typeof useModelFormState>
