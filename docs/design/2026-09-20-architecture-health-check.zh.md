# picoclaw 架构体检记录（2026-09-20）

> 状态：**仅诊断，未改动任何代码；重构批次待批准**。
> 可视化版本见同目录 `architecture-health-check-2026-09.html`。
> 数据来源：codebase-memory 知识图谱（full 模式，generation 2026-09-18，与 main 零漂移）+ 源码交叉验证（Auditor 级，16 个证据文件覆盖校验通过）。

## 总体判断

分层骨架健康：`config` / `logger` / `utils` / `media` 作为核心层 fan-in 大、fan-out 为零；`providers` 只出不进；`channels` 高内聚；29 个 CALLS 环绝大多数为良性委托/递归环。

病灶集中在两处：

1. **`pkg/agent` 流水线内部**——巨型函数 + 上帝状态结构体，"深模块"的窄接口被内部细节淹没；
2. **web 前端**——巨型组件与复制粘贴。

## 关键数字

- 规模：957 个 Go 文件 / 184 个 TS 文件；图谱 23,581 节点、144,482 边。
- `pkg/agent`：66 个非测试文件、约 5.59 万行（含测试）。
- `web/backend/api`：26 文件、12,687 行；backend→config 边界 1,080 次调用。
- 最长单函数 `ExecuteTools` 848 行，认知复杂度 410（全库第一）。
- 前端 Add/EditModelSheet 排序交集 647 行 / 846 行（~78% 重复，去重后下界）。

## 认知复杂度 Top 10（非测试 Go 函数）

| # | 函数 | 认知复杂度 | 行数 | 备注 |
|---|---|---|---|---|
| 1 | `pkg/agent.ExecuteTools` | 410 | 848 | P0 |
| 2 | `pkg/agent.CallLLM` | 175 | 704 | linear_scan_in_loop=10，P0 |
| 3 | `pkg/providers/bedrock.parseStreamResponse` | 174 | 141 | P2 散点 |
| 4 | `pkg/utils.walk`（markdown 转换器） | 153 | 234 | 包内递归 DOM 走访，P2 |
| 5 | `pkg/agent.Run` | 151 | 170 | — |
| 6 | `pkg/agent.runTurn` | 144 | 299 | — |
| 7 | `pkg/providers.CreateProviderFromConfig` | 135 | 279 | 13 个 case，新工厂 |
| 8 | `pkg/config.v0ProvidersMapToModelList` | 128 | 592 | 圈复杂度 110，P1 |
| 9 | `pkg/tools.runBackground` | 123 | 221 | P2 散点 |
| 10 | `web/backend.main` | 120 | 401 | P1 |

## 发现清单与最小重构方案

### P0-1 `ExecuteTools` 巨型函数

- **位置**：`pkg/agent/pipeline_execute.go:121`（文件 965 行，函数占 848 行）。
- **证据**：认知复杂度 410 / 圈复杂度 101；两个大型 `switch decision.normalizedAction()`（审批决策流 + 工具分发流）串联；11 个 case。
- **症状**：单一方法混杂工具审批、分发、skill 名推断、媒体解析、错误摘要、流式面板反馈、执行记录六种职责。
- **影响面**：每轮对话必经；但调用点唯一（`turn_coord.go:254`），重构不外溢出包。
- **最小重构**：纯内部拆分，不改签名不改行为——两个 switch 各提取为 `handleApprovalDecision()` / `dispatchTool()`；每个 case 体提取为私有方法；分发改查表 `map[string]toolHandler`。目标单函数 ≤120 行、每片认知复杂度 ≤40。动手前先跑全量 `go test ./pkg/...` 建基线。
- **预估**：1.5~2 天。

### P0-2 `CallLLM` 巨型函数 + 循环内线性扫描

- **位置**：`pkg/agent/pipeline_llm.go:28`。
- **证据**：认知复杂度 175；`linear_scan_in_loop=10`（循环内 find/contains 式扫描，潜在 O(n²)）；流式降级、fallback、hooks、重试全部内联。
- **影响面**：每轮对话必经；调用点唯一（`turn_coord.go:220`）。
- **最小重构**：提取 `callStreaming()` / `callNonStreaming()` 两分支；重试环独立成方法；循环内扫描改为循环外一次建 map 索引（先 benchmark 证实热点）。
- **红线**：fork 关键行为——ChatStream 独立流式 Transport、响应头超时 90s、`streamRoundTripper` 语义；禁止触碰 `openai_compat` 传输层；流式响应头超时测试必须保持绿。
- **预估**：1.5 天（先补降级路径回归测试）。

### P0-3 前端 Add/EditModelSheet ~78% 重复

- **位置**：`web/frontend/src/components/models/add-model-sheet.tsx`（846 行）与 `edit-model-sheet.tsx`（822 行）。
- **证据**：排序交集 647 行完全一致（含整段 import、字段渲染、FetchModelsDialog 集成、保存逻辑）。
- **最小重构**：提取 `ModelFormSheet` 共享组件 + `useModelFormState()` hook；两个 Sheet 只留标题、初始值、提交端点差异。预计净删 550~650 行。建议作为第一个动手项（不碰 Go 热路径）。
- **预估**：1 天。

### P1-4 `turnState` / `turnExecution` 上帝结构体

