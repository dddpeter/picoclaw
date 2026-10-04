# 飞书流式卡片 Bug 排查与评审（2026-10-04）

状态：**已修复（三批次 + 第二批 B6/优化全部落地）**｜排查与评审 2026-10-04，修复执行 2026-10-04｜排查基线：`a5ee5057`

本文记录对飞书 CardKit 流式卡片链路的一次端到端排查：通道侧（`pkg/channels/feishu/`）+
streamer 包装（`pkg/channels/manager.go`）+ 管线侧（`pkg/agent/pipeline_streaming.go`、
`pipeline_finalize.go`、`progress_heartbeat.go`）。共确认 5 项问题（2 高 1 中 2 低），
全部经代码走读定位、其中 2 项用假 Feishu 服务端（httptest + `lark.WithOpenBaseUrl`）
写了复现测试实际触发（复现代码已验证后删除，基线测试保持绿色）。

## 0. 排查方法与证据等级

- 走读范围：`feishu_stream.go`、`feishu_stream_card.go`、`feishu_reply.go`、`feishu_64.go`、
  `manager.go`（GetStreamer/splitMarkerStreamer/finalizeHookStreamer）、
  `pipeline_streaming.go`、`pipeline_finalize.go`、`agent.go`（publishTurnError）、
  `progress_heartbeat.go`、`session/allocator.go`（会话维度）。
- 复现手段：httptest 假服务端应答 `tenant_access_token` / cardkit create / im create /
  cardkit update / settings 五类端点，构造 `FeishuChannel` 后直接驱动 streamer 生命周期。
- 行号引用以排查基线 `a5ee5057` 为准。
- 评审基准：`docs/design/turn-llm-failure-resilience.zh.md`（§3.1 卡片生命周期与错误
  抑制）、`docs/design/long-task-execution.zh.md`（§4/§8.2 心跳 keep-alive）、
  `docs/design/fork-overview.zh.md` §1（飞书流式卡片功能地图）。

## 1. 总表

| # | 定级 | 一句话 | 触发条件 | 现网暴露面 |
|---|------|--------|----------|-----------|
| B1 | **P0** | 超长答案（卡片 JSON>30KB）→ Finalize 本地拒绝 → 卡片永不封板 + 用户零反馈 | 最终答案 ≳1 万汉字 / 30KB 代码输出 | 默认配置即暴露；长代码输出场景常态触发 |
| B2 | **P1** | `streams` 按 chatID 键控、无 turn 身份：并发 turn 抢卡、TTL 过期产幽灵卡、误删他人条目 | 同 chatID 并发 turn（`session.dimensions` 含 sender）或跨 turn 静默 >2min | **配置门控**：默认 dims=`["chat"]` 下同会话串行，B2a 不触发；`dm_scope=per-channel-peer`/`per-peer` 配置暴露 |
| B3 | P2 | 进度心跳以 `KindText` 走 AppendToolStep → 污染灰字叙事轨与「本轮说明」面板 | 长静默任务（默认 180s 一次心跳） | 默认配置即暴露，纯 UX 噪音 |
| B4 | P3 | `truncateFeishuReasoning` 用 `len()` 字节冒充"字"数 | 中文推理文本截断时 | 纯文案误导 |
| B5 | P3 | 图片 URL 含 `)` 时 `sanitizeFeishuMarkdownImages` 误截 | markdown 图片 URL 带括号（维基百科风格） | 小概率外观问题 |

## 2. B1（P0）：超长最终回答让卡片永不封板，且双重兜底全部静默

### 2.1 故障链（每一环都有代码/复现佐证）

1. `buildFeishuCardWithinSize`（feishu_stream_card.go:439）的预算循环只缩减 **panel**
   文本预算（16000→8000→3000→1000→0），**从不裁剪 answer 本身**；封卡时
   `buildFeishuFinalCardBudget`（:889）把完整答案放进 markdown 元素。
