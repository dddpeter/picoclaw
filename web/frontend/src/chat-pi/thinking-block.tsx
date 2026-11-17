// 移植自 pi-web-ui（MIT License）web/src/components/ThinkingBlock.tsx。
// SPDX-License-Identifier: MIT
// 适配点：i18n 由 pi-web-ui 的 useT() 换成 react-i18next 的 useTranslation()。
import { IconChevronDown, IconChevronRight, IconCpu } from "@tabler/icons-react"
import { useId, useState } from "react"
import { useTranslation } from "react-i18next"

import { MessageActionsBar } from "@/components/chat/message-actions-bar"

/** 折叠预览纯函数：流式取实时尾巴，结束取开头一行。单测直引此处，禁止在测试里抄一份实现。 */
export function thinkingPreview(thinking: string, streaming?: boolean): string {
  return streaming
    ? thinking.trimEnd().slice(-80)
    : thinking.split("\n")[0].slice(0, 80)
}

interface ThinkingBlockProps {
  thinking: string
  /** True while the assistant is still streaming this thinking block. */
  streaming?: boolean
  /** 「完整显示思考」开关：true（开）→ 思考始终完整展开并自动换行
   *  （流式推理过程也实时可见）；false（关）→ 折叠成一行摘要，流式中一行
   *  实时显示最新文本。 */
  wrap?: boolean
}

export function ThinkingBlock({
  thinking,
  streaming,
  wrap = true,
}: ThinkingBlockProps) {
  const { t } = useTranslation()
  const bodyId = useId()
  // null = 未手动点过 → 跟随开关：wrap=true（开）→ 完整展开；wrap=false（关）→ 折叠。
  const [open, setOpen] = useState<boolean | null>(null)
  const expanded = open ?? wrap
  const shown = expanded
  // 折叠预览：流式中取最新文本（实时尾巴），结束后取开头一行。
  const preview = thinkingPreview(thinking, streaming)

  return (
    <div
      className={`thinking ${shown ? "open" : ""} ${streaming ? "live" : ""}`}
    >
      <div
        className="chead thinking-head"
        role="button"
        tabIndex={0}
        aria-expanded={shown}
        aria-controls={bodyId}
        aria-label={shown ? t("piChat.collapseMsg") : t("piChat.expandMsg")}
        title={shown ? t("piChat.collapseMsg") : t("piChat.expandMsg")}
        onClick={() => setOpen(!expanded)}
        onKeyDown={(e) => {
          if (e.target !== e.currentTarget) return
          if (e.key === "Enter" || e.key === " ") {
            e.preventDefault()
            setOpen(!expanded)
          }
        }}
      >
        <button
          type="button"
          className="chead-toggle thinking-toggle"
          title={shown ? t("piChat.collapseMsg") : t("piChat.expandMsg")}
          aria-label={shown ? t("piChat.collapseMsg") : t("piChat.expandMsg")}
          aria-expanded={shown}
          aria-controls={bodyId}
          onClick={(e) => {
            e.stopPropagation()
            setOpen(!expanded)
          }}
        >
          {shown ? <IconChevronDown /> : <IconChevronRight />}
        </button>
        <span className="chead-icon thinking-icon">
          <IconCpu />
        </span>
        <span className="chead-title thinking-label">
          {streaming && shown ? (
            <span className="thinking-live-label">
              {t("piChat.thinkingNow")}
              <span className="dots" />
            </span>
          ) : shown ? (
            t("piChat.thinking")
          ) : (
            t("piChat.thinkingPreview", { preview })
          )}
        </span>
        <span
          className="chead-copy-slot thinking-copy"
          onClick={(e) => e.stopPropagation()}
        >
          <MessageActionsBar text={thinking || undefined} />
        </span>
      </div>
      {shown && (
        <div className="thinking-body" id={bodyId}>
          {thinking}
        </div>
      )}
    </div>
  )
}
