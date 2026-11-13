# A2UI Web 渲染集成 — 侦察与分析过程（交接文档）

> 本文是 `docs/design/a2ui/2026-11-13-a2ui-web-render-plan.md` 的姊妹篇：计划文档回答"做什么"，
> 本文回答"为什么这么设计、代码库事实依据在哪、调研中排除了什么"。
> 供接手开发的 agent 当地图使用：每条结论都带文件路径与行号锚点，动手前可逐一复核。
> 调研时间：2026-11-13；基线 commit：`bfbee3c0`（v0.5.6）。

## 0. 需求溯源（brainstorming 结论）

- 用户画面：picoclaw web 聊天中，agent 回复流里长出可交互表单/图表/卡片，能点、能填、结果回传 agent。
- 用户选定 MVP 场景：① 体检报告图表 ② 工具调用结果渲染成结构化卡片。
- 范围决策：**MVP 只做展示型**（看、hover），表单数据回传闭环放二期（用户确认"按你的建议实施"）。
- 技术选型：用户明确"直接用官方 `@a2ui/react`"（此前评估过社区 `a2ui-vue`，因 picoclaw web 是 React 19 而否决）。

## 1. 为什么不兼容飞书流式卡片（前置结论）

A2UI 渲染器只支持 React/Angular/Lit/Flutter/SwiftUI/Jetpack Compose，飞书客户端是封闭渲染环境，
只认飞书自家 card JSON schema——A2UI 消息发进飞书就是无人认识的 JSON。
picoclaw web 是**我们控制渲染端**的自建 SPA（Vite + React 19 + TanStack Router），所以 A2UI 在这里成立。
值得偷师：A2UI 的 catalog 白名单机制（严格 schema 约束模型只能产出白名单组件组合，防幻觉）。

## 2. 代码库侦察事实（全链路锚点）

### 2.1 既有范式：ask_user 管线（本设计的模板）

整条"agent → 聊天流内交互卡"的管线仓库里已经有一条，就是 ask_user。A2UI 集成 = 照此模式开平行通道：

| 层 | 文件 | 关键锚点 |
|---|---|---|
| 工具定义 | `pkg/agent/ask_user.go:86` | `askUserToolDef()` — LLM-facing 定义，不进 registry，逐次注入 |
| 工具拦截 | `pkg/agent/pipeline_execute_loop.go:558` | `if toolName == askUserToolName` — 在 registry 执行前拦截（阻塞等用户） |
| 元工具注入 | `pkg/agent/tool_defer.go:191` | `appendMetaToolDefs` — ask_user 在 defs 重建后补回 |
| 发卡 | `pkg/agent/ask_user.go:362` | `publishAskQuestion` — `msg.Ask = &bus.AskRequestMeta{...}` + 纯文本兜底 |
| 总线类型 | `pkg/bus/types.go:117` | `AskRequestMeta` 挂在 `OutboundMessage.Ask` |
| pico 序列化 | `pkg/channels/pico/pico.go:471` | `payload[PayloadKeyAsk] = ...` → `message.create` 广播 |
| 前端解析 | `web/frontend/src/features/chat/protocol.ts` | `parseAskFromPayload(payload)` → `msg.ask` |
| block 模型 | `web/frontend/src/chat-pi/turns.ts:59,276` | `PiBlock` union `{type:"ask"}` + buildTurns 分支 |
| 渲染组件 | `web/frontend/src/components/chat/ask-card.tsx` | 步进式问询卡 + seal 三态 |
| store | `web/frontend/src/store/chat.ts:71,97` | `ChatMessage.ask?: AskInfo` |
| 历史回放 | `web/backend/api/session.go:1102` | ask marker 文件读取 → `pending_ask` |

### 2.2 pico 协议（传输层）

- `pkg/channels/pico/protocol.go:66` — `PicoMessage{Type, ID, SessionID, Seq, Timestamp, Payload}`。
- 消息类型常量齐全：`message.create/update/delete`、`turn.status`、`stream.catchup`、seq 环 + 断线重放。
- **payload 组装点**（A2UI 要插的地方）：`pkg/channels/pico/pico.go` 的 `SendMessage`，`msg.Ask` 序列化在 :471，approval 在 :464。
- **progress_note 陷阱**（重要）：`pico.go:87` `outboundMessageIsProgressNote` — 审批/ask 卡**故意不打** progress_note 标记，因为旧客户端（TUI）会把 progress_note 渲染成状态行、把卡从时间线丢掉（注释见 :80-86，review P1-2）。A2UI 卡同理：**不标记**，TUI 走未知 payload 的纯文本降级。
- tool_calls 的既有传输模式：`pkg/agent/agent_outbound.go:254` — `Context.Raw["tool_calls"]` 携带序列化 JSON，pico 侧 `picoToolCallsPayload`（`pico.go:1721`）解出放 payload。`outboundMessageForTurnWithOptions`（`pkg/agent/agent_utils.go:120`）是构造出口。

