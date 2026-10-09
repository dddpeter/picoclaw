import { toast } from "sonner"

import {
  parseAssistantMessageCreateState,
  parseAssistantMessageUpdateState,
} from "@/features/chat/assistant-message-state"
import { normalizeUnixTimestamp } from "@/features/chat/state"
import {
  type ChatAttachment,
  type ContextUsage,
  type TurnStats,
  updateChatStore,
} from "@/store/chat"
import {
  matchApprovalReceipt,
  parseApprovalFromPayload,
  parseApprovalFromText,
  sealLastApproval,
} from "@/features/chat/approval"

export interface PicoMessage {
  type: string
  id?: string
  session_id?: string
  timestamp?: number | string
  payload?: Record<string, unknown>
}

function parseAttachments(
  payload: Record<string, unknown>,
): ChatAttachment[] | undefined {
  const raw = payload.attachments
  if (!Array.isArray(raw)) {
    return undefined
  }

  const attachments: ChatAttachment[] = []
  for (const item of raw) {
    if (!item || typeof item !== "object") {
      continue
    }

    const attachment = item as Record<string, unknown>
    const url = typeof attachment.url === "string" ? attachment.url : ""
    if (!url) {
      continue
    }

    const type =
      attachment.type === "audio" ||
      attachment.type === "video" ||
      attachment.type === "file" ||
      attachment.type === "image"
        ? attachment.type
        : "file"

    const filename =
      typeof attachment.filename === "string" ? attachment.filename : undefined
    const contentType =
      typeof attachment.content_type === "string"
        ? attachment.content_type
        : undefined

    attachments.push({
      type,
      url,
      ...(filename ? { filename } : {}),
      ...(contentType ? { contentType } : {}),
    })
  }

  return attachments.length > 0 ? attachments : undefined
}

function parseContextUsage(
  payload: Record<string, unknown>,
): ContextUsage | undefined {
  const raw = payload.context_usage
  if (!raw || typeof raw !== "object") return undefined
  const obj = raw as Record<string, unknown>
  const used = Number(obj.used_tokens)
  const total = Number(obj.total_tokens)
  if (!Number.isFinite(used) || !Number.isFinite(total) || total <= 0)
    return undefined
  return {
    used_tokens: used,
    total_tokens: total,
    history_tokens: obj.history_tokens != null ? Number(obj.history_tokens) : undefined,
    compress_at_tokens: Number(obj.compress_at_tokens) || 0,
    summarize_at_tokens: obj.summarize_at_tokens != null ? Number(obj.summarize_at_tokens) : undefined,
    used_percent: Number(obj.used_percent) || 0,
  }
}

function parseModelName(payload: Record<string, unknown>): string | undefined {
  if (typeof payload.model_name !== "string") {
    return undefined
  }
  const modelName = payload.model_name.trim()
  return modelName || undefined
}

/** 封板消息的 usage 载荷 → 回合用量快照（末次调用口径，含 LLM 调用次数）。 */
function parseTurnStats(
  payload: Record<string, unknown>,
): TurnStats | undefined {
  const raw = payload.usage
  if (!raw || typeof raw !== "object") {
    return undefined
  }
  const obj = raw as Record<string, unknown>
  const input = Number(obj.input_tokens)
  const output = Number(obj.output_tokens)
  const llmCalls = Number(obj.llm_calls)
  if (!Number.isFinite(input) || !Number.isFinite(output)) {
    return undefined
  }
  const modelName = parseModelName(payload)
  return {
    inputTokens: Math.max(0, input),
    outputTokens: Math.max(0, output),
    ...(Number.isFinite(llmCalls) && llmCalls > 0 ? { llmCalls } : {}),
    ...(modelName ? { modelName } : {}),
  }
}