- **位置**：`pkg/agent/turn_state.go:119/204`；turnState 约 70 字段，包内 28 个文件引用。
- **症状**：Pipeline 各阶段共享同一坨可变状态，字段所有权不可见；pkg/agent 隐藏耦合最大来源。
- **最小重构**：分三步且先只走第一步——① 按阶段写者重排并注释字段所有权（零行为变更）；② turnExecution 每迭代重建的字段抽成 `iterationState` 子结构；③ 其余留待 P0-1/P0-2 拆分时顺势收口。
- **红线**：turn 全程持有 agent 模型状态读锁（runTurn），turnState 的 mu 语义不得改变；任何命令路径禁止无超时拿写锁。
- **预估**：① 0.5 天；② 1 天。

### P1-5 前端巨型组件群

- `ModelsPage` 848 行（27 个 hooks）、`ConfigPage` 689、`ChannelConfigPage` 469、`useCredentialsPage` 413 行。
- **最小重构**：按库内既有 `use-skills-page.ts` / `use-credentials-page.ts` 模式逐页剥 hook（数据 / 派生 / 渲染三层）；ModelsPage 优先。每页 0.5~1 天，可渐进。

### P1-6 `v0ProvidersMapToModelList` 冻结迁移代码

- **位置**：`pkg/config/config_old.go:32`；592 行、圈复杂度 110、115 个 if/for；唯一调用点 `migration.go:236`（migrateV0ToV1）。
- **最小重构**：**不重写逻辑**。补 v0→v1 golden test 快照锁行为；文件头声明冻结、禁止新功能进入；上游淘汰 v0 后整文件删除。0.5 天。

### P1-7 web/backend 与配置文件隐式耦合

- **证据**：backend→config 1,080 次调用（全库最重边界）；backend 直写 `config.json`，与 gateway 的 mtime 轮询热重载构成跨进程文件级时序耦合（AGENTS.md 已记载 Windows rename 竞争）。
- **最小重构**：只收口写路径——backend 全部配置写入集中到一个 `configStore`（原子写 + 变更通知），读路径维持现状；`main.go` 按 CLI/服务/tray 拆分。写路径 1 天 + main.go 拆分 0.5 天。

### P2-8 双工厂并存

- `providers.CreateProvider`（`legacy_provider.go:26`，仅 `pkg/gateway/gateway.go:402` 与 `cmd/picoclaw/internal/agent/helpers.go:42` 两个生产调用点）vs `providers.CreateProviderFromConfig`（新工厂）。
- **最小重构**：迁移 2 个调用点后删除旧工厂（或改一行委托）；给 `factory.go` / `factory_provider.go` 分工加包注释。0.5 天。全库"删除收益/成本比"最高项。

### P2-9 命名混乱（改文档不改名）

- 三个"会话之家"：`pkg/memory`（JSONL 底层存储，被 session/agent/web 引用，**非死代码**）→ `pkg/session`（metaAwareStore 包装）→ `pkg/agent/sessions/`（运行时数据目录，未入 git）。
- 三代上下文管理器同包共存：`context.go` / `context_legacy.go` / `context_seahorse.go`；legacy 版仍在 `turn_coord.go:370/377/385` 作为 seahorse 关闭时的回退路径（活代码）。
- 双总线：`pkg/bus`（频道消息路由）与 `pkg/events`（运行时事件总线）职责不同但名字不体现。
- **最小重构**：在 `fork-overview.zh.md` 补概念地图；四个包各加 3 行角色 doc 注释。重命名包会破坏 100+ import 点与上游同步，不做。0.5 天。

### P2-10 散点高复杂度（童子军军规，不专项处理）

`bedrock.parseStreamResponse`(174) · `utils.walk`(153) · `integration/providerByName`(118) · `onebot.parseMessageSegments`(112) · `channels.hiddenValues`(110) · `tools.runBackground`(123)。
**例外**：`openai_compat.parseStreamResponse`（229 行）承载 fork 的 `streamRoundTripper` 关键语义，**明确不动**。

### P3 无需处理项（防误修）

- 29 个 CALLS 环：logger 8 节点环是门面递归设计，其余多为 A↔B 委托环（如 `Execute`↔`ExecuteWithContext`），良性。
- `providers` 根目录 `cli_facade.go` / `httpapi_facade.go` / `oauth_facade.go`：import 路径稳定兼容层，有意为之，补 deprecation 注释即可。
- `DefaultConfig` 518 行：复杂度 0，纯数据声明，观察。

## 建议路线图（待批准）

| 批次 | 内容 | 前置条件 |
|---|---|---|
| 1 | 前端 Sheet 合并(#3) + 双工厂合并(#8) + 概念地图(#9) | 无（不碰 Go 热路径） |
| 2 | ExecuteTools 内部拆分(#1) | 全量测试基线 |
| 3 | CallLLM 分支提取 + 索引化(#2) | 降级路径回归测试；不碰 openai_compat 传输层 |
| 4 | turnState 所有权文档 + iterationState(#4) + v0 golden test(#6) | 批次 2/3 完成 |
| 5 | 前端 hook 化(#5) + backend 写路径收口(#7) + 散点军规(#10) | 无硬依赖，可穿插 |

## 方法论与局限

- 图谱指标（cognitive/complexity/loop_depth/linear_scan_in_loop 等）为静态分析；全部 P0/P1 结论均经源码 grep/read 交叉验证。
- 设计性排除目录（`pkg/agent/sessions`、`cmd/picoclaw/internal/onboard`）已用文件系统补查。
- SIMILAR_TO 边无权重属性，重复判定用排序交集法（行序不计），647 行是去重后下界。
- `linear_scan_in_loop` 提示的 O(n²) 需 benchmark 证实后再优化。
- 所有方案遵循 fork 行为红线（AGENTS.md）：开放默认三件套、`streamRoundTripper` 语义、`*bool` 配置指针类型、命令路径不拿无超时写锁，均不得"修"掉。
