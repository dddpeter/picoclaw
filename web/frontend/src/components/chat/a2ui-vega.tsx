/**
 * VegaChart — 自建 A2UI catalog 组件（二期任务 6）：在 A2UI surface 内渲染
 * vega-lite 图表。官方 @a2ui/catalog-vegachart 未发布 npm，这里用
 * `createComponentImplementation`（@a2ui/react 的 React 实现，节点层按
 * `"render" in impl` 分派，A2uiSurface 挂载时 prepareCatalogs→toWebComponent
 * 自动注册为 a2ui-react-vegachart custom element）+ `Catalog.derive` 混入
 * basicCatalog。渲染管线（A2uiCard/MessageProcessor/A2uiSurface）零改动。
 *
 * 打包红线：react-vega（→ vega-embed → vega-lite/vega）只能在 React.lazy
 * 工厂里动态 import——图表栈落在独立 vite chunk，主 bundle 不得引入。
 * 类型导入（import type）编译期擦除，不影响打包。
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
import { IconChartBarOff } from "@tabler/icons-react"
import { Suspense, lazy, useEffect, useState } from "react"
import { useTranslation } from "react-i18next"
import type { EmbedOptions, VisualizationSpec } from "vega-embed"
import { z } from "zod"

/**
 * 懒加载的 vega-embed React 封装。首次渲染 VegaChart 才拉取图表 chunk。
 * react-vega@8 的 VegaEmbed 内部 catch 了 embed 异步错误并经 onError 上抛
 * （不冒泡异常），失败时落占位即可，不炸卡不炸聊天流。
 */
const LazyVegaEmbed = lazy(async () => {
  const { VegaEmbed } = await import("react-vega")
  return { default: VegaEmbed }
})

/** svg 渲染器：矢量清晰、无需 canvas（jsdom 测试可跑）；actions 关闭——
 * 导出/编辑菜单在聊天卡片里是噪音。模块级常量：react-vega 对 options 做
 * 深比较，恒定引用避免多余的 re-embed。 */
const VegaEmbedOptions: EmbedOptions = { renderer: "svg", actions: false }

/** 图表渲染失败/数据未就绪的组件内占位：不炸卡、不炸聊天流。 */
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

/**
 * VegaChart 视图。props.spec 已经过 generic binder 解析（schema 的
 * spec 字段因含 DataBindingSchema 分支被绑定为 DYNAMIC）：内联对象原样
 * 透传；{path} 绑定按 dataModel 值解析且响应式更新。解析结果为
 * undefined（未就绪/缺路径）或非对象 → 占位。
 */
function VegaChartView({ props }: ReactA2uiComponentProps<{ spec: unknown }>) {
  const spec = props.spec
  const [failed, setFailed] = useState(false)
  // dataModel 更新产生新 spec 对象时重置失败态，让修好的 spec 能重绘。
  useEffect(() => {
    setFailed(false)
  }, [spec])
  const usable = typeof spec === "object" && spec !== null && !("path" in spec)
  if (!usable || failed) return <VegaChartPlaceholder />
  return (
    <Suspense fallback={<VegaChartPlaceholder />}>
      <LazyVegaEmbed
        spec={spec as VisualizationSpec}
        options={VegaEmbedOptions}
        onError={() => setFailed(true)}
        className="w-full [&_svg]:mx-auto [&_svg]:max-w-full"
      />
    </Suspense>
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
