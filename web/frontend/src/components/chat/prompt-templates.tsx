import {
  IconEdit,
  IconPlus,
  IconRotateClockwise,
  IconSend,
  IconTemplate,
  IconTrash,
} from "@tabler/icons-react"
import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useState,
  type ReactNode,
} from "react"
import { useTranslation } from "react-i18next"

import {
  getPromptTemplates,
  savePromptTemplates,
  type StoredPromptTemplate,
} from "@/api/prompt-templates"
import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { ScrollArea } from "@/components/ui/scroll-area"
import { Textarea } from "@/components/ui/textarea"
import { cn } from "@/lib/utils"

export interface PromptTemplate {
  id: string
  icon: string
  title: string
  desc: string
  prompt: string
  builtin?: boolean
}

interface BuiltinDef {
  id: string
  icon: string
  titleKey: string
  descKey: string
  promptKey: string
}

// Built-in templates mirror the Matt Pocock skills library popularized by
// pi-web-ui. Copy lives in i18n (tpl.sk.*); the gateway only stores the user
// delta on top of these defaults.
const BUILTIN_DEFS: BuiltinDef[] = [
  { id: "setup-matt-pocock-skills", icon: "🧰", titleKey: "tpl.sk.setup.title", descKey: "tpl.sk.setup.desc", promptKey: "tpl.sk.setup.prompt" },
  { id: "ask-matt", icon: "🟣", titleKey: "tpl.sk.ask.title", descKey: "tpl.sk.ask.desc", promptKey: "tpl.sk.ask.prompt" },
  { id: "grill-with-docs", icon: "🔥", titleKey: "tpl.sk.grilldocs.title", descKey: "tpl.sk.grilldocs.desc", promptKey: "tpl.sk.grilldocs.prompt" },
  { id: "to-spec", icon: "📄", titleKey: "tpl.sk.tospec.title", descKey: "tpl.sk.tospec.desc", promptKey: "tpl.sk.tospec.prompt" },
  { id: "to-tickets", icon: "🎫", titleKey: "tpl.sk.totickets.title", descKey: "tpl.sk.totickets.desc", promptKey: "tpl.sk.totickets.prompt" },
  { id: "implement", icon: "🛠️", titleKey: "tpl.sk.implement.title", descKey: "tpl.sk.implement.desc", promptKey: "tpl.sk.implement.prompt" },
  { id: "code-review", icon: "🧐", titleKey: "tpl.sk.codeview.title", descKey: "tpl.sk.codeview.desc", promptKey: "tpl.sk.codeview.prompt" },
  { id: "wayfinder", icon: "🧭", titleKey: "tpl.sk.wayfinder.title", descKey: "tpl.sk.wayfinder.desc", promptKey: "tpl.sk.wayfinder.prompt" },
  { id: "prototype", icon: "🚀", titleKey: "tpl.sk.prototype.title", descKey: "tpl.sk.prototype.desc", promptKey: "tpl.sk.prototype.prompt" },
  { id: "research", icon: "🛰️", titleKey: "tpl.sk.research.title", descKey: "tpl.sk.research.desc", promptKey: "tpl.sk.research.prompt" },
  { id: "improve-codebase-architecture", icon: "🏛️", titleKey: "tpl.sk.arch.title", descKey: "tpl.sk.arch.desc", promptKey: "tpl.sk.arch.prompt" },
  { id: "diagnosing-bugs", icon: "🩺", titleKey: "tpl.sk.debug.title", descKey: "tpl.sk.debug.desc", promptKey: "tpl.sk.debug.prompt" },
  { id: "resolving-merge-conflicts", icon: "🔀", titleKey: "tpl.sk.merge.title", descKey: "tpl.sk.merge.desc", promptKey: "tpl.sk.merge.prompt" },
  { id: "triage", icon: "🚑", titleKey: "tpl.sk.triage.title", descKey: "tpl.sk.triage.desc", promptKey: "tpl.sk.triage.prompt" },
  { id: "wizard", icon: "🧙", titleKey: "tpl.sk.wizard.title", descKey: "tpl.sk.wizard.desc", promptKey: "tpl.sk.wizard.prompt" },
  { id: "grill-me", icon: "🥩", titleKey: "tpl.sk.grillme.title", descKey: "tpl.sk.grillme.desc", promptKey: "tpl.sk.grillme.prompt" },
  { id: "handoff", icon: "🤝", titleKey: "tpl.sk.handoff.title", descKey: "tpl.sk.handoff.desc", promptKey: "tpl.sk.handoff.prompt" },
  { id: "to-questionnaire", icon: "📋", titleKey: "tpl.sk.questionnaire.title", descKey: "tpl.sk.questionnaire.desc", promptKey: "tpl.sk.questionnaire.prompt" },
  { id: "teach", icon: "🧑‍🏫", titleKey: "tpl.sk.teach.title", descKey: "tpl.sk.teach.desc", promptKey: "tpl.sk.teach.prompt" },
  { id: "wait-what", icon: "✋", titleKey: "tpl.sk.ww.title", descKey: "tpl.sk.ww.desc", promptKey: "tpl.sk.ww.prompt" },
  { id: "writing-for-agents", icon: "✍️", titleKey: "tpl.sk.wfa.title", descKey: "tpl.sk.wfa.desc", promptKey: "tpl.sk.wfa.prompt" },
  { id: "codebase-design", icon: "📐", titleKey: "tpl.sk.codesign.title", descKey: "tpl.sk.codesign.desc", promptKey: "tpl.sk.codesign.prompt" },
  { id: "domain-modeling", icon: "🗺️", titleKey: "tpl.sk.domain.title", descKey: "tpl.sk.domain.desc", promptKey: "tpl.sk.domain.prompt" },
  { id: "grilling", icon: "🍖", titleKey: "tpl.sk.grilling.title", descKey: "tpl.sk.grilling.desc", promptKey: "tpl.sk.grilling.prompt" },
  { id: "tdd", icon: "🧪", titleKey: "tpl.sk.tdd.title", descKey: "tpl.sk.tdd.desc", promptKey: "tpl.sk.tdd.prompt" },
]

