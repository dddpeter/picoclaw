/**
 * VegaChart — 自建 A2UI catalog 组件（二期任务 6）：在 A2UI surface 内渲染
 * vega-lite 图表。官方 @a2ui/catalog-vegachart 未发布 npm，这里用
 * `createComponentImplementation`（@a2ui/react 的 React 实现，节点层按
 * `"render" in impl` 分派，A2uiSurface 挂载时 prepareCatalogs→toWebComponent
 * 自动注册为 a2ui-react-vegachart custom element）+ `Catalog.derive` 混入
 * basicCatalog。渲染管线（A2uiCard/MessageProcessor/A2uiSurface）零改动。
 *
 * 打包红线：react-vega（→ vega-embed → vega-lite/vega）只能在动态 import
 * 里引用——图表栈落在独立 vite chunk，主 bundle 不得引入。类型导入
 * （import type）编译期擦除，不影响打包。
 *
 * 失败分层（都不炸卡、不炸聊天流）：① spec 未就绪/缺路径 → 占位；
 * ② vega-lite 编译/渲染失败 → VegaEmbed 内部 catch 经 onError → 占位；
 * ③ chunk 加载被拒（网络抖动/重部署后旧 hash 404）→ Suspense 只兜 pending
 * 不兜 rejection，VegaChartChunkBoundary 组件内捕获降级——绝不冒泡到卡级
 * A2uiRenderBoundary 把整卡打成占位。②③ 共用 failed 态；spec 变化只重置
 * 失败态，lazy 实例仅在上一轮失败过时重建——未失败时复用实例，流式 spec
 * 更新不换组件类型（VegaEmbed 不重挂）。
 *
 * wire 兼容：Catalog.derive 不自动保留源 catalog id（实测省略 catalogId
 * 产生无 id catalog），因此显式传 basicCatalog.id——Go render_ui 门强制的
 * catalogId 不变；derive 保留 protocolVersion、theme schema 与全部 basic
 * 组件（同对象引用）。
 */
import {
  type ReactA2uiComponentProps,
  type ReactCatalogComponent,
  createComponentImplementation,
} from "@a2ui/react/v0_9"
import {
  Catalog,
  type ComponentApi,
  DataBindingSchema,
} from "@a2ui/web_core/v0_9"
import { basicCatalog } from "@a2ui/web_core/v0_9/basic_catalog"
import { type WebComponentImplementation } from "@a2ui/web_core/v0_9/universal"
import { IconChartBarOff, IconLoader2 } from "@tabler/icons-react"
import {
  Component,
  type ReactNode,
  Suspense,
  lazy,
  useEffect,
  useMemo,
  useRef,
  useState,
} from "react"
import { useTranslation } from "react-i18next"
import type { EmbedOptions, VisualizationSpec } from "vega-embed"
import { z } from "zod"

/**
 * 生成一个全新的 React.lazy 实例。ES module registry 缓存让首次成功后的
 * import() 立即返回，但 rejected 的 React.lazy 会把拒绝缓存进实例——
 * 重试 chunk 加载必须换新实例（见 VegaChartView 的 spec 变化重建分支）。
 */
function createLazyVegaEmbed() {
  return lazy(async () => {
    const { VegaEmbed } = await import("react-vega")
    return { default: VegaEmbed }
  })
}

/** svg 渲染器：矢量清晰、无需 canvas（jsdom 测试可跑）；actions 关闭——
 * 导出/编辑菜单在聊天卡片里是噪音。模块级常量：react-vega 对 options 做
 * 深比较，恒定引用避免多余的 re-embed。 */
const VegaEmbedOptions: EmbedOptions = { renderer: "svg", actions: false }

/** 主题友好的 vega 默认配置：透明背景融入卡片、轴/标题/图例文字用
 * currentColor 随主题、网格低透明度、单序列标记色用主题主色（运行时读
 * CSS 变量，主题切换后下一次 spec 解析自动取新值）。模型 spec 的显式
 * config/mark 颜色按键优先——这里只补默认，不覆盖模型意图。 */
function themeVegaDefaults(): Record<string, unknown> {
  const styles = getComputedStyle(document.documentElement)
  const primary = styles.getPropertyValue("--primary").trim() || "currentColor"
  return {
    background: "transparent",
    mark: { color: primary },
    axis: {
      domainColor: "currentColor",
      labelColor: "currentColor",
      tickColor: "currentColor",
      titleColor: "currentColor",
      gridColor: "currentColor",
      gridOpacity: 0.12,
    },
    legend: { labelColor: "currentColor", titleColor: "currentColor" },
    title: { color: "currentColor" },
  }
}