2. 答案 ≳1 万汉字（≈30KB UTF-8 + 卡片脚手架）→ 最终卡片 JSON 超限。复现实测：
   12000 汉字 → **36813 字节**。
3. `cardkitUpdateCard`（feishu_stream.go:815）在 :823 本地拒绝
   （`card json 36812 bytes exceeds Feishu 30KB limit`）——这是防御性本地检查，
   行为正确；问题在调用方的善后。
4. `FinalizeWithContext`（feishu_stream.go:631）此时已 `s.done=true` 并
   `defer streams.Delete`（:662），随后 `updateCard` 失败**直接返回错误**：
   `closeStreaming`（:848）永远不会执行 → 卡片服务端永远停留在 streaming 模式，
   状态行「✍ 正在生成回答…」+ 加载动画永久转圈。
5. 管线侧 `finalizeConfiguredStreamingLLM`（pipeline_streaming.go:250）：答案已
   流式输出过 → `visibleBeforeFinalize=true` → 返回
   `configuredStreamingVisibleError`（:272）并把 publisher 置 nil——此后**任何
   Cancel 兜底都够不着这张卡**（:281 的 detached Cancel 只在"未见输出"分支）。
6. **双重静默**：
   - `publishTurnError`（agent.go:766）对 visible error **跳过错误通知**；
   - `pipeline_finalize.go:108` 的 hermes 式纯文本兜底同样以
     `!isConfiguredStreamingVisibleError(streamErr)` 排除。

最终用户体验：打字机打完的答案留在卡片上，配一个永久转圈的状态行；无错误提示、
无文本兜底、无中断标记。答案"看起来在继续生成"，实际上是死卡。

### 2.2 与既有设计的冲突点（评审）

`turn-llm-failure-resilience.zh.md` §3.1 明确设计："可见输出后的流式失败不重复通知
（卡片已说明一切）"、"纯文本消息降为最后兜底：仅当卡片连 Finalize 都失败时才发
普通消息"。这套抑制的**隐含前提是"visible error ⇒ 卡片已封板"**。B1 恰好打破这个
前提：Finalize 失败于 updateCard 这一步，卡片既没封、兜底又被抑制。所以 B1 不是
"设计没考虑用户"，而是**设计前提在一条新失败路径上失效**——修复方向应当是恢复
前提（保证卡片一定被封 + 答案一定送达），而不是拆掉抑制（拆掉会回到 §1 记载的
"中断卡刷屏"事故形态）。

另注：该故障在中途就有前兆——流式过程中 panel 刷新（全卡更新）同样携带答案快照，
一旦快照+脚手架 >30KB，`flushPanelLocked` 的更新就开始持续失败（`logPanelErr`
静默降级，面板冻结但打字机继续）。这不是独立 bug，但可作 B1 的观测信号；且在
200850 降级模式（`streamingLost`）下答案只靠全卡刷新承载，超限即**答案彻底停止
更新**，比常规路径更糟。

### 2.3 修复方案（决策表）

| 选项 | 内容 | 评价 |
|------|------|------|
| **A（推荐）** | Finalize 路径给答案设卡片内预算（如 24KB）：超出部分截断并在卡片尾部注「⏬ 内容过长，余下部分已分段发送」，截断的余量用 `channels.SplitMessage` 按普通卡片消息补发（Send 路径；QQ 通道已有同款先例 manager.go:1601） | 治本：答案不丢、卡片必封；不引入错误卡（补发是正常内容消息，非 error 通知，不违反 §3.1 抑制） |
| B | Finalize 失败时降级重试一次「截断答案的封卡 + closeStreaming」，错误照旧上抛 | 保底：只保证卡片被封 + 中断标记，用户仍丢长答案尾部；实现最小 |
| C | 管线侧让 visible error 不再跳过 publishTurnError（需向 Streamer 查询"卡片是否已封"） | 治标：改管线错误分类语义，面大；与 §7 双发去重逻辑耦合，易回归 |
| 组合 | **A + B**：A 是主路径，B 作为 A 失败后的最后保底 | 推荐：任何路径下卡片必封、答案必达 |

