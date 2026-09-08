import { IconChevronDown } from "@tabler/icons-react"
import { useState } from "react"

import type { ModelInfo } from "@/api/models"

import { ModelCard } from "./model-card"
import { ProviderIcon } from "./provider-icon"
import type { ProviderCatalogEntry } from "./provider-registry"

interface ProviderSectionProps {
  provider: Pick<ProviderCatalogEntry, "key" | "label" | "iconSlug" | "domain">
  models: ModelInfo[]
  onEdit: (model: ModelInfo) => void
  onSetDefault: (model: ModelInfo) => void
  onToggleFallback: (model: ModelInfo) => void
  onDelete: (model: ModelInfo) => void
  fallbackChain: string[]
  defaultModelName: string
  defaultModelEntryCount: number
  defaultChainAllowedModelNames: Set<string>
  fallbackDefaultConflictModelNames: Set<string>
}

export function ProviderSection({
  provider,
  models,
  onEdit,
  onSetDefault,
  onToggleFallback,
  onDelete,
  fallbackChain,
  defaultModelName,
  defaultModelEntryCount,
  defaultChainAllowedModelNames,
  fallbackDefaultConflictModelNames,
}: ProviderSectionProps) {
  const [open, setOpen] = useState(true)

  // Stable per-provider accent hue so sections feel distinct, not monochrome.
  const HUES: Record<string, string> = {
    openai: "oklch(0.55 0.13 255)",
    anthropic: "oklch(0.6 0.15 40)",
    google: "oklch(0.62 0.14 250)",
    deepseek: "oklch(0.55 0.16 262)",
    moonshot: "oklch(0.6 0.15 330)",
    zhipu: "oklch(0.58 0.15 200)",
    alibaba: "oklch(0.6 0.16 25)",
    xai: "oklch(0.6 0.15 300)",
    mistral: "oklch(0.6 0.15 160)",
  }
  const accent = HUES[provider.key] ?? "var(--primary)"
  const iconBg = `color-mix(in oklab, ${accent} 14%, transparent)`
  const hairline = `color-mix(in oklab, ${accent} 30%, var(--border))`

  return (
    <section className="my-6">
      <button
        type="button"
        onClick={() => setOpen((v) => !v)}
        className="group/head hover:bg-accent/40 mb-3 flex w-full items-center gap-2.5 rounded-lg border border-border/50 bg-card px-3 py-2 text-left transition-colors"
        aria-expanded={open}
        style={{ borderColor: hairline }}
      >
        <span
          className="flex size-7 shrink-0 items-center justify-center rounded-md"
          style={{ backgroundColor: iconBg, color: accent }}
        >
          <ProviderIcon provider={provider} />
        </span>
        <span className="text-foreground text-sm font-semibold">
          {provider.label}
        </span>
        <span className="bg-muted text-muted-foreground shrink-0 rounded-full px-2 py-0.5 text-[10px] leading-none font-medium tabular-nums">
          {models.length}
        </span>
        <span className="border-border/50 mx-1 flex-1 border-t" />
        <span className="flex shrink-0 justify-end">
          <IconChevronDown
            className={[
              "text-muted-foreground size-4 transition-transform duration-200",
              open ? "rotate-180" : "",
            ].join(" ")}
          />
        </span>
      </button>

      {open && (
        <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-3">
          {models.map((model) => (
            <ModelCard
              key={model.index}
              model={model}
              onEdit={onEdit}
              onSetDefault={onSetDefault}
              onToggleFallback={onToggleFallback}
              onDelete={onDelete}
              inFallbackChain={fallbackChain.includes(model.model_name)}
              isDefault={defaultModelName === model.model_name}
              deleteDisabled={
                defaultModelName === model.model_name &&
                defaultModelEntryCount <= 1
              }
              defaultChainAllowed={defaultChainAllowedModelNames.has(
                model.model_name,
              )}
              fallbackDefaultConflict={fallbackDefaultConflictModelNames.has(
                model.model_name,
              )}
            />
          ))}
        </div>
      )}
    </section>
  )
}
