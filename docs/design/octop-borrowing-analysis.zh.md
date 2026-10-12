# Octop 借鉴分析

> 调研对象：`D:\code\Octop`（github.com/TencentCloud/Octop，v1.0.2b6，Python 3.12 + FastAPI + LangGraph，自托管多用户多 agent 助手平台）+ **`D:\code\octop-harness`**（2026-10-12 浅克隆 v1.0.2，deepagents>=0.7.16 的生产级封装——引擎本体：middleware 栈 22 件、compaction.py、tool_guard、security）。
> 本文聚焦 agent loop 与 web 聊天面（尤其 HITL 审批）与上下文管理，按"picoclaw 是否已有对应物 + 借鉴成本"筛选，与 hermes/mimo/agentscope-go 三篇同一方法。
> 调研日期：2026-10-12。方法：先读 `docs/architecture.md` + `AGENTS.md` 建立边界地图，再逐文件精读 HITL 链路（coordinator/store/format/stream_compat）、chat 路由（routes/ws/models/turn）、流投影（processor/stream_project）、WS hub、会话策略（hitl_session）、版本化历史（versioned-history.md + recorder）；picoclaw 侧逐条核实现状，防止推荐已存在的能力。

## 背景：两种审批暂停模型

Octop 的 agent loop 是**中断-检查点模型**：工具命中审批时 LangGraph 图 interrupt + checkpoint 落盘，turn 真的结束（流尾发 `hitl_required` chunk，历史不 finalize）；用户决定后走独立 HTTP 端点 `POST /api/agents/{aid}/chat/hitl/resume`（SSE 续流）从 checkpoint 恢复图，续跑中的嵌套 interrupt 再次注册进 pending store。picoclaw 是**阻塞等待模型**：`approval_hook.go` 在 hook 层同步等 `chan approvalReply`（默认 5 分钟，超时=拒绝，fail-closed），turn 挂着。

**结论：阻塞模型不换**——Go 里 goroutine 阻塞几乎免费，换中断模型需要给 agent loop 加 checkpoint 层（marker 需扩 iteration 与待执行队列），代价远超收益；且 Octop 的 `HitlPendingStore` 自己也是进程内存（`store.py` 文档串 "process-local, TTL-gc"），重启即丢卡，而 picoclaw 的审批 marker 落盘（`pkg/memory/approval_marker.go`）+ session detail `pending_approval` + stale 封存（`web/backend/api/session.go:1064`）反而更强。**值得借的是它把"审批"当一等公民的协议语义层**：会话策略、拒绝带理由、结构化多轮问询、turn 活跃状态位、入口校验、持久化顺序承诺。压缩引擎侧 picoclaw 六条自动路径全面占优，可借的是 Octop 的压缩**用户面**：手动 `/compact`、分段可见性、结果契约（§五）。

## 缺口核实总表

