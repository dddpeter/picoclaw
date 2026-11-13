# A2UI 二期侦察文档（交互回传 / 流式增量 / VegaChart）

> 一期（展示型 MVP）已上线：`render_ui` 工具 → `bus.OutboundMessage.UI` → pico `payload["a2ui"]` →
> 前端 `A2uiCard` 渲染。本文回答二期三件事的代码库与依赖库事实：① 交互回传（按钮/表单 → 动作/数据
> 回传 agent）② 流式增量更新（updateComponents 分帧热更新已有卡）③ VegaChart 图表（自建 React
> catalog 组件封装 Vega-Lite）。
> 每条结论带 `file:line` 锚点；标注【实测】（读了 node_modules 源码 / npm 命令验证）或【推断】。
> 前文姊妹篇：`docs/design/2026-11-13-a2ui-recon-analysis.zh.md`（一期设计依据）、
> `docs/design/a2ui/2026-11-13-a2ui-web-render-plan.md`（一期计划）。
> 调研时间：2026-11-14；基线 commit：`0132d61c`。

## A. `@a2ui/react@0.15` / `@a2ui/web_core@0.15` 的动作与表单 API（node_modules 实证）

### A1. `A2uiSurface` 的 props——没有 onAction，回调挂在 SurfaceModel 上

【实测】`web/frontend/node_modules/@a2ui/react/index.d.ts:67-69`：

```ts
declare const A2uiSurface: react__default.FC<{
    surface: SurfaceModel<ReactCatalogComponent>;
}>;
```

props 只有 `surface` 一个。**没有任何 onAction/事件回调 props**（全文件导出清单
`index.d.ts:140`：`A2uiSurface`、`MarkdownComponentContext`、`createComponentImplementation`、
`createBinderlessComponentImplementation`、`useA2uiSurface`、`useNodeView`、`useSignalValue`、
`useMarkdownRenderer`、两个 Markdown context——仅此而已）。

交互回传的接法有**两处**，任选其一（可同时用）：

1. **构造时**：`MessageProcessor` 第二参 `actionHandler`（见 A2）——全局兜底，覆盖所有 surface。
2. **取出后**：`SurfaceModel.onAction: EventSource<ActionPayload>`（`web_core/src/state/surface-model.d.ts:93`），
   `surface.onAction.subscribe(listener)` 返回 `Subscription`（`.unsubscribe()` 清理，
   `web_core/src/common/events.d.ts:1-22`）。`getSurfaces()` 拿到 surface 后订阅即可。
   一期 `A2uiCard` 的 `useMemo` 里已经取出 `surfaces` 数组（`a2ui-card.tsx:80-99`），
   在那里 subscribe + cleanup 是最小改动。

README 里的 `<A2UIProvider onAction={...}>` 是 **v0.8 legacy API**
（`@a2ui/react/README.md:296-318`，"Legacy Support (v0.8)" 一节，从 `@a2ui/react/v0_8` 导入），
v0.9 不适用——别照抄。

### A2. `MessageProcessor` 第二参 `actionHandler` 的签名与调用时机

【实测】`web_core/src/processing/message-processor.d.ts:120`：

```ts
constructor(catalogs: Catalog<any>[], actionHandler?: ActionListener | undefined, options?: MessageProcessorOptions);
```

- `ActionListener = (action: ActionPayload) => void | Promise<void>`
  （`web_core/src/state/surface-model.d.ts:62`）。
- 实现里构造函数直接把 handler 订阅到 SurfaceGroupModel 级的 onAction
  （`message-processor.js:74-76`：`if (this.actionHandler) { this.model.onAction.subscribe(this.actionHandler); }`），
  即**所有 surface 的所有 action 都会经过它**。
- `options`（`message-processor.d.ts:73-88`）里与回传相关的还有：
  `outboundListener`（RPC 出站监听，`callAgentFunction` 必需）、`errorHandler`（结构化错误）、
  `defaultTimeoutMs`（RPC 超时，默认 30000）。

**Button 点击时收到什么**【实测】`ActionPayload` 完整形状
（`web_core/src/state/surface-model.d.ts:8-30`）：

