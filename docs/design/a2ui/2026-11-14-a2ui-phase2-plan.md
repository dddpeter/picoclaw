# A2UI 二期实现计划（交互回传 + 流式增量 + VegaChart）

> **面向 AI 代理的工作者：** 必需子技能：使用 subagent-driven-development 逐任务实现此计划。步骤使用复选框（`- [ ]`）语法来跟踪进度。
> 姊妹篇：`docs/design/2026-11-14-a2ui-phase2-recon.zh.md`（侦察——所有 API 与代码锚点的实证依据）、`docs/design/2026-11-13-a2ui-web-render.zh.md`（一期 as-built）。

**目标：** ① 用户点击卡片按钮/提交表单 → 动作与表单值回传 agent 上下文；② 模型多帧更新已有卡片（updateComponents 增量到达，前端热更新）；③ 自建 VegaChart catalog 组件画真图表。

**基线：** 0132d61c（一期 + 五轮实战门加固之后）。

## 全局约束

- 交互回传走**普通聊天消息**（照抄 ask 答复模式：`message.send` 纯文本），不做专用拦截器；turn 活跃时入站走既有 steering 队列是可接受的 MVP 行为（侦察风险点 1，已裁决接受）。
- 动作消息格式（前端组装，模型靠它理解）：`[A2UI 交互] surface="<surfaceId>" action="<name>" source="<componentId>"` + `context=<json>`（非空时）。
- 流式增量的合并策略：**后端合并 + message.update 重播**——pico 层按 (session, surfaceId) 记录已广播的 message.create 的 message id 与累计 payload；工具收到 update-only 批次时 pico 把帧合并进累计 payload 后 `editMessagePayload` 广播 `message.update`；前端 message.update 分支消费 payload.a2ui 就地替换 `ChatMessage.ui`。**每卡独立 processor 语义不变**（合并后整批重处理，useMemo 按 ui.messages 新引用重算，旧 processor 由既有 useEffect cleanup dispose）。
- 历史回放折叠加帧：session JSONL 里按序读取 render_ui 调用，按 surfaceId 折叠——createSurface 批次立卡，后续 update-only 批次的帧并入该卡最终 payload；孤儿 update-only 批次（surfaceId 无主）只出文本兜底不出卡。
- update-only 批次的工具门：**delta-local 检查**（version/分支/键集/组件名/id/批内重复/catalogId 不查——无 createSurface/大小/单 surfaceId 仍查），root 存在性与可达性**跳过**（对增量无意义，合并树由前端 processor 全量重检）；局限性（坏 delta 可能破坏合并树且无重试反馈）写入设计文档。
- 批次单 surfaceId：同批所有分支载荷的 surfaceId 必须一致（新门检查，合并去重的前提）。
- VegaChart：catalog 组件名 `VegaChart`，props `{spec}`（对象或 `{path}` 绑定）；react-vega@8 动态 import 按需加载（不要打进主 chunk）；组件实现走 `createComponentImplementation` + `Catalog.derive()` 混入 basicCatalog——**derived catalog 必须保留 basicCatalog 的 catalogId**（实测确认；若 derive 改 id，回退方案=同时接受新旧两个 id，写进实现报告）。Go 门白名单与 description 同步加 `VegaChart`。
- 每个任务 TDD、一任务一 commit；Go 测试 `go test ./pkg/...`（失败集差集法，预存白名单见体检文档）；前端 `pnpm test` + `pnpm build`。
- 注释语言与周围代码一致（一期裁决：英文）。

## 审查重点（Review Focus）

1. **回传消息的注入安全**：action/context 内容进用户消息文本——模型输出导向的用户消息，确认无注入面扩大（与普通用户消息同级，无特权）。
2. **合并广播的会话隔离**：pico 层 surface 注册表按 (sessionKey/channel+chatID) 隔离，A 会话的 update 不得改 B 会话的卡；注册表上限防泄漏。
3. **message.update 的 ui 替换**：只更新存在 ui 字段的消息，update 分支不得扰动普通文本消息的既有更新语义（回归）。
4. **历史折叠的顺序性**：JSONL 按序折叠；createSurface 重名（同 surfaceId 两次 create）取最后一张卡。
5. **VegaChart 按需加载**：主 bundle 不得引入 vega-lite（grep 产物验证），动态 import 块独立。

