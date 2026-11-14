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
  const usable = typeof spec === "object" && spec !== null && !("path" in spec)
  const handleError = (error: unknown) => {
    console.warn("[VegaChart] chart failed to render/load:", error)
    failedRef.current = true
    setFailed(true)
  }
  if (!usable || failed) return <VegaChartPlaceholder />
  return (
    // 边界在 Suspense 外侧：Suspense 只兜 pending，rejection 抛给它上方
    // 最近的错误边界——正是本边界。
    <VegaChartChunkBoundary onError={handleError}>
      <Suspense fallback={<VegaChartLoadingPlaceholder />}>
        <LazyVegaEmbed
          spec={spec as VisualizationSpec}
          options={VegaEmbedOptions}
          onError={handleError}
          className="w-full [&_svg]:mx-auto [&_svg]:max-w-full"
        />
      </Suspense>
    </VegaChartChunkBoundary>
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