const BUILTIN_IDS = new Set(BUILTIN_DEFS.map((d) => d.id))

function randomId(): string {
  if (typeof crypto !== "undefined" && "randomUUID" in crypto) {
    return crypto.randomUUID()
  }
  return `tpl-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 10)}`
}

function isFullEntry(s: StoredPromptTemplate): boolean {
  return !s.hidden && typeof s.title === "string" && s.title.trim() !== ""
}

interface PromptTemplatesApi {
  templates: PromptTemplate[]
  loading: boolean
  loadError: string | null
  saveError: string | null
  overriddenIds: Set<string>
  resetConfirm: boolean
  toggleReset: () => void
  openPicker: () => void
  openEdit: (tpl?: PromptTemplate) => void
  saveTemplate: (tpl: PromptTemplate) => Promise<boolean>
  removeTemplate: (tpl: PromptTemplate) => Promise<void>
  restoreOverride: (tpl: PromptTemplate) => Promise<void>
  resetToDefaults: () => Promise<void>
  fill: (text: string) => void
  send: (text: string) => void
  canSend: boolean
  /** Dialog target: the template being edited, null when closed. */
  editing: PromptTemplate | null
}

const PromptTemplatesContext = createContext<PromptTemplatesApi | null>(null)

export function usePromptTemplates(): PromptTemplatesApi {
  const ctx = useContext(PromptTemplatesContext)
  if (!ctx) {
    throw new Error("usePromptTemplates must be used within PromptTemplatesProvider")
  }
  return ctx
}

interface ProviderProps {
  children: ReactNode
  onFill: (text: string) => void
  onSend: (text: string) => void
  canSend: boolean
}

