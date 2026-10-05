# 代码评审：空闲压缩扫描器（Idle Compaction）

> 评审日期：2026-10-05
> 评审对象：`928a552f`（空闲压缩扫描器落地，含 SessionStore.LastModified 接口扩展与 gateway 接线）
> 设计文档：`docs/design/idle-compaction-design.zh.md`（评审修订 v2 + §8 实现记录）
> 评审人：作者自审（同会话设计修订→实现→复核；非独立评审，结论按此折扣采信）
> 结论：**实现忠实于修订后设计；1 个已知设计权衡（进程内记账重启失效）与 2 个低危观察项，无功能性缺陷。**

---

## 1. 评审范围

| 文件 | 变更 | 性质 |
|---|---|---|
| `pkg/agent/idle_compact.go` | +250（扫描器主体） | 新增 |
| `pkg/agent/idle_compact_test.go` | +300（9 个锚点） | 新增 |
| `pkg/session/session_store.go` | +5（接口加 `LastModified`） | 接口扩展 |
| `pkg/session/manager.go` | +10（`Session.Updated` 转发） | 实现 |
| `pkg/agent/subturn.go` | +2（ephemeral stub） | 实现 |
| `pkg/agent/agent.go` | +4（`idleScanner` 字段） | 接线 |
| `pkg/config/config.go` | +60（`IdleCompactConfig` + clamp） | 配置 |
| `pkg/gateway/gateway.go` | +10（三处接线 + `stopIdleCompact` 参数） | 接线 |

验证：`go build -tags goolm,stdjson ./...` 干净；`pkg/agent`（91.3s）/`pkg/session`/`pkg/config` 全量绿；9 个专项测试全过。

## 2. 关键设计确认

1. **「只挑时机，压缩 100% 复用」的边界纪律执行到位**——扫描器对 `Compact` 的调用与回合尾压缩完全同入口（`Reason=summarize`、`Budget=CompactionBudget()`），共享 `compactScheduler.inFlight` 单飞与 `drainCompact` 关机覆盖。没有第二套压缩路径，这是本实现最重要的属性。
2. **成功记账（设计修订 §3.6）实现正确**——`{successAt, seenUpdated, fails}` 以活动为唯一重压依据，绕开了 seahorse「JSONL 水位不回落」的坑；`TestIdleCompact_SuccessBookkeepingBlocksRecompaction` 直接以「水位恒过阈 + Compact no-op」的 seahorse 形态钉住不重压。
3. **失败退避的锚定缺陷在实现中被抓到并修复**——失败记录不锚 `seenUpdated` 时零值会被「活动重置」规则误判为新活动、退避形同虚设（`TestIdleCompact_FailureBackoff` 第一次运行即红）。这正是设计 §3.6「记录锚定」规则的价值：记账结构统一锚定 UpdatedAt 快照，成功与失败同一套重置语义。
4. **门序即成本序**——mtime（免读文件内容）→ 活跃 turn（内存查表）→ 记账（内存 map）→ 消息数（拉全量历史）→ 水位（估算全量 token）→ 单飞。重活在最后，绝大多数 tick 在第一道门就短路。

## 3. 观察项（无功能性缺陷，按严重度记录）

### O-1 进程内记账在重启后失效（已知权衡，设计 §3.6 已声明）

记账是进程内 map：网关重启后第一次 tick 对「已压缩且无新活动」的 seahorse 会话会再发起一次 Compact。设计已声明可接受（Compact 对已压状态廉价）。**残余风险**：若 seahorse `Compact` 对已 condensed 会话并非 no-op 而是再跑一轮 leaf（需引擎侧确认），重启频繁的场景会重复烧摘要 token。建议上线后观察日志 `Idle compaction started` 频率与重启次数的相关性。

### O-2 `LastModified` 双实现的语义分叉

`JSONLBackend.LastModified` 是 jsonl 文件 mtime；`SessionManager.LastModified` 返回 `Session.Updated`（内存时间戳，精度=写时刻）。两者对「空闲」的判定粒度一致（分钟级），但 mtime 含非消息写入（meta 落盘）而 `Updated` 只反映消息追加——理论上 mtime 路径的「有活动」判定略偏保守（更晚判定空闲），方向安全。记录在案，不修。

### O-3 扫描 tick 与 `drainResults` 的节奏耦合

`resultCh` 容量 64；在途压缩结果若在两个 tick 之间塞满（>64 个并发压缩完成），`launchCompaction` 的 goroutine 会阻塞在 `s.resultCh <-` 直到下一 tick drain。此时该 goroutine 持有 `inFlight` 条目不放，效果等同「单飞未释放」——不会死锁（tick 一定会来），但极端批量场景下后续扫描会误判 in_flight 跳过。当前部署形态（个人网关、会话数几十）远达不到 64；记录为规模警示，不修。

## 4. 测试覆盖核对

| 设计 §5 计划 | 落地 |
|---|---|
| gate 矩阵（不闲/消息不足/…） | ✅ `GateNotIdle`、`GateTooFewMessages`；`turn_active`/`under_threshold`/`in_flight` 分支由代码审查覆盖（构造活跃 turn 与精确水位的测试环境成本高，收益低——分支是单行短路） |
| 单飞 | ✅ 复用 `compactScheduler.inFlight`（`TestFinalize_CompactDedup` 一族既有保障）；扫描器侧由 `in_flight` skip 分支覆盖 |
| 失败退避 | ✅ `FailureBackoff`（3 次后停） |
| 成功记账 / 新活动重置 | ✅ `SuccessBookkeepingBlocksRecompaction`、`NewActivityRestoresEligibility` |
| 配置 clamp / 显式关 | ✅ `IdleCompactConfig_Clamps`、`DisabledByConfig` |
| 水位 nil | ✅ `no_usage` skip 分支（`FiresWhenEligible` 的对侧由代码审查覆盖） |
| 启停往返 | ✅ `StartStopRoundtrip` |

## 5. 安全维度

无发现。摘要输入不出本机；后台 LLM 调用走用户已配置的 provider（与回合尾压缩同一计费面，无新密钥/端点）；`LastModified` 接口扩展无越权读取（路径由 store 内部派生）。

## 6. 后续事项

1. 观察项 O-1 的日志相关性（上线一周后回看）；
2. `turn_active`/`under_threshold` 分支若未来重构 gate 顺序，补构造型测试；
3. fork-overview 与 `docs/guides/configuration.zh.md` 的 `idle_compact`/`split_turn` 配置小节登记（与 split-turn 评审 P3-6 合并处理，建议单独一个 docs 提交）。
