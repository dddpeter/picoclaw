# 长任务执行优化（四件套）设计文档

> 状态：待评审
> 日期：2026-09-18
> 范围：① exec 超时引导与 per-call 超时；② `max_tool_iterations` 到顶自动续 turn；③ 网关重启后中断会话的封口与恢复提示；④ 长任务进度心跳。
> 动机：个人部署中长任务（大重构、长构建、跨夜任务）的四个实际痛点——长命令被默认 60s 超时杀掉后模型盲目重试；步数到顶 turn 直接终止；网关重启后进行中的任务静默丢失；长时间无输出时用户无法分辨"在干活"与"卡死"。

## 0. 现状盘点（代码锚点）

| 环节 | 现状 | 锚点 |
|---|---|---|
| 迭代上限 | 默认 50，到顶后 turn 以 `toolLimitResponse` 收尾（提示改配置），任务被截断 | `config/defaults.go:43`、`pkg/agent/turn_coord.go:282-289`、`pkg/agent/agent.go:129` |
| turn 顶层壳 | `runAgentLoop` 创建 turnState → `runTurn` → 发 followUps → 发最终响应 | `pkg/agent/agent.go:540-660` |
| 悬空封口 | `sealDanglingToolCalls` 给尾部悬空 tool_calls 补合成结果；/stop 与看门狗路径已用 | `pkg/agent/steering_abort.go:37-76` |
| 会话枚举与 scope | `ListSessions()` 枚举全部会话；scope 含 channel/peer（web 会话列表据此显示渠道徽标） | `pkg/memory/jsonl.go:939`、`web/backend/api/session.go:288-296` |
| 主动外发 | heartbeat 用 `GetLastChannel` + outbound 发布；runAgentLoop 最终响应走 `bus.PublishOutbound` | `pkg/heartbeat/service.go:177-179`、`pkg/agent/agent.go:617-640` |
| exec 超时 | 同步 run 默认 60s（`tools.exec.timeout_seconds`）；超时返回只有 "Command timed out after …" + 部分输出，**无 background 引导**；schema 声明的 per-call `timeout` 参数**无任何消费点（死参数）** | `pkg/tools/shell.go:370-372（声明）`、`:495-530（runSync 只读 t.timeout）`、`:582-592（超时返回）` |
| background 会话 | `background=true` 不受超时限制，poll/read/write/kill/send-keys 全套管理；工具描述已写 "Use background=true for long-running commands" | `pkg/tools/shell.go:328-330`、`:701+（runBackground）` |
| 流式卡面板 | `ToolStep{Running:true}` 发布运行中条目；KindText 步骤归档为「💬 本轮说明」——**新增文本步骤无需 feishu 端改动** | `pkg/bus/bus.go:130-152`、`pkg/agent/pipeline_streaming.go:550-595` |
| 上下文管理 | 每段 turn 开始 Assemble（proactive 压缩）；溢出同步压缩；turn 后异步摘要 | `pkg/agent/context_budget.go`、`context_legacy.go` |

## 1. ① exec 超时引导 + per-call 超时实现

### 动机
模型经常直接同步 run 长命令（build/test/爬虫），60s 被杀后原样重试，再被杀，直到触发 bash_retry 循环警示。工具描述里虽有 background 引导，但错误返回是引导模型改行为的最佳时机（相关性最高）。同时 schema 里的 `timeout` 参数是死参数——模型声明了它却毫无效果，属于上游遗留缺陷。