export function PromptTemplatesProvider({ children, onFill, onSend, canSend }: ProviderProps) {
  const { t } = useTranslation()
  const [stored, setStored] = useState<StoredPromptTemplate[]>([])
  const [loading, setLoading] = useState(true)
  const [loadError, setLoadError] = useState<string | null>(null)
  const [saveError, setSaveError] = useState<string | null>(null)
  const [pickerOpen, setPickerOpen] = useState(false)
  const [editing, setEditing] = useState<PromptTemplate | null>(null)
  const [editOpen, setEditOpen] = useState(false)
  const [resetConfirm, setResetConfirm] = useState(false)

  useEffect(() => {
    let alive = true
    getPromptTemplates()
      .then((list) => {
        if (alive) {
          setStored(list)
          setLoading(false)
        }
      })
      .catch(() => {
        if (alive) {
          setLoadError(t("tpl.loadError"))
          setLoading(false)
        }
      })
    return () => {
      alive = false
    }
  }, [t])

  const { templates, overriddenIds } = useMemo(() => {
    const hidden = new Set<string>()
    const overrides = new Map<string, StoredPromptTemplate>()
    const extras: PromptTemplate[] = []
    for (const s of stored) {
      if (s.hidden) {
        hidden.add(s.id)
        continue
      }
      if (BUILTIN_IDS.has(s.id)) {
        overrides.set(s.id, s)
        continue
      }
      if (isFullEntry(s)) {
        extras.push({
          id: s.id,
          icon: s.icon ?? "📌",
          title: s.title ?? "",
          desc: s.desc ?? "",
          prompt: s.prompt ?? "",
        })
      }
    }
    const builtins: PromptTemplate[] = BUILTIN_DEFS.filter((d) => !hidden.has(d.id)).map((d) => {
      const o = overrides.get(d.id)
      return {
        id: d.id,
        icon: o?.icon ?? d.icon,
        title: o?.title ?? t(d.titleKey),
        desc: o?.desc ?? t(d.descKey),
        prompt: o?.prompt ?? t(d.promptKey),
        builtin: true,
      }
    })
    return {
      templates: [...builtins, ...extras],
      overriddenIds: new Set(overrides.keys()),
    }
  }, [stored, t])

  const persist = useCallback(
    async (next: StoredPromptTemplate[]): Promise<boolean> => {
      const prev = stored
      setStored(next)
      setSaveError(null)
      try {
        const saved = await savePromptTemplates(next)
        setStored(saved)
        return true
      } catch (err) {
        setStored(prev)
        setSaveError(err instanceof Error ? err.message : t("tpl.saveError"))
        return false
      }
    },
    [stored, t],
  )

  const saveTemplate = useCallback(
    async (tpl: PromptTemplate): Promise<boolean> => {
      const entry: StoredPromptTemplate = {
        id: tpl.id,
        icon: tpl.icon,
        title: tpl.title,
        desc: tpl.desc,
        prompt: tpl.prompt,
      }
      const idx = stored.findIndex((c) => c.id === tpl.id && !c.hidden)
      const next = [...stored]
      if (idx >= 0) {
        next[idx] = entry
      } else {
        next.push(entry)
      }
      return persist(next)
    },
    [stored, persist],
  )

  const removeTemplate = useCallback(
    async (tpl: PromptTemplate) => {
      if (tpl.builtin) {
        const kept = stored.filter((c) => c.id !== tpl.id)
        await persist([...kept, { id: tpl.id, hidden: true }])
      } else {
        await persist(stored.filter((c) => c.id !== tpl.id))
      }
    },
    [stored, persist],
  )

  const restoreOverride = useCallback(
    async (tpl: PromptTemplate) => {
      await persist(stored.filter((c) => c.id !== tpl.id || c.hidden))
    },
    [stored, persist],
  )

  const resetToDefaults = useCallback(async () => {
    await persist([])
    setResetConfirm(false)
  }, [persist])

  const toggleReset = useCallback(() => {
    setResetConfirm((v) => !v)
  }, [])

  const openPicker = useCallback(() => {
    setPickerOpen(true)
  }, [])

  const openEdit = useCallback((tpl?: PromptTemplate) => {
    setEditing(tpl ?? { id: "", icon: "📌", title: "", desc: "", prompt: "" })
    setEditOpen(true)
  }, [])

  const closePicker = useCallback(() => setPickerOpen(false), [])
  const closeEdit = useCallback(() => {
    setEditOpen(false)
    setEditing(null)
    setSaveError(null)
  }, [])

  const fill = useCallback(
    (text: string) => {
      onFill(text)
      setPickerOpen(false)
      setEditOpen(false)
    },
    [onFill],
  )

  const send = useCallback(
    (text: string) => {
      if (!canSend) {
        return
      }
      onSend(text)
      setPickerOpen(false)
      setEditOpen(false)
    },
    [canSend, onSend],
  )

  const api: PromptTemplatesApi = {
    templates,
    loading,
    loadError,
    saveError,
    overriddenIds,
    resetConfirm,
    toggleReset,
    openPicker,
    openEdit,
    saveTemplate,
    removeTemplate,
    restoreOverride,
    resetToDefaults,
    fill,
    send,
    canSend,
    editing: editOpen ? editing : null,
  }

  return (
    <PromptTemplatesContext.Provider value={api}>
      {children}
      <TemplatePickerDialog open={pickerOpen} onOpenChange={(o) => !o && closePicker()} />
      <TemplateEditDialog open={editOpen} onOpenChange={(o) => !o && closeEdit()} />
    </PromptTemplatesContext.Provider>
  )
}