```ts
interface ActionPayload {
    name: string;                    // action 名（Button.action.event.name）
    surfaceId: string;               // 发起 action 的 surface
    sourceComponentId: string;       // 触发 action 的组件 id
    timestamp: string;               // ISO 8601，dispatch 时生成
    context: Record<string, unknown>;// action 携带的上下文参数（path 绑定在此刻解析）
    catalogId?: string;              // 显式指定 catalog 时才有
    userMessage?: string;
    metadata?: A2uiMetadata;
    [key: string]: unknown;
}
```

派发实现 `surface-model.js:236-274`（`dispatchAction`）：name/context 从组件 props 解析，
timestamp 现生成，`catalogId`/`userMessage`/`metadata` 仅在显式给出时才带。
同一测试佐证：`generic-binder.test.js:399-407` 断言 `{event:{name:'submit'}}` 点击后
`onAction` 收到回调。

### A3. 交互组件的值进出 dataModel：`{path}` 双向绑定 + Button action 分流

【实测】**值组件的 schema 全部是 `{path}` 绑定**（v0.9 adapter 语义）：

- `TextFieldApi`：`value?: string | {path: string} | FunctionCall`
  （`web_core/src/v0_9/basic_catalog/components/basic_components.d.ts:1119-1125`；任务提示的
  :1059 即 `TextFieldApi` 起点）。
- `CheckBoxApi` :1179 起、`ChoicePickerApi` :1293 起、`SliderApi` :1448 起、
  `DateTimeInputApi` :1568 起——同为 Dynamic 值绑定。
- 写回机制【实测】`generic-binder.js:500-517`（`bindObject` 第 2 步）：**每个 DYNAMIC 属性
  自动合成 `set<Value>` setter**（属性 `value` → `props.setValue`），setter 调
  `this.context.dataContext.set(pathVal, newValue)` 写进 surface 的 dataModel。
  组件实现侧逐个验证：`TextField.js:25`（`onInput → props.setValue?.(...)`）、
  `CheckBox.js:34`、`ChoicePicker.js:44`、`Slider.js:40`、`DateTimeInput.js:84-101`。
  即：**用户在输入框打字 → 立即写入 dataModel 的对应 JSON Pointer 路径**（本地，不自动上行）。
  README 对此的官方表述："Declarative two-way binding with injected setters"
  （`@a2ui/react/README.md` v0.8 vs v0.9 对照表）与 "Two-Way Binding ... adds
  `props.setUsername(val: string)` which updates the underlying data model"（:171-176）。

【实测】**Button 的 action 是二选一 union**（`basic_components.d.ts:976-1003`）：

```
action: { event: { name: string; context?: Record<string, DynamicValue> } }
      | { functionCall: FunctionCall }
```

点击分流【实测】：
- 点击处理：`universal/basic_catalog/components/Button.js:37-41`——`@click` 调 `props.action?.()`
  （generic binder 把 action prop 解析成闭包）。
- 闭包实现：`resolution/generic-binder.js:350-378`（`bindAction`）——
  `functionCall` / `{call}` 形状 → `dataContext.resolveAction(...)` **本地执行 catalog 函数，
  不发 onAction**（测试 `generic-binder.test.js:381-409` 明确断言 `dispatchedAction === null`）；
  `{event:{name, context}}` → `resolveAction` 解析深层 context 绑定（此刻从 dataModel 收集表单值）
  → `context.dispatchAction(resolved)` → `surface.dispatchAction` → `onAction` 事件 → handler。
- **这就是官方"表单提交"模式**【实测佐证】`@a2ui/react/README.md:187`：
  "Action Context: Action properties are resolved into ready-to-call functions. When called,
  they automatically resolve their deep context bindings (e.g., gathering form data from the
  model) before notifying the server."——Button 的 `event.context` 里放
  `{"path": "/form/field"}` 绑定，点击瞬间 binder 把 dataModel 当前值填进 ActionPayload.context。

### A4. `sendDataModel` 的作用——只标记，不自动发送；上行由渲染器应用拉取

【实测】`createSurface` 分支的 `sendDataModel` 布尔（v0.9 schema `adapters/v0_9.d.ts:14`，
默认 false——`message-processor.js:759`：`sendDataModel ?? false`），存为
`SurfaceModel.sendDataModel`（`surface-model.d.ts:121`，注释 "Whether the surface sends its
full data model back with actions"）。

