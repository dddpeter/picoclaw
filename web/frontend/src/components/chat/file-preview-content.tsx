import { useCallback, useEffect, useState } from "react"
import { useTranslation } from "react-i18next"

// The chat markdown stylesheet scopes its rules to .msg-text (bubble
// layout) and is imported by pi-message.tsx for the chat surface only.
// The preview renders the same Markdown component outside a bubble, so it
// must import the stylesheet itself and carry the same scope class —
// without it the preview renders unstyled raw markdown.
import "@/chat-pi/lobe-markdown.css"
import { Markdown } from "@/chat-pi/markdown"
import { Button } from "@/components/ui/button"
import { cn } from "@/lib/utils"

import { RichPreview, supportsRichPreview } from "./file-rich-preview"
import { HTML_PREVIEW_SANDBOX, isolateHtmlPreview } from "./html-preview"
import type { PreviewMode } from "./preview-mode"

/**
 * File preview renderer (design §5.6): the server's X-Picoclaw-File-Kind
 * header is authoritative; large text falls back to plain <pre>.
 */

const HIGHLIGHT_FENCE_LIMIT = 256 * 1024
const TEXT_DISPLAY_LIMIT = 1024 * 1024

export interface PreviewFetch {
  kind: "text" | "image" | "audio" | "video" | "binary"
  filename: string
  url: string
  text?: string
  truncated?: boolean
  error?: string
}

export function filePreviewUrl(path: string, download = false): string {
  const params = new URLSearchParams({ path })
  if (download) params.set("download", "1")
  return `/pico/file?${params.toString()}`
}

async function fetchPreview(path: string): Promise<PreviewFetch> {
  const url = filePreviewUrl(path)
  const res = await fetch(url)
  if (!res.ok) {
    if (res.status === 403) throw new Error("forbidden")
    if (res.status === 404) throw new Error("notfound")
    throw new Error(`http-${res.status}`)
  }
  const kindHeader = res.headers.get("X-Picoclaw-File-Kind") ?? "binary"
  const kind = (
    ["text", "image", "audio", "video", "binary"] as const
  ).includes(kindHeader as PreviewFetch["kind"])
    ? (kindHeader as PreviewFetch["kind"])
    : "binary"
  const filename = res.headers.get("X-Picoclaw-File-Name")
    ? decodeURIComponent(res.headers.get("X-Picoclaw-File-Name") as string)
    : path.split("/").pop() || path

  const fetchResult: PreviewFetch = { kind, filename, url }
  if (kind === "text") {
    const body = await res.text()
    if (body.length > TEXT_DISPLAY_LIMIT) {
      fetchResult.text = body.slice(0, TEXT_DISPLAY_LIMIT)
      fetchResult.truncated = true
    } else {
      fetchResult.text = body
    }
  }
  return fetchResult
}

export function useFilePreviewContent(path: string | null) {
  const [data, setData] = useState<PreviewFetch | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [loading, setLoading] = useState(false)
  const [reloadNonce, setReloadNonce] = useState(0)

  useEffect(() => {
    if (!path) {
      setData(null)
      setError(null)
      return
    }
    let cancelled = false
    setLoading(true)
    setError(null)
    fetchPreview(path)
      .then((result) => {
        if (!cancelled) setData(result)
      })
      .catch((err: unknown) => {
        if (!cancelled) setError(String((err as Error)?.message ?? err))
      })
      .finally(() => {
        if (!cancelled) setLoading(false)
      })
    return () => {
      cancelled = true
    }
  }, [path, reloadNonce])

  const reload = useCallback(() => setReloadNonce((n) => n + 1), [])
  return { data, error, loading, reload }
}

