/**
 * `.lobe-*` 令牌桥的 Tailwind 任意属性 util 串（评审 P2-2，消除重复）。
 *
 * 用途：model-selector / session-history-sidebar 等组件挂载在 `.pi-chat`
 * 作用域之外，无法继承 tokens.css 的变量定义，需在宿主元素上显式声明
 * `[--lobe-x:var(--y)]` 属性，内部 Tailwind 类才能用上令牌。
 *
 * 这里是两侧需求的**并集**：多声明一个未被使用的 CSS 变量无副作用，
 * 换取单一维护点，避免同一批映射在多处复制粘贴后各自漂移。
 *
 * 注意：串必须保持「静态字面量」，Tailwind 依赖文本扫描生成对应 CSS。
 */
export const LOBE_SCOPE_TOKENS =
  "[--lobe-text:var(--foreground)] [--lobe-text-2:var(--muted-foreground)] [--lobe-text-desc:var(--muted-foreground)] [--lobe-text-disabled:var(--muted-foreground)] [--lobe-border:var(--border)] [--lobe-border-2:var(--border)] [--lobe-fill:color-mix(in_srgb,var(--foreground)_14%,transparent)] [--lobe-fill-2:var(--accent)] [--lobe-fill-3:var(--secondary)] [--lobe-bg-elevated:var(--card)] [--lobe-info:var(--primary)]"