但**web_core 不会自己把 dataModel 发给任何"agent 侧"**【实测】全库 grep `sendDataModel`
（`src/` 下非测试代码）：该 flag 唯一消费点是
`MessageProcessor.getRendererDataModel(version?)`（`message-processor.js:246-296`）：
过滤出 `s.sendDataModel === true` 的 surface，输出
`{[surfaceId]: surface.dataModel.get('/')}`（v0.9 出站形状
`A2uiClientDataModel = {version, surfaces}`，`v0_9/schema/client-to-server.d.ts:175-185`）。
**发送动作、通道、消息类型全部是渲染器应用（我们）的职责**——正是二期要做的那条上行链。

### A5. v0.9 客户端→agent 消息的官方形状（上行协议选型依据）

【实测】`web_core/src/v0_9/schema/client-to-server.d.ts` 定义了 renderer→agent 的官方出站形状：

- `A2uiClientAction`（:2-21）：`{name, surfaceId, sourceComponentId, timestamp, context}`。
- `A2uiClientError`（:22-79）：`VALIDATION_FAILED`（含 surfaceId/path/message）或通用 error。
- `A2uiClientMessage`（:80-174）：`{version: "v0.9"|"v0.9.1"} & ({action} | {error})`。
- `A2uiClientDataModel`（:175-185）：`{version, surfaces: Record<surfaceId, Record<path, any>>}`。

v1.0 还有 RPC 通道【实测】`rpc/rpc-handler.d.ts:11`：`OutboundMessageListener` 收
`RendererFunctionResponseMessage | CallAgentFunctionMessage`；`callAgentFunction(surfaceId,
call, options)`（:85）让渲染器远程调用 agent 侧函数并等待返回（Promise + 超时）。
一期只发 v0.9 版本封包，二期若沿用 v0.9 则 action/dataModel 是两个独立上行概念；
RPC（callAgentFunction）是 v1.0 能力，v0.9 surface 不用。

### A6. "表单提交"官方模式小结（问题 4）

【实测】没有名为 "form submit" 的专用消息；官方模式就是上面三件套的组合：

1. 交互组件值 → `{path}` 绑定实时写入本地 dataModel（A3）。
2. Button `action: {event: {name, context: {…path 绑定}}}` → 点击时 binder 从 dataModel
   收集当前值 → `ActionPayload.context` → `actionHandler`/`onAction`（A3）。
3. surface 建 `sendDataModel: true` 时，渲染器可在 action 之外用
   `getRendererDataModel()` 把整个 dataModel 拍快照随上行带上（A4/A5）。

web_core README/tests 里再无其他"表单提交"协议原语；v1.0 的 callAgentFunction RPC 是
另一条（v0.9 surface 用不了）。

## B. web 前端 → 网关的发送路径（问题 5/6）

### B1. 今天唯一的上行：`message.send`

【实测】全前端只有一个 `socket.send` 调用点：
`web/frontend/src/features/chat/controller.ts:391-397`（`sendChatMessage` 内）：

```ts
socket.send(JSON.stringify({
  type: "message.send",
  id,                                  // "msg-<n>-<ts>"，幂等键
  payload: { content: normalizedContent, media: [...image urls] },
}))
```

- WS 端点：`controller.ts:142-145`——`wss://<host>/pico/ws?session_id=<id>`。
- pico 客户端→服务端方向今天只有两种 type 会被网关受理【实测】`pkg/channels/pico/pico.go:1414-1431`
  （`handleMessage`）：`ping`、`message.send`、`media.send`；其它 type 一律回
  `unknown message type` 错误。前端实际只发 `message.send`（`media.send` 无调用点）。
- 发送侧触发点【实测】：
  - 聊天输入框：`web/frontend/src/components/chat/chat-page.tsx:211,334`（还有 :528/:531 的
    `/stop`、`/context` 也是 `sendMessage({content})`）；
  - ask 卡答复：`web/frontend/src/components/chat/ask-card.tsx:61` ——
    **ask 答复就是一条普通 `message.send` 纯文本**（不是专用消息类型）；
  - 审批卡：`web/frontend/src/components/chat/approval-card.tsx:94`（发送审批命令文本）。
  - hook 层透出：`web/frontend/src/hooks/use-pico-chat.ts:29`（`sendMessage: sendChatMessage`）。
