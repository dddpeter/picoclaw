import { describe, expect, it } from "vitest"

import { mergeHistoryMessages } from "@/features/chat/history"
import {
  handlePicoMessage,
  parseA2uiFromPayload,
  parseContextUsage,
  parseTurnStats,
} from "@/features/chat/protocol"
import { getChatState, updateChatStore } from "@/store/chat"

// 分段解析（octop 借鉴 §五，评审 P3-8）：context_usage.segments 的 wire 解析
// 是 web 环分段堆叠条的数据源——畸形条目必须被丢弃而非炸掉整个环。
const wrap = (usage: Record<string, unknown>) => ({
  context_usage: usage,
})

describe("parseContextUsage segments", () => {
  it("parses valid segments and preserves order", () => {
    const usage = parseContextUsage(
      wrap({
        used_tokens: 1000,
        total_tokens: 4000,
        history_tokens: 400,
        segments: [
          { key: "system_prompt", tokens: 300 },
          { key: "conversation", tokens: 400 },
          { key: "mcp", tokens: 300 },
        ],
      }),
    )
    expect(usage?.segments).toEqual([
      { key: "system_prompt", tokens: 300 },
      { key: "conversation", tokens: 400 },
      { key: "mcp", tokens: 300 },
    ])
  })

  it("drops malformed segment entries, keeps the valid ones", () => {
    const usage = parseContextUsage(
      wrap({
        used_tokens: 100,
        total_tokens: 400,
        segments: [
          { key: "mcp", tokens: 100 },
          { tokens: 50 }, // missing key
          { key: "", tokens: 50 }, // empty key
          { key: "skills", tokens: Number.NaN }, // NaN tokens
          "junk",
          null,
        ],
      }),
    )
    expect(usage?.segments).toEqual([{ key: "mcp", tokens: 100 }])
  })

  it("omits segments when absent, not an array, or fully malformed", () => {
    expect(
      parseContextUsage(wrap({ used_tokens: 1, total_tokens: 2 }))?.segments,
    ).toBeUndefined()
    expect(
      parseContextUsage(
        wrap({ used_tokens: 1, total_tokens: 2, segments: "no" }),
      )?.segments,
    ).toBeUndefined()
    expect(
      parseContextUsage(
        wrap({ used_tokens: 1, total_tokens: 2, segments: [{}] }),
      )?.segments,
    ).toBeUndefined()
  })

  it("keeps zero-token entries (the ring filters them at render time)", () => {
    const usage = parseContextUsage(
      wrap({
        used_tokens: 50,
        total_tokens: 400,
        segments: [
          { key: "skills", tokens: 0 },
          { key: "conversation", tokens: 50 },
        ],
      }),
    )
    expect(usage?.segments).toEqual([
      { key: "skills", tokens: 0 },
      { key: "conversation", tokens: 50 },
    ])
  })

  it("returns undefined without finite used/total", () => {
    expect(parseContextUsage(wrap({}))).toBeUndefined()
    expect(
      parseContextUsage(wrap({ used_tokens: 1, total_tokens: 0 })),
    ).toBeUndefined()
    expect(
      parseContextUsage(wrap({ used_tokens: Number.NaN, total_tokens: 2 })),
    ).toBeUndefined()
  })
})

// a2ui 载荷（A2UI Web 渲染）：最小形状检查——数组且非空才挂 ui，形状不对
// 忽略该键让消息按普通 markdown 渲染，畸形 payload 不能炸掉聊天流。
describe("parseA2uiFromPayload", () => {
  it("attaches ui for a non-empty a2ui array, passing messages through", () => {
    const messages = [{ type: "container", id: "c1" }]
    expect(parseA2uiFromPayload({ a2ui: messages })).toEqual({ messages })
  })

  it("ignores the key when absent, null, not an array, or empty", () => {
    expect(parseA2uiFromPayload({})).toBeUndefined()
    expect(parseA2uiFromPayload({ a2ui: null })).toBeUndefined()
    expect(parseA2uiFromPayload({ a2ui: "no" })).toBeUndefined()
    expect(parseA2uiFromPayload({ a2ui: [] })).toBeUndefined()
  })
})

// 封板 usage（含缓存命中）：cached_tokens 含于 input_tokens，仅 >0 时携带。
const wrapTurnUsage = (
  usage: Record<string, unknown>,
  payload: Record<string, unknown> = {},
) => ({ usage, ...payload })

