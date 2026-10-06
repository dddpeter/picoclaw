# AgentScope-Go 设计借鉴分析

> 调研对象：`D:\code\agentscope-go`（`github.com/agentscope-ai/agentscope-go/v2`，阿里 AgentScope 概念的 Go 移植，Go 1.25，Apache-2.0，非测试约 6 万行）。
> 本文档梳理其设计中值得 picoclaw 吸收的要点，按优先级排序，并注明与 picoclaw 现有实现的对照。
> 调研日期：2026-10-05。方法：三轮并行探索（整体能力面 → pkg/agent 现状盘点 → 借用设计细节挖掘），关键断言逐条人工复核。

## 背景

缘起是评估"agent 底层整体换成 agentscope-go 以减小项目规模"，结论是**不换**，三条硬理由：

1. **无逐 token 流式**：其 `UnifiedAgent.ReplyStream` 是块级事件流，`callModel` 走非流式 `Chat`（项目自己的 CLAUDE.md 承认）——picoclaw 的流式卡片/sticky 降级/进度心跳全部建立在真流式上。
2. **fork 积累会全部丢失**：pkg/agent 的 2.4 万行非测试代码中约一半是框架级定制（学舌三层防线、split-turn、六路压缩、重启恢复、封口不回滚……），被 3.6 万行测试钉死，换框架等于全部重写再重踩一遍坑。
3. **规模不降反升**：引入 6 万行依赖 + otel/prometheus/qdrant/MQTT/钉钉 SDK 重依赖树；且该项目为单人维护、模块路径刚迁移、自评 30+ 包 Experimental（Stable 层仅 message/model/agent/tool/permission/formatter/errors），与上游 sipeed/picoclaw 的同步也会永久分叉。

本文是评估的后半程：**不换核心，借设计补缺口**。按"picoclaw 是否有对应痛点 + 借鉴成本"筛选，与 hermes/mimo 两篇借鉴分析同一方法。

一个前提认知：agentscope-go 与 picoclaw 在可靠性方向各有强弱——它的重试/降级（流式中途无 fallback、token 计数按字节÷4 估算）弱于 picoclaw 现有实现，循环检测/watchdog 与 picoclaw 大体等价。**真正值得借的是交互语义层（HITL 审批、模型自主压缩、诚实性原则），而不是执行与可靠性层。**

## 缺口核实总表

| 能力 | agentscope-go | picoclaw 现状（证据） | 结论 |
|---|---|---|---|
| HITL 工具审批 | permission 引擎 + `RequireUserConfirmEvent` 阻塞等待确认 | 扩展点齐备但零实现：`ToolApprover` 钩子（`pkg/agent/hooks.go:89`）存在、工具循环每步调用（`pipeline_execute.go:199`），但没有任何内置 approver，也没有"问用户再继续"的通道 | **借（§一）** |
| 模型自主压缩 | `compress_context` 工具，阈值取自动压缩一半，诚实返回是否真的压缩 | 六条全自动压缩路径（proactive/上下文错误/回合尾异步/空闲/迭代边界/split-turn），`pkg/tools` 无任何压缩工具，模型无主动入口 | **借（§二）** |
| 工具结果落盘引用 | `Offloader` 接口：截断必落盘 + `<system-reminder>offloaded to '%s'</system-reminder>` 引用 | 仅 exec 落盘（`pkg/tools/shell.go:757-766`）；其余工具走 `ApplyOutputBudget` 纯尾部截断（`output_budget.go:34-55`） | **借（§三）** |
| 上下文图片上限 | `MaxImageNum`：超限最旧图片换文字提醒（copy-on-write，`compress.go:750-900`） | 历史图片已标签化 `[image:/path]`（`agent_media.go:60-149`），但当前 turn 的工具结果图 base64 进上下文且**无数量上限** | **借（§三）** |
| 结构化摘要 | 五字段 schema（`compress.go:19-44`）+ 模板渲染 | legacy 自由文本（`context_legacy.go:380-411`）、seahorse 纯文本约定（与 short_expand 咬合，不动）、split-turn 半结构化中文 | 部分借（§二附带） |
| checkpoint 恢复续跑 | 批边界 checkpoint + 崩溃恢复 + 显式"非 exactly-once"契约 | 封口 + 通知 + 用户回复"继续"开新 turn（§12 既定 UX），marker 仅 4 字段 | 只借契约文档（§四） |
| 成本追踪/预算 | 定价表 + 硬中止/诱导收尾/只记账三种哲学 | 零定价，token 逐 turn 展示已有 | 不立项（§五） |
| middleware 洋葱链 / loop.Loop 接口化 | 8 钩子 + 全接口化可组合循环 | Pipeline + hooks 等价物已在且被测试钉死 | 仅作重构参照（§五） |
| 回放/评估（RunJSONL） | runlog 中间件 + replay 工具 | `runtime_event_logger.go` 仅结构化日志桥 | 不立项（§五） |

