# 代码评审：Split-Turn 摘要化（长任务上下文管理第三块）

> 评审日期：2026-10-05
> 评审对象：`bcbcc4da`（split-turn 摘要化落地）+ 需求来源 `476454c7`（迭代边界压缩 + split-turn 设计）
> 设计文档：`docs/design/split-turn-compaction-design.zh.md`
> 评审人：派克（Pike）
> 结论：**设计优秀，实现基本忠实；1 个功能性缺陷（P1）需修，若干规格偏差与测试盲区建议补齐。** 安全维度无高危发现。

---

## 1. 评审范围与方法

| 文件 | 变更 |
|---|---|
| `pkg/agent/iteration_compact.go` | +284（split-turn 主体） |
| `pkg/agent/instance.go` | +31（配置字段接线） |
| `pkg/agent/turn_state.go` | +12（`splitTurnDone` 节流位） |
| `pkg/config/config.go` | +30（`SplitTurnConfig`） |
| `pkg/agent/split_turn_test.go` | +331（测试） |
| `docs/design/split-turn-compaction-design.zh.md` | +16（实现记录） |

验证手段：

- `go build ./pkg/agent/... ./pkg/config/...` — 干净；
- `go test ./pkg/agent/ -run "SplitTurn|...SecondStage"` — 9 个 split-turn 测试全绿；
- **自建配对压力探针** `TestProbeSplitTurnPairing`：2 种 tail 布局 × 80 组 `keepTokens` 值，断言「保留尾内无孤立 tool 结果、前缀内结果无对应调用留存于保留尾」——通过（探针已删除，未入库）。

---

## 2. 关键设计确认（为什么这个实现方向是对的）

Split-turn 解决的是真实死结：活动 turn 的 tool 结果受 `splitHistoryForActiveTurn` 保护不可丢弃，工具输出密集时稳定历史已压无可压 → 此前唯一结局是 `fit=false` 显式失败。

三个决策值得确认：

1. **切点吸附协议安全是本案最漂亮的决策** —— 切点永不落在 tool 消息、吸附到 user/assistant 边界，使「前缀内 `assistant(tool_calls)` 与结果要么整体进摘要、要么整体保留」，从算法层面消除悬空配对。压力探针独立验证成立。
2. **只改请求视图、不落盘** —— turn 结束后 JSONL 事实记录完整，常规压缩自然接管，避免有损摘要污染事实记录。职责边界清晰。
3. **摘要直调主模型、不进 fallback 链** —— 摘要失败只是降载失败，不该触发候选轮换。语义正确。

---

## 3. 问题清单（按严重程度分级）

### P1 — 高（应修）

#### P1-1 媒体/附件在重写 user 锚点时被静默丢弃

**位置**：`pkg/agent/iteration_compact.go:306` `buildSplitTurnMessages`、`:322` `turnUserText`

```go
rewritten := providers.Message{Role: "user", Content: turnUserText +
    "\n\n<history>\n" + summary + "\n</history>"}
```

`providers.Message`（`protocoltypes/types.go`）除 `Role`/`Content` 外还携带：

- `Media []string`
- `Attachments []Attachment`
- `SystemParts []ContentBlock`
- `ModelName`、`CreatedAt`

重建时这些字段全部丢失；`turnUserText()` 亦只读 `Content`。

**影响**：多模态轮次（贴截图让 agent 评审——恰是长评审 turn 的常见形态）一旦触发 split-turn，**图片在上游调用里彻底消失**，后续模型只能凭摘要猜图内容。这是静默数据丢失，非降级；无日志、无测试覆盖。

**为何 P1**：设计 §3.4 明确「注入原 user 消息保持语境连续」，只搬 `Content` 恰恰破坏了该承诺。

**修改建议**：

```go
// 克隆原 user 消息再追加 summary，保留 Media/Attachments/SystemParts
anchor := tail[userIdx]
anchor.Content = anchor.Content + "\n\n<history>\n" + summary + "\n</history>"
// 找不到 user 时才退回裸锚点（保留现有 fallback 行为）
```

相应把 `turnUserText(tail) string` 改为 `turnUserAnchor(tail) (providers.Message, int)`。

**补充测试**：带 `Media` 的 user 消息经 `buildSplitTurnMessages` 后 `Media` 仍存在。

---

### P2 — 中（建议修）

#### P2-2 规格偏差：摘要输入未独立强调「本 turn 任务目标」

**位置**：`iteration_compact.go:362` 摘要输入拼装

设计 §3.3 要求摘要输入 =「切点前尾部序列化 **+ 本 turn 的 user 消息开头**」。实现依赖「user 恰在前缀内被 `serializeTurnPrefix` 带出」。若切点吸附后 user 不在前缀（`turnUserText` 从整条 tail 找 user，说明该情形在代码逻辑上被承认存在），**摘要输入会缺任务目标**。

