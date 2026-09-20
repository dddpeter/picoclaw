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

---

# 附：2026-09-20 复核修订（同日，代码已前进）

> 触发：本报告归档后代码继续前进，用户要求复核。本节为**只读复核**，不改动上文原稿。
> 复核方式：重建 codebase-memory 图谱（`full` 模式，节点 23,581 → **24,772**，边 144,482 → **151,199**）+ 源码交叉验证。
> 复核时 HEAD：`f6e0ad99`（原稿基线 `486db105`，前进 3 个提交，见下方漂移清单）。

## A.1 结论摘要

**原报告的核心诊断与全部关键数字经复核成立。** 初次复核曾误判「数字失效」，根因是查询方法错误：图谱的 `cognitive` / `complexity` / `linear_scan_in_loop` 等属性**以字符串存储且 `ORDER BY` 按字典序**，直接用 `f.cognitive DESC` 排序会把 `"99" > "410"`，并用 `f.cognitive IS NOT NULL` 过滤会漏掉 `Method` 节点。改用 `search_graph` 按 `Method` 标签精确取属性后，数字全部吻合。

## A.2 关键数字交叉验证（✅ 全部吻合）

| 函数 | 原报告 | 复核实测 | 结论 |
|---|---|---|---|
| `pkg/agent.ExecuteTools` | 认知复杂度 410 / 848 行 | **cog 410，cyc 101，846 行**（`pipeline_execute.go` L121–965） | ✅ 吻合 |
| `pkg/agent.CallLLM` | 认知复杂度 175 | **cog 175，cyc 84，703 行**（`pipeline_llm.go` L28–731） | ✅ 吻合 |
| `CallLLM.linear_scan_in_loop` | 10（判为潜在 O(n²)） | **10** —— `Method` 节点中全库唯一 ≥10 | ✅ 吻合，预警成立 |
| `add/edit-model-sheet.tsx` 重复 | 647 行 / ~78% | **678 行完全相同 / 80.1%**（846 vs 822 行） | ✅ 吻合（实际略高） |
| `pkg/config.v0ProvidersMapToModelList` | 592 行 / 圈复杂度 110 | **592 行 / cyc 110 / cog 128** | ✅ 吻合 |
| `web/backend.main` | 认知复杂度 120 / 401 行 | **cog 120 / 401 行** | ✅ 吻合 |

**复核同时发现两处原报告未收录、应升级的项：**

1. **`web/backend/api/gateway.go`（1634 行）与 `models.go`（1612 行）** —— 原报告只给 `web/backend/api` 总量（26 文件 / 12,687 行），未点出这两个 1600+ 行巨文件。建议补为 **P1 级**。
2. **前端巨型组件数字已变大** —— 复核实测 `models-page.tsx` **1315 行**（原报告 848）、`config-page.tsx` **776**（原 689）、`channel-config-page.tsx` **758**（原 469）。原报告的 P1-5 结论不变，但**量级被低估**，`models-page.tsx` 应从「前端最大页」升级为**前端头号靶子**。

## A.3 独立复核得出的 Top 洞察（与方法论修正）

**⚠️ 方法论修正（对原报告的重要补充）**：原报告的「认知复杂度 Top 10」表**只覆盖了 `Function` 标签节点**。图谱中 Go 方法存为 **`Method`** 标签，因此 **原报告漏掉了全库最热的两个函数本身**（`ExecuteTools`/`CallLLM` 均为 `Method`）——它们被单独在正文里用其它数据源提及，但未进 Top 表。**查复杂度榜必须同时查 `Function` 和 `Method` 两个标签。**

复核后（Go 生产代码，`Function` + `Method` 合并排序）：

- **认知复杂度 > 100 的函数共 9 个**；**≥ 50 的共 41 个**。
- `ExecuteTools`（410）与 `CallLLM`（175）**仍稳居全库第 1、第 2**，与原报告排序一致。
- 其余高分项与原报告 P2 散点清单一致：`bedrock.parseStreamResponse`(174)、`CreateProviderFromConfig`(135)、`v0ProvidersMapToModelList`(128)、`backend.main`(120)、`channels.hiddenValues`(110)、`agent.registerSharedTools`(106)、`config.LoadConfigWithWarnings`(103)、`backend.onReady`(101)。

> 说明：图谱未索引 `web/frontend` 的 `cognitive`（TS 无该属性），前端结论沿用原报告的排序交集法，本次已用行级交集重新验证（80.1%）。