- turn 活跃时发送照常（前端标记 steering 徽标）【实测】`controller.ts:355-357`：
  "A message sent while a turn is already active is steering: it gets queued by the agent"。

**二期回传可以照抄 ask 模式：ActionPayload 序列化成一条 `message.send` 文本/结构化 payload，
不需要新 WS 消息类型**——但 payload 里塞什么（`content` 只有 string + media 数组）见 §风险。

## C. 网关入站 → agent turn（问题 7/8/9）

### C1. 入站链路锚点（chat 消息 → agent turn）

【实测】全链：

| 步 | 位置 | 内容 |
|---|---|---|
| 1 | `pkg/channels/pico/pico.go:1414-1431` | `handleMessage`：type 分发（只认 ping/message.send/media.send） |
| 2 | `pico.go:1434-1517` | `handleMessageSend`：取 `payload["content"]` + media → 空内容拒 → 幂等（:1487-1495 `seenIDs`）→ `HandleInboundContext`（:1509，失败回滚幂等 :1510） |
| 3 | `pkg/channels/base.go:360-368` → `:263-356` | `HandleInboundContext` → `HandleMessageWithContext`：组装 `bus.InboundMessage` → `PublishInbound`（:347） |
| 4 | `pkg/agent/agent.go:230-301` | inbound 泵：`tryHandleApprovalReply`（:240）→ sessionKey `LoadOrStore` 占位（:250-260）→ **busy 分支**或新 turn |

### C2. ask_user 答复回流（二期交互回传的模板）

【实测】完整链路（前端答 ask → pendingAsk resolve → agent 上下文）：

1. **前端**：ask-card 提交 `sendChatMessage({content: 答复文本})`（`ask-card.tsx:61`）。
2. **网关**：`message.send` → `handleMessageSend`（同 C1 步 2）→ bus。
3. **agent 泵 busy 分支**（turn 活跃时必然走这里——ask 问出时 turn 一定在跑）：
   `agent.go:276` `tryHandleAskReply`。
4. **匹配与解析**：`pkg/agent/ask_user.go:421-475`——
   `pendingAsks.Load(sessionKey)`（:422）；channel/chatID 必须一致（:430）；群聊 senderID 钉住
   （:440-450）；非 /skip 命令放行走命令路径（:451-455）；`collectAskReply`（:456 →
   :504-529：/skip → skipped；单题整条消息是答案；多题按行拆，行多余折叠进末题，截断
   `askMaxAnswerRunes`）；`p.resolve <- reply`（:462-467，resolved 标记防双发）；发回执
   `publishAskReceipt`（:469）。**该消息被消费，不进模型上下文**（:413-420 注释）。
5. **等待方**：ask_user 工具在 `pipeline_execute_loop.go:558` 被拦截（`toolName ==
   askUserToolName`），进入等待循环 `ask_user.go:280-321`：`reply := <-resolve` →
   `finish()`（:253-260，置 resolved + CompareAndDelete + 清 marker）→
   `askResultFor`（:325-346）拼 "The user answered:\n- <题>: <答案>" 的 `tools.ToolResult` →
   作为 ask_user 工具结果进 agent 上下文。超时/abort 各有兜底（:297-314）。
6. **挂起注册**：问的时候 `ask_user.go:207-228` 建 `pendingAsk{resolve chan, channel, chatID,
   senderID, questions}`，`pendingAsks.Swap` 覆盖旧问（旧问按 skipped 释放）；问卡发布
   `publishAskQuestion`（:230 → :366 起，`msg.Ask = &bus.AskRequestMeta{...}`）；持久化
   marker 供刷新还原（:236-246）。

二期回传若做成"阻塞等结果"（render_ui_interact 之类）可直接复刻 4-6；若做成"即发即忘 +
下一轮 turn 消费"则更简单（见 §设计含义）。

### C3. turn 活跃时的入站语义：steering 排队，不是 busy 拒绝

【实测】`agent.go:260-301`：sessionKey 已被占（turn 活跃或 setup 中）→
依次尝试 stop/compact/revert/askReply 拦截（:261-277），全部未命中 →
`enqueueSteeringMessage(sessionKey, agentID, providers.Message{Role:"user", Content, Media})`
（:282-286）→ `NotifySteering`（:294）。**队列注入**语义由
`pkg/agent/steering.go:34-41`：mode `all` 或默认 `oneAtATime`（配置
`agents.defaults.steering_mode`，`agent_init.go:82`）；turn 结束后
`drainQueuedSteeringContinuations`（`agent_steering.go:65-98`）继续消化队列。
没有任何"busy 拒绝"路径（`/switch`、`/new` 的 busy 是聊天命令层的另一回事）。