export function handlePicoMessage(
  message: PicoMessage,
  expectedSessionId: string,
) {
  if (message.session_id && message.session_id !== expectedSessionId) {
    return
  }

  const payload = message.payload || {}

  switch (message.type) {
    case "message.create":
    case "media.create": {
      const messageId = (payload.message_id as string) || `pico-${Date.now()}`
      const { content, kind, toolCalls } =
        parseAssistantMessageCreateState(payload)
      const attachments = parseAttachments(payload)
      const contextUsage = parseContextUsage(payload)
      const isPlaceholder = payload.placeholder === true
      const modelName = parseModelName(payload)
      const timestamp =
        message.timestamp !== undefined &&
        Number.isFinite(Number(message.timestamp))
          ? normalizeUnixTimestamp(Number(message.timestamp))
          : Date.now()

      const turnFinished =
        !isPlaceholder &&
        (kind === "normal" || message.type === "media.create")
      const turnStats = turnFinished ? parseTurnStats(payload) : undefined
      // 审批询问：v1 payload 优先，v0 兜底文本特征（历史回放同款解析）。
      // 注意询问本身是 kind=normal 的终结消息（turnFinished），不能据此排除。
      const approval =
        !isPlaceholder
          ? (parseApprovalFromPayload(payload) ?? parseApprovalFromText(content))
          : undefined
      // 审批回执：封存最近未封卡并抑制回执文本本身；无配对目标按普通文本。
      const receiptVerdict = !isPlaceholder ? matchApprovalReceipt(content) : undefined

      updateChatStore((prev) => {
        const turnState = isPlaceholder
          ? {}
          : turnFinished
            ? {
                turnStartedAt: undefined,
                lastTurnActivityAt: undefined,
                turnStatus: "done" as const,
                // 回合结束：冻结耗时快照（状态栏显示用），并记录末次用量。
                turnElapsedMs: prev.turnStartedAt
                  ? Date.now() - prev.turnStartedAt
                  : prev.turnElapsedMs,
                ...(turnStats ? { turnStats } : {}),
              }
            : { lastTurnActivityAt: Date.now() }
        const common = {
          isTyping: turnFinished ? false : prev.isTyping,
          ...turnState,
          ...(contextUsage ? { contextUsage } : {}),
        }

        if (receiptVerdict) {
          const sealed = sealLastApproval(prev.messages, receiptVerdict, timestamp)
          if (sealed) {
            return { ...common, messages: sealed }
          }
        }

        return {
          ...common,
          messages: [
            ...prev.messages,
            {
              id: messageId,
              role: "assistant",
              content,
              kind,
              ...(modelName ? { modelName } : {}),
              ...(toolCalls ? { toolCalls } : {}),
              ...(approval ? { approval } : {}),
              attachments,
              timestamp,
            },
          ],
        }
      })
      break
    }

    case "message.update": {
      const messageId = payload.message_id as string
      const attachments = parseAttachments(payload)
      const contextUsage = parseContextUsage(payload)
      const modelName = parseModelName(payload)
      const timestamp =
        message.timestamp !== undefined &&
        Number.isFinite(Number(message.timestamp))
          ? normalizeUnixTimestamp(Number(message.timestamp))
          : Date.now()
      if (!messageId) {
        break
      }

      updateChatStore((prev) => ({
        messages: (() => {
          let found = false
          const messages = prev.messages.map((msg) => {
            if (msg.id !== messageId) {
              return msg
            }
            found = true
            const { content, kind, toolCalls } =
              parseAssistantMessageUpdateState(payload, msg)
            return {
              ...msg,
              id: messageId,
              content,
              kind,
              toolCalls,
              ...(modelName ? { modelName } : {}),
              ...(attachments ? { attachments } : {}),
            }
          })
          if (found) {
            return messages
          }

          const { content, kind, toolCalls } =
            parseAssistantMessageUpdateState(payload)

          return [
            ...messages,
            {
              id: messageId,
              role: "assistant" as const,
              content,
              kind,
              toolCalls,
              ...(modelName ? { modelName } : {}),
              ...(attachments ? { attachments } : {}),
              timestamp,
            },
          ]
        })(),
        // While a turn is streaming, an update event is server-side
        // liveness; once idle, clear any stale activity timestamp.
        ...(prev.isTyping
          ? { lastTurnActivityAt: Date.now() }
          : { lastTurnActivityAt: undefined }),
        ...(contextUsage ? { contextUsage } : {}),
      }))
      break
    }

    case "message.delete": {
      const messageId = payload.message_id as string
      if (!messageId) {
        break
      }

      updateChatStore((prev) => ({
        messages: prev.messages.filter((msg) => msg.id !== messageId),
      }))
      break
    }

    case "typing.start":
      updateChatStore((prev) => ({
        isTyping: true,
        ...(prev.turnStartedAt
          ? { lastTurnActivityAt: Date.now() }
          : { turnStartedAt: Date.now(), lastTurnActivityAt: Date.now() }),
      }))
      break

    case "typing.stop":
      updateChatStore((prev) => ({
        isTyping: false,
        turnStartedAt: undefined,
        lastTurnActivityAt: undefined,
        // 封板消息可能不再触发 typing.stop 之后的 create（媒体/直答路径由
        // create 分支冻结），这里兜底冻结一次耗时，避免状态栏永跳。
        ...(prev.turnStartedAt
          ? { turnElapsedMs: Date.now() - prev.turnStartedAt }
          : {}),
      }))
      break

    case "error": {
      const requestId =
        typeof payload.request_id === "string" ? payload.request_id : ""
      const errorMessage =
        typeof payload.message === "string" ? payload.message : ""

      console.error("Pico error:", payload)
      if (errorMessage) {
        toast.error(errorMessage)
      }
      updateChatStore((prev) => ({
        messages: requestId
          ? prev.messages.filter((msg) => msg.id !== requestId)
          : prev.messages,
        isTyping: false,
        turnStartedAt: undefined,
        lastTurnActivityAt: undefined,
        turnStatus: "error" as const,
        ...(prev.turnStartedAt
          ? { turnElapsedMs: Date.now() - prev.turnStartedAt }
          : {}),
      }))
      break
    }

    case "pong":
      break

    default:
      console.log("Unknown pico message type:", message.type)
  }
}
