import { describe, expect, it } from "vitest"

import type { ChatMessage } from "@/store/chat"

import {
  cacheSessionMessages,
  cachedSessionMessages,
} from "./history"

function msg(id: string, content = "hi"): ChatMessage {
  return { id, role: "user", content, timestamp: 1_700_000_000 }
}

describe("会话消息 LRU 缓存（F8）", () => {
  it("写入后命中，重复写刷新热度", () => {
    cacheSessionMessages("s-a", [msg("a1")])
    expect(cachedSessionMessages("s-a")?.map((m) => m.id)).toEqual(["a1"])
    // 重写同会话：覆盖且热度刷新（不产生双条目）。
    cacheSessionMessages("s-a", [msg("a1"), msg("a2")])
    expect(cachedSessionMessages("s-a")).toHaveLength(2)
  })

  it("容量 8：最旧会话被淘汰，热度刷新保住新会话", () => {
    for (let i = 0; i < 8; i++) {
      cacheSessionMessages(`hot-${i}`, [msg(`m${i}`)])
    }
    // 触碰 hot-0 使其成为最新热度。
    expect(cachedSessionMessages("hot-0")).toHaveLength(1)
    // 再写一个 → 淘汰的是最久未触碰的 hot-1，而非 hot-0。
    cacheSessionMessages("new", [msg("n1")])
    expect(cachedSessionMessages("hot-1")).toBeUndefined()
    expect(cachedSessionMessages("hot-0")).toHaveLength(1)
    expect(cachedSessionMessages("new")).toHaveLength(1)
  })

  it("空消息不写入缓存", () => {
    cacheSessionMessages("empty", [])
    expect(cachedSessionMessages("empty")).toBeUndefined()
  })
})
