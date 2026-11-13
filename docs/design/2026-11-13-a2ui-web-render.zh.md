# A2UI Web 渲染（展示型 MVP）— 落地设计文档

> A2UI 三部曲的第三篇：侦察分析（`docs/design/2026-11-13-a2ui-recon-analysis.zh.md`）回答"为什么这么设计"，
> 实现计划（`docs/design/a2ui/2026-11-13-a2ui-web-render-plan.md`）回答"打算做什么"，
> **本文回答"实际落地成什么样"**——所有锚点从已合入代码（commits `873a912e`..`e7f81977`）逐一核对，
> 与计划不符处以本文为准，偏差与理由见 §4。
> 范围：展示型 MVP（看、hover），表单交互回传是明确的二期非目标（决策依据见侦察分析 §0/§4，不重复）。

## 1. 全链路管线（as-built）

```
模型（turn 中调用工具）
  │ messages: A2UI v0.9 消息数组（+可选 summary 兜底文本）
  ▼
render_ui 工具  pkg/tools/render_ui.go:87 Execute
  │ 门1 json.Valid → 门2 封包（version 前缀 v0.9 + createSurface 非空，
  │   renderUIEnvelopeViolation :169）→ 门3 序列化批次 ≤256KB（:21）
  │ 兜底文本推导 renderUITextSummary :226（summary 参数 → 组件树摘要 → 计数行）
  │ 5s 超时 PublishOutbound（fire-and-forget，无拦截器、无状态机）
  ▼
bus.OutboundMessage{ Content: 兜底文本, UI: &UIMeta{Messages: 原样 JSON} }
  │            pkg/bus/types.go:110（字段）、:146（UIMeta 定义，json.RawMessage 透传）
  ▼
pico 通道序列化  pkg/channels/pico/pico.go:490 SendMessage
  │ payload[PayloadKeyA2UI("a2ui")] = 反序列化后的消息数组（protocol.go:59 常量）
  │ message.create 广播；不打 progress_note mark（TUI 兼容，理由见侦察分析 2.2）
  ▼
web 前端（live）
  │ protocol.ts:159 parseA2uiFromPayload（数组且非空才挂，形状不对忽略键）
  │   → store/chat.ts:74 ChatMessage.ui / :111 A2uiInfo
  │   → chat-pi/turns.ts:352 buildTurns push {type:"a2ui"} block（text 块之后）
  │   → chat-pi/pi-message.tsx:192 case "a2ui" → <A2uiCard/>
  │   → components/chat/a2ui-card.tsx:73（渲染细节见 §3）
  ▼
web 前端（刷新/重进——历史回放，无 marker 方案）
  session JSONL 里持久化的 ToolCalls[].Function.Arguments（pipeline 原样写入，不截断）
  │ web/backend/api/session.go:845 visibleAssistantToolMessages 识别 render_ui 调用
  │   → tools.RenderUIHistoryMessages（render_ui.go:195）用与 live 相同的门校验参数
  │   → sessionChatMessage{A2UI, Content: 同款兜底}（session.go:87）
  │ 前端 features/chat/history.ts:68 复用 parseA2uiFromPayload 映射回 ChatMessage.ui
  │   → 与 live 同一条 turns/block/A2uiCard 渲染路径
```

各层落点与计划的对应：任务 1 = bus/pico 三处（原样落地）；任务 2 = `render_ui.go` + `agent_init.go:287`
（`cfg.Tools.IsToolEnabled("render_ui")` 门——无独立配置键，走 `config.go:2473` 的 default 分支即默认开，
msgBus 类型断言注入）；任务 3 = 前端解析三层；
任务 4 = `a2ui-card.tsx` + `main.tsx` 入口初始化；任务 5 = 历史回放（方案有变，见 §4.1）。

## 2. 关键机制（落地形态）

### 2.1 工具侧：三道门 + 兜底 + 静默成功

- **校验只做廉价防线**：`json.Valid` → 每条消息 `version` 以 `"v0.9"` 开头（覆盖 v0.9.1）且 `createSurface`
  为非空对象 → 序列化批次 ≤256KB。Go 侧不解析 A2UI 结构（侦察分析 D3），schema 权威校验在前端 zod 层。
