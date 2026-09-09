# Turn 韧性设计：LLM 失败、429 风暴与 loop 边界

状态：已实现（未提交，基线 v0.3.3-dirty）｜ 2026-09-09 ｜ 决策记录：§4 预算方案已评估并**移除**（选 B）

本文记录 2026-09-09 针对"上游 429 限流风暴导致回复中断/已中断卡片刷屏"的整套改造：
流式失败处理、卡片生命周期（参考 hermes-lark-streaming）、turn 级失败预算
（`maxTurnLLMFailures`）及其合理性评估，以及与 pi（`D:\code\pi`）loop/turn 机制的对照结论。

## 1. 背景与问题实证

用户的 agnes-ai 渠道 RPM 配额偏紧，2026-09-09 晚高峰出现持续性 429 风暴
（gateway.log 单日 26 次 `Status: 429`）。当时的行为链：

1. 每轮 iteration 的流式首跳命中 429 → 失败发生在出字前 → **封卡显示"⚠ 已中断"**；
2. fallback 链换候选成功 → loop 继续下一轮 iteration → **再开一张新卡 → 再 429 → 再封卡**；
3. 用户视角：一条回复伴随一串"已中断"卡片 + 最终答案回退成一条普通飞书消息
   （卡片与答案割裂）。

用户需求（原话归纳）：

- 中断后继续 loop 可以接受，但**不应每次失败都发一张"已中断"卡片**；
- 模型失败应**明确告知用户"模型有问题"**；
- fallback 也不行时**立即终止 loop**，所有模型的重试**不超过一定总次数**。

## 2. 分层模型与既有硬边界

评估任何"加预算"类机制前，先厘清现有结构中已经存在的边界
（`pkg/agent/pipeline_llm.go`、`pkg/providers/fallback.go`、`pkg/agent/turn_coord.go`）：

```
turn（一次回复）
└─ iteration（≤ MaxToolIterations，本机配置 100，未配置默认 20）
   └─ CallLLM（每次 iteration 一次）
      ├─ 流式首跳：ChatStream 最多 1 次/turn（失败后 sticky 降级，见 §3.2）
      └─ 重试层：1 + MaxLLMRetries 次链执行（本机 = 3，退避 2s/4s 线性）
         └─ fallback 链：N 个候选各试 1 次（本机 N=2：主模型 + 1 个 fallback）
            ├─ 冷却中的候选被 skip（不计失败、不发请求）
            └─ 真实 Chat 调用失败 → 该候选进指数冷却（1min→5min→25min→1h 封顶）
```

关键结构事实（决定 §4 的结论）：

| # | 事实 | 出处 |
|---|------|------|
| F1 | 单次 CallLLM 最多消耗 `(1+MaxLLMRetries)×N` 次真实上游调用（本机 = 3×2 = **6**） | pipeline_llm.go retry loop |
| F2 | CallLLM 整体失败 → **turn 当场终止**（`turn_coord.go` 无条件 `TurnEndStatusError` return），不存在"LLM 报错后继续 loop"的路径 | turn_coord.go:215 |
| F3 | 冷却按失败次数指数增长（1min→5min→25min→1h），同一候选单 turn 内**真实失败约 4 次后即被压制到 1 小时冷却**，之后只剩 skip（skip 不计失败） | cooldown.go `calculateStandardCooldown` |
| F4 | 流式首跳失败后本轮 sticky 降级，后续 iteration 不再尝试流式 | §3.2 |

因此 turn 级的"失败后仍继续"形态只有一种：**主候选真实失败、其余候选成功**的 iteration
反复出现。而 F3 保证了这种形态下失败计数会快速枯竭（主候选 4 次真实失败后进长冷却，
之后全部走 skip）。**turn 的上游调用量天然有界，不存在无限重试风暴。**

## 3. 已落地的改造

### 3.1 卡片生命周期（参考 hermes-lark-streaming）

hermes 的核心原则：**卡片是答案的第一载体，纯文本只是最后兜底**
（`controller/core.py` `_do_linear_complete_with_fallback` → `_send_text_fallback`）。
picoclaw 原行为与之相反：出字前流式失败 → 封卡/降级 → fallback 答案降级为普通飞书消息。

改造后（`pkg/agent/pipeline_streaming.go`）：

- 出字前流式失败（429 等）**不再封卡**：卡片保持"正在思考"存活，fallback 链的答案
  在 finalize 时**写入同一张卡片**，封"✓ 已完成"（含模型/耗时 footer）——用户无感；
