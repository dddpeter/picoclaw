import { atom, getDefaultStore } from "jotai"
import { useAtom } from "jotai"

/** Preview target shown in the right dock's preview tab (design §5.5). */
export interface PreviewFile {
  path: string
  filename?: string
}

export type FileDockTab = "preview" | "files"

export const fileDockOpenAtom = atom(false)
export const fileDockTabAtom = atom<FileDockTab>("preview")
export const previewFileAtom = atom<PreviewFile | null>(null)

/** Release focus from the invoking control before the dock dialog opens.
 * Radix aria-hides the app root when the dialog mounts; a focused
 * descendant (the chip/button the user just clicked or Enter-pressed)
 * would end up hidden from assistive tech — the browser a11y warning
 * "Blocked aria-hidden … descendant retained focus". Blurring here
 * covers every entry point (attachment chips, tool-card buttons, file
 * manager rows) in one place. */
function releaseInvokerFocus(): void {
  if (document.activeElement instanceof HTMLElement) {
    document.activeElement.blur()
  }
}

/** Open the preview tab on a file from anywhere (event handlers, non-hook
 * code) — same default-store pattern the chat controller uses. */
export function openFilePreview(path: string, filename?: string): void {
  releaseInvokerFocus()
  const store = getDefaultStore()
  store.set(previewFileAtom, { path, filename })
  store.set(fileDockTabAtom, "preview")
  store.set(fileDockOpenAtom, true)
}

/** Switch the dock to the file-manager tab. */
export function openFileManager(): void {
  releaseInvokerFocus()
  const store = getDefaultStore()
  store.set(fileDockTabAtom, "files")
  store.set(fileDockOpenAtom, true)
}

/** Convenience hook mirroring the atoms for component consumers. */
export function useFileDock() {
  const [open, setOpen] = useAtom(fileDockOpenAtom)
  const [tab, setTab] = useAtom(fileDockTabAtom)
  const [file, setFile] = useAtom(previewFileAtom)
  return {
    open,
    tab,
    file,
    showPreview(path: string, filename?: string) {
      setFile({ path, filename })
      setTab("preview")
      setOpen(true)
    },
    showFiles() {
      setTab("files")
      setOpen(true)
    },
    /** Switch tabs without touching the preview target (safe when the
     * preview tab has no file yet). */
    switchTab(next: FileDockTab) {
      setTab(next)
    },
    close() {
      setOpen(false)
      setFile(null)
    },
  }
}