**注意**：`/new`、`/switch` 命令在 turn 活跃时返回 busy 的行为是 CLI 命令路径
（AGENTS.md 所述），与 pico 消息通道无关——pico 通道的 chat 消息永远走 steering。

## D. 流式增量更新（问题 10/11/12）

### D1. 网关侧 `message.update`：可以更新已广播的消息，payload 任意

【实测】三个出站 API：
- `EditMessage` / `EditMessageWithPayload`（`pico.go:515-526`，实现 `channels.MessageEditor`）；
- 底层 `editMessagePayload`（`pico.go:1756-1777`）：**接收任意 payload map**，强制补
  `message_id`、缺 `content` 补 `""`，组 `TypeMessageUpdate` 帧 → `broadcastToSession`
  （seq 戳 + 断线重放 ring，`pico.go:1132-1147`）。流式 markdown（BeginStream，`pico.go:677`）
  与 tool-feedback 收尾都在用它。

即：**网关已有"对既有 message_id 广播一帧更新"的全部传输设施**，只是 `editMessagePayload`
目前没有 a2ui 键的调用方（payload 里放什么由调用者定，无白名单）。

### D2. 前端 `message.update` 处理：只更新五个字段，不碰 ui

【实测】`web/frontend/src/features/chat/protocol.ts:279-342`：
按 `payload.message_id` 就地更新 `{content, kind, toolCalls, modelName, attachments}`
（:293-312）；**不解析/不更新 `ui`（a2ui）、`ask`、`approval`**；找不到该 id 时
追加一条新 assistant 消息（:317-331）。`payload.a2ui` 的解析只在 `message.create`
分支（`parseA2uiFromPayload`，:157-167 → :206，`ChatMessage.ui`，`store/chat.ts:74,111-113`）。

### D3. 分帧到达的两种机制现状与最小改法（问题 11，推断+实测依据）

现状事实：
- 一期一条 `OutboundMessage` 发整个批次（`pkg/tools/render_ui.go:220-226` 一次 PublishOutbound
  → pico.go:490-499 一次 `message.create` 带 `payload["a2ui"]` = 完整数组）。
- `A2uiCard` 的 processor 生命周期绑定 `useMemo(..., [ui.messages])`
  （`a2ui-card.tsx:80-99`）：`ui.messages` **引用变化** → 整个 processor 重建 + 全量
  processMessages + surface 全量重挂载（dataModel 也会按初始帧重置——用户已填一半的表单会清空）。
- web_core 的 `processMessages` 天生支持分帧【实测】：`ProcessableMessagePayload` 接受
  单条/数组/包装（`message-processor.d.ts:24-26`），`updateComponents`/`updateDataModel`
  操作直接打到已存在的 surface（`operations.d.ts:42-62`）；同一 surface 重复 createSurface
  抛 `Surface already exists`（`message-processor.js:748-750`）；对不存在的 surface 发
  updateComponents/updateDataModel 抛 `Surface not found for message`
  （`message-processor.js:1057-1059,1077-1079`）。

推论（供计划选择）：
- **改法甲（同卡热更新，二期目标形态）**：首帧 render_ui 发 `message.create`（现状不变）；
  后续 update 帧 → 网关对该 message_id 广播 `message.update`（`EditMessageWithPayload`
  放 `payload["a2ui"]` = 增量帧数组）→ 前端 protocol.ts 的 `message.update` 分支解析
  a2ui 并 **append 到 `msg.ui.messages`**（新数组引用）→ A2uiCard 拆成
  "useMemo 建一次 processor + useEffect 监听 `ui.messages.length` 增长 →
  `processor.processMessages(新增切片)`"。数据流是 MessageProcessor 状态机吃增量，
  surface 内部按 node 信号热更新，不重置 dataModel。改动点：protocol.ts（update 分支解析）、
  a2ui-card.tsx（增量消费）、pico.go（a2ui 进 update 帧）、render_ui.go（见 D4 门槛）。
