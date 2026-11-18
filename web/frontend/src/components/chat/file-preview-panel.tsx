import { IconEye, IconFile, IconX } from "@tabler/icons-react"
import { useCallback, useEffect, useRef, useState } from "react"
import { useTranslation } from "react-i18next"

import { useRouterState } from "@tanstack/react-router"

import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { useIsMobile } from "@/hooks/use-mobile"
import { cn } from "@/lib/utils"
import { useFileDock } from "@/store/file-preview"

import { FileManagerPanel } from "./file-manager-panel"
import { FilePreviewContent } from "./file-preview-content"
import { filePreviewUrl } from "./file-preview-shared"

const WIDTH_STORAGE_KEY = "picoclaw:file-preview:width"
const DEFAULT_WIDTH = 420
const MIN_WIDTH = 320
const MAX_WIDTH = 720

function clampWidth(value: number): number {
  return Math.min(MAX_WIDTH, Math.max(MIN_WIDTH, value))
}

function loadWidth(): number {
  try {
    const raw = window.localStorage.getItem(WIDTH_STORAGE_KEY)
    if (raw) return clampWidth(Number.parseInt(raw, 10) || DEFAULT_WIDTH)
  } catch {
    /* ignore */
  }
  return DEFAULT_WIDTH
}

/**
 * Right-side file dock (design §5.5/§11.5): two tabs — preview renders the
 * current file, files lists session uploads + generated files plus a
 * workspace browser. Desktop renders an inline resizable column; small
 * screens fall back to a full-screen dialog with the same content. Esc
 * closes.
 */
export function FilePreviewPanel() {
  const dock = useFileDock()
  // The radix Dialog portals its overlay to <body> without inheriting the
  // md:hidden class, so a CSS-hidden desktop dialog still paints a fullscreen
  // mask. Gate the dialog by viewport instead: desktop uses the inline dock
  // column, mobile uses the dialog — never both.
  const isMobile = useIsMobile()
  // The dock state is global (jotai), so it survives route changes — e.g.
  // opening a preview on /files then navigating to the chat page would carry
  // the panel over. A preview belongs to the page it was opened on: close
  // it whenever the pathname changes.
  const pathname = useRouterState({ select: (s) => s.location.pathname })
  const prevPathname = useRef(pathname)
  useEffect(() => {
    if (prevPathname.current !== pathname) {
      prevPathname.current = pathname
      dock.close()
    }
  }, [pathname, dock])

  return (
    <>
      <DesktopDock dockOpen={dock.open && !isMobile}>
        <FileDockBody />
      </DesktopDock>
      <MobileDialog dockOpen={dock.open && isMobile}>
        <FileDockBody />
      </MobileDialog>
    </>
  )
}

function DesktopDock({
  dockOpen,
  children,
}: {
  dockOpen: boolean
  children: React.ReactNode
}) {
  const [width, setWidth] = useState(loadWidth)
  // Track the live width in a ref so the pointer-up handler persists the
  // final value instead of a stale closure capture.
  const widthRef = useRef(width)

  const onResizeStart = useCallback((event: React.PointerEvent) => {
    event.preventDefault()
    const startX = event.clientX
    const startWidth = widthRef.current
    const onMove = (move: PointerEvent) => {
      const next = clampWidth(startWidth + (startX - move.clientX))
      widthRef.current = next
      setWidth(next)
    }
    const onUp = () => {
      window.removeEventListener("pointermove", onMove)
      window.removeEventListener("pointerup", onUp)
      try {
        window.localStorage.setItem(WIDTH_STORAGE_KEY, String(widthRef.current))
      } catch {
        /* ignore */
      }
    }
    window.addEventListener("pointermove", onMove)
    window.addEventListener("pointerup", onUp)
  }, [])

  if (!dockOpen) return null

  return (
    <div
      className="bg-background/95 relative hidden h-full shrink-0 border-l md:flex md:flex-col"
      style={{ width }}
      data-testid="file-preview-panel"
    >
      {/* Resize handle overlays the left edge. It must NOT be a flex child:
          in the column flow its full height would consume the whole panel
          and squeeze the body to zero height (2026-11-18 invisible-preview
          bug). */}
      <div
        className="hover:bg-primary/10 absolute inset-y-0 left-0 -ml-1 w-1.5 cursor-col-resize"
        onPointerDown={onResizeStart}
        aria-hidden
      />
      {children}
    </div>
  )
}

