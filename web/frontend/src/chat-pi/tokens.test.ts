// [评审 P2-2] 令牌桥「单一维护点」纪律单测。
//
// 背景：model-selector / session-history-sidebar 挂载在 `.pi-chat` 作用域之外，
// 无法继承 tokens.css，需在宿主元素上显式声明 `[--lobe-x:var(--y)]` 映射。
// 该串原为两处复制粘贴（同批映射各维护一份），评审登记为重复代码。
// 这里以源码文本断言锁住「只有一份」，行为断言无法区分「共享」与「各抄一份」。
//
// 环境：源码经 Vite `?raw` 载入（与 copy-interaction-convergence.test.ts 同法），
// 不引入 node:fs。
import { describe, expect, it } from "vitest"

import modelSelectorSrc from "../components/chat/model-selector.tsx?raw"
import sidebarSrc from "../components/chat/session-history-sidebar.tsx?raw"
import tokensSrc from "./tokens.ts?raw"

const consumers = [
  { name: "model-selector.tsx", src: modelSelectorSrc },
  { name: "session-history-sidebar.tsx", src: sidebarSrc },
] as const

describe("LOBE_SCOPE_TOKENS 共享纪律 [评审 P2-2]", () => {
  it("TK-01 两个组件都从 @/chat-pi/tokens 引入共享常量", () => {
    for (const { name, src } of consumers) {
      expect(src, name).toMatch(
        /import \{ LOBE_SCOPE_TOKENS \} from "@\/chat-pi\/tokens"/,
      )
    }
  })

  it("TK-02 两个组件都不再自带 [--lobe-*] 映射声明（回归锁）", () => {
    for (const { name, src } of consumers) {
      expect(src, name).not.toMatch(/\[--lobe-/)
    }
  })

  it("TK-03 共享常量保持静态字面量（Tailwind 依赖文本扫描生成对应 CSS）", () => {
    expect(tokensSrc).toContain("[--lobe-text:var(--foreground)]")
    expect(tokensSrc).toContain("[--lobe-info:var(--primary)]")
  })
})
