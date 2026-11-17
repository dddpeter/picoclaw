// [REQ-019] 复制交互「收敛到单一 hook」纪律单测（cut-5 的核心断言之一）。
//
// 为什么是「源码文本断言」：REQ-019 验收标准是「两个组件都走同一 hook、节律
// 单一来源」，这属于**代码结构不变量**，行为断言无法区分「真收敛」与「各自抄一份
// 同样 2000ms 的实现」——旧实现正是各抄一份（CopyButton 1200ms / MessageActionsBar
// 1200ms）。故此处直接读源码断言结构与节律常量，作为回归锁。
//
// 环境：纯文本断言，无需 jsdom（走默认 node 环境）；源码经 Vite `?raw` 载入为字符串，
// 不引入 node:fs（tsconfig.app.json 的 types 仅含 vite/client，Node 类型不可用）。
import { describe, expect, it } from "vitest"

import copyButtonSrc from "../chat-pi/copy-button.tsx?raw"
import actionsBarSrc from "../components/chat/message-actions-bar.tsx?raw"
import hookSrc from "./use-copy-to-clipboard.ts?raw"

/** 去掉注释再断言：避免注释里出现的字面量（如「2000ms 恢复节律」）造成误判。
 *  这三个文件不含带 `//` 的字符串字面量，故简易剥离是安全的（已核对）。 */
function code(source: string): string {
  return source.replace(/\/\*[\s\S]*?\*\//g, "").replace(/\/\/[^\n]*/g, "")
}

const consumers = [
  { name: "copy-button.tsx", src: copyButtonSrc },
  { name: "message-actions-bar.tsx", src: actionsBarSrc },
] as const

describe("复制交互收敛纪律 [REQ-019]", () => {
  it("CV-01 两个组件都从 @/hooks/use-copy-to-clipboard 引入 hook", () => {
    for (const { name, src } of consumers) {
      expect(code(src), name).toMatch(
        /import \{ useCopyToClipboard \} from "@\/hooks\/use-copy-to-clipboard"/,
      )
    }
  })

  it("CV-02 两个组件都不再直接依赖 @/lib/clipboard（回归锁：不得绕过 hook）", () => {
    for (const { name, src } of consumers) {
      expect(code(src), name).not.toContain("@/lib/clipboard")
      expect(code(src), name).not.toMatch(/\bcopyText\b/)
    }
  })

  it("CV-03 两个组件都不再手写复位定时器（setTimeout 归零）", () => {
    for (const { name, src } of consumers) {
      expect(code(src), name).not.toMatch(/\bsetTimeout\s*\(/)
      expect(code(src), name).not.toMatch(/\bclearTimeout\s*\(/)
    }
  })

  it("CV-04 旧 1200ms 节律常量已无残留", () => {
    for (const { name, src } of consumers) {
      expect(code(src), name).not.toMatch(/\b1200\b/)
    }
  })

  it("CV-05 两个组件都以无参形式调用 useCopyToClipboard()（节律单一来源，不得各自覆写延迟）", () => {
    for (const { name, src } of consumers) {
      expect(code(src), name).toMatch(/useCopyToClipboard\(\s*\)/)
      expect(code(src), name).not.toMatch(/useCopyToClipboard\(\s*\d/)
    }
  })

  it("CV-06 hook 是节律唯一来源：默认 2000ms，且成功态复位计时器在重复复制时被清除", () => {
    const src = code(hookSrc)
    expect(src).toContain("const DEFAULT_RESET_DELAY_MS = 2000")
    expect(src).toMatch(/resetDelayMs: number = DEFAULT_RESET_DELAY_MS/)
    // markCopied 先清旧计时器再重排（连续复制的语义：复位从最后一次起算）。
    expect(src).toMatch(
      /const markCopied = \(\) => \{[\s\S]*clearTimeout[\s\S]*setIsCopied\(true\)[\s\S]*window\.setTimeout\(/,
    )
    // 卸载时清理未到期计时器（无泄漏）。
    expect(src).toMatch(
      /useEffect\(\(\) => \{[\s\S]*clearTimeout[\s\S]*\}, \[\]\)/,
    )
  })

  it("CV-07 hook 契约：await copyText → 仅成功才置位 → 返回 boolean", () => {
    const src = code(hookSrc)
    // 评审 P1-1 后不再锁“裸 const didCopy = await …”形态：hook 允许在
    // copyText 调用上追加 reject 兜底（.catch / try），意图是「仅成功才置位」。
    expect(src).toMatch(/await copyText\(text\)/)
    expect(src).toMatch(/if \(didCopy\) \{[\s\S]*markCopied\(\)[\s\S]*\}/)
    expect(src).toMatch(/return didCopy/)
  })

  it("CV-08 两个组件的失败提示都走同一 i18n key（piChat.copyFailed）", () => {
    for (const { name, src } of consumers) {
      expect(code(src), name).toMatch(
        /toast\.error\(t\("piChat\.copyFailed"\)\)/,
      )
    }
  })
})
