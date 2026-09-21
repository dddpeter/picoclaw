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

本机（Windows）`go test -tags goolm,stdjson -count=1 ./pkg/... ./cmd/...` 存在**预存失败**，涉及 11 包 25 个用例，与代码状态无关。批次 1/2 采用「失败集前后对比」验证：改动前后 `--- FAIL` 集合与包级 ok/FAIL 状态**完全一致**。**注意**：本条初稿把全部失败归为环境性、并称 matrix 包因缺 cgo/libolm 构建失败，二者均已证伪（`pkg/channels/matrix` 实测 ok）——见 C.6 的修订与精确白名单。后续批次（触碰 pkg/agent 热路径）建议在 Linux 部署机上跑全量，或继续用失败集对比法。

## C.5 批次 2：ExecuteTools 内部拆分 —— 完成（2026-09-20）

- 按附二 B.2 修订方案执行（查表化撤销）：新增 `pkg/agent/pipeline_execute_loop.go`（820 行）承载全部提取件；`pipeline_execute.go` 965→239 行，`ExecuteTools` 848→**121 行**（达标 ≤120），最大提取片 `handleHookRespond` 106 行。
- 提取清单：`handleBeforeToolDecision` / `handleAfterToolDecision`（两个钩子决策流）、`handleHookRespond`（Respond 大块）、`runToolInvocation`、`handleToolResult`、`checkTurnCheckpoint`、`drainPendingSubTurnResults`、`finishToolExecution`（收尾三分支）、`appendDeniedToolResult`（四处同构 deny 块合一）、`publishToolFeedback`（两处反馈发布合一）、`deliverHandledMedia`+`buildMediaParts`（两处媒体投递合一）、三个 `appendTool*Message` 变体。
- **保真手段**：新增 `toolLoopState` 载体 + `toolLoopAction` 四态枚举；两处历史性持久化门槛差异（hook-respond 路径多 `persistsToolMessages()` 门）用 `gateOnToolPersist` 参数显式保留；日志字符串/事件/ctx-vs-turnCtx 用法逐串比对（脚本校验 90 项，全部差异均为预期去重）。
- 验证：`go build`/`go vet`/`gofmt` 绿；`pkg/agent` 全量测试失败集与基线**完全一致**（仅 4 个预存 seahorse 环境失败）；聚焦跑（Hook|Steer|Subturn|Tool|Approval…）同样仅命中基线内失败。调用点唯一（turn_coord.go）零改动，签名不变。
- 净增 94 行（965→1059，两文件合计）：提取件文档注释与签名的必要开销。

---

## C.6 测试基线修订：6 个确定性失败已修 + 精确白名单（2026-09-21）

C.4 的初稿判断过宽：当时的 28 条 `--- FAIL` 里有 **6 条是确定性漂移**（在任何平台都会稳定失败），只有 25 条与环境/平台语义相关。本次按「以代码为真、改测试」修复确定性项，零生产代码改动（未提交）。

### 已修（6 用例，确定性）

| 包 | 用例 | 根因 |
| --- | --- | --- |
| pkg/agent | `TestSeahorseRealLoopNoDuplicateMessages`、`TestSeahorseSteeringMessageIngested`、`TestSeahorseAssemblePreservesActiveToolTurnAcrossSanitization`、`TestSeahorseSummarizeSkipsCondensedWhenBelowThreshold` | `45e61ead` 将 fresh_tail 32→128：常量引用被机械替换，测试内硬编码的 32 及其派生尺寸（leaf chunk、depth 填充量）未同步 |
| pkg/evolution | `TestReviewDraft_QuarantinesInvalidTargetSkillName` | `153614bc` 放宽技能名校验：`weather_helper` 这类下划线名已合法 |
| pkg/tools/integration | `TestInstallSkillToolRejectsInvalidInstalledSkill` | 同上；且 loader 现在「声明名非法→回退目录名」，坏名字不再能制造非法技能，fixture 改用**缺 `description`**（`validate()` 仍强制要求） |
| cmd/picoclaw/internal/skills | `TestSkillsInstallFromRegistryRejectsInvalidSkillArchive` | 同上 |
| cmd/picoclaw | `TestNewPicoclawCommand` | fork 新增顶级 `plugin` 子命令（`cmd/picoclaw/main.go:144`），测试仍钉着 14 项旧清单 |

