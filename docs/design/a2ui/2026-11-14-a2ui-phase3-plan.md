# A2UI 三期实现计划（本地态保持 + 体验补齐 + 压缩开关 + 视觉进阶）

> **面向 AI 代理的工作者：** 必需子技能：使用 subagent-driven-development 逐任务实现此计划。步骤使用复选框（`- [ ]`）语法来跟踪进度。
> 前序文档：`docs/design/2026-11-14-a2ui-phase2.zh.md`（二期 as-built）、`docs/design/a2ui/2026-11-14-a2ui-phase2-plan.md`、一期三件套。

**目标：** ① 流式增量到达时**保留卡片本地态**（用户未提交的表单输入不丢）；② 交互体验补齐（发送失败反馈 + 过期卡片防错位）；③ render_ui description 压缩（>3KB→<2.5KB）+ 工具真正可配置关闭；④ 视觉进阶（VegaChart 主题联动 + 卡面渐变深化）。

**基线：** b6a8f9e5（二期 + 全场景真实测试 + 三批视觉打磨之后）。

## 全局约束

- **任务 1 的核心语义**：pico 合并广播的 payload 是**累计批**（新帧 append 在尾部——二期已实证），前端 A2uiCard 因此可以增量喂帧。processor 常驻（useRef），ui.messages 变化时只 `processMessages(ui.messages.slice(processedCount))`。三个重建触发条件：① processor 不存在；② 新帧里出现 createSurface（同 surfaceId 重 create 接管——processor 里的 surface 需要重建）；③ surfaceId 变了（新卡）。其余情况一律增量。兜底：增量 processMessages 抛错 → 丢弃 processor 全量重建一次；再失败 → 错误占位（现状行为）。
- 本地态保留的机制依据：Lit 元素实例在增量 processMessages 下复用（组件 id 不变），未触碰 path 的控件值由 DOM 保留——这正是二期"整批重播重置输入"问题的反面。任务 1 测试必须实证：渲染→fill 输入→追加一帧 updateDataModel（**不含**该输入的 path）→ 断言输入值保留。
- 全量重建 fallback 的触发必须记录（console.warn 一次），出现频率高说明 pico 合并语义与前端假设漂移。
- **任务 3 的 description 压缩红线**：所有 SchemaContract 锁定的教学短语（分帧形状/根节点/树不变量/ChoicePicker options/交互回传契约/VegaChart 主题）**语义保留**，压缩的是措辞冗余与重复示例（完整表单示例可从 5 条组件压成 2 条）。压缩后 SchemaContract 逐条复核——丢一条教学 = 回归。目标 <2.5KB。
- **任务 3 开关语义**：IsToolEnabled 增加 `case "render_ui"` 显式分支（读 `t.RenderUI` 布尔配置，未配置默认 true——保持开放默认），config 里可配置关闭。关掉后 agent 工具列表不出现 render_ui（与 messageTool 等的关闭行为一致）。
- **任务 4 VegaChart 主题联动**：监听 `document.documentElement` 的 class 属性变化（MutationObserver，主题切换 = .dark class 切换）→ bump themeVersion → normalizeVegaSpec 重新执行（重新读 --primary）→ 重嵌。防抖（主题切换是低频事件，300ms）。
- **数字滚动动效明确不做**：Text 是 Lit 组件渲染，数值滚动需要组件层 JS 拦截，侵入价值比低——写进非目标。
- 前端测试 vitest（jsdom），Go 测试失败集差集法（白名单照旧）；每个任务 TDD 一 commit；注释语言与周围代码一致。
- kimi-webbridge 真实测试（任务 5）：流式更新时先填输入再触发 update 验证本地态保留；登录过期让用户手动登录后轮询接管（不要要密码）。

## 审查重点（Review Focus）

1. **任务 1 的重建/增量分支正确性**：追加 updateDataModel 帧 → 必须走增量；追加含 createSurface 的帧 → 必须全量重建；surfaceId 变化 → 全量重建。分支错一处就是本地态丢失或"already exists"崩溃。
2. **processor 泄漏**：常驻 processor 的 dispose 路径——组件卸载、全量重建（旧 processor 必须先 dispose）、surfaceId 变化重建。任何路径漏 dispose 都是泄漏。
3. **description 压缩不丢教学**：SchemaContract 全部锁定短语复核；交互回传契约（[A2UI 交互] 格式）一字不动——agent 解析靠它。
4. **开关关闭路径**：关掉 render_ui 后 agent_init 不注册、agent 工具列表干净、无残留错误。
5. **任务 4 不引入 fixed 包含块**：任何新 CSS 不得给 .a2ui-surface 及祖先加 transform/filter/backdrop-filter（fill-mode 教训，b6a8f9e5）。