- **成功即静默**：执行成功返回 `ToolResult{ForLLM:"UI rendered: …", Silent:true}`——工具反馈流里不显示
  成功噪音（`render_ui_test.go` TestRenderUIToolValidBatchPublishesUI 锁定）。
- **降级零新代码**：非 pico 渠道（飞书/TUI）看到的 `OutboundMessage.Content` 就是兜底文本
  （summary 参数优先，缺省由组件树生成"组件类型 + 文本属性"逐行摘要），现有渠道逻辑直接显示。
- **发布 5s 超时**（`renderUIPublishTimeout`）：bus 拥塞时不无限占用工具执行。

### 2.2 传输侧：原样透传

`UIMeta.Messages` 是 `json.RawMessage`，pico 侧 `json.Unmarshal` 进 `map[string]any` payload 后原样发出
（pico_test.go TestPicoA2UI_SendCarriesA2UIPayload 锁定）。协议演进（v1.0 adapter 等）只影响前端。

### 2.3 前端：每卡独立 processor + 双层错误兜底

- **每卡独立 `MessageProcessor`**（`a2ui-card.tsx:80` useMemo 内构造，卸载/替换时 `dispose()`）：
  surfaceId 撞车、一 turn 多次 render_ui 天然隔离（侦察分析 D5）。
- **错误双层兜底**：`processMessages` 抛错（strict zod 拒畸形封包）→ 就地降级错误占位文案
  （i18n `chat.a2uiRenderFailed`）；渲染期组件崩溃 → `A2uiRenderBoundary`（error boundary）兜底。
  任何情况下不炸聊天流（a2ui-card.test.tsx 两个用例分别锁定）。
- **basicCatalog 注册在应用入口**：`a2ui-card.tsx:35 initA2uiBasicCatalog()`（注册 universal custom
  elements + `injectBasicCatalogStyles()`，幂等），`main.tsx:16` 调用一次。
- **Text 组件 markdown 接入 app 同一管线**：`MarkdownComponentContext.Provider`（`a2ui-card.tsx:109`）
  提供 app 的 `<Markdown/>`——不提供时 basicCatalog Text 退化为原文 span（@a2ui/react README 推荐做法）。

### 2.4 light DOM 与 dispose 语义（任务 4 实测发现，计划未预见）

- basicCatalog 的 Lit Web Component 渲染进 **light DOM**（非 shadow）：`container.textContent` 能直接
  断言数据文本；custom element 宿主（如 `a2ui-card`、`a2ui-basic-text`）同步出现，但子组件与数据模型
  文本要等 Lit 异步渲染链（card → child text → markdown host → React portal）跨多个微/宏任务轮次——
  测试用轮询 `waitFor` 等待（`a2ui-card.test.tsx:72`），单次 flush 不可靠。
- `message-processor.dispose()` 实测只清 RPC（`rpc.dispose()`），已取出的 surface 模型不受影响——
  卸载时 dispose 与 React StrictMode 重挂载安全（`a2ui-card.tsx:77` 注释记录）。

## 3. v0.9 封包（真实 schema）

以下来自 `@a2ui/web_core@0.15.0` 的 v0_9 adapter（`src/processing/adapters/v0_9.d.ts`）与
MessageProcessor 实测（2026-11-13，node 直跑验证）。

消息是 **strict zod union**，每条消息顶层只允许 `version` + 下列四者之一：

| 分支 | 载荷字段 | 说明 |
|---|---|---|
| `createSurface` | `surfaceId`, `catalogId`, `theme?`, `sendDataModel?` | **不含 components/dataModel**（strict，多余键被拒） |
| `updateComponents` | `surfaceId`, `components: [{id, component, ...props, child\|childIds?}]` | 组件树唯一入口 |
| `updateDataModel` | `surfaceId`, `path?`, `value?` | 数据模型（组件经 `{path:"/title"}` 绑定） |
| `deleteSurface` | `surfaceId` | 删面 |