describe("parseTurnStats", () => {
  it("parses input/output/cached/llm_calls/model_name together", () => {
    const stats = parseTurnStats(
      wrapTurnUsage(
        {
          input_tokens: 1200,
          output_tokens: 340,
          cached_tokens: 900,
          llm_calls: 3,
        },
        { model_name: "glm-5.3" },
      ),
    )
    expect(stats).toEqual({
      inputTokens: 1200,
      outputTokens: 340,
      cachedTokens: 900,
      llmCalls: 3,
      modelName: "glm-5.3",
    })
  })

  it("omits cachedTokens/llmCalls when absent or zero", () => {
    expect(
      parseTurnStats(
        wrapTurnUsage({
          input_tokens: 100,
          output_tokens: 50,
          cached_tokens: 0,
        }),
      ),
    ).toEqual({ inputTokens: 100, outputTokens: 50 })
  })

  it("drops non-positive or malformed cached values", () => {
    expect(
      parseTurnStats(
        wrapTurnUsage({
          input_tokens: 100,
          output_tokens: 50,
          cached_tokens: -5,
        }),
      ),
    ).not.toHaveProperty("cachedTokens")
    expect(
      parseTurnStats(
        wrapTurnUsage({
          input_tokens: 100,
          output_tokens: 50,
          cached_tokens: "x",
        }),
      ),
    ).not.toHaveProperty("cachedTokens")
  })

  it("returns undefined without a usage object or finite input/output", () => {
    expect(parseTurnStats({})).toBeUndefined()
    expect(parseTurnStats(wrapTurnUsage({ output_tokens: 5 }))).toBeUndefined()
    expect(
      parseTurnStats(
        wrapTurnUsage({ input_tokens: Number.NaN, output_tokens: 5 }),
      ),
    ).toBeUndefined()
  })
})

// message.update consuming a2ui (phase 2, task 3): the backend merges
// update-only frames into the already broadcast card's payload and broadcasts
// a message.update carrying the merged batch (task 5); the frontend swaps the
// card data in place. The replacement must be immutable (fresh ui object /
// fresh messages array reference) — A2uiCard's useMemo depends on the
// ui.messages reference, so in-place mutation would never recompute. Updates
// without the a2ui key or with a malformed shape (non-array / empty array)
// leave the existing card and the 5-field update semantics untouched
// (regression red line).
describe("handlePicoMessage message.update a2ui", () => {
  const seedCardMessage = (batch: unknown[]) => {
    handlePicoMessage(
      {
        type: "message.create",
        session_id: "s1",
        timestamp: 1_700_000_000,
        payload: {
          message_id: "card-1",
          content: "交互视图",
          a2ui: batch,
        },
      },
      "s1",
    )
  }

  const sendUpdate = (payload: Record<string, unknown>) => {
    handlePicoMessage(
      {
        type: "message.update",
        session_id: "s1",
        timestamp: 1_700_000_060,
        payload,
      },
      "s1",
    )
  }

  it("replaces ui with the new batch immutably when the update carries a valid a2ui", () => {
    const snapshot = getChatState()
    try {
      const initialBatch = [
        { version: "v0.9", createSurface: { surfaceId: "s" } },
      ]
      seedCardMessage(initialBatch)

      const before = getChatState().messages[0]
      const beforeUi = before.ui
      expect(beforeUi?.messages).toEqual(initialBatch)

      const mergedBatch = [
        { version: "v0.9", createSurface: { surfaceId: "s" } },
        { version: "v0.9", updateComponents: { surfaceId: "s" } },
      ]
      sendUpdate({
        message_id: "card-1",
        content: "交互视图（更新）",
        a2ui: mergedBatch,
      })

      const state = getChatState()
      expect(state.messages).toHaveLength(1)
      const after = state.messages[0]
      // Card data swapped to the merged batch...
      expect(after.ui?.messages).toEqual(mergedBatch)
      // ...as an immutable replacement: fresh ui object and fresh messages
      // array reference so A2uiCard's [ui.messages] useMemo recomputes.
      expect(after.ui).not.toBe(beforeUi)
      expect(after.ui?.messages).not.toBe(beforeUi?.messages)
      // Untouched dimensions survive the spread (no accidental reset).
      expect(after.content).toBe("交互视图（更新）")
      expect(after.kind).toBe("progress_note")
      expect(after.timestamp).toBe(before.timestamp)
    } finally {
      updateChatStore(snapshot)
    }
  })

  it("keeps the existing card and the 5-field semantics when the update has no a2ui key", () => {
    const snapshot = getChatState()
    try {
      const batch = [{ version: "v0.9", createSurface: { surfaceId: "s" } }]
      seedCardMessage(batch)

      const beforeUi = getChatState().messages[0].ui
      sendUpdate({
        message_id: "card-1",
        content: "内容更新",
        model_name: "glm-5.3",
        attachments: [{ url: "https://example.com/a.png", type: "image" }],
      })

      const after = getChatState().messages[0]
      // Regression red line: ui untouched (same reference, not even cloned).
      expect(after.ui).toBe(beforeUi)
      // The 5 existing update fields keep their semantics.
      expect(after.content).toBe("内容更新")
      expect(after.modelName).toBe("glm-5.3")
      expect(after.attachments).toEqual([
        { type: "image", url: "https://example.com/a.png" },
      ])
      expect(after.kind).toBe("progress_note")
    } finally {
      updateChatStore(snapshot)
    }
  })

  it("updates plain text messages exactly as before (no ui key appears)", () => {
    const snapshot = getChatState()
    try {
      handlePicoMessage(
        {
          type: "message.create",
          session_id: "s1",
          timestamp: 1_700_000_000,
          payload: { message_id: "plain-1", content: "第一版" },
        },
        "s1",
      )
      sendUpdate({ message_id: "plain-1", content: "第二版" })

      const after = getChatState().messages[0]
      expect(after.content).toBe("第二版")
      expect("ui" in after).toBe(false)
    } finally {
      updateChatStore(snapshot)
    }
  })

  it("ignores a malformed a2ui key without disturbing the existing card", () => {
    const snapshot = getChatState()
    try {
      const batch = [{ version: "v0.9", createSurface: { surfaceId: "s" } }]
      seedCardMessage(batch)
      const beforeUi = getChatState().messages[0].ui

      for (const bad of [null, "no", [], { broken: true }, 42]) {
        sendUpdate({
          message_id: "card-1",
          content: "畸形批次",
          a2ui: bad,
        })
        const after = getChatState().messages[0]
        expect(after.ui).toBe(beforeUi)
        expect(after.content).toBe("畸形批次")
      }
    } finally {
      updateChatStore(snapshot)
    }
  })

  it("attaches ui when an update for an unseen id creates the message", () => {
    const snapshot = getChatState()
    try {
      updateChatStore({ messages: [] })
      const batch = [{ version: "v0.9", createSurface: { surfaceId: "s" } }]
      sendUpdate({ message_id: "late-1", content: "迟到更新", a2ui: batch })

      const state = getChatState()
      expect(state.messages).toHaveLength(1)
      expect(state.messages[0]).toMatchObject({
        id: "late-1",
        content: "迟到更新",
        ui: { messages: batch },
      })
    } finally {
      updateChatStore(snapshot)
    }
  })
})

