import {
  IconArrowUp,
  IconCpu,
  IconFile,
  IconPhotoPlus,
  IconSquare,
  IconX,
} from "@tabler/icons-react"
import {
  type ClipboardEvent as ReactClipboardEvent,
  type DragEvent as ReactDragEvent,
  type KeyboardEvent as ReactKeyboardEvent,
  memo,
  useRef,
} from "react"
import { useTranslation } from "react-i18next"
import TextareaAutosize from "react-textarea-autosize"

import { ContextUsageRing } from "@/components/chat/context-usage-ring"
import { TemplatePickerButton } from "@/components/chat/prompt-templates"
import { TurnStatusBar } from "@/components/chat/turn-status-bar"
import { Button } from "@/components/ui/button"
import { cn } from "@/lib/utils"
import type { ChatAttachment, ContextUsage } from "@/store/chat"
import { openFilePreview } from "@/store/file-preview"

export type ChatInputDisabledReason =
  | "nonPicoSession"
  | "gatewayUnknown"
  | "gatewayStarting"
  | "gatewayRestarting"
  | "gatewayStopping"
  | "gatewayStopped"
  | "gatewayError"
  | "websocketConnecting"
  | "websocketDisconnected"
  | "websocketError"
  | "noDefaultModel"

interface ChatComposerProps {
  input: string
  attachments: ChatAttachment[]
  onInputChange: (value: string) => void
  onAddImages: () => void
  onPaste: (event: ReactClipboardEvent<HTMLTextAreaElement>) => void
  onDragEnter: (event: ReactDragEvent<HTMLDivElement>) => void
  onDragLeave: (event: ReactDragEvent<HTMLDivElement>) => void
  onDragOver: (event: ReactDragEvent<HTMLDivElement>) => void
  onDrop: (event: ReactDragEvent<HTMLDivElement>) => void
  onRemoveAttachment: (index: number) => void
  onSend: () => void
  onStop?: () => void
  isTyping?: boolean
  onContextDetail?: () => void
  inputDisabledReason: ChatInputDisabledReason | null
  canSend: boolean
  isDragActive: boolean
  contextUsage?: ContextUsage
  /** 末次回合/消息解析出的当前模型；新会话回退 defaultModelName。 */
  modelName?: string
  defaultModelName?: string
  /** 会话级模型（fork）：已绑定时 title 说明仅对本会话生效。 */
  sessionModel?: string
  /** 活动模型上下文窗口总量（配置），实时用量缺失时用于显示总量。 */
  contextWindow?: number
  /** 末次 LLM 调用输入 token——上下文环与 TurnStatusBar 同口径。 */
  inputTokens?: number
}