## 一、HITL 工具审批（优先级：高）

**来源**：`permission` 包 + `unified_agent.go:839-941`（executeToolCallWithPermission / waitForConfirmation）。

agentscope-go 的设计是一个"权限三明治"：

- **模式引擎**（`engine.go:78-252`）：Default/AcceptEdits/Explore/Bypass/DontAsk 五模式，规则分 Allow/Ask/Deny 三张表，命令级 glob 匹配；Ask 决策自动附带建议规则（"允许 `git push origin*`？"）。
- **工具自评**（`permission/checker.go:5-27`）：每个工具实现 `CheckPermissions/CheckReadOnly/MatchRule/GenerateSuggestions`——只读性判断和命令 glob 匹配由最懂工具的工具自己提供。
- **调用点强制**（两条路径两种 Ask 后果，刻意设计）：agent 路径发 `RequireUserConfirmEvent` 后**阻塞等确认**，确认可附规则即时 `AddRule`（会话内生效，随 checkpoint 持久化）；无交互通道的 loop 路径**fail-closed**——直接返回错误结果。原文原则："Do not silently approve a tool because an execution path cannot request confirmation."
- 无人值守的正解是前置选 DontAsk 模式（Ask 一律转 Deny），而不是给确认加超时放行。

**picoclaw 现状**：扩展点已经齐备，缺的只是最后一环——

- `ToolApprover` 接口（`hooks.go:89-91`）+ `HookManager.ApproveTool`（`hooks.go:538-562`，任一 approver 拒绝即拒，**没有 approver 时默认放行**）；
- 工具循环在每个工具执行前同步调用（`pipeline_execute.go:199`），拒绝走 `appendDeniedToolResult`：模型收到含原因的拒绝通知，turn 继续——这正是 agentscope 的 Deny 语义；
- 审批超时默认 60s（`hooks.go:21`），可配 `hooks.defaults.approval_timeout_ms`（`config.go:278`）；
- **内置钩子注册表已存在**（`hook_mount.go:56` `builtinHookRegistry` + config `hooks.builtins`），当前全仓 0 注册——审批钩子将是第一个用户；
- 注意 `hooks.go:30` 的 SECURITY 注释：BeforeTool 的 `respond` 决策**绕过 ApproveTool**，审批设计不能依赖"所有路径都过审批"。

**嫁接方案**：

1. **内置审批钩子 `approval`**：以 BuiltinHookFactory 注册（config 形如 `hooks.builtins.approval = {enabled, config}`）。规则集 `ask_patterns`（命令 glob / 工具名，语义对齐现有 `custom_deny_patterns` 的匹配方式），求值顺序 **defaultDenyPatterns（硬拒，不动）→ ask_patterns 命中 → 放行**。`ask_patterns` 默认空 = 行为零变化，守住开放默认三件套（fork-overview §同步注意事项）。
2. **通道复用**：审批请求经现有外发口（`agent_outbound.go`）发到会话所在 channel（飞书卡片 / web / pico 文本）：「即将执行 `git push`，回复 `/approve` 或 `/deny`」。picoclaw 本身就是聊天网关，这是比 agentscope 更天然的落点。
3. **回答路由**：会话存在 pending 审批时，入站消息先过审批解析器（`/approve`、`/deny`，可扩展中文别名），不匹配则走原 steering 路径（`agent.go:210-236`）。审批等待即阻塞在 ApproveTool 的同步窗口内，建议默认超时从 60s 提到适合 IM 节奏的 300s（仅审批钩子自身配置，不改全局默认）。
4. **超时 = 拒绝**（fail-closed，借 agentscope 原则）；**无交互通道的会话（cron/心跳触发、无绑定 channel）Ask 命中时立即拒绝并说明原因，不空等超时**——这是 agentscope DontAsk 模式语义的特例化，也是五模式里唯一有真实对应需求的分支。拒绝/超时都走 `appendDeniedToolResult`，模型可自行改道。审批等待必须挂在 turnCtx 上：hard abort 时立即返回拒绝，不留僵尸等待。
5. **二期**：「本次会话总是允许」——确认时可附规则，会话内存生效并落 JSONL note（picoclaw 落盘比 agentscope 的会话内 Engine 更简单）。

