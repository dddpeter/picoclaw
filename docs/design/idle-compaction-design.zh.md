# 空闲压缩设计（Idle Compaction：闲置会话主动摘要，下次提问无缝续聊）

> 状态：**已实现**（2026-10-05，评审修订 v2 同日落地；实现记录见 §8）。日期：2026-10-06（评审修订与实现 2026-10-05）。
> 前置阅读：`docs/design/fork-overview.zh.md`（同步注意事项）、
> `docs/design/split-turn-compaction-design.zh.md`（同系列上下文管理设计）、
> `pkg/agent/compact_schedule.go`（回合尾异步压缩，本设计的姊妹触发点）。

## 1. 问题

现有压缩触发点**全部挂在 turn 生命周期上**，会话一旦闲置就再无压缩机会：

| 触发点 | 时机 | 闲置会话能否受益 |
|---|---|---|
| 回合尾异步压缩（`compact_schedule.go`） | turn 结束且 `HistoryTokens ≥ 阈值` | 不能——最后一个 turn 结束后就不再触发 |
| 迭代边界压缩（`iteration_compact.go`） | 长 turn 中每次 LLM 调用前 | 不能——turn 没有活动就没有迭代 |
| 超窗应急压缩（legacy `forceCompression` / seahorse `CompactUntilUnder`） | LLM 报 context overflow 重试时 | 能，但这是**最差**的兜底（见下） |

由此产生两类实际症状：

1. **压缩失败的会话永久高水位**。回合尾压缩是异步 fire-and-forget：LLM 瞬断、`compactCallTimeout`（3min）超时、关机时 `drainCompact`（30s）放弃，都会让压缩静默失败且**不重试**。会话带着超阈值的历史一直躺着。
2. **放置很久后下次提问首字延迟变差**。迭代边界压缩（`iteration_compact.go`，2026-10-05 上线）保证了这种会话的下次提问**不会 overflow 失败**——首个 LLM 调用前会被同步接住；但代价是压缩（一次摘要 LLM 调用，实测 10~30s+）发生在用户正等着首字的时刻。本设计把这件事挪到用户不在场时，属于**延迟优化 + 回合尾失败会话的补票**，不再是"防 overflow"（那道防线已由迭代边界压缩承担）。

**目标**：会话闲置一段时间后，由网关后台主动做一次**真正的摘要式压缩**（不是应急裁剪），使下次提问时上下文已处于压缩后的稳态，摘要自动注入延续话题。

## 2. 现有基建盘点（本设计只做"触发器"，不重造压缩）

延续话题所需的基础设施已全部存在，缺的只是一个"空闲时机的调度器"：

- **摘要的产出与注入**
  - legacy 路径：`legacyContextManager.summarizeSession`（`context_legacy.go`）做增量 LLM 摘要 → `SetSummary` + `TruncateHistory` 落盘到 `SessionMeta.Summary`；下次 turn 的 `Assemble` 直接读 Summary 注入系统提示词。
  - seahorse 路径：`seahorseContextManager.Compact` → `engine.Compact`（leaf 同步 + condensed 异步，`short_compaction.go`），摘要存 SQLite；`engine.Assemble` 自动拼摘要 XML。
  - 两条路径都已实现 `ContextManager.Compact(ctx, &CompactRequest{Reason: ContextCompressReasonSummarize})`——**空闲压缩只是换一个时机调用同一个入口**。
- **水位判定**：`shouldCompactNow(threshold, usage, agent)`（`compact_schedule.go`）——HistoryTokens 对 `threshold × (window − maxTokens)`。扫描器没有回合 usage，数据源用 `computeContextUsage(agent, sessionKey)`（`context_usage.go:17`，回合尾压缩 `scheduleCompactWithUsage` 传的就是它，HistoryTokens 基准一致）。**注意 nil 语义分叉**：`shouldCompactNow` 对 nil usage 保守放行（压），但扫描器场景 nil（agent 异常）应**跳过**——5 分钟一轮的调度器过度压缩比漏压更糟，实现时显式 nil→skip。
- **去重**：`compactScheduler.inFlight`（`sync.Map`，sessionKey 级单飞）。
- **忙碌判定**：`al.getActiveTurnState(sessionKey)`（`turn_state.go`）非 nil 即 turn 活跃。
- **空闲判定**：`SessionMeta.UpdatedAt`（`memory/jsonl.go`，每次写消息都刷新）——但注意 `SessionStore` 接口目前**不暴露** meta 读取，需要补一个只读方法（见 §4.4）。
- **持久会话过滤**：seahorse `engine.ShouldPersistSession(sessionKey)` 已有；legacy 侧等价条件是 `opts.NoHistory` 类会话（heartbeat 等）——扫描器按"历史非空且 count > skip"过滤即可。
- **多 agent**：`agentForSession(sessionKey)`（`Clear` 已用）解析归属；每个 agent 有自己的 `Sessions` store，扫描需遍历各 agent 的 `ListSessions()`。
- **关机收尾**：`drainCompact` 已纳入停机流程，空闲压缩产生的 in-flight 工作自动被覆盖。

