# A2UI Web 渲染（展示型 MVP）实现计划

> **面向 AI 代理的工作者：** 必需子技能：使用 subagent-driven-development（推荐）或 executing-plans 逐任务实现此计划。步骤使用复选框（`- [ ]`）语法来跟踪进度。

**目标：** agent 在回复中途调用 `render_ui` 工具产出 A2UI JSON，pico web 聊天流里即时渲染出可交互的结构化 UI（图表 / 工具结果卡片）。MVP 只做展示型（看、hover），不做表单数据回传（二期）。

**架构：** 完全复用 ask_user 既有管线模式：新工具 `render_ui`（`pkg/tools/`，通过 msgBus 直接 `PublishOutbound`）→ `bus.OutboundMessage` 新增 `UI *UIMeta` 字段 → pico 通道把它序列化为 `message.create` 的 `payload["a2ui"]` → web 前端 `protocol.ts` 解析出 `ChatMessage.ui` → `turns.ts` 生成 `a2ui` block → 新组件 `a2ui-card.tsx` 用官方 `@a2ui/react` 的 `A2uiSurface` 渲染。历史回放走 `web/backend/api/session.go` 的 transcript，`sessionChatMessage` 透传 `a2ui` 字段。

**技术栈：** Go（后端工具 + pico 协议）、React 19 + `@a2ui/react@0.15` + `@a2ui/web_core@0.15`（A2UI v0.9 规范、basicCatalog Web Components）、Vitest。

**规格：** 本文件即规格（brainstorming 结论：展示型 MVP，两个场景 = Vega 图表 + 工具结果结构化卡片；交互回传明确非目标）。A2UI 协议侧事实见「全局约束」。

## 全局约束

- A2UI 规范版本：**v0.9**（`@a2ui/react@0.15` 导出 `A2uiSurface` 接收 `SurfaceModel`；`@a2ui/web_core@0.15` 的 `MessageProcessor` + `basicCatalog` 挂 `https://a2ui.org/specification/v0_9/catalogs/basic/catalog.json`）。
- `@a2ui/catalog-vegachart` **未发布到 npm**（404）——图表用 basicCatalog 的 Card/Column/Text 组件以表格/结构化卡片呈现数据；图表能力二期再评估（自建 React catalog 组件包 Vega-Lite）。
- agent 产出消息封包（v0.9 adapter）：`{"version":"v0.9","createSurface":{"surfaceId":"...","catalogId":"https://a2ui.org/specification/v0_9/catalogs/basic/catalog.json","components":[...],"dataModel":{...}}}`，`components` 为 `[{id, component, ...props, childIds?}]` 数组（InternalComponentPayload）。
- 工具命名遵循现有 snake_case：`render_ui`；工具定义进 registry（`agent.Tools.Register`），走 `cfg.Tools.IsToolEnabled("render_ui")` 开关，默认开启。
- pico payload 键名：`a2ui`（新 `PayloadKeyA2UI = "a2ui"`）；**不要**给这条消息打 progress_note mark——与 ask/approval 卡同理，旧客户端（TUI）会把 progress_note 渲染成状态行丢掉卡片；TUI 端看到未知 payload 时按现有降级路径显示纯文本兜底即可。
- 前端依赖安装用 pnpm（仓库用 `pnpm@10.33.0`）；`@a2ui/react` peerDeps 支持 react 19。
- 每个任务 TDD：先写失败测试，再实现，跑通后 commit。Go 测试 `go test ./pkg/...`，前端测试 `pnpm test`（vitest）。
- 所有新代码注释风格与仓库一致（中文注释 + 借鉴出处标注风格）。

## 审查重点（Review Focus）