### 方案
1. **超时返回追加引导**（`shell.go` runSync 超时分支）：
   ```
   Command timed out after 60s

   Partial output before timeout: …

   Tip: if this command is expected to run longer, re-run it with background=true (returns a sessionId), then use poll/read to check progress. Alternatively pass timeout=<seconds> to extend this run's limit.
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
`TestExecTool_TimeoutErrorSuggestsBackground`（超时 ForLLM 含引导文案）、`TestExecTool_PerCallTimeoutOverridesDefault`（per-call 2s 覆盖默认，长命令按 2s 被杀）、`TestExecTool_PerCallTimeoutZeroMeansNoTimeout`（timeout=0 时长命令存活）、`TestExecTool_NoTimeoutArgUsesConfigDefault`（回归：不传参数行为不变）。

## 2. ② `max_tool_iterations` 到顶自动续 turn

### 动机
50 步对"读代码→改多处→跑测试→修失败"类任务不够。到顶即终止，用户只能手动"继续"，且 `toolLimitResponse` 作为最终答复发给用户没有信息量。

### 方案（B1：runAgentLoop 层续段）
- **检测**：`turnResult` 增加 `endedByIterationLimit bool`；`runTurn` 在 `finalContent = toolLimitResponse` 分支处置 true（比字符串比较稳）。
- **续段循环**（`runAgentLoop` 内，runTurn 返回后）：
  ```
  for segment := 1; endedByIterationLimit && segment <= autoContinueTurns; segment++ {
      向 session 追加 synthetic user message：
        "[continue segment k/N] The previous round hit the tool-step limit mid-task. Continue the original task from where it stopped; do not restart or re-ask."
      过渡文案替换该段对外内容：finalContent = "⚙ 本轮工具步数达上限，自动继续（第 k/N 段）"
      新建 turnState（复用 opts，UserMessage=续段提示），runTurn
  }
  ```
  - 中间段的 assistant 落盘消息用过渡文案（模型在历史里看到自己的"达上限"记录 + 用户的续段指令，衔接自然）；
  - `SendResponse` 对外只发**最后一段**的 finalContent（中间段不外发，卡片以过渡文案封卡）。
- **配置**：`agents.defaults.auto_continue_turns`（int，默认 2，0=关闭；总步数上限 ≈ (1+N)×max_tool_iterations）。env `PICOCLAW_AGENTS_DEFAULTS_AUTO_CONTINUE_TURNS`。
- **适用范围**：仅顶层 turn（subturn 不过 runAgentLoop，天然排除）；`NoHistory` 跳过；aborted/error 段不续。

### 关键交互
- **上下文瘦身**：每段 turn 开始时 Assemble 做 proactive 压缩——跨段长任务自动瘦身，这是选 B1（而非抬单 turn 上限）的核心理由。
- **busy 语义**：段间有短暂 session 空闲窗口，用户消息可插入（模型在续段中会看到，视为转向）；不影响 TryLock busy 语义。
- **防失控**：段数硬顶 + 每段 loop_detection 照常 + /stop 随时可杀（kill 的是当前段）。
- **事件**：每段一次 turn.start/turn.end（一段=一个用户可见回合，监控语义自洽）。
- **turn 健康与标题**：newTurnState 每段新建（health 重置——总步数硬顶是兜底）。

### 被否方案
- **B2（runTurn 内抬上限+续注 synthetic steering）**：单卡单 turn 更优雅，但污染 `max_tool_iterations` 语义（现有大量测试断言该值），且单 turn 内上下文只增不瘦。否。
- **followUps 总线回灌**：经过 processMessage 全路由（会撞 handleCommand、路由策略变化等不可控环节）。否。

### 测试锚点
`TestRunAgentLoop_AutoContinuesOnIterationLimit`（到顶后续段、最终答复为末段内容）、`TestRunAgentLoop_AutoContinueRespectsBudget`（段数用尽后以 toolLimitResponse 终止）、`TestRunAgentLoop_AutoContinueDisabled`（=0 时行为与现状一致）、`TestRunAgentLoop_AutoContinueSkipsNoHistory`、`TestRunAgentLoop_AutoContinueSendsOnlyFinalSegment`（中间段不外发）。

## 3. ③ 网关重启后中断会话恢复

### 动机
systemd 重启/OOM/断电后，JSONL 历史在磁盘上，但进行中的 turn 静默丢失：尾部悬空 tool_calls 会让下次请求直接 400（provider 要求 tool 消息配对），且用户不知道有未完成任务。

### 方案
新文件 `pkg/agent/restart_recovery.go`，`RunRestartRecovery(ctx, al)`：
1. **时机**：gateway `setupAndStartServices` 完成后异步 goroutine 执行（不阻塞启动，失败只记日志）。
2. **检测**：`ListSessions()` 遍历 → `GetHistory(key)` → 从 `sealDanglingToolCalls` 抽出纯检测函数 `detectDanglingToolCalls(history) []providers.ToolCall`（重构，逻辑不变）。
3. **封口**：命中则用**重启语义 note** 封口落盘（与 abort note 区分，引导模型复查而非假定失败）：
   ```
   [interrupted by gateway restart: this tool call's result is unknown — re-check the affected state before relying on it]
   ```
4. **通知**：`GetSessionScope(key)` 解析 channel/peer（参考 `web/backend/api/session.go` 的 scope→channel 逻辑），仅对 jsonl mtime 在通知窗口内（默认 24h）的会话发：
   > ⚠ 检测到上次任务被中断（网关重启）。会话已封口保留，回复"继续"可让模型接着做。
   
   经 `bus.PublishOutbound` 直发（与 heartbeat 通知同模式）。
5. **恢复**：用户回复"继续"→ 正常 processMessage → 同 session → 历史含封口 note → 模型自然续做。**零新恢复机制**。
6. **配置**：`agents.defaults.restart_recovery`：`{ enabled *bool（nil=开，fork 惯例）, notify_window_hours int（默认 24，0=只封口不通知）}`。

### 边界
- 封口幂等（已封口的 history 检测不命中）；
- 主动 /stop 的会话重启检测不会命中（stop 时已实时封口）——命中的都是真中断；
- pico（web）会话：通知走 pico 通道出现在 web UI；scope 无法解析 channel 的会话只封口不通知；
- 大历史会话的 GetHistory 成本：启动后异步执行，可接受。

### 测试锚点
`TestRestartRecovery_SealsDanglingSessions`、`TestRestartRecovery_SkipsCleanSessions`、`TestRestartRecovery_RespectsNotifyWindow`（窗口外只封口不通知）、`TestRestartRecovery_Disabled`、`TestDetectDanglingToolCalls`（抽函数后 seal 逻辑回归：`TestSealDanglingToolCalls` 既有测试必须仍过）。

## 4. ④ 长任务进度心跳

### 动机
一个跑了 20 分钟的 background 命令或长推理期间，卡片只有静态 running 条目，用户无法分辨"在干活"与"卡死"。

### 方案
- **数据源**：`turnState` 增加 `lastActivity atomic.Int64`（UnixNano）。更新点：`setIteration`、`streamingChunkPublisher.AppendChunk`、ExecuteTools 工具完成处（与 `noteToolHealth` 相邻）。
- **发布器访问**：turnState 持 `streamingPublisher` 的原子引用（Setup 赋值 / Seal 后置 nil 与 `exec.streamingPublisher` 同步维护），心跳 goroutine 线程安全读取。
- **心跳 goroutine**：`SetupTurn` 成功后启动，30s tick：
  ```
  if now-lastActivity >= interval && publisher != nil && turn 未在 finalizing:
      publisher.AppendToolStep(KindText, "⏱ 进度：第 N 轮迭代，任务已运行 Xm（工具步 Y 次）")
  ```
  turnCtx cancel / turn 结束即退出。
- **渲染**：Kind 复用 `KindText` → 面板「💬 本轮说明」归档，**feishu 端零改动**。
- **配置**：`agents.defaults.progress_heartbeat_seconds`（int，默认 180，0=关闭）。env 同前缀。
- **顺带收益**：间隔小于飞书 200850 服务端无写入超时窗口时兼作流式卡保活（不作为承诺，保活失败已有重开/降级兜底）。

### 并发安全注意（实现时验证）
`AppendToolStep` 目前只从 turn goroutine 调用；心跳 goroutine 并发调用前需确认 feishu streamer 内部有互斥（fork-overview §1 的 200850 重开逻辑暗示有状态锁），否则在 `streamingChunkPublisher` 层加互斥。

### 测试锚点
`TestProgressHeartbeat_PublishesAfterIdle`（闲置超阈值后发出 KindText 步骤）、`TestProgressHeartbeat_SilentWhenActive`（持续活动不发）、`TestProgressHeartbeat_StopsAfterFinalize`（封卡后不再发布）、`TestProgressHeartbeat_Disabled`（=0 不启动 goroutine）。

## 5. 配置汇总（新增）

```jsonc
{
  "agents": { "defaults": {
    "auto_continue_turns": 2,          // ② 0=关
    "progress_heartbeat_seconds": 180, // ④ 0=关
    "restart_recovery": {              // ③ enabled 省略=开
      "notify_window_hours": 24        // 0=只封口不通知
    }
    // ① 无新配置（per-call timeout 走工具参数）
  }}
}
```

## 6. 实施顺序与依赖

按用户指定顺序 ①→②→③→④；四项彼此独立，②③共享"封口/续段"的历史语义但无代码依赖。全部完成后更新 `docs/design/fork-overview.zh.md`（新功能块 + 同步注意事项 + 差异表三行）。

## 7. 明确不做

- 不给 picoclaw.service 加任何 sandbox 指令（运维禁令）；
- 不引入 LLM 失败计数预算（turn 韧性结构性保证，fork 明令）；
- 不改 `/new`、`/switch` 的 TryLock busy 语义；
- ④ 不改 feishu 卡片结构（KindText 复用）；
- ② 不动 `max_tool_iterations` 既有语义与相关测试断言。