**修改建议**：将 `turnUserText` 的结果作为独立段落显式拼进摘要 prompt 顶部，确保任意切点下任务目标都在输入里。成本极低，消除不确定性。

#### P2-3 `keepTokens<=0` 兜底与 config 兜底重复 + 魔法数散落

**位置**：`iteration_compact.go:208` 与 `config.go:498`

`findSplitTurnCutPoint` 内部再次 `if keepTokens <= 0 { keepTokens = 8192 }`，而 `EffectiveKeepRecentTokens()` 已保证 ≤0 → 8192。两处 `8192` 各自硬编码。

**修改建议**：抽 `const defaultSplitTurnKeepTokens = 8192`，config 与函数共用；函数内兜底保留作防御但引用同一常量。

#### P2-4 摘要调用无独立超时治理

**位置**：`iteration_compact.go:362` `exec.activeProvider.Chat(ctx, ...)`

使用 turn 的 `ctx`，不受 CallLLM 重试链里 `providerCtx` 超时保护（split-turn 在其之前）。设计说「摘要失败只是降载失败」，但未定义**超时**边界。一个挂死的摘要请求会把整个 turn 拖住。

**修改建议**：摘要调用套独立 `context.WithTimeout`（如 60s），超时视同失败 fail-open。

#### P2-5 落盘隔离缺回归锚点

设计 §3.4 强调「不落盘前缀摘要、JSONL 不动」，实现正确，但测试计划里的「JSONL 无摘要消息」锚点**未落地**（`split_turn_test.go` 无对应断言）。

**修改建议**：补 `TestSplitTurn_DoesNotPersistSummary`，split 后断言 `ts.persistedMessagesSnapshot()` / store 中无 `<history>` 摘要消息。

---

### P3 — 低（可选）

#### P3-6 配置未进用户文档

`split_turn` 出现在 `config.go` 与设计文档，但 `docs/guides/configuration.zh.md` 与 `docs/design/fork-overview.zh.md` 均未登记。本仓 fork 惯例是配置项必进 configuration guide（对照 `fresh_tail_messages`、`trust_configured_context_window` 都有专门小节）。

**修改建议**：补 `split_turn.enabled / keep_recent_tokens` 配置小节，并同步 fork-overview 修复清单。

#### P3-7 `firstNRunes` 每次全量 `[]rune(s)` 拷贝

`serializeTurnPrefix` 对每条消息调用 `firstNRunes`，大内容每次全量转 rune 再截断，长 turn 里累积 O(总前缀字节)。

**修改建议**（性能，非阻塞）：截断先按字节粗切再 rune 修正，或先 `len()` 快速判空。当前量级不构成问题，仅记录。

#### P3-8 字段重排引入无关 diff 噪音

`turn_state.go`、`instance.go` 的 struct 字段对齐被 gofmt 整体重排（如 `Provider`→`LoopDetection` 20 行），由新字段插入触发，功能无关。

**修改建议**：无碍，仅提示评审时忽略；若追求最小 diff 可将新字段置于结构体末尾。

#### P3-9 提示词硬编码中文

`turnPrefixSummarizationPrompt` 为纯中文，仓库面向多语言用户。设计 §7 已列为「遗留调优项」。

**修改建议**：无需现在改，建议 prompt 增加「用对话原本语言输出摘要」，避免英文会话被压成中文摘要。

---

## 4. 规格覆盖核对表

| 规格条目 | 状态 |
|---|---|
| 触发点=迭代边界第二段、节流每 turn 一次 | ✅ `ts.splitTurnDone` |
| 切点永不落在 tool 结果、吸附到 user/assistant 边界 | ✅ 已探针验证配对完整 |
| 找不到合法切点 → 走 `fit=false` | ✅ `p<=0` 返回 |
| 前缀摘要化、保留尾原样 | ✅ |
| 摘要注入 user 消息内（不新造 user） | ⚠️ 形式满足，但丢 `Media`/`Attachments`（P1-1） |
| 摘要走独立调用、不进 fallback | ✅ 直调 `activeProvider.Chat` |
| 失败 fail-open、不置节流 | ✅ 测试覆盖 |
| `stop_reason=length` 截断摘要接受 | ✅ 注释 + 接受 |
| 不落盘、JSONL 不动 | ✅ 实现正确，⚠️ 缺测试（P2-5） |
| 配置 `*bool` 省略=开、默认 8192 | ✅ 测试覆盖 |
| keepRecentTokens 从尾部累计 token | ✅ |
| 摘要输入截 tool result 2000 字符 | ✅ 测试覆盖 |
| 与 seahorse / 封口 / 续段 / steering 关系 | ✅ `context_seahorse.go` 零改动，注释说清 |

