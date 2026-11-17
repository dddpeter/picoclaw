/// <reference types="node" />
// [REQ-023] 口径 B：断点体系必须真实落盘可检索——CSS 三条宽度断点 + owner 链
// 三方对齐（注释审计 ↔ Tailwind owner ↔ CSS 幂等固化）。
//
// 为什么是「源码文本断言」：断言对象是源码契约而非运行时级联（jsdom 不做真实
// 布局）；owner（chat-page / composer）为禁改文件，任何静默漂移须红灯拦截。
// CSS 用 node:fs 读盘——本仓 vitest 的 CSS 插件会把 ?raw 的 css 转成空串
// （见 pi-chat-animation.test.ts 头部记载）。
import { readFileSync } from "node:fs"

import { describe, expect, it } from "vitest"

const css = readFileSync(new URL("./pi-chat.css", import.meta.url), "utf8")
const chatPage = readFileSync(
  new URL("../components/chat/chat-page.tsx", import.meta.url),
  "utf8",
)
const composer = readFileSync(
  new URL("../components/chat/chat-composer.tsx", import.meta.url),
  "utf8",
)

/**
 * 取 media query 的完整块体（含 @media 头，花括号配平截止）。
 * 修正规格 §4.2 的「slice 到文件末尾」：xl 之后若混入别的 max-width 声明会假通过，
 * 配平切片保证断言锁定固化规则本身。
 */
function mediaBlock(source: string, query: string): string {
  const start = source.indexOf(query)
  expect(start, `missing media query: ${query}`).toBeGreaterThanOrEqual(0)
  const open = source.indexOf("{", start)
  expect(open, `missing opening brace for: ${query}`).toBeGreaterThan(start)
  let depth = 0
  for (let i = open; i < source.length; i += 1) {
    if (source[i] === "{") depth += 1
    else if (source[i] === "}") {
      depth -= 1
      if (depth === 0) return source.slice(start, i + 1)
    }
  }
  throw new Error(`unbalanced media block for: ${query}`)
}

describe("REQ-023 breakpoint contract", () => {
  it("pi-chat.css 携带全部三条宽度断点", () => {
    expect(css).toContain("@media (max-width: 768px)")
    expect(css).toContain("@media (min-width: 1024px)")
    expect(css).toContain("@media (min-width: 1280px)")
  })

  it("lg/xl 固化规则存在且值为 1000px（幂等守门）", () => {
    const lg = mediaBlock(css, "(min-width: 1024px)")
    const xl = mediaBlock(css, "(min-width: 1280px)")
    expect(lg).toContain("max-width: 1000px")
    expect(xl).toContain("max-width: 1000px")
  })

  it("窄屏主断点行为未被回退（基线 6px 间距 + a2ui 溢出防御）", () => {
    const narrow = mediaBlock(css, "(max-width: 768px)")
    expect(narrow).toContain("padding: 6px 0 12px")
    expect(narrow).toContain("overflow-x: auto")
  })

  it("断点行为 owner（chat-page / composer 的留白阶梯与封顶）未被静默变更", () => {
    expect(chatPage).toContain("md:px-8 lg:px-24 xl:px-48")
    expect(chatPage).toContain("max-w-250")

    // [D-1] composer 断言限定到根容器 className 片段，不做全文件 toContain：
    // 全文件断言无法区分「前缀在根容器」还是「被搬到无关元素」（漏检 M2）。
    // 根容器定位特征 = 同一 className 内同时含 md:px-8 与 md:pb-8（文件内
    // 唯一，当前 L106）；片段内四前缀顺序无关断言（容忍 M1：lg/xl 顺序对调
    // 在 Tailwind 下语义等价不应红灯；拦截 M2：挪去无关 span 则片段缺失红灯）。
    const rootMatch = composer.match(
      /className="([^"]*md:px-8[^"]*md:pb-8[^"]*)"/s,
    )
    expect(rootMatch, "composer 根容器 className 未找到").toBeTruthy()
    const rootCls = rootMatch?.[1] ?? ""
    expect(rootCls).toContain("md:px-8")
    expect(rootCls).toContain("md:pb-8")
    expect(rootCls).toContain("lg:px-24")
    expect(rootCls).toContain("xl:px-48")

    // 封顶列宽限定在根容器或其直接子容器（当前 L107
    // `mx-auto flex max-w-[1000px] flex-col items-end`）；Tailwind 任意值
    // 语法的方括号在正则中已转义。
    const capMatch = composer.match(/className="([^"]*max-w-\[1000px\][^"]*)"/s)
    expect(capMatch, "composer 封顶容器 max-w-[1000px] 未找到").toBeTruthy()
    expect(capMatch?.[1] ?? "").toContain("max-w-[1000px]")
  })
})
