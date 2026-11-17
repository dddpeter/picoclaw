// [REQ-020] MessageActionsBar 骨架：对齐 LobeHub 消息 hover 底部操作条。
// 本刀只交付骨架（复制 + 重新生成 + 更多），挂载点与动作接线在后续刀补齐；
// 图标统一 @tabler/icons-react（REQ-010），文案全部走 i18n（REQ-013 纪律）。
import {
  IconCircleCheck,
  IconCopy,
  IconDots,
  IconRefresh,
} from "@tabler/icons-react"
import { memo } from "react"
import { useTranslation } from "react-i18next"
import { toast } from "sonner"

import { useCopyToClipboard } from "@/hooks/use-copy-to-clipboard"

interface MessageActionsBarProps {
  /** 复制的原文（undefined 时隐藏复制键）。 */
  text?: string
  /** 重新生成回调（undefined 时隐藏按钮）。 */
  onRegenerate?: () => void
  /** 更多操作（占位，后续刀扩展菜单）。 */
  onMore?: () => void
}

/** 单个操作键：ghost 28px，与 chead-copy 同一密度家族。 */
function ActionButton({
  label,
  onClick,
  children,
}: {
  label: string
  onClick?: () => void
  children: React.ReactNode
}) {
  return (
    <button
      type="button"
      className="msg-actions-btn"
      title={label}
      aria-label={label}
      onClick={onClick}
    >
      {children}
    </button>
  )
}

export const MessageActionsBar = memo(function MessageActionsBar({
  text,
  onRegenerate,
  onMore,
}: MessageActionsBarProps) {
  const { t } = useTranslation()
  const { copy, isCopied: copied } = useCopyToClipboard()
  const hasAny =
    text !== undefined || onRegenerate !== undefined || onMore !== undefined
  if (!hasAny) return null
  return (
    <div
      className="msg-actions-bar"
      role="toolbar"
      aria-label={t("piChat.actionsBar")}
    >
      {text !== undefined && (
        <ActionButton
          label={copied ? t("piChat.copied") : t("piChat.copy")}
          onClick={() => {
            // [REQ-019] 统一 useCopyToClipboard 的 2000ms 恢复节律；失败态保持可重试。
            void copy(text).then((ok) => {
              if (!ok) toast.error(t("piChat.copyFailed"))
            })
          }}
        >
          {copied ? <IconCircleCheck /> : <IconCopy />}
        </ActionButton>
      )}
      {onRegenerate && (
        <ActionButton label={t("piChat.regenerate")} onClick={onRegenerate}>
          <IconRefresh />
        </ActionButton>
      )}
      {onMore && (
        <ActionButton label={t("piChat.moreActions")} onClick={onMore}>
          <IconDots />
        </ActionButton>
      )}
    </div>
  )
})
