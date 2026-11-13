// @vitest-environment jsdom
// A2uiCard 组件测试：jsdom 环境（React 渲染 + basicCatalog custom elements）。
// 断言真实 DOM：
//  1) 合法 v0.9 createSurface/updateComponents/updateDataModel 消息 →
//     容器内出现 basicCatalog 的 custom element（实测渲染进 light DOM），
//     且 Lit 异步渲染链走完后数据模型文本真正出现（非"空标签"）；
//  2) 畸形 messages（v0.9 封包是 strict 的，多余顶层键被 zod 拒）→
//     渲染错误占位文案，而非抛异常炸聊天流。
import { basicCatalog } from "@a2ui/web_core/v0_9/basic_catalog"
import { type ReactElement, StrictMode, act } from "react"
import { type Root, createRoot } from "react-dom/client"
import { beforeEach, describe, expect, it, vi } from "vitest"

import { sendChatMessage } from "@/features/chat/controller"
import i18n from "@/i18n"

import { A2uiCard, initA2uiBasicCatalog } from "./a2ui-card"

// 动作回传走普通聊天消息（ask-card 同一条 sendChatMessage 路径）：mock
// 发送函数断言消息文本。importOriginal 保留模块其余导出，controller 的
// 其他消费方不受影响。
vi.mock("@/features/chat/controller", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/features/chat/controller")>()),
  sendChatMessage: vi.fn(() => true),
}))

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

/** 带表单的合法批次：TextField 用 {path} 双向绑定写 dataModel，提交
 * Button 的 event.context 用 {path} 绑定在点击瞬间从 dataModel 收集表单
 * 值；第二个按钮无 context（验证消息不带 context 段）。遵守一期全部门：
 * 根 id="root"、id 唯一、树不变量、无 style props。 */
const interactiveMessages = [
  {
    version: "v0.9",
    createSurface: {
      surfaceId: "form-test",
      catalogId: basicCatalog.id,
    },
  },
  {
    version: "v0.9",
    updateComponents: {
      surfaceId: "form-test",
      components: [
        { id: "root", component: "Card", child: "col" },
        {
          id: "col",
          component: "Column",
          children: ["nameField", "submitBtn", "noopBtn"],
        },
        {
          id: "nameField",
          component: "TextField",
          label: "Name",
          value: { path: "/form/name" },
        },
        {
          id: "submitBtn",
          component: "Button",
          child: "submitLabel",
          action: {
            event: {
              name: "submit",
              context: { name: { path: "/form/name" } },
            },
          },
        },
        { id: "submitLabel", component: "Text", text: "提交" },
        {
          id: "noopBtn",
          component: "Button",
          child: "noopLabel",
          action: { event: { name: "noop" } },
        },
        { id: "noopLabel", component: "Text", text: "无参" },
      ],
    },
  },
  {
    version: "v0.9",
    updateDataModel: {
      surfaceId: "form-test",
      path: "/",
      value: { form: { name: "张三" } },
    },
  },
]

describe("A2uiCard", () => {
  beforeEach(async () => {
    // 入口初始化（幂等）：注册 universal elements + 注入样式。
    initA2uiBasicCatalog()
    // 占位文案断言固定在 en（i18n 语言探测在 jsdom 下不确定）。
    await i18n.changeLanguage("en")
    vi.mocked(sendChatMessage).mockClear()
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

  it("sends the a2ui action message through the chat send path on button click", async () => {
    const { container, unmount } = mount(
      <StrictMode>
        <A2uiCard ui={{ messages: interactiveMessages }} />
      </StrictMode>,
    )
    try {
      // Lit 异步渲染链走完后按钮才存在；两个按钮按 DOM 顺序：submitBtn、
      // noopBtn。basicCatalog 是 light DOM 渲染，内部 <button> 可直接触达。
      await act(async () => {
        await waitFor(() => {
          expect(
            container.querySelectorAll("a2ui-basic-button button").length,
          ).toBe(2)
        })
      })
      const buttons = container.querySelectorAll<HTMLButtonElement>(
        "a2ui-basic-button button",
      )
      const submit = buttons[0]
      act(() => {
        submit.click()
      })
      await act(async () => {
        await waitFor(() => {
          expect(vi.mocked(sendChatMessage)).toHaveBeenCalledTimes(1)
        })
      })
      // 消息格式逐字锁定：前缀 + surface/action/source + context 紧凑
      // JSON（表单值经 event.context 的 {path} 绑定从 dataModel 收集）。
      expect(vi.mocked(sendChatMessage).mock.calls[0][0]).toEqual({
        content:
          '[A2UI 交互] surface="form-test" action="submit" source="submitBtn" context={"name":"张三"}',
        attachments: [],
      })
    } finally {
      unmount()
    }
  })

  it("omits the context segment when the action carries no context", async () => {
    const { container, unmount } = mount(
      <StrictMode>
        <A2uiCard ui={{ messages: interactiveMessages }} />
      </StrictMode>,
    )
    try {
      await act(async () => {
        await waitFor(() => {
          expect(
            container.querySelectorAll("a2ui-basic-button button").length,
          ).toBe(2)
        })
      })
      const noop = container.querySelectorAll<HTMLButtonElement>(
        "a2ui-basic-button button",
      )[1]
      act(() => {
        noop.click()
      })
      await act(async () => {
        await waitFor(() => {
          expect(vi.mocked(sendChatMessage)).toHaveBeenCalledTimes(1)
        })
      })
      expect(vi.mocked(sendChatMessage).mock.calls[0][0].content).toBe(
        '[A2UI 交互] surface="form-test" action="noop" source="noopBtn"',
      )
    } finally {
      unmount()
    }
  })
})