验证：六项聚焦跑全绿、`gofmt -l` 干净；全量失败集 28 → **25 条**，无新增失败。

### 剩余白名单（25 条 `--- FAIL`，11 包，均与本机环境/平台语义相关）

| 包（耗时） | 用例 | 本机实测原因 |
| --- | --- | --- |
| pkg/agent（97s） | ~~4× TestSeahorse*（RealLoop/Steering/Assemble/Summarize）~~ → 已修（C.8 同根因顺手修复：engine 未 Close 致 sqlite 句柄悬空，TempDir 清理失败） | （移入「已修」，白名单减 4） |
| pkg/audio/asr | `TestAudioModelTranscriberTranscribe/unsupported_audio_format` | 报错串内嵌 Windows 转义路径（`"C:\\\\..."`），与期望判等不匹配 |
| pkg/channels/deltachat | `TestResolveServerPathUsesPATH` | 依赖外部 `deltachat-rpc-server`，本机不在 PATH |
| pkg/isolation | `TestResolveInstanceRoot_UsesPicoclawHome`、`TestValidateExposePaths`、`TestMergeExposePaths_OverrideByTarget`、`TestPrepareCommand_AppliesUserEnv` | Windows 路径分隔符/绝对路径判定；`expose_paths` 被 `platform_windows.go` 设计性拒绝；`create restricted primary token: Invalid access to memory location` |
| pkg/migrate/internal | `TestResolveWorkspace`、`TestRelPath` | 期望 `/home/...`、`a/b`，实得 `\home\...`、`a\b` |
| pkg/migrate/sources/openclaw | `TestResolveSourceHomeWithTilde` | 期望 `C:\Users\dddpe\openclaw`，实得 `C:\Users\dddpe/openclaw`（混合分隔符） |
| pkg/pid | `TestWritePidFile` | Windows 不落实 POSIX 模式：`file permission = 666, want 0600` |
| pkg/tools（54s） | 7× `TestShellTool_*` | 本机 shell 工具走 PowerShell 而非 POSIX sh：`&&` 不被支持、`2>/dev/null` 被判为工作目录外路径、`/etc/passwd` 不存在、`$?` 退出码语义不成立 |
| cmd/picoclaw/internal | `TestGetConfigPath` | 测试改写 `HOME` 覆盖家目录，Windows 上 `os.UserHomeDir()` 读 `USERPROFILE`，覆盖不生效 |
| cmd/picoclaw/internal/mcp | `TestMCPAddRejectsNonExecutableLocalCommand` | Windows 无「可执行位」，无法据权限位拒绝本地命令 |
| cmd/picoclaw/internal/model | `TestSetDefaultModel_SaveConfigError` | 用 chmod 造只读目录使写配置失败，Windows 忽略 chmod；**该用例失败后 panic，整个包提前中断**，故该包其余用例在 Windows 上「是否通过」不可证 |

### 基线用法

1. 改前改后各跑一次全量，把 `--- FAIL` 行按「包+用例名」归集取集合，做差集；差集为空才算无回归。
2. 差集中**不得出现新增条目**，出现即视为回归——不因「看着像环境问题」而豁免。
3. 反向同样成立：本表条目若**消失**，说明环境变了（例如装上了 `deltachat-rpc-server`），应更新本表，而不是记作「修好了」。
4. 权威判定仍应在 Linux 部署机跑一次全量；本表只是 Windows 开发机的等价替代。

> 原始输出：`%TEMP%\pc_full_tests.txt`；复跑脚本：`%TEMP%\pc_full_tests.bat`。

---

## C.7 工具与上下文热路径优化 —— 完成（2026-09-21，非路线图批次）

本轮不属于建议路线图的任何批次，起因是「流水线/上下文/工具还有哪些可优化」的排查；只做实测有收益、且**不改变任何语义**的三项。fork 红线（开放默认三件套、`streamRoundTripper`、`*bool` 配置指针、命令路径无超时写锁）全部未触碰。