// messageSignature gains the ui dimension (phase 2, task 3; fixes phase-1
// deferred minor #18): the REST history copy and the WS live copy of the same
// logical message must only dedupe when their card matches too — same-frame
// replays parse to equal-length batches and still dedupe, while a hot-updated
// merged batch with a different length keeps both copies (prefer a visible
// duplicate over silently dropping one); the backend closes that window by
// folding history (task 4) the same way it merges live updates (task 5).
describe("mergeHistoryMessages ui dimension", () => {
  const base = {
    role: "assistant" as const,
    content: "hi",
    timestamp: 1_700_000_000,
  }

  it("still dedupes same-content copies without ui", () => {
    const merged = mergeHistoryMessages(
      [{ ...base, id: "hist-1" }],
      [{ ...base, id: "live-1" }],
    )
    expect(merged).toHaveLength(1)
    expect(merged[0].id).toBe("live-1")
  })

  it("dedupes when both copies carry an equal-length batch", () => {
    const merged = mergeHistoryMessages(
      [
        {
          ...base,
          id: "hist-1",
          ui: { messages: [{ createSurface: { surfaceId: "s" } }, {}] },
        },
      ],
      [
        {
          ...base,
          id: "live-1",
          ui: { messages: [{ version: "v0.9" }, { updateComponents: {} }] },
        },
      ],
    )
    expect(merged).toHaveLength(1)
    expect(merged[0].id).toBe("live-1")
  })

  it("keeps both copies when the batch length differs (hot-updated vs stale)", () => {
    const merged = mergeHistoryMessages(
      [{ ...base, id: "hist-1", ui: { messages: [{}] } }],
      [
        {
          ...base,
          id: "live-1",
          ui: { messages: [{}, {}, {}] },
        },
      ],
    )
    expect(merged).toHaveLength(2)
  })

  it("keeps both copies when only one side carries a card", () => {
    const merged = mergeHistoryMessages(
      [{ ...base, id: "hist-1", ui: { messages: [{}] } }],
      [{ ...base, id: "live-1" }],
    )
    expect(merged).toHaveLength(2)
  })
})
