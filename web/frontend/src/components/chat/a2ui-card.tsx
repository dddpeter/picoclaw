import {
  A2uiSurface,
  MarkdownComponentContext,
  type ReactCatalogComponent,
} from "@a2ui/react/v0_9"
import {
  type ActionPayload,
  MessageProcessor,
  type ProcessableMessage,
  type SurfaceModel,
} from "@a2ui/web_core/v0_9"
import {
  basicCatalog,
  injectBasicCatalogStyles,
} from "@a2ui/web_core/v0_9/basic_catalog"
import { registerUniversalElement } from "@a2ui/web_core/v0_9/universal"
import { IconAlertTriangle, IconLayoutGrid } from "@tabler/icons-react"
import {
  Component,
  type ReactNode,
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
} from "react"
import { useTranslation } from "react-i18next"

import { Markdown } from "@/chat-pi/markdown"
import { sendChatMessage } from "@/features/chat/controller"
import type { A2uiInfo } from "@/store/chat"

import { catalogWithVegaChart } from "./a2ui-vega"

// basicCatalog Icon 用 'Material Icons' 字体 ligature 渲染图标名（无字体时
// 退化成原文，2026-11-14 用户截图实证）——本地打包字体，不依赖 Google CDN。
// 只在此模块导入：字体随聊天路由 chunk 加载，主 bundle 不背。
import "material-icons/iconfont/material-icons.css"

/**
 * A2UI 渲染卡（D5 设计）：聊天流内渲染 agent 经 render_ui 工具产出的
 * A2UI v0.9 surface。前序链路：payload["a2ui"] → protocol.ts →
 * ChatMessage.ui → turns.ts 的 a2ui block → pi-message.tsx 接线到本组件。
 *
 * - 每卡独立常驻 MessageProcessor（useRef，天然隔离 surfaceId 撞车 /
 *   一 turn 多次 render_ui）。pico 合并广播的 payload 是累计批（新帧
 *   append 尾部 / 同 path updateDataModel 原地覆盖）：ui.messages 变化
 *   时增量喂帧（processor 常驻 → Lit 元素复用，未提交表单输入不丢），
 *   重 create 接管 / surfaceId 变化 / 批次回缩才全量重建；增量抛错 →
 *   warn 一次 + 全量重建一次兜底。组件卸载 dispose 旧 processor。
 * - processMessages 自带 zod 校验（v0.9 封包是 strict 的）：畸形消息在
 *   校验层抛错 → 就地降级为错误占位文案，绝不炸聊天流；渲染期异常由
 *   error boundary 兜底。
 * - 交互回传：卡片按钮/表单动作经 MessageProcessor 第二参 actionHandler
 *   组装成 `[A2UI 交互]` 前缀的用户消息，走既有聊天发送路径（ask-card
 *   同款 sendChatMessage）上行；turn 活跃时由既有 steering 队列承接。
 *   失败有反馈（三期任务 2）：发送失败（WS 未连接）→ 卡内瞬时提示 +
 *   冷却禁用；非最新卡（buildTurns 只标最近一张 a2ui 块 latest）的动作
 *   被拦截并提示用最新卡（防错位——回传会带旧 surfaceId/旧 dataModel
 *   值）。VegaChart（vega-lite 图表，二期任务 6）在 catalogWithVegaChart
 *   （basicCatalog 经 Catalog.derive 混入，catalogId 不变）中注册。
 */

/**
 * 动作 → 用户消息（A2UI 二期任务 1 契约，逐字）：`[A2UI 交互]
 * surface="<surfaceId>" action="<name>" source="<componentId>"`，context
 * 非空时追加 ` context=<紧凑 JSON>`（event.context 的 {path} 绑定在点击
 * 瞬间由 generic-binder 从 dataModel 收集的表单值）。
 *
 * 前缀是给 agent 解析的协议文本（render_ui description 教了同一形状），
 * 五语言一致、刻意不做 i18n 键——本地化会破坏逐字契约。
 *
 * 三个插值字段是 agent 侧可控字符串（三期任务 2 顺手项）：裸拼时引号会
 * 提前闭合字段、换行直接破坏单行性——经 JSON.stringify 转义成单行 JSON
 * 字符串字面量（正常 id 与旧格式逐字节一致，异常字符成为 \" \n 转义
 * 序列，agent 按 JSON 反转义还原）。导出供单测驱动。
 */
