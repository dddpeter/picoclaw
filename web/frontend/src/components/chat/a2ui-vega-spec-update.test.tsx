// @vitest-environment jsdom
// VegaChart lazy 实例稳定性测试（二期终审收尾，配套 a2ui-vega.test.tsx）：
// react-vega mock 组件用 useEffect([]) 记录每次真挂载（mountLog）并逐次
// render 记录 spec.title（renderLog）。流式 updateDataModel 频繁产生新 spec
// 对象引用，VegaChartView 的 spec 变化 effect 不得重建 lazy 实例——重建等于
// 换组件类型 → VegaEmbed 卸载重挂 → mountLog 增长（图表闪烁的测试化替身）。
// 断言：① 未失败时连续 spec 更新 mount 数不变、新 spec 仍到达组件；
// ② onError 上报失败后的 spec 变化仍须重建实例真重试。独立文件：vi.mock
// 文件作用域，不影响真实渲染链测试（vitest 按文件隔离模块图）。
import {
  A2uiSurface,
  MarkdownComponentContext,
  type ReactCatalogComponent,
} from "@a2ui/react/v0_9"
import { MessageProcessor, type ProcessableMessage } from "@a2ui/web_core/v0_9"
import { basicCatalog } from "@a2ui/web_core/v0_9/basic_catalog"
import { type ReactElement, act } from "react"
import { type Root, createRoot } from "react-dom/client"
import { beforeEach, describe, expect, it, vi } from "vitest"

import i18n from "@/i18n"

import { initA2uiBasicCatalog } from "./a2ui-card"
import { catalogWithVegaChart } from "./a2ui-vega"

const { mountLog, renderLog } = vi.hoisted(() => ({
  mountLog: [] as string[],
  renderLog: [] as string[],
}))

vi.mock("react-vega", async () => {
  const { useEffect } = await import("react")
  return {
    // boom=true 的 spec 经 onError 回调上报失败——真实 react-vega 对
    // vega-lite 编译/渲染失败是 embed promise 链内部 catch 后走 onError，
    // 异步到达（setTimeout(0) 模拟）。不能同步触发（提交期 setState 与
    // a2ui 节点层交互异常），也不能 render 期 throw（会二次逃逸到卡级
    // 边界打死整卡——都不是 ② 路径的生产行为）。
    VegaEmbed: (props: {
      onError?: (error: unknown) => void
      spec?: { boom?: boolean; title?: string }
    }) => {
      const title = String(props.spec?.title)
      renderLog.push(title)
      // 挂载计数：[] 依赖只在真挂载（组件类型变化 → 卸载重挂）时触发。
      useEffect(() => {
        mountLog.push(title)
      }, [])
      useEffect(() => {
        if (props.spec?.boom) {
          const timer = setTimeout(
            () => props.onError?.(new Error("mock render fail")),
            0,
          )
          return () => clearTimeout(timer)
        }
      }, [props.spec])
      return null
    },
  }
})

// React 19：act 需要显式声明测试环境，否则 createRoot 渲染告警。
;(globalThis as Record<string, unknown>).IS_REACT_ACT_ENVIRONMENT = true

/** createRoot + act 渲染（同 a2ui-vega.test.tsx，刻意不用 StrictMode——
 * 其 dev 双挂载会把 mountLog 翻倍，干扰挂载计数断言）。 */
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

/** 裸轮询等待断言（不包 act——见 a2ui-vega.test.tsx 顶部注释）。 */
async function waitFor(ready: () => void, timeoutMs = 5000): Promise<void> {
  const start = Date.now()
  for (;;) {
    try {
      ready()
      return
    } catch (error) {
      if (Date.now() - start > timeoutMs) throw error
      await new Promise((resolve) => setTimeout(resolve, 10))
    }
  }
}

/** {path} 绑定的 VegaChart 卡：dataModel 值由各步骤的 updateDataModel 注入。
 * 显式返回类型让字面量按 union 成员上下文收窄（否则 version 拓宽为 string
 * 不能赋给 ProcessableMessage）。 */