function TemplateCard({
  tpl,
  compact,
}: {
  tpl: PromptTemplate
  compact?: boolean
}) {
  const { t } = useTranslation()
  const { fill, openEdit } = usePromptTemplates()
  return (
    <div className="group/card relative">
      <button
        type="button"
        onClick={() => fill(tpl.prompt)}
        title={tpl.title}
        className={cn(
          "flex h-full w-full cursor-pointer flex-col gap-2 rounded-xl border bg-card p-4 text-left transition-colors hover:border-foreground/25 hover:bg-accent",
          compact && "gap-1.5 p-3",
        )}
      >
        <span className="flex items-center gap-2 min-w-0">
          <span aria-hidden className="shrink-0 text-lg leading-none">
            {tpl.icon}
          </span>
          <span className="truncate text-sm font-medium">{tpl.title}</span>
        </span>
        {!compact && tpl.desc && (
          <span className="line-clamp-2 text-xs leading-relaxed text-muted-foreground">
            {tpl.desc}
          </span>
        )}
      </button>
      <button
        type="button"
        onClick={() => openEdit(tpl)}
        title={t("tpl.editTitle")}
        className="absolute top-2 right-2 hidden h-6 w-6 cursor-pointer items-center justify-center rounded-lg bg-background/80 text-muted-foreground transition-colors hover:text-foreground group-hover/card:flex"
      >
        <IconEdit size={13} />
      </button>
    </div>
  )
}

function ResetButton({ className }: { className?: string }) {
  const { t } = useTranslation()
  const { resetConfirm, toggleReset, resetToDefaults } = usePromptTemplates()
  return (
    <Button
      type="button"
      variant={resetConfirm ? "destructive" : "outline"}
      size="sm"
      className={className}
      onClick={() => (resetConfirm ? resetToDefaults() : toggleReset())}
    >
      <IconRotateClockwise />
      {resetConfirm ? t("tpl.resetConfirm") : t("tpl.reset")}
    </Button>
  )
}