- **改法乙（每帧新卡，零前端改动）**：每次 render_ui 都要求整批自包含（createSurface +
  全量组件），天然新 `message.create` 新卡。缺点：卡片堆积、无热更新，"分帧到达"体验不成立
  ——只能当兜底，不是目标。
- **不可行的中间态**：新 processor 里直接发 update 帧（不带 createSurface）→
  "Surface not found"（D3 实测）。

### D4. 隐性门槛：render_ui 工具今天**拒绝** update-only 批次

【实测】`pkg/tools/render_ui.go:294-296`：批次必须含至少一个 `createSurface`
（`renderUIEnvelopeViolation` 收尾检查），且 :283-287 强制 `catalogId` 精确匹配 basicCatalog。
分帧方案里"第二次 render_ui 只发 updateComponents"会被工具侧拒绝 → 二期必须放行
update-only 帧（例如按"本会话/本 turn 已创建过该 surfaceId"放行，或引入新的工具形态），
并考虑 history replay 的一致性（`RenderUIHistoryMessages` :476-511 从单次 tool-call args
还原整卡；update 帧分散在多次调用/多条消息后，历史还原策略要重新设计）。

### D5. 一期遗留 minor 的现状摘录（问题 12）

【实测】`web/frontend/src/features/chat/history.ts:180-193`——`messageSignature` 不含 ui：

```ts
function messageSignature(message: ChatMessage): string {
  const attachmentSignature = (message.attachments ?? [])
    .map((a) => `${a.type}\u0001${a.url}\u0001${a.filename ?? ""}`).join("\u0002")
  return `${message.role}\u0000${message.content}\u0000${normalizeMessageTimestamp(
    message.timestamp)}\u0000${message.kind ?? ""}\u0000${message.modelName ?? ""}\u0000${attachmentSignature}\u0000${toolCallsSignature(message.toolCalls)}`
}
```

（用途：`mergeHistoryMessages` :201-209 按 id/signature 去重合并 REST 历史与 WS 现存消息。
ui/ask/approval 都不在签名里——ui 变化不会触发 merge 侧去重差异，二期若让 update 帧改 ui
不受此处影响，但若做"历史合并时 ui 以谁为准"要回来补。）

【实测】`web/frontend/src/components/chat/a2ui-card.tsx:80-99`——useMemo 依赖
`[ui.messages]`（引用突变不重算）：

```tsx
const { surfaces, processor, failed } = useMemo(() => {
  const p = new MessageProcessor<WebComponentImplementation>(
    [basicCatalog], undefined, { version: "v0.9" },
  )
  try {
    p.processMessages(ui.messages as ProcessableMessage[])
    return { surfaces: Array.from(p.getSurfaces().values()), processor: p, failed: false }
  } catch {
    p.dispose()
    return { surfaces: [], processor: undefined, failed: true }
  }
}, [ui.messages])
```

第二参 `undefined` 就是二期接 `actionHandler` 的位置；`ui.messages` 是 store 里
`ChatMessage.ui.messages`（`store/chat.ts:111-113`）——store 不可变更新时数组引用才会变。

## E. VegaChart（问题 13/14/15）

### E1. `@a2ui/catalog-vegachart` 仍未发 npm

【实测】2026-11-14 `npm view @a2ui/catalog-vegachart version` → **404**。
web_core 里只有测试 fixture 提到其 spec 路径
（`web_core/src/catalog/schema_serializer.test.js:59-60`：`catalogs/vegachart/v0_9/catalog.json`、
`catalogs/vegachart/v1/catalog.json`）。结论与一期相同：官方包不可用，图表二期自建。

### E2. 自建 catalog 机制（问题 14）

【实测】注册自定义 React 组件的完整路径（`@a2ui/react/README.md:120-205` "Defining
Custom Components" + 实测类型）：

1. **定义 ComponentApi**：`{name, schema(Zod), allowedParents?, allowedChildren?}`
   （`web_core/src/catalog/types.d.ts:95-111`）。props 里用 `CommonSchemas.DynamicString/
   DynamicBoolean/Action/Checkable` 获得自动绑定；README :147-152 说明 allowedParents/
   allowedChildren 违规时 processor 以 `UNALLOWED_PARENT`/`UNALLOWED_CHILD` 拒绝 payload。
