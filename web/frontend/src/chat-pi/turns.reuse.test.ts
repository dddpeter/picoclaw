import { describe, expect, it } from "vitest"

import type { ChatMessage } from "@/store/chat"

import { buildTurns, resetFeedbackObservations, turnReuseCacheSize } from "./turns"

function user(id: string, content = "hi"): ChatMessage {
  return { id, role: "user", content, timestamp: 1_700_000_000 }
}

function assistant(id: string, content = ""): ChatMessage {
  return { id, role: "assistant", content, timestamp: 1_700_000_001 }
}

const ALL = { isTyping: false, detail: "all" } as const

describe("buildTurns 身份复用（F1 结构共享）", () => {
  it("未触达区间复用上一帧的 turn 对象，流式只重建末 turn", () => {
    resetFeedbackObservations()
    const u1 = user("u1", "第一问")
    const a1 = assistant("a1", "第一答")
    const u2 = user("u2", "第二问")
    const a2 = assistant("a2", "第二答")
    const first = buildTurns([u1, a1, u2, a2], ALL)
    expect(turnReuseCacheSize()).toBe(2)

    // 流式追加一条助手消息（协议层 map 保留前四条消息的对象身份）；
    // 助手消息并入当前末 turn。
    const a3 = assistant("a3", "第二答续…")
    const second = buildTurns([u1, a1, u2, a2, a3], { isTyping: true, detail: "all" })

    expect(second[0]).toBe(first[0]) // 历史 turn：引用稳定，memo 生效
    expect(second[1]).not.toBe(first[1]) // 末 turn（活跃面）每帧重建
    // 值等价：复用不是偷懒换语义。
    expect(second[0]).toEqual(first[0])
    expect(second[1].assistant?.blocks).toHaveLength(2)
    expect(second[1].assistant?.blocks[1]).toMatchObject({ text: "第二答续…", live: true })
  })

  it("消息对象被替换（而非追加）时区间失配，对应 turn 重建", () => {
    resetFeedbackObservations()
    const u1 = user("u1", "问")
    const a1 = assistant("a1", "答")
    buildTurns([u1, a1], ALL)

    // 协议层对命中消息生成新对象：内容相同但引用不同 → 不得复用旧 turn。
    const a1Edited = assistant("a1", "答（编辑后）")
    const rebuilt = buildTurns([u1, a1Edited], ALL)
    expect(rebuilt[0].assistant?.blocks[0]).toMatchObject({ text: "答（编辑后）" })
  })

  it("detail 变化触发整表重建，不沿用旧 detail 的过滤结果", () => {
    resetFeedbackObservations()
    const u1 = user("u1", "问")
    const a1 = assistant("a1", "答")
    const all = buildTurns([u1, a1], ALL)

    const none = buildTurns([u1, a1], { isTyping: false, detail: "none" })
    expect(none[0]).not.toBe(all[0])

    // 切回 detail=all：与最近一帧（none）不同 detail → 重建（允许引用不同）。
    const again = buildTurns([u1, a1], ALL)
    expect(again[0]).not.toBe(none[0])
  })

  it("换会话（首消息 id 变化）后不复用旧会话的 turn", () => {
    resetFeedbackObservations()
    const u1 = user("u1", "会话一")
    const a1 = assistant("a1", "答一")
    const session1 = buildTurns([u1, a1], ALL)

    const u2 = user("u2", "会话二")
    const a2 = assistant("a2", "答二")
    const session2 = buildTurns([u2, a2], ALL)
    expect(session2[0]).not.toBe(session1[0])
    expect(session2[0].user?.text).toBe("会话二")
  })
})