| 能力 | Octop（证据） | picoclaw 现状（证据） | 结论 |
|---|---|---|---|
| 会话级审批策略 | `hitl_session.py`：`ask/allow_all/allow_tools` 三档 + 工具白名单，粘在 thread 上，`ask_user_question` 永不跳过 | "always" 回答把**命中的那条 pattern** 提升为会话规则（`approval_hook.go` sessionRuleSet，6h TTL）≈ allow_tools 单元素；无 allow_all 档，无策略选择 UI | **借（§一）** |
| 拒绝带理由 | `coordinator.py:143` `build_decisions(approve=False, message=...)` → `{type:"reject", message}` 作为决策回给图 | `approvalReply{approved, always}` 仅二元，deny 干巴巴（`approval_hook.go:143`） | **借（§二）** |
| agent 主动多轮问询 | `ask_user_question` 工具挂 interrupt：dashboard `AskQuestionCard.tsx` 停靠在输入框上方的多题卡片（A–F 选项/multi_select/自由文本"其他"）；IM 逐题纯文本卡（`format.py:168`），中间答案只记不恢复 turn（`coordinator.py:235`） | 无对应工具：模型只能文本发问并结束 turn；steering 是反方向（用户→agent 注入），不覆盖 | **借（§三）** |
| turn 活跃状态位 | `ws_hub.py:103` `_active_turns` + `is_turn_active`；`ws.py:115` subscribe 即回 `turn_status(active)` 帧；thread detail 带 `turn_active` | pico WS 新连接无任何状态帧（`pkg/channels/pico/pico.go` 无 hello/turn_active）；刷新后 `isTyping` 丢失 | **借（§四）** |
| 历史持久化顺序 | `versioned-history.md`：v2 下每条保留正文**先等 DB 事务提交再转发**，失败停发且 turn 不得标 complete；取消等待在途写完 | 会话 JSONL 走 `SessionManager.Save(key)` 全量保存（`pkg/session/manager.go:210`），落盘与 outbounds 转发的先后顺序未见显式契约 | 审计点（§八） |
| 重放事件去重 | `stream_project.py:46` `_ToolProjectionState`：`saw_tool_call`（仅本 turn 真实 tool_call 后才发媒体）+ `emitted_media_ids`（按 tool_call_id 去重，防 `Overwrite(full_history)` 重放重发） | 飞书流式卡片重放是同族问题（`2026-10-04-feishu-streaming-card-bug-review.zh.md`）；pico/web 路径是否有等价守卫未逐条核实 | 审计点（§九） |
| 手动强制压缩 | `/compact` slash（`composite.py:63`）：不换 thread，压缩模型选择顺序透明（composer → thread 粘性 `/model` → fallback），结果契约 `CompactResult{ok, reason, summarized_count, display_path}` | 无手动入口：六条自动路径 + `/context` 只读展示（`cmd_context.go`）+ compact_context 模型自主工具 | **借（§五）** |
| 上下文分段可见性 | `ContextWindowRing.tsx` 7 段分桶（system_prompt/tool_definitions/rules/skills/mcp/subagent_definitions/conversation）+ 流式 hint + 30s 缓存 | `ContextUsageRing` 单百分比环；`ContextStats`（`runtime.go:32`）无分段字段 | **借（§五）** |
| 渐进式工具加载（deferred tools） | `octop-harness/middleware/tool_search.py`：全量目录不进上下文，模型见"延迟引用"（name+短描述+"[Deferred reference: call tool_search before use.]"），`tool_search(query, max_results≤5)` 按需加载 schema；per-thread `loaded_tool_names`（并发 union 合并）+ 2048 线程目录 LRU；openai/anthropic 走 provider 原生 | 无：工具定义（含 MCP 全量 schema）每 turn 注入；turn profile `tools.allow` 只能静态圈选不能延迟 | **借（§六）** |
| CJK 感知 token 估算 | `octop-harness/context_usage.py`：CJK 1.6 字/token vs 拉丁 4 字/token（"flat len/4 under-counts a Chinese prompt by more than half"）、JSON 3 字/token、消息框架 4 token、内联图 1600、媒体块 400 | `pkg/tokenizer/estimator.go` 平铺 `chars*2/5`（2.5 字/token）+ 媒体一律 256 token/项，无 CJK 区分——纯中文低估 ~36%、图片低估一个量级 | **借（§七，计量地基修正）** |
| 决策入口校验 | `coordinator.py:79` `decision_rejection_reason`：数量必须等于 action_requests、type 必须在该工具 `allowed_decisions` 内 → 400，不进图 | `/approve` `/deny` 纯文本单工具，无校验面 | 随协议升级（§十） |
| pending 重注入 | thread detail `hitl_pending` + 前端 `injectPendingHitlMessage.ts` 按 pending_id 去重重注入 | **已更强**：marker 落盘 + `pending_approval` + stale 预封存（`session.go:1064`） | 已对齐 |
| WS 断线重连 | `ws.py:189` 断开不取消在途 turn，重连 subscribe 续收（弱续流，错过的不补） | **已有**：`controller.ts:53` scheduleReconnect + generation 守卫 | 已对齐（差 §四 的状态位） |
| turn 卡死检测 | 仅 turn_active 标志 | **已更强**：前端 5 分钟提示（`use-turn-stall.ts`）+ 后端进度心跳 3 分钟 + stall watchdog 10 分钟 | 已对齐 |
| 上下文压缩引擎 | 仓内仅见引擎入口（`acompact_conversation` 在 octop-harness，deepagents SummarizationMiddleware 血统：摘要消息 + `conversation_history` 卸载） | **已更强**：六条自动触发路径 + split-turn + short_expand 回读 + compact_context 模型自主工具（见 agentscope-go 借鉴分析 §二） | 已对齐（用户面缺口见 §五，引擎细节见 §五增补） |
| 媒体内联卸载 | `octop-harness/middleware/media_offload.py`：sha 去重落盘 + 块级 `_offload_sha/_offload_path` 标记；动机注释点破要害——内联 base64 每轮重发 + **prompt cache 前缀失效** | **已有等价**：`agent_media.go` 标签化 + `load_image` 回读 + 当前 turn 图片上限（agentscope 借鉴 §三） | 已对齐 |
| 审批跨渠道同源 | coordinator 同时服务 IM（slash `/approve`、卡片）与 dashboard（SSE resume） | **已对齐**：hook → session channel outbound，任何渠道 `/approve` | 已对齐 |

## 一、HITL 会话策略三档化（优先级：高）

**来源**：`infra/agents/security/hitl_session.py`（`HitlSessionPolicy` frozen dataclass：`mode: ask|allow_all|allow_tools` + `tools` 白名单；`with_tools/without_tools` 合并语义；`_MAX_TOOLS=64`、`_MAX_TOOL_NAME=128` 防炸；`ask_user_question` 永不跳过；序列化为 thread 行上的 JSON，`parse_hitl_session_policy` 对任何脏输入降级为 ask）+ 前端 `HitlPolicyPicker.tsx`（卡片上的策略选择器）。

