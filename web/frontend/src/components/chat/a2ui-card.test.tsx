// @vitest-environment jsdom
// A2uiCard 组件测试：jsdom 环境（React 渲染 + basicCatalog custom elements）。
// 断言真实 DOM：
//  1) 合法 v0.9 createSurface/updateComponents/updateDataModel 消息 →
//     容器内出现 basicCatalog 的 custom element（实测渲染进 light DOM），
//     且 Lit 异步渲染链走完后数据模型文本真正出现（非"空标签"）；
//  2) 畸形 messages（v0.9 封包是 strict 的，多余顶层键被 zod 拒）→
//     渲染错误占位文案，而非抛异常炸聊天流；
//  3) 三期任务 1 增量 processor：流式追加 updateDataModel 帧（不含输入
//     path）→ 同一 input 元素实例保留未提交输入且新文本渲染；追加含
//     createSurface 帧 → processor 全量重建，重 create 接管渲染新内容。
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

/** createRoot + act 渲染（仓库无 @testing-library，按最小依赖自建）。
 * render 供流式更新用例重渲染（store 广播语义 = 每次 ui.messages 都是
 * 新数组引用，act 内同步冲刷 layout effect + setState）。 */
function mount(ui: ReactElement) {
  const container = document.createElement("div")
  document.body.appendChild(container)
  const root: Root = createRoot(container)
  const render = (element: ReactElement) => {
    act(() => {
      root.render(element)
    })
  }
  render(ui)
  return {
    container,
    render,
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

/** 本地态保留实证批次（三期任务 1）：Text 绑 /title（追加帧改写它，产生
 * "新文本渲染"断言点），TextField 绑 /form/name（fill 后追加帧不含该
 * path——旧实现整卡重建会以 wire 批次里的 "张三" 重挂输入，输入丢失）。
 * clone() 供各用例取独副本：真实链路里 store 每次广播都是全新
 * JSON.parse 的帧对象，测试间共享帧引用会让 signal/binder 层的对象身份
 * 缓存跨测试串味（实测教训）。 */
const streamingMessages = [
  {
    version: "v0.9",
    createSurface: {
      surfaceId: "stream-test",
      catalogId: basicCatalog.id,
    },
  },
  {
    version: "v0.9",
    updateComponents: {
      surfaceId: "stream-test",
      components: [
        { id: "root", component: "Card", child: "col" },
        { id: "col", component: "Column", children: ["titleText", "nameField"] },
        { id: "titleText", component: "Text", text: { path: "/title" } },
        {
          id: "nameField",
          component: "TextField",
          label: "Name",
          value: { path: "/form/name" },
        },
      ],
    },
  },
  {
    version: "v0.9",
    updateDataModel: {
      surfaceId: "stream-test",
      path: "/",
      value: { title: "Hello A2UI", form: { name: "张三" } },
    },
  },
]

/** 每用例独占的批次副本（wire 语义：store 每次广播都是全新 JSON.parse 的
 * 帧对象）。测试间共享帧对象会让 signal/binder 层按对象身份缓存的状态跨
 * 测试串味（实测：前例的 /title 增量值渗入后例首屏）。 */
function freshStreamingBatch(): unknown[] {
  return JSON.parse(JSON.stringify(streamingMessages)) as unknown[]
}

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

  // 三期任务 1 红线（本地态保留实证）：流式追加一帧 updateDataModel（不含
  // 输入的 path）→ 增量喂帧，同一 input 元素实例保留未提交输入，同时新
  // 文本渲染。旧实现（useMemo 整卡重建）下 input 被重挂、值回落到 wire
  // 批次里的 "张三" —— 本用例在旧实现下必然失败。
  it("preserves uncommitted form input across a streamed incremental update", async () => {
    const initial = freshStreamingBatch()
    const { container, render, unmount } = mount(
      <StrictMode>
        <A2uiCard ui={{ messages: initial }} />
      </StrictMode>,
    )
    try {
      await act(async () => {
        await waitFor(() => {
          expect(
            container.querySelector("a2ui-basic-textfield input"),
          ).not.toBeNull()
        })
      })
      const inputBefore = container.querySelector<HTMLInputElement>(
        "a2ui-basic-textfield input",
      )!
      // fill：双向往 dataModel /form/name 写值（Lit @input → setValue）。
      act(() => {
        inputBefore.value = "用户未提交的输入"
        inputBefore.dispatchEvent(new Event("input", { bubbles: true }))
      })

      // 流式增量（pico 合并广播语义）：累计批 append 尾部，message.update
      // 携全新数组引用换卡数据（protocol.ts parseA2uiFromPayload）。
      const updated = [
        ...initial,
        {
          version: "v0.9",
          updateDataModel: {
            surfaceId: "stream-test",
            path: "/title",
            value: "流式更新后的标题",
          },
        },
      ]
      act(() => {
        render(
          <StrictMode>
            <A2uiCard ui={{ messages: updated }} />
          </StrictMode>,
        )
      })
      await act(async () => {
        await waitFor(() => {
          expect(container.textContent).toContain("流式更新后的标题")
        })
      })

      // 同一 input 元素实例（未重挂）+ 未提交输入仍在 DOM。
      const inputAfter = container.querySelector<HTMLInputElement>(
        "a2ui-basic-textfield input",
      )
      expect(inputAfter).toBe(inputBefore)
      expect(inputAfter?.value).toBe("用户未提交的输入")
    } finally {
      unmount()
    }
  })

  // pico 合并语义（pico_test.go:1355 钉住）：同 path 的 updateDataModel
  // 帧被原地覆盖（last value wins），累计批长度不变、前缀内容变化。只看
  // 尾部追加的增量会漏掉这次更新（卡面停留旧值）——逐帧指纹比对从首个
  // 分歧帧起重播，与整批重播收敛到同一最终状态。
  it("applies an in-place overwritten updateDataModel frame when the batch length is unchanged", async () => {
    const initial = freshStreamingBatch()
    const { container, render, unmount } = mount(
      <StrictMode>
        <A2uiCard ui={{ messages: initial }} />
      </StrictMode>,
    )
    try {
      await act(async () => {
        await waitFor(() => {
          expect(container.textContent).toContain("Hello A2UI")
        })
      })

      // 长度不变、最后一帧（path "/"）整值覆盖——pico 合并的 last-value-wins
      // （未变帧保持同一引用，覆盖帧是全新对象，与 mergeA2UIFrames 的
      // copy-in-place 语义一致）。
      const overwritten = [
        initial[0],
        initial[1],
        {
          version: "v0.9",
          updateDataModel: {
            surfaceId: "stream-test",
            path: "/",
            value: { title: "覆盖后的标题", form: { name: "张三" } },
          },
        },
      ]
      act(() => {
        render(
          <StrictMode>
            <A2uiCard ui={{ messages: overwritten }} />
          </StrictMode>,
        )
      })
      await act(async () => {
        await waitFor(() => {
          expect(container.textContent).toContain("覆盖后的标题")
        })
      })
      expect(container.textContent).not.toContain("Hello A2UI")
    } finally {
      unmount()
    }
  })

  // 三期任务 1 红线（重 create 接管）：追加含 createSurface 帧的批次 →
  // processor 全量重建（同 surfaceId 重 create 按 RenderUIHistoryFold 语义
  // 取最后一张卡），新内容渲染、旧内容消亡。旧实现下累计批含两个
  // createSurface，整批重播被 processor "already exists" 拒 → 错误占位，
  // 本用例在旧实现下必然失败。
  it("rebuilds the processor when a re-arriving createSurface takes over the surface", async () => {
    const initial = freshStreamingBatch()
    const { container, render, unmount } = mount(
      <StrictMode>
        <A2uiCard ui={{ messages: initial }} />
      </StrictMode>,
    )
    try {
      await act(async () => {
        await waitFor(() => {
          expect(
            container.querySelector("a2ui-basic-textfield input"),
          ).not.toBeNull()
        })
      })

      // 重 create 接管批次：同 surfaceId 再 create + 新组件树 + 新数据。
      const updated = [
        ...initial,
        {
          version: "v0.9",
          createSurface: {
            surfaceId: "stream-test",
            catalogId: basicCatalog.id,
          },
        },
        {
          version: "v0.9",
          updateComponents: {
            surfaceId: "stream-test",
            components: [
              { id: "root", component: "Card", child: "note" },
              { id: "note", component: "Text", text: "重建后的卡片" },
            ],
          },
        },
        {
          version: "v0.9",
          updateDataModel: {
            surfaceId: "stream-test",
            path: "/",
            value: {},
          },
        },
      ]
      act(() => {
        render(
          <StrictMode>
            <A2uiCard ui={{ messages: updated }} />
          </StrictMode>,
        )
      })
      await act(async () => {
        await waitFor(() => {
          expect(container.textContent).toContain("重建后的卡片")
        })
      })

      // 旧 incarnation 被丢弃（fold 取最后一张卡）：旧文本与旧输入不在。
      expect(container.textContent).not.toContain("Hello A2UI")
      expect(container.querySelector("a2ui-basic-textfield")).toBeNull()
    } finally {
      unmount()
    }
  })
})