- 纯文本消息降为最后兜底：仅当卡片连 Finalize 都失败（`finalizeConfiguredStreamingLLM`
  返回非 visible 错误）时才发普通消息（`pkg/agent/pipeline_finalize.go`）；
- 已出内容后的中断仍封"⚠ 已中断"卡（用户需要知道回复为什么停在半截）；
- turn 真正失败（链打穿/预算打穿）→ 封卡 + 明确报错消息
  "⚠ 模型调用失败，本轮已中止：<原因>"（`pkg/agent/agent.go` `publishTurnError`），
  消除"沉默死亡"。可见输出后的流式失败不重复通知（卡片已说明一切）；
- feishu 空卡删除：从未展示内容的卡在取消时直接删消息而非封中断标记
  （`pkg/channels/feishu/feishu_stream.go` `CancelWithReason`），作为卡片区
  的最后防线（正常路径下 §3.2 已保证不会反复开卡）。

### 3.2 sticky 流式降级 + 冷却门控

- `exec.streamingDegraded`：一次出字前流式失败后，turn 内后续 iteration 全部跳过
  流式首跳，直接走 Chat 链 → 整轮最多一张卡；
- 流式首跳前检查主候选冷却状态（`FallbackChain.Available`）：主模型刚 429 进冷却时
  直接跳过流式，避免"每轮 iteration 拿限流模型撞一次"。

### 3.3 turn 级重试边界：结构性保证（预算机制评估后移除）

实现过程中曾加过 turn 级失败预算 `maxTurnLLMFailures=10`（累计真实 Chat 调用失败、
打满即终止 turn），完成评估（§4）后**已移除**：结构性边界（F1/F2/F3）已完整覆盖
"总重试有界、全败即停"的需求，预算接近不可达且语义含混。该结论以代码注释形式
留在 `pkg/agent/pipeline_llm.go` 的 `CallLLM` 文档注释中，防止未来被当作缺口
重新"加固"。

## 4. maxTurnLLMFailures=10 合理性评估

### 4.1 它防的是什么？

需求原话是"所有模型 loop 重试不超过一定次数"。但对照 §2 的结构事实：

- 单次 CallLLM 有硬界：≤6 次真实调用（F1）；
- 全候选失败 → turn 立即终止，**不会继续 loop**（F2）；
- 持续 429 场景的真实消耗：第一次 CallLLM 烧掉 ≤6 次调用后 turn 就结束，
  之后主/备候选都进冷却，后续调用链是"全 skip → 立即 FallbackExhaustedError"，
  **零真实调用**。

也就是说，**用户要的"总次数有界"在结构上已经成立，预算不是安全性的必要组成**。

### 4.2 预算实际何时触发？

失败计数的唯一累积途径是"主候选真实失败 + 其余候选成功"的 iteration 反复出现
（每次 +1，turn 继续）。受 F3 冷却指数增长约束：

- 主候选第 1 次真实失败 → 冷却 1min；第 2 次 → 5min；第 3 次 → 25min；第 4 次 → 1h；
- 一个持续几分钟到几十分钟的 turn，主候选最多真实失败 **~4 次**，之后永久进入
  skip 状态（不再计数）；
- 备候选偶发失败再加 1~2 次。

**现实上界约 4~6，距离 10 很远。** 换言之，以当前结构（2 候选、retries=2、
指数冷却），预算在真实负载下接近不可达（dormant），既防不了什么，也几乎不会误伤。

### 4.3 误杀风险

理论误杀形态：超长 agentic turn（日志中有 91 iteration/12.5 分钟的真实案例）+
主候选每次冷却到期后都真实失败 + 备候选始终成功 → 第 10 次失败时预算终止一个
"仍在正常产出"的 turn。按 §4.2 的冷却曲线，该形态需要的失败次数（≥10）超出
冷却机制允许的上限（~4），**当前参数下不可能发生**；但若未来候选数增多或冷却
策略调整，累计语义的误杀面会重新打开。

### 4.4 结论与决策

**回答"还需要这个吗"：不需要**——结构边界（F1/F2/F3）已经完整覆盖了
"总重试有界、不行就停"的需求，预算是冗余保险。三个选项：

| 选项 | 内容 | 评价 |
|------|------|------|
| A | 保留但改为**连续失败语义**：链成功时 `llmFailures` 清零 | 一行改动；语义自洽（"连续 10 次失败"在当前结构下不可达，纯保险）；保留显式命名上限，防未来回归 |
| **B（已采纳）** | **直接移除预算** | **最小机制；实际保护零损失；结构边界以 CallLLM 文档注释显式记录** |
| C | 维持现状（累计语义）| 不采纳：语义上"跨 iteration 累计"没有清晰的物理含义，且候选数增多后误杀面会重新打开 |