不搬五模式引擎：五个模式里四个是 picoclaw 已有旋钮可拼出的姿态——**Default 的 Ask 层即本节 `ask_patterns`（唯一真缺口）**；AcceptEdits 弱于开放默认三件套的有意开放；Explore ≈ turn profile `tools.allow` 圈只读工具集；Bypass ≈ 现状默认开放（deny patterns 本就不可绕过）；DontAsk ≈ 无交互通道会话的立即拒绝特例（第 4 点）。整体引入会形成第二套权限优先级模型，与现有五个门禁面（deny patterns 三旋钮 / `protect_system_paths` / `restrict_to_workspace` / turn profile allow / agent allowlist）并存，且其 ask-first 默认姿态与开放默认三件套冲突。也不需要它的批量确认分发——picoclaw 工具循环是顺序执行，审批天然串行。若将来出现"会话中一键切姿态"或会话级规则集膨胀的需求，再以该引擎为参照收敛成统一门禁。

测试锚点建议：`TestApprovalHook_AskPatternMatches`、`TestApprovalHook_TimeoutFailsClosed`、`TestApproval_NoInteractiveChannelDeniesImmediately`、`TestApprovalReply_RoutedBeforeSteering`、`TestApproval_HardAbortReturnsImmediately`、`TestApproval_EmptyPatternsNoBehaviorChange`。

## 二、模型自主压缩工具（优先级：中高）

**来源**：`tool/compress_context.go` + `compress.go:89-104,192-201`（AgentDrivenTriggerRatio / compressContextForTool）。

agentscope-go 的工程要点：

- 工具 schema 仅一个可选字段 `reason`；`ConcurrencySafe=false`（重写共享会话状态，禁并行）；`CheckPermissions` 直接 Allow（"compress_context only rewrites the agent's own context"，内部维护动作不触发确认）；
- **阈值取自动压缩的一半**（TriggerRatio/2）——自动压缩在 TriggerRatio 已触发，同值则工具永远无事可做；
- **诚实性原则**（最值得抄的部分）：`Compressed=false` 不是错误——"Claiming 'context compressed' when nothing happened teaches the model that details it can no longer see are still in context"；压缩了则明说"细节已不可逐字可见，需要就重读文件/重跑工具"；
- 与自动压缩不打架：同一条压缩实现 + triggerOverride，两次执行天然串行，低于阈值 no-op。

**picoclaw 现状**：六条自动路径触发点齐备（首次调用前 `pipeline_setup.go:55`、上下文错误重试 `pipeline_llm.go:495`、回合尾异步 + 0.75 使用率门 `compact_schedule.go`、空闲扫描 `idle_compact.go`、迭代边界 + split-turn `iteration_compact.go:54-71`），但模型全程无主动入口。picoclaw 有一处**强于**对方的基础：seahorse 压缩与 `short_expand` 回读联动——被压缩的细节可按需展开，不是单向丢失。

**嫁接方案**：

