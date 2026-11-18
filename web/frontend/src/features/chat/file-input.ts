import type { TFunction } from "i18next"
import { toast } from "sonner"

import type { ChatAttachment } from "@/store/chat"

import { buildChatImageAttachments } from "./image-input"

// Drag/paste helpers operate on File objects generically — re-export so the
// chat page keeps a single import source for the whole upload pipeline.
export { getTransferredFiles, hasFileTransfer } from "./image-input"

/** Any file type is selectable; images within the size cap stay inline. */
export const CHAT_FILE_ACCEPT = "*/*"

interface UploadResponse {
  path: string
  filename: string
  content_type: string
  size: number
}

/** Inline (data URL) when the existing image pipeline handles it, else upload. */
function isInlineImage(file: File): boolean {
  return (
    file.type.startsWith("image/") &&
    // The inline path enforces its own size cap and reports failures with
    // toast inside buildChatImageAttachments, so only route candidates there.
    file.size <= 7 * 1024 * 1024
  )
}

/**
 * Upload one non-image file via POST /pico/upload (design §4.1/§4.6).
 * Returns an attachment carrying the workspace-relative path; url stays
 * empty — preview hrefs resolve from the path later (P1).
 */
async function uploadChatFile(
  file: File,
  t: TFunction,
): Promise<ChatAttachment | null> {
  const form = new FormData()
  form.append("file", file)
  if (file.name) {
    form.append("filename", file.name)
  }

  try {
    const res = await fetch("/pico/upload", { method: "POST", body: form })
    if (!res.ok) {
      if (res.status === 413) {
        toast.error(t("chat.fileTooLarge", { name: file.name || "file" }))
      } else {
        throw new Error(`upload failed: ${res.status}`)
      }
      return null
    }
    const data = (await res.json()) as UploadResponse
    const contentType =
      data.content_type || file.type || "application/octet-stream"
    return {
      type: attachmentTypeFromContentType(contentType),
      url: "",
      filename: data.filename || file.name,
      contentType,
      path: data.path,
    }
  } catch (error) {
    console.error("chat file upload failed", error)
    toast.error(t("chat.uploadFailed", { name: file.name || "file" }))
    return null
  }
}

function attachmentTypeFromContentType(
  contentType: string,
): ChatAttachment["type"] {
  if (contentType.startsWith("image/")) return "image"
  if (contentType.startsWith("audio/")) return "audio"
  if (contentType.startsWith("video/")) return "video"
  return "file"
}

/**
 * Split incoming files: images the inline pipeline accepts keep the existing
 * data-URL path; everything else uploads and references the workspace path.
 */
export async function buildChatFileAttachments(
  files: readonly File[],
  t: TFunction,
): Promise<ChatAttachment[]> {
  const inline: File[] = []
  const toUpload: File[] = []
  for (const file of files) {
    if (isInlineImage(file)) {
      inline.push(file)
    } else {
      toUpload.push(file)
    }
  }

  const attachments: ChatAttachment[] = []

  const inlineResults =
    inline.length > 0 ? await buildChatImageAttachments(inline, t) : []
  attachments.push(...inlineResults)

  for (const file of toUpload) {
    const attachment = await uploadChatFile(file, t)
    if (attachment) {
      attachments.push(attachment)
    }
  }

  return attachments
}
