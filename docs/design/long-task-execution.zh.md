# 长任务执行优化（四件套）设计文档

> 状态：已实施（2026-09-18，四项全部落地；正文已按实现回填，`fork-overview.zh.md` 已同步）
> 日期：2026-09-18
> 范围：① exec 超时引导与 per-call 超时；② `max_tool_iterations` 到顶自动续 turn；③ 网关重启后中断会话的封口与恢复提示；④ 长任务进度心跳。
> 动机：个人部署中长任务（大重构、长构建、跨夜任务）的四个实际痛点——长命令被默认 60s 超时杀掉后模型盲目重试；步数到顶 turn 直接终止；网关重启后进行中的任务静默丢失；长时间无输出时用户无法分辨"在干活"与"卡死"。

## 0. 现状盘点（**实施前基线**，代码锚点）

> 下表描述的是四件套实施前的状态；① 落地后 exec 超时行、shell.go 行号已变化。

| 环节 | 现状 | 锚点 |
|---|---|---|
| 迭代上限 | 默认 50，到顶后 turn 以 `toolLimitResponse` 收尾（提示改配置），任务被截断 | `pkg/config/defaults.go:43`、`pkg/agent/turn_coord.go:287-289`、`pkg/agent/agent.go:129` |
| turn 顶层壳 | `runAgentLoop` 创建 turnState → `runTurn` → 发 followUps → 发最终响应 | `pkg/agent/agent.go:540-660` |
| 悬空封口 | `sealDanglingToolCalls` 给尾部悬空 tool_calls 补合成结果；/stop 与看门狗路径已用 | `pkg/agent/steering_abort.go:37-76` |
| 会话枚举与 scope | `ListSessions()` 枚举全部会话；scope 含 channel/peer（web 会话列表据此显示渠道徽标） | `pkg/memory/jsonl.go:939`、`web/backend/api/session.go:288-296` |
| 主动外发 | heartbeat 用 `GetLastChannel` + outbound 发布；runAgentLoop 最终响应走 `bus.PublishOutbound` | `pkg/heartbeat/service.go:177-179`、`pkg/agent/agent.go:617-640` |
| exec 超时 | 同步 run 默认 60s（`tools.exec.timeout_seconds`）；超时返回只有 "Command timed out after …" + 部分输出，**无 background 引导**；schema 声明的 per-call `timeout` 参数**无任何消费点（死参数）** | `pkg/tools/shell.go:370-372（声明）`、`:495-530（runSync 只读 t.timeout）`、`:582-592（超时返回）` |
| background 会话 | `background=true` 不受超时限制，poll/read/write/kill/send-keys 全套管理；工具描述已写 "Use background=true for long-running commands" | `pkg/tools/shell.go:328-330`、`:701+（runBackground）` |
| 流式卡面板 | `ToolStep{Running:true}` 发布运行中条目；KindText 步骤归档为「💬 本轮说明」——**新增文本步骤无需 feishu 端改动** | `pkg/bus/bus.go:130-152`、`pkg/agent/pipeline_streaming.go:550-595` |
| 上下文管理 | 每段 turn 开始 Assemble（proactive 压缩）；溢出同步压缩；turn 后异步摘要 | `pkg/agent/context_budget.go`、`context_legacy.go` |

## 1. ① exec 超时引导 + per-call 超时实现（已实施）

### 动机
模型经常直接同步 run 长命令（build/test/爬虫），60s 被杀后原样重试，再被杀，直到触发 bash_retry 循环警示。工具描述里虽有 background 引导，但错误返回是引导模型改行为的最佳时机（相关性最高）。同时 schema 里的 `timeout` 参数是死参数——模型声明了它却毫无效果，属于上游遗留缺陷。

### 方案（已实现，文案以代码为准）
1. **超时返回追加引导**（`shell.go` runSync 超时分支）：
   ```
   Command timed out after 60s

   Partial output before timeout: …

   Tip: if this command needs longer, re-run it with background=true (returns a sessionId) and use poll/read to check progress, or pass timeout=<seconds> to extend this run's wait.
   ```
2. **实现 per-call `timeout`**（`executeRun`）：读取 `args["timeout"]`（JSON number，容忍 int/float64），语义与 schema 描述对齐：
   - 未提供 → 用 `tools.exec.timeout_seconds` 默认值（现状不变）；
   - `>0` → 覆盖本次 runSync 超时；
   - `=0` → 本次无超时。
   实现上把超时值作为参数传入 `runSync`（不再读 `t.timeout` 字段），cron 路径的 `SetTimeout` 不受影响。
