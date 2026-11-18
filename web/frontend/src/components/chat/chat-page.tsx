import {
  IconAlertTriangle,
  IconArrowDown,
  IconArrowUp,
  IconHistory,
  IconPlus,
} from "@tabler/icons-react"
import { useAtom } from "jotai"
import {
  type ChangeEvent,
  type ClipboardEvent,
  type DragEvent,
  useEffect,
  useRef,
  useState,
} from "react"
import { useTranslation } from "react-i18next"

import { renameSession } from "@/api/sessions"
import { PiMessageList } from "@/chat-pi/pi-message-list"
import {
  ChatComposer,
  type ChatInputDisabledReason,
} from "@/components/chat/chat-composer"
import { ChatEmptyState } from "@/components/chat/chat-empty-state"
import { FilePreviewPanel } from "@/components/chat/file-preview-panel"
import { ModelSelector } from "@/components/chat/model-selector"
import { PromptTemplatesProvider } from "@/components/chat/prompt-templates"
import { SessionHistorySidebar } from "@/components/chat/session-history-sidebar"
import { PageHeader } from "@/components/page-header"
import { Button } from "@/components/ui/button"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { Sheet, SheetContent } from "@/components/ui/sheet"
import {
  CHAT_FILE_ACCEPT,
  buildChatFileAttachments,
  getTransferredFiles,
  hasFileTransfer,
} from "@/features/chat/file-input"
import { useTurnStall } from "@/features/chat/use-turn-stall"
import { useChatModels } from "@/hooks/use-chat-models"
import { useGateway } from "@/hooks/use-gateway"
import { usePicoChat } from "@/hooks/use-pico-chat"
import { useSessionHistory } from "@/hooks/use-session-history"
import { useSessionModel } from "@/hooks/use-session-model"
import { cn } from "@/lib/utils"
import type { AssistantDetailVisibility } from "@/store/chat"
import type { ConnectionState } from "@/store/chat"
import type { ChatAttachment } from "@/store/chat"
import { assistantDetailVisibilityAtom } from "@/store/chat"
import type { GatewayState } from "@/store/gateway"

function resolveChatInputDisabledReason({
  hasDefaultModel,
  connectionState,
  gatewayState,
  activeSessionChannel,
}: {
  hasDefaultModel: boolean
  connectionState: ConnectionState
  gatewayState: GatewayState
  activeSessionChannel?: string
}): ChatInputDisabledReason | null {
  // Sessions from other channels (feishu, telegram, ...) are read-only here:
  // the web chat can only write into pico-scoped sessions, so composing would
  // fork a ghost pico session detached from the real conversation.
  if (activeSessionChannel && activeSessionChannel !== "pico") {
    return "nonPicoSession"
  }

  if (gatewayState === "unknown") {
    return "gatewayUnknown"
  }

  if (gatewayState === "starting") {
    return "gatewayStarting"
  }

  if (gatewayState === "restarting") {
    return "gatewayRestarting"
  }

  if (gatewayState === "stopping") {
    return "gatewayStopping"
  }

  if (gatewayState === "stopped") {
    return "gatewayStopped"
  }

  if (gatewayState === "error") {
    return "gatewayError"
  }

  if (connectionState === "connecting") {
    return "websocketConnecting"
  }

  if (connectionState === "error") {
    return "websocketError"
  }

  if (connectionState === "disconnected") {
    return "websocketDisconnected"
  }

  if (!hasDefaultModel) {
    return "noDefaultModel"
  }

  return null
}

