// @vitest-environment jsdom
// F3 尾部窗口裁剪：长会话只挂载最近 N 个 turn，顶部按钮步进展开；
// 换会话（首消息 id 变化）重置窗口。
import { act } from "react"
import { createRoot, type Root } from "react-dom/client"
import { afterEach, describe, expect, it } from "vitest"

import type { ChatMessage } from "@/store/chat"

import { PiMessageList } from "./pi-message-list"

;(globalThis as Record<string, unknown>).IS_REACT_ACT_ENVIRONMENT = true

function mount(ui: React.ReactElement) {
  const container = document.createElement("div")
  document.body.appendChild(container)
  const root: Root = createRoot(container)
  act(() => {
    root.render(ui)
  })
  return {
    container,
    rerender(next: React.ReactElement) {
      act(() => {
        root.render(next)
      })
    },
    unmount() {
      act(() => {
        root.unmount()
      })
      container.remove()
    },
  }
}

function turns(count: number): ChatMessage[] {
  const messages: ChatMessage[] = []
  for (let i = 0; i < count; i++) {
    messages.push({ id: `u${i}`, role: "user", content: `问 ${i}`, timestamp: 1_700_000_000 + i })
    messages.push({ id: `a${i}`, role: "assistant", content: `答 ${i}`, timestamp: 1_700_000_000 + i })
  }
  return messages
}

afterEach(() => {
  document.body.innerHTML = ""
})

describe("PiMessageList 尾部窗口（F3）", () => {
  it("60 个 turn 默认只挂载最近 50 个，顶部按钮提示剩余数", () => {
    const view = mount(
      <PiMessageList messages={turns(60)} isTyping={false} detail="all" />,
    )
    const rendered = view.container.querySelectorAll("[data-turn-window]")
    expect(rendered.length).toBeGreaterThan(0)
    const button = view.container.querySelector("[data-expand-earlier]")
    expect(button?.textContent).toContain("10")
    view.unmount()
  })

  it("点击按钮步进展开，全部展开后按钮消失", () => {
    const view = mount(
      <PiMessageList messages={turns(60)} isTyping={false} detail="all" />,
    )
    const button = () => view.container.querySelector("[data-expand-earlier]")
    act(() => {
      button()?.dispatchEvent(new MouseEvent("click", { bubbles: true }))
    })
    // 50 + 50 > 60 → 全部可见，按钮消失。
    expect(button()).toBeNull()
    const items = view.container.querySelectorAll("[data-turn-window]")
    expect(items.length).toBe(60)
    view.unmount()
  })

  it("首消息 id 变化（换会话）重置窗口", () => {
    const view = mount(
      <PiMessageList messages={turns(60)} isTyping={false} detail="all" />,
    )
    act(() => {
      view.container
        .querySelector("button")
        ?.dispatchEvent(new MouseEvent("click", { bubbles: true }))
    })
    expect(view.container.querySelector("[data-expand-earlier]")).toBeNull()

    // 同一根上换到只有 3 个 turn 的新会话：窗口重置后全部可见且无按钮。
    const session2 = turns(3).map((m, i) => ({
      ...m,
      id: `s2-${m.id}-${i}`,
    }))
    view.rerender(
      <PiMessageList messages={session2} isTyping={false} detail="all" />,
    )
    expect(view.container.querySelectorAll("[data-turn-window]").length).toBe(
      3,
    )
    expect(view.container.querySelector("[data-expand-earlier]")).toBeNull()
    view.unmount()
  })
})