1. 新内置工具 `compact_context`（无参或可选 `reason`），注册进 seahorse 工具组（与 `short_expand` 同列，避免被 turn profile `tools.allow` 裁掉——对照 `short_expand` 的注册方式）。
2. **不在工具执行中直接改写历史**——活动 turn 中途改历史正是 split-turn 机制要规避的场景。工具只置一个"下次 LLM 调用前强制压缩检查"标志，由现成检查点 `compactBeforeLLMCall`（`iteration_compact.go:13-134`，调用点 `pipeline_llm.go:124`）加 force 参数消化。
3. 触发阈值 = 使用率门一半（0.75/2 ≈ 0.375，可配 `compaction.tool_trigger_ratio`）；防滥用：每 turn 至多 2 次。
4. 返回文案遵循诚实性原则：真压缩了 →「旧消息已摘要化，细节可用 short_expand 回读」；未达阈值 → 明说本次未压缩、未丢失任何内容。压缩失败 fail-open（返回错误文本，不中止 turn）。
5. 附带升级（低成本）：split-turn 摘要 prompt（`iteration_compact.go:198-209`，已半结构化中文）与 legacy 摘要 prompt（`context_legacy.go:380-406`，自由文本）对齐 agentscope 五字段模板（task_overview / current_state / important_discoveries / next_steps / context_to_preserve，`compress.go:19-44`）。**seahorse 不动**——其"Plain text only, no headings"约定与 `Files: none` / `Expand for details` 尾注和 short_expand 咬合，是自成体系的契约。

测试锚点建议：`TestCompactContextTool_ForcesNextBoundaryCheck`、`TestCompactContextTool_HonestNoOp`、`TestCompactContextTool_DoesNotConflictWithSplitTurn`。

## 三、通用工具结果落盘引用 + 上下文图片上限（优先级：中）

**来源**：`Offloader` 接口（`unified_agent.go:214-218`）+ `truncateToolResult`（`:1177-1194`）；`limitContextImages`（`compress.go:750-900`）。

agentscope-go：任何截断必落盘（`_offloaded/tool_result_<id>.txt`），上下文里留 `<system-reminder>The remaining content has been offloaded to '%s'.</system-reminder>`；图片超限时最旧的换文字提醒，整个操作 copy-on-write 防与并发读者竞争。

**picoclaw 现状**：exec 已经在做正确的事——截断触发时 `persistFullOutput` 写 `<workspace>/tmp/shell-output-*.log` 并附 `Full output: <path>`（`shell.go:757-766,787`）；但这是 exec 的私有实现，MCP 等其余工具超预算只纯截断（`output_budget.go:34-55`，默认 128KB）。图片侧：历史消息图片永不进像素（只注 `[image:/path]` 标签，模型用 `load_image` 回读，`agent_media.go:60-149`）——但**当前 turn 的工具结果图 base64 进上下文，无数量上限**，一次多图任务就能顶掉大量窗口。

**嫁接方案**（两项都是纯增量，无行为冲突）：

1. **落盘引用泛化**：`ApplyOutputBudget` 增加可选 offload 回调——截断触发时把原文写 `<workspace>/tmp/tool-output-*.log` 并附 `Full output: <path>`（复用 exec 的措辞与目录约定；exec 改为走同一实现）。落盘失败退回纯截断，遵循 never-worse 精神（不因落盘失败而让工具失败）。
2. **图片上限**：`resolveMediaRefs` 对当前 turn 合成消息里的 base64 图加数量上限（配置 `max_context_images`，建议默认 8），超限**最旧的换成 `[image:/path]` 标签**——picoclaw 已有标签 + load_image 回读闭环，等价于 agentscope 的 offload 提醒且更强（可回读，非单向丢弃）。

测试锚点建议：`TestOutputBudget_OffloadsTruncatedOriginal`、`TestOutputBudget_OffloadFailureFallsBackToTruncate`、`TestMediaRefs_CapsCurrentTurnImages`。

## 四、checkpoint 契约（仅借文档，不借机制）

agentscope-go 把崩溃语义写成显式契约（`checkpoint.go:23-29`），值得原文抄进 fork-overview §12 与 `restart_recovery.go` 注释：

> A crash MID-BATCH resumes by re-executing the whole batch. Tool side effects are therefore NOT exactly-once across crashes; prefer read-only/idempotent tools in crash-sensitive deployments. Crashes while parked are lossless.

配套的版本单向门（只拒"来自未来的 schema"，旧版本向前兼容加载）与 detached snapshot（marshal/unmarshal 隔离快照防撕裂）也是好范式。