/** Suggestion cards shown on the chat empty state. */
export function EmptyTemplateCards() {
  const { t } = useTranslation()
  const { templates, loading, loadError, openEdit } = usePromptTemplates()
  if (loading) {
    return null
  }
  return (
    <div className="mt-10 flex flex-col items-center gap-3">
      <div className="flex items-center gap-2 text-sm text-muted-foreground">
        <IconTemplate size={16} aria-hidden />
        <span>{t("tpl.hint")}</span>
      </div>
      {loadError && (
        <p className="text-xs text-destructive">{loadError}</p>
      )}
      <ScrollArea className="max-h-72 w-full max-w-2xl">
        <div className="grid grid-cols-2 gap-2 p-0.5 sm:grid-cols-3">
          {templates.map((tpl) => (
            <TemplateCard key={tpl.id} tpl={tpl} compact />
          ))}
          <button
            type="button"
            onClick={() => openEdit()}
            className="flex h-full min-h-16 cursor-pointer flex-col items-center justify-center gap-1.5 rounded-xl border border-dashed p-3 text-muted-foreground transition-colors hover:border-foreground/25 hover:bg-accent hover:text-foreground"
          >
            <IconPlus size={16} aria-hidden />
            <span className="text-xs font-medium">{t("tpl.add")}</span>
          </button>
        </div>
      </ScrollArea>
      <ResetButton className="mt-1" />
    </div>
  )
}

function TemplatePickerDialog({
  open,
  onOpenChange,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  const { t } = useTranslation()
  const { templates, loadError, openEdit } = usePromptTemplates()
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            <IconTemplate size={16} aria-hidden />
            {t("tpl.pickerTitle")}
          </DialogTitle>
          <DialogDescription>{t("tpl.pickerHint")}</DialogDescription>
        </DialogHeader>
        {loadError && <p className="text-xs text-destructive">{loadError}</p>}
        <ScrollArea className="max-h-[50vh]">
          <div className="grid grid-cols-1 gap-2 p-0.5 sm:grid-cols-2">
            {templates.map((tpl) => (
              <TemplateCard key={tpl.id} tpl={tpl} />
            ))}
            <button
              type="button"
              onClick={() => openEdit()}
              className="flex min-h-20 cursor-pointer flex-col items-center justify-center gap-1.5 rounded-xl border border-dashed p-4 text-muted-foreground transition-colors hover:border-foreground/25 hover:bg-accent hover:text-foreground"
            >
              <IconPlus size={18} aria-hidden />
              <span className="text-sm font-medium">{t("tpl.add")}</span>
              <span className="text-xs">{t("tpl.addDesc")}</span>
            </button>
          </div>
        </ScrollArea>
        <div className="flex items-center justify-between">
          <ResetButton />
        </div>
      </DialogContent>
    </Dialog>
  )
}

