// [REQ-007 / 评审 P3-4] isSingleLine 判定口径回归锁。
//
// 为什么锁：该函数决定「单行短代码走内联徽章 vs 完整代码块 chrome」，
// 是渲染形态分流点。评审 P3-4 指出两处边界会产出空徽章 / 判定带偏，
// 修复后必须固定口径以防无声漂移。
//
// 注：32 字符阈值本身尚无规格出处（评审 P1-3，待需求侧确认）；
// 此处只锁**现状边界**——若规格给出权威数值，本文件是唯一需同步的回归锁。
import { describe, expect, it } from "vitest"

import { isSingleLine } from "./message-code-block.utils"

describe("isSingleLine [REQ-007 / 评审 P3-4]", () => {
  it("IL-01 单行短代码 → true", () => {
    expect(isSingleLine("const a = 1")).toBe(true)
  })

  it("IL-02 阈值边界：32 字符为 true、33 字符为 false", () => {
    expect(isSingleLine("x".repeat(32))).toBe(true)
    expect(isSingleLine("x".repeat(33))).toBe(false)
  })

  it("IL-03 多行 → false", () => {
    expect(isSingleLine("a\nb")).toBe(false)
  })

  it("IL-04 空串 → false", () => {
    expect(isSingleLine("")).toBe(false)
  })

  it("IL-05 纯空白 / 纯换行 → false（P3-4：不再渲染空徽章）", () => {
    expect(isSingleLine("   ")).toBe(false)
    expect(isSingleLine("\t")).toBe(false)
    expect(isSingleLine("\n\n")).toBe(false)
  })

  it("IL-06 首尾换行被忽略 → true", () => {
    expect(isSingleLine("\n\ncode\n\n")).toBe(true)
  })

  it("IL-07 CRLF 单行归一后 → true（P3-4）", () => {
    expect(isSingleLine("const a = 1\r\n")).toBe(true)
    expect(isSingleLine("\r\ncode\r\n")).toBe(true)
  })

  it("IL-08 CRLF 多行 → false", () => {
    expect(isSingleLine("a\r\nb")).toBe(false)
  })
})