Octop 的语义切分值得照抄：**"本次批准"（一次性）与"会话策略"（粘性）是两个正交维度**，策略三档各自含义无歧义——ask（每次问）、allow_all（本会话全放行）、allow_tools（本会话放行这些工具）。决策体可随请求带 `hitl_policy` 更新（`models.py` `HitlSessionPolicyBody`），也可 PATCH thread。

**picoclaw 现状**：`/approve always` 只把**命中的 pattern** 提升进 sessionRuleSet（6h TTL，内存）。语义上等于 allow_tools 的单元素特例，但用户无从表达"这个会话别再问了"；且策略只在回答时隐式形成，卡片上没有显式选择器。

**嫁接方案**：
1. `approvalSessionPolicy` 结构化：`{mode: ask|allow_all|allow_tools, patterns[]}`，`always` 回答 = `with_patterns(命中 pattern)`；新增 `/approve all`（或卡片按钮"本会话总是允许"）= allow_all。deny 语义不变（deny 永远是单次拒绝，不形成策略——与 Octop 一致：拒绝不写策略）。
2. 保持现有 6h TTL 内存作用域不升级为持久化（Octop 持久化到 thread 行，代价是用户忘了关；picoclaw 个人部署 + TTL 自动重新武装更符合 fork 的开放默认取向）。
3. 卡片加策略选择器（对齐 `HitlPolicyPicker`）：三个按钮"批准一次 / 本会话总是允许此规则 / 本会话允许全部"，替代现在只藏在 `/approve always` 文本里的暗道。
4. 防御细节照抄：模式/数量上限、脏输入降级为 ask、`inherit_exec_deny_patterns` 与 hard_deny 的现有语义不动（hard deny 不可被任何策略绕过，等价 Octop 的 `ask_user_question` 永不跳过位）。

测试锚点建议：`TestApprovalPolicy_AllowAllSkipsAsk`、`TestApprovalPolicy_DenyNeverFormsPolicy`、`TestApprovalPolicy_TTLReArms`、`TestApprovalPolicy_DirtyInputDegradesToAsk`、`TestApprovalPolicy_HardDenyNotBypassable`。

## 二、拒绝带理由（优先级：高）

**来源**：`coordinator.py:143`（reject 决策可带 `message`，默认 "Rejected by user"）+ `models.py:13`（`_MAX_DECISION_MESSAGE_CHARS = 8000`）。

拒绝理由会作为决策回给图，模型收到的是**含原因的拒绝**，能理解为什么被拒并改道，而不是把"⛔ 已拒绝"当成撞墙重试。这是纯增益、零风险的一小块语义。

**picoclaw 现状**：`approvalReply{approved, always}` 无理由字段；deny 走 `appendDeniedToolResult` 固定文案。

**嫁接方案**：
1. `approvalReply` 加 `reason string`；`/deny` 命令支持尾随文本（`/deny 别动生产分支`），web 卡片拒绝按钮支持可选理由输入框（v1 payload 扩展字段，v0 文本不需要承载）。
2. 理由拼进 `appendDeniedToolResult` 的拒绝结果："用户拒绝执行：别动生产分支"。长度上限对齐 8000 字符量级即可。
3. 超时与无交互通道拒绝附原因已有（fail-closed 文案），保持。

测试锚点建议：`TestApprovalDeny_ReasonReachesToolResult`、`TestApprovalDeny_ReasonTruncated`、`TestApprovalDeny_EmptyReasonKeepsDefaultMessage`。

## 三、ask_user 结构化多轮问询（优先级：中高）

**来源**：工具 `ask_user_question` 挂 HITL interrupt（与审批同一机制，决策类型 `respond`）。四块拼图：

- **dashboard**：`AskQuestionCard.tsx`（多题一卡：stepped done/current 状态、A–F 选项键上限 6、multi_select 多选、`__other__` 哨兵展开自由文本、onDismiss 关卡结束暂停）+ `index.tsx:1617` `askQuestionDock`——卡片**停靠在输入框上方**而非消息流内，不随滚动漂走；提交即一次 respond 决策携带全部答案（`_collected_answer_message` 拼 `header: answer` 行列表，多选 `; ` 连接）。
- **IM 降级**：`format.py:168` `format_ask_card` 一次只出一题的纯文本卡（进度 `x/n`、字母/数字选项、多选提示、pending_id 脚注）；`_resolve_selection` 把 `a`/`3` 等回复 token 映射回选项 label 原文，映射不上当自由文本。
- **逐题收集**：`coordinator.py:235` `collect_ask_reply`——IM 中间答案只记录（`ask_question_index` 前进、发下一题卡、`awaiting_more=True`，turn 不恢复），最后一题收齐才拼总结消息走 respond 恢复图。避免"答了第一题 turn 就跑掉"。
- **权限钉住**：`resolve_ask_pending` 校验 `record.user_id`——群聊里只允许问向的用户作答，他人消息继续走正常路径。

**picoclaw 现状**：无 agent 主动问询能力——模型只能文本发问并结束 turn，用户下一条消息作答（跨 turn、无结构、选项靠模型自己用字母列表模拟）。steering 是反方向（用户→agent 中途注入，`steering-spec.md`：消息队列 + checkpoint 轮询），不覆盖本场景；审批钩子的管道（阻塞等待 + 回复先于 steering 路由 + 收据 + marker 落盘 + channel/chatID 钉死）恰好是所需形状的 80%。