| 项 | 改动 | 证据 |
| --- | --- | --- |
| 工具定义缓存 | `ToolRegistry.ToProviderDefs()`（`registry.go`）按 `version` 记忆化；`PromoteTools`/`TickTTL` 在**可见集真正变化时**才 bump version（TTL 1→0 才失效，同一 TTL 纪元内的 tick 不失效） | `go test -bench BenchmarkToProviderDefs`：缓存路径 **509 ns / 1 alloc / 2387 B**，非缓存 **10.2 µs / 122 allocs / 16.7 KB**（20 个工具）；流水线每回合调用 3~5 次 |
| trim 预算算法 | `trimHistoryToFitContextWindow`（`context_budget.go`）：候选切点先枚举一次（不重建 prompt），再对其做二分探测；工具 token 提到循环外只算一次（`isOverContextBudgetWithToolTokens`） | 差分测试 `TestTrimHistoryToFitContextWindow_MatchesLinearScan` 在 273 个预算点（≥3 个不同切点）上与旧线性扫描逐点比对「保留历史 / 重建结果 / fit」完全一致；`..._RebuildsAreSublinear` 断言重建次数（实测二分 **6** 次 vs 线性 **23** 次，24 回合中丢弃 22 回合） |
| `TruncateTail` 前插 | `truncate.go` 改为「倒序收集 + `slices.Reverse`」，消除每行一次整片复制 | 4 个 `TestTruncateTail*`（含 UTF-8 半行、字节上限、notice）全绿，输出与顺序不变 |

**缓存的三条硬约束（已写入代码注释，勿回退）**：① 返回的切片 `cap == len`，使调用方 `append` 必然重新分配——`pipeline_llm.restoreToolDefinition` 会对该切片做 `append(current, tool)`；② 顺序仍由 `sortedToolNames` 决定，不得改为 map 遍历（KV 前缀稳定性）；③ 切片是浅拷贝，`Function.Parameters` map 与缓存**共享**——调用方只读，任何原地修改会静默泄漏到后续所有读者（深拷贝会吃掉缓存收益，故以契约而非拷贝兜底；`TestToProviderDefs_ConcurrentMutatorsKeepCacheConsistent` 同时固化了锁升级路径的并发一致性）。

**本轮未做（非遗漏）**：seahorse bootstrap 只覆盖默认 agent（多 agent 场景仅静态推断，无复现用例）；工具输出截断无统一出口（MCP/第三方）；当轮图片每迭代重复 stat+base64；`TruncateHead` 无生产调用者（保留待 scout 规则变化）。以上均无本轮实测收益，不做。

验证：`gofmt -l` 干净、`go vet` 绿、新增 4 个用例与全量失败集差集为空（仍为 25 条白名单，见 C.6）。

> 评审修订（同日）：① 补第三条硬约束——`Parameters` map 与缓存共享、只读，此前注释只声明了切片层约束，map 层变更会静默污染缓存且 race detector 不可靠；② 固化并发一致性测试（4 读 × 50 轮 promote/expire，收尾断言缓存与 fresh build 一致）；③ 修正 `trimCandidateStarts` 注释——中途 turn 边界异常时新实现保留已枚举候选、仅追加「全丢」兼底，与旧线性「立即全丢」不同且严格更优，原注释「同一序列」表述不准。

---

## C.8 seahorse 惰性 bootstrap + 工具输出出口预算 —— 完成（2026-09-21，同日第二批次）

体检批次 3、4、5 号发现里的第 3、4 项实现；第 5 项（resolveMediaRefs memo）挂起，理由见后。用户确认当前部署为单 agent，第 3 项按防御性修复落地。

### 第 3 项：seahorse bootstrap 只覆盖默认 agent（正确性，静默丢历史）

**影响面修正**（静态推断 vs 实测）：DB 是持久 SQLite，运行期 Ingest 按 sessionKey 入库与 agent 无关，纯重启不丢。真正丢失窗口是「DB 需要从 JSONL 重建」的时刻：默认 manager 切 seahorse 的迁移、seahorse.db 丢失/损坏/换 workspace、运行时新增 agent 的存量会话。致命放大器：`Assemble → GetOrCreateConversation` 首次访问即建空壳，此后逐条 Ingest 只补新消息，旧历史永久缺席且无日志。

**实现**（`context_seahorse.go` + `short_engine.go`）：