export const ChatComposer = memo(function ChatComposer({
  input,
  attachments,
  onInputChange,
  onAddImages,
  onPaste,
  onDragEnter,
  onDragLeave,
  onDragOver,
  onDrop,
  onRemoveAttachment,
  onSend,
  onStop,
  isTyping,
  onContextDetail,
  inputDisabledReason,
  canSend,
  isDragActive,
  contextUsage,
  modelName,
  defaultModelName,
  sessionModel,
  contextWindow,
  inputTokens,
}: ChatComposerProps) {
  const { t } = useTranslation()
  const canInput = inputDisabledReason === null
  const composingRef = useRef(false)
  const hasInput = input.trim().length > 0
  const disabledMessage =
    inputDisabledReason === null
      ? null
      : t(`chat.disabledPlaceholder.${inputDisabledReason}`)
  const placeholder = disabledMessage ?? t("chat.placeholder")

  const handleKeyDown = (e: ReactKeyboardEvent<HTMLTextAreaElement>) => {
    const nativeEvent = e.nativeEvent as Event & {
      isComposing?: boolean
      keyCode?: number
    }
    if (
      composingRef.current ||
      nativeEvent.isComposing ||
      nativeEvent.keyCode === 229
    ) {
      return
    }
    if (e.key === "Enter" && !e.shiftKey) {
      e.preventDefault()
      onSend()
    }
  }

  return (
    <div className="before:bg-background pointer-events-none relative z-10 -mt-[24px] shrink-0 [scrollbar-gutter:stable] overflow-y-auto px-3 pb-[env(safe-area-inset-bottom)] before:pointer-events-none before:absolute before:inset-x-0 before:top-[24px] before:bottom-0 before:content-[''] sm:px-4 md:px-6 md:pb-0 lg:px-10 xl:px-16 2xl:px-24">
      <div className="pointer-events-auto flex w-full flex-col items-end">
        <TurnStatusBar />
        <div
          className={cn(
            "bg-card border-border/60 relative flex w-full flex-col gap-2 rounded-2xl border px-3 pt-3 pb-4 shadow-sm transition-colors",
            isDragActive && "border-orange-400/70 bg-orange-500/5",
          )}
          onDragEnter={onDragEnter}
          onDragLeave={onDragLeave}
          onDragOver={onDragOver}
          onDrop={onDrop}
        >
          {isDragActive && (
            <div className="pointer-events-none absolute inset-0 z-10 flex items-center justify-center rounded-2xl border-2 border-dashed border-orange-400/70 bg-orange-500/10">
              <div className="bg-background/95 text-foreground rounded-full px-4 py-2 text-sm font-medium shadow-sm">
                {t("chat.dropImagesActive")}
              </div>
            </div>
          )}

          {attachments.length > 0 && (
            <div className="flex flex-wrap gap-2 px-2">
              {attachments.map((attachment, index) => (
                <div
                  key={`${attachment.path ?? attachment.url}-${index}`}
                  className="bg-background relative overflow-hidden rounded-xl border"
                >
                  {attachment.url ? (
                    <div className="h-20 w-20">
                      <img
                        src={attachment.url}
                        alt={attachment.filename || t("chat.uploadedImage")}
                        className="h-full w-full object-cover"
                      />
                    </div>
                  ) : (
                    <button
                      type="button"
                      className="text-muted-foreground hover:bg-accent/50 flex h-14 max-w-56 items-center gap-2 px-3 text-left transition"
                      onClick={() => {
                        if (attachment.path) {
                          openFilePreview(
                            attachment.path,
                            attachment.filename ?? undefined,
                          )
                        }
                      }}
                      disabled={!attachment.path}
                      title={
                        attachment.path
                          ? t("filePreview.openPreview")
                          : (attachment.filename ?? "")
                      }
                    >
                      <IconFile className="size-4 shrink-0" />
                      <span
                        className="truncate text-sm"
                        title={attachment.filename || attachment.path}
                      >
                        {attachment.filename || attachment.path}
                      </span>
                    </button>
                  )}
                  <button
                    type="button"
                    onClick={() => onRemoveAttachment(index)}
                    className="bg-background/85 text-foreground absolute top-1 right-1 inline-flex h-6 w-6 items-center justify-center rounded-full border shadow-sm transition hover:bg-white"
                    aria-label={t("chat.removeImage")}
                    title={t("chat.removeImage")}
                  >
                    <IconX className="h-3.5 w-3.5" />
                  </button>
                </div>
              ))}
            </div>
          )}

          <TextareaAutosize
            value={input}
            onChange={(e) => onInputChange(e.target.value)}
            onCompositionStart={() => {
              composingRef.current = true
            }}
            onCompositionEnd={() => {
              composingRef.current = false
            }}
            onPaste={onPaste}
            onKeyDown={handleKeyDown}
            placeholder={placeholder}
            disabled={!canInput}
            title={disabledMessage || undefined}
            className={cn(
              "placeholder:text-muted-foreground/50 max-h-[200px] min-h-[64px] resize-none border-0 bg-transparent px-2 py-1 text-[15px] leading-[1.5] shadow-none transition-colors focus-visible:ring-0 focus-visible:outline-none dark:bg-transparent",
              !canInput && "cursor-not-allowed",
            )}
            minRows={1}
            maxRows={8}
          />

          <div className="flex items-end justify-between px-1">
            <div className="flex items-center gap-1">
              <Button
                type="button"
                variant="ghost"
                size="icon"
                className="text-muted-foreground hover:text-foreground h-8 w-8 rounded-full"
                onClick={onAddImages}
                disabled={!canInput}
                aria-label={t("chat.attachImage")}
                title={t("chat.attachImage")}
              >
                <IconPhotoPlus className="size-4" />
              </Button>
              <TemplatePickerButton className="text-muted-foreground hover:text-foreground h-8 w-8 rounded-full" />
              {/* 常驻元信息：当前模型 + 上下文（紧随模板图标，参考 pi 状态栏）。
                  与左侧图标按钮统一：size-4 图标 + text-muted-foreground。
                  上下文 chip 常驻：新会话无用量时显示 0（总量取模型配置窗口）。
                  会话级模型生效时，title 说明「仅对本会话生效」。 */}
              <span
                className="text-muted-foreground hidden items-center gap-1.5 text-xs sm:flex"
                title={
                  sessionModel
                    ? `${t("chat.statusModel")} · ${t("chat.sessionModelTitle")}`
                    : t("chat.statusModel")
                }
              >
                <IconCpu className="size-4" />
                <span className="max-w-40 truncate">
                  {modelName ||
                    defaultModelName ||
                    t("chat.statusModelUnknown")}
                </span>
              </span>
            </div>

            <div className="flex items-center gap-1.5">
              {/* 常驻：无实时用量时合成零用量（总量取活动模型配置窗口），
                  保证上下文环永远可见。 */}
              <ContextUsageRing
                usage={
                  contextUsage ?? {
                    used_tokens: 0,
                    total_tokens: contextWindow ?? 0,
                    compress_at_tokens: contextWindow ?? 0,
                    used_percent: 0,
                  }
                }
                inputTokens={inputTokens}
                onDetailClick={onContextDetail}
              />
              {canInput && isTyping && onStop ? (
                <Button
                  type="button"
                  size="icon"
                  className="bg-destructive hover:bg-destructive/90 size-8 rounded-full text-white transition-transform active:scale-95"
                  onClick={onStop}
                  aria-label={t("chat.stopGeneration")}
                >
                  <IconSquare className="size-3" fill="currentColor" />
                </Button>
              ) : canInput ? (
                <span tabIndex={!canSend ? 0 : undefined}>
                  <Button
                    type="button"
                    size="icon"
                    className="size-8 rounded-full bg-orange-500 text-white transition-transform hover:bg-orange-600 active:scale-95"
                    onClick={onSend}
                    disabled={!canSend}
                    aria-label={t("chat.sendMessage")}
                  >
                    <IconArrowUp className="size-4" />
                  </Button>
                </span>
              ) : null}
            </div>
          </div>
        </div>

        <div
          aria-hidden={!hasInput}
          className={cn(
            "border-border/50 bg-muted/55 text-muted-foreground dark:bg-muted/45 mt-2 inline-flex items-center rounded-md border px-3 py-1 text-[11px] shadow-sm transition-all duration-200",
            hasInput
              ? "translate-y-0 opacity-100"
              : "pointer-events-none -translate-y-1 opacity-0",
          )}
        >
          {t("chat.composeHint")}
        </div>
      </div>
    </div>
  )
})