附带一致性修正：`buildFeishuRefreshCard` 的答案快照同样套预算（仅刷新卡截断；
`streamingLost` 降级模式例外——降级模式本就该走 A 的分段补发，属同一改动面）。

## 3. B2（P1）：streams map 无 turn 身份——抢卡、幽灵卡、误删

### 3.1 结构事实

- `FeishuChannel.streams`（feishu_64.go:56）按 `chatID -> *feishuCardStreamer` 键控，
  **值上没有任何 turn/session 身份**；`BeginStream`（feishu_stream.go:167）的复用
  判据只有 `!s.done && time.Since(s.lastAt) < feishuStreamReuseTTL(2min)`。
- `GetStreamer`（manager.go:622）签名里有 `sessionKey`（仅用于流抑制键
  `streamSuppressionKey`，:227），**没有传进 BeginStream**。
- 同 turn 内每次 LLM 迭代都会走一遍 `GetStreamer→BeginStream`
  （pipeline_streaming.go:71）——复用逻辑为迭代连续性设计，但无法区分"同一 turn
  的下一次迭代"与"另一个 turn 的第一次迭代"。

### 3.2 三个变体

**B2a 抢卡（答案丢失，最重）**：同 chatID 两个 turn 并发（跨会话，fork 默认允许），
turn A 的流式卡存活且 `lastAt` 新鲜 → turn B 的 `BeginStream` **复用 A 的卡**：
两 turn 答案交替覆盖同一答案元素、LLMCalls 混计；先结束者 Finalize 封卡，
后结束者 `FinalizeWithContext` 见 `s.done=true` **静默 return nil**（feishu_stream.go:631
开头的幂等保护）→ 管线认为已投递，**该 turn 的完整答案就此丢失，零报错**。

**B2b 幽灵卡（TTL 过期不封旧卡）**：`lastAt` 超 2min → 复用拒绝 → 建新卡并
`streams.Store` 覆盖。旧 streamer **没有任何路径 seal**（复现实测：旧卡
`done=false, updateCardCalls=0, closeStreamingCalls=0`）——旧卡永久停留在
streaming 模式转圈。窗口真实存在：默认心跳 180s（config/defaults.go:45）**大于**
TTL 120s，静默 120s–180s 之间无任何 keep-alive；`BeginStream` 若恰好在此窗口被
另一个 turn 调用即触发。同 turn 内部倒难触发——工具启动/完成的 `AppendToolStep`
都会刷新 `lastAt`，下一次 `BeginStream` 紧随其后。

**B2c 误删条目（卡片churn）**：A 的卡被 B 覆盖后，A 结束时
`defer s.ch.streams.Delete(s.chatID)`（feishu_stream.go:662/720）删掉的是 **B 的
条目** → B 下一次迭代查不到自己 → 再开一张新卡。每张卡最终仍会被各自 owner 封板
（对象引用仍有效），但用户看到一 turn 多卡。

### 3.3 触发条件评审（诚实定级）

默认 `session.dimensions=["chat"]`（config/defaults.go:74）：一个 chat 一个会话，
同会话消息经 steering 串行 → **默认配置下 B2a/B2b 的跨 turn 路径都不触发**。暴露
条件是 `dm_scope=per-channel-peer`（dims 变为 `["chat","sender"]`，config.go:422）
或 `per-peer`（dims 仅 `["sender"]`，同群多 sender 同样共 chatID）+ 群聊多 sender 并发。因此定 P1 而非 P0：**默认安全、特定配置下出现静默丢答案**。
但 B2 的后果形态（答案静默丢失）与故障定位难度远高于 B1，且 `BeginStream` 复用
逻辑对"谁能调用"完全没有假设，属结构性缺口——上游一旦改动并发语义（如放开同
会话并发）会立即扩大暴露面。