---

### 任务 1：交互回传——前端动作上行 + 模型契约

**文件：**
- 修改：`web/frontend/src/components/chat/a2ui-card.tsx`（MessageProcessor 构造第二参接 actionHandler；ActionPayload `{name, surfaceId, sourceComponentId, context}` 组装动作消息，经既有聊天发送路径发出——发送函数的取用参照 `ask-card.tsx:61` 的 ask 答复发送）
- 修改：`web/frontend/src/i18n/locales/*.json`（动作消息若需本地化前缀——MVP 用固定 `[A2UI 交互]` 前缀，五语言一致可不做 i18n 键，说明取舍即可）
- 修改：`pkg/tools/render_ui.go`（description 补交互契约：Button 用 `event.context` 的 `{path}` 绑定收集表单值、用户交互会以 `[A2UI 交互]` 前缀的用户消息到达、教一个可回传表单的完整示例）
- 测试：`web/frontend/src/components/chat/a2ui-card.test.tsx` 追加（mock 发送函数断言消息文本）；`pkg/tools/render_ui_test.go` SchemaContract 锁新短语

- [ ] **步骤 1：失败的测试** — vitest：渲染带 Button 的合法批次（actionHandler mock），`dispatchAction`/点击后断言发送函数收到含 `[A2UI 交互]`、surfaceId、action name、context JSON 的消息文本；无 context 时不带 data= 段。Go：SchemaContract 锁 `A2UI 交互` 与 `event.context`。
- [ ] **步骤 2：验证失败** → **步骤 3：实现** → **步骤 4：通过**（`pnpm vitest run src/components/chat/`；`go test ./pkg/tools/ -run TestRenderUI`）
- [ ] **步骤 5：Commit** — `feat(a2ui): 卡片动作回传——onAction 组装用户消息走聊天发送`

### 任务 2：批次单 surfaceId 校验 + UIMeta.SurfaceID（Go 侧铺垫）

**文件：**
- 修改：`pkg/bus/types.go`（`UIMeta` 加 `SurfaceID string`，json tag `surfaceId,omitempty`）
- 修改：`pkg/tools/render_ui.go`（新门：批内所有分支载荷 surfaceId 一致且非空；Execute 提取 surfaceId 填入 UIMeta；description 不变）
- 测试：`pkg/tools/render_ui_test.go` 追加（两 surfaceId 混批 → IsError；UIMeta.SurfaceID 值断言）

- [ ] TDD 五步照旧 → **Commit** — `feat(a2ui): 批次单 surfaceId 门 + UIMeta.SurfaceID 透传`

### 任务 3：前端 message.update 消费 a2ui payload

**文件：**
- 修改：`web/frontend/src/features/chat/protocol.ts`（message.update 分支：payload 显式带 `a2ui` 时替换消息的 ui 字段——解析复用 `parseA2uiFromPayload`，形状不对忽略；不扰动既有 5 字段更新语义）
- 修改：`web/frontend/src/features/chat/history.ts`（messageSignature 补 ui 维度——`ui ? ui.messages.length : 0` 一类，修一期延后 minor #18）
- 测试：`web/frontend/src/features/chat/protocol.test.ts`（update 携带 a2ui → ui 替换；不带 → 既有行为回归）

- [ ] TDD 五步 → **Commit** — `feat(web): message.update 消费 a2ui payload——卡片可被后端热更新`

### 任务 4：工具门放开 update-only 批次 + 历史回放折叠加帧