3. **工具描述补充**：加一句 `Pass timeout=<seconds> to extend the wait for a single run (0 = no timeout).`

### 取舍
- **不设 per-call 上限**：`background=true` 本就无超时，per-call 延长不是新攻击面；/stop 的 Job Object 整树击杀对超长同步命令同样有效。若未来需要上限再加 `max_call_timeout_seconds`，现在不加（YAGNI）。
- **被否方案**：调大全局默认超时——治标且拖慢真死循环的失败暴露。

### 测试锚点
`TestExecTool_TimeoutErrorSuggestsBackground`（超时 ForLLM 含引导文案）、`TestExecTool_PerCallTimeoutOverridesDefault`（per-call 覆盖默认，长命令按 per-call 值被杀）、`TestExecTool_PerCallTimeoutZeroMeansNoTimeout`（timeout=0 时长命令存活）、`TestExecTool_NoTimeoutArgKeepsConfigDefault`（回归：不传参数行为不变）、`TestResolveRunTimeout`（表驱动：float64/int/int64/负数/字符串容忍与回退）。

## 2. ② `max_tool_iterations` 到顶自动续 turn（已实施）

### 动机
50 步对"读代码→改多处→跑测试→修失败"类任务不够。到顶即终止，用户只能手动"继续"，且 `toolLimitResponse` 作为最终答复发给用户没有信息量。

### 方案（B1：runAgentLoop 层续段，已实现）
- **检测**：`turnState.endedByIterationLimit`（`markIterationLimit`/`iterationLimitHit`，turn_state.go）→ Finalize 传播进 `turnResult.endedByIterationLimit`（pipeline_finalize.go），比字符串比较稳。
- **续段循环**（`runAgentLoop` 内，`for { ... }` 结构）：
  - 每段 `newTurnState` + `runTurn`；
  - 段到顶且 `segment < autoContinueTurns` 时，改写 `opts.Dispatch.UserMessage` 为续段指令：
    `[auto-continue segment k/N] The previous round ended because it hit the tool-step limit mid-task. Continue the original task from where it stopped; do not restart it, do not ask for confirmation, and finish with a final answer when done.`
  - **过渡文案机制**（与原方案的"替换 finalContent"不同）：新增 `processOptions.IterationLimitResponse`——进循环前若 `auto_continue_turns > 0` 预置首段文案（首段触顶即续，不应展示"改配置"的英文默认话术），非末段置为 `"⚙ 本轮工具步数达到上限，自动继续执行（第 k/N 段）"`，turn_coord 在触顶分支用覆盖落盘 assistant 内容；**末段置空**，再触顶时回落 `toolLimitResponse`（含改配置引导，不谎称继续）。
  - `SendResponse` 对外只发**最后一段**的 finalContent；每段 followUps 在段内照常 `PublishInbound`。
- **配置**：`agents.defaults.auto_continue_turns`（int，默认 2，负值钳 0=关闭；总步数上限 ≈ (1+N)×max_tool_iterations）。env `PICOCLAW_AGENTS_DEFAULTS_AUTO_CONTINUE_TURNS`。getter：`GetAutoContinueTurns`。
- **适用范围**：仅顶层 turn（subturn 不过 runAgentLoop，天然排除）；`NoHistory` 跳过；aborted/error 段不续。