**决策：B**（2026-09-09）。A 与 B 在当前结构下行为完全等价（都不可达），
选 B 以保持最小实现；原始需求"所有模型重试不超过一定次数"由 §2 的
F1（单次 CallLLM ≤ (1+MaxLLMRetries)×N）、F2（全败即 turn 终止）、
F3（冷却指数增长压制重复失败）共同保证，并在 `CallLLM` 文档注释中留档。

## 5. 与 pi 的 loop/turn 机制对照

参考实现：`D:\code\pi`（`packages/agent/src/agent-loop.ts`、
`packages/ai/src/utils/retry.ts` / `provider-retry.ts`、
`packages/coding-agent/src/core/agent-session.ts`）。

| 机制 | pi | picoclaw 现状 | 结论 |
|------|----|--------------|------|
| 循环结构 | 双层：外层 follow-up 队列、内层 tool-calls+steering；`stopReason==="error"` 即终止整个 loop | 单层 coordinator，CallLLM 错误即终止 turn，语义等价 | 已对齐，不动 |
| 重试分层 | transport（SDK 级，尊重 Retry-After）→ ai 调用（默认 3 次指数退避）→ session 整体重试 | 重试层 × fallback 链 + 冷却 | 粒度不同但覆盖等价，不动 |
| 错误分类 | 瞬态可重试 / **配额账单耗尽不可重试**（立即 failover）两张模式表 | ~40 模式表，除 Format/ContextOverflow 外全部可重试 | 见 §6-①，值得借鉴 |
| Retry-After | 读 `Retry-After`/`retry-after-ms`，60s 上限 | 有完整解析代码（`pkg/utils/http_retry.go`）但 **LLM 链未接入**（死代码） | 见 §6-① |
| steering | 双队列，默认 one-at-a-time | 单队列全量注入 | 见 §6-②，可选 |
| 迭代上限 | 无内置（`shouldStopAfterTurn` 钩子） | `MaxToolIterations` + 低收益循环检测 | **picoclaw 更优**（无人值守渠道需要硬上限），不搬 |
| session 层整体重试 | `_prepareRetry` 指数退避续跑 | 无（链+预算粒度更细） | 不搬；与 fork 的 /new、/switch 非阻塞语义测试耦合小但收益低 |

## 6. 后续工作

1. **Retry-After 接入 LLM 链 + 配额类错误快速 failover** —— ✅ 已实施（2026-09-09）：
   `HTTPError`/`FailoverError` 携带 `RetryAfter`（`ParseRetryAfterHeader` 支持
   delay-seconds 与 HTTP-date，上限 10 分钟）；冷却 `MarkFailureWithHint` 取
   `max(指数退避, hint)`（仅 RateLimit 类适用）；`exceeded your current quota`/
   `quota exceeded`/`usage limit`/`insufficient_quota`/`out of budget` 等从
   RateLimit 迁到 Billing；`rpm/tpm exhausted` 显式留在 RateLimit；429 状态码 +
   配额语义 body 时消息语义优先。测试锚点：`TestClassifyError_QuotaExhaustionIsBilling`、
   `TestClassifyError_429WithQuotaBodyIsBilling`、`TestClassifyError_429PlainIsRateLimit`、
   `TestParseRetryAfterHeader`、`TestHandleErrorResponseCapturesRetryAfter`、
   `TestClassifyErrorPreservesRetryAfter`、`TestCooldownHintRaisesFloor`、
   `TestCooldownHintDoesNotAffectBilling`。
2. **steering one-at-a-time 配置项** —— ✅ 确认已存在，无需实施：`steering.go`
   的 `SteeringMode` 机制与 `agents.defaults.steering_mode` 配置（config.go，默认
   one-at-a-time）均已接线；本机配置即为 one-at-a-time。早前观察到的"一次注入
   count=2"是 iteration 边界与工具间检查点两个 deposit 点各取一条的该模式正常语义。
3. 若本设计定稿，将行为差异摘要并入 `docs/design/fork-overview.zh.md` —— ✅ 已完成
   （§5 可靠性加固、对比表、同步上游注意事项）。

## 7. 同日深夜追加：双发去重、文案剩余时间、glm 非流式超时

用户实测复盘（22:41–22:47 事故：两候选同时 skipped (cooldown)、同一错误连收两条）
后的三个落地修复：