---

## 5. 测试覆盖缺口

| 缺口 | 关联 |
|---|---|
| 媒体 user 消息经 split 的保真 | P1-1 |
| JSONL / 落盘隔离断言 | P2-5 |
| 摘要调用超时 / 取消路径 | P2-4 |
| `TestDoSplitTurnCompact_RewritesRequestView` 结构断言用 `t.Logf` 而非 `Fatal`，形同虚设 | 建议收紧为精确断言 |
| 真实 `ContextBuilder` 重建路径 | `TestCompactBeforeLLMCall_SecondStageFiresSplitTurn` 用 fake ContextManager 返回空 history，掩盖真实重建；测试中已出现 `Post-compact rebuild changed the turn tail shape` 警告，说明不保形分支在真实路径下是活的，值得补集成用例 |

---

## 6. 安全维度小结

**无 P1/P2 发现。**

- **注入面**：摘要输入为本地拼装纯文本，无外部可控模板；`serializeTurnPrefix` 截断无越界。
- **敏感信息**：不落盘是设计目标，且实现一致，无泄漏面。
- **协议完整性**：切点吸附消除不完整调用序列，无注入式协议破坏。

---

## 7. 修复优先级建议

1. **P1-1**（媒体丢失）—— 决定多模态会话是否安全，必须先修，附测试。
2. **P2-4**（摘要超时）—— 生产健壮性，建议随批修。
3. **P2-2 / P2-3 / P2-5** —— 规格一致性与回归锚点，低风险易补。
4. **P3-x** —— 文档与可读性，可并入后续 docs 同步。


---

## 8. 核实与修复记录（2026-10-05，实现方复核）

对派克评审逐条核实（对照 `bcbcc4da` 的实际代码），结论与修复如下：

| 项 | 核实结论 | 处置 |
|---|---|---|
| P1-1 媒体/附件丢失 | **坐实**。`providers.Message{Role, Content}` 重建确实丢弃 Media/Attachments/SystemParts；多模态评审 turn 一旦触发 split 即静默丢图。 | 已修：锚点改为**克隆原 user 消息**再追加 summary（`turnUserAnchor` 返回整条消息），补 `TestBuildSplitTurnMessages_PreservesMediaFields`（含原消息不被原地污染的断言）。 |
| P2-2 摘要输入缺任务目标 | **部分不成立**。切点 `p ≥ 1` 恒成立（累计循环 `i > 0`），user 恒为 `tail[0]` 且必在 prefix 内——「user 不在前缀」的结构性场景不存在。但显式拼入成本极低且对未来 turn 形态变化免疫，作为防御性强化采纳。 | 已修：摘要输入顶部显式加 `[任务目标]` 段（取 `turnUserAnchor` 前 2000 字符）。 |
| P2-4 摘要无独立超时 | **成立，严重度修正**。「把整个 turn 拖住」受 provider HTTP 超时上限约束（全局 10min / glm 1200s），非无限挂死；但 60s 预算远低于该上限，独立超时仍是正确设计。 | 已修：`context.WithTimeout(ctx, 60s)`（`splitTurnSummarizeTimeout`），超时 fail-open。 |
| P2-3 魔法数重复 | **坐实**。 | 已修：`defaultSplitTurnKeepTokens = 8192` 单源，config 与切点兜底共用。 |
| P2-5 落盘隔离缺锚点 | **坐实**。 | 已补：`TestDoSplitTurnCompact_DoesNotPersistSummary`（persisted 快照长度不变 + 无 `<history>`/摘要文本泄漏 + exec.history 干净）。 |
| §5 宽松断言（`t.Logf`） | **坐实**。 | 已收紧：`RewritesRequestView` 的形状断言改为 `t.Fatalf` 精确断言（3 条：sys + anchor + kept）。 |
| §5 rebuild 不保形分支 | 属实（测试日志可见该分支活跃）。 | 该分支行为正确（保持旧视图 + 告警），本轮不改；真实 ContextBuilder 集成用例仍留作后续。 |
| P3-6/7/8/9 | 全部属实（低）。 | P3-6 配置文档留待 docs 同步批处理；其余记录在案不动。 |

**净结论**：评审质量高——P1-1 是本实现唯一的功能性缺陷且被准确捕获；P2-2 的「规格偏差」定性偏严（结构上恒满足），其余 P2/P3 全部核实成立。修复后新增 2 个测试锚点 + 收紧 1 处断言，split-turn/idle 全部相关测试绿。
