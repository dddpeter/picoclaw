import { IconDownload, IconFile, IconRefresh } from "@tabler/icons-react"
import dayjs from "dayjs"
import { useCallback, useEffect, useState } from "react"
import { useTranslation } from "react-i18next"

import { FilePreviewPanel } from "@/components/chat/file-preview-panel"
import { VirtualFileList } from "@/components/files/virtual-file-list"
import { PageHeader } from "@/components/page-header"
import { Button } from "@/components/ui/button"
import { openFilePreview } from "@/store/file-preview"

/**
 * Global "my files" page (design §11.1): the files THIS user uploaded or
 * the agent generated — across all sessions. Deliberately not the workspace
 * tree: that is mostly noise (cron jobs, plans, backups).
 *
 * Efficiency: lists are virtualized (@tanstack/react-virtual) so a
 * workspace with thousands of files still scrolls smoothly — only the rows
 * inside the viewport are mounted.
 */

interface MyFile {
  path: string
  filename: string
  origin: "upload" | "generated"
  session?: string
  size: number
  content_type?: string
  mtime?: string
}

/** Relative time for recent files, absolute date for older ones. */
function formatTime(iso: string): string {
  const date = dayjs(iso)
  if (!date.isValid()) return ""
  const diffDays = dayjs().diff(date, "day")
  if (diffDays < 1) return date.format("HH:mm")
  if (diffDays < 7) return date.format("MM-DD HH:mm")
  return date.format("YYYY-MM-DD")
}

function formatSize(bytes: number): string {
  if (!bytes || bytes <= 0) return "-"
  const units = ["B", "KB", "MB", "GB"]
  let value = bytes
  let unit = 0
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024
    unit += 1
  }
  return `${value.toFixed(unit === 0 ? 0 : 1)} ${units[unit]}`
}

const ROW_HEIGHT = 32

export function FilesPage() {
  const { t } = useTranslation()
  const [files, setFiles] = useState<MyFile[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [reloadNonce, setReloadNonce] = useState(0)

  useEffect(() => {
    let cancelled = false
    setLoading(true)
    setError(null)
    fetch("/pico/myfiles")
      .then(async (res) => {
        if (!res.ok) throw new Error(`http-${res.status}`)
        return (await res.json()) as { files?: MyFile[] }
      })
      .then((data) => {
        if (!cancelled) setFiles(data.files ?? [])
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

  const reload = useCallback(() => setReloadNonce((n) => n + 1), [])

  return (
    <div className="flex h-full min-h-0">
      <div className="flex min-w-0 flex-1 flex-col">
        <PageHeader title={t("navigation.files")}>
          <Button
            size="sm"
            variant="outline"
            onClick={reload}
            disabled={loading}
          >
            <IconRefresh className="size-4" />
            {t("filePreview.retry")}
          </Button>
        </PageHeader>
        <div className="flex min-h-0 flex-1 flex-col gap-6 px-4 py-4 md:px-8">
          {loading && files.length === 0 ? (
            <p className="text-muted-foreground text-sm">
              {t("filePreview.loading")}
            </p>
          ) : error ? (
            <div className="flex flex-col items-start gap-2">
              <p className="text-muted-foreground text-sm">{error}</p>
              <Button size="sm" variant="outline" onClick={reload}>
                {t("filePreview.retry")}
              </Button>
            </div>
          ) : files.length === 0 ? (
            <p className="text-muted-foreground text-sm">
              {t("fileManager.empty")}
            </p>
          ) : (
            <div className="flex min-h-0 flex-1 flex-col">
              <div className="min-h-0 flex-1">
                <VirtualFileList
                  items={files}
                  rowHeight={ROW_HEIGHT}
                  getKey={(file) => file.path}
                  renderRow={(file) => <FileRow file={file} />}
                />
              </div>
            </div>
          )}
        </div>
      </div>
      <FilePreviewPanel />
    </div>
  )
}

function FileRow({ file }: { file: MyFile }) {
  const { t } = useTranslation()
  const isUpload = file.origin === "upload"
  return (
    <div className="hover:bg-accent/50 group flex h-8 items-center gap-2 rounded-md px-2">
      <button
        type="button"
        className="flex min-w-0 flex-1 items-center gap-2 text-left"
        onClick={() => openFilePreview(file.path, file.filename)}
        title={file.path}
      >
        <IconFile className="text-muted-foreground size-4 shrink-0" />
        <span
          className={
            isUpload
              ? "bg-secondary text-secondary-foreground shrink-0 rounded px-1.5 py-0.5 text-[10px] leading-none"
              : "text-muted-foreground/80 shrink-0 rounded border px-1.5 py-0.5 text-[10px] leading-none"
          }
        >
          {isUpload
            ? t("fileManager.tagUploaded")
            : t("fileManager.tagGenerated")}
        </span>
        <span className="truncate text-sm">{file.filename}</span>
        <span className="text-muted-foreground ml-auto shrink-0 text-xs">
          {formatSize(file.size)}
        </span>
        {file.mtime && (
          <span
            className="text-muted-foreground/70 shrink-0 text-xs"
            title={file.mtime}
          >
            {formatTime(file.mtime)}
          </span>
        )}
      </button>
      <a
        href={`/pico/file?${new URLSearchParams({ path: file.path, download: "1" })}`}
        download
        className="text-muted-foreground hover:text-foreground opacity-0 transition group-hover:opacity-100"
        aria-label={file.filename}
      >
        <IconDownload className="size-4" />
      </a>
    </div>
  )
}