**嫁接方案**：
1. 内置工具 `ask_user`（builtin 工具组注册；参数 `questions[{question, header?, options[{label, description?}]?, multi_select?}]`；上限：题数 5、选项 6、描述短文本；工具描述明写"仅在真正被用户决策卡住时使用"）。
2. **复用管道、独立状态**：独立 pendingAsk 等待与 marker（刷新恢复卡片）、独立收据；不并入 approvalReply/会话规则——问询永不形成任何放行策略（对应 Octop：ask 不受 allow_all/allow_tools 豁免）。
3. 路由顺序扩展为：审批回复 > **ask 作答** > steering > 命令；ask pending 时普通消息默认视为作答（DM 语义），斜杠命令仍走命令路由；逃生舱对齐 Octop onDismiss（web 卡"跳过"按钮；IM 端 `/skip`）。
4. **超时语义与审批相反**：审批超时=拒绝（fail-closed 安全门）；ask 超时/无交互通道（cron/心跳）=工具返回"用户未回答"软失败，模型自行改道收尾——非安全决策不硬拒。等待同样挂 turnCtx，hard abort 返回"turn 已中止"。
5. **watchdog 协调（picoclaw 特有坑）**：后端 stall watchdog（10 分钟）会把长等待误判卡死——ask 等待期间须按进度心跳同等对待（或注册 expected-wait），否则多轮问询会在第二题时被自杀。前端 `use-turn-stall` 阈值同理豁免。
6. 答案回注是确定性格式（label 原文 + `; ` 连接 + 自由文本），模型拿到结构化结果而非转述。

测试锚点建议：`TestAskUser_BlocksAndReceivesAnswer`、`TestAskUser_TimeoutSoftFails`、`TestAskUser_NoInteractiveChannel`、`TestAskUser_ReplyRoutedBeforeSteering`、`TestAskUser_SkipEscapeHatch`、`TestAskUser_WatchdogTreatsWaitAsProgress`、`TestAskUser_MultiSelectJoinsLabels`、`TestAskUser_NeverFormsSessionRule`。

## 四、turn_active 状态位与弱续流语义（优先级：中高）

**来源**：`infra/gateway/ws/ws_hub.py:103-111`（`mark_turn_active/mark_turn_idle/is_turn_active`——一个内存 set，不猜、不推导）+ `ws.py:112-126`（客户端 `subscribe` 成功即回 `{type:"turn_status", thread_id, active}` 帧）+ thread detail `turn_active` 字段（`octopThreads.ts`："True while a turn is still streaming server-side"）。

Octop 对"断线期间发生了什么"的承诺是诚实的**弱续流**：重连后收**后续** chunk，错过的不补，靠历史读兜底；客户端靠 `turn_status` 知道"有个 turn 正在跑"，把 spinner/输入框状态接回去。配套细节：每帧盖 `thread_id`（`stamp_thread_id`，客户端丢弃串台帧）；一个连接同时只订阅一个 thread，切 thread 自动退订。

**picoclaw 现状**：WS 自动重连已有（generation 守卫），但 pico 通道新连接无状态帧——刷新页面后前端不知道 turn 在跑，`isTyping` 归 false，用户可能重复发消息；网关侧 turn 不会被杀（turn 由网关驱动，不随 HTTP/WS 客户端生死），这点与 Octop 同构，缺的只是**状态可见性**。

**嫁接方案**：
1. pico WS 连接建立（或 session 切换）时由网关下发一帧 `{type:"turn_status", session_id, active}`：active 直接问 agent loop 的 in-flight 状态（`runTurn` 所在结构已有"turn 活跃"概念，加一个原子读即可，注意不要新拿任何锁——读锁或 atomic）。
2. session detail 接口补 `turn_active` 布尔（web backend 从网关状态查询；拿不到就缺省 false，别阻塞响应）。
3. 明确文档化弱续流语义：重连收后续不补历史；turn 结束后 JSONL 回放兜底。不要试图做 chunk 缓冲补发（复杂度陡增且收益小）。
4. 顺手项：pico 帧若尚无 session 标识字段，对齐 `thread_id` 盖章做法（防多会话标签页串台）——若已有 session_id 路由则跳过。

测试锚点建议：`TestPicoWS_TurnStatusOnConnect`、`TestPicoWS_TurnStatusReflectsInFlight`、`TestSessionDetail_TurnActiveField`、`TestPicoWS_ReconnectDoesNotCancelTurn`（若已有则补断言）。

## 五、压缩的用户面：手动 /compact、分段可见性、结果契约（优先级：中高）

**来源**：`slash/handlers/composite.py:63` `cmd_compact` + `dashboard/src/pages/Chat/components/ContextWindowRing.tsx`。Octop 压缩引擎本体在 octop-harness pip 包里（deepagents `SummarizationMiddleware` 血统：摘要消息 + `conversation_history` 卸载），仓内可见的是三个用户面设计：

