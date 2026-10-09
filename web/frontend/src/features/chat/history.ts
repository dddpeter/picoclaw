import { getSessionHistory } from "@/api/sessions"
import { pairApprovalHistory } from "@/features/chat/approval"
import { normalizeUnixTimestamp } from "@/features/chat/state"
import {
  parseToolCallsValue,
  toolCallsSignature,
} from "@/features/chat/tool-calls"
import type { ChatAttachment, ChatMessage } from "@/store/chat"

function toChatAttachments({
  media,
  attachments,
}: {
  media?: string[]
  attachments?: {
    type?: "image" | "audio" | "video" | "file"
    url: string
    filename?: string
    content_type?: string
  }[]
}): ChatAttachment[] | undefined {
  const normalizedAttachments = attachments
    ?.filter((attachment) => attachment.url)
    .map(
      (attachment) =>
        ({
          type: attachment.type ?? "file",
          url: attachment.url,
          filename: attachment.filename,
          contentType: attachment.content_type,
        }) satisfies ChatAttachment,
    )

  const legacyMediaAttachments = (media ?? [])
    .filter((item) => item.startsWith("data:image/"))
    .map((url) => ({ type: "image" as const, url }))

  const merged = [...(normalizedAttachments ?? []), ...legacyMediaAttachments]

  return merged.length > 0 ? merged : undefined
}

export async function loadSessionMessages(
  sessionId: string,
  channel?: string,
): Promise<{ messages: ChatMessage[]; channel?: string }> {
  const detail = await getSessionHistory(sessionId, channel)
  const messages = detail.messages.map((message, index) => ({
    id: `hist-${index}-${Date.now()}`,
    role: message.role,
    content: message.content,
    kind: message.role === "assistant" ? (message.kind ?? "normal") : undefined,
    modelName: message.model_name,
    toolCalls:
      message.role === "assistant"
        ? parseToolCallsValue(message.tool_calls)
        : undefined,
    attachments: toChatAttachments({
      media: message.media,
      attachments: message.attachments,
    }),
    timestamp: message.created_at ?? detail.updated,
  }))
  // 历史审批消息走 v0 文本特征识别 + 回执配对（协议 payload 不入库）。
  const paired = pairApprovalHistory(messages)
  // 刷新时待审批的问题恢复为 pending 卡：ask 本身 outbound 不入转录，
  // 由后端的持久化标记（<key>.approval.json）还原。若历史里已有未封存
  // 的 pending 卡（不应发生——标记与卡片同生命周期）则不重复注入。
  if (
    detail.pending_approval?.tool &&
    !paired.some((m) => m.role === "assistant" && m.approval && !m.approvalSeal)
  ) {
    // stale：网关在审批等待中崩溃残留的标记——问题早已 fail-closed 拒绝、
    // 回执永远不会来。预封存为 timeout，渲染为已结束的低权重卡，而不是
    // 一张倒计时归零后永远悬着的可点卡。
    const staleSeal = detail.pending_approval.stale
      ? {
          approvalSeal: {
            verdict: "timeout" as const,
            at: (detail.pending_approval.started_at ?? 0) +
              (detail.pending_approval.timeout_ms ?? 0),
          },
        }
      : {}
    paired.push({
      id: `pending-approval-${Date.now()}`,
      role: "assistant",
      content: "",
      kind: "normal",
      approval: {
        tool: detail.pending_approval.tool,
        ...(detail.pending_approval.preview
          ? { preview: detail.pending_approval.preview }
          : {}),
        ...(detail.pending_approval.timeout_ms && detail.pending_approval.timeout_ms > 0
          ? { timeoutMs: detail.pending_approval.timeout_ms }
          : {}),
      },
      timestamp: detail.pending_approval.started_at ?? Date.now(),
      ...staleSeal,
    })
  }
  return { messages: paired, channel: detail.channel }
}

function normalizeMessageTimestamp(timestamp: number | string): string {
  if (typeof timestamp === "number") {
    return String(normalizeUnixTimestamp(timestamp))
  }

  const trimmed = timestamp.trim()
  if (/^-?\d+(\.\d+)?$/.test(trimmed)) {
    return String(normalizeUnixTimestamp(Number(trimmed)))
  }

  const parsed = Date.parse(trimmed)
  return Number.isNaN(parsed) ? trimmed : String(parsed)
}

function messageSignature(message: ChatMessage): string {
  const attachmentSignature = (message.attachments ?? [])
    .map(
      (attachment) =>
        `${attachment.type}\u0001${attachment.url}\u0001${attachment.filename ?? ""}`,
    )
    .join("\u0002")

  return `${message.role}\u0000${message.content}\u0000${normalizeMessageTimestamp(
    message.timestamp,
  )}\u0000${message.kind ?? ""}\u0000${message.modelName ?? ""}\u0000${attachmentSignature}\u0000${toolCallsSignature(
    message.toolCalls,
  )}`
}

function comparableTimestamp(timestamp: number | string): number {
  const normalized = normalizeMessageTimestamp(timestamp)
  const numeric = Number(normalized)
  return Number.isFinite(numeric) ? numeric : 0
}

export function mergeHistoryMessages(
  historyMessages: ChatMessage[],
  currentMessages: ChatMessage[],
): ChatMessage[] {
  const currentIds = new Set(currentMessages.map((message) => message.id))
  const currentSignatures = new Set(
    currentMessages.map((message) => messageSignature(message)),
  )

  const merged = [
    ...historyMessages.filter(
      (message) =>
        !currentIds.has(message.id) &&
        !currentSignatures.has(messageSignature(message)),
    ),
    ...currentMessages,
  ]

  return merged.sort(
    (left, right) =>
      comparableTimestamp(left.timestamp) -
      comparableTimestamp(right.timestamp),
  )
}