/** spec 归一化（用户反馈 2026-11-14：白底窄图与主题不搭）：宽度自适应
 * 宿主（宿主可测宽时注入数值 width——"container" 依赖布局环境，jsdom 为
 * 0 宽会让图表塌陷）、透明背景、主题 config。两层合并：vega-lite 顶层
 * view 键 + config 内按段（axis/title/...）合并，模型显式值逐键胜出。
 * spec 非对象时原样返回（占位路径自己处理）。 */
function normalizeVegaSpec(
  spec: unknown,
  hostWidth: number,
): VisualizationSpec {
  if (typeof spec !== "object" || spec === null || "path" in (spec as object)) {
    return spec as VisualizationSpec
  }
  const s = { ...(spec as Record<string, unknown>) }
  if (s.width === undefined && hostWidth > 60) {
    // 全宽会把少类目图表的柱子撑肥（5 根柱 × 250px）；上限 560px 配合
    // svg mx-auto 居中，宽表/折线在 760 内也够读。下限保护窄屏。
    s.width = Math.min(Math.max(Math.floor(hostWidth), 240), 560)
  }
  const modelConfig = (typeof s.config === "object" && s.config !== null
    ? s.config
    : {}) as Record<string, Record<string, unknown>>
  const defaults = themeVegaDefaults()
  const mergedConfig: Record<string, unknown> = {}
  for (const [key, value] of Object.entries(defaults)) {
    mergedConfig[key] =
      key === "background"
        ? value
        : { ...(value as object), ...(modelConfig[key] ?? {}) }
  }
  // background 在 vega-lite 顶层是 view 属性不是 config 段：模型显式
  // background 优先于默认透明。
  if (s.background === undefined) s.background = defaults.background
  s.config = { ...modelConfig, ...mergedConfig }
  return s as VisualizationSpec
}

/**
 * 图表级错误边界：懒加载 chunk 被拒时 React 会把 rejection 抛给 Suspense
 * 外侧最近的错误边界（Suspense 自身只处理 pending），若不拦会把异常送到
 * 卡级 A2uiRenderBoundary → 整卡降级且无重试路径。本边界就地拦截：
 * catch 当轮渲染 null（阻断冒泡），componentDidCatch 上抛给视图的 failed
 * 态（复用 spec 变化重置机制，attempt 重建 lazy 实例实现真重试）。位于
 * Suspense 外侧，同时兜子树内任何渲染崩溃。
 */
class VegaChartChunkBoundary extends Component<
  { onError: (error: unknown) => void; children: ReactNode },
  { failed: boolean }
> {
  state = { failed: false }
  static getDerivedStateFromError() {
    return { failed: true }
  }
  componentDidCatch(error: unknown) {
    this.props.onError(error)
  }
  render() {
    return this.state.failed ? null : this.props.children
  }
}

/**
 * VegaChart 视图。props.spec 已经过 generic binder 解析（schema 的
 * spec 字段因含 DataBindingSchema 分支被绑定为 DYNAMIC）：内联对象原样
 * 透传；{path} 绑定按 dataModel 值解析且响应式更新。解析结果为
 * undefined（未就绪/缺路径）或非对象 → 占位。
 */