- **手动强制压缩**：`/compact` 在当前 thread 上强制摘要+卸载，**不换 thread**（与 `/new` 明确区分）；压缩调用的模型选择顺序透明：composer 本 turn 选的模型 → thread 粘性 `/model` 覆盖 → 设置活跃/首个可用模型——不藏在隐藏默认里。
- **结果契约**：`CompactResult{ok, reason, error, summarized_count, file_path, display_path}`，reason 枚举（`nothing_to_compact`/`unavailable`）在 UI 上区分"没东西可压 / 引擎不可用 / 失败"三种情况；成功消息带压缩条数 + 卸载文件展示路径（用户知道原文去哪了）。
- **分段上下文环**：`ContextUsageBreakdown{max_tokens, used_tokens, segments[7]}`——system_prompt / tool_definitions / rules / skills / mcp / subagent_definitions / conversation 各自着色；环随流式 hint 实时走，点开抽屉拉 30s 缓存的精确分桶。

引擎侧细节（octop-harness 已克隆核实）：`compaction.py`——强制路径构建一次性 `SummarizationMiddleware`（`keep=("messages", 6)`，auto 路径保留 ~10% 窗口；`_force_policy` 临时换 turn 模型 + `with_retry`，用完恢复）；`MIN_SUMMARIZE_MESSAGES=2` 防"summarized 1 message"空转；卸载复用 deepagents 的 history 文件 id（`conversation_history/<sid>.md`，后续 auto 压缩追加同一文件，模型可用文件工具回读）；`removed_tokens` 估算增量 + `shrink_snapshot`——压缩不产生 provider 计数，环靠估算 delta 立即收缩而非等下一 turn。`middleware/context_usage.py`——分段在**最终出站 ModelRequest** 上量（实际请求而非组装前预估）；快照双持久化：内存按 thread_id + 盖章 `AIMessage.additional_kwargs["context_usage"]`（随 checkpoint 持久，历史回填可恢复）。

**picoclaw 现状**：引擎侧全面占优（六条自动触发路径 + split-turn + short_expand 回读 + compact_context 工具）；用户面三个缺口：
1. **无手动入口**——`/context`（`cmd_context.go`）只读展示 used/compress_at/summarize_at，用户无法主动清窗；自动压缩失效时（2026-10-04 排查过 empty-response 根因）也没有手动重试口。
2. **无分段**——`ContextStats`（`runtime.go:32`）与 web `ContextUsageRing` 都只有总量；"上下文为什么这么满"（MCP 工具定义膨胀是头号嫌疑）不可见。
3. 压缩调用用的模型（会话当前 vs 配置默认）未文档化（审计点）。

**嫁接方案**：
1. 新增 `/compact` 命令（注册进 `commands/builtin.go`）：空闲时同步执行现有压缩路径并回复结果；**turn 活跃时不阻塞**——对齐 `/new`/`/switch` 非阻塞语义，优选挂到回合尾异步压缩调度（`compact_schedule.go` 已有汇合点）而非返回 busy。结果契约照抄：压缩条数、压缩后 used/compress_at 对比、原文位置——picoclaw 的卸载等价物是 JSONL 转录本身 + short_expand 回读，回复里指明"全文在会话转录，细节可经 short_expand 回读"。force 语义细节照抄 `compaction.py`：保留最近 6 条（对比 auto 的 ~10% 窗口，让用户看到明确下降）；可压消息 <2 条时报 nothing_to_compact；压缩后 used 环按估算 delta 立即收缩。
2. `ContextStats` 加 `Segments`（picoclaw 对应分段：system_prompt（含 rules）/ tool_definitions / skills / mcp / subagents / conversation），计量点抄 Octop：包在最终出站模型请求上量实际请求而非组装前预估，JSON 密集段按 ~3 字/token 单独估（依赖 §七 修正后的估算器）；快照双持久化：内存 + 盖章到会话最后一条 assistant 消息（picoclaw 等价物：落 JSONL 转录，刷新/重启后从最近盖章恢复）。`/context` 文本与 web 环同步分段。低成本高频诊断价值：MCP 膨胀一眼可见。
3. 审计压缩调用模型选择顺序，参照 Octop（composer → thread 粘性 → fallback）文档化。
4. 与 `idle-compaction-design.zh.md`（2026-10-06 设计稿）互补不冲突：闲置压缩是自动面，`/compact` 是手动面，两者汇合到同一压缩实现。

测试锚点建议：`TestCompactCommand_IdleRunsAndReports`、`TestCompactCommand_TurnActiveSchedulesNotBusy`、`TestCompactCommand_NothingToCompact`、`TestContextStats_SegmentsPopulated`、`TestContextRing_ShowsSegmentBreakdown`、`TestCompactModelSelection_Documented`。

## 六、渐进式工具加载 tool_search（优先级：中高）