function stepUpdateDataModel(value: unknown): ProcessableMessage[] {
  return [
    {
      version: "v0.9",
      updateDataModel: {
        surfaceId: "vega-stable",
        path: "/chart/spec",
        value,
      },
    },
  ]
}

describe("VegaChart lazy instance stability", () => {
  beforeEach(async () => {
    initA2uiBasicCatalog()
    await i18n.changeLanguage("en")
    mountLog.length = 0
    renderLog.length = 0
  })

  it("keeps the lazy instance across clean spec updates and rebuilds only after a failure", async () => {
    const processor = new MessageProcessor<ReactCatalogComponent>(
      [catalogWithVegaChart],
      undefined,
      { version: "v0.9" },
    )
    processor.processMessages([
      {
        version: "v0.9",
        createSurface: {
          surfaceId: "vega-stable",
          catalogId: basicCatalog.id,
        },
      },
      {
        version: "v0.9",
        updateComponents: {
          surfaceId: "vega-stable",
          components: [
            { id: "root", component: "Card", child: "chart" },
            {
              id: "chart",
              component: "VegaChart",
              spec: { path: "/chart/spec" },
            },
          ],
        },
      },
      {
        version: "v0.9",
        updateDataModel: {
          surfaceId: "vega-stable",
          path: "/",
          value: {},
        },
      },
    ])
    const surface = processor.getSurfaces().get("vega-stable")
    expect(surface).toBeDefined()

    const { container, unmount } = mount(
      <MarkdownComponentContext.Provider value={() => null}>
        <A2uiSurface surface={surface!} />
      </MarkdownComponentContext.Provider>,
    )
    try {
      // dataModel 为空：绑定解析为 undefined → 占位。
      await waitFor(() => {
        expect(
          container.querySelector("[data-vegachart-placeholder]"),
        ).not.toBeNull()
      })

      // spec v1 → mock VegaEmbed 首次挂载（恰好一次）。
      act(() => {
        processor.processMessages(stepUpdateDataModel({ title: "one" }))
      })
      await waitFor(() => expect(mountLog.length).toBe(1))
      expect(renderLog).toContain("one")

      // spec v2（新对象引用，未失败）→ 只重渲染拿到新 spec，不重挂。
      act(() => {
        processor.processMessages(stepUpdateDataModel({ title: "two" }))
      })
      await waitFor(() => expect(renderLog).toContain("two"))
      expect(mountLog.length).toBe(1)

      // spec v3 boom → onError 上报 → 失败态占位；实例复用（原地重渲染）
      // 不产生新 mount，mountLog 不变。
      act(() => {
        processor.processMessages(
          stepUpdateDataModel({ title: "three", boom: true }),
        )
      })
      await waitFor(() => {
        expect(
          container.querySelector("[data-vegachart-placeholder]"),
        ).not.toBeNull()
      })
      expect(renderLog).toContain("three")
      expect(mountLog.length).toBe(1)

      // spec v4 → 上一轮失败过 → 重建 lazy 实例，重试挂载成功。
      act(() => {
        processor.processMessages(stepUpdateDataModel({ title: "four" }))
      })
      await waitFor(() => expect(mountLog.length).toBe(2))
      expect(renderLog).toContain("four")
      expect(container.querySelector("[data-vegachart-placeholder]")).toBeNull()

      // spec v5（新对象引用，未失败）→ 复用实例不再重挂——终审收尾断言。
      // 误重建若存在会在 act 刷新窗口内表现为额外 mount，容错间隔兜底。
      act(() => {
        processor.processMessages(stepUpdateDataModel({ title: "five" }))
      })
      await waitFor(() => expect(renderLog).toContain("five"))
      await new Promise((resolve) => setTimeout(resolve, 100))
      expect(mountLog.length).toBe(2)
    } finally {
      unmount()
      processor.dispose()
    }
  }, 15000)
})