1. **超大 A2UI JSON**：模型可能产出几百 KB 的 components——工具侧必须有大小上限（拒绝超限），前端的 `processor.processMessages` 由 zod 校验兜底，但工具侧先拦截。测试：超限 JSON 返回错误 ForLLM。
2. **非法 A2UI JSON**：`json.Valid` + 每条消息必须含 `createSurface` 的最小校验在工具侧完成；前端 zod 校验失败时渲染错误占位卡，不能崩整个聊天流。测试：畸形 JSON → 工具拒绝；前端 error boundary。
3. **历史回放一致性**：刷新页面后 A2UI 卡必须仍在（session transcript 透传）。测试：session.go transcript 含 `a2ui` 字段。
4. **多 surface / 重复 surfaceId**：同一 turn 多次调用 render_ui 或 surfaceId 撞车——前端按 message 维度各建独立 processor 实例（每卡一个 MessageProcessor），天然隔离。测试：两条 a2ui 消息渲染两张卡。
5. **非 pico 渠道降级**：render_ui 在飞书/TUI 等渠道调用时，用户看到的必须是可读的纯文本兜底（basicCatalog 组件树的文字摘要），不能静默吞掉。测试：非 pico 渠道时 OutboundMessage.Content 非空。

---

### 任务 1：Go 侧 `UIMeta` 类型与 pico payload 序列化

**文件：**
- 修改：`pkg/bus/types.go`（`OutboundMessage` 加 `UI *UIMeta`；新类型 `UIMeta{Messages json.RawMessage}` — 原样承载 A2UI 消息数组）
- 修改：`pkg/channels/pico/protocol.go`（`PayloadKeyA2UI = "a2ui"` 常量）
- 修改：`pkg/channels/pico/pico.go`（`SendMessage` 的 payload 组装处，ask 块之后加：`if msg.UI != nil { payload[PayloadKeyA2UI] = json.RawMessage(msg.UI.Messages) }`——注意现有 payload 是 `map[string]any`，需 `var v any; json.Unmarshal(msg.UI.Messages, &v)` 后放入）
- 测试：`pkg/channels/pico/pico_test.go` 追加用例

- [ ] **步骤 1：编写失败的测试** — 现有 SendMessage 测试风格，构造 `bus.OutboundMessage{UI: &bus.UIMeta{Messages: json.RawMessage(`[{"version":"v0.9","createSurface":{"surfaceId":"s1","catalogId":"basic","components":[]}}]`)}}`，断言广播出的 `message.create` payload 里 `payload["a2ui"]` 反序列化后是长度 1 的数组且含 `createSurface.surfaceId == "s1"`。
- [ ] **步骤 2：运行测试验证失败** — 运行 `go test ./pkg/channels/pico/ -run TestPicoA2UI`，预期编译失败（UIMeta 未定义）。
- [ ] **步骤 3：实现** — 类型与序列化按上述文件清单；`UIMeta.Messages` 保持 `json.RawMessage` 原样透传，不做任何结构解析（协议演进由前端 zod 层兜）。
- [ ] **步骤 4：运行测试验证通过** — `go test ./pkg/channels/pico/`，预期 PASS。
- [ ] **步骤 5：Commit** — `git commit -m "feat(pico): A2UI payload 透传（bus.UIMeta + payload[a2ui]）"`

### 任务 2：`render_ui` 工具

**文件：**
- 创建：`pkg/tools/render_ui.go`
- 测试：`pkg/tools/render_ui_test.go`
- 修改：`pkg/agent/agent_init.go`（按 `cfg.Tools.IsToolEnabled("render_ui")` 注册；构造时注入 `*bus.MessageBus`，参照 `pkg/tools/cron.go:32,42` 的 msgBus 注入模式）

- [ ] **步骤 1：编写失败的测试** — 用例一：合法 A2UI JSON → 返回 ForLLM 成功提示，且向注入的 mock bus 发布了一条 `OutboundMessage`，其 `UI.Messages` 与输入一致、`Content` 非空（纯文本兜底摘要）；用例二：非法 JSON（非合法 JSON / 缺 createSurface）→ `result.IsError == true`，ForLLM 含错误说明，无消息发布；用例三：超过大小上限（256KB）→ IsError。工具签名：`Name()=="render_ui"`、`Parameters()` 含 `messages`（array of object，description 写明 v0.9 封包格式与 createSurface 必填）、可选 `summary`（string，纯文本兜底）。
- [ ] **步骤 2：运行测试验证失败** — `go test ./pkg/tools/ -run TestRenderUI`，预期编译失败。
- [ ] **步骤 3：实现** — 校验顺序：`json.Valid` → 每条消息 `version` 字段以 `"v0.9"` 开头且含 `createSurface` → 大小上限。纯文本兜底 Content：优先取 `summary` 参数，缺省时从 components 树生成文字摘要（组件类型名 + 文本属性逐行列出）。
- [ ] **步骤 4：运行测试验证通过** — `go test ./pkg/tools/`，预期 PASS。
- [ ] **步骤 5：Commit** — `git commit -m "feat(tools): render_ui 工具——agent 产出 A2UI v0.9 JSON 到聊天流"`

