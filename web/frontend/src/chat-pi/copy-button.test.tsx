// @vitest-environment jsdom
// [REQ-019] CopyButton 复制交互单测（cut-5 收敛点）：
//  - 走 useCopyToClipboard，成功 2000ms 后回「可复制」态（check → copy 图标）；
//  - 失败不置 isCopied（图标不变），并 toast.error 提示；
//  - 空 text 边界 → 不渲染。
//
// mock 边界（如实登记）：
//  1) `@/lib/clipboard` 被 mock —— jsdom 无真实系统剪贴板，只验组件对复制结果的
//     反应；真实写入剪贴板属「需浏览器」项（见测试报告「未验证项」）。
//  2) `@tabler/icons-react` 被 mock 成带 data-icon 的 span —— 断言的是「成功态与
//     失败态分别选中哪个图标」这一契约，不是图标外观；真实 svg 外观需浏览器。
import { act } from "react"
import { type ReactElement } from "react"
import { type Root, createRoot } from "react-dom/client"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import i18n from "@/i18n"

import { CopyButton } from "./copy-button"

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
  IconCheck: () => <span data-icon="check" />,
  IconCopy: () => <span data-icon="copy" />,
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

function iconOf(container: HTMLElement): string | null {
  return (
    container.querySelector("[data-icon]")?.getAttribute("data-icon") ?? null
  )
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

describe("CopyButton [REQ-019]", () => {
  it("CB-01 初始渲染为 copy 图标，title 取 piChat.copy 文案", () => {
    const h = mount(<CopyButton text="hello" />)
    const btn = h.container.querySelector("button")
    expect(btn).not.toBeNull()
    expect(iconOf(h.container)).toBe("copy")
    expect(btn?.getAttribute("title")).toBe("Copy")
    h.unmount()
  })

  it("CB-02 text 为空串 → 不渲染按钮（边界）", () => {
    const h = mount(<CopyButton text="" />)
    expect(h.container.querySelector("button")).toBeNull()
    h.unmount()
  })

  it("CB-03 点击 → 调 copy(text) → 成功态切换为 check 图标", async () => {
    const h = mount(<CopyButton text="hello" />)
    await act(async () => {
      h.container
        .querySelector("button")
        ?.dispatchEvent(new MouseEvent("click", { bubbles: true }))
    })

    expect(copyTextMock).toHaveBeenCalledWith("hello")
    expect(iconOf(h.container)).toBe("check")
    expect(toastErrorMock).not.toHaveBeenCalled()
    h.unmount()
  })

  it("CB-04 成功态 2000ms 后自动回到 copy 图标（reset 节律）", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] })
    const h = mount(<CopyButton text="hello" />)
    await act(async () => {
      h.container
        .querySelector("button")
        ?.dispatchEvent(new MouseEvent("click", { bubbles: true }))
    })
    expect(iconOf(h.container)).toBe("check")

    act(() => {
      vi.advanceTimersByTime(1999)
    })
    expect(iconOf(h.container)).toBe("check")

    act(() => {
      vi.advanceTimersByTime(1)
    })
    expect(iconOf(h.container)).toBe("copy")
    h.unmount()
  })

  it("CB-05 复制失败 → 图标保持 copy（不显示已复制）且 toast.error 提示", async () => {
    copyTextMock.mockImplementation(async () => false)
    const h = mount(<CopyButton text="hello" />)

    await act(async () => {
      h.container
        .querySelector("button")
        ?.dispatchEvent(new MouseEvent("click", { bubbles: true }))
    })

    expect(iconOf(h.container)).toBe("copy")
    expect(toastErrorMock).toHaveBeenCalledTimes(1)
    expect(toastErrorMock).toHaveBeenCalledWith("Copy failed, please retry")
    h.unmount()
  })

  it("CB-06 失败后可重试成功（失败态可恢复）", async () => {
    copyTextMock.mockImplementationOnce(async () => false)
    const h = mount(<CopyButton text="hello" />)

    await act(async () => {
      h.container
        .querySelector("button")
        ?.dispatchEvent(new MouseEvent("click", { bubbles: true }))
    })
    expect(iconOf(h.container)).toBe("copy")

    copyTextMock.mockImplementation(async () => true)
    await act(async () => {
      h.container
        .querySelector("button")
        ?.dispatchEvent(new MouseEvent("click", { bubbles: true }))
    })
    expect(iconOf(h.container)).toBe("check")
    expect(toastErrorMock).toHaveBeenCalledTimes(1)
    h.unmount()
  })
})