### 2.3 工具注册与执行（agent 侧）

- 工具接口：`pkg/tools/shared/base.go:10` — `Name()/Description()/Parameters()/Execute(ctx, args) *ToolResult`。
- `ToolResult`：`pkg/tools/shared/result.go:16` — `ForLLM`（进上下文）/ `ForUser`（直接给用户）/ `IsError` / `Silent` 等字段。
- 注册点：`pkg/agent/agent_init.go` — `cfg.Tools.IsToolEnabled("message")` 等开关逐个 `agent.Tools.Register(...)`（:282 messageTool 是最好参照——它注入回调直接发 OutboundMessage）。
- **msgBus 注入模式**：`pkg/tools/cron.go:32,42,869` — CronTool 持有 `*bus.MessageBus`，`PublishOutbound(pubCtx, bus.OutboundMessage{...})` 直接推消息。render_ui 采用同款。
- render_ui 与 ask_user 的**关键差异**：ask_user 需要阻塞等用户答复所以要拦截器；render_ui 是即发即忘（展示型），走普通 registry 工具即可，**不需要**拦截器，更简单。

### 2.4 前端渲染管线（web 侧）

- 技术栈：React 19.2 + Vite + TanStack Router + jotai + react-markdown + radix；测试 vitest（`web/frontend/package.json`）。
- 消息流：`features/chat/protocol.ts`（415 行）按 `message.create/update/delete` 等类型分发 → `store/chat.ts`（`ChatMessage` 结构）→ `chat-pi/turns.ts`（396 行，`buildTurns` 把消息流转成 `PiTurn`/`PiBlock` 数组，approval/ask 整条消息替换为对应卡 block）→ `chat-pi/pi-message.tsx`（按 block type 分发到 `ApprovalCard`/`AskCard`/`StreamMarkdown` 等）。
- `message.update`（`protocol.ts:264-327`）按 message_id 就地更新——二期做流式 updateComponents 时复用。
- 历史回放：`web/backend/api/session.go:589` `sessionTranscriptMessages` 从 session JSONL 组装 transcript；assistant 分支在 :619+。

### 2.5 A2UI npm 包实测事实（npm pack 解包验证）

- `@a2ui/react@0.15.0`：导出 `A2uiSurface`（FC，props 是 **`surface: SurfaceModel`**，不是旧文档的 surfaceId 字符串——a2ui-vue README 的 Quick Start 是 Vue 版写法，别照抄）、`useA2uiSurface` 等。peerDeps：react ^18||^19 ✅、zod ^3.25.76。
- 内部 `prepare` 机制：catalog 里的 React 组件实现会被 `toWebComponent` 转 Web Components（`chunk-H4D2JAR7.js:144-176`），即渲染走 Custom Elements。
- `@a2ui/web_core@0.15.0`：`MessageProcessor`（`src/processing/message-processor.d.ts:96`）— 构造 `(catalogs, actionHandler?, options?)`；`processMessages` 消费消息数组；`getSurfaces()` 返回 surface map；`dispose()` 清理。自带 zod schema 校验（畸形消息在校验层报错，不崩）。
- `basicCatalog`：`src/v0_9/basic_catalog/catalog.js:30` — catalogId = `https://a2ui.org/specification/v0_9/catalogs/basic/catalog.json`。组件清单（`basic_components.d.ts`）：Text/Image/Icon/Video/AudioPlayer/Row/Column/List/**Card**/Tabs/Modal/Divider/Button/TextField/CheckBox/ChoicePicker/Slider/DateTimeInput —— **没有 Chart**。
- **`@a2ui/catalog-vegachart` 未发布**：npm 404（实测）。web_core 里只有测试引用其 spec 路径。→ MVP 图表场景用 Card/Column/List 结构化呈现，真 Vega 图表二期自建。
- v0.9 消息封包 schema（`src/processing/adapters/v0_9.d.ts`）：顶层 union，createSurface 分支必填 `version:"v0.9"|"v0.9.1"` + `createSurface{surfaceId, catalogId, theme?, sendDataModel?}`；components 走 `InternalComponentPayload`（`operations.d.ts:8`）— `{id, component, ...props}` 数组。**注意 schema 是 strict 的**（多余顶层键会被 zod 拒）——工具侧约束模型输出时别让它夹带私货。

