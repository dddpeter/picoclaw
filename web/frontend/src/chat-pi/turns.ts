// turn 聚合层：把 picoclaw 扁平的 ChatMessage[]（一个逻辑回合被拆成
// thought / tool_calls / normal 多条消息）归并成 pi-web-ui 的「turn」视图
// 模型 —— 一条用户消息 + 一张助手卡（blocks[]）。
// 设计依据：docs/design/web-chat-pi-rendering.zh.md §8（纯前端推导，零后端改动）。
//
// 工具状态机（picoclaw 协议无服务端工具状态事件，由时序推导）：
//   running      = 无反馈且 turn 仍在流式（isTyping）；
//   waitingModel = 反馈已到且 turn 仍在流式 —— 对应 pi-web-ui 的 tool_status
//                  阶段（表已出、模型还在消化），卡头显示耗时；
//   done         = 反馈已到且 turn 已结束（历史会话一律 done）；
//   idle         = turn 结束但没等到反馈（中断等异常）。
import {
  type AssistantDetailVisibility,
  shouldShowAssistantMessage,
} from "@/features/chat/detail-visibility"
import type {
  A2uiInfo,
  ApprovalInfo,
  ApprovalSeal,
  AskInfo,
  AskSeal,
  ChatAttachment,
  ChatMessage,
} from "@/store/chat"

export interface PiUserPart {
  id: string
  text: string
  attachments?: ChatAttachment[]
  /** 用户在 turn 进行中插入的插问（steering）消息。 */
  steering?: boolean
  timestamp: number
}

export interface PiToolFeedback {
  /** 工具反馈原文（extraContent.toolFeedbackExplanation）。 */
  text: string
  /** 启发式错误判定（isLikelyToolError）。 */
  isError: boolean
  /** 客户端首次观察到反馈的时刻（ms epoch）；历史会话未知 → undefined。 */
  endedAt?: number
}

export type PiBlock =
  | { type: "text"; id: string; text: string; live: boolean }
  | {
      type: "approval"
      id: string
      /** 审批询问（v1 payload / v0 文本解析）。 */
      approval: ApprovalInfo
      /** 封存结论；缺省 = 待操作。 */
      seal?: ApprovalSeal
      /** 询问时刻（ms epoch），倒计时锚点。 */
      timestamp: number
      /** 最近一张未封存卡 = true（可操作）。服务端每会话只有一个
       * waiter，旧 pending 卡的按钮实际作用在最新审批上——禁用防错位。 */
      latest?: boolean
    }
  | {
      type: "ask"
      id: string
      /** ask_user 问询（octop 借鉴 §三）。 */
      ask: AskInfo
      /** 封存结论；缺省 = 待作答。 */
      seal?: AskSeal
      timestamp: number
      latest?: boolean
    }
  | {
      type: "a2ui"
      id: string
      /** A2UI 渲染载荷（render_ui 工具产出，A2uiCard 组件渲染）。 */
      ui: A2uiInfo
      /** 最近一张 a2ui 块 = true（可交互）；其余块为历史版本——动作
       * 回传会带旧 surfaceId/旧 dataModel 值，A2uiCard 拦截并提示
       * （三期任务 2 防错位）。a2ui 无 seal（动作消息没有服务端回执
       * 可封存），被新 render_ui 取代即历史。 */
      latest?: boolean
    }
  | { type: "thinking"; id: string; thinking: string; live: boolean }
  | {
      type: "toolCall"
      id: string
      name: string
      argumentsText?: string
      /** 调用消息的服务器时间戳。 */
      startedAt?: number
      feedback?: PiToolFeedback
    }
  | {
      type: "image"
      id: string
      url: string
      filename?: string
      /** true = 图片（<img>）；false = 音频/视频/文件（链接 chip）。 */
      media: boolean
      /** workspace 相对路径（上传文件 url 为空时启用预览入口，设计 §5.4 来源 a）。 */
      path?: string
    }