---

### 任务 1：A2uiCard 增量 processor——卡片本地态保持

**文件：**
- 修改：`web/frontend/src/components/chat/a2ui-card.tsx`（useMemo 重建模式 → processor 常驻 + 增量喂帧；三重建触发条件 + fallback；详见全局约束核心语义）
- 测试：`web/frontend/src/components/chat/a2ui-card.test.tsx` 追加用例（**必须含本地态保留实证**：渲染→fill 输入→追加 updateDataModel 帧→断言输入值保留且新文本渲染；追加含 createSurface 帧→断言 processor 重建即新内容渲染）

- [ ] TDD 五步 → **Commit** — `feat(web): A2uiCard 增量 processor——流式更新保留卡片本地态`

### 任务 2：交互体验补齐

**文件：**
- 修改：`web/frontend/src/components/chat/a2ui-card.tsx`（sendChatMessage 返回 false 时按钮瞬时禁用 + 失败提示文案，i18n 五语言；实现者先读 ask-card 的 submitting/seal 模式照抄）
- 修改：过期卡片防错位——实现者先调研 ask-card 的 latest/seal 机制（读 `web/frontend/src/components/chat/ask-card.tsx` 与其数据来源），选最小侵入方案（如消息序号比对禁用旧卡按钮）；方案写进报告
- 测试：断言发送失败 → 提示出现；过期卡按钮禁用路径（按选定方案）

- [ ] TDD 五步 → **Commit** — `feat(web): A2UI 交互反馈与过期卡防错位`

### 任务 3：description 压缩 + render_ui 配置开关

**文件：**
- 修改：`pkg/tools/render_ui.go`（description 重写压缩：全 SchemaContract 教学语义保留、示例精简；目标 <2.5KB）
- 修改：`pkg/config/config.go`（IsToolEnabled 加 `case "render_ui"` 显式分支；检查 ToolsConfig 里 RenderUI 字段的添加位置与 JSON tag）
- 修改：`pkg/agent/agent_init.go`（关闭路径验证——注册被跳过后无残留）
- 测试：SchemaContract 全量复核（含新压缩文本）；config 开关 on/off 两态测试；agent_init 注册跳过测试

- [ ] TDD 五步 → **Commit** — `feat(tools): render_ui description 压缩 + 可配置关闭`

### 任务 4：视觉进阶——VegaChart 主题联动 + 卡面渐变

**文件：**
- 修改：`web/frontend/src/components/chat/a2ui-vega.tsx`（MutationObserver 监听 documentElement class → 防抖 300ms → bump themeVersion → normalizeVegaSpec 重跑 + lazy 实例重建（主题色变了必须重嵌））
- 修改：`web/frontend/src/chat-pi/pi-chat.css`（卡面渐变深化：主色→次色的对角渐变 + hover 光晕增强；**红线：不得给 .a2ui-surface 及祖先加 transform/filter/backdrop-filter**）
- 测试：mock 主题切换（html class 变更）→ 断言 spec 重新归一化（新主题色出现）；CSS 不破坏既有测试

- [ ] TDD 五步 → **Commit** — `feat(web): VegaChart 主题联动 + 卡面渐变深化`

### 任务 5：端到端验证 + 文档

**文件：**
- 修改：`docs/design/2026-11-14-a2ui-phase3.zh.md`（as-built：四项交付链路、kimi-webbridge 实测记录、遗留项）
- 修改：`CHANGELOG.md`（0.5.9 条目）

- [ ] 全量测试（差集法）+ `pnpm build`
- [ ] kimi-webbridge 真实测试：**流式更新本地态保留**（渲染→填输入→让模型发增量→断言输入保留）、发送失败反馈（断网模拟可选）、开关关闭（config 改 false → agent 工具列表验证）
- [ ] **Commit** — `docs: A2UI 三期设计文档与 0.5.9 发布注记`

## 明确非目标（本计划不做）

- 数字滚动动效（Text 是 Lit 渲染，需组件层 JS 拦截，价值比低——进三期后仍不做）
- 多 surface 同 turn（二期非目标延续）
- Video/AudioPlayer 组件（外部媒体 URL 可达性不可控）
- Modal 内表单交互的特殊处理（走通用交互回传）