**文件：**
- 修改：`pkg/tools/render_ui.go`（`renderUIEnvelopeViolation`：批内无 createSurface 时走 update-only 路径——跳过 root 存在性/可达性/多父引用检查（delta-local），保留 version/分支/键集/组件名/id/批内重复/单 surfaceId/大小；catalogId 检查仅对 createSurface 帧生效）
- 修改：`pkg/tools/render_ui.go` 的 `RenderUIHistoryMessages` 或新函数（web/backend 调用点同步改）：支持按调用序列折叠——新导出 `RenderUIHistoryFold(calls []argsJSON)` 按 surfaceId 折叠出最终批次列表（createSurface 批次立卡、update-only 帧并入、无主 update-only 出文本不出卡）
- 修改：`web/backend/api/session.go`（transcript 组装处改用折叠函数——assistant 消息序列按序传入）
- 修改：description 教 update-only 用法（"call again with updateComponents only to update an existing surface"）
- 测试：`pkg/tools/render_ui_test.go`（update-only 批次发布成功/坏 delta 检查仍拦）；`web/backend/api/session_test.go`（折叠：create+update → 一张卡含最终帧；孤儿 update → 文本）

- [ ] TDD 五步 → **Commit** — `feat(a2ui): update-only 批次门放开 + 历史回放按 surfaceId 折叠加帧`

### 任务 5：pico 层 surface 注册表 + 合并广播 message.update

**文件：**
- 修改：`pkg/channels/pico/pico.go`（Send 的 a2ui 块：UIMeta.SurfaceID 非空且批内无 createSurface → 查 (sessionKey, surfaceId) 注册表 → 帧合并进累计 payload → `editMessagePayload`（:1756 附近）广播 message.update；有 createSurface → message.create 并注册 (sessionKey, surfaceId) → {messageID, 累计 payload}。注册表 LRU 上限 64 表项防泄漏；payload 存储注意并发（channel 级 mutex））
- 测试：`pkg/channels/pico/pico_test.go` 追加（create+update 序列 → 第二次广播是 message.update 且 payload.a2ui 为合并后批次；跨会话隔离：B 会话 update 不动 A 会话卡；未知 surfaceId 的 update → 降级 message.create 新卡）

- [ ] TDD 五步 → **Commit** — `feat(pico): surface 注册表合并 update-only 帧——message.update 热更新卡片`

### 任务 6：VegaChart catalog 组件

**文件：**
- 创建：`web/frontend/src/components/chat/a2ui-vega.tsx`（`createComponentImplementation` + `Catalog.derive()` 混入 basicCatalog；组件名 `VegaChart`，props `{spec}` 支持对象与 `{path}` 绑定；react-vega 动态 import）
- 修改：`web/frontend/src/components/chat/a2ui-card.tsx`（MessageProcessor catalogs 数组换 derived catalog）
- 修改：`pkg/tools/render_ui.go`（白名单加 `VegaChart`；description 加一行用法与"spec 是 vega-lite JSON"）
- 测试：`web/frontend/src/components/chat/a2ui-vega.test.tsx`（合法 spec → 容器出现 vega 图表 DOM 标记；path 绑定 spec → dataModel 取值渲染；主 bundle 无 vega——`pnpm build` 后 grep 产物）
- **前置实测**：`Catalog.derive()` 是否保留 basicCatalog catalogId——不保留则实现报告写明并同步调整门常量

- [ ] TDD 五步 → **Commit** — `feat(web): VegaChart catalog 组件——vega-lite 图表进 A2UI 卡片`

### 任务 7：端到端验证 + 文档

**文件：**
- 修改：`docs/design/2026-11-14-a2ui-phase2.zh.md`（as-built 设计文档：三条链路管线图、已知局限）
- 修改：`CHANGELOG.md`（0.5.8 条目）

- [ ] 全量测试（失败集差集法）+ `pnpm build`
- [ ] 手工端到端可做部分；手测清单：①按钮点击 → `[A2UI 交互]` 消息 → agent 响应；②表单填写提交 → agent 收到值；③模型两次 render_ui（create+update）→ 卡片原地热更新；④图表卡片渲染；⑤刷新后历史折叠正确
- [ ] **Commit** — `docs: A2UI 二期设计文档与 0.5.8 发布注记`

## 明确非目标（本计划不做）

- 卡片动作的专用总线通道/拦截器（走普通 message.send）
- update-only 批次的合并树服务端校验（delta-local + 前端全量重检）
- VegaChart 的主题联动（用 vega-lite 默认主题）
- 多 surface 同批（单 surfaceId 门强制一个批次一张 surface）
