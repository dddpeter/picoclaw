// A2UI catalog 基础设施（从 a2ui-card.tsx 抽出：组件文件保持
// components-only，react-refresh/HMR 才能保留组件状态）。buildActionMessage
// 是 A2UI 二期任务 1 的逐字协议契约，由单测直引；initA2uiBasicCatalog 由
// 应用入口（main.tsx）与全部 a2ui 测试文件调用。
import {
  type ActionPayload,
} from "@a2ui/web_core/v0_9"
import {
  basicCatalog,
  injectBasicCatalogStyles,
} from "@a2ui/web_core/v0_9/basic_catalog"
import { registerUniversalElement } from "@a2ui/web_core/v0_9/universal"

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