1. **turn 失败错误双发去重**：`publishTurnError` 改为返回"是否实际发出"，发出后
   `runAgentLoop` 用 `turnErrorNotifiedError` 包装错误上抛（保留 Unwrap）；
   `maybePublishError`（agent_outbound.go）`errors.As` 识别后跳过发布、返回 true
   继续后续流程。publishTurnError 未发的场景（内部渠道、streaming visible
   error、总线发布失败）不包装，旧路径照常兜底——用户对每次失败只收到一条通知。
2. **FallbackExhaustedError 文案**：skip 尝试带剩余冷却时间
   （`FallbackAttempt.CooldownRemaining`，fallback.go）；全 skip 的聚合头从
   "all N candidates failed" 改为 **"all N candidates unavailable"**，与真实上游
   失败区分——用户能看出这是"冷却保护中、会自愈"，以及该等多久。
3. **glm 非流式 120s 超时（事故根因，已确认并量化）**：当晚 glm 仅有的真实失败都
   是精确挂满 `DefaultRequestTimeout=120s` 的 awaiting-headers 超时；用户提供的
   第二段日志（23:03–23:06，pico 渠道鲁班 turn）量化了增长曲线：同一 turn 内
   glm 生成耗时 15s（iteration 2）→ 55s（iteration 3）→ >120s 被掐（iteration 4），
   随工具结果累积、上下文膨胀单调爬升。fallback 候选走非流式 `Chat`，响应头要等
   **完整生成结束**才返回，按对话时长校准的 120s 对 thinking 模型 + agentic 大
   上下文构成系统性误杀——恰好在最需要 fallback 的时刻（主模型 429 + 长上下文）
   掐死 fallback。**决策（用户拍板）：改全局默认而非 per-model 配置**——
   `DefaultRequestTimeout` 120s → **10 分钟**（`pkg/providers/common/common.go`，
   openai_compat/azure/bedrock 均引用该常量；`anthropic_messages` 原独立硬编码
   120s 也对齐共享）；glm-5.3-flash 另配 per-model `request_timeout: 1200`
   （20 分钟，重负载鲁班工作流实测就是慢）。默认值的边界：它同时是非流式调用
   唯一的挂死检测器（响应头之前无任何中间信号），须保持在"黑洞上游几分钟能
   失败转移"的量级，个别慢模型用 per-model 放宽。流式 transport 不受影响
   （独立 90s 响应头超时 + 5min 读空闲超时）。教训：**非流式默认超时必须按
   "完整生成时长"校准**；per-model `request_timeout` 保留为快模型收紧失败
   检测、或特慢模型继续放宽的旋钮。

事故时序存档（gateway.log）：deepseek-v4-flash（token.sensenova.cn）整晚真实
429（tpm/rpm）；glm-5.3-flash（open.bigmodel.cn coding plan）当晚经 fallback
成功 234 次，仅 iteration-2（工具结果返回、上下文膨胀后）的两次真实调用各挂满
120s → 双双进冷却 → 全 skip → "再试试"类消息落在冷却窗口内立即失败。两候选
端点/key 均不同，是独立失败域——"同时失效"实为两个不同故障的叠加。

新增测试锚点：`pkg/agent/turn_error_dedupe_test.go`、
`pkg/providers/fallback_exhausted_format_test.go`。

## 附：涉及文件（未提交）

- `pkg/agent/pipeline_streaming.go` — sticky 降级、冷却门控、保卡承接
- `pkg/agent/pipeline_finalize.go` — 纯文本兜底条件收紧
- `pkg/agent/pipeline_llm.go` — CallLLM 结构性边界文档注释（预算已移除）
- `pkg/agent/turn_state.go` — streamingDegraded 字段
- `pkg/agent/agent.go` — publishTurnError（§7 起返回是否发出）+ turnErrorNotifiedError
- `pkg/agent/agent_outbound.go` — maybePublishError 去重（§7）
- `pkg/providers/fallback.go` — Available 探针；§7 起 FallbackAttempt.CooldownRemaining 与 unavailable 文案
- `pkg/channels/feishu/feishu_stream.go` — 空卡删除
- 测试：`pkg/agent/pipeline_streaming_degrade_test.go`（新）、
  `pkg/agent/turn_error_dedupe_test.go`（新，§7）、
  `pkg/agent/pipeline_streaming_test.go`、`pkg/providers/fallback_available_test.go`（新）、
  `pkg/providers/fallback_exhausted_format_test.go`（新，§7）、
  `pkg/channels/feishu/feishu_stream_cancel_test.go`（新）
