// @vitest-environment jsdom
// VegaChart catalog 组件测试（A2UI 二期任务 6）：jsdom 环境，断言真实 DOM。
//  1) 合法内联 vega-lite spec 的 VegaChart（经 MessageProcessor + derived
//     catalog 渲染）→ 容器内出现 vega-embed 的 svg.marks 图表 DOM；
//  2) {path} 数据绑定 spec → dataModel 取值渲染（updateDataModel 后图表
//     才出现，标题来自 dataModel 值——证明绑定取值而非占位）；
//  3) 非法 spec（vega-lite 编译失败）→ 组件内占位标记，不抛异常、不炸卡
//     （a2ui-card 容器仍在）。
import {
  A2uiSurface,
  MarkdownComponentContext,
  type ReactCatalogComponent,
} from "@a2ui/react/v0_9"
import { MessageProcessor } from "@a2ui/web_core/v0_9"
import { basicCatalog } from "@a2ui/web_core/v0_9/basic_catalog"
import { type ReactElement, StrictMode, act } from "react"
import { type Root, createRoot } from "react-dom/client"
import { beforeEach, describe, expect, it } from "vitest"

import i18n from "@/i18n"

import { A2uiCard, initA2uiBasicCatalog } from "./a2ui-card"
import { catalogWithVegaChart } from "./a2ui-vega"

// React 19：act 需要显式声明测试环境，否则 createRoot 渲染告警。

;(globalThis as Record<string, unknown>).IS_REACT_ACT_ENVIRONMENT = true

/** 合法 v0.9 消息批：Card → VegaChart（内联 vega-lite 条形图）。遵守一期
 * 全部门：根 id="root"、id 唯一、树不变量、props strict 无 style。 */
const inlineSpecMessages = [
  {
    version: "v0.9",
    createSurface: {
      surfaceId: "vega-inline",
      catalogId: basicCatalog.id,
    },
  },
  {
    version: "v0.9",
    updateComponents: {
      surfaceId: "vega-inline",
      components: [
        { id: "root", component: "Card", child: "chart" },
        {
          id: "chart",
          component: "VegaChart",
          spec: {
            data: {
              values: [
                { category: "A", amount: 28 },
                { category: "B", amount: 55 },
                { category: "C", amount: 43 },
              ],
            },
            mark: "bar",
            encoding: {
              x: { field: "category", type: "nominal" },
              y: { field: "amount", type: "quantitative" },
            },
            title: "Bars From Inline Spec",
          },
        },
      ],
    },
  },
]

/** vega-lite 编译期必抛的非法 spec（实测：未知的 mark 类型在 compile 抛
 * "Cannot read properties of undefined"）。 */
const invalidSpec = {
  mark: "bogus-mark",
  encoding: { x: { field: "a", type: "quantitative" } },
}

const invalidSpecMessages = [
  {
    version: "v0.9",
    createSurface: {
      surfaceId: "vega-invalid",
      catalogId: basicCatalog.id,
    },
  },
  {
    version: "v0.9",
    updateComponents: {
      surfaceId: "vega-invalid",
      components: [
        { id: "root", component: "Card", child: "chart" },
        { id: "chart", component: "VegaChart", spec: invalidSpec },
      ],
    },
  },
]

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

/** 轮询等待断言成立：vega-embed 异步链（懒加载 chunk → compile → View
 * runAsync → svg 落 DOM）跨多个微/宏任务轮次，单次 flush 不可靠。
 * 注意：不能把本循环包进 act()——懒加载 chunk 在 act 刷新窗口内解析时
 * React 19 的 act promise 会挂起，且 act 的逐 tick flush 会让 vega 的
 * rAF 渲染链饿死（实测 svg 永不出现）；裸轮询只读 DOM，React 的挂载/
 * 更新照常在 act(render) 内触发，这里只等异步结果落地。 */
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

