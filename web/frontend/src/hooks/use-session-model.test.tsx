// @vitest-environment jsdom
// useSessionModel 契约单测（fork §28 评审 yellow #4 的回归守卫）：
//  1) POST 在途切会话：A 会话的 setSessionModel 响应晚于切到 B 落地时，
//     不得覆写 B 的显示（GET 路径已有 cancelled 守卫，POST 路径靠
//     activeSessionRef 守卫）；
//  2) 断连清缓存：isConnected false→true 后同一会话必须重拉——网关重启
//     会清掉内存 override，重启前的旧值不允许一直挂着；
//  3) 失败回滚：同会话内 setSessionModel 失败恢复旧值并弹 toast。
//
// mock 边界（如实登记）：api 层（@/api/session-model）与 toast（sonner）
// 全部 mock，只验证 hook 对请求结果的处理契约。
import { act } from "react"
import { type Root, createRoot } from "react-dom/client"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import { useSessionModel } from "./use-session-model"

;(globalThis as Record<string, unknown>).IS_REACT_ACT_ENVIRONMENT = true

const { getSessionModelMock, setSessionModelMock, toastErrorMock } = vi.hoisted(
  () => ({
    getSessionModelMock: vi.fn<
      (sessionId: string) => Promise<{ model: string; context_window?: number }>
    >(async () => ({ model: "" })),
    setSessionModelMock: vi.fn<
      (
        sessionId: string,
        model: string,
      ) => Promise<{ model: string; context_window?: number }>
    >(async (_sessionId, model) => ({ model })),
    toastErrorMock: vi.fn(),
  }),
)

vi.mock("@/api/session-model", () => ({
  getSessionModel: getSessionModelMock,
  setSessionModel: setSessionModelMock,
}))

vi.mock("sonner", () => ({
  toast: { error: toastErrorMock },
}))

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (key: string) => key }),
}))

type HookProps = { activeSessionId: string; isConnected: boolean }
type HookApi = ReturnType<typeof useSessionModel>

/** createRoot + act 挂载 hook 宿主（仓库无 @testing-library，按最小依赖自建）。 */
function mountSessionModelHook(initial: HookProps) {
  const container = document.createElement("div")
  document.body.appendChild(container)
  const root: Root = createRoot(container)
  let api: HookApi | undefined

  function Harness(props: HookProps) {
    api = useSessionModel(props)
    return null
  }

  act(() => {
    root.render(<Harness {...initial} />)
  })

  return {
    get current(): HookApi {
      if (!api) throw new Error("hook 尚未挂载")
      return api
    },
    /** 切换 props（模拟切会话 / 断连重连）。 */
    setProps(props: HookProps) {
      act(() => {
        root.render(<Harness {...props} />)
      })
    },
    /** flush effect 里的异步请求落地。 */
    async flush() {
      await act(async () => {})
    },
    unmount() {
      act(() => {
        root.unmount()
      })
      container.remove()
    },
  }
}

function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((res) => {
    resolve = res
  })
  return { promise, resolve }
}

beforeEach(() => {
  getSessionModelMock.mockReset()
  getSessionModelMock.mockImplementation(async () => ({ model: "" }))
  setSessionModelMock.mockReset()
  setSessionModelMock.mockImplementation(async (_sessionId, model) => ({ model }))
  toastErrorMock.mockReset()
})

afterEach(() => {
  document.body.innerHTML = ""
})

describe("useSessionModel", () => {
  it("POST 在途切会话：晚到的响应不得覆写新会话的显示", async () => {
    const pending = deferred<{ model: string; context_window?: number }>()
    const sessionModels: Record<string, string> = { "sess-b": "model-b" }
    getSessionModelMock.mockImplementation(async (sessionId: string) => ({
      model: sessionModels[sessionId] ?? "",
    }))
    setSessionModelMock.mockImplementation(() => pending.promise)

    const h = mountSessionModelHook({
      activeSessionId: "sess-a",
      isConnected: true,
    })
    await h.flush()
    expect(h.current.sessionModel).toBe("")

    // 会话 A 上发起切换：乐观置为 model-x，请求在途。
    let inFlight!: Promise<void>
    act(() => {
      inFlight = h.current.handleSetSessionModel("model-x").then(() => {})
    })
    expect(h.current.sessionModel).toBe("model-x")

    // 切到会话 B：B 的 GET 落地，选择器显示 B 自己的模型。
    h.setProps({ activeSessionId: "sess-b", isConnected: true })
    await h.flush()
    expect(h.current.sessionModel).toBe("model-b")

    // A 的 POST 此刻才回来：不得串显到 B。
    await act(async () => {
      pending.resolve({ model: "model-x", context_window: 12345 })
      await inFlight
    })
    expect(h.current.sessionModel).toBe("model-b")
    expect(h.current.sessionContextWindow).toBe(0)

    h.unmount()
  })

  it("断连清缓存：重连后同一会话重新拉取（网关重启会清内存 override）", async () => {
    const h = mountSessionModelHook({
      activeSessionId: "sess-a",
      isConnected: true,
    })
    await h.flush()
    expect(getSessionModelMock).toHaveBeenCalledTimes(1)

    // 断连：loadedSessionRef 复位。
    h.setProps({ activeSessionId: "sess-a", isConnected: false })
    // 重连：同一会话必须重新 GET。
    h.setProps({ activeSessionId: "sess-a", isConnected: true })
    await h.flush()
    expect(getSessionModelMock).toHaveBeenCalledTimes(2)

    // 对照：不断连重渲染同一会话不会重复拉取。
    h.setProps({ activeSessionId: "sess-a", isConnected: true })
    await h.flush()
    expect(getSessionModelMock).toHaveBeenCalledTimes(2)

    h.unmount()
  })

  it("失败回滚：同会话内 setSessionModel 失败恢复旧值并弹 toast", async () => {
    getSessionModelMock.mockImplementation(async () => ({ model: "" }))
    setSessionModelMock.mockImplementation(async () => {
      throw new Error("session is busy")
    })

    const h = mountSessionModelHook({
      activeSessionId: "sess-a",
      isConnected: true,
    })
    await h.flush()

    await act(async () => {
      await h.current.handleSetSessionModel("model-x")
    })
    expect(h.current.sessionModel).toBe("")
    expect(toastErrorMock).toHaveBeenCalledWith("session is busy")
    expect(h.current.isSettingSessionModel).toBe(false)

    h.unmount()
  })
})
