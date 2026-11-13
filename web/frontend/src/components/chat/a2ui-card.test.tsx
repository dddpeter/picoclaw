// @vitest-environment jsdom
// A2uiCard 组件测试：jsdom 环境（React 渲染 + basicCatalog custom elements）。
// 断言真实 DOM：
//  1) 合法 v0.9 createSurface/updateComponents/updateDataModel 消息 →
//     容器内出现 basicCatalog 的 custom element（实测渲染进 light DOM），
//     且 Lit 异步渲染链走完后数据模型文本真正出现（非"空标签"）；
//  2) 畸形 messages（v0.9 封包是 strict 的，多余顶层键被 zod 拒）→
//     渲染错误占位文案，而非抛异常炸聊天流。
import { type ReactElement, StrictMode, act } from "react"
import { type Root, createRoot } from "react-dom/client"
import { beforeEach, describe, expect, it } from "vitest"

import { basicCatalog } from "@a2ui/web_core/v0_9/basic_catalog"

import i18n from "@/i18n"

import { A2uiCard, initA2uiBasicCatalog } from "./a2ui-card"

// React 19：act 需要显式声明测试环境，否则 createRoot 渲染告警。
;(globalThis as Record<string, unknown>).IS_REACT_ACT_ENVIRONMENT = true

/** 合法 v0.9 消息：Card（child 单引用）+ Text（path 绑定数据模型）。 */
const validMessages = [
  {
    version: "v0.9",
    createSurface: {
      surfaceId: "card-test",
      catalogId: basicCatalog.id,
    },
  },
  {
    version: "v0.9",
    updateComponents: {
      surfaceId: "card-test",
      components: [
        { id: "root", component: "Card", child: "title" },
        { id: "title", component: "Text", text: { path: "/title" } },
      ],
    },
  },
  {
    version: "v0.9",
    updateDataModel: {
      surfaceId: "card-test",
      path: "/",
      value: { title: "Hello A2UI" },
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

/** 轮询等待断言成立：Lit 异步渲染链（card → child text → markdown
 * host → React portal）跨多个微/宏任务轮次，单次 flush 不可靠。 */
async function waitFor(ready: () => void, timeoutMs = 1000): Promise<void> {
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

describe("A2uiCard", () => {
  beforeEach(async () => {
    // 入口初始化（幂等）：注册 universal elements + 注入样式。
    initA2uiBasicCatalog()
    // 占位文案断言固定在 en（i18n 语言探测在 jsdom 下不确定）。
    await i18n.changeLanguage("en")
  })

  it("renders basic catalog custom elements for a valid v0.9 surface", async () => {
    const { container, unmount } = mount(
      <StrictMode>
        <A2uiCard ui={{ messages: validMessages }} />
      </StrictMode>,
    )
    try {
      // custom element 宿主同步出现；注册成功则已是升级后的类实例
      // （漏注册 = 空标签 HTMLUnknownElement）。
      const card = container.querySelector("a2ui-card")
      expect(card).not.toBeNull()
      expect(card).toBeInstanceOf(customElements.get("a2ui-card"))

      // 子组件与数据模型文本经 Lit 异步链路进入 light DOM。
      await act(async () => {
        await waitFor(() => {
          expect(container.querySelector("a2ui-basic-text")).not.toBeNull()
        })
      })
      expect(container.textContent).toContain("Hello A2UI")
    } finally {
      unmount()
    }
  })

  it("renders an error placeholder instead of throwing for malformed messages", () => {
    // strict 封包：多余顶层键在 zod 校验层被拒（processMessages 抛
    // A2uiValidationError）→ 组件必须以占位文案兜底，不得冒泡。
    const malformed = [
      {
        version: "v0.9",
        createSurface: {
          surfaceId: "bad",
          catalogId: basicCatalog.id,
        },
        extra: "unrecognized top-level key",
      },
    ]
    const { container, unmount } = mount(
      <StrictMode>
        <A2uiCard ui={{ messages: malformed }} />
      </StrictMode>,
    )
    try {
      expect(container.textContent).toContain("could not be displayed")
      expect(container.querySelector("a2ui-card")).toBeNull()
    } finally {
      unmount()
    }
  })
})