function TemplateEditDialog({
  open,
  onOpenChange,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  const { t } = useTranslation()
  const {
    editing: target,
    saveTemplate,
    removeTemplate,
    restoreOverride,
    overriddenIds,
    saveError,
    fill,
    send,
    canSend,
  } = usePromptTemplates()
  const [draft, setDraft] = useState<PromptTemplate | null>(null)
  const [error, setError] = useState<string | null>(null)

  // Sync local form state whenever the dialog opens with a new target.
  useEffect(() => {
    if (open) {
      setDraft(target)
      setError(null)
    }
  }, [open, target])

  const isNew = draft !== null && draft.id === ""
  const isBuiltin = draft?.builtin === true
  const overridden = draft !== null && overriddenIds.has(draft.id)

  const set = (patch: Partial<PromptTemplate>) => {
    setDraft((prev) => (prev ? { ...prev, ...patch } : prev))
  }

  const onSave = async () => {
    if (!draft) {
      return
    }
    const icon = draft.icon.trim()
    const title = draft.title.trim()
    const desc = draft.desc.trim()
    const prompt = draft.prompt.trim()
    if (!title || !prompt) {
      setError(t("tpl.required"))
      return
    }
    setError(null)
    const ok = await saveTemplate({
      ...draft,
      id: draft.id || randomId(),
      icon: icon || "📌",
      title,
      desc,
      prompt,
    })
    if (ok) {
      onOpenChange(false)
    }
  }

  const onFill = () => {
    if (!draft) {
      return
    }
    const prompt = draft.prompt.trim()
    if (!prompt) {
      setError(t("tpl.required"))
      return
    }
    fill(prompt)
  }

  const onSend = () => {
    if (!draft) {
      return
    }
    const prompt = draft.prompt.trim()
    if (!prompt) {
      setError(t("tpl.required"))
      return
    }
    send(prompt)
  }

  const onRemove = async () => {
    if (draft) {
      await removeTemplate(draft)
    }
    onOpenChange(false)
  }

  const onRestore = async () => {
    if (draft) {
      await restoreOverride(draft)
    }
    onOpenChange(false)
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>{isNew ? t("tpl.newTitle") : t("tpl.editTitle")}</DialogTitle>
        </DialogHeader>
        {!draft ? null : (
          <div className="flex flex-col gap-3">
            <div className="flex gap-2">
              <div className="flex w-20 flex-col gap-1.5">
                <label className="text-xs font-medium text-muted-foreground" htmlFor="tpl-icon">
                  {t("tpl.fieldIcon")}
                </label>
                <Input
                  id="tpl-icon"
                  value={draft.icon}
                  onChange={(e) => set({ icon: e.target.value })}
                  maxLength={8}
                />
              </div>
              <div className="flex flex-1 flex-col gap-1.5">
                <label className="text-xs font-medium text-muted-foreground" htmlFor="tpl-title">
                  {t("tpl.fieldTitle")}
                </label>
                <Input
                  id="tpl-title"
                  value={draft.title}
                  onChange={(e) => set({ title: e.target.value })}
                  placeholder={t("tpl.fieldTitlePh")}
                  maxLength={120}
                />
              </div>
            </div>
            <div className="flex flex-col gap-1.5">
              <label className="text-xs font-medium text-muted-foreground" htmlFor="tpl-desc">
                {t("tpl.fieldDesc")}
              </label>
              <Input
                id="tpl-desc"
                value={draft.desc}
                onChange={(e) => set({ desc: e.target.value })}
                placeholder={t("tpl.fieldDescPh")}
                maxLength={200}
              />
            </div>
            <div className="flex flex-col gap-1.5">
              <label className="text-xs font-medium text-muted-foreground" htmlFor="tpl-prompt">
                {t("tpl.fieldPrompt")}
              </label>
              <Textarea
                id="tpl-prompt"
                value={draft.prompt}
                onChange={(e) => set({ prompt: e.target.value })}
                placeholder={t("tpl.fieldPromptPh")}
                rows={10}
                className="font-mono text-xs leading-relaxed"
              />
            </div>
            {(error || saveError) && (
              <p className="text-xs text-destructive">{error ?? saveError}</p>
            )}
          </div>
        )}
        <div className="flex flex-wrap items-center justify-between gap-2">
          <div className="flex flex-wrap gap-2">
            {isBuiltin && overridden && (
              <Button type="button" variant="outline" size="sm" onClick={onRestore}>
                <IconRotateClockwise />
                {t("tpl.restore")}
              </Button>
            )}
            {!isNew && (
              <Button type="button" variant="destructive" size="sm" onClick={onRemove}>
                <IconTrash />
                {isBuiltin ? t("tpl.remove") : t("tpl.delete")}
              </Button>
            )}
          </div>
          <div className="flex flex-wrap gap-2">
            <Button type="button" variant="outline" size="sm" onClick={onFill}>
              {t("tpl.fill")}
            </Button>
            <Button
              type="button"
              variant="outline"
              size="sm"
              onClick={onSend}
              disabled={!canSend}
            >
              <IconSend />
              {t("tpl.sendNow")}
            </Button>
            <Button type="button" size="sm" onClick={onSave}>
              {t("tpl.save")}
            </Button>
          </div>
        </div>
      </DialogContent>
    </Dialog>
  )
}

/** Toolbar button for the chat composer: opens the template picker. */
export function TemplatePickerButton({ className }: { className?: string }) {
  const { t } = useTranslation()
  const { openPicker } = usePromptTemplates()
  return (
    <Button
      type="button"
      variant="ghost"
      size="icon"
      className={className}
      onClick={openPicker}
      title={t("tpl.pickerTitle")}
    >
      <IconTemplate />
    </Button>
  )
}