### 3.4 修复方案（决策表）

| 选项 | 内容 | 评价 |
|------|------|------|
| 1（最小止血） | ① TTL 拒绝复用时对旧 streamer 做 best-effort 封板（`CancelWithReason`，独立 ctx，done 幂等保护已有）——杀 B2b；② Finalize/Cancel 的 defer Delete 改为"map 里还是自己才删"（CAS 语义，`Load`+指针比对后 `CompareAndDelete`）——杀 B2c | 两处局部改动、零接口变更；B2a 仍在 |
| 2（结构修复） | streamer 构造时绑定身份并让 `GetStreamer` 传递：扩展可选能力接口（如 `SessionBoundBeginStream(ctx, chatID, sessionKey)`），feishu 实现之；复用仅当身份匹配或旧卡已 done，否则新卡 + 旧卡 best-effort 封板 | 杀全部三变体；接口面改动，需同步评估 pico/telegram 等通道（pico 同 chatID 复用消息语义，大概率同病但后果更轻，需另查） |
| 组合 | **1 先行（独立小改），2 作为后续结构项** | 推荐：止血与结构解耦，1 不阻塞 2 |

## 4. B3（P2）：进度心跳污染叙事轨与过程面板

`publishProgressBeat`（progress_heartbeat.go:144）对流式会话用
`bus.ToolStep{Kind: ToolStepKindText, Result: 心跳文案}` 走 `AppendToolStep`。
feishu 侧 `AppendToolStep` 对 KindText 完成态做两件事：钉入灰字叙事轨
（`pinNarrationLocked`，feishu_stream.go:145，5 行封顶挤掉真实草稿）+ 归档为面板
「💬 本轮说明」条目。于是 10 分钟静默任务 ≈ 3 条「⏱ 进度：…」占据叙事轨全部
5 行，封卡后满屏心跳噪音。

**评审**：这是 long-task-execution §8.2「心跳兼作 200850 keep-alive」的有意副作用
被低估——keep-alive 本意是让 `lastAt`/面板写入保持活性，但复用 KindText 让心跳
承袭了"草稿归档"的展示语义。修复必须**保留 keep-alive 效果**（刷新 lastAt +
一次面板刷新），只改展示形态：给 `bus.ToolStep` 加独立 kind（如
`ToolStepKindProgress`，pico 渠道已有 `progress_note` kind 先例），feishu 侧渲染为
面板尾部一条可覆盖的灰色状态行（不进叙事轨、不进 Tools 时间线）。

## 5. B4/B5（P3）：两个小缺陷

- **B4**：`truncateFeishuReasoning`（feishu_stream_card.go:1148）后缀
  `fmt.Sprintf("…（已截断，共 %d 字）", len(text))`——`len` 是字节，中文 3000 字节
  显示「共 3000 字」实际 1000 字（复现实测）。改 `utf8.RuneCountInString(text)`，
  一行修。
- **B5**：`feishuImageRefRe`（feishu_stream_card.go:34）`([^)]+)` 在 URL 含 `)`
  时截断在第一个括号，`![a](https://x/a_(b).png)` 被降级成坏链接
  `[a](https://x/a_(b)`。仅影响含括号 URL 的降级展示，真实 img_v2_/img_v3_ key
  不受影响。修复可改为贪婪匹配到行内最后一个 `)` 或对捕获串做括号配平。

## 6. 修复批次建议与不变量

**批次**（均未实施，待拍板）：

1. **批次 1（P0+P3 小修）**：B1-A+B、B4、B5 —— 全部是 feishu 通道内局部改动，
   不触碰管线语义。
2. **批次 2（止血）**：B2-1（TTL 拒绝时封旧卡 + Delete 的 CAS 化）。
3. **批次 3（结构项）**：B2-2（streamer 会话身份）+ B3（心跳 kind 化）——两者都
   涉及 `bus`/接口面，合并一批评估。

