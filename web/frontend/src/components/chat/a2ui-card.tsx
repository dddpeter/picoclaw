import {
  A2uiSurface,
  MarkdownComponentContext,
  type ReactCatalogComponent,
} from "@a2ui/react/v0_9"
import {
  type ActionPayload,
  MessageProcessor,
  type ProcessableMessage,
} from "@a2ui/web_core/v0_9"
import {
  basicCatalog,
  injectBasicCatalogStyles,
} from "@a2ui/web_core/v0_9/basic_catalog"
import { registerUniversalElement } from "@a2ui/web_core/v0_9/universal"
import { IconLayoutGrid } from "@tabler/icons-react"
import { Component, type ReactNode, useEffect, useMemo } from "react"
import { useTranslation } from "react-i18next"

import { Markdown } from "@/chat-pi/markdown"
import { sendChatMessage } from "@/features/chat/controller"
import type { A2uiInfo } from "@/store/chat"

import { catalogWithVegaChart } from "./a2ui-vega"

/**
 * A2UI 渲染卡（D5 设计）：聊天流内渲染 agent 经 render_ui 工具产出的
 * A2UI v0.9 surface。前序链路：payload["a2ui"] → protocol.ts →
 * ChatMessage.ui → turns.ts 的 a2ui block → pi-message.tsx 接线到本组件。
 *
 * - 每卡独立 MessageProcessor（天然隔离 surfaceId 撞车 / 一 turn 多次
 *   render_ui），组件卸载 dispose。
 * - processMessages 自带 zod 校验（v0.9 封包是 strict 的）：畸形消息在
 *   校验层抛错 → 就地降级为错误占位文案，绝不炸聊天流；渲染期异常由
 *   error boundary 兜底。
 * - 交互回传：卡片按钮/表单动作经 MessageProcessor 第二参 actionHandler
 *   组装成 `[A2UI 交互]` 前缀的用户消息，走既有聊天发送路径（ask-card
 *   同款 sendChatMessage）上行；turn 活跃时由既有 steering 队列承接。
 *   VegaChart（vega-lite 图表，二期任务 6）在 catalogWithVegaChart（
 *   basicCatalog 经 Catalog.derive 混入，catalogId 不变）中注册。
 */

/**
 * 动作 → 用户消息（A2UI 二期任务 1 契约，逐字）：`[A2UI 交互]
 * surface="<surfaceId>" action="<name>" source="<componentId>"`，context
 * 非空时追加 ` context=<紧凑 JSON>`（event.context 的 {path} 绑定在点击
 * 瞬间由 generic-binder 从 dataModel 收集的表单值）。
 *
 * 前缀是给 agent 解析的协议文本（render_ui description 教了同一形状），
 * 五语言一致、刻意不做 i18n 键——本地化会破坏逐字契约。
 */
function buildActionMessage(action: ActionPayload): string {
  let message = `[A2UI 交互] surface="${action.surfaceId}" action="${action.name}" source="${action.sourceComponentId}"`
  if (action.context && Object.keys(action.context).length > 0) {
    message += ` context=${JSON.stringify(action.context)}`
  }
  return message
}

/**
 * 交互回传处理器（MessageProcessor 构造第二参，全局兜底覆盖所有
 * surface）：动作组装成用户消息后走既有聊天发送路径上行（ask-card
 * 同一条 sendChatMessage——普通 message.send，无专用 WS 消息类型）。
 * 发送失败（WS 未连接）静默忽略：整条聊天链路不可用时无处可报，
 * sendChatMessage 内部已告警。
 */
function handleA2uiAction(action: ActionPayload): void {
  sendChatMessage({ content: buildActionMessage(action), attachments: [] })
}

/** 入口一次性初始化（幂等）：注册 basicCatalog 的 universal（Lit Web
 * Component）custom elements 并注入全局样式。漏注册只会渲染空标签；
 * registerUniversalElement 对同名同构造器重复调用 no-op，
 * injectBasicCatalogStyles 自带 includes 守卫。 */
export function initA2uiBasicCatalog(): void {
  for (const component of basicCatalog.components.values()) {
    registerUniversalElement(component)
  }
  injectBasicCatalogStyles()
}

/** basicCatalog Text 的 markdown 走 app 同一管线（@a2ui/react README
 * 推荐的 MarkdownComponentContext；不提供时 Text 退化为原文 span）。 */
const A2uiMarkdown = ({ markdown }: { markdown: string }) => (
  <Markdown text={markdown} />
)

/** 错误占位：zod 校验失败或渲染期异常的统一降级文案。 */
function A2uiErrorPlaceholder() {
  const { t } = useTranslation()
  return (
    <div className="border-border/60 bg-muted/30 text-muted-foreground my-2 flex w-full items-center gap-2 rounded-xl border px-3 py-2 text-[13px]">
      <IconLayoutGrid className="size-4 shrink-0" />
      {t("chat.a2uiRenderFailed")}
    </div>
  )
}

/** 渲染期异常兜底：A2uiSurface 内部崩溃时降级为占位，不冒泡炸聊天流。 */
class A2uiRenderBoundary extends Component<
  { fallback: ReactNode; children: ReactNode },
  { failed: boolean }
> {
  state = { failed: false }
  static getDerivedStateFromError() {
    return { failed: true }
  }
  render() {
    return this.state.failed ? this.props.fallback : this.props.children
  }
}

export function A2uiCard({ ui }: { ui: A2uiInfo }) {
  // processMessages 同步消费消息并产出 surface 模型；畸形消息（strict
  // zod）在此抛错 → 转错误占位。dispose 只清 RPC（实测
  // message-processor.dispose() 仅调 rpc.dispose()），已取出的 surface
  // 模型不受影响，因此卸载时 dispose 与 StrictMode 重挂载安全。
  const { surfaces, processor, failed } = useMemo(() => {
    const p = new MessageProcessor<ReactCatalogComponent>(
      [catalogWithVegaChart],
      handleA2uiAction,
      { version: "v0.9" },
    )
    try {
      // messages 是 wire 数据（unknown[]）：真正的把关是 processor 内的
      // strict zod 校验，此断言只为通过编译器，不改变运行时行为。
      p.processMessages(ui.messages as ProcessableMessage[])
      return {
        surfaces: Array.from(p.getSurfaces().values()),
        processor: p,
        failed: false,
      }
    } catch {
      p.dispose()
      return { surfaces: [], processor: undefined, failed: true }
    }
  }, [ui.messages])

  // 卸载（或 processor 因 messages 变化被替换）时释放。
  useEffect(() => () => processor?.dispose(), [processor])

  if (failed) return <A2uiErrorPlaceholder />
  if (surfaces.length === 0) return null

  return (
    <A2uiRenderBoundary fallback={<A2uiErrorPlaceholder />}>
      <MarkdownComponentContext.Provider value={A2uiMarkdown}>
        {/* 容器自适应聊天流宽度（无 max-w 上限，与相邻消息等宽）；a2ui-surface
            class 在 pi-chat.css 把 basicCatalog 的 --a2ui-color-* 变量映射到
            app 主题 token——内层元素不再用注入默认的灰黑/蓝配色。头部标题
            行（图标+"交互视图"）已移除：卡片自解释，用户反馈为噪音。 */}
        <div className="a2ui-surface border-border/60 bg-card my-2 w-full overflow-hidden rounded-xl border shadow-sm">
          <div className="px-3.5 py-3">
            {surfaces.map((surface) => (
              <A2uiSurface key={surface.id} surface={surface} />
            ))}
          </div>
        </div>
      </MarkdownComponentContext.Provider>
    </A2uiRenderBoundary>
  )
}
