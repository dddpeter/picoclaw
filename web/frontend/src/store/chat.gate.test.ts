import { describe, expect, it } from "vitest"

import { getChatState, updateChatStore } from "./chat"

describe("updateChatStore 相等性熔断（F2）", () => {
  it("同值 patch 返回原 state 引用，不触发订阅者", () => {
    updateChatStore({ messages: [], isTyping: false })
    const before = getChatState()

    updateChatStore({ isTyping: false })
    updateChatStore({ messages: before.messages })
    updateChatStore(() => ({ connectionState: before.connectionState }))

    expect(getChatState()).toBe(before)
  })

  it("任一字段实际变化才产生新对象", () => {
    updateChatStore({ messages: [], isTyping: false })
    const before = getChatState()

    updateChatStore({ isTyping: true })

    expect(getChatState()).not.toBe(before)
    expect(getChatState().isTyping).toBe(true)

    updateChatStore({ isTyping: false })
    expect(getChatState().isTyping).toBe(false)
    expect(getChatState()).not.toBe(before)
  })
})