## 3. 设计

### 3.1 总体形态

新增一个**空闲压缩扫描器**，作为网关 services 之一（挂载方式仿照 `HeartbeatService`，`gateway.go` L443-447 / L696-698 两处启动路径都要接）：

```
idleCompactScanner（pkg/agent/idle_compact.go）
  └─ 每 scan_interval（默认 5min）tick：
       对每个 agent → ListSessions() → 逐会话检查：
         ① UpdatedAt 距今 ≥ idle_after（默认 30min）        [闲]
         ② getActiveTurnState(key) == nil                   [不忙]
         ③ ShouldPersistSession / 历史非空                   [可压]
         ④ shouldCompactNow(...) == true 且消息数 ≥ 最小收益  [值得压]
         ⑤ 无未消化的成功记录（成功记账，见 §3.6）            [不重压]
         ⑥ compactScheduler.inFlight LoadOrStore 成功        [单飞]
       通过 → goroutine 调 contextManager.Compact(summarize)

       （门序即成本序：①②最便宜先查，④要拉全量历史最贵最后查。）
```

核心原则：**扫描器只挑时机，压缩动作 100% 复用现有路径**。压缩内部逻辑（摘要提示词、协议安全切界、落盘格式）一个字不改。

### 3.2 触发条件细则

- **idle_after 默认 30 分钟**：短空闲即压（用户已确认取向）。话题还"热"时摘要质量最好；超过 30 分钟未回复，用户大概率已离开，此刻后台压缩对体验零打扰。tick 周期 5 分钟意味着实际空闲在 30~35 分钟之间。
- **水位门槛沿用 `compact_usage_threshold`（默认 0.75）**：不额外引入新阈值，空闲压缩与回合尾压缩的"值得压"标准完全一致。低于水位闲置的会话保持原始历史——这正是 pi 式"真实压力才压"的语义，空闲不是压的理由，**水位才是**；空闲只是给了"错过回合尾时机的会话"一次补票机会。
- **最小收益守门**：`len(history) ≥ min_history_messages`（默认 12）。防止对"几条消息 + 略超阈值"的会话白白调一次摘要 LLM（token 花了，摘要收益趋近于零）。
- **失败退避**：Compact 返回 error 时记录 `失败次数 + 上次失败时间`（进程内 map 即可，不落盘；map 由扫描 goroutine 独占读写，在途压缩经 channel 回报结果，无锁）。同一会话连续失败 3 次后进入长退避（下次活动刷新 UpdatedAt 时清零重计）——模型持续故障时不要每个 tick 都空烧重试。

### 3.6 成功记账（评审新增，防 seahorse 会话反复空压）

进程内 per-session 记录：`{ successAt, seenUpdatedAt }`。

- **跳过条件⑤**：`successAt` 非零且该会话 `UpdatedAt == seenUpdatedAt`（压缩完成后无新活动）→ 本轮跳过；
- **重置**：`UpdatedAt > seenUpdatedAt`（有新消息）→ 清空记录（含失败计数），会话重新获得被压资格；
- 兼容两条路径：不依赖水位回落（legacy 会回落、seahorse 不会），以"活动与否"为唯一重压依据；
- 进程重启即清零（记账是进程内状态）——重启后第一次 tick 对 seahorse 会话可能多压一次，可接受（Compact 对已压状态是廉价 no-op 或直接跳过）。
- **忙碌竞态**：扫描判定 ② 与用户恰好发消息之间存在窗口。Compact 入口本身不持 turn 锁（与回合尾压缩同一条路径），且：
  - legacy `summarizeSession` 末尾 `TruncateHistory + Save` 与消息追加共用 JSONL store 的分片锁，不会写坏；
  - seahorse 有 `condensing` 去重 + store 级一致性；
  - 最坏情况是"压缩与提问并发"，Assemble 读到压缩前/后的任一一致状态——与现有回合尾压缩和快速追问并发的语义完全相同，不新增风险。
  - 附加保险：goroutine 启动前复查一次 `getActiveTurnState`，启动后再 `inFlight.LoadOrStore`（顺序不可反，先占坑再复查亦可，以实现时竞态分析为准，本文不锁定顺序）。