export interface PiAssistantPart {
  id: string
  modelName?: string
  timestamp: number
  blocks: PiBlock[]
}

export interface PiTurn {
  id: string
  user?: PiUserPart
  /** 助手卡；turn 尚无任何助手消息（或全被 detail 过滤）时可能缺省。 */
  assistant?: PiAssistantPart
  /** 属于正在流式的 turn（isTyping 且是最后一个 turn）。 */
  active: boolean
}

export interface BuildTurnsOptions {
  /** turn 是否仍在流式（chatAtom.isTyping）。 */
  isTyping: boolean
  /** 「思考与工具调用」展示级别（block 级过滤）。 */
  detail: AssistantDetailVisibility
  /** 注入时钟（测试用）；缺省 Date.now()。 */
  now?: number
}

/**
 * 工具反馈到达时刻的客户端观测注册表。picoclaw 的 message.update 就地更新
 * 反馈且不改消息时间戳，服务器也不给反馈时刻 —— 只能在渲染时观测。
 * 仅用于 waitingModel 阶段的耗时显示（只存在于流式中的 turn），历史会话
 * （turn 一律 done）完全不依赖它。
 */
const feedbackSeenAt = new Map<string, number>()

/**
 * 观测表上界：条目只对「当前流式 turn 的耗时显示」有用，历史重建不会再读。
 * 长驻页面（launcher 常年不刷新）里若无界增长，每个流式工具块都会留下一条
 * 永久记录，故按插入序淘汰最旧项 —— Map 迭代序即插入序。
 */
const FEEDBACK_SEEN_MAX = 500

function observeFeedbackEndedAt(key: string, now: number): number {
  let at = feedbackSeenAt.get(key)
  if (at === undefined) {
    if (feedbackSeenAt.size >= FEEDBACK_SEEN_MAX) {
      const oldest = feedbackSeenAt.keys().next().value
      if (oldest !== undefined) feedbackSeenAt.delete(oldest)
    }
    at = now
    feedbackSeenAt.set(key, at)
  }
  return at
}

/** 测试辅助：清空反馈观测注册表。 */
export function resetFeedbackObservations(): void {
  feedbackSeenAt.clear()
  resetTurnReuseCache()
}

/**
 * turn 身份复用缓存（F1 结构共享）。buildTurns 在每条流式增量上都全量重建
 * PiTurn 数组，而 TurnItem/PiTurnView 的 memo 按对象引用比较——引用每次全新，
 * memo 恒失效，每个 token 触发整个消息列表的 O(N) 组件重渲。
 *
 * 这里在每次构建完成后记录「上一帧的最终形态 turn + 该 turn 消费的消息
 * 引用区间」；下一帧对非末 turn 做区间逐引用比对（protocol.ts 的
 * messages.map 保留未命中消息的对象身份），命中则把新构建的同槽位对象
 * 替换为上一帧对象——值等价（turn 是消息区间 + detail 的纯函数），但引用
 * 稳定 → memo 生效，只有末 turn（isTyping 活跃面）每帧重渲。
 *
 * 复用条件刻意排除含 approval/ask/a2ui 块的 turn：跨 turn 的 latest 标记
 * 扫描会按「更晚的未封存卡」改写早先 turn 的块，其最终形态不纯是本区间
 * 消息的函数。这类 turn 少见，重建即可。
 */
interface TurnReuseEntry {
  turn: PiTurn
  /** 该 turn 是否含 approval/ask/a2ui 块（此类 turn 不参与复用）。 */
  hasCardBlock: boolean
}

let turnReuse: {
  detail: AssistantDetailVisibility
  firstMessageId: string
  messages: ChatMessage[]
  /** 每个 turn 的起始消息下标（与 turns 一一对应；pending 合成 turn 无）。 */
  startIndices: number[]
  turns: TurnReuseEntry[]
} | null = null

