// @vitest-environment jsdom
// [REQ-019] useCopyToClipboard 契约单测（cut-5 收敛目标 hook）。
// 验证 4 条契约语义：
//  1) copy(text) 返回 Promise<boolean>，成功 resolve true / 失败 resolve false；
//  2) 成功 → isCopied 置 true，并在 resetDelayMs（默认 2000ms）后自动复位 false；
//  3) 失败 → isCopied 不置位（失败态不得显示「已复制」），且失败不启动复位计时；
//  4) 复制未完成（Promise pending）期间 isCopied 仍为 false。
//
// mock 边界（如实登记）：jsdom 环境无真实系统剪贴板——`navigator.clipboard`
// 不存在、`document.execCommand` 未实现，故此处 mock `@/lib/clipboard` 的
// `copyText`，只验证本 hook 对 `copyText` 结果的处理契约。真实写入系统剪贴板
// 属「需浏览器」项，见测试报告「未验证项」。
import { act } from "react"
import { type Root, createRoot } from "react-dom/client"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import { useCopyToClipboard } from "./use-copy-to-clipboard"

// React 19：act 需要显式声明测试环境，否则 createRoot 渲染告警。

;(globalThis as Record<string, unknown>).IS_REACT_ACT_ENVIRONMENT = true

const { copyTextMock } = vi.hoisted(() => ({
  copyTextMock: vi.fn<(text: string) => Promise<boolean>>(async () => true),
}))

vi.mock("@/lib/clipboard", () => ({
  copyText: copyTextMock,
}))

type HookApi = ReturnType<typeof useCopyToClipboard>

/** createRoot + act 挂载 hook 宿主（仓库无 @testing-library，按最小依赖自建）。 */
function mountHook(resetDelayMs?: number) {
  const container = document.createElement("div")
  document.body.appendChild(container)
  const root: Root = createRoot(container)
  let api: HookApi | undefined

  function Harness() {
    api = useCopyToClipboard(resetDelayMs)
    return null
  }

  act(() => {
    root.render(<Harness />)
  })

  return {
    get current(): HookApi {
      if (!api) throw new Error("hook 尚未挂载")
      return api
    },
    unmount() {
      act(() => {
        root.unmount()
      })
      container.remove()
    },
  }
}

beforeEach(() => {
  copyTextMock.mockReset()
  copyTextMock.mockImplementation(async () => true)
})

afterEach(() => {
  vi.useRealTimers()
  document.body.innerHTML = ""
})

