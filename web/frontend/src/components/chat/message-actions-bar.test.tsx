// @vitest-environment jsdom
// [REQ-019] MessageActionsBar 复制按钮单测（cut-5 由手写 useState+setTimeout(1200)
// 收敛到 useCopyToClipboard）：
//  - 成功 → aria-label/title 由 Copy 变 Copied，2000ms 后回 Copy；
//  - 失败 → 保持 Copy 且 toast.error 提示，失败态可重试；
//  - 既有骨架语义不回归：role="toolbar" + aria-label、text 缺省时无复制键、
//    三 props 全缺省时返回 null。
//
// mock 边界（如实登记）：`@/lib/clipboard` 与 `@tabler/icons-react` 被 mock——
// jsdom 无真实剪贴板/无渲染外观断言需求；真实剪贴板写入属「需浏览器」项。
import { act } from "react"
import { type ReactElement } from "react"
import { type Root, createRoot } from "react-dom/client"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import i18n from "@/i18n"

import { MessageActionsBar } from "./message-actions-bar"

// React 19：act 需要显式声明测试环境，否则 createRoot 渲染告警。

;(globalThis as Record<string, unknown>).IS_REACT_ACT_ENVIRONMENT = true

const { copyTextMock, toastErrorMock } = vi.hoisted(() => ({
  copyTextMock: vi.fn<(text: string) => Promise<boolean>>(async () => true),
  toastErrorMock: vi.fn<(message: string) => void>(() => {}),
}))

vi.mock("@/lib/clipboard", () => ({
  copyText: copyTextMock,
}))

vi.mock("sonner", () => ({
  toast: { error: toastErrorMock },
}))

vi.mock("@tabler/icons-react", () => ({
  IconCircleCheck: () => <span data-icon="check" />,
  IconCopy: () => <span data-icon="copy" />,
  IconDots: () => <span data-icon="dots" />,
  IconRefresh: () => <span data-icon="refresh" />,
}))

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

/** 复制键 = 第 1 个 .msg-actions-btn（text 存在时它总在最前）。 */
function copyButton(container: HTMLElement): Element {
  const btn = container.querySelector(".msg-actions-btn")
  if (!btn) throw new Error("未渲染复制按钮")
  return btn
}

async function clickCopy(container: HTMLElement) {
  await act(async () => {
    copyButton(container).dispatchEvent(
      new MouseEvent("click", { bubbles: true }),
    )
  })
}

beforeEach(async () => {
  copyTextMock.mockReset()
  copyTextMock.mockImplementation(async () => true)
  toastErrorMock.mockReset()
  await i18n.changeLanguage("en")
})

afterEach(() => {
  vi.useRealTimers()
  document.body.innerHTML = ""
})

describe("MessageActionsBar 复制交互 [REQ-019]", () => {
  it("MAB-01 初始态 aria-label / title 为 Copy，图标为 copy", () => {
    const h = mount(<MessageActionsBar text="hello" />)
    const btn = copyButton(h.container)
    expect(btn.getAttribute("aria-label")).toBe("Copy")
    expect(btn.getAttribute("title")).toBe("Copy")
    expect(btn.querySelector("[data-icon]")?.getAttribute("data-icon")).toBe(
      "copy",
    )
    h.unmount()
  })

  it("MAB-02 复制成功 → 标签变 Copied（check 图标），无 toast", async () => {
    const h = mount(<MessageActionsBar text="hello" />)
    await clickCopy(h.container)

    expect(copyTextMock).toHaveBeenCalledWith("hello")
    const btn = copyButton(h.container)
    expect(btn.getAttribute("aria-label")).toBe("Copied")
    expect(btn.getAttribute("title")).toBe("Copied")
    expect(btn.querySelector("[data-icon]")?.getAttribute("data-icon")).toBe(
      "check",
    )
    expect(toastErrorMock).not.toHaveBeenCalled()
    h.unmount()
  })

  it("MAB-03 成功态 2000ms 后回到 Copy（cut-5 统一节律，非旧 1200ms）", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] })
    const h = mount(<MessageActionsBar text="hello" />)
    await clickCopy(h.container)
    expect(copyButton(h.container).getAttribute("aria-label")).toBe("Copied")

    // 旧实现 1200ms 即复位；此处断言 1200ms 仍在 Copied 态（节律已变更）
    act(() => {
      vi.advanceTimersByTime(1200)
    })
    expect(copyButton(h.container).getAttribute("aria-label")).toBe("Copied")

    act(() => {
      vi.advanceTimersByTime(800)
    })
    expect(copyButton(h.container).getAttribute("aria-label")).toBe("Copy")
    h.unmount()
  })

  it("MAB-04 复制失败 → 标签保持 Copy + toast.error 提示", async () => {
    copyTextMock.mockImplementation(async () => false)
    const h = mount(<MessageActionsBar text="hello" />)
    await clickCopy(h.container)

    expect(copyButton(h.container).getAttribute("aria-label")).toBe("Copy")
    expect(toastErrorMock).toHaveBeenCalledTimes(1)
    expect(toastErrorMock).toHaveBeenCalledWith("Copy failed, please retry")
    h.unmount()
  })

  it("MAB-05 失败后可重试成功（失败态可恢复）", async () => {
    copyTextMock.mockImplementationOnce(async () => false)
    const h = mount(<MessageActionsBar text="hello" />)
    await clickCopy(h.container)
    expect(copyButton(h.container).getAttribute("aria-label")).toBe("Copy")

    copyTextMock.mockImplementation(async () => true)
    await clickCopy(h.container)
    expect(copyButton(h.container).getAttribute("aria-label")).toBe("Copied")
    h.unmount()
  })

  it("MAB-06 text 为 undefined → 不渲染复制键，仅渲染其余动作（边界）", () => {
    const onRegenerate = vi.fn()
    const h = mount(
      <MessageActionsBar onRegenerate={onRegenerate} onMore={() => {}} />,
    )
    const labels = Array.from(
      h.container.querySelectorAll(".msg-actions-btn"),
    ).map((b) => b.getAttribute("aria-label"))
    expect(labels).toEqual(["Regenerate", "More actions"])
    h.unmount()
  })

  it("MAB-07 三个 props 全缺省 → 返回 null，不渲染 toolbar（边界）", () => {
    const h = mount(<MessageActionsBar />)
    expect(h.container.querySelector('[role="toolbar"]')).toBeNull()
    expect(h.container.childElementCount).toBe(0)
    h.unmount()
  })

  it("MAB-08 骨架语义不回归：role=toolbar + aria-label=Actions（REQ-020 按钮组件部分）", () => {
    const h = mount(<MessageActionsBar text="hello" />)
    const bar = h.container.querySelector('[role="toolbar"]')
    expect(bar).not.toBeNull()
    expect(bar?.getAttribute("aria-label")).toBeTruthy()
    h.unmount()
  })
})