### 3.3 与回合尾压缩的关系

不是替代，是补位：

- 回合尾压缩仍然是**主**触发点（低延迟：turn 结束几分钟内完成）；
- 空闲扫描器只处理"回合尾错过/失败"的会话——`inFlight` 单飞保证两者不会对同一会话同时压缩；
- 成本上界：一个闲置会话最多被补压一次——但**不能靠"压缩后水位回落"来保证**（评审发现，2026-10-05）：`computeContextUsage` 的 HistoryTokens 基于 JSONL 原始历史，legacy 路径 `TruncateHistory` 真裁 JSONL 会回落，而 **seahorse 路径的 Compact 只动 SQLite**（摘要入库、引擎内部水位下降），JSONL 不变 → 水位门恒过 → 每 tick 空转重压。改由**成功记账**保证（§3.6）。

### 3.4 配置

`agents.defaults.idle_compact` 块（fork 惯例：块缺省 = 默认值全生效，`enabled` 用 `*bool`，nil = 开——**不要改成值类型 bool**）：

```json
{
  "agents": {
    "defaults": {
      "idle_compact": {
        "enabled": true,
        "idle_after_minutes": 30,
        "scan_interval_seconds": 300,
        "min_history_messages": 12
      }
    }
  }
}
```

| 字段 | 默认 | 说明 |
|---|---|---|
| `enabled` | nil = 开 | 关闭时扫描器不启动（零成本） |
| `idle_after_minutes` | 30 | 下限 clamp 到 5（与 heartbeat `minIntervalMinutes` 同型） |
| `scan_interval_seconds` | 300 | 下限 clamp 到 60 |
| `min_history_messages` | 12 | 最小收益守门 |

### 3.5 AgentLoop 公开接口

gateway 只需要两个方法：

```go
// StartIdleCompactScanner 启动空闲压缩扫描器（幂等；enabled=false 时 no-op）。
func (al *AgentLoop) StartIdleCompactScanner(cfg config.IdleCompactConfig) error
// StopIdleCompactScanner 停止 tick 并等待在途压缩结束（内部走 drainCompact）。
func (al *AgentLoop) StopIdleCompactScanner()
```

扫描器持有 `stopChan`，实现与 `heartbeat.HeartbeatService.Start/Stop` 同构，gateway 的热重载/重启两条启动路径照抄 heartbeat 的接线即可。

### 3.6 观测

- 复用现有事件 `KindAgentSessionSummarize`（legacy）与 seahorse 内部日志——下游无需新增消费端。
- 扫描器自身日志统一 `logger.InfoCF/DebugCF("agent", ...)`，字段带 `idle_minutes`、`history_tokens`、`skipped_reason`，方便 `/status` 之外用日志直接回答"为什么没压"。

## 4. 实现落点清单

| 改动 | 文件 | 性质 |
|---|---|---|
| 空闲扫描器主体 | `pkg/agent/idle_compact.go`（新） | 新增，不动现有代码路径 |
| `SessionStore` 补只读 meta 访问：`GetSessionUpdatedAt(key) (time.Time, bool)` | `pkg/session/session_store.go` + `manager.go` + `jsonl_backend.go` | 接口加方法（`JSONLStore` 已有 meta 读取内部件，暴露即可） |
| 配置块 + clamp | `pkg/config/config.go` | 新增 struct，缺省语义见 §3.4 |
| AgentLoop 启停方法 | `pkg/agent/idle_compact.go` | 新增 |
| gateway 挂载（两条启动路径） | `pkg/gateway/gateway.go` | 仿 heartbeat 三行接线 |
| 测试 | `pkg/agent/idle_compact_test.go` 等 | 见 §5 |

## 5. 测试计划