**修复不得违反的既有不变量**（来源：fork-overview §1、turn-llm-failure-resilience
§3.1/§7、long-task-execution §8.2）：

- 流式卡片失败**绝不失败 LLM 调用**（200850 降级路径的立身之本）——B1 的分段
  补发、B2 的 best-effort 封板都必须 best-effort 化，失败只记日志。
- 不恢复「中断卡刷屏」：补发的是正常内容消息，不是 error 通知；visible error 的
  双重抑制保留（前提由 B1 修复恢复成立）。
- 心跳 keep-alive 语义（lastAt + 面板活性）保留，只改展示。
- 空卡删除、`enforceFeishuElementLimit`、seq 单调等既有安全网不动。

## 7. 复现方法（排查会话已验证）

httptest 假服务端处理五类端点：`/open-apis/auth/v3/tenant_access_token/internal`
（回 `{"code":0,"tenant_access_token":"t","expire":7200}`）、`/open-apis/cardkit/v1/cards`
（create，回 card_id）、`/open-apis/im/v1/messages`（回 message_id）、
`/open-apis/cardkit/v1/cards/{id}`（PUT update）、`.../settings`（PUT）。
通道构造：`lark.NewClient("app","secret", lark.WithOpenBaseUrl(srv.URL),
lark.WithTokenCache(newTokenCache()))`。

- **B1**：streamer 的 `updateCard` 接真实 `ch.cardkitUpdateCard`（保留 30KB 本地
  检查），`closeStreaming` 用计数桩；`Finalize(ctx, strings.Repeat("字", 12000))`
  → 断言：返回 "exceeds Feishu 30KB limit" 错误、closeCalls==0、streamer done=true
  → 卡片永封不了。
- **B2b**：旧 streamer（计数桩）存入 `c.streams`，`lastAt` 置 -3min，调
  `ch.BeginStream` → 断言：返回新卡、旧桩 updateCardCalls==0（无人封它）。
- 基线验证：`go test ./pkg/channels/feishu/` 全绿（复现测试删除后）。

## 附录：涉及文件（修复时的改动面）

- `pkg/channels/feishu/feishu_stream.go` — B1（Finalize 答案预算+分段）、B2b/B2c
  （BeginStream 善后、Delete CAS）、B3（AppendToolStep 分流）
- `pkg/channels/feishu/feishu_stream_card.go` — B1（refresh 卡答案预算）、B4、B5
- `pkg/bus/`（ToolStep kind）+ `pkg/agent/progress_heartbeat.go` — B3
- `pkg/channels/interfaces.go`（可选能力接口）+ `pkg/channels/manager.go`
  （GetStreamer 传递身份）— B2-2
- 建议测试锚点（修复时）：B1
  `TestFinalizeOversizedAnswerSealsAndSplits`/`TestFinalizeFallbackSealOnSplitFailure`；
  B2 `TestBeginStreamSealsStaleStreamer`/`TestFinalizeDeleteIsCASScoped`/
  `TestConcurrentTurnsGetDistinctCards`；B3 `TestProgressBeatDoesNotPinNarration`；
  B4 `TestTruncateReasoningCountsRunes`；B5 `TestSanitizeImagesWithParenURL`。

## 9. 第二批（同日）：B6 跨通道媒体重试去重 + 流式卡片三项优化

### 9.1 B6（中危，跨通道）：SendMedia 部分成功后整批重试 → 媒体重复发送