function MobileDialog({
  dockOpen,
  children,
}: {
  dockOpen: boolean
  children: React.ReactNode
}) {
  const { t } = useTranslation()
  const dock = useFileDock()
  return (
    <Dialog
      open={dockOpen}
      onOpenChange={(next) => (!next ? dock.close() : undefined)}
    >
      <DialogContent className="bg-background flex h-[85vh] max-w-lg flex-col p-0 md:hidden">
        <DialogHeader className="border-b px-4 py-2">
          <DialogTitle className="text-sm">
            {dock.file?.filename ?? t("filePreview.tabFiles")}
          </DialogTitle>
          <DialogDescription className="sr-only">
            {t("filePreview.dialogDesc")}
          </DialogDescription>
        </DialogHeader>
        <div className="min-h-0 flex-1 overflow-hidden">{children}</div>
      </DialogContent>
    </Dialog>
  )
}

function FileDockBody() {
  const { t } = useTranslation()
  const dock = useFileDock()
  const filename = dock.file?.filename ?? dock.file?.path.split("/").pop()

  useEffect(() => {
    if (!dock.open) return
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape") dock.close()
    }
    window.addEventListener("keydown", onKeyDown)
    return () => window.removeEventListener("keydown", onKeyDown)
  }, [dock])

  return (
    <div className="flex h-full min-h-0 flex-1 flex-col">
      <div className="flex items-center gap-1 border-b px-2 py-1.5">
        <div className="flex items-center gap-0.5">
          <Button
            type="button"
            variant={dock.tab === "preview" ? "secondary" : "ghost"}
            size="sm"
            className="h-7 gap-1.5 px-2.5"
            onClick={() => dock.switchTab("preview")}
            disabled={dock.tab === "preview"}
          >
            <IconEye className="size-3.5" />
            <span className="text-xs">{t("filePreview.tabPreview")}</span>
          </Button>
          <Button
            type="button"
            variant={dock.tab === "files" ? "secondary" : "ghost"}
            size="sm"
            className="h-7 gap-1.5 px-2.5"
            onClick={() => dock.showFiles()}
            disabled={dock.tab === "files"}
          >
            <IconFile className="size-3.5" />
            <span className="text-xs">{t("filePreview.tabFiles")}</span>
          </Button>
        </div>
        <div className="flex-1" />
        {dock.file && dock.tab === "preview" && (
          <a
            href={filePreviewUrl(dock.file.path, true)}
            download
            className="text-muted-foreground hover:text-foreground max-w-40 truncate px-1.5 text-xs underline-offset-2 hover:underline"
            title={dock.file.path}
          >
            {filename}
          </a>
        )}
        <Button
          type="button"
          variant="ghost"
          size="icon"
          className="text-muted-foreground hover:text-foreground h-7 w-7"
          onClick={() => dock.close()}
          aria-label={t("filePreview.close")}
        >
          <IconX className="size-4" />
        </Button>
      </div>
      <div className={cn("min-h-0 flex-1 overflow-hidden")}>
        {dock.tab === "preview" ? (
          dock.file ? (
            <FilePreviewContent path={dock.file.path} />
          ) : (
            <EmptyPreviewHint />
          )
        ) : (
          <FileManagerPanel />
        )}
      </div>
    </div>
  )
}

function EmptyPreviewHint() {
  const { t } = useTranslation()
  return (
    <div className="text-muted-foreground flex h-full items-center justify-center p-6 text-sm">
      {t("filePreview.empty")}
    </div>
  )
}