### 任务 3：前端协议解析与 block 模型

**文件：**
- 修改：`web/frontend/src/store/chat.ts`（`ChatMessage` 加 `ui?: A2uiInfo`；`export interface A2uiInfo { messages: unknown[] }`）
- 修改：`web/frontend/src/features/chat/protocol.ts`（`message.create` 分支解析 `payload.a2ui`，仅当 payload 显式带该键时挂到消息上；ask 解析之后照抄模式）
- 修改：`web/frontend/src/chat-pi/turns.ts`（`PiBlock` union 加 `{ type: "a2ui"; id: string; ui: A2uiInfo }`；buildTurns 里 `message.ui` 存在时 push 该 block，位置在 text 块之后）
- 测试：`web/frontend/src/chat-pi/turns.test.ts` 追加用例

- [ ] **步骤 1：编写失败的测试** — turns.test.ts 新用例：含 `ui` 的 assistant 消息 buildTurns 后产出 `{type:"a2ui"}` block；不含 ui 的消息不受影响（回归）。
- [ ] **步骤 2：运行测试验证失败** — `cd web/frontend && pnpm vitest run src/chat-pi/turns.test.ts`，预期 FAIL。
- [ ] **步骤 3：实现** — 按文件清单；protocol.ts 解析时对 `payload.a2ui` 做最小形状检查（数组且非空），形状不对就忽略该键（不挂 ui），让消息按普通 markdown 渲染。
- [ ] **步骤 4：运行测试验证通过** — `pnpm vitest run src/chat-pi/turns.test.ts src/features/chat/protocol.test.ts`，预期 PASS。
- [ ] **步骤 5：Commit** — `git commit -m "feat(web): 协议解析 a2ui payload → a2ui block"`

### 任务 4：`@a2ui/react` 集成与 A2uiCard 组件

**文件：**
- 修改：`web/frontend/package.json`（dependencies 加 `"@a2ui/react": "^0.15.0"`、`"@a2ui/web_core": "^0.15.0"`、`"zod": "^3.25.76"`）
- 创建：`web/frontend/src/components/chat/a2ui-card.tsx`
- 修改：`web/frontend/src/chat-pi/pi-message.tsx`（block 渲染 switch 加 `case "a2ui"`，与 ApprovalCard/AskCard 同级）
- 修改：`web/frontend/index.html` 或入口（加载 `@a2ui/web_core/v0_9/basic_catalog` 的 universal elements 注册 + `injectBasicCatalogStyles()`，一次即可）
- 测试：`web/frontend/src/components/chat/a2ui-card.test.tsx`

- [ ] **步骤 1：安装依赖** — `cd web/frontend && pnpm add @a2ui/react@^0.15.0 @a2ui/web_core@^0.15.0 zod@^3.25.76`，预期安装成功无 peer 冲突（react 19 在支持范围）。
- [ ] **步骤 2：编写失败的测试** — vitest（jsdom）用例：渲染 `<A2uiCard messages={[合法 v0.9 createSurface 消息，含 Card+Text 组件]} />`，断言容器内出现 basicCatalog 的 custom element 标签（如 `a2ui-card` / 组件标签）；用例二：畸形 messages → 渲染错误占位文案而非抛异常。
- [ ] **步骤 3：实现 A2uiCard** — 组件内部：`useMemo(() => new MessageProcessor([basicCatalog], undefined, {version:"v0.9"}), [])`（组件卸载时 `dispose()`）；`processor.processMessages(props.messages)`；`const surfaces = processor.getSurfaces()`；对每个 surface 渲染 `<A2uiSurface surface={model} />`（0.15 的 FC 签名是 `surface: SurfaceModel`，非 surfaceId 字符串）。外层包 React error boundary + zod 层失败的错误占位。样式：容器加最大宽度与卡片边框，融入现有聊天流视觉（参照 ask-card 的容器样式）。
- [ ] **步骤 4：运行测试验证通过** — `pnpm vitest run src/components/chat/a2ui-card.test.tsx`，预期 PASS。
- [ ] **步骤 5：Commit** — `git commit -m "feat(web): A2uiCard——聊天流内渲染 A2UI v0.9 surface"`