describe("useCopyToClipboard [REQ-019]", () => {
  it("UT-01 copy() 返回 Promise<boolean>，成功后 resolve true 并把原文透传给 copyText", async () => {
    const h = mountHook()
    const ret = h.current.copy("hello")
    expect(ret).toBeInstanceOf(Promise)

    let resolved: boolean | undefined
    await act(async () => {
      resolved = await ret
    })

    expect(resolved).toBe(true)
    expect(copyTextMock).toHaveBeenCalledTimes(1)
    expect(copyTextMock).toHaveBeenCalledWith("hello")
    h.unmount()
  })

  it("UT-02 复制成功 → isCopied 由 false 置 true", async () => {
    const h = mountHook()
    expect(h.current.isCopied).toBe(false)

    await act(async () => {
      await h.current.copy("hello")
    })

    expect(h.current.isCopied).toBe(true)
    h.unmount()
  })

  it("UT-03 成功后 1999ms 不复位、2000ms 整复位（默认 resetDelayMs 边界）", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] })
    const h = mountHook()

    await act(async () => {
      await h.current.copy("hello")
    })
    expect(h.current.isCopied).toBe(true)

    act(() => {
      vi.advanceTimersByTime(1999)
    })
    expect(h.current.isCopied).toBe(true)

    act(() => {
      vi.advanceTimersByTime(1)
    })
    expect(h.current.isCopied).toBe(false)
    h.unmount()
  })

  it("UT-04 复制失败 → resolve false，isCopied 保持 false，且不启动复位计时", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] })
    copyTextMock.mockImplementation(async () => false)
    const h = mountHook()

    let resolved: boolean | undefined
    await act(async () => {
      resolved = await h.current.copy("hello")
    })

    expect(resolved).toBe(false)
    expect(h.current.isCopied).toBe(false)
    // 失败态不应留下定时器（否则 2s 后会触发一次无意义的 setState）
    expect(vi.getTimerCount()).toBe(0)
    h.unmount()
  })

  it("UT-05 自定义 resetDelayMs 生效（1000ms 而非默认 2000ms）", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] })
    const h = mountHook(1000)

    await act(async () => {
      await h.current.copy("hello")
    })
    expect(h.current.isCopied).toBe(true)

    act(() => {
      vi.advanceTimersByTime(999)
    })
    expect(h.current.isCopied).toBe(true)

    act(() => {
      vi.advanceTimersByTime(1)
    })
    expect(h.current.isCopied).toBe(false)
    h.unmount()
  })

  it("UT-06 连续两次成功 → 复位计时从最后一次起算（前一个计时器被清除）", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] })
    const h = mountHook()

    await act(async () => {
      await h.current.copy("first")
    })
    act(() => {
      vi.advanceTimersByTime(1500)
    })
    await act(async () => {
      await h.current.copy("second")
    })
    expect(h.current.isCopied).toBe(true)

    // 计时从第二次复制（t=1500）起算：t=3499 仍未到期，
    // 若第一次的 2000ms 计时器未被清除，此处在 t=2500/3000 就会提前复位。
    act(() => {
      vi.advanceTimersByTime(1999)
    })
    expect(h.current.isCopied).toBe(true)

    // t=3500（距第二次整 2000ms）复位。
    act(() => {
      vi.advanceTimersByTime(1)
    })
    expect(h.current.isCopied).toBe(false)
    h.unmount()
  })

  it("UT-07 复制未完成（copyText pending）期间 isCopied 仍为 false", async () => {
    let resolveCopy: ((ok: boolean) => void) | undefined
    copyTextMock.mockImplementation(
      () =>
        new Promise<boolean>((resolve) => {
          resolveCopy = resolve
        }),
    )
    const h = mountHook()

    act(() => {
      void h.current.copy("hello")
    })
    expect(h.current.isCopied).toBe(false)

    await act(async () => {
      resolveCopy?.(true)
    })
    expect(h.current.isCopied).toBe(true)
    h.unmount()
  })

  it("UT-08 卸载时清除未到期的复位计时器（无泄漏计时器）", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] })
    const h = mountHook()

    await act(async () => {
      await h.current.copy("hello")
    })
    expect(vi.getTimerCount()).toBe(1)

    h.unmount()
    expect(vi.getTimerCount()).toBe(0)
  })

  it("UT-09 copyText reject（实现漂移兜底）→ resolve false，不外泄 rejection，isCopied 保持 false", async () => {
    copyTextMock.mockImplementation(async () => {
      throw new Error("boom")
    })
    const h = mountHook()

    let resolved: boolean | undefined
    await act(async () => {
      resolved = await h.current.copy("hello")
    })

    expect(resolved).toBe(false)
    expect(h.current.isCopied).toBe(false)
    h.unmount()
  })

  it("UT-10 pending 中卸载 → 完成后不再 setState（无卸载后更新 / 无异常）", async () => {
    const errorSpy = vi.spyOn(console, "error").mockImplementation(() => {})
    let release: ((ok: boolean) => void) | undefined
    copyTextMock.mockImplementation(
      () =>
        new Promise<boolean>((resolve) => {
          release = resolve
        }),
    )
    const h = mountHook()

    let pending: Promise<boolean> | undefined
    act(() => {
      pending = h.current.copy("hello")
    })

    h.unmount()
    await act(async () => {
      release?.(true)
    })
    await expect(pending).resolves.toBe(true)

    // mountedRef 拦住卸载后的 setIsCopied；React 18+ 已不再输出卸载更新告警，
    // 此处锁「不产生任何 console.error」，兼作未来版本行为的回归锁。
    expect(errorSpy).not.toHaveBeenCalled()
    errorSpy.mockRestore()
  })
})
