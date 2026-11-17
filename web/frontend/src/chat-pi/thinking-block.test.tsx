// @vitest-environment jsdom
// [REQ-020] ThinkingBlock 可达性单测（cut-5：useId() 生成 bodyId，.thinking-head /
// .thinking-toggle 挂 aria-controls，.thinking-body 挂 id，仍仅展开时渲染）：
//  - 展开态：body 渲染且带 id，head/toggle 的 aria-controls 与该 id 一致，
//    aria-expanded="true"；
//  - 折叠态：aria-expanded="false" 正确，但 body 不渲染 → aria-controls 成为
//    断链 IDREF（无同 id 元素）。本条如实锁定期望行为，同时在测试报告登记为
//    边界/缺陷 A11Y-01，不做「假装通过」处理（见 TB-02 注释）；
//  - 展开/收起（鼠标 + 键盘）后 id 关联不漂移，useId 在同一 root 内不冲突；
//  - 复制槽 stopPropagation：点复制槽不得误触发折叠（边界）。
//
// mock 边界（如实登记）：本文件不 mock 任何模块——ThinkingBlock 渲染期不触碰
// 剪贴板；仅点击 .thinking-copy 槽容器（不点其中的复制按钮），故无需 mock
// @/lib/clipboard。真实剪贴板写入属「需浏览器」项，见测试报告「未验证项」。
import { type ReactElement, act } from "react"
import { type Root, createRoot } from "react-dom/client"
import { afterEach, beforeEach, describe, expect, it } from "vitest"

import i18n from "@/i18n"

import { ThinkingBlock, thinkingPreview } from "./thinking-block"

// React 19：act 需要显式声明测试环境，否则 createRoot 渲染告警。

;(globalThis as Record<string, unknown>).IS_REACT_ACT_ENVIRONMENT = true

/** createRoot + act 渲染（仓库无 @testing-library，按最小依赖自建）。 */
function mount(ui: ReactElement) {
  const container = document.createElement("div")
  document.body.appendChild(container)
  const root: Root = createRoot(container)
  act(() => {
    root.render(ui)
  })
  return {
    container,
    unmount() {
      act(() => {
        root.unmount()
      })
      container.remove()
    },
  }
}

function must(container: HTMLElement, selector: string): Element {
  const el = container.querySelector(selector)
  if (!el) throw new Error(`未渲染 ${selector}`)
  return el
}

const head = (c: HTMLElement) => must(c, ".thinking-head")
const toggle = (c: HTMLElement) => must(c, ".thinking-toggle")
const body = (c: HTMLElement) => c.querySelector(".thinking-body")

function click(el: Element) {
  act(() => {
    el.dispatchEvent(new MouseEvent("click", { bubbles: true }))
  })
}

function pressKey(el: Element, key: string) {
  act(() => {
    el.dispatchEvent(new KeyboardEvent("keydown", { key, bubbles: true }))
  })
}

/** 折叠态判据统一走 aria-expanded（不是 class 名，避免锁样式实现）。 */
const isExpanded = (c: HTMLElement) =>
  head(c).getAttribute("aria-expanded") === "true"

beforeEach(async () => {
  // 文案断言固定在 en（i18n 语言探测在 jsdom 下不确定）。
  await i18n.changeLanguage("en")
})

afterEach(() => {
  document.body.innerHTML = ""
})