**不做恢复续跑**，理由：picoclaw turn marker 仅 4 字段（`turn_marker.go:13-18`）；在途 LLM 生成从未持久化、不可恢复；IM 异步通道上"封口 + 通知 + 回复继续"是既定 UX（fork-overview §12）；恢复续跑需要 marker 存 iteration 与待执行队列，复杂度高且收益被"继续"语义摊薄。若将来为通知文案扩展 marker（如加 Iteration 字段），遵循版本单向门即可。

## 五、评估后不借鉴的部分

| 项 | 理由 |
|---|---|
| 整体替换执行核心 | 见"背景"三理由；本文仅取设计 |
| permission 五模式引擎 | 五个模式里四个是现有旋钮可拼出的姿态（映射见 §一"不搬五模式引擎"段），唯一真缺口 Ask 层已单列 §一；整体引入会形成与现有五个门禁面并存的第二套优先级模型，且 ask-first 默认姿态与开放默认三件套冲突（2026-10-05 确认：权限方向只补 Ask） |
| middleware 洋葱链 / loop.Loop 接口化重构 | Pipeline + hooks 等价物已在且被 3.6 万行测试钉死；主循环重构风险大收益小。其接口切面（ModelCaller/ToolExecutor/ContextManager/ExitCondition 全注入）留作 pkg/agent 未来瘦身时的参照系 |
| 成本追踪（定价表 / CostLedger / 预算中间件） | 个人部署收益低，逐 turn usage 展示已有；定价表是持续维护负担。其"硬中止 vs 诱导收尾 vs 只记账"三种预算哲学记入 §六 作概念储备 |
| RunJSONL 回放/评估 | 测试密度已足够（729 个测试函数钉死 fork 行为）；需要时 `runtime_event_logger.go` 可自然演化 |
| otel tracing / prometheus metrics / webui / channel / team / a2a | 重依赖或场景不符，与"减小项目规模"的初衷相反 |
| RepetitionBreaker / ReplyWatchdog / MaxIters 强制总结 | picoclaw 有更强等价物：turn_health（bash_retry/edit_streak，MiMo 借鉴）、stall watchdog 两级升级（idle→graceful→hard）、auto-continue 续段（比强制总结更适合 IM 场景） |

## 六、仅借概念

| 概念 | 来源 | 对 picoclaw 的落点 |
|---|---|---|
| 诚实性原则：压缩/截断的结果必须如实告知"细节已不可逐字可见" | compress_context 工具文案 | §二工具返回语；顺带审查现有截断注记（`output truncated: kept the last %d of %d bytes`）措辞是否让模型明白细节可从落盘文件回读 |
| fail-closed 原则：无法请求确认的路径不得静默放行 | CLAUDE.md:66-68 | §一审批超时=拒绝；外部进程钩子（process hook）文档中的对应提醒 |
| 预算三种哲学：诱导收尾（tool_choice=none + 收尾提示）/ 硬中止 / 只记账 | budget.go / cost_tracker.go / cost_ledger.go | 若将来做 turn 级 token 预算，"诱导收尾"比硬中止更贴合 IM 场景（用户看到的是自然收束而非报错） |
| checkpoint 版本单向门 + detached snapshot | checkpoint.go:65-81 | 若将来扩展 turn marker schema |
| 压缩切分不变量：call/result 必须同侧；in-flight call 不得入摘要 | compress.go:464-563 | split-turn 已遵守同侧原则；"活动 turn 尾部独占窗口"即 in-flight 保护的等价物，可在 `iteration_compact.go` 补注释明示这两条不变量 |

## 落地建议

1. **第一项**：HITL 审批钩子——新文件 `pkg/agent/hook_approval.go` + builtin 注册 + 通道外发 + 入站审批解析；2-3 天。先做文本协议（`/approve` `/deny`），卡片按钮随飞书卡片迭代。
2. **第二项**：`compact_context` 工具 + `compactBeforeLLMCall` force 参数 + split-turn/legacy 摘要 prompt 五字段化；1 天。
3. **第三项**：`ApplyOutputBudget` offload 泛化 + `resolveMediaRefs` 图片上限；1 天。
4. §四 契约注释随下一次触碰 restart_recovery 时顺带；§六 概念各自随对应模块迭代参考，不单独立项。

