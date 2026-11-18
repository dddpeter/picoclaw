import { useVirtualizer } from "@tanstack/react-virtual"
import { useRef } from "react"

/**
 * Virtualized file list (efficiency follow-up): renders only the rows
 * inside the viewport, so a workspace with thousands of files still scrolls
 * smoothly. Rows are fixed height, which keeps measurement cheap.
 */

export interface VirtualFileListProps<T> {
  items: T[]
  rowHeight?: number
  overscan?: number
  className?: string
  getKey: (item: T) => string
  renderRow: (item: T) => React.ReactNode
}

export function VirtualFileList<T>({
  items,
  rowHeight = 32,
  overscan = 12,
  className,
  getKey,
  renderRow,
}: VirtualFileListProps<T>) {
  const scrollRef = useRef<HTMLDivElement>(null)
  // eslint-disable-next-line react-hooks/incompatible-library -- TanStack Virtual 返回函数值，React Compiler 无法安全记忆化（已知库限制，无代码侧修法）；该组件未被上层 memo 依赖，跳过编译安全。
  const virtualizer = useVirtualizer({
    count: items.length,
    getScrollElement: () => scrollRef.current,
    estimateSize: () => rowHeight,
    overscan,
  })

  // jsdom (and any environment without ResizeObserver) has no layout, so
  // the virtualizer measures a zero-height viewport and renders nothing.
  // Fall back to rendering every row there — the browser path stays
  // virtualized (the fallback only exists for tests/ssr).
  if (typeof ResizeObserver === "undefined") {
    return (
      <div
        className={className ?? "min-h-0 flex-1 overflow-y-auto"}
        data-testid="virtual-file-list"
      >
        {items.map((item) => (
          <div key={getKey(item)} style={{ height: rowHeight }}>
            {renderRow(item)}
          </div>
        ))}
      </div>
    )
  }

  return (
    <div
      ref={scrollRef}
      className={className ?? "min-h-0 flex-1 overflow-y-auto"}
      data-testid="virtual-file-list"
    >
      <div
        className="relative w-full"
        style={{ height: `${virtualizer.getTotalSize()}px` }}
      >
        {virtualizer.getVirtualItems().map((item) => {
          const entry = items[item.index]
          return (
            <div
              key={getKey(entry)}
              className="absolute top-0 left-0 w-full"
              style={{
                height: `${item.size}px`,
                transform: `translateY(${item.start}px)`,
              }}
            >
              {renderRow(entry)}
            </div>
          )
        })}
      </div>
    </div>
  )
}
