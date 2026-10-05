# Split-Turn 摘要化设计（长任务上下文管理第三块）

> 状态：**已实现**（2026-10-05，同日设计同日落地；实现要点与设计差异见文末"实现记录"）。日期：2026-10-05。
> 前置阅读：`docs/design/tui-client-design.zh.md` 无关；本设计的参照系是 pi 的
> `packages/coding-agent/src/core/compaction/compaction.ts` 与本仓
> `pkg/agent/iteration_compact.go`（①迭代边界压缩，已实现）。

## 1. 问题

单 turn 内的上下文增长（代码评审：几十次工具调用，每次工具结果数千 token）有两种现有防线，但都救不了「活动 turn 超限」：

- **回合尾异步压缩**（compact_schedule.go）：turn 结束后才检查，长 turn 中途已超窗。
- **迭代边界压缩**（iteration_compact.go，2026-10-05 新增）：LLM 调用前同步压缩稳定历史——但**活动 turn 的消息受 splitHistoryForActiveTurn 保护不可丢弃**，工具结果密集时稳定历史已压无可压，活动尾部独占窗口，压缩后仍超限 → `fit=false` → 「refusing to drop active turn messages」显式失败。
- **重试链的 trimHistoryToFitContextWindow** 同样只裁稳定历史。

死结：活动 turn 消息 = 本轮 user 消息 + 已发生的 assistant 推理/工具调用 + 工具结果。工具结果是对话推进的事实记录（OpenAI 协议要求 tool_calls 与 tool 结果配对完整），**丢弃即破坏协议**；但 40 次迭代 × 每次几 KB 的工具输出可以轻松吃掉 100k+ token。

## 2. pi 的解法（参照）

pi 的切点算法（compaction.ts:351-501）允许**把同一个 turn 从中间切开**：

- 切点只能落在 user / assistant / custom 消息上，**永不落在 toolResult 上**（保证配对完整）；
- 从尾部向前累计 `keepRecentTokens`（20k 估算）决定切点；
- 切点之前的「turn 前缀」（含其中的工具调用与结果）整体喂给摘要模型，用专门的 `TURN_PREFIX_SUMMARIZATION_PROMPT` 生成 "Turn Context (split turn)" 段；
- 摘要作为 user 消息注入（"The conversation history before this point was compacted..."），后续请求只见 [摘要 + 保留的尾部]。

即：**不丢弃活动 turn，而是把它的老前缀摘要化**。信息以有损摘要形式保留，协议配对在切点处天然完整（切点前的 tool_calls 连同其结果一起进摘要输入，不再出现在请求里）。

配套细节（同文件）：
- 摘要输入降载：喂给摘要模型时 tool result 截 2000 字符、序列化成 `[Tool result]` 文本、包 `<conversation>` 标签防续写（utils.ts:114-155）；
- 摘要失败防护：stopReason=error/length 的摘要拒绝落盘；摘要试图调工具视为失败；
- 迭代式摘要：取上次 compaction 的 summary 作 previousSummary 做增量合并，不是摘要的摘要链。

## 3. 本仓设计

### 3.1 触发点

在 `iteration_compact.go` 的现有流程内扩展，不新增触发路径：

```
CallLLM 迭代边界检查
  └─ 估算超窗 → Compact(稳定历史)  [现有]
       └─ 重新 Assemble + 拼活动尾部 → 再估算 [现有]
            └─ 仍超窗（today: fit=false 显式失败）
                 └─ 【新】split-turn 摘要化：把活动尾部切出「前缀/保留尾」，
                    前缀摘要化后重装，再估算；仍超窗才走 fit=false 失败
```

即 split-turn 是迭代边界压缩的**第二段降载**，只在稳定历史压缩后仍超窗时触发。触发节流：每 turn 至多一次（turnState 记 `splitTurnDone bool`），防摘要风暴。

### 3.2 切点选择（协议安全优先）

对活动尾部消息序列 `[user, asst₁(+tool_calls), tool₁…toolₙ, asst₂(+tool_calls), tool…, asstₖ]`：

1. 从尾部向前累计 token（EstimateMessageTokens），达到 `keepRecentTokens`（默认 8k，配置 `agents.defaults.split_turn_keep_tokens`）处为初始切点；
2. 切点必须**向前吸附到某个 assistant 消息（不带未闭合 tool_calls）或 user 消息的边界**——保证切点之后不存在悬空的 tool 结果引用（tool result 的 parent assistant 在切点前）。吸附算法与 `trimCandidateStarts` 同型；
3. 找不到合法切点（活动尾部本身 < keepRecentTokens 却仍超窗）→ 放弃，走 fit=false 既有路径（此时是输出预算问题，交给 clamp）。

### 3.3 摘要生成

- 输入：切点前的活动尾部消息序列化（复用 legacy `summarizeBatch` 的序列化格式，新增 tool result 截断 2000 字符——摘要不需要全量输出）+ 本 turn 的 user 消息开头（让摘要知道任务目标）；
- 提示词（新增 `turnPrefixSummarizationPrompt`，中文）：说明这是同一场任务的中途压缩，要求保留：任务目标、已完成的检查项、发现的问题清单（编号）、当前正在做什么、下一步计划；不保留工具输出的原文细节；
- 模型：主模型（与 pi 一致），`max_tokens = min(4096, clampMaxTokensToContext(...))`；**走独立 LLM 调用，不进 fallback 链**（摘要失败只是降载失败，不应触发候选轮换）；
- 失败语义：返回 error → iteration_compact 记告警，视同未压缩继续（现有兜底不变）。摘要成功但 stop_reason=length（截断）→ 接受（截断的摘要仍优于丢弃）。

### 3.4 重装与协议完整性