export function ChatPage() {
  const { t } = useTranslation()
  const scrollRef = useRef<HTMLDivElement>(null)
  const fileInputRef = useRef<HTMLInputElement>(null)
  const dragDepthRef = useRef(0)
  const [sessionsSheetOpen, setSessionsSheetOpen] = useState(false)
  const [isAtBottom, setIsAtBottom] = useState(true)
  // 「回到顶部」：距顶 > 10px 时出现（与 isAtBottom 的阈值对称）。
  const [isAtTop, setIsAtTop] = useState(true)
  const [hasScrolled, setHasScrolled] = useState(false)
  const [input, setInput] = useState("")
  const [attachments, setAttachments] = useState<ChatAttachment[]>([])
  const [isDragActive, setIsDragActive] = useState(false)
  const [assistantDetailVisibility, setAssistantDetailVisibility] = useAtom(
    assistantDetailVisibilityAtom,
  )

  const assistantDetailVisibilityOptions: Array<{
    value: AssistantDetailVisibility
    label: string
  }> = [
    { value: "none", label: t("chat.assistantDetailVisibility.none") },
    { value: "thought", label: t("chat.assistantDetailVisibility.thought") },
    {
      value: "tool_calls",
      label: t("chat.assistantDetailVisibility.toolCalls"),
    },
    { value: "all", label: t("chat.assistantDetailVisibility.all") },
  ]

  const {
    messages,
    connectionState,
    isTyping,
    activeSessionId,
    activeSessionChannel,
    contextUsage,
    turnStats,
    sendMessage,
    switchSession,
    newChat,
  } = usePicoChat()

  const { state: gwState } = useGateway()
  const isGatewayRunning = gwState === "running"
  const { stalled: turnStalled, idleMs: turnIdleMs } = useTurnStall()

  const {
    models,
    defaultModelName,
    hasAvailableModels,
    apiKeyModels,
    oauthModels,
    localModels,
  } = useChatModels({ isConnected: isGatewayRunning })
  const hasDefaultModel = Boolean(defaultModelName)

  // 会话级模型（fork）：只对当前会话生效，不改配置文件。空串 = 跟随配置
  // 默认模型；会话内切换模型不会动 config.json 的默认模型。
  const {
    sessionModel,
    sessionContextWindow,
    isSettingSessionModel,
    handleSetSessionModel,
  } = useSessionModel({ activeSessionId, isConnected: isGatewayRunning })

  // 常驻元信息：会话模型 → 末次回合模型 → 末条助手消息 → 配置主模型。
  const model =
    sessionModel ||
    turnStats?.modelName ||
    [...messages].reverse().find((m) => m.modelName)?.modelName ||
    defaultModelName
  // 活动模型的上下文窗口总量：
  //   1. 会话模型已绑定 → 用会话模型的窗口（切换模型后立即生效；实时用量
  //      里还是上一个模型的 total，不能盖住新窗口）；
  //   2. 否则用实时用量的 total（真实窗口）；
  //   3. 最后查模型配置（新会话显示 0 / 总量）。
  const contextWindow = sessionModel
    ? sessionContextWindow || contextUsage?.total_tokens || 0
    : contextUsage?.total_tokens ||
      models.find(
        (m) => m.model_name === model || m.model_name === defaultModelName,
      )?.context_window ||
      0
  const inputDisabledReason = resolveChatInputDisabledReason({
    hasDefaultModel,
    connectionState,
    gatewayState: gwState,
    activeSessionChannel,
  })
  const canInput = inputDisabledReason === null

  const {
    sessions,
    hasMore,
    isLoading,
    isLoadingMore,
    loadError,
    loadErrorMessage,
    loadSessions,
    loadMore,
    refreshSessions,
    handleDeleteSession,
  } = useSessionHistory({
    activeSessionId,
    onDeletedActiveSession: newChat,
  })

  const syncScrollState = (element: HTMLDivElement) => {
    const { clientHeight, scrollHeight, scrollTop } = element
    setHasScrolled(scrollTop > 0)
    setIsAtBottom(scrollHeight - scrollTop <= clientHeight + 10)
    setIsAtTop(scrollTop <= 10)
  }

  const handleScroll = (e: React.UIEvent<HTMLDivElement>) => {
    syncScrollState(e.currentTarget)
  }

  // REQ-018: 点击后平滑滚底即可；落底时 onScroll → syncScrollState 会把
  // isAtBottom 置真，按钮随之隐藏，流式自动跟随恢复（无需在此手动 setState）。
  const scrollToLatest = () => {
    const element = scrollRef.current
    if (!element) return
    element.scrollTo({ top: element.scrollHeight, behavior: "smooth" })
  }

  // 「回到顶部」：与「回到最新」对称的平滑滚顶；滚到顶后 onScroll →
  // syncScrollState 把 isAtTop 置真，按钮随之隐藏。
  const scrollToTop = () => {
    const element = scrollRef.current
    if (!element) return
    element.scrollTo({ top: 0, behavior: "smooth" })
  }

  useEffect(() => {
    if (scrollRef.current) {
      if (isAtBottom) {
        scrollRef.current.scrollTop = scrollRef.current.scrollHeight
      }
      syncScrollState(scrollRef.current)
    }
  }, [messages, isTyping, isAtBottom])

  const handleSend = () => {
    if ((!input.trim() && attachments.length === 0) || !canInput) return
    if (
      sendMessage({
        content: input,
        attachments,
      })
    ) {
      setInput("")
      setAttachments([])
    }
  }

  const handleAddImages = () => {
    if (!canInput) return
    fileInputRef.current?.click()
  }

  const handleRemoveAttachment = (index: number) => {
    setAttachments((prev) => prev.filter((_, itemIndex) => itemIndex !== index))
  }

  const appendImageFiles = async (files: readonly File[]) => {
    if (!canInput || files.length === 0) {
      return
    }

    const nextAttachments = await buildChatFileAttachments(files, t)
    if (nextAttachments.length === 0) {
      return
    }

    setAttachments((prev) => [...prev, ...nextAttachments])
  }

  const handleImageSelection = async (event: ChangeEvent<HTMLInputElement>) => {
    const files = Array.from(event.target.files ?? [])
    event.target.value = ""

    if (files.length === 0) {
      return
    }

    await appendImageFiles(files)
  }

  const resetDragState = () => {
    dragDepthRef.current = 0
    setIsDragActive(false)
  }

  const handleComposerPaste = async (
    event: ClipboardEvent<HTMLTextAreaElement>,
  ) => {
    const files = getTransferredFiles(event.clipboardData)
    if (files.length === 0) {
      return
    }

    await appendImageFiles(files)
  }

  const handleComposerDragEnter = (event: DragEvent<HTMLDivElement>) => {
    if (!hasFileTransfer(event.dataTransfer)) {
      return
    }

    event.preventDefault()
    if (!canInput) {
      return
    }
    dragDepthRef.current += 1
    setIsDragActive(true)
  }

  const handleComposerDragLeave = (event: DragEvent<HTMLDivElement>) => {
    if (!hasFileTransfer(event.dataTransfer)) {
      return
    }

    event.preventDefault()
    if (!canInput) {
      resetDragState()
      return
    }
    dragDepthRef.current = Math.max(0, dragDepthRef.current - 1)
    if (dragDepthRef.current === 0) {
      setIsDragActive(false)
    }
  }

  const handleComposerDragOver = (event: DragEvent<HTMLDivElement>) => {
    if (!hasFileTransfer(event.dataTransfer)) {
      return
    }

    event.preventDefault()
    event.dataTransfer.dropEffect = canInput ? "copy" : "none"
  }

  const handleComposerDrop = async (event: DragEvent<HTMLDivElement>) => {
    if (!hasFileTransfer(event.dataTransfer)) {
      return
    }

    event.preventDefault()
    const files = getTransferredFiles(event.dataTransfer)
    resetDragState()

    if (!canInput || files.length === 0) {
      return
    }

    await appendImageFiles(files)
  }

  const canSubmit =
    canInput && (Boolean(input.trim()) || attachments.length > 0)

  const handleTemplateFill = (text: string) => {
    if (!canInput) return
    setInput(text)
  }

  const handleTemplateSend = (text: string) => {
    if (!canInput) return
    if (sendMessage({ content: text, attachments: [] })) {
      setInput("")
    }
  }

  const sessionSidebar = (
    <SessionHistorySidebar
      sessions={sessions}
      activeSessionId={activeSessionId}
      activeSessionBusy={isTyping}
      hasMore={hasMore}
      isLoading={isLoading}
      isLoadingMore={isLoadingMore}
      loadError={loadError}
      loadErrorMessage={loadErrorMessage}
      onLoadMore={loadMore}
      onSwitchSession={switchSession}
      onDeleteSession={handleDeleteSession}
      onRenameSession={async (sessionId, title) => {
        try {
          const channel = sessions.find((s) => s.id === sessionId)?.channel
          await renameSession(sessionId, title, channel)
          void loadSessions(true)
        } catch (err) {
          console.error("rename session failed", err)
        }
      }}
      onRefresh={() => void loadSessions(true)}
      onAutoRefresh={() => void refreshSessions()}
    />
  )

  return (
    <PromptTemplatesProvider
      onFill={handleTemplateFill}
      onSend={handleTemplateSend}
      canSend={canInput}
    >
      <div className="bg-background/95 flex h-full">
        {/* M-3: wide desktop keeps the inline sidebar; below lg only the
          drawer is available (the trigger button below is lg:hidden). The
          inline column costs 320px, which on tablet-width viewports left
          the conversation column unusably narrow — so the inline sidebar
          now starts at lg, and the drawer covers everything narrower. */}
        <div className="hidden h-full lg:flex">{sessionSidebar}</div>

        <Sheet open={sessionsSheetOpen} onOpenChange={setSessionsSheetOpen}>
          <SheetContent side="left" className="w-80 p-0">
            <div
              className="h-full"
              onClick={(event) => {
                // Switching sessions from the drawer should close it; delete /
                // rename clicks bubble too but their own stopPropagation wins.
                const target = event.target as HTMLElement
                if (target.closest("[data-session-item]")) {
                  setSessionsSheetOpen(false)
                }
              }}
            >
              {sessionSidebar}
            </div>
          </SheetContent>
        </Sheet>

        <div className="flex min-w-0 flex-1">
          <div className="flex min-w-0 flex-1 flex-col">
            <PageHeader
              title={t("navigation.chat")}
              className={`transition-shadow ${
                hasScrolled ? "shadow-xs" : "shadow-none"
              }`}
              titleExtra={
                hasAvailableModels && (
                  <ModelSelector
                    defaultModelName={defaultModelName}
                    sessionModel={sessionModel}
                    apiKeyModels={apiKeyModels}
                    oauthModels={oauthModels}
                    localModels={localModels}
                    disabled={isSettingSessionModel}
                    onValueChange={handleSetSessionModel}
                  />
                )
              }
            >
              {/* Radix SheetTrigger 必须位于 <Sheet> 上下文内；此按钮在 PageHeader
            下、Sheet 树外，且 Sheet 已是受控模式——直接 onClick 打开即可。 */}
              <Button
                variant="ghost"
                size="icon"
                className="lg:hidden"
                aria-label={t("chat.sessionHistory")}
                onClick={() => setSessionsSheetOpen(true)}
              >
                <IconHistory className="size-5" />
              </Button>
              <div className="border-border/60 hidden items-center gap-2 rounded-lg border px-3 py-1.5 sm:flex">
                <span className="text-muted-foreground text-sm">
                  {t("chat.showAssistantDetails")}
                </span>
                <Select
                  value={assistantDetailVisibility}
                  onValueChange={(value) =>
                    setAssistantDetailVisibility(
                      value as AssistantDetailVisibility,
                    )
                  }
                >
                  <SelectTrigger
                    size="sm"
                    aria-label={t("chat.showAssistantDetails")}
                    className="text-muted-foreground hover:text-foreground focus-visible:border-input h-8 min-w-[104px] bg-transparent shadow-none focus-visible:ring-0"
                  >
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent align="end">
                    {assistantDetailVisibilityOptions.map((option) => (
                      <SelectItem key={option.value} value={option.value}>
                        {option.label}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </div>

              <Button
                variant="secondary"
                size="sm"
                onClick={newChat}
                className="h-9 gap-2"
              >
                <IconPlus className="size-4" />
                <span className="hidden sm:inline">{t("chat.newChat")}</span>
              </Button>
            </PageHeader>

            {/* REQ-018: relative 包裹层让「回到最新」按钮锚定在消息区底部，
            不随内容滚动；内部滚动容器几何不变。 */}
            <div className="relative min-h-0 flex-1">
              <div
                ref={scrollRef}
                onScroll={handleScroll}
                className="h-full [scrollbar-gutter:stable] overflow-y-auto px-3 py-6 sm:px-4 md:px-6 lg:px-10 xl:px-16 2xl:px-24"
              >
                <div className="pi-chat flex w-full flex-col pb-8">
                  {messages.length === 0 && !isTyping && (
                    <ChatEmptyState
                      hasAvailableModels={hasAvailableModels}
                      defaultModelName={defaultModelName}
                      isConnected={isGatewayRunning}
                    />
                  )}

                  <PiMessageList
                    messages={messages}
                    isTyping={isTyping}
                    detail={assistantDetailVisibility}
                  />

                  {isTyping && turnStalled && (
                    <div
                      role="status"
                      className="mt-3 flex items-start gap-2 rounded-lg border border-amber-500/40 bg-amber-500/10 px-3 py-2 text-sm text-amber-600 dark:text-amber-400"
                    >
                      <IconAlertTriangle className="mt-0.5 size-4 shrink-0" />
                      <span>
                        {t("chat.turnStalled", {
                          minutes: Math.max(
                            1,
                            Math.floor((turnIdleMs ?? 0) / 60_000),
                          ),
                        })}
                      </span>
                    </div>
                  )}
                </div>
              </div>

              {/* REQ-018: 滚离底部（isAtBottom=false）时浮现「回到最新」；
              宽度/内边距与消息列、输入卡同列，右缘对齐。
              「回到顶部」与之对称：滚离顶部（isAtTop=false）时浮现于其上方。 */}
              <div className="pointer-events-none absolute inset-x-0 bottom-8 z-10 px-3 sm:px-4 md:px-6 lg:px-10 xl:px-16 2xl:px-24">
                <div className="flex w-full flex-col items-end gap-2">
                  <button
                    type="button"
                    onClick={scrollToTop}
                    aria-hidden={isAtTop}
                    tabIndex={isAtTop ? -1 : 0}
                    className={cn(
                      "border-border/60 bg-card/95 text-muted-foreground hover:text-foreground inline-flex items-center gap-1.5 rounded-full border px-3 py-1.5 text-xs font-medium shadow-md backdrop-blur transition-all duration-200",
                      isAtTop
                        ? "pointer-events-none translate-y-1 opacity-0"
                        : "pointer-events-auto translate-y-0 opacity-100",
                    )}
                  >
                    <IconArrowUp className="size-3.5" />
                    {t("piChat.backToTop")}
                  </button>
                  <button
                    type="button"
                    onClick={scrollToLatest}
                    aria-hidden={isAtBottom}
                    tabIndex={isAtBottom ? -1 : 0}
                    className={cn(
                      "border-border/60 bg-card/95 text-muted-foreground hover:text-foreground inline-flex items-center gap-1.5 rounded-full border px-3 py-1.5 text-xs font-medium shadow-md backdrop-blur transition-all duration-200",
                      isAtBottom
                        ? "pointer-events-none translate-y-1 opacity-0"
                        : "pointer-events-auto translate-y-0 opacity-100",
                    )}
                  >
                    <IconArrowDown className="size-3.5" />
                    {t("piChat.backToLatest")}
                  </button>
                </div>
              </div>
            </div>

            <input
              ref={fileInputRef}
              type="file"
              accept={CHAT_FILE_ACCEPT}
              multiple
              className="hidden"
              onChange={handleImageSelection}
            />

            <ChatComposer
              input={input}
              attachments={attachments}
              onInputChange={setInput}
              onAddImages={handleAddImages}
              onPaste={handleComposerPaste}
              onDragEnter={handleComposerDragEnter}
              onDragLeave={handleComposerDragLeave}
              onDragOver={handleComposerDragOver}
              onDrop={handleComposerDrop}
              onRemoveAttachment={handleRemoveAttachment}
              onSend={handleSend}
              isTyping={isTyping}
              onStop={() => {
                sendMessage({ content: "/stop", attachments: [] })
              }}
              onContextDetail={() => {
                if (sendMessage({ content: "/context", attachments: [] })) {
                  setInput("")
                }
              }}
              inputDisabledReason={inputDisabledReason}
              canSend={canSubmit}
              isDragActive={isDragActive}
              contextUsage={contextUsage}
              modelName={model}
              defaultModelName={defaultModelName}
              sessionModel={sessionModel}
              contextWindow={contextWindow}
              inputTokens={turnStats?.inputTokens}
            />
          </div>
          <FilePreviewPanel />
        </div>
      </div>
    </PromptTemplatesProvider>
  )
}
