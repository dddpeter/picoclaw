import {
  IconChevronDown,
  IconChevronRight,
  IconDownload,
  IconFile,
} from "@tabler/icons-react"
import { useAtomValue } from "jotai"
import { useCallback, useEffect, useMemo, useState } from "react"
import { useTranslation } from "react-i18next"

import {
  type DetectedFile,
  detectFiles,
  detectUploadedFiles,
} from "@/features/chat/file-detection"
import { chatAtom } from "@/store/chat"
import { openFilePreview } from "@/store/file-preview"

/**
 * File manager (design §11.4): session view groups uploads vs generated
 * files detected from messages; the workspace view browses via /pico/tree.
 */

type ManagerView = "session" | "workspace"

interface TreeEntry {
  name: string
  path: string
  is_dir: boolean
  size: number
  mtime: string
}

interface TreeResponse {
  path: string
  entries: TreeEntry[]
  truncated: boolean
}

async function fetchTree(path: string, depth: number): Promise<TreeResponse> {
  const params = new URLSearchParams({ path, depth: String(depth) })
  const res = await fetch(`/pico/tree?${params.toString()}`)
  if (!res.ok) throw new Error(`http-${res.status}`)
  return (await res.json()) as TreeResponse
}

function downloadHref(path: string): string {
  return `/pico/file?${new URLSearchParams({ path, download: "1" })}`
}

export function FileManagerPanel() {
  const { t } = useTranslation()
  const [view, setView] = useState<ManagerView>("session")

  return (
    <div className="flex h-full flex-col">
      <div className="flex items-center gap-2 border-b px-3 py-1.5">
        {(["session", "workspace"] as const).map((candidate) => (
          <button
            key={candidate}
            type="button"
            className={
              view === candidate
                ? "bg-secondary text-secondary-foreground rounded-md px-2.5 py-1 text-xs font-medium"
                : "text-muted-foreground hover:text-foreground rounded-md px-2.5 py-1 text-xs"
            }
            onClick={() => setView(candidate)}
          >
            {t(`fileManager.view-${candidate}`)}
          </button>
        ))}
      </div>
      <div className="min-h-0 flex-1 overflow-y-auto">
        {view === "session" ? <SessionFilesView /> : <WorkspaceView />}
      </div>
    </div>
  )
}

function SessionFilesView() {
  const { t } = useTranslation()
  const chat = useAtomValue(chatAtom)
  const messages = useMemo(() => chat.messages ?? [], [chat.messages])

  const uploaded = useMemo(() => detectUploadedFiles(messages), [messages])
  const all = useMemo(() => detectFiles(messages), [messages])
  const generated = useMemo(
    () =>
      all.filter(
        (file) =>
          !uploaded.some((item) => item.path === file.path) &&
          file.source !== "attachment",
      ),
    [all, uploaded],
  )

  if (uploaded.length === 0 && generated.length === 0) {
    return (
      <p className="text-muted-foreground p-4 text-sm">
        {t("fileManager.empty")}
      </p>
    )
  }

  return (
    <div className="p-2">
      {uploaded.length > 0 && (
        <FileSection title={t("fileManager.uploadedFiles")}>
          {uploaded.map((file) => (
            <FileRow key={file.path} file={file} />
          ))}
        </FileSection>
      )}
      {generated.length > 0 && (
        <FileSection title={t("fileManager.generatedFiles")}>
          {generated.map((file) => (
            <FileRow key={file.path} file={file} />
          ))}
        </FileSection>
      )}
    </div>
  )
}

function FileSection({
  title,
  children,
}: {
  title: string
  children: React.ReactNode
}) {
  return (
    <section className="mb-3">
      <h4 className="text-muted-foreground px-2 py-1 text-xs font-medium tracking-wide">
        {title}
      </h4>
      <div className="flex flex-col">{children}</div>
    </section>
  )
}

function FileRow({ file }: { file: DetectedFile }) {
  return (
    <div className="hover:bg-accent/50 group flex items-center gap-2 rounded-md px-2 py-1.5">
      <button
        type="button"
        className="flex min-w-0 flex-1 cursor-pointer items-center gap-2 text-left"
        onClick={() => openFilePreview(file.path, file.filename)}
        title={file.path}
      >
        <IconFile className="text-muted-foreground size-4 shrink-0" />
        <span className="truncate text-sm">{file.filename}</span>
      </button>
      <a
        href={downloadHref(file.path)}
        download
        className="text-muted-foreground hover:text-foreground opacity-0 transition group-hover:opacity-100"
        aria-label="download"
      >
        <IconDownload className="size-4" />
      </a>
    </div>
  )
}