**来源**：`octop-harness/middleware/tool_search.py`（统一客户端 + provider 原生两路，`_SUPPORTED_NATIVE_PROVIDERS = {"openai", "anthropic"}`）。机制四件套：
1. **延迟引用**：全量工具目录不进系统提示——模型看到 name + 短描述 + 后缀 `[Deferred reference: call tool_search before use.]`，schema 为空（`_DeferredToolInput`）。
2. **按需加载**：元工具 `tool_search(query: "Short capability keywords, preferably in English.", max_results 1–5)` 匹配后把完整 schema 装载进本 thread 工具集。
3. **会话粘性**：`ClientToolSearchState.loaded_tool_names`（`OmitFromInput` + 并发 union 合并 reducer）——加载过的工具在后续 turn 持续可见。
4. **有界内存**：线程目录 LRU 上限 2048（`_MAX_THREAD_CATALOGS`）。

**与 §五 的关系**：分段可见性是**诊断**（MCP 膨胀看得见），渐进加载是**治疗**（膨胀根本不进上下文）。先诊断后治疗。

**picoclaw 现状**：工具定义（含 MCP 服务器全量 schema）每 turn 全量注入；turn profile `tools.allow` 只能静态圈选，不能延迟。

**嫁接方案**：
1. MCP 工具超过阈值（建议 20 个）自动启用延迟模式：注入延迟引用（name + 描述首行 + 后缀提示），内置 `tool_search` 元工具（query 匹配名称/描述，返回完整 schema 并注册进本会话工具集，会话级粘性）。
2. 阈值以下保持现状（小目录不值得多一跳）。
3. 弱模型兜底：模型直接调用延迟工具时，工具层返回"此工具为延迟引用，请先调用 tool_search 加载"而非报错失败。
4. 观测：`/context` 分段（§五）里 tool_definitions 段的下降可直接验证收益。

测试锚点建议：`TestToolSearch_DeferredRefNotInContext`、`TestToolSearch_LoadsFullSchema`、`TestToolSearch_SessionStickyLoaded`、`TestToolSearch_BelowThresholdNoDefer`、`TestToolSearch_DirectCallDeferredReturnsHint`、`TestToolSearch_LRUBound`。

## 七、CJK 感知 token 估算（优先级：高——计量地基修正）

**来源**：`octop-harness/context_usage.py:34-46`，原文注释值得整段抄：

> "Han / kana / Hangul cost far more tokens per character than Latin text: BPE vocabularies spend roughly one token per 1-2 CJK characters against ~4 Latin characters. A flat len/4 under-counts a Chinese prompt by more than half."

常量：CJK `1.6 字/token`、拉丁 `4.0 字/token`、JSON/结构化 `3.0 字/token`、消息框架 `+4 token`、内联图片块 `1600`、其他媒体块 `400`。

**picoclaw 现状**：`pkg/tokenizer/estimator.go` 平铺 `chars * 2 / 5`（2.5 字/token）+ 媒体一律 `256 token/项`：纯中文实际 ~1.6 字/token → 估算仅为实际的 64%，**低估 ~36%**（中文重度部署系统性偏晚触发压缩）；图片按 256 估而实际多模态 tile 计费可达数千 token，**低估一个量级**。

**影响面**（这是所有计量的大门）：使用率显示（`/context`、web 环）、0.75 压缩环门（`compact_schedule.go`）、`compact_context` 工具触发比、split-turn 迭代边界判断、分段环（§五）全部建立在它上面。低估 → 压缩触发偏晚 → 上下文超限错误（2026-10-04 empty-response 排查的同族诱因之一）。

**嫁接方案**：
1. `estimator.go` 按 rune 分类计数：CJK 正则范围照抄（`\u3040-\u30ff\u3400-\u4dbf\u4e00-\u9fff\uf900-\ufaff\uff66-\uff9f\uac00-\ud7af`，覆盖假名/CJK 扩展 A/汉字/兼容/半角片假名/谚文），`cjk/1.6 + latin/4.0` 加权；JSON 参数串单独 `len/3`。
2. 媒体分型：image `1600`、其他 `400`（若 picoclaw 有更准的多模态 tile 计费公式则用更准的）。
3. 修完跑全量测试：预期部分阈值断言钉死了旧估算值，需同步校准——这是行为修正不是回归。
4. 用真实中文语料对拍一次 provider 返回的 usage（cache 分桶照 `usage_record.py` 口径），把误差留在文档里。

测试锚点建议：`TestEstimator_CJKVsLatin`、`TestEstimator_MixedContentWeighted`、`TestEstimator_JSONDenser`、`TestEstimator_ImageVsOtherMedia`、`TestCompactionThresholds_WithCJKCorpus`。

## 八、"先落盘再转发"的历史顺序承诺（优先级：中，审计点）

**来源**：`docs/versioned-history.md`——v2 历史的持久化契约写得非常清楚，两段值得原样抄进设计原则：

- "每个需要保留的正文……都在向 Dashboard / IM 转发前等待数据库事务提交；写入失败时停止转发该内容并报告错误，回合不能标为成功归档。"
- 崩溃窗口语义："进程在提交前退出时，尚未提交的内容不会由这条路径发给前端；提交后、发送前退出时，可能出现'历史里已有，但前端还没收到'的内容，刷新即可读取。"