function VegaChartView({ props }: ReactA2uiComponentProps<{ spec: unknown }>) {
  const spec = props.spec
  const [failed, setFailed] = useState(false)
  // failed 的镜像 ref：spec 变化的 effect 读最新失败态（state 在 effect 闭包
  // 里是旧值；把 failed 放进 deps 又会在失败态翻转时多跑一轮 effect）。
  const failedRef = useRef(false)
  // lazy 实例持有在 state（创建发生在 initializer/updateUpdater，渲染期
  // 只消费）：rejected 的 React.lazy 会把拒绝缓存进实例，复用实例不会
  // 重试 chunk 加载——重置失败态时必须换新实例。
  const [LazyVegaEmbed, setLazyVegaEmbed] = useState(createLazyVegaEmbed)
  // 流式场景 updateDataModel 频繁产生新 spec 对象引用（{path} 绑定每次解析
  // 出新值）：spec 变化只重置失败态，lazy 实例仅在上一轮真失败过（实例可能
  // 已缓存 rejection）时才重建——无条件重建会换组件类型 → VegaEmbed 卸载
  // 重挂 → 图表闪烁。
  // 生产构建下重试有效性依赖 bundler 的 import 失败缓存行为（vite
  // __vitePreload 有 URL 级缓存），不可依赖；chunk 404 的标准解法是刷新页面。
  useEffect(() => {
    if (!failedRef.current) return
    failedRef.current = false
    setFailed(false)
    setLazyVegaEmbed(createLazyVegaEmbed())
  }, [spec])
  // 宿主宽度测量（spec 归一化用）：ResizeObserver 初始即回调一次，此后
  // 容器宽度变化（侧栏开关等）驱动图表重嵌。jsdom 无 ResizeObserver 时
  // 宽度恒 0 → 归一化不注入 width → 测试走 vega-lite 默认宽度。
  const hostRef = useRef<HTMLDivElement>(null)
  const [hostWidth, setHostWidth] = useState(0)
  useEffect(() => {
    const el = hostRef.current
    if (!el || typeof ResizeObserver === "undefined") return
    const observer = new ResizeObserver((entries) => {
      const width = entries[0]?.contentRect.width ?? 0
      setHostWidth((prev) => (Math.abs(prev - width) > 1 ? width : prev))
    })
    observer.observe(el)
    return () => observer.disconnect()
  }, [])
  const handleError = (error: unknown) => {
    console.warn("[VegaChart] chart failed to render/load:", error)
    failedRef.current = true
    setFailed(true)
  }
  const usable = typeof spec === "object" && spec !== null && !("path" in spec)
  // 归一化按 spec + 宿主宽度记忆化：宽度和主题 config 都在这里注入。
  const normalized = useMemo(
    () => normalizeVegaSpec(spec, hostWidth),
    [spec, hostWidth],
  )
  if (!usable || failed) return <VegaChartPlaceholder />
  return (
    <div ref={hostRef} className="w-full">
      {/* 边界在 Suspense 外侧：Suspense 只兜 pending，rejection 抛给它上方
      最近的错误边界——正是本边界。 */}
      <VegaChartChunkBoundary onError={handleError}>
        <Suspense fallback={<VegaChartLoadingPlaceholder />}>
          <LazyVegaEmbed
            spec={normalized}
            options={VegaEmbedOptions}
            onError={handleError}
            className="w-full [&_svg]:mx-auto [&_svg]:max-w-full"
          />
        </Suspense>
      </VegaChartChunkBoundary>
    </div>
  )
}

/** chunk 加载/编译渲染失败的组件内占位：与卡级错误占位同款虚线视觉，
 * 但失败被限制在图表槽位内。 */
function VegaChartPlaceholder() {
  const { t } = useTranslation()
  return (
    <div
      data-vegachart-placeholder=""
      className="border-border/60 bg-muted/30 text-muted-foreground flex min-h-16 w-full items-center justify-center gap-2 rounded-lg border border-dashed px-3 py-4 text-[13px]"
    >
      <IconChartBarOff className="size-4 shrink-0" />
      {t("chat.a2uiChartFailed")}
    </div>
  )
}

/** chunk 加载中的中性占位：只用加载图标不用文案（chunk 通常亚秒完成，
 * 为一瞬间的回退补五语言 loading 文案不值——中性符号免 i18n）。 */
function VegaChartLoadingPlaceholder() {
  return (
    <div
      data-vegachart-loading=""
      aria-label="loading chart"
      className="border-border/60 bg-muted/30 text-muted-foreground flex min-h-16 w-full items-center justify-center rounded-lg border border-dashed px-3 py-4"
    >
      <IconLoader2 className="size-4 animate-spin" />
    </div>
  )
}

/** 组件 API：props 仅 {spec}（strict，无 weight/accessibility/style——
 * 与 Go render_ui description 教给模型的 VegaChart{spec} 一致）。 */
const VegaChartApi = {
  name: "VegaChart",
  schema: z
    .object({
      spec: z.union([
        // {"path": "/pointer"} 数据绑定（generic binder 按值解析）。
        DataBindingSchema,
        // 内联 vega-lite JSON（顶层 view spec，任意键的对象）。
        z.record(z.string(), z.any()),
      ]),
    })
    .strict(),
} satisfies ComponentApi

export const VegaChartImplementation = createComponentImplementation(
  VegaChartApi,
  VegaChartView,
)

/**
 * basicCatalog + VegaChart 的派生 catalog。derive 的 components 列表里，
 * 字符串名沿用 basicCatalog 原条目（同对象引用），其余为新增实现。
 * cast 说明：derive 的类型签名只收 WebComponentImplementation（basic
 * catalog 的 Lit 实现），而 VegaChartImplementation 是 React 实现——
 * 运行时两个实现都会被 @a2ui/react 的节点层接受（ReactCatalogComponent
 * union），cast 只收窄类型不改变运行时对象。
 */
export const catalogWithVegaChart = basicCatalog.derive({
  catalogId: basicCatalog.id,
  components: [
    ...basicCatalog.components.keys(),
    VegaChartImplementation as unknown as WebComponentImplementation,
  ],
}) as unknown as Catalog<ReactCatalogComponent>