function WorkspaceView() {
  const { t } = useTranslation()
  const [tree, setTree] = useState<TreeResponse | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [loading, setLoading] = useState(true)
  const [reloadNonce, setReloadNonce] = useState(0)
  const [expanded, setExpanded] = useState<Set<string>>(new Set())

  useEffect(() => {
    let cancelled = false
    setLoading(true)
    setError(null)
    fetchTree(".", 1)
      .then((res) => {
        if (!cancelled) setTree(res)
      })
      .catch(() => {
        if (!cancelled) setError(t("fileManager.loadFailed"))
      })
      .finally(() => {
        if (!cancelled) setLoading(false)
      })
    return () => {
      cancelled = true
    }
  }, [t, reloadNonce])

  const toggleDir = useCallback((path: string) => {
    setExpanded((prev) => {
      const next = new Set(prev)
      if (next.has(path)) {
        next.delete(path)
      } else {
        next.add(path)
      }
      return next
    })
  }, [])

  if (loading && !tree) {
    return (
      <p className="text-muted-foreground p-4 text-sm">
        {t("filePreview.loading")}
      </p>
    )
  }
  if (error) {
    return (
      <div className="flex flex-col items-start gap-2 p-4">
        <p className="text-muted-foreground text-sm">{error}</p>
        <button
          type="button"
          className="text-sm underline"
          onClick={() => setReloadNonce((n) => n + 1)}
        >
          {t("filePreview.retry")}
        </button>
      </div>
    )
  }

  return (
    <div className="p-2">
      <WorkspaceDirChildren
        entries={tree?.entries ?? []}
        depth={0}
        expanded={expanded}
        onToggleDir={toggleDir}
      />
      {tree?.truncated && (
        <p className="text-muted-foreground px-2 py-1 text-xs">
          {t("fileManager.truncated")}
        </p>
      )}
    </div>
  )
}

function WorkspaceDirChildren({
  entries,
  depth,
  expanded,
  onToggleDir,
}: {
  entries: TreeEntry[]
  depth: number
  expanded: Set<string>
  onToggleDir: (path: string) => void
}) {
  const { t } = useTranslation()
  return (
    <>
      {entries.map((entry) =>
        entry.is_dir ? (
          <WorkspaceDir
            key={entry.path}
            entry={entry}
            depth={depth}
            expanded={expanded}
            onToggleDir={onToggleDir}
          />
        ) : (
          <div key={entry.path} style={{ paddingLeft: depth * 14 }}>
            <FileRow
              file={{
                path: entry.path,
                filename: entry.name,
                source: "toolCall",
              }}
            />
          </div>
        ),
      )}
      {entries.length === 0 && depth === 0 && (
        <p className="text-muted-foreground p-4 text-sm">
          {t("fileManager.empty")}
        </p>
      )}
    </>
  )
}

function WorkspaceDir({
  entry,
  depth,
  expanded,
  onToggleDir,
}: {
  entry: TreeEntry
  depth: number
  expanded: Set<string>
  onToggleDir: (path: string) => void
}) {
  const { t } = useTranslation()
  const isOpen = expanded.has(entry.path)
  const [children, setChildren] = useState<TreeEntry[] | null>(null)
  const [error, setError] = useState(false)

  useEffect(() => {
    if (!isOpen || children) return
    let cancelled = false
    fetchTree(entry.path, 1)
      .then((res) => {
        if (!cancelled) setChildren(res.entries)
      })
      .catch(() => {
        if (!cancelled) setError(true)
      })
    return () => {
      cancelled = true
    }
  }, [isOpen, children, entry.path])

  return (
    <div>
      <button
        type="button"
        className="hover:bg-accent/50 flex w-full items-center gap-1.5 rounded-md px-2 py-1.5 text-left"
        style={{ paddingLeft: depth * 14 + 8 }}
        onClick={() => onToggleDir(entry.path)}
        title={entry.path}
      >
        {isOpen ? (
          <IconChevronDown className="text-muted-foreground size-3.5 shrink-0" />
        ) : (
          <IconChevronRight className="text-muted-foreground size-3.5 shrink-0" />
        )}
        <span className="text-muted-foreground truncate text-sm font-medium">
          {entry.name}
        </span>
      </button>
      {isOpen && (
        <div>
          {error && (
            <p className="text-muted-foreground px-2 py-1 text-xs">
              {t("fileManager.loadFailed")}
            </p>
          )}
          {children && (
            <WorkspaceDirChildren
              entries={children}
              depth={depth + 1}
              expanded={expanded}
              onToggleDir={onToggleDir}
            />
          )}
        </div>
      )}
    </div>
  )
}