export function FilePreviewContent({ path }: { path: string }) {
  const { t } = useTranslation()
  const { data, error, loading, reload } = useFilePreviewContent(path)
  // "rendered" = parsed/rich view, "source" = raw file text.
  const [mode, setMode] = useState<PreviewMode>("rendered")
  useEffect(() => {
    setMode("rendered")
  }, [path])

  if (loading && !data) {
    return (
      <div className="text-muted-foreground p-6 text-sm">
        {t("filePreview.loading")}
      </div>
    )
  }
  if (error) {
    return (
      <PreviewErrorCard
        message={
          error === "forbidden"
            ? t("filePreview.forbidden")
            : error === "notfound"
              ? t("filePreview.notFound")
              : t("filePreview.loadFailed")
        }
        path={path}
        onRetry={reload}
      />
    )
  }
  if (!data) return null

  // Rich formats (pdf/docx/xlsx) come back as binary but render client-side.
  const rich = supportsRichPreview(data.filename)
  if (rich && data.kind === "binary") {
    return <RichPreview kind={rich} url={data.url} filename={data.filename} />
  }

  switch (data.kind) {
    case "image":
      return (
        <div className="flex h-full items-start justify-center overflow-auto p-4">
          <img
            src={data.url}
            alt={data.filename}
            className="max-h-full max-w-full rounded-lg object-contain"
          />
        </div>
      )
    case "audio":
      return (
        <div className="flex h-full items-center justify-center p-6">
          <audio controls src={data.url} className="w-full max-w-md" />
        </div>
      )
    case "video":
      return (
        <div className="flex h-full items-center justify-center p-4">
          <video
            controls
            src={data.url}
            className="max-h-full max-w-full rounded-lg"
          />
        </div>
      )
    case "text":
      return <TextPreviewShell data={data} mode={mode} onModeChange={setMode} />
    default:
      return <DownloadFallbackCard path={path} filename={data.filename} />
  }
}

/**
 * Shared shell for textual previews: the content view fills the space and
 * the mode switch lives in ONE fixed footer, so "rendered"/"source" never
 * shift position between views.
 */
function TextPreviewShell({
  data,
  mode,
  onModeChange,
}: {
  data: PreviewFetch
  mode: PreviewMode
  onModeChange: (mode: PreviewMode) => void
}) {
  const lower = data.filename.toLowerCase()
  const switchable = isHtmlFilename(lower) || isMarkdownFilename(lower)

  return (
    <div className="flex h-full min-h-0 flex-col">
      <div className="min-h-0 flex-1 overflow-hidden">
        {isHtmlFilename(lower) ? (
          mode === "source" ? (
            <SourceView data={data} />
          ) : (
            <HtmlRenderedView data={data} />
          )
        ) : isMarkdownFilename(lower) ? (
          mode === "source" ? (
            <SourceView data={data} />
          ) : (
            <MarkdownRenderedView data={data} />
          )
        ) : (
          <PlainTextView data={data} />
        )}
      </div>
      {switchable && (
        <div className="flex shrink-0 justify-end border-t px-2 py-1">
          <ModeToggle mode={mode} onModeChange={onModeChange} />
        </div>
      )}
    </div>
  )
}

function isHtmlFilename(lower: string): boolean {
  return (
    lower.endsWith(".html") ||
    lower.endsWith(".htm") ||
    lower.endsWith(".xhtml")
  )
}

function isMarkdownFilename(lower: string): boolean {
  return lower.endsWith(".md") || lower.endsWith(".markdown")
}

function MarkdownRenderedView({ data }: { data: PreviewFetch }) {
  const { t } = useTranslation()
  return (
    <div className="msg-text h-full overflow-auto p-4">
      <Markdown text={data.text ?? ""} />
      {data.truncated && (
        <p className="text-muted-foreground mt-4 text-xs">
          {t("filePreview.textTruncated")}
        </p>
      )}
    </div>
  )
}

function PlainTextView({ data }: { data: PreviewFetch }) {
  const { t } = useTranslation()
  const lower = data.filename.toLowerCase()

  if (!data.truncated && (data.text?.length ?? 0) < HIGHLIGHT_FENCE_LIMIT) {
    const ext = lower.split(".").pop() ?? ""
    return (
      <div className="msg-text h-full overflow-auto p-4">
        <Markdown text={"```" + ext + "\n" + (data.text ?? "") + "\n```"} />
      </div>
    )
  }

  return (
    <div className="h-full overflow-auto p-4">
      <pre className="text-foreground text-xs leading-relaxed whitespace-pre-wrap">
        {data.text}
      </pre>
      {data.truncated && (
        <p className="text-muted-foreground mt-4 text-xs">
          {t("filePreview.textTruncated")}
        </p>
      )}
    </div>
  )
}

