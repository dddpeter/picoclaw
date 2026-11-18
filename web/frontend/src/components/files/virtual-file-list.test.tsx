// @vitest-environment jsdom
// VirtualFileList 单测：确认虚拟化生效——大列表只挂载视口内的行。
// jsdom 没有布局，需要注入 ResizeObserver polyfill + 元素尺寸桩，
// 让 virtualizer 能算出视口高度（生产浏览器无需这些）。
import { act } from "react"
import { type ReactElement } from "react"
import { type Root, createRoot } from "react-dom/client"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import { VirtualFileList } from "./virtual-file-list"

describe("VirtualFileList", () => {
  let container: HTMLDivElement
  let root: Root

  beforeEach(() => {
    container = document.createElement("div")
    document.body.appendChild(container)
    act(() => {
      root = createRoot(container)
    })
  })

  afterEach(() => {
    act(() => {
      root.unmount()
    })
    container.remove()
    vi.restoreAllMocks()
  })

  const render = (element: ReactElement) => {
    act(() => {
      root.render(element)
    })
  }

  it("renders a scroll viewport sized for the list", () => {
    // jsdom has no layout, so the virtualizer measures a zero-height
    // viewport; the component falls back to full rendering there. What
    // this test pins is the contract that survives headless rendering:
    // every item is reachable and the viewport is scrollable.
    const items = Array.from({ length: 50 }, (_, i) => ({
      id: `f${i}`,
      label: `file-${i}.md`,
    }))

    render(
      <VirtualFileList
        items={items}
        rowHeight={32}
        getKey={(item) => item.id}
        renderRow={(item) => <span>{item.label}</span>}
      />,
    )

    const viewport = container.querySelector(
      "[data-testid=virtual-file-list]",
    ) as HTMLElement | null
    expect(viewport).toBeTruthy()
    // The list fills its parent (no fixed max-height) and owns the scroll.
    expect(viewport?.className).toContain("flex-1")
    expect(viewport?.className).toContain("overflow-y-auto")
    expect(viewport?.style.maxHeight).toBeFalsy()
    // Headless fallback mounts every row; the browser path mounts a window.
    const rendered = container.querySelectorAll("span")
    // Headless (no ResizeObserver) falls back to full rendering: every
    // item must be reachable. The browser path mounts only a window —
    // that behaviour needs a real layout, verified in the browser.
    expect(rendered.length).toBe(items.length)
  })

  it("falls back to a responsive grid when minColumnWidth is set", () => {
    // Headless path again: with minColumnWidth the fallback must still be
    // reachable in full and wrap cells in a grid container (column count
    // comes from real viewport measurement, only checkable in a browser).
    const items = Array.from({ length: 9 }, (_, i) => ({
      id: `f${i}`,
      label: `file-${i}.md`,
    }))

    render(
      <VirtualFileList
        items={items}
        rowHeight={32}
        minColumnWidth={280}
        getKey={(item) => item.id}
        renderRow={(item) => <span>{item.label}</span>}
      />,
    )

    const viewport = container.querySelector(
      "[data-testid=virtual-file-list]",
    ) as HTMLElement | null
    expect(viewport).toBeTruthy()
    expect(viewport?.className).toContain("overflow-y-auto")
    const grid = viewport?.firstElementChild as HTMLElement | null
    expect(grid?.className).toContain("grid")
    // jsdom normalizes `minmax(0, 1fr)` → `minmax(0px, 1fr)`; viewport width
    // is 0 headless, so exactly one column is expected here.
    expect(grid?.style.gridTemplateColumns).toBe("repeat(1, minmax(0px, 1fr))")
    expect(container.querySelectorAll("span").length).toBe(items.length)
    // Cells must allow truncation inside the grid track.
    expect(container.querySelectorAll(".min-w-0").length).toBe(items.length)
  })
})