### 任务 5：历史回放透传

**文件：**
- 修改：`web/backend/api/session.go`（`sessionChatMessage` 结构加 `A2UI json.RawMessage \`json:"a2ui,omitempty"\``；`sessionTranscriptMessages` 的 assistant 分支透传——transcript 的来源是 session JSONL 里的消息记录，render_ui 工具执行时需同步把 A2UI 消息写入 session 历史：在 `pkg/agent/pipeline_execute_loop.go` 的 `handleToolResult` 或工具 result 持久化路径上，让 render_ui 的执行除 PublishOutbound 外还在 ForLLM/持久层留痕）

> 实现说明（选择余地收口）：session 历史里 assistant 消息携带 A2UI 的最小侵入做法 = render_ui 工具把 `messages` JSON 原样存入一个 session 层 metadata（参照 ask marker 的 session.AskMarkerStore 模式新建 `render_ui` marker），session.go transcript 读取该 marker 并填进对应 assistant 消息的 `a2ui` 字段。若调研发现 tool result 消息本就持久化 ForLLM 文本且前端已渲染 toolCall block，则可接受更简单的替代：历史回放时 A2UI 卡降级为文本摘要（Content 兜底），把本任务缩为「确认降级路径可用 + 测试」。实现者二选一，在 PR 描述里写明选择与理由。

- [ ] **步骤 1：调研 + 编写失败的测试** — 先确认 session JSONL 持久化路径（`pkg/session/`）里 tool result 的落盘形态，再定方案；session_test.go 加用例：含 render_ui 的会话，transcript API 返回的消息带 `a2ui`（方案 A）或带非空文本兜底（方案 B）。
- [ ] **步骤 2：运行测试验证失败** — `go test ./web/backend/api/ -run TestSessionA2UI`，预期 FAIL。
- [ ] **步骤 3：实现** — 按选定方案。
- [ ] **步骤 4：运行测试验证通过** — `go test ./web/backend/api/`，预期 PASS。
- [ ] **步骤 5：Commit** — `git commit -m "feat(session): A2UI 卡历史回放透传"`

### 任务 6：端到端验证 + 文档

**文件：**
- 修改：`docs/design/2026-11-13-a2ui-web-render.zh.md`（新建设计文档：管线图、v0.9 封包示例、二期范围——交互回传 / VegaChart catalog / 表单 submit）
- 修改：`CHANGELOG.md`（0.5.7 条目预留）

- [ ] **步骤 1：全量测试** — `go test ./pkg/... ./web/backend/...` 与 `cd web/frontend && pnpm test && pnpm build`（`build:backend` 一并过 tsc），预期全绿。
- [ ] **步骤 2：手工端到端** — 启动 gateway + web，让模型对「用 render_ui 画一张 2026 年季度体检人数表格」出图；验证：流式中卡片出现、刷新后仍在（或按任务 5 方案降级）、飞书渠道同会话调用 render_ui 显示文本兜底。
- [ ] **步骤 3：写设计文档 + CHANGELOG，Commit** — `git commit -m "docs: A2UI web 渲染设计文档与发布注记"`

---

## 二期范围（本计划明确不做）

- 表单交互回传：`sendDataModel` / `onAction` → 新消息 → agent 上下文的闭环
- VegaChart 图表：`@a2ui/catalog-vegachart` 未上 npm，需自建 React catalog 组件包 Vega-Lite
- 流式增量更新（`updateComponents` 分帧到达）——MVP 单帧 createSurface