摘要后的请求消息结构：

```
[系统提示词（含稳定历史 Summary + 本 turn 前缀摘要）]
[user 原始消息 → 替换为：user 原始消息 + "\n\n<history>\n{turnPrefixSummary}\n</history>"]
```

关键决策：**前缀摘要注入在 user 消息内**而非新造 user 消息——OpenAI 协议里 turn 以 user 开头，新造消息会改变 turn 边界语义；注入原 user 消息保持「本轮任务是什么」的语境连续。保留尾部的 assistant/tool_calls/tool 结果序列原样跟在后面，配对完整。

会话持久化：**不落盘前缀摘要**（它只是单次请求的降载视图）。理由：turn 结束后完整历史仍在 JSONL 里（含全部工具结果），下回合的常规压缩会正常摘要整段；把 split-turn 摘要写进历史会让「有损视图」污染「事实记录」。副作用：若 turn 内连续多次 split（被节流限制为一次），第二次不存在——一次 8k 保留尾 + 摘要前缀后仍超窗的只剩输出预算问题。

### 3.5 与 fork 保护区的关系

- **seahorse 引擎**：split-turn 不调用 `seahorse.Compact`（那操作 store 内的稳定历史），只在请求装配层（pipeline）操作活动尾部消息切片——`context_seahorse.go` 零改动；
- **封口语义**（sealDanglingToolCalls）：切点吸附保证请求内配对完整，落盘历史不受影响，封口语义不变；
- **续段/自动继续**（runAgentLoop）：中间段的 split-turn 摘要不落盘 → 续段重建时用完整历史 → 续段首次装配可能又触发 split-turn（新 turn 新节流）——行为正确；
- **steering**：压缩与摘要期间到达的 steering 消息在下一次 LLM 调用前的既有 poll 点被拾取，无新增竞态。

## 4. 配置

```jsonc
"agents": { "defaults": {
  "split_turn": {
    "enabled": true,            // 省略=开（*bool，对齐 fork 惯例）
    "keep_recent_tokens": 8192, // 保留尾部的 token 预算
  }
}}
```

## 5. 测试计划

- 切点吸附：悬空 tool 结果吸附前移（`TestFindSplitTurnCutPoint_*`）；
- 协议完整性：split 后请求消息里每个 tool 结果都有配对的 tool_calls（遍历断言）；
- 摘要注入位置：user 消息内、保留尾原样（`TestBuildSplitTurnMessages`）；
- 节流：同 turn 第二次超窗不再 split；
- 摘要失败/落盘隔离：fake 摘要 provider 失败 → 行为同未压缩；JSONL 无摘要消息；
- 集成：40 次大工具输出的合成 turn，断言第二次 LLM 调用的请求 token 在窗口内。

## 6. 工作量与风险

约 1.5~2 天（切点+重装 0.5 天、摘要提示词与调用 0.5 天、测试 0.5~1 天）。主要风险：

1. 摘要质量决定模型能否延续任务——提示词需要用真实代码评审会话调优（先上灰度：日志记录 split 前后的 token 与后续 turn 的失败率）；
2. 阈值交互：keepRecentTokens 太小 → 模型丢失正在做的事的细节；太大 → 降载不足。默认 8k 是 pi 经验值（20k）与窗口占比的折中，需实测；
3. `EstimateMessageTokens` 估算偏差在切点选择上被放大——切点吸附加 8k safety 已缓冲，超窗兜底仍是 fit=false 显式失败，无静默路径。


## 7. 实现记录（2026-10-05）

与设计的差异与落地要点：

- **触发接线**：`compactBeforeLLMCall`（iteration_compact.go）第一段压缩重装后重估算，仍超窗即调 `doSplitTurnCompact`——包括「rebuild 不保形、请求视图未替换」的情形（旧视图直接进第二段）。
- **重装保形验证**（实现中发现的必要防御）：`BuildMessagesFromPrompt` 重建可能折叠/归组 turn 消息（实测 8 条→6 条），`currentTurnStart = len−tailLen` 算术失真甚至为负。重装后用 `matchingTurnMessageTail(rebuilt, activeTail) == len(activeTail)` 验证保形，不保形则保持旧请求视图（压缩经 exec.history 下回合生效），本次超窗由 split-turn 在旧视图上处理。
- **currentTurnStart 守卫**：`<0 || >= len-1`（stable 为空合法——ContextBuilder 为 nil 的路径重装后 stable 可为空）。
- **摘要调用**：`exec.activeProvider.Chat` 单次直调（不经 fallback 链），`max_tokens=4096, temperature=0.3`；EmptyCompletionError/空内容一律视为失败、fail-open 不断路、节流不置位；`stop_reason=length` 的截断摘要接受。
- **切点吸附**：从尾累计 keepTokens 后向前扫过 tool 消息（`isToolResultMessage`：role=tool 或带 ToolCallID），落在 user/assistant 边界。
- **落盘隔离**：只改 exec.messages/callMessages/currentTurnStart，store 与 JSONL 不动。
- 测试锚点：`TestFindSplitTurnCutPoint_{NeverLandsOnToolResult,TailUnderBudgetReturnsMinusOne}`、`TestBuildSplitTurnMessages`、`TestDoSplitTurnCompact_{RewritesRequestView,ThrottledOncePerTurn,FailureKeepsContext,DisabledByConfig}`、`TestSerializeTurnPrefix_CapsToolResults`、`TestSplitTurnConfigDefaults`、`TestCompactBeforeLLMCall_SecondStageFiresSplitTurn`。
- 遗留调优项：摘要提示词（turnPrefixSummarizationPrompt）未经真实代码评审会话调优；`keep_recent_tokens` 默认 8192 待实测校准（pi 用 20k）。