`version` 枚举 `"v0.9" | "v0.9.1"`。可渲染的最小完整示例（卡片 + 列表 + 数据绑定文本；
已用真实 `MessageProcessor` + basicCatalog 实测通过）：

```jsonc
[
  { "version": "v0.9",
    "createSurface": { "surfaceId": "report-2026q", "catalogId": "https://a2ui.org/specification/v0_9/catalogs/basic/catalog.json" } },
  { "version": "v0.9",
    "updateComponents": { "surfaceId": "report-2026q", "components": [
      { "id": "card", "component": "Card", "child": "list" },
      { "id": "list", "component": "List", "children": ["t1", "t2"] },
      { "id": "t1",   "component": "Text", "text": { "path": "/rows/0" } },
      { "id": "t2",   "component": "Text", "text": { "path": "/rows/1" } }
    ] } },
  { "version": "v0.9",
    "updateDataModel": { "surfaceId": "report-2026q", "path": "/", "value": { "rows": ["2026 Q1：128 人", "2026 Q2：157 人"] } } }
]
```

组件 props 也是 strict 的（实测：Card 带 `label` 被拒 `Unrecognized key(s)`）。常用组件的
**真实** props（从 basicCatalog 运行时 schema 读出，非文档臆测）：`Card{child}`（单子引用）、
`List{children, direction, align}` / `Column{children, justify, align}`（子 id 数组）、
`Text{text, variant, weight}`（text 支持字面量或 `{path}` 数据绑定）；通用还有 `weight`/`accessibility`。
basicCatalog 组件清单：Text/Image/Icon/Video/AudioPlayer/Row/Column/List/Card/Tabs/Modal/Divider/
Button/TextField/CheckBox/ChoicePicker/Slider/DateTimeInput（无 Chart，图表二期，见 §6）。

### 3.1 已知缺陷：工具参数描述与 schema 不符（待修复）

`pkg/tools/render_ui.go:72-75` 的 messages 参数 description 教模型产出
`createSurface:{..., "components":[...], "dataModel":{}}` 的**内联形态**——该形态会被 strict zod 拒绝
（实测报 `Unrecognized key(s) in object: 'components', 'dataModel'`），前端渲染错误占位卡。
正确形态是上面的分帧三消息。Go 侧三门不校验 components 位置，所以工具会"成功"发布一条渲染必败的
批次。**影响**：模型严格照抄工具描述时卡片渲染失败（降级占位文案，不崩）；模型自用分帧形态则正常。
**修复面**：仅 `render_ui.go` 的 description 字符串改为分帧示例（`TestRenderUIToolSchemaContract`
只断言描述含 `{"version":"v0.9","createSurface":` 前缀，不锁定错误形态，无需改测试）。
根因：任务 2 先落地时抄了计划全局约束里的（不准确）示例，任务 4 TDD 发现真实 schema 时未回改工具描述。

## 4. 实现与计划的偏差

### 4.1 任务 5：历史回放选了"无 marker 提取"（计划 A/B 的第三条路）

计划给的 A（marker 文件，照 AskMarkerStore）与 B（接受降级为文本兜底）都未采用。实际方案：
pipeline 本就把完整工具参数原样持久化进 `ToolCalls[].Function.Arguments`（不截断），A2UI 载荷
**本来就在 session JSONL 里**——`web/backend/api/session.go:845` 对 render_ui 调用经
`tools.RenderUIHistoryMessages`（与 live 完全相同的门 + 兜底推导）提取，产出携带 `a2ui` 字段的
assistant 消息。弃 B 的实证理由：刷新后连文本兜底都不在——OutboundMessage.UI 是 fire-and-forget
不落盘，只剩 toolCall block 里按 `toolFeedbackMaxArgsLength` 截断的原始 args JSON。弃 marker 的理由：
需给 bus-only 的 RenderUITool 注入 session store、新增 marker 文件类型、N 卡/会话的消息关联复杂度
远超 ask 的单 pending 模型。**已知局限（接受）**：提取基于调用参数而非执行结果，被 hook/turn-profile
拒绝的 render_ui 调用（live 未渲染）历史中也会重现卡；纯展示工具正常配置不走拒绝路径。