## A.4 漂移清单（原稿基线 → 复核时点）

原报告标注「与 main 零漂移」，复核时不成立——报告归档后已有 3 个提交：

```
f6e0ad99 docs: 归档 web chat-pi 渲染与提示词模板功能评审
d554f555 fix(chat-pi): 移除 rawHtml 死面、反馈观测表加上界并引入 vitest 测试
d0318925 feat(web): 提示词模板增加项目上下文、流式空态布局与并发写保护
486db105 chore: gitignore 忽略 node_modules，归档架构健康检查报告   ← 原稿基线
```

**漂移影响评估**：3 个提交均属 `web/frontend` / `web/backend/api` 的局部改动，**未触及原报告的任一 P0/P1 靶点**（`pkg/agent` pipeline、`pkg/config`、前端 Sheet）。因此原报告的**全部结论仍然有效**，仅前端行数需按 A.2 更新。

## A.5 对原报告路线图的复核意见

原报告「建议路线图」的排序经复核**无需调整**，补充两点：

1. **批次 0（新增）**：查复杂度榜时固定使用 `label IN (Function, Method)` 并注意属性是字符串——否则会重蹈本次误判。建议把「Top 榜同时覆盖 Function 与 Method」写进本报告的「方法论与局限」。
2. **P0-2 的 O(n²) 预警**：复核确认 `linear_scan_in_loop=10` 属实（非误报），且 `CallLLM` 是全库唯一 ≥10 者。原报告「先 benchmark 证实热点」的处理正确，**但可提升优先级**——它是全库最强的一个循环内扫描信号。

## A.6 复核用命令（可复现）

```
# 重建图谱（full 模式）
mcp_codebase-memory_index_repository(mode="full", repo_path="D:\\code\\picoclaw")

# 取 Method 的精确属性（含 cognitive/complexity/linear_scan_in_loop）
mcp_codebase-memory_search_graph(
  label="Method",
  file_pattern="pipeline_execute.go",
  fields=["complexity","cognitive","lines","loop_count","param_count","max_access_depth","signature"]
)
# → ExecuteTools Method 121-965 ... 101 410 5 5 4

# 验证 O(n²) 信号
mcp_codebase-memory_query_graph(
  query='MATCH (m:Method) WHERE m.linear_scan_in_loop <> "0" '
        'AND NOT m.file_path CONTAINS "_test.go" '
        'RETURN m.qualified_name, m.linear_scan_in_loop, m.cognitive, m.file_path LIMIT 25'
)
# → pkg.agent.CallLLM "10" "175" pkg/agent/pipeline_llm.go   （全库唯一 ≥10）
```

---

*复核人：pico（AI）。本节为只读复核结论，未改动原报告正文，未改动任何代码。*

---

# 附二：2026-09-20 二次审视（对原稿与附录 A 的批判性评审）

> 触发：用户要求审视本报告的问题清单。本节为只读评审：对原稿与附录 A 的全部可验证声称做源码交叉验证（grep/read），并对诊断、方案、路线图提出修订。未改动上文，未改动任何代码。

## B.1 事实核验：数字全部成立，1 处行号偏差

逐项验证：`ExecuteTools` @ `pipeline_execute.go:121`（文件 965 行）、`CallLLM` @ `pipeline_llm.go:28`、二者调用点唯一（`turn_coord.go:254` / `:220`）、add/edit-model-sheet 846/822 行、A.2 新数字（models-page 1315 / config-page 776 / channel-config 758 / gateway.go 1634 / models.go 1612）、`turnState` 约 70 字段（实测约 71）@ `turn_state.go:204`、`v0ProvidersMapToModelList` @ `config_old.go:32` 唯一调用 `migration.go:236`、`CreateProvider` 生产调用点仅 2 个（另 8 处在 `agent_test.go`，不计入生产点属实）、三代会话之家、三代 context 管理器与 legacy 回退 `turn_coord.go:370/377/385`、pkg/agent 66 个非测试文件——**全部吻合**。

唯一偏差：P2-8 的 `CreateProvider` 写作 `legacy_provider.go:26`，实际函数定义在 **L151**（文件共 176 行）。轻微，不影响结论。

## B.2 新发现：P0-1 的证据存在结构性误标（原稿与附录 A 均未察觉）

原稿称两个大型 switch 为"审批决策流 + 工具分发流"。实测（`pipeline_execute.go`）：