- **gate 矩阵**：UpdatedAt 新鲜/过期、turn 活跃、水位不足、消息数不足、NoHistory/heartbeat 会话——各自跳过且原因进日志字段。
- **单飞**：扫描器与 `scheduleCompact` 并发调度同一会话，`Compact` 只进一次（`inFlight` 复用是天然保障，测试防回归）。
- **失败退避**：mock Compact 连续失败 3 次后不再发起；会话有新活动后退避清零。
- **配置**：缺省块 = 默认开 + 默认值；`enabled: false` 显式关；越界值 clamp（沿用 `instance_test.go` 里 CompactUsageThreshold clamp 的测试风格）。
- **成功记账**（评审新增）：会话压缩成功且无新活动 → 后续 tick 跳过（尤其水位恒过阈的 seahorse 形态：JSONL 不变、Compact no-op，断言第二次 tick 不再发起 Compact）；`UpdatedAt` 前进后记录重置。
- **水位 nil**：`computeContextUsage` 返回 nil 的会话跳过而非保守压缩。
- **fork 存量语义**：`/new`、`/switch` 非阻塞、流式头超时、输出清理、低收益循环检测全套测试必须原样通过——本设计不触碰这些路径，但按 AGENTS.md 惯例同步后全量跑 `go test ./pkg/...` 确认。

## 6. 与 fork 保护区的关系

- **不碰模型状态写锁**：空闲压缩走 `contextManager.Compact` 后台路径，与回合尾压缩（`compactScheduler` goroutine）同构；`retryLLMCall`/seahorse `completeFn` 读 `agent.Provider/Model` 的并发语义与现有后台压缩一致，不新增锁需求。
- **never-worse 守门**：扫描器独立于 turn 关键路径，最坏故障 = 多花摘要 token 或漏压（漏压由下次 tick / 现有 overflow 兜底接住），不可能影响会话正确性或 turn 响应。
- **开放默认**：默认开启符合 fork 三件套哲学；但后台 LLM 调用花的是用户 token，故提供显式 `enabled: false` 关闭位，且默认参数（30min 空闲 + 0.75 水位 + 12 条消息）保证只有"真的压过阀值又错过回合尾"的会话才产生调用。

## 7. 备选方案（否决理由）

- **下次提问时懒压缩（turn 前 idle check）**：被否决——首问延迟增加；迭代边界压缩上线后这条路径事实上已存在（turn 首 LLM 调用前同步压缩），本设计与它的关系是"把同一件事提前到用户不在场时做"，而非否决它——它仍是不可移除的兜底。
- **复用 heartbeat loop 顺带扫描**：被否决——heartbeat interval 可配置到小时级且服务可整体关闭，压缩节奏不应与 heartbeat 巡检耦合；独立扫描器 20 行 tick 循环，复杂度可控。
- **空闲时做两级压缩（短闲 leaf + 长闲 condensed）**：暂缓——seahorse 的 condensed 已是 Compact 内部的异步第二阶段，外部再分级收益有限、状态机复杂度高。等 leaf-only 空闲压缩上线后按实际 token 曲线再议。


## 8. 实现记录（2026-10-05）

- **落点**：`pkg/agent/idle_compact.go`（扫描器主体 + Start/Stop 公开方法）、`SessionStore.LastModified`（接口新增；`JSONLBackend` 已有实现直接提升、`SessionManager` 用 `Session.Updated`、`ephemeralSessionStore` stub）——评审时计划的 `GetSessionUpdatedAt` 落地为既有的 `LastModified`（jsonl mtime，restart recovery 同源）。`pkg/config/config.go` 的 `IdleCompactConfig`（含 Effective* clamp 方法）；`gateway.go` 三处接线（启动/重载/停止——停止经 `stopAndCleanupServices` 新增 `stopIdleCompact func()` 参数传入）。
- **成功记账实现**（§3.6）：`records[sessionKey]{successAt, seenUpdated, fails, lastFail}`，扫描 goroutine 独占读写，压缩结果经 `resultCh` 回流。**失败记录同样锚定 seenUpdated**——实现时发现的缺陷：失败分支不记 updated 快照则记录保持零值，后续扫描把「仍闲置」误判为「有新活动」而重置退避（`TestIdleCompact_FailureBackoff` 钉住）。
- **水位 nil 语义**：扫描器对 `computeContextUsage == nil` 跳过（§2 修订的扫描器分支）。
- **压缩动作**：复用 `compactScheduler.inFlight` 单飞 + `wg`（drainCompact 覆盖关机），Reason=summarize、Budget=`agent.CompactionBudget()`，与回合尾压缩完全同一条 Compact 入口。
- 测试锚点（9 个）：`TestIdleCompact_{GateNotIdle,GateTooFewMessages,FiresWhenEligible,SuccessBookkeepingBlocksRecompaction,NewActivityRestoresEligibility,FailureBackoff,DisabledByConfig,StartStopRoundtrip}`、`TestIdleCompactConfig_Clamps`。