### 4.2 其余偏差（小）

| 项 | 计划 | 实际 | 说明 |
|---|---|---|---|
| 依赖导入路径 | `@a2ui/react` 根导出 | `@a2ui/react/v0_9`、`@a2ui/web_core/v0_9`（子路径导出） | 0.15.0 按协议版本分子路径，类型更精确 |
| catalog 初始化位置 | index.html 或入口 | `main.tsx:16` 调 `initA2uiBasicCatalog()`（a2ui-card.tsx 导出） | 幂等；入口一次 |
| 工具成功反馈 | 未提 | `Silent: true` | 成功不进工具反馈流，避免噪音 |
| i18n | 未提 | `chat.a2uiTitle`/`chat.a2uiRenderFailed` 五语言（zh/en/bn-in/cs/pt-br） | 卡片标题与错误占位 |
| 兜底 Content | 未提推导细节 | summary 参数 → 组件树逐行摘要 → `Interactive UI (N message(s))` | live 与历史回放同一路推导，逐字节一致 |
| 封包示例 | 全局约束给的是内联形态 | 实际 schema 只收分帧形态 | 即 §3.1 缺陷，唯一待修复项 |

## 5. 测试与验证状态

新增测试（按任务）：

- Go：`pkg/channels/pico/pico_test.go` TestPicoA2UI_SendCarriesA2UIPayload；
  `pkg/tools/render_ui_test.go` TestRenderUIToolValidBatchPublishesUI / …SummaryParamWinsAsFallbackContent /
  …InvalidBatchRejected / …OversizedBatchRejected / …SchemaContract / TestRenderUIHistoryMessages；
  `web/backend/api/session_test.go` TestSessionA2UIHistoryReplay / TestSessionA2UIHistoryReplayInvalidBatchNotResurfaced（任务 5）。
- 前端：`turns.test.ts` "buildTurns a2ui block" 3 用例；`protocol.test.ts` parseA2uiFromPayload 2 用例；
  `a2ui-card.test.tsx` 2 用例（合法 surface 渲染出 custom element + 数据文本；畸形封包渲染占位不抛）。

全量回归（2026-11-13，本机 Windows）：`go test ./pkg/... ./web/backend/...` 71 包 ok；失败集与
A2UI 合入前基线（31cc8bde worktree 复跑）**逐项一致**（pkg/tools 的 TestShellTool_*/TestWindows_*
9 个环境性失败 + asr/deltachat/isolation/migrate/pid 的 Unix 假设失败 + matrix/gateway 的本机
cgo 构建失败）——差集为空，**无回归**。前端 vitest 75/75 通过；`pnpm build`（tsc + vite）通过，
产物含 basicCatalog custom elements。

**待用户手测**（涉及真实模型与多渠道环境，本文不代测）：

1. pico web 会话中让模型"用 render_ui 画一张 2026 年季度体检人数表格"——流式中卡片出现
   （注意 §3.1 缺陷：模型若照抄工具描述会得错误占位卡，手测可顺带确认修复前行为）。
2. 刷新页面——卡片仍在（历史回放路径）。
3. 飞书渠道同会话调用 render_ui——显示纯文本兜底（Content），无 JSON 原文泄漏。

## 6. 二期范围（本 MVP 明确不做）

- **交互回传**：`sendDataModel` / `onAction` → 组件事件组装成新消息 → agent 上下文的闭环
  （Button/TextField/CheckBox 等交互组件当前渲染但点击无回路）。
- **VegaChart 图表**：`@a2ui/catalog-vegachart` 未发 npm（侦察实测 404），需自建 React catalog
  组件包 Vega-Lite；体检报告"真图表"场景依赖此项。
- **表单 submit**：多字段表单一次性提交（依赖交互回传）。
- **流式增量**：`updateComponents` 分帧到达的流式渲染（当前一批消息同步消费；协议本身已支持分帧，
  前端管线无需大改，主要是 turn 流式语义的产品决策）。