## 3. 设计决策与理由

### D1：走工具调用而非正文裸 JSON
模型在正文里裸写 JSON 不可控（截断、夹 markdown 围栏、越写越大）。工具参数有 schema 约束 + 校验点，
失败可向模型报错重试。这也是 A2UI 官方 Agent SDK 的思路（skill 约束 + 校验编译）。

### D2：render_ui 是普通 registry 工具（非拦截器）
与 ask_user 本质不同：ask_user 阻塞等答复，render_ui 即发即忘。走 `agent_init.go` 的常规注册
（`IsToolEnabled("render_ui")` 开关），内部像 CronTool 那样拿 msgBus `PublishOutbound`。
省掉 pipeline 拦截器、pendingAsk 式状态机、超时兜底一整套复杂度。

### D3：`UIMeta.Messages` 用 `json.RawMessage` 原样透传
Go 侧不解析 A2UI 结构。理由：① 协议在快速演进（v0.8/0.9/v1.0 adapter 并存），Go 侧一旦建模型就成
维护负担；② 前端 `MessageProcessor` 自带 zod 校验，是唯一权威消费端；③ 工具侧只做廉价防线
（json.Valid + version 前缀 + createSurface 存在 + 256KB 上限），足以拦截模型明显跑偏。

### D4：payload 键 `a2ui` + 不打 progress_note
照 ask/approval 的 payload 模式挂 `message.create`。不标记 progress_note（TUI 兼容，见 2.2）。
TUI/飞书等渠道看到未知 payload：OutboundMessage.Content 携带纯文本兜底摘要（工具侧生成），
现有渠道逻辑天然显示 Content——**降级路径零新代码**。

### D5：每卡独立 MessageProcessor
多 surface / surfaceId 撞车 / 一 turn 多次 render_ui：每张 A2uiCard 组件实例自建 processor + dispose，
天然隔离，不用全局单例管理 surface 生命周期。

### D6：历史回放 A/B 方案（计划任务 5 留了收口）
- 方案 A（完整）：render_ui 在 session 层留 marker（照 `session.AskMarkerStore` 模式），transcript 读取还原 `a2ui` 字段，刷新后卡片原样重现。
- 方案 B（降级）：接受历史里只看到文本兜底摘要。
A 体验对、侵入大；B 零侵入、体验缩水。让实现者调研 session JSONL 里 tool result 落盘形态后定夺，
别拍脑袋。

## 4. 明确排除项（调研中否决的路线）

| 路线 | 否决原因 |
|---|---|
| 飞书流式卡片直接用 A2UI | 渲染端不受我们控制，协议不兼容（见 §1） |
| `a2ui-vue`（shawnwang15） | picoclaw web 是 React 19；且社区件单人维护、规范快速迭代期 |
| MVP 就做交互回传（表单 submit） | 多打通"组件事件→新消息→agent 上下文"全链路，复杂度高一截；用户同意切二期 |
| MVP 上 VegaChart | 官方包未发 npm（404 实测）；自建 catalog 组件包 Vega-Lite 是独立工作量 |
| agent 正文裸写 A2UI JSON | 不可控、无校验点（见 D1） |
| Go 侧解析 A2UI 结构 | 无必要的前置耦合（见 D3） |
| ask_user 式 pipeline 拦截器 | render_ui 不阻塞，用不上那套状态机（见 D2） |

## 5. 给实现者的复核清单

1. 动手前 `git log --oneline -5` 确认基线仍在 `bfbee3c0` 之后；若 TUI 大改已合入，`cmd/picoclaw/internal/tui/` 的变更与本工作流无关，别混提交。
2. `@a2ui/react` 的 FC props 是 `surface: SurfaceModel`（本文名 2.5，实测 index.d.ts:67-69）；网上旧示例传 surfaceId 字符串会 TS 报错。
3. v0.9 envelope schema 是 **strict** 的——给模型的工具参数 description 里要写死封包格式（计划全局约束有完整示例），并强调顶层只能有 `version` + `createSurface`。
4. basicCatalog 需要注册 universal elements + `injectBasicCatalogStyles()`（入口一次性），漏了会渲染空标签。
5. pico 测试参照 `pkg/channels/pico/pico_test.go` 既有 SendMessage 用例风格；前端参照 `turns.test.ts` / `ask-flow.test.ts`。
6. 提交切分照计划任务 1-6，一任务一 commit；TUI 那批未提交改动（11 个文件）属于另一条工作流，保持原样。
