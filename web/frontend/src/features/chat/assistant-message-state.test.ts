import { describe, expect, it } from "vitest"

import { getChatState, updateChatStore } from "@/store/chat"

import {
  parseAssistantMessageCreateState,
  parseAssistantMessageUpdateState,
} from "./assistant-message-state"
import { handlePicoMessage } from "./protocol"

// kind 推导（审批/问询/a2ui 卡）：这些卡在 wire 上不带 kind（TUI 等旧客户端
// 要把卡当普通消息进时间线），web 前端由 payload 推导为 progress_note——
// 非终结帧，turn 不能被提前终结。
describe("parseAssistantMessageCreateState kind 推导", () => {
  it("derives progress_note for an a2ui payload (mid-turn render)", () => {
    const state = parseAssistantMessageCreateState({
      message_id: "m1",
      content: "交互视图",
      a2ui: [{ version: "v0.9" }],
    })
    expect(state.kind).toBe("progress_note")
    expect(state.toolCalls).toBeUndefined()
  })

  it("derives progress_note for ask / approval payloads (existing parity)", () => {
    expect(
      parseAssistantMessageCreateState({
        ask: { questions: [{ question: "q" }] },
      }).kind,
    ).toBe("progress_note")
    expect(
      parseAssistantMessageCreateState({ approval: { tool: "shell" } }).kind,
    ).toBe("progress_note")
  })

  it("keeps normal for payloads without kind or card markers", () => {
    expect(parseAssistantMessageCreateState({ content: "hi" }).kind).toBe(
      "normal",
    )
  })

  it("lets an explicit wire kind win over the a2ui derivation", () => {
    expect(
      parseAssistantMessageCreateState({ kind: "thought", a2ui: [{}] }).kind,
    ).toBe("thought")
  })

  it("keeps an a2ui message non-normal across later kind-less updates", () => {
    const updated = parseAssistantMessageUpdateState(
      { content: "still rendering" },
      { kind: "progress_note", toolCalls: undefined },
    )
    expect(updated.kind).toBe("progress_note")
  })
})

// 端到端回归（终审 Important 1）：中途 render_ui 发布的 a2ui message.create
// 不得把 turn 提前终结——isTyping 保持、turnStatus 不落 done；普通文本
// 消息（无 kind、无卡标记）仍是终结帧。经全局 chatAtom 快照/恢复隔离。
describe("handlePicoMessage a2ui turn 终结语义", () => {
  const setupActiveTurn = () => {
    updateChatStore({
      messages: [],
      isTyping: true,
      turnStartedAt: 1_000,
      lastTurnActivityAt: 1_000,
      turnStatus: undefined,
      turnStats: undefined,
      turnElapsedMs: undefined,
    })
  }

  it("a mid-turn a2ui card keeps the turn running", () => {
    const snapshot = getChatState()
    try {
      setupActiveTurn()

      handlePicoMessage(
        {
          type: "message.create",
          session_id: "s1",
          timestamp: 1_700_000_000,
          payload: {
            message_id: "a2ui-1",
            content: "交互视图",
            a2ui: [{ version: "v0.9", createSurface: { surfaceId: "s" } }],
          },
        },
        "s1",
      )

      const state = getChatState()
      expect(state.isTyping).toBe(true)
      expect(state.turnStatus).toBeUndefined()
      expect(state.messages).toHaveLength(1)
      expect(state.messages[0]).toMatchObject({
        id: "a2ui-1",
        kind: "progress_note",
        ui: {
          messages: [{ version: "v0.9", createSurface: { surfaceId: "s" } }],
        },
      })
    } finally {
      updateChatStore(snapshot)
    }
  })

  it("a plain kind-less message still finishes the turn", () => {
    const snapshot = getChatState()
    try {
      setupActiveTurn()

      handlePicoMessage(
        {
          type: "message.create",
          session_id: "s1",
          timestamp: 1_700_000_000,
          payload: { message_id: "plain-1", content: "最终答复" },
        },
        "s1",
      )

      const state = getChatState()
      expect(state.isTyping).toBe(false)
      expect(state.turnStatus).toBe("done")
      expect(state.messages[0]?.kind).toBe("normal")
    } finally {
      updateChatStore(snapshot)
    }
  })
})