三项互不依赖，第一项价值最高（它是 config 里 `hooks.builtins` 与 `ToolApprover` 扩展点等待已久的第一个消费者）。

## 实施状态

- 2026-10-05：调研完成，本文档存档。
- 2026-10-05：评审确认——权限方向**只补 Ask 层**（不搬五模式引擎）；§一 增补无交互通道立即拒绝语义（DontAsk 特例化）与五模式不搬论证，§五 同步。
- 2026-10-05：**四项全部实施**（fork-overview §23）：
  - §一 HITL 审批钩子：`pkg/agent/approval_hook.go` + `hooks.builtins.approval` 配置 + 消息泵 steering 前路由 + 无通道/超时/中止 fail-closed + 挂载时自动抬高 HookManager 审批超时。7 个测试锚点全绿。实现与设计的偏差：无"二期会话内总是允许"（未做，确认规则语义时再立项）；审批提问措辞用中文（部署主用飞书）。
  - §二 compact_context：工具 + force 标志（`compactBeforeLLMCall` 消费，reason=`model_tool_request`）+ 阈值 `compact_tool_trigger_ratio`（钳到 ≤ 全门槛）+ 每回合 2 次限流 + split-turn/legacy prompt 五字段化。与设计的偏差：用量不可估（usage==nil）时改为保守 no-op（不盲目请求），更符合诚实性原则。
  - §三 offload 泛化（`ApplyOutputBudgetWithOffload` + registry `SetWorkspaceDir` + 异步出口同权）+ 图片上限（`resolveMediaRefs` maxImages 参数，8 调用点；预扫描丢弃集，最旧换 `[image:/path]` 标签）。
  - §四 契约注释写入 `restart_recovery.go` 头注。
  - 配置文档：configuration.zh.md 新增「工具审批门禁」节、`compact_tool_trigger_ratio` 行、「上下文图片上限」节。
- 2026-10-05：**评审收口**（外部审查报告，M1/M2/M3/L1/L2/L3/L5/N1/N2/N3 全部处理；L6 与 fork-overview"占位符"两条经核实为误报——agent_init 闭包是逐调用求值，`<2026-10-05>` 符合表格惯例）：
  - M1 审批回复来源校验：pending 改带提问目标 channel/chatID，跨渠道/跨会话的 `/approve` 不生效（照常进 steering）；
  - M2 `HookManager.approvalTimeout` 锁纪律统一（ConfigureTimeouts 加锁写、读取走 RLock 快照）；
  - M3 offload 治理：不改"原文落盘"语义（与 exec 一致且全局过滤已被部署关闭，文档明示"未过滤原文"），新增启动清扫 >7 天的 tool-output-*/shell-output-* 落盘文件；
  - L1 命令带尾随内容（`/approve 请继续…`）不再当作审批回答；L2 超时抬高移到挂载成功之后；L3 超过 maxSize 必被编码器跳过的图不占 cap 名额；L4 双扫描以注释明示接受；L5 工具文案从"已排程"软化为"已请求（失败则上下文不变）"；N1 Description 写明 no-op 也计数；N2 裸 `tool:` 前缀构造期报错；N3 文档补"迟到 /approve 不补生效"。
  - 新增锚点：`TestApprovalHook_BareToolPrefixRejected`、M1/L1 用例并入 `TestApprovalReply_RoutedBeforeSteering`、`TestMediaRefs_CapIgnoresOversizedImages`、config `TestGetMaxContextImagesDefaults`。`TestCappedOutputBuffer_ConcurrentWrites` 确认为负载时序抖动（单独跑 3/3 绿，与改动无关）。
- 2026-10-05：**§一 二期"会话内总是允许"实施**：回答 `/approve always`（或 `总是允许`）批准本次并把命中的 ask 规则提升为会话级自动放行（内存 + 6h TTL，重启失效；`/deny` 无 always 形式）。`matchedAskPattern` 返回命中规则文本、`pendingApproval` 携带 pattern、`sessionRuleSet` 持 TTL 规则表。锚点 `TestApproval_SessionAlwaysAllow`。原实施状态中"无二期"的偏差自此作废。
