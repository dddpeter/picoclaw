/**
 * BubblesLoading — 对齐 LobeHub @lobehub/ui 的 BubblesLoading（REQ-004）。
 * 视觉：3 个圆点（cx=7/30/53, cy=16, r=6），1.5s 波浪起伏动画，
 * delay 0 / 0.3 / 0.6s，仅 transform/opacity，fill 取次要文字色。
 * 替换 pi-chat.css 原 .thinking-wait 的文字 + 闪烁圆点视觉；
 * 空流式占位的组件逻辑（isEmptyStreaming）保留不变。
 */
import { memo } from "react"

export const BubblesLoading = memo(function BubblesLoading() {
  return (
    <svg
      aria-label="loading"
      className="lobe-bubbles-loading"
      height="32"
      viewBox="0 0 60 32"
      width="60"
    >
      <circle className="lobe-bubbles-dot" cx="7" cy="16" data-delay="0" r="6" />
      <circle
        className="lobe-bubbles-dot"
        cx="30"
        cy="16"
        data-delay="1"
        r="6"
      />
      <circle
        className="lobe-bubbles-dot"
        cx="53"
        cy="16"
        data-delay="2"
        r="6"
      />
    </svg>
  )
})
