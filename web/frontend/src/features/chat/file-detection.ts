import type { ChatMessage } from "@/store/chat"

/**
 * Workspace file detection (design §5.4): derive previewable file paths
 * from chat messages via three sources —
 *   a) message attachments carrying a path (uploads + send_file replay)
 *   b) write-like tool calls (path key in the arguments JSON)
 *   c) content tags ([file:/path] etc.) and exec "Full output:" pointers
 */
export interface DetectedFile {
  path: string
  filename: string
  source: "attachment" | "toolCall" | "tag"
}

const PATH_KEYS = [
  "path",
  "file_path",
  "filepath",
  "filename",
  "dest",
  "target_path",
  "output_path",
] as const

const FILE_WRITE_TOOLS = new Set([
  "write_file",
  "edit_file",
  "append_file",
  "write",
  "edit",
  "send_file",
])

export { FILE_WRITE_TOOLS }

const TAG_RE = /\[(file|image|audio|video):([^\]\s]+)\]/g
const FULL_OUTPUT_RE = /Full output:\s*(\S+)/g
const PATH_EXT_RE = /\.[A-Za-z][A-Za-z0-9._+-]{0,11}$/

function basename(path: string): string {
  const parts = path.replace(/\\/g, "/").split("/")
  return parts[parts.length - 1] || path
}

/** A path is listable when it has a file-ish extension (mirrors Octop). */
function isPreviewablePath(path: string): boolean {
  const posix = path.replace(/\\/g, "/")
  const base = posix.split("/").filter(Boolean).pop() ?? ""
  if (!base || /^[\d.]+$/.test(base)) return false
  return PATH_EXT_RE.test(base)
}

/** Extract a file path from tool-call arguments (JSON or streaming
 * half-JSON); exported for the tool-call preview entry (§5.4 source b). */
export function toolCallPath(argumentsText: string | undefined): string | null {
  return pickPathFromArgs(argumentsText)
}

function pickPathFromArgs(argsText: string | undefined): string | null {
  if (!argsText) return null
  const trimmed = argsText.trim()
  if (!trimmed) return null
  // Prefer structured JSON when it parses (write_file args carry content,
  // so a raw regex would match inside file bodies).
  try {
    const parsed = JSON.parse(trimmed) as Record<string, unknown>
    for (const key of PATH_KEYS) {
      const value = parsed[key]
      if (typeof value === "string" && value.trim()) return value.trim()
    }
  } catch {
    // Streaming half-JSON: fall through to the key regex.
  }
  const match =
    /"(?:path|file_path|filepath|dest)"\s*:\s*"((?:[^"\\\n]|\\.)*)"/.exec(
      trimmed,
    )
  if (match?.[1]) {
    try {
      return JSON.parse(`"${match[1]}"`) as string
    } catch {
      return match[1]
    }
  }
  return null
}

function extractPathsFromText(text: string | undefined): string[] {
  if (!text) return []
  const found: string[] = []
  for (const match of text.matchAll(TAG_RE)) {
    found.push(match[2])
  }
  for (const match of text.matchAll(FULL_OUTPUT_RE)) {
    found.push(match[1])
  }
  return found
}

function normalizeDetectedPath(path: string): string {
  return path.replace(/\\/g, "/").replace(/^\.\//, "")
}

/** Collect unique detected files across a message slice. */
export function detectFiles(messages: ChatMessage[]): DetectedFile[] {
  const seen = new Map<string, DetectedFile>()
  const add = (path: string, source: DetectedFile["source"]) => {
    const normalized = normalizeDetectedPath(path)
    if (!isPreviewablePath(normalized)) return
    if (!seen.has(normalized)) {
      seen.set(normalized, {
        path: normalized,
        filename: basename(normalized),
        source,
      })
    }
  }

  for (const message of messages) {
    // a) attachments with paths
    for (const attachment of message.attachments ?? []) {
      if (attachment.path) add(attachment.path, "attachment")
    }
    // b) write-like tool calls
    for (const toolCall of message.toolCalls ?? []) {
      const name = toolCall.function?.name?.trim() ?? ""
      if (!FILE_WRITE_TOOLS.has(name)) continue
      const picked = pickPathFromArgs(toolCall.function?.arguments)
      if (picked) add(picked, "toolCall")
    }
    // c) content tags + exec pointers
    for (const path of extractPathsFromText(message.content)) {
      add(path, "tag")
    }
  }

  return [...seen.values()]
}

/** Only the uploads: attachments typed as files with a path. */
export function detectUploadedFiles(messages: ChatMessage[]): DetectedFile[] {
  const seen = new Map<string, DetectedFile>()
  for (const message of messages) {
    for (const attachment of message.attachments ?? []) {
      const path = attachment.path
      if (!path || attachment.type === "image") continue
      const normalized = normalizeDetectedPath(path)
      if (!seen.has(normalized)) {
        seen.set(normalized, {
          path: normalized,
          filename: attachment.filename || basename(normalized),
          source: "attachment",
        })
      }
    }
  }
  return [...seen.values()]
}