### 关键交互（含实现决策表态）
- **上下文瘦身**：每段 turn 开始时 Assemble 做 proactive 压缩——跨段长任务自动瘦身，这是选 B1（而非抬单 turn 上限）的核心理由。
- **busy 语义**：段间有短暂 session 空闲窗口，用户消息可插入（模型在续段中会看到，视为转向）；不影响 TryLock busy 语义。
- **段间间隙守卫**：runTurn 返回时注册即释放，段间存在短暂无主窗口——若其他 turn（用户消息/cron/followUp）恰好在此窗口夺注会话，续段的 `registerActiveTurn` 会 Store 覆盖其注册，双 turn 并发写同一会话（/stop、看门狗、steering 对两个 turn 同时失效）。守卫在续开前重查 `getActiveTurnState`，命中则放弃续段且不外发（段 1 卡片已预告续开，新 turn 接管回复流，再发过渡文案只会误导）。检查与下一段注册之间无 I/O，TOCTOU 窗口为纳秒级。测试锚点：`TestRunAgentLoop_AutoContinueDropsWhenSessionReclaimed`。
- **合成消息可见性（表态：接受可见）**：`[auto-continue segment k/N]` 是普通 user 消息经 SetupTurn 落盘，历史与 web UI 中可见。用户可读、模型衔接自然，不加 hidden 标记。
- **loop_detection 跨段清零（表态：接受）**：turnHealth 在 newTurnState 每段新建，跨段的重复失败模式在段边界重新计数。段数硬顶是失控兜底，与下条一致。
- **防失控**：段数硬顶 + 每段 loop_detection 照常 + /stop 随时可杀（kill 的是当前段）。
- **事件**：每段一次 turn.start/turn.end（一段=一个用户可见回合，监控语义自洽）。
- **turn 健康与标题**：newTurnState 每段新建（health 重置——总步数硬顶是兜底）。

### 被否方案
- **B2（runTurn 内抬上限+续注 synthetic steering）**：单卡单 turn 更优雅，但污染 `max_tool_iterations` 语义（现有大量测试断言该值），且单 turn 内上下文只增不瘦。否。
- **followUps 总线回灌**：经过 processMessage 全路由（会撞 handleCommand、路由策略变化等不可控环节）。否。

### 测试锚点
`TestRunTurn_MarksIterationLimitOnTurnResult`（触顶标记传播到 turnResult）、`TestRunAgentLoop_AutoContinuesOnIterationLimit`（到顶后续段、最终答复为末段内容）、`TestRunAgentLoop_AutoContinueRespectsBudget`（段数用尽后以 toolLimitResponse 终止）、`TestRunAgentLoop_AutoContinueDisabledByZero`（=0 时行为与现状一致）。NoHistory 跳过与"中间段不外发"的断言并入上述循环类测试。

## 3. ③ 网关重启后中断会话恢复（已实施）

### 动机
systemd 重启/OOM/断电后，JSONL 历史在磁盘上，但进行中的 turn 静默丢失：尾部悬空 tool_calls 会让下次请求直接 400（provider 要求 tool 消息配对），且用户不知道有未完成任务。

### 方案
新文件 `pkg/agent/restart_recovery.go`，`RunRestartRecovery(ctx, al)`：
1. **时机**：gateway `setupAndStartServices` 完成后 `go agentLoop.RunRestartRecovery(context.Background())`（gateway.go；不阻塞启动，失败只记日志）。
2. **检测**：`ListSessions()` 遍历 → `GetHistory(key)` → 从 `sealDanglingToolCalls` 抽出纯检测函数 `detectDanglingToolCalls(history) []providers.ToolCall`（重构，逻辑不变）。
3. **封口**：命中则用**重启语义 note** 封口落盘（与 abort note 区分，引导模型复查而非假定失败）：
   ```
   [interrupted by gateway restart: this tool call's result is unknown — re-check the affected state before relying on it]
   ```
4. **通知**：`outboundTargetForSession` 用 `session.MetadataAwareSessionStore.GetSessionScope` + `scope.Values["chat"]`（pkg/session 的 `buildSessionScope` 约定）解析 channel/chatID——**不经 web/backend，无依赖方向问题**；仅对 jsonl mtime 在通知窗口内（默认 24h）的会话发：
   > ⚠ 检测到上次任务被中断（网关重启）。会话已封口保留，回复「继续」可让模型接着做。
   
   经 `bus.PublishOutbound` 直发（与 heartbeat 通知同模式，含 AgentID）。
   **关键顺序（评审修复记录）**：通知窗口判定（`sessionActiveWithin`，jsonl mtime）必须在 `SetHistory` 封口**之前**完成——封口会重写 jsonl 并刷新 mtime，先封口后判断会让每个命中的会话恒判"刚活跃"，`notify_window_hours` 形同虚设。`TestRunRestartRecovery_RespectsNotifyWindow` 用 `os.Chtimes` 回拨真实 jsonl mtime 验证此顺序（不经过包装层，能捕获顺序回退）。
