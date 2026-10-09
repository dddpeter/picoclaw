import { atom, getDefaultStore } from "jotai"
import { atomWithStorage } from "jotai/utils"

import {
  ASSISTANT_DETAIL_VISIBILITY_STORAGE_KEY,
  type AssistantDetailVisibility,
  DEFAULT_ASSISTANT_DETAIL_VISIBILITY,
  assistantDetailVisibilityStorage,
  shouldShowAssistantMessage,
} from "@/features/chat/detail-visibility"
import {
  getInitialActiveSessionId,
  writeStoredSessionId,
} from "@/features/chat/state"

export interface ChatAttachment {
  type: "image" | "audio" | "video" | "file"
  url: string
  filename?: string
  contentType?: string
}

export interface ChatToolCallFunction {
  name?: string
  arguments?: string
}

export interface ChatToolCallExtraContent {
  toolFeedbackExplanation?: string
}

export interface ChatToolCall {
  id?: string
  type?: string
  function?: ChatToolCallFunction
  extraContent?: ChatToolCallExtraContent
}

export type AssistantMessageKind =
  | "normal"
  | "thought"
  | "tool_calls"
  /** 非终结性进度提示（心跳/卡死监护通知），不算本轮最终回复。 */
  | "progress_note"

/** HITL 审批询问（v1 协议 payload 或 v0 文本特征解析而来）。 */
export interface ApprovalInfo {
  tool: string
  preview?: string
  /** 超时上限（ms）；v0 兜底解析失败时缺省（倒计时降级为不显示）。 */
  timeoutMs?: number
}

/** 审批卡的封存结论：回执配对 / 超时 / 历史遗留（无法回溯）。 */
export interface ApprovalSeal {
  verdict: "approved" | "always" | "denied" | "timeout" | "done"
  /** 结论时刻（ms epoch）；历史遗留配对无时间时为 0。 */
  at: number
}

export interface ChatMessage {
  id: string
  role: "user" | "assistant"
  content: string
  timestamp: number | string
  kind?: AssistantMessageKind
  modelName?: string
  attachments?: ChatAttachment[]
  toolCalls?: ChatToolCall[]
  /** 本消息是审批询问 → 渲染为审批卡而非 markdown 文本。 */
  approval?: ApprovalInfo
  /** 审批卡的封存结论；未封存 = 待操作。 */
  approvalSeal?: ApprovalSeal
  /** User message queued as steering for the active turn (client-side flag). */
  steering?: boolean
}

export interface ContextUsage {
  used_tokens: number
  total_tokens: number
  history_tokens?: number
  compress_at_tokens: number
  summarize_at_tokens?: number
  used_percent: number
}

/** 回合末次 LLM 用量快照（封板消息 usage 载荷；token 为末次调用口径，与飞书卡片一致）。 */
export interface TurnStats {
  inputTokens: number
  outputTokens: number
  llmCalls?: number
  modelName?: string
}

export type ConnectionState =
  | "disconnected"
  | "connecting"
  | "connected"
  | "error"

export interface ChatStoreState {
  messages: ChatMessage[]
  connectionState: ConnectionState
  isTyping: boolean
  activeSessionId: string
  /** Originating channel of the active session ("pico" for web chat); undefined until known. */
  activeSessionChannel?: string
  hasHydratedActiveSession: boolean
  contextUsage?: ContextUsage
  /** When the current assistant turn started (ms epoch); undefined when idle. */
  turnStartedAt?: number
  /**
   * When the last server-side turn activity was observed (message.create /
   * message.update / typing events, ms epoch); while typing, the difference
   * between now and this timestamp drives the stall hint. undefined when idle.
   */
  lastTurnActivityAt?: number
  /** 已结束回合的耗时快照（ms）；进行中回合用 turnStartedAt 实时计算。 */
  turnElapsedMs?: number
  /** 已结束回合的用量快照（usage 载荷 + 模型名）；进行中回合视为陈旧不展示。 */
  turnStats?: TurnStats
  /** 最近一次回合的结束态（error 仅在收到 error 事件时标记）。 */
  turnStatus?: "done" | "error"
}

type ChatStorePatch = Partial<ChatStoreState>

const DEFAULT_CHAT_STATE: ChatStoreState = {
  messages: [],
  connectionState: "disconnected",
  isTyping: false,
  activeSessionId: getInitialActiveSessionId(),
  hasHydratedActiveSession: false,
}

export const chatAtom = atom<ChatStoreState>(DEFAULT_CHAT_STATE)
export const assistantDetailVisibilityAtom =
  atomWithStorage<AssistantDetailVisibility>(
    ASSISTANT_DETAIL_VISIBILITY_STORAGE_KEY,
    DEFAULT_ASSISTANT_DETAIL_VISIBILITY,
    assistantDetailVisibilityStorage,
    { getOnInit: true },
  )
export const showAssistantDetailsAtom = atom(
  (get) => get(assistantDetailVisibilityAtom) !== "none",
)

const store = getDefaultStore()

export function getChatState() {
  return store.get(chatAtom)
}

export function updateChatStore(
  patch:
    | ChatStorePatch
    | ((prev: ChatStoreState) => ChatStorePatch | ChatStoreState),
) {
  store.set(chatAtom, (prev) => {
    const nextPatch = typeof patch === "function" ? patch(prev) : patch
    const next = { ...prev, ...nextPatch }

    if (next.activeSessionId !== prev.activeSessionId) {
      writeStoredSessionId(next.activeSessionId)
    }

    return next
  })
}

export { shouldShowAssistantMessage, DEFAULT_ASSISTANT_DETAIL_VISIBILITY }
export type { AssistantDetailVisibility }