- 契约：`manager.go` `sendMediaWithRetry` 对非 `ErrNotRunning/ErrSendFailed` 的失败一律重试（≤3 次）。
- 违反：各通道 SendMedia 逐 part 发送——任一 part（或尾随 caption）失败时返回 `ErrTemporary`，但此前的 part **已在聊天里** → 重试把已送达部分重复发 1-3 次。触发面恰是限流/网关抖动（manager 认为重试能救的那类）。
- 同型通道（逐行核实 feishu/telegram，模式核实其余）：feishu（含 caption 路径）、telegram（含 media group 分块）、deltachat、discord（超时路径：发送 goroutine 无 ctx，超时后仍可能送达）、line（文本兜底循环）、matrix、qq、slack（含 caption 兜底）、wecom、weixin。原子单发（无此问题）：onebot、pico。
- 修复（方案 A）：`pkg/channels/media_send.go` 统一助手 `MediaSendErr(sent, err)`——`sent>0` 降级为 `ErrSendFailed`（永久、不重试）并保留原错误链（双 %w）；sent=0 保留原临时分类可安全重试。discord 超时路径特殊处理：发送状态未知 → 直接报永久。
- 顺手修：feishu `mediaCaptions` 合并全部 part caption（原先只取第一个，其余静默丢弃）。
- 测试锚点：`TestMediaSendErr*`（3）、feishu `TestSendMediaPartialFailureIsPermanent`、`TestSendMediaFirstPartFailureStaysRetryable`、`TestSendMediaCaptionFailureAfterDeliveryIsPermanent`、`TestSendMediaJoinsAllCaptions`。

### 9.2 流式卡片三项优化（O1-O3）

- **O1**：`Update` 的 compose+sanitize 移到节流判定之后——原先每个 chunk（远快于 200ms 节流窗口）都对全长累积答案跑一次正则，纯浪费（每 turn O(n²)）；现在只在真正写遇时计算。
- **O2**：`sanitizeFeishuMarkdownImages` 无 `![` 快速路径直接返回原文。
- **O3**：`feishuAnswerFlushIntervalFor` 自适应节流——元素 API 每次写遇携带**全量**累积内容，长答案时每 200ms 一次 O(answer) 字节负载；>24KB → 500ms、>48KB → 1s（打字机自身的 print pacing 保显示平滑）；`pinNarrationLocked` 重置 answerSentAt 的写遇语义不变。
- 测试锚点：`TestAnswerFlushIntervalForAdaptsToSize`、`TestUpdateThrottledSkipsCompose`、`TestSanitizeFastPathEquivalence`。

### 9.3 已评估未实施（proposal-only）

- 面板刷新持锁期间 API 调用（≤10s）会阻塞并发 Update：改为锁外发送需引入 seq 到达序保证，风险大于收益，不动。
- Update 的 streamContent seq 与面板刷新 seq 共用计数器但 API 在锁外发出，理论上有乱序到达被拒的小窗口（非致命、有日志），不动。
- 遗留项更新：pico/telegram `BeginStream` 每次新建 streamer（无 map 复用），**无 B2 同型丢答案风险**——上轮遗留项关闭。

## 8. 执行记录（2026-10-04，三批次全部完成）

批次划分与实际落地：

- **批次 1（B1-A+B、B4、B5）**：全部落地。
  - B1：`clampFeishuAnswerForCard`（24KB 预算 + rune 安全截断 + 尾注），Finalize 封卡带预算、
    余量经 `channels.SplitMessage`（4000 rune/段）由新 seam `deliverPart`
    （`FeishuChannel.deliverCardOrText`，卡片→纯文本降级，刻意绕开 tool-feedback 机制）
    best-effort 补发；封卡失败时降级重试 `buildFeishuMinimalFinalCard`（无面板 + 12KB 硬预算，
    必定 ≤ 30KB）；Cancel 同样带预算但不补发余量（中断语义，尾部在会话历史中可续）；
    refresh 卡答案快照同样 clamp（仅显示层，打字机元素仍收全文；200850 降级模式下尾部
    缺口由封卡补发兑付）。closeStreaming 的 seq 改为封卡时新取，避免重试后序列回退。
  - B4：`utf8.RuneCountInString`。B5：正则改 `((?:[^()\s]|\([^()\s]*\))+)`
    （一层括号配平 + 禁空白，同行多图不融合；畸形嵌套原文保留不降级）。
  - 构造重构：`buildFeishuFinalCard` → `buildFeishuFinalCardComposed`（预组合内容）+
    `feishuVerdictElement` 提取；旧签名保留供既有测试。