describe("ThinkingBlock 可达性 [REQ-020]", () => {
  it("TB-01 折叠态（wrap=false）：head 与 toggle 均 aria-expanded=false，且 body 不渲染", () => {
    const h = mount(<ThinkingBlock thinking="思考内容" wrap={false} />)
    expect(head(h.container).getAttribute("aria-expanded")).toBe("false")
    expect(toggle(h.container).getAttribute("aria-expanded")).toBe("false")
    // 仅展开时渲染：折叠态无 body 节点（cut-5 未改变该渲染条件）。
    expect(body(h.container)).toBeNull()
    h.unmount()
  })

  it("TB-02 折叠态：aria-controls 指向的 id 在文档中不存在（断链 IDREF，登记为 A11Y-01）", () => {
    // 断言的是「当前实现的客观事实」，不是「理想可达性」。因此本用例通过 =
    // 该事实成立，不代表 aria-controls 合法。判定与影响见测试报告缺陷 A11Y-01：
    // 折叠时 body 不渲染，ARIA IDREF 悬空；因 aria-expanded 已独立承载展开态，
    // 读屏仍可操作该披露控件，故按低严重度登记。
    const h = mount(<ThinkingBlock thinking="思考内容" wrap={false} />)

    for (const el of [head(h.container), toggle(h.container)]) {
      const controls = el.getAttribute("aria-controls")
      expect(controls).toBeTruthy()
      // 用 getElementById 而非 querySelector(`#${id}`)：React useId 的 id 含
      // 非 CSS 标识符字符，选择器会转义失败。
      expect(document.getElementById(controls!)).toBeNull()
    }
    h.unmount()
  })

  it("TB-03 展开态（wrap 默认 true）：body 带 id，head/toggle 的 aria-controls 与该 id 一致", () => {
    const h = mount(<ThinkingBlock thinking="思考内容" />)
    const bodyEl = body(h.container)
    expect(bodyEl).not.toBeNull()

    const id = bodyEl?.getAttribute("id")
    expect(id).toBeTruthy()
    expect(head(h.container).getAttribute("aria-controls")).toBe(id)
    expect(toggle(h.container).getAttribute("aria-controls")).toBe(id)
    expect(head(h.container).getAttribute("aria-expanded")).toBe("true")
    expect(toggle(h.container).getAttribute("aria-expanded")).toBe("true")
    // id 真实可解析（展开态 IDREF 闭合）。
    expect(document.getElementById(id!)).toBe(bodyEl)
    // 正文可读（不是空壳节点）。
    expect(bodyEl?.textContent).toBe("思考内容")
    h.unmount()
  })

  it("TB-04 折叠态点 toggle → 展开，body 出现且 id 与 aria-controls 对齐", () => {
    const h = mount(<ThinkingBlock thinking="思考内容" wrap={false} />)
    click(toggle(h.container))

    expect(isExpanded(h.container)).toBe(true)
    const bodyEl = body(h.container)
    const id = bodyEl?.getAttribute("id")
    expect(id).toBeTruthy()
    expect(head(h.container).getAttribute("aria-controls")).toBe(id)
    expect(document.getElementById(id!)).toBe(bodyEl)
    h.unmount()
  })

  it("TB-05 展开态点 head → 收起，body 移除", () => {
    const h = mount(<ThinkingBlock thinking="思考内容" />)
    expect(body(h.container)).not.toBeNull()

    click(head(h.container))

    expect(isExpanded(h.container)).toBe(false)
    expect(body(h.container)).toBeNull()
    h.unmount()
  })

  it("TB-06 键盘可达：head 上 Enter / Space 可切换展开态", () => {
    const h = mount(<ThinkingBlock thinking="思考内容" wrap={false} />)

    pressKey(head(h.container), "Enter")
    expect(isExpanded(h.container)).toBe(true)

    pressKey(head(h.container), " ")
    expect(isExpanded(h.container)).toBe(false)

    // 非切换键不改变状态（边界）。
    pressKey(head(h.container), "a")
    expect(isExpanded(h.container)).toBe(false)
    h.unmount()
  })

  it("TB-07 键盘事件源自内层 toggle 时不冒泡触发 head 的切换（边界：target!==currentTarget 直接返回）", () => {
    const h = mount(<ThinkingBlock thinking="思考内容" wrap={false} />)
    pressKey(toggle(h.container), "Enter")
    expect(isExpanded(h.container)).toBe(false)
    h.unmount()
  })

  it("TB-08 useId 唯一性：同一 root 内两个实例的 bodyId 不冲突", () => {
    const h = mount(
      <>
        <ThinkingBlock thinking="A" />
        <ThinkingBlock thinking="B" />
      </>,
    )
    const ids = Array.from(h.container.querySelectorAll(".thinking-body")).map(
      (el) => el.getAttribute("id"),
    )
    expect(ids).toHaveLength(2)
    expect(ids[0]).toBeTruthy()
    expect(ids[1]).toBeTruthy()
    expect(ids[0]).not.toBe(ids[1])
    // 各自的 aria-controls 指向各自的 body（不串号）。
    const heads = h.container.querySelectorAll(".thinking-head")
    expect(heads[0]?.getAttribute("aria-controls")).toBe(ids[0])
    expect(heads[1]?.getAttribute("aria-controls")).toBe(ids[1])
    h.unmount()
  })

  it("TB-09 点复制槽容器不触发折叠（stopPropagation 边界）", () => {
    const h = mount(<ThinkingBlock thinking="思考内容" />)
    click(must(h.container, ".thinking-copy"))
    expect(isExpanded(h.container)).toBe(true)

    const h2 = mount(<ThinkingBlock thinking="思考内容" wrap={false} />)
    click(must(h2.container, ".thinking-copy"))
    expect(isExpanded(h2.container)).toBe(false)

    h.unmount()
    h2.unmount()
  })

  it("TB-10 折叠态渲染预览文案（走 piChat.thinkingPreview，不渲染思考正文）", () => {
    const h = mount(
      <ThinkingBlock thinking={"首行摘要\n第二行不应出现"} wrap={false} />,
    )
    // mount() 的 container 是自建包裹 div（className 为空），ThinkingBlock 的根
    // 节点是其第一个子元素——按结构取根节点断言，勿误读 container.className。
    const rootEl = h.container.firstElementChild
    expect(rootEl?.className).toContain("thinking")
    expect(rootEl?.className).not.toContain("open")
    expect(h.container.querySelector(".thinking-label")?.textContent).toContain(
      "Thinking: 首行摘要",
    )
    expect(h.container.textContent).not.toContain("第二行不应出现")
    expect(body(h.container)).toBeNull()
    h.unmount()
  })
})

// thinkingPreview 是 thinking-block.tsx 导出的纯函数（源码注释明确要求单测直引
// 此处、禁止在测试里抄一份实现）。此段关闭遗留挂账 4「thinkingPreview 无单测」。
describe("thinkingPreview 折叠摘要纯函数 [REQ-020]", () => {
  it("TP-01 streaming=true → 取 trimEnd 后末 80 字符（实时尾巴）", () => {
    expect(thinkingPreview("a".repeat(100), true)).toBe("a".repeat(80))
    // 尾部空白先被 trimEnd 掉，不计入窗口。
    expect(thinkingPreview("a".repeat(100) + "  \n", true)).toBe("a".repeat(80))
    // 短文本原样返回。
    expect(thinkingPreview("hi", true)).toBe("hi")
  })

  it("TP-02 streaming=false → 取首行前 80 字符", () => {
    expect(thinkingPreview("first line\nsecond line")).toBe("first line")
    expect(thinkingPreview("x".repeat(120) + "\nrest")).toBe("x".repeat(80))
    expect(thinkingPreview("", false)).toBe("")
  })

  it("TP-03 边界：恰好 80 字符与第 81 字符的截断位置", () => {
    expect(thinkingPreview("y".repeat(80), false)).toBe("y".repeat(80))
    expect(thinkingPreview("y".repeat(81), false)).toBe("y".repeat(80))
  })
})
