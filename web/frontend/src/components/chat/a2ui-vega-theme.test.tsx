// @vitest-environment jsdom
// VegaChart 主题联动测试（三期任务 4）：html 根元素 class 切换（主题开关
// = .dark class 增删）→ MutationObserver 防抖 bump themeVersion →
// normalizeVegaSpec 重跑（重新读 --primary）→ 新 spec 对象 → VegaEmbed
// 重嵌，图表颜色跟随主题。独立文件复用 spec-update 的 react-vega mock
// （renderLog 记录每次 embed；vi.mock 文件作用域，不影响其他测试文件）。
import {
  A2uiSurface,
  MarkdownComponentContext,
  type ReactCatalogComponent,
} from "@a2ui/react/v0_9"
import { MessageProcessor } from "@a2ui/web_core/v0_9"
import { basicCatalog } from "@a2ui/web_core/v0_9/basic_catalog"
import { type ReactElement, act } from "react"
import { type Root, createRoot } from "react-dom/client"
import { beforeEach, describe, expect, it, vi } from "vitest"

import i18n from "@/i18n"

import { initA2uiBasicCatalog } from "./a2ui-card"
import { catalogWithVegaChart } from "./a2ui-vega"

const { renderLog } = vi.hoisted(() => ({
  renderLog: [] as string[],
}))

vi.mock("react-vega", async () => {
  return {
    VegaEmbed: (props: { spec?: { title?: string } }) => {
      renderLog.push(String(props.spec?.title))
      return null
    },
  }
})

;(globalThis as Record<string, unknown>).IS_REACT_ACT_ENVIRONMENT = true

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

describe("VegaChart theme linking", () => {
  beforeEach(async () => {
    initA2uiBasicCatalog()
    await i18n.changeLanguage("en")
    renderLog.length = 0
    document.documentElement.className = ""
  })

  it("re-embeds the chart when the theme class toggles", async () => {
    const processor = new MessageProcessor<ReactCatalogComponent>(
      [catalogWithVegaChart],
      undefined,
      { version: "v0.9" },
    )
    processor.processMessages([
      {
        version: "v0.9",
        createSurface: {
          surfaceId: "vega-theme",
          catalogId: basicCatalog.id,
        },
      },
      {
        version: "v0.9",
        updateComponents: {
          surfaceId: "vega-theme",
          components: [
            { id: "root", component: "Card", child: "chart" },
            {
              id: "chart",
              component: "VegaChart",
              spec: { mark: "bar", title: "theme-one" },
            },
          ],
        },
      },
    ])
    const surface = processor.getSurfaces().get("vega-theme")
    expect(surface).toBeDefined()

    const { container, unmount } = mount(
      <MarkdownComponentContext.Provider value={() => null}>
        <A2uiSurface surface={surface!} />
      </MarkdownComponentContext.Provider>,
    )
    try {
      // 首次 embed：spec 归一化初值。
      await waitFor(() => expect(renderLog).toContain("theme-one"))

      // 主题切换（.dark class 增删）→ 防抖 300ms 后重归一化 → 新 spec
      // 对象 → 重嵌（renderLog 增长）。旧实现无 observer → renderLog
      // 不变，断言失败。
      renderLog.length = 0
      act(() => {
        document.documentElement.classList.add("dark")
      })
      await waitFor(
        () => expect(renderLog.length).toBeGreaterThan(0),
        3000,
      )

      // 回切主题同样触发。
      renderLog.length = 0
      act(() => {
        document.documentElement.classList.remove("dark")
      })
      await waitFor(
        () => expect(renderLog.length).toBeGreaterThan(0),
        3000,
      )
      // 主题切换不重挂 lazy 实例（重嵌 ≠ 重建组件类型）。
      expect(container.querySelector("[data-vegachart-placeholder]")).toBeNull()
    } finally {
      unmount()
    }
  })
})