- **批次 2（B2-1 止血）**：落地。
  TTL 拒绝复用且旧卡未封时，BeginStream 异步（detached ctx）
    `CancelWithReason("superseded")` 封旧卡（新原因码文案「已被新任务取代」；空卡走既有
    删除路径）；Finalize/Cancel 的 map 删除改为 `streams.CompareAndDelete(chatID, s)`
    （CAS，只删自己的条目）。异步封旧与新建卡的竞争由 per-streamer seq 与 CAS 保证无害。
- **批次 3（B2-2 + B3）**：落地。
  - B2-2：新增 `channels.SessionScopedBeginStreamer` 可选能力接口，
    `Manager.GetStreamer` 首跳与 `splitMarkerStreamer.begin` 闭包均改走
    `BeginStreamForSession(ctx, chatID, sessionKey)`（不支持则回退旧 BeginStream）；
    feishu streamer 增加 `sessionKey` 字段，复用矩阵按 §3.4 落地（同会话新鲜→复用；
    异会话新鲜→新卡不动旧卡；陈旧（≥TTL，任意会话）→新卡 + 异步封旧；已封→新卡）。
    pico/telegram 未实现新接口，行为不变（是否同病待后续评估，见遗留）。
  - B3：`bus.ToolStepKindProgress`（"progress"）；心跳改发 Progress 步；
    publisher 无名门禁放行 Progress；feishu 侧 Progress 渲染为面板尾部一条可覆盖灰色状态行
    （`feishuStreamState.ProgressNote`，不进叙事轨/不进 Tools 时间线/不计入标题统计，
    计入 hasPanelContent 以免空卡误删）；lastAt 刷新 + 面板刷新保留（200850 keep-alive 不变）。

测试锚点（全部新增且通过）：B1 `TestFinalizeOversizedAnswerSealsAndSplits`、
`TestFinalizeFallsBackToMinimalSealWhenCardRejected`、`TestCancelOversizedAnswerClampsInCard`、
`TestRefreshAnswerSnapshotClamped`；B2 `TestBeginStreamSealsStaleStreamer`、
`TestFinalizeDeleteIsCASScoped`、`TestCancelDeleteIsCASScoped`、
`TestConcurrentTurnsGetDistinctCards`、`TestGetStreamerPassesSessionScope`、
`TestGetStreamerFallsBackToUnscopedBegin`；B3 `TestProgressBeatDoesNotPinNarration`；
B4 `TestTruncateReasoningCountsRunes`；B5 `TestSanitizeImagesWithParenURL`。
测试基建：httptest 假 Feishu 服务端（`newFakeFeishuServer`，`lark.WithOpenBaseUrl`）沉淀为
常驻测试工具。

验证：`pkg/bus`、`pkg/channels`、`pkg/channels/feishu`、`pkg/agent`（全量 87s）全绿；
`pkg/tools`、`pkg/pid`、`pkg/migrate/...`、`pkg/audio/asr`、`deltachat`、`matrix` 在本机
（Windows）基线即有的环境性失败，已用 stash 前后对照确认与本次改动无关。

遗留（非本次范围）：

1. pico/telegram 的 BeginStream 是否存在同型 B2 缺口（pico 同 chatID 复用消息语义）——
   需单独排查；若同病，接入 SessionScopedBeginStreamer 即可。
2. B2-2 的「异会话新鲜卡不动」依赖 TTL 兜底陈旧卡；若某会话的 streamer 永远新鲜但
   owner 已死（理论上心跳会停→变陈旧），仍由 TTL+superseded 封板兑底，无需额外机制。