function ModeToggle({
  mode,
  onModeChange,
}: {
  mode: PreviewMode
  onModeChange: (mode: PreviewMode) => void
}) {
  const { t } = useTranslation()
  return (
    <div className="bg-background/85 sticky bottom-2 left-0 z-10 mt-3 flex w-fit gap-1 rounded-lg border p-0.5">
      <button
        type="button"
        className={
          mode === "rendered"
            ? "bg-secondary text-secondary-foreground rounded-md px-2 py-1 text-xs"
            : "text-muted-foreground hover:text-foreground rounded-md px-2 py-1 text-xs"
        }
        onClick={() => onModeChange("rendered")}
      >
        {t("filePreview.modeRendered")}
      </button>
      <button
        type="button"
        className={
          mode === "source"
            ? "bg-secondary text-secondary-foreground rounded-md px-2 py-1 text-xs"
            : "text-muted-foreground hover:text-foreground rounded-md px-2 py-1 text-xs"
        }
        onClick={() => onModeChange("source")}
      >
        {t("filePreview.modeSource")}
      </button>
    </div>
  )
}

function HtmlRenderedView({ data }: { data: PreviewFetch }) {
  return (
    <div className="flex h-full flex-col">
      <div className="min-h-0 flex-1">
        <iframe
          title={data.filename}
          className="h-full w-full border-0 bg-white"
          srcDoc={isolateHtmlPreview(data.text ?? "")}
          sandbox={HTML_PREVIEW_SANDBOX}
        />
      </div>
    </div>
  )
}

function SourceView({ data }: { data: PreviewFetch }) {
  const { t } = useTranslation()
  const lower = data.filename.toLowerCase()
  const ext = lower.split(".").pop() ?? ""
  return (
    <div className="msg-text h-full overflow-auto p-4">
      <Markdown text={"```" + ext + "\n" + (data.text ?? "") + "\n```"} />
      {data.truncated && (
        <p className="text-muted-foreground mt-4 text-xs">
          {t("filePreview.textTruncated")}
        </p>
      )}
    </div>
  )
}

function DownloadFallbackCard({
  path,
  filename,
}: {
  path: string
  filename: string
}) {
  const { t } = useTranslation()
  return (
    <div className="flex h-full flex-col items-center justify-center gap-3 p-6">
      <p
        className="text-muted-foreground max-w-full truncate text-sm"
        title={filename}
      >
        {filename}
      </p>
      <p className="text-muted-foreground text-sm">
        {t("filePreview.noInlinePreview")}
      </p>
      <Button asChild variant="outline" size="sm">
        <a href={filePreviewUrl(path, true)} download>
          {t("filePreview.download")}
        </a>
      </Button>
    </div>
  )
}

function PreviewErrorCard({
  message,
  path,
  onRetry,
}: {
  message: string
  path: string
  onRetry: () => void
}) {
  const { t } = useTranslation()
  return (
    <div className="flex h-full flex-col items-center justify-center gap-3 p-6">
      <p className="text-muted-foreground text-sm">{message}</p>
      <p
        className="text-muted-foreground/70 max-w-full truncate font-mono text-xs"
        title={path}
      >
        {path}
      </p>
      <div className="flex gap-2">
        <Button variant="outline" size="sm" onClick={onRetry}>
          {t("filePreview.retry")}
        </Button>
        <Button asChild variant="outline" size="sm">
          <a href={filePreviewUrl(path, true)} download>
            {t("filePreview.download")}
          </a>
        </Button>
      </div>
    </div>
  )
}

/** Shared class hook so the panel can lay children into a scroll area. */
export const previewContentClass = cn("min-h-0 flex-1")