describe("VegaChart", () => {
  beforeEach(async () => {
    // 入口初始化（幂等）：注册 universal elements + 注入样式。
    initA2uiBasicCatalog()
    // 占位文案断言固定在 en（i18n 语言探测在 jsdom 下不确定）。
    await i18n.changeLanguage("en")
  })

  it("renders an inline vega-lite spec as a real chart DOM", async () => {
    const { container, unmount } = mount(
      <StrictMode>
        <A2uiCard ui={{ messages: inlineSpecMessages }} />
      </StrictMode>,
    )
    try {
      // VegaChart 的 React 宿主 custom element（toWebComponent 命名为
      // a2ui-react-<name>）与 vega-embed 的 svg 图表 DOM。
      await waitFor(() => {
        expect(container.querySelector("a2ui-react-vegachart")).not.toBeNull()
        expect(container.querySelector("svg.marks")).not.toBeNull()
      })
      // 图表内容来自内联 spec（标题渲染进 svg 文本节点）。
      expect(container.querySelector("svg.marks")?.textContent).toContain(
        "Bars From Inline Spec",
      )
    } finally {
      unmount()
    }
  })

  it("renders a {path}-bound spec after updateDataModel delivers the value", async () => {
    // 直接驱动 MessageProcessor（同 A2uiCard 内部构造），先挂载空
    // dataModel，再 updateDataModel 注入 spec——证明绑定取值 + 响应式
    // 重渲染，而非仅挂载期一次性解析。
    const processor = new MessageProcessor<ReactCatalogComponent>(
      [catalogWithVegaChart],
      undefined,
      { version: "v0.9" },
    )
    processor.processMessages([
      {
        version: "v0.9",
        createSurface: {
          surfaceId: "vega-path",
          catalogId: basicCatalog.id,
        },
      },
      {
        version: "v0.9",
        updateComponents: {
          surfaceId: "vega-path",
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
          surfaceId: "vega-path",
          path: "/",
          value: {},
        },
      },
    ])
    const surface = processor.getSurfaces().get("vega-path")
    expect(surface).toBeDefined()

    const { container, unmount } = mount(
      <StrictMode>
        <MarkdownComponentContext.Provider value={() => null}>
          <A2uiSurface surface={surface!} />
        </MarkdownComponentContext.Provider>
      </StrictMode>,
    )
    try {
      // dataModel 为空：绑定解析为 undefined → 组件内占位，无图表 DOM。
      await waitFor(() => {
        expect(
          container.querySelector("[data-vegachart-placeholder]"),
        ).not.toBeNull()
      })
      expect(container.querySelector("svg.marks")).toBeNull()

      // updateDataModel 注入 spec → {path} 绑定取值 → 图表渲染。
      act(() => {
        processor.processMessages([
          {
            version: "v0.9",
            updateDataModel: {
              surfaceId: "vega-path",
              path: "/chart/spec",
              value: {
                data: {
                  values: [
                    { category: "A", amount: 10 },
                    { category: "B", amount: 20 },
                  ],
                },
                mark: "bar",
                encoding: {
                  x: { field: "category", type: "nominal" },
                  y: { field: "amount", type: "quantitative" },
                },
                title: "Bars From DataModel",
              },
            },
          },
        ])
      })
      await waitFor(() => {
        expect(container.querySelector("svg.marks")).not.toBeNull()
      })
      expect(container.querySelector("svg.marks")?.textContent).toContain(
        "Bars From DataModel",
      )
    } finally {
      unmount()
      processor.dispose()
    }
  })

  it("shows an in-component placeholder for an invalid spec without crashing the card", async () => {
    const { container, unmount } = mount(
      <StrictMode>
        <A2uiCard ui={{ messages: invalidSpecMessages }} />
      </StrictMode>,
    )
    try {
      // vega-lite 编译失败 → onError → 组件内占位标记出现，svg 不存在。
      await waitFor(() => {
        expect(
          container.querySelector("[data-vegachart-placeholder]"),
        ).not.toBeNull()
      })
      expect(container.querySelector("svg.marks")).toBeNull()
      // 不炸卡：A2uiCard 的卡片容器与 basicCatalog 的 Card 宿主仍在，
      // 整卡未被降级为 a2ui 错误占位。
      expect(container.querySelector("a2ui-card")).not.toBeNull()
      expect(container.textContent).not.toContain("could not be displayed")
    } finally {
      unmount()
    }
  })
})