export function buildActionMessage(action: ActionPayload): string {
  let message = `[A2UI 交互] surface=${JSON.stringify(action.surfaceId)} action=${JSON.stringify(action.name)} source=${JSON.stringify(action.sourceComponentId)}`
  if (action.context && Object.keys(action.context).length > 0) {
    message += ` context=${JSON.stringify(action.context)}`
  }
  return message
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

/** 瞬时提示条（三期任务 2）：sendFailed = 动作回传发送失败（WS 未连接，
 * 冷却窗口内同时有半透明遮罩表达"瞬时禁用"）；stale = 非最新卡的动作被
 * 拦截（防错位，提示用最新卡）。展示 NOTICE_WINDOW_MS 后自动消失，
 * 成功路径零噪音。z-20 压在遮罩上保证可读。 */
function A2uiNoticeBar({ notice }: { notice: A2uiNotice }) {
  const { t } = useTranslation()
  return (
    <div
      role="status"
      className={
        notice === "sendFailed"
          ? "border-destructive/40 bg-destructive/10 text-destructive relative z-20 mb-2 flex w-full items-center gap-2 rounded-lg border px-3 py-2 text-[13px]"
          : "border-amber-500/40 bg-amber-500/10 text-amber-700 dark:text-amber-400 relative z-20 mb-2 flex w-full items-center gap-2 rounded-lg border px-3 py-2 text-[13px]"
      }
    >
      <IconAlertTriangle className="size-4 shrink-0" />
      {notice === "sendFailed"
        ? t("chat.a2uiSendFailed")
        : t("chat.a2uiStaleCard")}
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

/** v0.9 帧的 union 分支键（与 Go 侧 renderUIBranchKeys 同集）。 */
const A2UI_FRAME_OPS = [
  "createSurface",
  "updateComponents",
  "updateDataModel",
  "deleteSurface",
] as const

/** 从 wire 帧（unknown）提取 surfaceId：帧形如
 * {version, <op>: {surfaceId, ...}}。识别不了的帧返回 undefined——调用方
 * 一律原样保留，交给 processor 的 zod 层拒绝（错误占位现状行为）。 */
function frameSurfaceId(frame: unknown): string | undefined {
  if (typeof frame !== "object" || frame === null) return undefined
  const record = frame as Record<string, unknown>
  for (const op of A2UI_FRAME_OPS) {
    const payload = record[op]
    if (typeof payload === "object" && payload !== null) {
      const surfaceId = (payload as Record<string, unknown>).surfaceId
      if (typeof surfaceId === "string") return surfaceId
    }
  }
  return undefined
}

/** 帧是否携带 createSurface 分支（重 create 接管的判定）。 */
function isCreateSurfaceFrame(frame: unknown): boolean {
  return typeof frame === "object" && frame !== null && "createSurface" in frame
}

/** 帧指纹（JSON 序列化）：pico 合并对同 path 的 updateDataModel 原地覆盖
 * （last value wins，pico_test 钉住）——累计批长度不变但前缀内容变化，
 * 且每次广播都是全新 JSON.parse（引用比较无效），只能逐帧内容比对。 */
function frameFingerprint(frame: unknown): string {
  return JSON.stringify(frame)
}

/** 累计批里出现过的 createSurface surfaceId 集（排序便于比较）：条件 ③
 * "surfaceId 变了（新卡）"的判据。 */
function collectCreateSurfaceIds(messages: unknown[]): string[] {
  const ids = new Set<string>()
  for (const frame of messages) {
    if (!isCreateSurfaceFrame(frame)) continue
    const surfaceId = frameSurfaceId(frame)
    if (surfaceId !== undefined) ids.add(surfaceId)
  }
  return [...ids].sort()
}

/** 排序 id 序列相等（逐元素比较，不拼串——surfaceId 理论上可含换行）。 */
function sameStringSeq(a: string[], b: string[]): boolean {
  return a.length === b.length && a.every((id, i) => id === b[i])
}

/** 历史折叠语义（对齐 RenderUIHistoryFold）：同 surfaceId 的最后一次
 * createSurface 起的帧构成幸存卡，被取代 incarnation 的帧丢弃（"第二次
 * create 取代前卡并丢弃已合并更新"）。正常累计批每 surface 只含一次
 * create——fold 恒等；仅重 create 接管批次（防线）在此收敛，让重建出的
 * processor 能整批喂入而不撞 "already exists"。识别不了的帧原样保留。 */
function foldAtLastCreate(messages: unknown[]): unknown[] {
  const lastCreate = new Map<string, number>()
  messages.forEach((frame, index) => {
    if (!isCreateSurfaceFrame(frame)) return
    const surfaceId = frameSurfaceId(frame)
    if (surfaceId !== undefined) lastCreate.set(surfaceId, index)
  })
  if (lastCreate.size === 0) return messages
  return messages.filter((frame, index) => {
    const surfaceId = frameSurfaceId(frame)
    if (surfaceId === undefined) return true
    const start = lastCreate.get(surfaceId)
    return start === undefined || index >= start
  })
}

/** processor 产出状态：surfaces 每次喂帧后更新（增量喂帧时 SurfaceModel
 * 实例不变，Lit 元素经细粒度 signal 复用——未触碰 path 的控件值由 DOM
 * 保留）；failed = 畸形消息或兜底再失败的错误占位。 */
interface A2uiCardState {
  surfaces: SurfaceModel<ReactCatalogComponent>[]
  failed: boolean
}

/** 卡内瞬时提示种类（三期任务 2，互斥——latest 卡只可能 sendFailed、
 * 历史卡只可能 stale，共用一个提示槽）。 */
type A2uiNotice = "sendFailed" | "stale"

/** 提示展示时长 = 失败冷却窗口（ms）：瞬时禁用的时长（ask-card 的
 * submitting 30s 恢复计时同构——那里等服务端回执，这里动作消息无回执，
 * 3s 读完提示即恢复可点）。 */
const NOTICE_WINDOW_MS = 3000

export function A2uiCard({
  ui,
  latest = true,
}: {
  ui: A2uiInfo
  /** false = 存在更新的 A2UI 卡，本卡为历史版本：动作回传在 handler
   * 拦截并提示（防错位）。默认 true：直接渲染（未经 buildTurns 标记）
   * 视为可交互；buildTurns 只给最近一张 a2ui 块标 latest（pi-message
   * 显式传 `latest === true`，未标记的块落到 false → 历史卡）。 */
  latest?: boolean
}) {
  const [state, setState] = useState<A2uiCardState>({
    surfaces: [],
    failed: false,
  })
  // 常驻 processor：useMemo 每次全量重建是"整批重播重置输入"问题的根源
  // （元素重挂，未提交表单输入丢失）。processedCount = 已喂帧数
  // （ui.messages[0..count) 已反映在 processor 里）。
  const processorRef = useRef<
    MessageProcessor<ReactCatalogComponent> | undefined
  >(undefined)
  const processedCountRef = useRef(0)
  // 已喂帧指纹（原地覆盖检测）+ 建卡时的 createSurface id 集（条件 ③）。
  const fedFingerprintsRef = useRef<string[]>([])
  const createIdsRef = useRef<string[]>([])

  // 瞬时提示（三期任务 2）：state 驱动提示条/遮罩渲染；noticeRef 供
  // handler 同步读取（冷却拒绝）。ref 与 state 在 showNotice 内同步更新，
  // 不走 effect 镜像——点击与提示之间无窗口期。
  const [notice, setNotice] = useState<A2uiNotice | undefined>(undefined)
  const noticeRef = useRef<A2uiNotice | undefined>(undefined)
  const noticeTimerRef = useRef<ReturnType<typeof setTimeout> | undefined>(
    undefined,
  )
  // 卸载清计时器（ask-card 的 submitting 恢复计时同款守卫）。
  useEffect(() => () => clearTimeout(noticeTimerRef.current), [])

  // latest 的 handler 内同步可读镜像：handler 闭包在建 processor 时定格，
  // latest 变化经 ref 传导（重渲染不重建 processor——增量更新语义不破坏）。
  const latestRef = useRef(latest)
  useEffect(() => {
    latestRef.current = latest
  }, [latest])

  useLayoutEffect(() => {
    const messages = ui.messages

    // 瞬时提示展示 NOTICE_WINDOW_MS：ref 与 state 同步更新（handler 内
    // 可同步读到冷却状态），连续提示顶掉旧计时。组件体内不定义本函数与
    // handleAction——它们只被 processor 构造消费，且仅引用 ref/setState
    // （exhaustive-deps 豁免项），留在 effect 内避免假依赖。
    const showNotice = (kind: A2uiNotice): void => {
      noticeRef.current = kind
      setNotice(kind)
      clearTimeout(noticeTimerRef.current)
      noticeTimerRef.current = setTimeout(() => {
        noticeRef.current = undefined
        setNotice(undefined)
      }, NOTICE_WINDOW_MS)
    }

    /** 动作回传处理器（MessageProcessor 构造第二参，全局兜底覆盖所有
     * surface）：动作组装成用户消息后走既有聊天发送路径上行（ask-card
     * 同一条 sendChatMessage——普通 message.send，无专用 WS 消息类型）。
     * - 历史卡（!latestRef）：拦截 + stale 提示（防错位——回传会带旧
     *   surfaceId/旧 dataModel 值，三期任务 2）。
     * - 发送失败（WS 未连接，返回 false）：瞬时提示 + 冷却窗口；窗口内
     *   重试直接拒绝（overlay 挡不住键盘/程序化触发，handler 是唯一
     *   漏斗），窗口过后自动恢复可点。
     * - 成功：静默（消息进聊天流；turn 活跃时由既有 steering 队列承接）。
     */
    const handleAction = (action: ActionPayload): void => {
      if (!latestRef.current) {
        showNotice("stale")
        return
      }
      if (noticeRef.current === "sendFailed") return
      if (
        !sendChatMessage({
          content: buildActionMessage(action),
          attachments: [],
        })
      ) {
        showNotice("sendFailed")
      }
    }

    /** 全量建/重建共用体：fold → 新 processor → 整批喂入。processor 先入
     * ref 再喂帧；喂帧抛错时立即 dispose 并清引用再上抛（不泄漏、不留下
     * 半建 processor）。 */
    const buildFrom = (
      batch: unknown[],
    ): SurfaceModel<ReactCatalogComponent>[] => {
      const folded = foldAtLastCreate(batch)
      const processor = new MessageProcessor<ReactCatalogComponent>(
        [catalogWithVegaChart],
        handleAction,
        { version: "v0.9" },
      )
      processorRef.current = processor
      try {
        // batch 是 wire 数据（unknown[]）：真正的把关是 processor 内的
        // strict zod 校验，此断言只为通过编译器，不改变运行时行为。
        processor.processMessages(folded as ProcessableMessage[])
      } catch (error) {
        processorRef.current = undefined
        processor.dispose()
        throw error
      }
      processedCountRef.current = batch.length
      fedFingerprintsRef.current = batch.map(frameFingerprint)
      createIdsRef.current = collectCreateSurfaceIds(batch)
      return Array.from(processor.getSurfaces().values())
    }

    /** 全量重建：旧 processor 必须先 dispose（泄漏防线）。dispose 只清
     * RPC（实测 message-processor.dispose() 仅调 rpc.dispose()），已取出
     * 的 surface 模型不受影响，StrictMode 双 effect 同理安全。重建失败 →
     * 错误占位（现状行为；buildFrom 抛错前已自带 dispose + 清引用）。 */
    const rebuild = (batch: unknown[]): void => {
      processorRef.current?.dispose()
      processorRef.current = undefined
      try {
        setState({ surfaces: buildFrom(batch), failed: false })
      } catch {
        processorRef.current = undefined
        processedCountRef.current = 0
        fedFingerprintsRef.current = []
        createIdsRef.current = []
        setState({ surfaces: [], failed: true })
      }
    }

    // 分支 ① processor 不存在（首挂 / 此前失败后的重试）→ 全量建。
    if (processorRef.current === undefined) {
      rebuild(messages)
      return
    }

    const processedCount = processedCountRef.current
    // 累计批只会追加 / 原地覆盖（pico 合并语义）：长度回缩说明批次被整体
    // 换掉，增量无法对账——直接重建。
    if (messages.length < processedCount) {
      rebuild(messages)
      return
    }

    // 前缀分歧检测：第一个内容变化的帧。有分歧 → 从该帧起整体重播（后缀
    // 未变帧重播幂等 / 值相同，与整批重播收敛到同一最终状态）。
    let firstDivergence = -1
    for (let i = 0; i < processedCount; i++) {
      if (fedFingerprintsRef.current[i] !== frameFingerprint(messages[i])) {
        firstDivergence = i
        break
      }
    }
    const feedStart = firstDivergence === -1 ? processedCount : firstDivergence

    // 分支 ② 分歧 / 新增帧里出现 createSurface → 重 create 接管 →
    // 全量重建（processor 里的 surface 需要重建）。
    for (let i = feedStart; i < messages.length; i++) {
      if (isCreateSurfaceFrame(messages[i])) {
        rebuild(messages)
        return
      }
    }

    // 分支 ③ createSurface id 集变化 → 新卡换入（React 复用组件实例 /
    // 批次被换）→ 全量重建。
    const createIds = collectCreateSurfaceIds(messages)
    if (!sameStringSeq(createIds, createIdsRef.current)) {
      rebuild(messages)
      return
    }

    // 无新帧且前缀无分歧（如重连重播同批 / 无关 store 更新）→ 无操作。
    if (feedStart >= messages.length) return

    // 分支 ④ 增量喂帧：MessageProcessor 天生吃增量（同 surface 重复
    // createSurface 抛 "already exists"、未知 surface 的 update 抛
    // "not found"——上面分支已挡住重 create，orphan update 仍会抛错进兜底）。
    try {
      processorRef.current.processMessages(
        messages.slice(feedStart) as ProcessableMessage[],
      )
      processedCountRef.current = messages.length
      fedFingerprintsRef.current = messages.map(frameFingerprint)
      setState({
        surfaces: Array.from(processorRef.current.getSurfaces().values()),
        failed: false,
      })
    } catch (error) {
      // 兜底：增量抛错 → warn 一次（出现频率高说明 pico 合并语义与前端
      // 假设漂移）+ 全量重建一次；再失败 → 错误占位（rebuild 内）。
      console.warn(
        "[A2uiCard] incremental A2UI update failed; rebuilt the card from the full batch",
        error,
      )
      rebuild(messages)
    }
  }, [ui.messages])

  // 卸载时释放（dispose 只清 RPC，已取出的 surface 模型不受影响——
  // StrictMode 双 effect 安全）。重建路径的旧 processor 在 effect 内
  // 同步 dispose，不走这条 cleanup（否则增量更新会误杀常驻 processor）。
  useEffect(
    () => () => {
      processorRef.current?.dispose()
    },
    [],
  )

  if (state.failed) return <A2uiErrorPlaceholder />
  if (state.surfaces.length === 0) return null

  return (
    <A2uiRenderBoundary fallback={<A2uiErrorPlaceholder />}>
      <MarkdownComponentContext.Provider value={A2uiMarkdown}>
        {/* 容器自适应聊天流宽度（无 max-w 上限，与相邻消息等宽）；a2ui-surface
            class 在 pi-chat.css 把 basicCatalog 的 --a2ui-color-* 变量映射到
            app 主题 token——内层元素不再用注入默认的灰黑/蓝配色。头部标题
            行（图标+"交互视图"）已移除：卡片自解释，用户反馈为噪音。 */}
        <div className="a2ui-surface border-border/60 bg-card my-2 w-full overflow-hidden rounded-xl border shadow-sm">
          <div className="relative px-3.5 py-3">
            {notice && <A2uiNoticeBar notice={notice} />}
            {/* 发送失败冷却窗口的"瞬时禁用"遮罩：挡住指针交互（键盘/
                程序化触发由 handler 的冷却拒绝兜底——唯一漏斗）。 */}
            {notice === "sendFailed" && (
              <div
                aria-hidden
                className="bg-muted/20 absolute inset-0 z-10 cursor-not-allowed"
              />
            )}
            {state.surfaces.map((surface) => (
              <A2uiSurface key={surface.id} surface={surface} />
            ))}
          </div>
        </div>
      </MarkdownComponentContext.Provider>
    </A2uiRenderBoundary>
  )
}
