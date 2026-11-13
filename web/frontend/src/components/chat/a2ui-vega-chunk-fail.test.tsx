// @vitest-environment jsdom
// VegaChart chunk 加载被拒测试（fix round 1，配套 a2ui-vega.test.tsx）：
// react-vega mock 工厂恒抛（模拟网络抖动/网关重部署后长驻聊天页请求旧
// hash chunk 404），断言失败落在组件内占位——VegaChartChunkBoundary 拦截
// lazy rejection（Suspense 只兜 pending 不兜 rejection），不冒泡到卡级
// A2uiRenderBoundary 把整卡打成占位。独立文件：vi.mock 文件作用域恒拒，
// 不影响 a2ui-vega.test.tsx 的真实渲染链（vitest 按文件隔离模块图）。
import { basicCatalog } from "@a2ui/web_core/v0_9/basic_catalog"
import { StrictMode, act } from "react"
import { createRoot } from "react-dom/client"
import { beforeEach, describe, expect, it, vi } from "vitest"

import i18n from "@/i18n"

import { A2uiCard, initA2uiBasicCatalog } from "./a2ui-card"

vi.mock("react-vega", () => {
  throw new Error("chunk 404 (mock)")
})

// React 19：act 需要显式声明测试环境，否则 createRoot 渲染告警。
;(globalThis as Record<string, unknown>).IS_REACT_ACT_ENVIRONMENT = true

const messages = [
  {
    version: "v0.9",
    createSurface: {
      surfaceId: "vega-chunk-fail",
      catalogId: basicCatalog.id,
    },
  },
  {
    version: "v0.9",
    updateComponents: {
      surfaceId: "vega-chunk-fail",
      components: [
        { id: "root", component: "Card", child: "chart" },
        {
          id: "chart",
          component: "VegaChart",
          spec: {
            data: { values: [{ category: "A", amount: 1 }] },
            mark: "bar",
            encoding: {
              x: { field: "category", type: "nominal" },
              y: { field: "amount", type: "quantitative" },
            },
          },
        },
      ],
    },
  },
]

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

describe("VegaChart chunk load failure", () => {
  beforeEach(async () => {
    initA2uiBasicCatalog()
    await i18n.changeLanguage("en")
  })

  it("falls back in-component instead of crashing the whole card", async () => {
    const container = document.createElement("div")
    document.body.appendChild(container)
    const root = createRoot(container)
    act(() => {
      root.render(
        <StrictMode>
          <A2uiCard ui={{ messages }} />
        </StrictMode>,
      )
    })
    try {
      // chunk 加载被拒 → 组件内边界拦截 → 图表槽位落占位，无图表 DOM。
      await waitFor(() => {
        expect(
          container.querySelector("[data-vegachart-placeholder]"),
        ).not.toBeNull()
      })
      expect(container.querySelector("svg.marks")).toBeNull()
      // 关键断言：失败没有升级为整卡失败——卡级错误占位（"This
      // interactive view could not be displayed."）不出现，卡片容器与
      // basicCatalog Card 宿主仍在。
      expect(container.querySelector("a2ui-card")).not.toBeNull()
      expect(container.textContent).not.toContain("could not be displayed")
    } finally {
      act(() => {
        root.unmount()
      })
      container.remove()
    }
  })
})