5. **未响应重提醒**：首次通知后登记会话级 pending 提醒（内存态，`restart_recovery.go` 内部维护，含 agentID/channel/chatID）——
   - 恢复 goroutine 自身以 1 分钟 ticker 巡检：到期（默认 30 分钟）且该会话**仍无任何用户消息** → 再发一条同样提示（含 AgentID，与首条一致）；
   - 最多 3 次（默认），之后转静默（封口仍有效，用户随时可"继续"）；
   - `processMessage` 路由出 sessionKey 后调用 `al.cancelRecoveryReminder(sessionKey)`（agent_message.go），任何用户消息（含"继续"）立即取消；
   - 再次重启不会重复首通知（已封口的会话检测不命中）；封顶 3 次保证打扰有界。
6. **恢复**：用户回复"继续"→ 正常 processMessage → 同 session → 历史含封口 note → 模型自然续做。**零新恢复机制**。
7. **配置**：`agents.defaults.restart_recovery`：`{ enabled *bool（nil=开，fork 惯例）, notify_window_hours int（默认 24，0=只封口不通知）, reminder_interval_minutes int（默认 30，0=关闭重提醒）, reminder_max int（默认 3）}`。

### 边界
- 封口幂等（已封口的 history 检测不命中）；
- 主动 /stop 的会话重启检测不会命中（stop 时已实时封口）——命中的都是真中断；
- pico（web）会话：通知走 pico 通道出现在 web UI；scope 无法解析 channel 的会话只封口不通知；
- 大历史会话的 GetHistory 成本：启动后异步执行，可接受。

### 测试锚点
`TestRunRestartRecovery_SealsAndNotifies`（封口 + 窗口内通知）、`TestRunRestartRecovery_SkipsCleanSessions`、`TestRunRestartRecovery_RespectsNotifyWindow`（窗口外只封口不通知，真实 mtime 路径）、`TestRunRestartRecovery_Disabled`、`TestRunRestartRecovery_ReminderStopsAfterMax`（按间隔重发且封顶后静默）、`TestRunRestartRecovery_ReminderCancelledByUserMessage`（cancelRecoveryReminder 生效）、`TestDetectDanglingToolCalls_FindsMissingResults`（抽函数 + 幂等；`TestSealDanglingToolCalls` 既有测试仍过）。

## 4. ④ 长任务进度心跳（已实施）

### 动机
一个跑了 20 分钟的 background 命令或长推理期间，卡片只有静态 running 条目，用户无法分辨"在干活"与"卡死"。

### 方案
- **数据源（已实现）**：`turnState` 活动时间戳。更新点：`setIteration`、流式 chunk、工具完成处。
- **发布器访问（已实现）**：turnState 持 `streamingPublisher` 原子引用（`loadStreamPublisher`，CAS 清空防跨 turn 竞态）；`AppendToolStep` 加互斥，心跳 goroutine 并发调用安全。
- **心跳 goroutine（已实现，`startProgressHeartbeat`）**：以 interval/4 轮询（最小 100ms），闲置达阈值发：
  ```
  publisher.AppendToolStep(turnCtx, ToolStep{Kind: ToolStepKindText,
      Result: "⏱ 进度：任务仍在进行——第 N 轮迭代，已运行 Xm"})
  ```
  turnCtx cancel / stop / turn 结束即退出。**非流式 provider 的长推理期间无 chunk、无迭代推进、无工具完成 → 心跳照发，这是预期行为**（正是动机里"长推理"场景），不要当误报修掉。
- **渲染**：Kind 复用 `KindText` → 面板「💬 本轮说明」归档，**feishu 端零改动**。
- **配置**：`agents.defaults.progress_heartbeat_seconds`（int，默认 180，0=关闭）。env 同前缀。
- **顺带收益**：间隔小于飞书 200850 服务端无写入超时窗口时兼作流式卡保活（不作为承诺，保活失败已有重开/降级兜底）。

### 并发安全（已验证）
`AppendToolStep` 加互斥，publisher 原子引用 + CAS 清空，心跳 goroutine 并发调用安全。

### 测试锚点
`TestProgressHeartbeat_PublishesAfterIdle`（闲置超阈值后发出 KindText 步骤）、`TestProgressHeartbeat_SilentWhenActive`（持续活动不发）、`TestProgressHeartbeat_StopsAfterClear`（停止后不再发布）、`TestProgressHeartbeat_Disabled`（=0 不启动 goroutine）、`TestTurnState_ActivityTracking`/`TestTurnState_StreamPublisherAtomicRef`（数据源与原子引用单测）。

## 5. 配置汇总（新增）

