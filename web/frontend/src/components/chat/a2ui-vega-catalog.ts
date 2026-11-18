// VegaChart catalog 装配（从 a2ui-vega.tsx 抽出：组件文件保持
// components-only，react-refresh/HMR 才能保留组件状态）。本文件没有任何
// React 组件——只有 schema、实现装配与派生 catalog。
//
// 打包红线与失败分层、wire 兼容说明见 a2ui-vega.tsx 头注。
import {
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
import { z } from "zod"

import { VegaChartView } from "./a2ui-vega"

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

const VegaChartImplementation = createComponentImplementation(
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