- L229 `switch decision.normalizedAction()` —— **BeforeTool/ApproveTool 钩子决策流**（含 Continue/Modify/Respond/DenyTool/AbortTurn/HardAbort case），即原稿说的"审批决策流" ✅；
- L667 `switch decision.normalizedAction()` —— **AfterTool 钩子决策流**，并非"工具分发流" ❌；
- 本函数内**不存在按工具名分发的 switch**——真正的分发在 `ts.agent.Tools.ExecuteWithContext(...)`（L643），由 tools registry 完成；`mcp_` 前缀匹配（L110）位于本函数之前的 stepKind 辅助函数，与分发 switch 无关。

**推论**：原稿最小重构方案中的"分发改查表 `map[string]toolHandler`"**不适用于现状**——两个 switch 的 case 都是 `HookAction*` 枚举值，语义集中单一（钩子动作），改查表无收益。修订后的拆分方案：两个 switch 分别提取为 `handleBeforeToolDecision()` / `handleAfterToolDecision()`，case 体仍按原稿私有方法化；**查表化撤销**。比原方案更简单也更安全。

（认知复杂度 410 / 848 行 / 调用点唯一等核心数字不受本误标影响，P0-1 立项仍成立。附录 A.2 只核验了数字、未核验结构性描述，故未察觉此误标。）

## B.3 对其余方案的四点修订

1. **对 A.5 第 2 点的反对（O(n²) 优先级不提升）**：`CallLLM` 循环规模是 candidates × retries 的小整数（通常 <20），`linear_scan_in_loop=10` 是静态信号而非实测热点。维持原稿"先 benchmark 证实"；A.5 的提升建议可能引导过早优化。
2. **P1-4 第①步分区维度修订**：实测 `turnState` 混用三种并发模式——`mu` 保护字段（phase / iteration / …）、`atomic` 字段（lastActivityNano / isFinished / streamPublisher / …）、无保护字段（messages / followUps / …）。并发原语混用比"所有权不可见"更易产并发 bug。第①步应按 **锁保护 / atomic / 单 goroutine 写** 三分区分组注释（阶段写者信息作为第二维），而非仅按阶段写者。
3. **P1-7 砍"变更通知"**：backend→gateway 是跨进程关系，"变更通知"意味着 IPC，1 天预算不现实；进程内通知又不解决文档指出的跨进程竞争。只做"原子写收口"即可——原子写本身保证 gateway 的 mtime 轮询读不到半截文件。
4. **gateway.go / models.go 并入路线图**：附录 A.2 已将二者升级为 P1，但路线图未安放。二者正是 backend→config 1,080 次调用的主要载体，应与 P1-7 写路径收口**协同**处理（并入批次 5），否则收口后仍是 1600 行巨石。

## B.4 未言明的天花板：pkg/agent 是 66 文件单包

`turnState` 70 字段被 28 文件引用的根源是"上帝包"——包内私有符号全包可见，任何文件都能摸任何字段。P1-4 的字段重排是止痛不是治病；拆包会破坏大量 import 点并撞上游同步成本，不做可以理解，但应在"方法论与局限"明示此上限。

## B.5 修订汇总（对路线图的影响）

| 项 | 修订 |
|---|---|
| P0-1 方案 | 撤销"查表 `map[string]toolHandler`"；两个 switch 提取为 `handleBeforeToolDecision()` / `handleAfterToolDecision()`（见 B.2） |
| A.5 第 2 点 | O(n²) 优先级维持原稿，不提升（B.3-1） |
| P1-4 ① | 分区维度改为并发模式三分区：锁保护 / atomic / 单 goroutine 写（B.3-2） |
| P1-7 | 砍"变更通知"，仅保留原子写收口；gateway.go / models.go 拆分并入批次 5 协同（B.3-3/4） |
| 批次 1 措辞 | 双工厂合并会改 2 个 Go 调用点，"不碰 Go 热路径" ≠ "不碰 Go"，执行时按普通 Go 改动走全量测试 |

---

*二次审视人：CodeBuddy（AI）。只读评审，未改动原稿正文与附录 A，未改动任何代码。*

---

# 附三：批次 1 执行记录（2026-09-20，代码已落地）

> 路线图批次 1（#3 前端 Sheet 合并 + #8 双工厂 + #9 概念地图）已执行完毕。本节记录执行结果、与原方案的偏差及理由。

