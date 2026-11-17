/// <reference types="node" />
// [REQ-024] pi-chat.css 动效定义唯一性单测（cut-5：删除 L1675 死定义
// `@keyframes a2ui-row-in` 纯位移版，保留 L1821 弹簧版）。
//
// 为什么是「源码文本断言」：CSS 文件无法在无浏览器的 jsdom 里求值出级联结果
// （jsdom 不解析动画规则、不回放 @keyframes）。本刀要锁的正是「定义唯一 + 存活
// 的是弹簧版 + 无孤儿引用」这一文本层不变量，故直接读源码做结构断言，属真实可
// 验证范围；「动效实际播放效果」属「需浏览器」项，见测试报告「未验证项」。
//
// 为什么用 node:fs 而不是 Vite `?raw`：vitest 默认 `test.css: false`，其 CSS
// 插件会把所有 isCSSRequest 的模块（含 `?raw` 变体）转成空字符串——实测
// `import css from "./pi-chat.css?raw"` 得到 `""`（同 query 的 `.tsx` 则正常）。
// 因此改为直接读取磁盘上的真实文件；读到的即仓库工作树内容，未做任何改写。
import { readFileSync } from "node:fs"

import { describe, expect, it } from "vitest"

const css = readFileSync(new URL("./pi-chat.css", import.meta.url), "utf8")

/** 提取全部 @keyframes 名（含重复项，顺序保留）。 */
function keyframeNames(source: string): string[] {
  return Array.from(source.matchAll(/@keyframes\s+([A-Za-z0-9_-]+)/g)).map(
    (m) => m[1],
  )
}

/** 提取某个 @keyframes 的定义块正文（到下一个 @keyframes 或文件尾为止）。 */
function keyframeBody(source: string, name: string): string {
  const start = source.indexOf(`@keyframes ${name}`)
  expect(start, `未找到 @keyframes ${name}`).toBeGreaterThanOrEqual(0)
  const rest = source.slice(start)
  const next = rest.indexOf("@keyframes", 1)
  return next === -1 ? rest : rest.slice(0, next)
}

describe("pi-chat.css 动效定义唯一性 [REQ-024]", () => {
  it("AN-01 @keyframes a2ui-row-in 全文恰有 1 处定义（cut-5 删除死定义后）", () => {
    const defs = keyframeNames(css).filter((n) => n === "a2ui-row-in")
    expect(defs).toHaveLength(1)
  })

  it("AN-02 存活的定义是弹簧版（缩放 + 回弹曲线），非被删的纯位移版", () => {
    const body = keyframeBody(css, "a2ui-row-in")
    // 弹簧版 from 帧带 scale；被删的纯位移版只有 translateX。
    expect(body).toContain("translateX(-8px) scale(0.985)")
    expect(body).toContain("opacity: 0")
  })

  it("AN-03 被删定义的特征行在全文只出现 1 次（重加死定义即失败）", () => {
    const hits = css.match(/translateX\(-8px\)/g) ?? []
    expect(hits).toHaveLength(1)
  })

  it("AN-04 全文无重名 @keyframes（泛化守卫：任何动画名都不得定义两次）", () => {
    const names = keyframeNames(css)
    expect(names.length).toBeGreaterThan(0)
    const duplicated = names.filter((n, i) => names.indexOf(n) !== i)
    expect(duplicated).toEqual([])
  })

  it("AN-05 定义非孤儿：a2ui-row-in 仍被规则引用（含 backwards fill-mode）", () => {
    const usages = Array.from(
      css.matchAll(/animation:\s*a2ui-row-in[^;]*/g),
      (m) => m[0],
    )
    // 生效引用至少 1 处（cut-5 只删定义、不动引用）。
    expect(usages.length).toBeGreaterThan(0)
    // 每条引用都能解析到 AN-01 的唯一同名定义（无悬空 animation-name）。
    expect(keyframeNames(css).filter((n) => n === "a2ui-row-in")).toHaveLength(
      1,
    )
    // 行入场用的 backwards（刻意不用 forwards，避免残留 identity transform）。
    expect(css).toContain("animation: a2ui-row-in 0.38s")
  })
})