2. **React 实现**：`createComponentImplementation(api, FC)`（`@a2ui/react/index.d.ts:74`）
   ——generic binder 先解析 props 再渲染（DynamicString → 解析后的 string；Action →
   `() => void`；双向 setter 自动注入）；自管响应式的用
   `createBinderlessComponentImplementation`（:78，拿裸 `ComponentContext`，含
   `dispatchAction`——`component-context.d.ts:36-44`）。
3. **装进 Catalog**：`new Catalog(id, protocolVersion, components, functions, options)`
   （`types.d.ts:332-493`）或基于现成的改：
   `basicCatalog.derive({catalogId, components: [...原名或新组件]})`（:469-477，保留
   theme/布局字段、丢弃 instructions——:455-464 注释解释为何不保留）。
   catalogId **必须与 createSurface 消息里的 `catalogId` 完全一致**
   （v0.9 schema 必填 + 逐字匹配：`adapters/v0_9.d.ts:10-25`；一期 Go 侧也硬编码校验了
   basicCatalog 的 id，`render_ui.go:69,283-287`）。
4. **渲染走 Web Components**（与 basicCatalog 同路）：`A2uiSurface` 挂载时
   `prepareCatalogs(surface)`（`@a2ui/react/chunk-H4D2JAR7.js:175-177`，调用点 :633-635）
   把 catalog 里每个非 WebComponent 的 React 实现经 `toWebComponent` 包装
   （:144-160：自动生成 tagName + `ReactWcHost` 元素类，WeakSet 幂等）并
   `registerUniversalElement`（:210）。**即自建 React 组件不需要我们自己注册 custom
   element，A2uiSurface 全自动**——一期 `A2uiCard` 渲染管线零改动可用。
5. **多 catalog 并存**：`MessageProcessor` 构造第一参就是 `Catalog[]`（优先序）
   （`message-processor.d.ts:120`）；surface 建立时把版本兼容的 catalog 全部收进
   `availableCatalogs`（`message-processor.js:753-758`）；**组件条目可以带
   `catalogId` 键指定所属 catalog**（`message-processor.js:845-850` 读取并
   `resolveSurfaceItemCatalog`；:822 该键会先从 props 剥离再校验）。v0.9 的
   updateComponents 组件 payload 是 pass-through record（`adapters/v0_9.d.ts:44-53`），
   带额外 `catalogId` 键不会被 strict schema 拒。**即：createSurface 仍写 basicCatalog id，
   Chart 组件条目单独标 `catalogId: "<自建 catalog id>"` 是可行的混排方案**；或者更简单
   ——`basicCatalog.derive()` 出一个含 Chart 的单一 catalog，createSurface 直接写新 id
   （需要同步放宽 Go 侧 `renderUIBasicCatalogID` 的精确匹配）。

### E3. vega-lite / react-vega（问题 15）

【实测】（npm registry，2026-11-14）：
- `vega-lite` 最新 **6.5.0**（peerDep `vega ^6.4.0`；npm dist.unpackedSize 5,940,557 字节
  ≈ 5.9MB 源码包）。浏览器 min bundle 约 500KB 级、gzip 约 130KB 级【推断，未实测 bundle】
  ——建议动态 `import()` 按需加载（首屏不背）。
- `vega-embed` 最新 **7.3.0**。
- `react-vega` **8.0.0** 可用，peerDeps `react ^17||^18||^19`（本仓库 react 19.2.5 ✅）
  + `vega-embed ^7`，运行时仅依赖 `fast-deep-equal`——React 封装惯例成立。
  也可以不用 react-vega、自写薄封装（vega-embed 一个调用），减一层依赖【两条路线留计划定】。
- 前端包管理是 pnpm@10.33.0（`web/frontend/package.json`），react 19.2.5、zod 3.25.76
  （@a2ui peerDep 匹配）。

## 设计含义（侦察事实直接推出的边界条件，供控制者写计划）

1. **回传通道现成**：前端只有 `message.send` 一条上行；ask 答复就是普通文本 `message.send`
   （B1）。A2UI 交互回传最小实现 = A2uiCard 里 `surface.onAction.subscribe` →
   `sendChatMessage`（或直接 socket 层加一种轻量 payload 形态）→ 网关走既有
   `handleMessageSend` → agent 泵。若要"不产生用户气泡/不进 steering"，需要网关加结构化
   分支（照 `tryHandleAskReply` 模式在 busy 分支前加 `tryHandleA2uiAction`），见 C2。