function resetTurnReuseCache(): void {
  turnReuse = null
}

/** 测试辅助：上一帧复用缓存的引用数（供身份断言）。 */
export function turnReuseCacheSize(): number {
  return turnReuse?.turns.length ?? 0
}

const UNIX_MS_THRESHOLD = 1e12

/** ChatMessage.timestamp（number | string）→ ms epoch；解析失败返回 0。 */
export function parseTimestampMs(timestamp: number | string): number {
  if (typeof timestamp === "number") {
    return timestamp < UNIX_MS_THRESHOLD ? timestamp * 1000 : timestamp
  }
  const trimmed = timestamp.trim()
  if (/^-?\d+(\.\d+)?$/.test(trimmed)) {
    const numeric = Number(trimmed)
    if (Number.isFinite(numeric)) {
      return numeric < UNIX_MS_THRESHOLD ? numeric * 1000 : numeric
    }
  }
  const parsed = Date.parse(timestamp)
  return Number.isFinite(parsed) ? parsed : 0
}

const ERROR_FIRST_LINE_RE = /^(error|failed|failure|panic|⛔)/i
const EXIT_CODE_NONZERO_RE = /exit code [1-9]\d*/i

/**
 * 工具反馈的错误启发式（设计文档 §8）：
 * 首个非空行以 error/failed/failure/panic/⛔ 开头，或正文含非零 exit code。
 */
export function isLikelyToolError(text: string): boolean {
  const firstLine = text.split("\n").find((line) => line.trim()) ?? ""
  if (ERROR_FIRST_LINE_RE.test(firstLine.trim())) return true
  return EXIT_CODE_NONZERO_RE.test(text)
}

/** block → AssistantMessageKind 映射（复用 detail-visibility 的过滤口径）。 */
function blockDetailKind(block: PiBlock): "normal" | "thought" | "tool_calls" {
  switch (block.type) {
    case "thinking":
      return "thought"
    case "toolCall":
      return "tool_calls"
    default:
      return "normal"
  }
}

function attachmentBlocks(
  messageId: string,
  attachments: ChatAttachment[] | undefined,
): PiBlock[] {
  if (!attachments || attachments.length === 0) return []
  return attachments.map((attachment, index) => ({
    type: "image" as const,
    id: `${messageId}#att${index}`,
    url: attachment.url,
    filename: attachment.filename,
    media: attachment.type === "image",
    path: attachment.path,
  }))
}

/**
 * 归并规则：
 *  - 非插问的用户消息开新 turn；
 *  - 插问（steering）用户消息也开新 turn（渲染为独立用户卡，其后助手块并入）；
 *  - 助手消息追加进当前 turn 的助手卡（无当前 turn 时开无用户头的合成 turn）；
 *  - 末 turn 且 isTyping → active（块 live、工具 running/waitingModel、流式光标）。
 */