即**宁可"历史有、前端没看到"（刷新即齐），不可"前端看到、历史没有"（永久丢）**。配套：turn 状态机 `active/paused/complete/partial/failed/interrupted`，缺工具结果/解码失败不得标 complete；导出端点区分"记录还在但界面没显示"与"读取失败"。

**picoclaw 现状**：会话 JSONL 走 `SessionManager.Save(key)`（`pkg/session/manager.go:210`，全量保存）。本轮调研未逐条核实 outbounds 转发与 Save 的先后顺序及崩溃窗口的语义——不下结论，列为审计项。

**审计清单**：
1. 逐个出站路径（IM channel Send、pico WS 推送、web approval ask）确认：对应会话记录的持久化点在转发之前还是之后？
2. 若在之后：崩溃窗口内用户看过但历史缺失的消息有哪些（尤其审批 ask 本身——marker 机制已保证它可恢复，但正文消息未必）。
3. 若审计发现"转发在前"，评估把关键类别（assistant 完整消息、审批 ask）挪到先落盘的成本；JSONL 追加写本身廉价，主要是时序调整。
4. turn 终态语义：picoclaw turn marker 已有 4 字段（`turn_marker.go`），对照 `complete/partial/interrupted` 的区分度，看 split-turn/崩溃恢复路径是否已隐式覆盖。

## 九、工具媒体事件的重放去重（优先级：中，审计点）

**来源**：`stream_project.py:46-63`。两个守卫：`saw_tool_call`（本 turn 见过真实 `tool_call_chunk` 才允许从 tool_result 提取媒体外发——历史 tool_result 经 `PatchToolCallsMiddleware` 的 `Overwrite(full_history)` 重放时不得再次推送）+ `emitted_media_ids`（按 tool_call_id 去重，同一结果不重发）。另 `processor.py:1127` 在 dashboard 路径复用了同一对守卫并注释了动机。

**picoclaw 现状**：飞书流式卡片重放问题已在 `2026-10-04-feishu-streaming-card-bug-review.zh.md` 处理过（同族问题）；但"媒体/工具事件只在本 turn 真实调用后外发 + 按 call id 去重"这对守卫在 pico/web 路径是否等价存在，未逐条核实。

**审计清单**：web 图片产出（`agent_media.go` 相关出站）、pico 工具卡片、IM 媒体推送三条路径各确认一次：重放/回填触发时会不会重复推；若有疑点，照抄这对守卫（成本极低：一个 bool + 一个 map）。

## 十、结构化审批回复与入口校验（优先级：低，随协议演进）

**来源**：`models.py`（`HitlResumeBody{thread_id, decisions[], hitl_policy}`；decisions 类型 `approve/edit/reject/respond`，每工具一条、上限 16 条、message ≤8000 字符）+ `coordinator.py:79`（`decision_rejection_reason` 在 API 边界校验数量与 allowed_decisions，把"过期/坏客户端"变成可操作的 400 而不是图深处的 "model retry failed"）。

picoclaw 目前 `/approve` `/deny` 单工具文本已够用；若将来升级为结构化回复（WS 帧或 POST，v1 payload 已是半步），**入口校验的模式照抄**：决策数量与待审批项对齐、类型合法、非法即 400 带原因。`edit`（改参数重跑）与 `respond`（问句回答）不搬：前者在 picoclaw 工具循环无回注点，后者已被 steering 覆盖。

## 十一、小件清单（优先级：低）

