import { useCallback, useEffect, useMemo, useState } from "react"
import { useTranslation } from "react-i18next"

import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { cn } from "@/lib/utils"
import { patchAppConfig } from "@/api/channels"
import {
  addModel,
  getModels,
  setDefaultModel,
  type ModelInfo,
  type ModelProviderOption,
} from "@/api/models"

const WIZARD_STORAGE_KEY = "picoclaw-setup-wizard-dismissed"

/** True when the wizard should auto-open: never dismissed AND no usable model. */
export function shouldAutoOpenWizard(
  models: ModelInfo[] | undefined,
  dismissed: boolean,
): boolean {
  if (dismissed) return false
  if (!models) return false // still loading — don't flash the wizard
  return models.every((m) => m.status !== "available")
}

export function getWizardDismissed(): boolean {
  return localStorage.getItem(WIZARD_STORAGE_KEY) === "1"
}

export function setWizardDismissed(v: boolean) {
  localStorage.setItem(WIZARD_STORAGE_KEY, v ? "1" : "0")
}

type WizardStep = "provider" | "key" | "testing" | "done" | "error"

export interface SetupWizardResult {
  providerAdded: boolean
  defaultModel?: string
  denyProfile?: string
}

interface SetupWizardProps {
  open: boolean
  onClose: (result?: SetupWizardResult) => void
}

const recommendedList = [
  "zhipu",
  "deepseek",
  "moonshot",
  "openai",
  "anthropic",
  "openrouter",
]

function providerKeyURL(p: ModelProviderOption): string {
  const urls: Record<string, string> = {
    zhipu: "https://open.bigmodel.cn/usercenter/apikeys",
    deepseek: "https://platform.deepseek.com/api_keys",
    moonshot: "https://platform.moonshot.cn/console/api-keys",
    openai: "https://platform.openai.com/api-keys",
    anthropic: "https://console.anthropic.com/settings/keys",
    openrouter: "https://openrouter.ai/keys",
  }
  return urls[p.id] || `https://${p.domain || p.id + ".com"}`
}