export function buildTurns(
  messages: ChatMessage[],
  opts: BuildTurnsOptions,
): PiTurn[] {
  const now = opts.now ?? Date.now()
  const turns: PiTurn[] = []
  const startIndices: number[] = []
  let current: PiTurn | null = null
  let walkIndex = -1

  const openTurn = (turn: PiTurn): void => {
    turns.push(turn)
    startIndices.push(walkIndex)
    current = turn
  }

  const ensureAssistant = (message: ChatMessage): PiAssistantPart => {
    if (!current) {
      // 首条就是助手消息（历史从半截开始）：开一个无用户头的合成 turn。
      openTurn({ id: `head-${message.id}`, active: false })
    }
    const turn = current
    if (!turn) throw new Error("buildTurns: openTurn 必须已设置 current")
    if (!turn.assistant) {
      turn.assistant = {
        id: message.id,
        ...(message.modelName ? { modelName: message.modelName } : {}),
        timestamp: parseTimestampMs(message.timestamp),
        blocks: [],
      }
    } else if (message.modelName && !turn.assistant.modelName) {
      turn.assistant.modelName = message.modelName
    }
    return turn.assistant
  }

  const lastIndex = messages.length - 1
  messages.forEach((message, index) => {
    walkIndex = index
    // 本条消息是否处于流式（live 块 / running 工具只可能出现在这里）。
    const messageLive =
      opts.isTyping && index === lastIndex && message.role === "assistant"

    if (message.role === "user") {
      openTurn({
        id: message.id,
        active: false,
        user: {
          id: message.id,
          text: message.content,
          ...(message.attachments ? { attachments: message.attachments } : {}),
          ...(message.steering ? { steering: true } : {}),
          timestamp: parseTimestampMs(message.timestamp),
        },
      })
      return
    }

    const assistant = ensureAssistant(message)
    // 审批询问整条消息渲染为审批卡（替代 markdown 文本块）。
    if (message.approval) {
      assistant.blocks.push({
        type: "approval",
        id: `${message.id}#approval`,
        approval: message.approval,
        ...(message.approvalSeal ? { seal: message.approvalSeal } : {}),
        timestamp: parseTimestampMs(message.timestamp),
      })
      return
    }
    // ask_user 问询同理：整条消息渲染为问询卡（octop 借鉴 §三）。
    if (message.ask) {
      assistant.blocks.push({
        type: "ask",
        id: `${message.id}#ask`,
        ask: message.ask,
        ...(message.askSeal ? { seal: message.askSeal } : {}),
        timestamp: parseTimestampMs(message.timestamp),
      })
      return
    }
    const toolCalls = message.toolCalls ?? []

    if (message.kind === "thought" && toolCalls.length === 0) {
      assistant.blocks.push({
        type: "thinking",
        id: message.id,
        thinking: message.content,
        live: messageLive,
      })
      return
    }

    if (toolCalls.length > 0) {
      toolCalls.forEach((toolCall, callIndex) => {
        const explanation =
          toolCall.extraContent?.toolFeedbackExplanation?.trim() ?? ""
        const feedbackKey = `${message.id}#${toolCall.id ?? callIndex}`
        assistant.blocks.push({
          type: "toolCall",
          id: feedbackKey,
          name: toolCall.function?.name?.trim() ?? "",
          ...(toolCall.function?.arguments
            ? { argumentsText: toolCall.function.arguments }
            : {}),
          startedAt: parseTimestampMs(message.timestamp),
          ...(explanation
            ? {
                feedback: {
                  text: explanation,
                  isError: isLikelyToolError(explanation),
                  // endedAt 仅对活跃流式 turn 可信（评审 P2-4）：历史会话的反馈
                  // 时刻服务端不提供，若在此写 now，会与很旧的 startedAt 相减
                  // 得出巨型时长（当前仅靠 waitingModel 渲染条件兜住）。与
                  // PiToolFeedback.endedAt 的可选语义保持一致：历史 → 不登记，
                  // 同时避免模块级 feedbackSeenAt 被历史消息无界污染。
                  ...(messageLive
                    ? { endedAt: observeFeedbackEndedAt(feedbackKey, now) }
                    : {}),
                },
              }
            : {}),
        })
      })
      // tool_calls 消息的 content 通常为空；legacy 🔧 卡片已被解析进 toolCalls。
      const text = message.content.trim()
      if (text && message.kind !== "tool_calls") {
        assistant.blocks.push({
          type: "text",
          id: `${message.id}#text`,
          text: message.content,
          live: messageLive,
        })
      }
      return
    }

    // 普通文本消息（含 media.create 的空 content → 只剩附件块）。
    if (message.content) {
      assistant.blocks.push({
        type: "text",
        id: message.id,
        text: message.content,
        live: messageLive,
      })
    }
    // A2UI 渲染块：跟在 text 块之后（无文本时单独成块）。
    if (message.ui) {
      assistant.blocks.push({
        type: "a2ui",
        id: `${message.id}#a2ui`,
        ui: message.ui,
      })
    }
    assistant.blocks.push(...attachmentBlocks(message.id, message.attachments))
  })

  // detail 过滤（thought / tool_calls 级别；text/image 恒显示）。
  for (const turn of turns) {
    if (!turn.assistant) continue
    turn.assistant.blocks = turn.assistant.blocks.filter((block) =>
      shouldShowAssistantMessage(opts.detail, blockDetailKind(block)),
    )
  }

  // active 标记 + 占位助手卡（isTyping 但末 turn 还没有任何助手块）。
  const last = turns[turns.length - 1]
  if (last) {
    last.active = opts.isTyping
    if (last.active && !last.assistant) {
      last.assistant = {
        id: `${last.id}#pending`,
        timestamp: last.user?.timestamp ?? now,
        blocks: [],
      }
    }
  }
  if (opts.isTyping && turns.length === 0) {
    turns.push({
      id: "pending",
      active: true,
      assistant: { id: "pending", timestamp: now, blocks: [] },
    })
  }

  // 审批/问询/A2UI 卡可操作性：每类只把最近一张未封存卡标为 latest，其余
  // 未封存卡保持 undefined（组件侧禁用/拦截）。审批/问询：服务端每会话
  // 同类只有一个 waiter，旧 pending 卡的提交会错位作用于最新 waiter
  // （超时幽灵卡 + 新卡并存是最常见的错位场景）。a2ui：回传带旧
  // surfaceId/旧 dataModel 值（三期任务 2 防错位）。
  for (const blockType of ["approval", "ask", "a2ui"] as const) {
    let latestMarked = false
    for (let t = turns.length - 1; t >= 0 && !latestMarked; t--) {
      const blocks = turns[t].assistant?.blocks
      if (!blocks) continue
      for (let b = blocks.length - 1; b >= 0; b--) {
        const block = blocks[b]
        if (block.type !== blockType) continue
        // a2ui 无 seal（动作消息没有服务端回执）——永远"未封存"。
        if ("seal" in block && block.seal) continue
        block.latest = true
        latestMarked = true
        break
      }
    }
  }

  // F1 身份复用：非末 turn 且消费的消息区间与上一帧逐引用一致时，用上一帧
  // 的最终形态对象替换本帧新对象（值等价、引用稳定 → 下游 memo 生效）。
  // 末 turn 永不复用（isTyping/active/pending 都作用于它）。
  const prev = turnReuse
  if (
    prev !== null &&
    prev.detail === opts.detail &&
    (prev.messages[0]?.id ?? "") === (messages[0]?.id ?? "")
  ) {
    for (let i = 0; i < turns.length - 1 && i < startIndices.length; i++) {
      const prevEntry = prev.turns[i]
      if (!prevEntry || prevEntry.hasCardBlock) continue
      const start = startIndices[i]
      const prevStart = prev.startIndices[i]
      if (prevStart === undefined) continue
      const end = i + 1 < startIndices.length ? startIndices[i + 1] : messages.length
      const prevEnd =
        i + 1 < prev.startIndices.length ? prev.startIndices[i + 1] : prev.messages.length
      if (end - start !== prevEnd - prevStart) continue
      let match = true
      for (let k = 0; k < end - start; k++) {
        if (prev.messages[prevStart + k] !== messages[start + k]) {
          match = false
          break
        }
      }
      if (match) {
        turns[i] = prevEntry.turn
      }
    }
  }
  turnReuse = {
    detail: opts.detail,
    firstMessageId: messages[0]?.id ?? "",
    messages,
    startIndices,
    turns: turns.map((turn) => ({
      turn,
      hasCardBlock: (turn.assistant?.blocks ?? []).some(
        (block) =>
          block.type === "approval" || block.type === "ask" || block.type === "a2ui",
      ),
    })),
  }

  return turns
}