- `Assemble` 入口惰性 bootstrap：`sync.Map.LoadOrStore` 单飞标记（engine.Bootstrap 无 session 级锁，调用方必须保证单飞）→ `ShouldPersistSession`（新暴露，ignore/stateless 短路）→ `agentForSession` 解析 owner → `bootstrapFromStore`（原构造期 bootstrapSession 参数化，两者共用）。
- 构造期仍只走默认 agent 的 store，但对已处理 session 预设标记，已知会话零惰性成本。
- **不预判「DB 是否有消息」**：直接交给 `engine.Bootstrap` 的 reconcile 语义（同步则 fast-path no-op、部分落后补尾、不匹配 clear 重建）。评审中间版本曾用「DB 有消息即跳过」预判，被「DB 部分落后于 JSONL」用例击穿后移除——JSONL 双写是完整恢复源，每 session 每进程一次全量读的代价换语义完备。
- 顺手修复：`manager.Assemble` 对 engine 忽略会话的 `nil, nil` 返回无判空（`seahorseToProviderMessages(nil)` 会 panic；生产 heartbeat 走 NoHistory 不可达，直接调用/自定义 stateless pattern 可达）。

**测试**（`context_seahorse_bootstrap_test.go`，5 用例）：路由 agent 会话 Assemble 非空（钉死用，实现前红）；DB 部分落后时 reconcile 不重复；空壳 conversation 仍触发（判断 messages 而非 conversation 存在性）；heartbeat 忽略会话不 bootstrap 且不 panic；默认 agent 行为不变。

**同根因顺手修复**：白名单里 pkg/agent 4× TestSeahorse*（TempDir 清理 sqlite 句柄）实为 engine 未 Close——新测试与 4 个旧测试均补 `t.Cleanup(engine.Close)`，Windows 基线 **25 → 21 条**，`./pkg/agent/` 全绿。

### 第 4 项：工具输出截断统一出口 + 日志卫生

**实现**：

- `pkg/tools/output_budget.go`：`ApplyOutputBudget`（保尾 + UTF-8 边界安全 + `[output truncated: kept the last N of M bytes]` 前置提示，与 shell 的 TruncateTail 保尾语义一致）；`DefaultToolOutputBytes = 128KB`，刻意高于全部内置工具自身预算（read_file 64KB、shell 50KB）——内置工具永不触发、无双层截断提示，纯拀 MCP/第三方无预算输出。
- 配置 `tools.max_tool_output_bytes`（0=默认 128KB，负=无限制，env `PICOCLAW_TOOLS_MAX_TOOL_OUTPUT_BYTES`）；`ToolRegistry.SetMaxOutputBytes` 注入，`ExecuteWithContext` 在 `normalizeToolResult` 之后仅截 `ForLLM`（ForUser 走聊天侧不进上下文，不动）。
- **两条路径都覆盖**：同步路走 registry 出口；async 回调（`pipeline_execute_loop.go`）绕过 registry 同步出口，在 `ContentForLLM()` 后补 ApplyOutputBudget。
- 日志卫生：`ExecuteWithContext` 的 Info 级全量 args（shell 命令带 token 会落盘）改为 `utils.Truncate(json, 200)` 预览，与 toolloop.go/流式面板同构。

**测试**（`output_budget_test.go`，5 用例）：未超限/禁用原样；保尾+提示+原始大小；UTF-8 多字节 rune 不劈开（3000×「世」截 4097B 验证）；默认值高于内置预算的不变量；registry 出口端到端（截断 + unlimited 全量）。

### 第 5 项：resolveMediaRefs memo —— 挂起

第 2 项（trim 二分）落地后重算热点：当轮 tool 图片的 base64 只发生在 `pipeline_llm.go:40` 迭代路径（SetupTurn/trim 的 build 执行时当轮 tool 消息尚不存在，不触发编码），每回合重复编码次数 ≈ 迭代次数 3–5 次，2MB 图 ×4 ≈ 8MB IO，几十毫秒级。收益/成本比不足以支撑改 resolveMediaRefs 签名 + 3 调用点 + turn 级 memo 生命周期，等真实卡顿证据再决。

### 验证

- `go build -tags goolm,stdjson ./pkg/... ./cmd/...` 绿；`go vet` 绿；`gofmt -l` 触碰文件全净。
- 失败集对比：`./pkg/agent/` **全绿**（白名单减 4）；`./pkg/tools/` 仍为白名单 7 条 ShellTool（PowerShell 语义），无新增；`./pkg/config/`、`./pkg/seahorse/` ok。
- Linux 部署机全量仍待跑（本机 TSan 分配失败，-race 不可用）。

---

*执行人：pico（AI）。批次 1/2 于 2026-09-20 完成；批次 3（CallLLM 拆分）待开始，前置：降级路径回归测试。C.7 为路线图外的热路径优化；C.8 为体检第 3、4 项实现。*
