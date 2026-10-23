import { describe, expect, it } from "vitest"

import { parseContextUsage, parseTurnStats } from "@/features/chat/protocol"

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