```jsonc
{
  "agents": { "defaults": {
    "auto_continue_turns": 2,          // ② 0=关
    "progress_heartbeat_seconds": 180, // ④ 0=关
    "restart_recovery": {              // ③ enabled 省略=开
      "notify_window_hours": 24,        // 0=只封口不通知
      "reminder_interval_minutes": 30,  // 0=关闭重提醒
      "reminder_max": 3
    }
    // ① 无新配置（per-call timeout 走工具参数）
  }}
}
```

## 6. 实施顺序与依赖

按用户指定顺序 ①→②→③→④，四项彼此独立；**四项已全部实施完成**，`docs/design/fork-overview.zh.md` 已同步（新功能块 + 差异表）。评审修复记录：③ 的通知窗口判定顺序 bug（先封口后判断导致窗口失效）已修，`TestRunRestartRecovery_RespectsNotifyWindow` 改走真实 mtime 路径守护该顺序。

## 7. 明确不做

- 不给 picoclaw.service 加任何 sandbox 指令（运维禁令）；
- 不引入 LLM 失败计数预算（turn 韧性结构性保证，fork 明令）；
- 不改 `/new`、`/switch` 的 TryLock busy 语义；
- ④ 不改 feishu 卡片结构（KindText 复用）；
- ② 不动 `max_tool_iterations` 既有语义与相关测试断言。

## 8. 第二批改进（2026-09-18 晚，评审深挖后）

五项改进 + 一项顺手修复（编号沿用前文语义）：

### 8.1 ③ 多代理恢复扫描
`RunRestartRecovery` 只扫 `GetDefaultAgent()` 的会话——配了 `agents.list` 时其他 agent 的中断会话漏扫。改为遍历 `ListAgentIDs()` + 默认 agent，对 Sessions store **按实例指针去重**后逐 store 扫描（同 workspace 的多 agent 共享目录但各持 store 实例；封口幂等保证重复扫描安全——第二次读到已封口即跳过）。通知的 `AgentID` 用各会话所属 agent 的 ID。

### 8.2 ④ 心跳非流式降级 + 节流修复
- **节流缺陷（本轮发现）**：现实现闲置超阈值后**每 interval/4 连发**（条件只查 `idle >= interval`，无上次发送时间）——20 分钟静默工具会以 45 秒间隔刷屏。修复：`turnState.lastHeartbeatNano`，触发条件改为 `idle >= interval && now-lastBeat >= interval`（持续静默期每 interval 一拍，180s 间隔仍优于飞书 200850 窗口，保活语义保留）。
- **非流式降级**：fire 时 `publisher == nil` 且 `ts.channel` 非空非 internal → 以 `outboundMessageForTurn` 发普通进度消息，`Context.Raw[message_kind] = "progress_note"`（新 kind 常量，通道可识别样式；不识别则按普通文本显示）。流式会话行为不变（面板步骤优先）。无新配置——`progress_heartbeat_seconds` 统一治理两种面。

### 8.3 ② 续段卡面板标题标识
续段卡与首段卡在面板 header 上无法区分。注入链：`turnState.segmentLabel`（如 `续 2/3`，runAgentLoop 续段分支设置）→ `streamingChunkPublisher` 构造时对实现可选接口 `SetSegmentLabel(string)` 的 streamer 调用 → feishu 流式 state 存储并并入 `feishuPanelHeader` 的 parts（`🧠 Agent 过程 · 续 2/3 · N 轮推理 …`）。非 feishu 通道不实现 setter 即无感知。

### 8.4 ADR 留档
经 codebase-memory 的 `manage_adr` 把四件套 + 本批改进的架构决策写入图谱 ADR（跨会话可见；仓库内设计文档仍是真源）。

### 8.5 ① exec 默认超时 60s → 120s
有错误层引导 + per-call 覆盖兜底后，默认值可放宽以减少误杀（`config/defaults.go` `TimeoutSeconds`）。配置显式设置者不受影响。

### 测试锚点
`TestRunRestartRecovery_ScansAllAgents`、`TestProgressHeartbeat_ThrottledToOnePerInterval`、`TestProgressHeartbeat_OutboundFallbackWithoutStreamer`、`TestFeishuPanelHeaderShowsSegmentLabel`、`TestStreamingPublisherSetsSegmentLabel`、`TestDefaultConfig_ExecTimeout`（更新断言 120）。