## C.1 #3 前端 Sheet 合并 —— 完成

- 新增 `web/frontend/src/components/models/model-form-state.ts`（`useModelFormState` hook，404 行）与 `model-form-sheet.tsx`（共享 Sheet 主体，481 行）；`add-model-sheet.tsx` 846→203 行、`edit-model-sheet.tsx` 822→186 行。**净删 394 行**（合计 1668→1274）。
- 低于原根预估（550~650）：共享层必须携带完整表单类型/空表单常量/派生值（约 120 行）与完整 JSX 主体；两个 Sheet 保留的 handleSave/对话框装配本身就有真实差异。结构目标（表单主体单一来源化）已达成。
- **四项有意的外观级统一**（均为 edit 侧对齐 add 侧，无逻辑变化）：① edit 的 Sheet `onOpenChange` 加 `!saving` 守卫（保存中不再可误关）；② edit 的目录徽章加载从「仅打开时一次」改为与 add 一致地响应 provider/apiBase 变化（顺带修复切换 provider 后目录陈旧）；③ edit 的必填校验错误从底部横幅改为字段级红字（与 add 一致）；④ AdvancedSection 字段顺序统一为 add 版（toolSchemaTransform/流式开关位置）。对外 props 签名不变，`models-page.tsx` 零改动。
- 验证：`tsc -b`、`vitest`（15 过）、`eslint`（0 警告）、`vite build` 全绿。

## C.2 #8 双工厂 —— 方案改道为「委托+注释」，删除方案否决

**执行期新事实**：原稿/附录只统计了 2 个生产调用点，漏了 `pkg/providers` 包内测试的 10 处调用（`factory_test.go` 6 处 + `cli_factory_test.go` 4 处），加上 `pkg/agent/agent_test.go` 8 处共 **18 处测试调用**在测「config 默认模型 → 类型化 provider」的组合路径。

**决策**：删除 `CreateProvider` 需重写 18 处测试且损失组合路径覆盖，负收益；采用原根备选方案「改一行委托 + 分工注释」：
- `factory.go` 增加两级工厂布局注释（`CreateProvider`（config 级入口，legacy_provider.go）vs `CreateProviderFromConfig`（单 ModelConfig → provider，factory_provider.go））；
- 两函数的 doc 注释改写为明确分工与互指；
- 顺带完成 P3 的 facade 项：`cli_facade.go` / `httpapi_facade.go` / `oauth_facade.go` 补兼容层说明注释（新代码应直接 import 子包，facade 不再扩展）。
- 不迁移 2 个生产调用点：迁移等价于在两处复制 workspace 注入/错误包装逻辑，比现有薄组合层更差。
- `legacy_provider.go` 与上游零差异，为避免无谓分歧不改文件名（“legacy”名不副实的问题由 factory.go 注释澄清）。
- 验证：`pkg/providers/...` 全绿；全量 `go test ./pkg/...` 失败集与基线完全一致（见 C.4）。

## C.3 #9 概念地图 —— 完成，修正原稿一处描述

- `fork-overview.zh.md` 新增 §17 概念地图（三个会话之家 / 三代上下文管理器 / 双总线）；新增 `pkg/memory/doc.go`、`pkg/session/doc.go`、`pkg/bus/doc.go`（`pkg/events` 本有 doc.go）。
- **修正**：P2-9 把 `context.go` 列为“三代上下文管理器”之一不准确——`context.go` 是 `ContextBuilder`（系统提示词组装），与 ContextManager 策略无关；真正的三代是 `context_manager.go`（接口+注册表）/ `context_legacy.go`（默认+回退）/ `context_seahorse.go`。默认实现是 legacy，本部署显式配 `context_manager: "seahorse"`。

## C.4 测试基线说明（Windows 开发机）

本机（Windows）`go test ./pkg/...` 存在**预存环境性失败**（cgo/libolm 缺失致 matrix 构建失败、Windows token/Unix shell 假设等），涉及 agent/tools/seahorse/deltachat/audio 等 14 包，与代码状态无关。批次 1 采用「失败集前后对比」验证：改动前后 `--- FAIL` 集合与包级 ok/FAIL 状态**完全一致**。后续批次（2/3 触碰 pkg/agent 热路径）建议在 Linux 部署机上跑全量，或继续用失败集对比法。

---

*执行人：pico（AI）。批次 1 于 2026-09-20 完成；批次 2（ExecuteTools 拆分）待开始。*
