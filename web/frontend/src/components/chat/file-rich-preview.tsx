import { useEffect, useRef, useState } from "react"
import { useTranslation } from "react-i18next"

/**
 * Rich-format renderers (design §5.6 P2): pdf / docx / xlsx load their
 * libraries lazily via dynamic import so the main bundle stays lean.
 * Each takes the raw bytes URL (/pico/file?path=…).
 */

export function RichPreview({
  kind,
  url,
  filename,
}: {
  kind: "pdf" | "docx" | "xlsx"
  url: string
  filename: string
}) {
  if (kind === "pdf") return <PdfPreview url={url} />
  if (kind === "docx") return <DocxPreview url={url} />
  return <XlsxPreview url={url} filename={filename} />
}

function useArrayBuffer(url: string): ArrayBuffer | null {
  const [buffer, setBuffer] = useState<ArrayBuffer | null>(null)
  useEffect(() => {
    let cancelled = false
    setBuffer(null)
    fetch(url)
      .then((res) => res.arrayBuffer())
      .then((data) => {
        if (!cancelled) setBuffer(data)
      })
      .catch(() => {
        if (!cancelled) setBuffer(null)
      })
    return () => {
      cancelled = true
    }
  }, [url])
  return buffer
}

function PdfPreview({ url }: { url: string }) {
  const { t } = useTranslation()
  const containerRef = useRef<HTMLDivElement>(null)
  const [error, setError] = useState(false)

  useEffect(() => {
    let cancelled = false
    let destroy: (() => void) | undefined
    ;(async () => {
      try {
        const pdfjs = await import("pdfjs-dist")
        // Static ?url import lets Vite emit the worker asset; a runtime
        // new URL("pdfjs-dist/…") would not resolve node_modules.
        const { default: workerUrl } =
          await import("pdfjs-dist/build/pdf.worker.min.mjs?url")
        pdfjs.GlobalWorkerOptions.workerSrc = workerUrl

        const task = pdfjs.getDocument({ url })
        const pdf = await task.promise
        if (cancelled) {
          void task.destroy()
          return
        }
        const container = containerRef.current
        if (!container) return
        destroy = () => void task.destroy()

        // Render up to the first 20 pages sequentially.
        const pages = Math.min(pdf.numPages, 20)
        for (let pageNum = 1; pageNum <= pages; pageNum++) {
          if (cancelled || !containerRef.current) break
          const page = await pdf.getPage(pageNum)
          const viewport = page.getViewport({ scale: 1.4 })
          const canvas = document.createElement("canvas")
          canvas.className = "mb-4 max-w-full rounded shadow-sm"
          canvas.width = viewport.width
          canvas.height = viewport.height
          containerRef.current.appendChild(canvas)
          const context = canvas.getContext("2d")
          if (!context) continue
          await page.render({
            canvas,
            canvasContext: context,
            viewport,
          }).promise
        }
        void pdf.cleanup()
      } catch (err) {
        console.error("pdf preview failed", err)
        if (!cancelled) setError(true)
      }
    })()
    return () => {
      cancelled = true
      destroy?.()
    }
  }, [url])

  if (error) {
    return (
      <p className="text-muted-foreground p-4 text-sm">
        {t("filePreview.richFailed")}
      </p>
    )
  }
  return (
    <div
      ref={containerRef}
      className="h-full overflow-auto bg-neutral-100 p-4 dark:bg-neutral-900"
    />
  )
}

function DocxPreview({ url }: { url: string }) {
  const { t } = useTranslation()
  const containerRef = useRef<HTMLDivElement>(null)
  const buffer = useArrayBuffer(url)
  const [error, setError] = useState(false)

  useEffect(() => {
    if (!buffer || !containerRef.current) return
    let cancelled = false
    ;(async () => {
      try {
        const { renderAsync } = await import("docx-preview")
        if (cancelled || !containerRef.current) return
        containerRef.current.replaceChildren()
        await renderAsync(buffer, containerRef.current, undefined, {
          className: "docx",
          inWrapper: true,
          ignoreWidth: false,
        })
      } catch (err) {
        console.error("docx preview failed", err)
        if (!cancelled) setError(true)
      }
    })()
    return () => {
      cancelled = true
    }
  }, [buffer])

  if (error) {
    return (
      <p className="text-muted-foreground p-4 text-sm">
        {t("filePreview.richFailed")}
      </p>
    )
  }
  return (
    <div
      ref={containerRef}
      className="h-full overflow-auto bg-neutral-100 p-4 dark:bg-neutral-900"
    />
  )
}

const XLSX_MAX_ROWS = 200
const XLSX_MAX_COLS = 40

function XlsxPreview({ url, filename }: { url: string; filename: string }) {
  const { t } = useTranslation()
  const buffer = useArrayBuffer(url)
  const [rows, setRows] = useState<string[][] | null>(null)
  const [sheetName, setSheetName] = useState("")
  const [error, setError] = useState(false)

  useEffect(() => {
    if (!buffer) return
    let cancelled = false
    ;(async () => {
      try {
        const XLSX = await import("xlsx")
        const workbook = XLSX.read(buffer, { type: "array" })
        const name = workbook.SheetNames[0]
        if (!name) {
          if (!cancelled) setRows([])
          return
        }
        const sheet = workbook.Sheets[name]
        const data = XLSX.utils.sheet_to_json<unknown[]>(sheet, {
          header: 1,
          blankrows: false,
          defval: "",
        })
        if (cancelled) return
        setSheetName(name)
        setRows(
          data
            .slice(0, XLSX_MAX_ROWS)
            .map((row) =>
              (row as unknown[])
                .slice(0, XLSX_MAX_COLS)
                .map((cell) => String(cell ?? "")),
            ),
        )
      } catch (err) {
        console.error("xlsx preview failed", err)
        if (!cancelled) setError(true)
      }
    })()
    return () => {
      cancelled = true
    }
  }, [buffer])

  if (error) {
    return (
      <p className="text-muted-foreground p-4 text-sm">
        {t("filePreview.richFailed")}
      </p>
    )
  }
  if (!rows) {
    return (
      <p className="text-muted-foreground p-4 text-sm">
        {t("filePreview.loading")}
      </p>
    )
  }
  if (rows.length === 0) {
    return (
      <p className="text-muted-foreground p-4 text-sm">
        {t("fileManager.empty")}
      </p>
    )
  }

  return (
    <div className="h-full overflow-auto p-4">
      <p className="text-muted-foreground mb-2 text-xs">
        {filename} — {sheetName}
      </p>
      <table className="w-full border-collapse text-xs">
        <tbody>
          {rows.map((row, rowIndex) => (
            <tr key={rowIndex} className="border-b">
              {row.map((cell, cellIndex) => (
                <td
                  key={cellIndex}
                  className="border-border/60 max-w-48 truncate border px-2 py-1"
                  title={cell}
                >
                  {rowIndex === 0 ? <strong>{cell}</strong> : cell}
                </td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}