| 项 | 来源 | 说明 |
|---|---|---|
| 输入框润色端点 | `chat/routes.py:38` `_POLISH_SYSTEM_PROMPT` | "改写草稿而非回答草稿"的系统提示 + 独立小端点，前端输入框加个 ✨ 按钮。picoclaw 已有 prompt 模板功能，可复用同一模型配置。半小时级玩具，体验加分 |
| usage 归一分桶 | `usage_record.py:55` `normalize_usage` | 把各 provider usage 归一为**互斥分桶**（uncached_input / cache_read / cache_write / output / reasoning），脏输入全量容错。审计 picoclaw 现有 usage 展示是否已含 cache 维度，缺则补齐口径 |
| 版本偏差垫片模式 | `hitl/stream_compat.py` | 上游（octop-harness/LangGraph v2）把 interrupt 挪了位置，Octop 用 import 时安装的垫片把语义补回来而不是锁死上游版本。picoclaw 同步上游时同族问题可参照：兼容层放消费侧入口、幂等安装、附最小复现注释 |
| IM 一次性投递模式 | `response_mode.py` `collapse_to_invoke_response` | 不支持流式卡片的 IM 上把流式 turn 折叠成一条终稿：缓冲 delta、工具调用前的正文视为进度旁白丢弃、终稿+媒体一次性投递。审计 picoclaw 非流式 IM 通道（如部分微信形态）是否有等价折叠；注意它明确保留 ask_agent 旁白 |
| ADR 习惯 | `docs/adr/` | 数据库双后端等决定各一页：背景/决定/后果。picoclaw 的设计文档已有同等作用，ADR 的增量价值是"一决定一页、可引用"，慢慢补即可 |
| 危险命令规则集 | `octop-harness/security/tool_guard/rules/dangerous_shell_commands.yaml`（14KB） | 三档 severity（MEDIUM 宽 catch-all 建议复核 / HIGH / CRITICAL 定向破坏）+ `exclude_patterns` + `remediation` 文案 + 威胁分类。映射：MEDIUM 素材 → ask_patterns，HIGH/CRITICAL 对照补 defaultDenyPatterns 漏项（如 fork 炸弹、`dd of=/dev/*`、`kill -9 -1`）；remediation 文案抄进 `appendDeniedToolResult`（拒绝带理由 §二 的天然素材库）。守住开放默认三件套——只补 CRITICAL 级真实毁灭性模式 |
| PII / API key 掩码 | `octop-harness/middleware/pii.py` | API key 格式库（`sk-`/`sk-ant-`/`AKIA`/`AIzaSy`/`hf_`/`api_key=` 后 hex）+ 中国手机号 + 18 位身份证 MOD 11-2 校验；策略"前缀+末 4+星号"。落点：exec 输出清理管线（`pkg/tools/output_clean.go`，保持 never-worse 守门）——`cat .env`、带 key 的报错日志进上下文是真实风险 |
| 多模态感知选模 | `octop-harness/middleware/model_router.py` | 默认选择器检测最新用户消息含非文本块 → 路由多模态模型；优先级链 configurable.model → 用户 hook → 内建检测 → default → 首个可用。审计 picoclaw default chain 是否已有按内容自动切多模态，缺则补 |

## 十二、评估后不借鉴的部分

| 项 | 理由 |
|---|---|
| 中断-检查点暂停模型 | 见"背景"。Go 阻塞 goroutine 免费；checkpoint 化 marker 需扩 iteration 与待执行队列，复杂度高且收益被"继续"语义摊薄；Octop 的 pending store 同样进程内存，重启丢卡，durability 并不占优 |
| 批量 decisions（一次审多个并行工具） | picoclaw 工具串行执行、逐次 ask，粒度更细实现更简；批量审批反而扩大误批半径 |
| `ask_user_question` 走 interrupt 机制 | 不搬其**机制**（picoclaw 阻塞模型更简），但能力本身改为内置 `ask_user` 工具借鉴（§三）——初稿误判为"steering 已覆盖"，已纠正：steering 是用户→agent 注入，方向相反 |
| `edit` 决策（改参数重跑） | 工具循环无参数回注点；拒绝带理由（§二）已覆盖 90% 的改道需求 |
| deepagents `SummarizationMiddleware` 引擎 + `conversation_history` 回读工具 | 引擎在 octop-harness pip 包内不可考；picoclaw 六条自动路径 + split-turn + short_expand 回读覆盖更全（见 agentscope-go 借鉴分析 §二）；仓内可借的只有用户面（§五），卸载文件等价物 = JSONL 转录 + short_expand |
| `octop memory slim` 记忆库瘦身（VACUUM/去重，`memory-slim.md`） | 作用于 octop-memory SQLite 库文件，picoclaw 无同构长期记忆库；JSONL 转录体积可另议 |
| deepagents 中间件框架 / LangGraph 生态 | picoclaw Pipeline + hooks 被 3.6 万行测试钉死（见 agentscope-go 借鉴分析）；harness 的 22 件 middleware 中 picoclaw 无对应实现的只有渐进工具加载（§六 已单列），其余均有等价或更强实现 |
| teams inbox（agent↔agent 邮箱协作） | `octop-harness/teams/` 的 InboxManager + TeamProcessor 协议面向多专家平台；picoclaw `delegate.go` 子代理委托已覆盖个人部署场景，引入邮箱生命周期纯增重 |
| 多用户 JWT + 行级隔离 | 产品定位差异：Octop 面向家庭/小团队，picoclaw 个人部署 |
| LangGraph/四包拆分架构 | 语言与依赖模型不同；picoclaw 单仓被 3.6 万行测试钉死，拆分纯增负担 |
| 专家市场/MBTI/插件生态/浏览器与远程桌面 | 产品面功能，超出网关关注域，且多为 octop-harness/browser 包内实现，Octop 仓库里只有组装胶水 |

## 落地顺序建议

1. §七 CJK 估算修正（计量地基，最小 diff 最大正确性收益——使用率、压缩门、分段环全建立在它上面）→ 2. §二 拒绝带理由 → 3. §一 会话策略三档 → 4. §五 /compact + 分段（分段建立在修正后的估算上）→ 5. §六 渐进工具加载（MCP 膨胀的治疗）→ 6. §四 turn_active → 7. §三 ask_user → 8. §八/§九 审计（只读调查，产出结论后再定是否动代码）→ 9. §十 随未来协议升级自然发生 → §十一 随手。

每步都不得触碰开放默认三件套与 deny patterns 硬拒语义；`/deny` 永远可用、hard deny 永不被策略绕过是本设计中不可移动的地板。
