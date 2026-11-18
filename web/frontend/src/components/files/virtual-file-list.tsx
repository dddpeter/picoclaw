import { useVirtualizer } from "@tanstack/react-virtual"
import { useLayoutEffect, useRef, useState } from "react"

/**
 * Virtualized file list (efficiency follow-up): renders only the rows
 * inside the viewport, so a workspace with thousands of files still scrolls
 * smoothly — scrolling progressively mounts more rows. Rows are fixed
 * height, which keeps measurement cheap.
 *
 * With `minColumnWidth` the rows become a responsive grid: the viewport
 * width is measured (ResizeObserver) and each virtualized row holds as
 * many cells as fit, so wide screens stop wasting horizontal space.
 */

export interface VirtualFileListProps<T> {
  items: T[]
  rowHeight?: number
  overscan?: number
  className?: string
  /** Minimum cell width in px; when set, items flow into a responsive grid. */
  minColumnWidth?: number
  getKey: (item: T) => string
  renderRow: (item: T) => React.ReactNode
}

export function VirtualFileList<T>({
  items,
  rowHeight = 32,
  overscan = 12,
  className,
  minColumnWidth,
  getKey,
  renderRow,
}: VirtualFileListProps<T>) {
  const scrollRef = useRef<HTMLDivElement>(null)
  // 0 until measured → first paint is single-column, then the observer
  // (useLayoutEffect, so before paint) snaps to the real column count.
  const [viewportWidth, setViewportWidth] = useState(0)

  useLayoutEffect(() => {
    const el = scrollRef.current
    if (!el) return
    setViewportWidth(el.clientWidth)
    const observer = new ResizeObserver((entries) => {
      const width = entries[0]?.contentRect.width ?? 0
      setViewportWidth(width)
    })
    observer.observe(el)
    return () => observer.disconnect()
  }, [])

  const columnCount =
    minColumnWidth !== undefined && viewportWidth > 0
      ? Math.max(1, Math.floor(viewportWidth / minColumnWidth))
      : 1
  const rowCount = Math.ceil(items.length / columnCount)

  // eslint-disable-next-line react-hooks/incompatible-library -- TanStack Virtual 返回函数值，React Compiler 无法安全记忆化（已知库限制，无代码侧修法）；该组件未被上层 memo 依赖，跳过编译安全。
  const virtualizer = useVirtualizer({
    count: rowCount,
    getScrollElement: () => scrollRef.current,
    estimateSize: () => rowHeight,
    overscan,
  })

  const gridStyle = minColumnWidth
    ? {
        gridTemplateColumns: `repeat(${columnCount}, minmax(0, 1fr))`,
      }
    : undefined

  // jsdom (and any environment without ResizeObserver) has no layout, so
  // the virtualizer measures a zero-height viewport and renders nothing.
  // Fall back to rendering every row there — the browser path stays
  // virtualized (the fallback only exists for tests/ssr).
  if (typeof ResizeObserver === "undefined") {
    const cells = items.map((item) => (
      <div key={getKey(item)} className="min-w-0" style={{ height: rowHeight }}>
        {renderRow(item)}
      </div>
    ))
    if (gridStyle) {
      return (
        <div
          className={className ?? "min-h-0 flex-1 overflow-y-auto"}
          data-testid="virtual-file-list"
        >
          <div className="grid" style={gridStyle}>
            {cells}
          </div>
        </div>
      )
    }
    return (
      <div
        className={className ?? "min-h-0 flex-1 overflow-y-auto"}
        data-testid="virtual-file-list"
      >
        {cells}
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
        {virtualizer.getVirtualItems().map((row) => {
          const start = row.index * columnCount
          const cells = items.slice(start, start + columnCount)
          return (
            <div
              key={row.key}
              className="absolute top-0 left-0 grid w-full"
              style={{
                height: `${row.size}px`,
                transform: `translateY(${row.start}px)`,
                ...gridStyle,
              }}
            >
              {cells.map((entry) => (
                <div key={getKey(entry)} className="min-w-0">
                  {renderRow(entry)}
                </div>
              ))}
            </div>
          )
        })}
      </div>
    </div>
  )
}