2. **不阻塞版最简**：action 回传即发即忘（进下一 turn 或 steering），不需要 pendingAsk
   状态机；阻塞版复刻 ask_user 三件套（pendingAsks + tryHandleXxxReply + 拦截器）。
3. **sendDataModel 别指望自动上行**：flag 只让 `getRendererDataModel()` 可拉（A4），
   "何时拉、怎么带"全是我们的代码。多数场景 Button event.context 的 path 绑定已经把
   表单值带上了（A3），sendDataModel 可二期先不开。
4. **分帧热更新可行但有三处必改**：render_ui 工具放行 update-only 帧（D4）、pico update 帧
   携带 a2ui（D1 设施现成）、前端 update 分支解析 a2ui + A2uiCard 增量消费（D3 改法甲）；
   历史回放（session.go `RenderUIHistoryMessages`，D4）需要配套策略。
5. **VegaChart 自建**：catalog 机制完全支持（E2 第 4/5 点），无需改 @a2ui 包；主要工作量在
   vega-lite 按需加载 + 组件 schema 设计 + Go 侧 catalogId 校验放宽（若用新 catalog id）。

## 明确排除项（本次侦察中确认不走的路）

| 路线 | 排除原因 |
|---|---|
| `<A2UIProvider onAction>`（README 出现的写法） | v0.8 legacy API，从 `@a2ui/react/v0_8` 导入；v0.9 无此组件（A1 实测） |
| 等 `@a2ui/catalog-vegachart` 官方包 | npm 404 复测仍存在（E1）；2026-11 已两月余，不可依赖 |
| v1.0 callAgentFunction RPC 作为回传通道 | v0.9 surface/adapter 不走它（A5）；引入 v1.0 协议面超出二期范围 |
| MessageProcessor 全局单例管理 surface | 一期 D5 决策维持：每卡独立 processor；增量消费在卡内做（D3） |
| 前端为回传新增 WS 消息类型（如 `a2ui.action`） | 网关 `handleMessage` 只认 ping/message.send/media.send（B1），新增 type 要动协议层；ask 模式证明 `message.send` 载荷够用——是否新增留计划权衡 |
| updateComponents 打到新 processor（不带 createSurface） | web_core 抛 `Surface not found`（D3 实测）——不可行 |
| react-vega 强制采用 | 可行但不强制；vega-embed 直调是更薄的备选（E3） |

## 附：本次侦察覆盖的文件清单

- 依赖库（node_modules，只读）：`@a2ui/react/index.d.ts`、`@a2ui/react/README.md`、
  `@a2ui/react/chunk-H4D2JAR7.js`、`@a2ui/web_core/src/{state/surface-model,processing/
  message-processor,processing/operations,processing/adapters/v0_9,resolution/
  generic-binder,resolution/component-context,universal/basic_catalog/components/*,
  v0_9/basic_catalog/components/basic_components,v0_9/schema/client-to-server,rpc/
  rpc-handler,catalog/types,common/events}.d.ts`（及对应 .js 实现数处）。
- 本仓库：`pkg/tools/render_ui.go`、`pkg/bus/types.go`（UI/UIMeta :106-146）、
  `pkg/channels/pico/{pico.go,client.go,protocol.go}`、`pkg/channels/base.go`、
  `pkg/agent/{agent.go,ask_user.go,agent_steering.go,steering.go,agent_init.go}`、
  `pkg/agent/pipeline_execute_loop.go`（:558 锚点）、`web/frontend/src/features/chat/
  {controller.ts,protocol.ts,history.ts,websocket.ts}`、`web/frontend/src/hooks/use-pico-chat.ts`、
  `web/frontend/src/store/chat.ts`、`web/frontend/src/components/chat/{a2ui-card.tsx,
  ask-card.tsx,approval-card.tsx,chat-page.tsx}`、`web/frontend/src/chat-pi/turns.ts`、
  `web/backend/api/session.go`（:83-87,:845-860 历史回放）、`web/frontend/package.json`。
- npm registry 实测：`@a2ui/catalog-vegachart`（404）、`vega-lite@6.5.0`、`vega-embed@7.3.0`、
  `react-vega@8.0.0`。