export function SetupWizard({ open, onClose }: SetupWizardProps) {
  const { t } = useTranslation()
  const [step, setStep] = useState<WizardStep>("provider")
  const [providers, setProviders] = useState<ModelProviderOption[]>([])
  const [selected, setSelected] = useState<ModelProviderOption | null>(null)
  const [apiKey, setApiKey] = useState("")
  const [modelName, setModelName] = useState("")
  const [denyProfile, setDenyProfile] = useState<"open" | "strict">("open")
  const [testError, setTestError] = useState("")
  const [savedEntryName, setSavedEntryName] = useState("")
  const [busy, setBusy] = useState(false)

  useEffect(() => {
    if (!open) return
    // Fresh state on every open (review: don't leak apiKey/step across runs)
    setStep("provider")
    setSelected(null)
    setApiKey("")
    setModelName("")
    setDenyProfile("open")
    setTestError("")
    setBusy(false)
    getModels()
      .then((data) => setProviders(data.provider_options || []))
      .catch(() => setProviders([]))
  }, [open])

  const handlePickProvider = (p: ModelProviderOption) => {
    setSelected(p)
    setModelName("")
    setApiKey("")
    setStep("key")
  }

  const handleTestAndSave = useCallback(async () => {
    if (!selected) return
    setBusy(true)
    setTestError("")
    setStep("testing")
    try {
      const entryName = modelName || `${selected.id}-default`
      setSavedEntryName(entryName)
      // 1) add the model entry (backend probes availability on add)
      await addModel({
        model_name: entryName,
        provider: selected.id,
        model: modelName || "",
        api_base: selected.default_api_base,
        api_key: apiKey || undefined,
        enabled: true,
      })
      // 2) set as default
      await setDefaultModel(entryName)
      // 3) persist the chosen security profile (RFC 7396 merge-patch)
      await patchAppConfig({
        tools: { exec: { deny_profile: denyProfile } },
      })
      setStep("done")
    } catch (e) {
      setTestError(e instanceof Error ? e.message : String(e))
      setStep("error")
    } finally {
      setBusy(false)
    }
  }, [selected, apiKey, modelName, denyProfile])

  const recommendedFirst = useMemo(() => {
    return [...providers]
      .filter((p) => p.create_allowed !== false)
      .sort((a, b) => {
        const ai = recommendedList.indexOf(a.id)
        const bi = recommendedList.indexOf(b.id)
        return (ai === -1 ? 99 : ai) - (bi === -1 ? 99 : bi)
      })
  }, [providers])

  // Escape closes the wizard from any step except while a save is in flight.
  useEffect(() => {
    if (!open || step === "testing") return
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") onClose()
    }
    window.addEventListener("keydown", onKey)
    return () => window.removeEventListener("keydown", onKey)
  }, [open, step, onClose])

  if (!open) return null

  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center bg-black/50 backdrop-blur-sm"
      role="dialog"
      aria-modal="true"
      aria-label={t("wizard.title", { defaultValue: "Welcome to PicoClaw 🦞" })}
    >
      <div className="bg-card w-full max-w-lg rounded-2xl border p-6 shadow-2xl">
        {step === "provider" && (
          <>
            <h2 className="text-lg font-semibold">
              {t("wizard.title", { defaultValue: "Welcome to PicoClaw 🦞" })}
            </h2>
            <p className="text-muted-foreground mt-1 text-sm">
              {t("wizard.pickProvider", {
                defaultValue:
                  "Pick a provider to get started (you can add more later)",
              })}
            </p>
            <div className="mt-4 grid max-h-80 grid-cols-2 gap-2 overflow-y-auto pr-1">
              {recommendedFirst.map((p) => (
                <button
                  key={p.id}
                  onClick={() => handlePickProvider(p)}
                  className="hover:bg-accent flex items-center gap-2 rounded-lg border p-3 text-left transition-colors"
                >
                  <span className="text-sm font-medium">
                    {p.display_name || p.id}
                  </span>
                  {recommendedList.includes(p.id) && (
                    <span className="bg-primary/10 text-primary rounded-full px-1.5 py-0.5 text-[10px]">
                      ★
                    </span>
                  )}
                </button>
              ))}
            </div>
            <div className="mt-4 flex justify-end">
              <Button variant="ghost" onClick={() => onClose()}>
                {t("wizard.skip", { defaultValue: "Skip for now" })}
              </Button>
            </div>
          </>
        )}

        {step === "key" && selected && (
          <>
            <div className="flex items-start justify-between">
              <h2 className="text-lg font-semibold">
                {selected.display_name || selected.id}
              </h2>
              <button
                type="button"
                aria-label={t("wizard.skip", { defaultValue: "Skip for now" })}
                className="text-muted-foreground hover:text-foreground -mt-1 text-xl leading-none"
                onClick={() => onClose()}
              >
                ×
              </button>
            </div>
            <div className="mt-4 space-y-3">
              <div>
                <label className="text-sm font-medium" htmlFor="wizard-api-key">
                  {t("wizard.apiKey", { defaultValue: "API Key" })}
                </label>
                <Input
                  id="wizard-api-key"
                  type="password"
                  value={apiKey}
                  onChange={(e) => setApiKey(e.target.value)}
                  placeholder={
                    selected.empty_api_key_allowed
                      ? t("wizard.apiKeyOptional", {
                          defaultValue: "optional for this provider",
                        })
                      : "sk-..."
                  }
                  className="mt-1"
                />
                <a
                  href={providerKeyURL(selected)}
                  target="_blank"
                  rel="noreferrer"
                  className="text-primary mt-1 inline-block text-xs underline"
                >
                  {t("wizard.getKey", { defaultValue: "Get a key →" })}
                </a>
              </div>
              <div>
                <label className="text-sm font-medium" htmlFor="wizard-model">
                  {t("wizard.modelName", {
                    defaultValue: "Model ID (optional)",
                  })}
                </label>
                <Input
                  id="wizard-model"
                  value={modelName}
                  onChange={(e) => setModelName(e.target.value)}
                  placeholder="e.g. glm-4.7 / deepseek-chat"
                  className="mt-1"
                />
              </div>
              <div>
                <span className="text-sm font-medium">
                  {t("wizard.security", { defaultValue: "Security level" })}
                </span>
                <div className="mt-1 grid grid-cols-2 gap-2">
                  {(["open", "strict"] as const).map((p) => (
                    <button
                      key={p}
                      type="button"
                      onClick={() => setDenyProfile(p)}
                      className={cn(
                        "rounded-lg border p-3 text-left text-xs transition-colors",
                        denyProfile === p
                          ? "border-primary bg-primary/10"
                          : "hover:bg-accent",
                      )}
                    >
                      <div className="font-semibold">
                        {t(`wizard.profile.name.${p}`, p)}
                        {p === "open" && (
                          <>
                            {" ★"}
                            <span className="sr-only">
                              {t("pages.config.recommended")}
                            </span>
                          </>
                        )}
                      </div>
                      <div className="text-muted-foreground mt-1">
                        {t(`wizard.profile.desc.${p}`)}
                      </div>
                    </button>
                  ))}
                </div>
              </div>
            </div>
            <div className="mt-5 flex justify-between">
              <Button variant="outline" onClick={() => setStep("provider")}>
                {t("common.back")}
              </Button>
              <Button onClick={handleTestAndSave} disabled={busy}>
                {t("wizard.testAndSave", { defaultValue: "Test & Save" })}
              </Button>
            </div>
          </>
        )}

        {step === "testing" && (
          <div className="flex flex-col items-center gap-3 py-10">
            <div className="size-8 animate-spin rounded-full border-2 border-current border-t-transparent" />
            <p className="text-sm">
              {t("wizard.testing", { defaultValue: "Testing connection…" })}
            </p>
          </div>
        )}

        {step === "error" && (
          <div className="flex flex-col items-center gap-3 py-8">
            <p className="text-sm text-red-500">{testError}</p>
            <Button onClick={() => setStep("key")}>
              {t("common.retry", { defaultValue: "Retry" })}
            </Button>
          </div>
        )}

        {step === "done" && (
          <div className="flex flex-col items-center gap-3 py-8">
            <div className="text-3xl">🎉</div>
            <p className="text-sm">
              {t("wizard.done", {
                defaultValue: "You're all set! Try sending a message.",
              })}
            </p>
            <Button
              onClick={() =>
                onClose({
                  providerAdded: true,
                  defaultModel: savedEntryName,
                  denyProfile,
                })
              }
            >
              {t("wizard.startChatting", {
                defaultValue: "Start chatting",
              })}
            </Button>
          </div>
        )}
      </div>
    </div>
  )
}
