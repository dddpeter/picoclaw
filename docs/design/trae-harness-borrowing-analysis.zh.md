# Trae harness 借鉴分析

> 调研对象：Trae CN 1.0.13339 的 `resources/app/modules/ai-agent/harness.dll`（248 MB Rust cdylib，内部 crate `harness_server`，北京引力弹弓科技 EV 签名）——**纯静态逆向**（PE 结构 / 984 导出 / 导入表 / 118 万条字符串分类 / 定点反汇编），分析报告见 `WorkBuddy reverse-harness/REPORT.md`。
> 参照面：宿主 `out/main.js` 的加载决策链、`DesktopSseEnvelope` 线协议、`custom-model-proxy` 隧道、panic location 还原的内部 crate 地图。
> 本文按"picoclaw 是否已有对应物 + 借鉴成本"筛选，与 hermes/mimo-code/agentscope-go/octop 四篇同一方法；缺口核实先于推荐，防止重复造已有能力。
> 调研日期：2026-11-07。**边界**：专有商业软件，不链接、不分发、不提取密钥/凭据/授权逻辑；只借鉴观察到的架构模式、线协议字段与行为契约，Go 独立实现。

## 背景：一台 248 MB 的 DLL 教科书

Trae 的 AI Agent 服务端整体在 `harness_server.dll`：V8 130.0.7 整包静态链接（`.text` 单节 178 MB、约 66 万函数展开记录）、libgit2（940/984 导出是符号泄漏）、sqlx/sea-orm、aws-lc/rustls、AWS Bedrock SDK。宿主 `main.js` 按 `ai_assistant.request.enable_harness` → 动态配置 `iCubeApp.aiChat.enableHarness` → 内部账号默认的优先级判灰度，按平台三选一：本地进程内 `harness.dll` / 远程 SSH `harness-server.exe` / 回退旧实现 `ai_agent.dll`（282 MB）——新旧双实现共存、整体可回退。

内部 crate 地图（panic location 统计）：`server` / `storage-db`(SQLite+FTS5) / `hub` / `core` / `tools` / `skills` / `custom-model-proxy` / `permission` / `event-cache` / `workflow`。通信三层：aha-ipc/aha-rpc（私有 fork）+ jsonrpsee 走 Windows 命名管道与 40005 端口；`DesktopSseEnvelope`（`__event_id__`/`__packet_seq__` + SSE）走 desktop IPC；云端 WebSocket `x-bridge-transport` + HTTP 回退。

**picoclaw 侧结论**：FTS5 检索、MCP、HITL 审批、ask_user、技能仓库、Provider 多协议 + fallback/cooldown、上下文六段计量 + `/compact`、`tool_search` 渐进加载、`turn.status` 重连信号、stall watchdog——Trae 能观察到的能力绝大部分已对齐或更强。真正的缺口集中在**传输层可恢复性**与**密钥隔离**，本文只展开这三节。

## 缺口核实总表

| 能力 | Trae harness（证据） | picoclaw 现状（证据） | 结论 |
|---|---|---|---|
| 事件序号 + 断线补发 | `DesktopSseEnvelope` 字段 `__event_id__`/`__packet_seq__`；`/wsmessages/sse/poll?frontier_id=` 补拉 | `PicoMessage` 只有可空 `ID` + `Timestamp`，无单调序号；`turn.status` 只给 `{active:bool}`，刷新后知道 turn 在跑但错过的 delta 拿不回来 | **借（§一）** |
| LLM 请求隧道 + 密钥宿主隔离 | `custom-model-proxy`：tunnel + JSON-RPC 2.0，`SseOpen/Delta/End/Error` 四段分帧，配置 `encrypted_model_params` 落库加密 | 凭据由网关主进程加载并拼出网请求；`SecureStore` 只在内存存 passphrase；无独立转发进程 | **借（§二，与 sidecar 方向一致）** |
| 工具输出按 offset 分页 | `output_offset` / `max_output_bytes` / `output_priority` / `tool_call_id` | `output_budget.go` 保头保尾截断 + 全文落盘 + 模型 `read_file` 回读；无分页游标 | **借（§三，中）** |
| 技能包按环境/模型变体分发 | TOS zip 命名 `trae_agent::<work_mode>::<env>::<skill>/<version>/package.zip`、`InstallTrialSkillsCmd` | `pkg/skills` 有 clawhub/github registry、installer、恶意标记；无变体分发 | 可选（低） |
| 启动装配自检统计 | `module_count/api_route_count/hook_count/tool_count`（`startup/run/helpers.rs:69`） | 只有 MCP `tool_count`；无全局装配统计 | 可选（小） |
| 双实现灰度 | `uce()` 三层优先级 + `mLe()` 三选一（新旧各 250 MB+） | 单实现 + `gateway.hot_reload` | 不借 |
| FTS5 会话检索 | `fts_session_title`/`fts_message_content`/`fts_backfill_status` | `pkg/seahorse`：`summaries_fts`/`messages_fts`（trigram）+ 能力探测降级 | 已对齐 |
| HITL 审批 / 结构化问询 | `RequestUserInputAsyncOutput`、`permission` crate | `approval_hook.go` + `ask_user.go` + marker 落盘 + 三档策略，更完整 | 已对齐 |
| turn 重连状态位 | SSE 信封序号 | `turn_status_test.go` + `turn.status` 首帧（octop 借鉴 §四已落地）；stall watchdog、进度心跳 | 已对齐（差补发 → §一） |
| MCP 工具协议 | `tools/call`、`resources/read`、`apply_patch` | `pkg/mcp` + Web `/mcp` 页 + doctor probing | 已对齐 |
| Provider 抽象 | `Provider`(13 字段)、`LlmIdentity`、`model_config_cache` | `provider_catalog.go`/`protocoltypes`/`fallback.go`/`cooldown.go`/bedrock/azure/oauth | 已对齐（字段可对照） |
| 上下文计量 / 渐进工具 / 手动压缩 | `context_window_tokens`、`toolcall_history_max_tokens` 等 | 七件套（CJK 估算、六段计量、`/compact`、`tool_search`、分段压缩） | 已对齐 |
| 单二进制体量 | V8 + libgit2 + AWS SDK + 图像栈全静态链接 → 248 MB / 66 万函数 | 单 Go 二进制，内存与启动是核心卖点 | **反面教材** |

## 一、事件序号与断线补发（优先级：高，纯增量、零迁移）【已落地 2026-11-07】

**来源**：`DesktopSseEnvelope`——每个出站帧带 `__packet_seq__`（单调递增，允许跳号）与 `__event_id__`；客户端重连后按 `frontier_id` 向 `/wsmessages/sse/poll` 补拉缺口。淘汰发生时明确告知而非静默。

**picoclaw 现状**：`turn.status` 首帧（octop 借鉴 §四）解决了"turn 还在跑吗"，但没解决"错过了什么"——页面刷新/网络抖动期间的流式 delta 永久丢失，只能等 turn 结束后靠历史回放。出站帧全部汇聚在 `broadcastToSession`（`pico.go:1043`），广播失败（无连接）的帧直接消失，连"客户端回来时补给他"的机会都没有。

**落地记录**（本次已实现）：

1. `PicoMessage` 加 `Seq uint64`（`json:"seq,omitempty"`——旧客户端忽略未知字段，旧服务端发给新客户端也不炸）。
2. 新增 `seq.go`：`sessionSeqBuffer`——per-session 独立互斥锁（**不碰 connsMu，更不碰模型状态锁**，AGENTS.md 的 turn 读锁纪律不受影响）；发号在 `broadcastToSession` 入口完成，**先入环形缓冲再写连接**，广播失败的帧留在缓冲里，恰好成为重连补发的素材；默认容量 512 帧（`DefaultSeqBufferCapacity`），环形覆盖淘汰，内存有硬上限。**同一 message_id 的连续 message.update 在环内合并为最新一帧**（编辑语义下中间快照对补发无用）——长流式会话的环内存从"随内容增长"压平为"每消息一份最新快照"。
3. 握手补发：`GET /pico/ws?session_id=X&last_seq=N`——`turn.status` 首帧之后、正常流之前，服务端补发 `(last_seq, now]` 的缓冲帧（单连接直写，不干扰其他连接）。**恰好一次、严格有序**：新连接自注册起进入 catch-up 模式（`picoConn.deliver`/`endCatchup`，`deliveryMu` 串行化），补发期间的并发广播先入该连接 pending 队列、补发完成后按序 flush（跳过已被补发覆盖的 seq）——杜绝"直播帧插队重放帧"的乱序与重复。
4. 淘汰边界显式化：若客户端 `last_seq+1` 早于缓冲最老帧，补发所有缓冲帧后追加一帧 `stream.catchup`（`{complete:false, dropped:n}`）——"中间有 n 帧已被淘汰，等当前 turn 结束后拉历史"，绝不静默。客户端领先（服务端重启过）时安全降级为不补。
5. `turn.status` 语义原样保留（叠加不替换）；补发帧原样重放（带原 seq 与 message_id），前端按 message_id 幂等渲染即可避免媒体/工具步骤重复。
6. Go 客户端 `pico_client` 通道：入站帧更新 `lastSeq`，重连拨号自动带 `?last_seq=N`。
7. web 前端（刷新重连的主要受益者）尚未发送 `last_seq`、未处理 `stream.catchup`——分阶段推进，Go `pico_client` 通道先行；前端接入后再消费 `dropped` 分支的"拉历史"提示。

**评审修复**（2026-11-08）：① 补发/直播并发窗口的重复+乱序 → catch-up 模式 + pending 队列（见 3）；② `TestCatchupReplaysOnlyMissedFrames` 依赖固定 sleep 的竞态假设 → 轮询连接数；③ 环内存随内容增长 → 同消息 update 合并（见 2）；另修 `protocol.go` gofmt（注释打断字段对齐组）并删死代码 `seqStore.reset`。

**测试锚点**（`seq_test.go`，全部落地）：`TestPicoSeqMonotonicUnderConcurrency`、`TestCatchupReplaysOnlyMissedFrames`（含"广播失败帧仍可补发"）、`TestCatchupOrderedExactlyOnceUnderLiveTraffic`、`TestCatchupBoundaryWhenBufferEvicted`、`TestTurnStatusCoexistsWithCatchup`、`TestSeqBufferIsolationBetweenSessions`、`TestSeqRingCoalescesConsecutiveUpdates`、`TestClientDialCarriesLastSeq`、`TestSeqlessFramesOmitSeq`。

## 二、LLM 请求隧道与密钥隔离（优先级：高，但要算清代价）

**来源**：`custom-model-proxy`——出网 LLM 请求收敛到本地 tunnel 进程：宿主侧持有凭据并做模型配置管理（敏感字段落库加密为 `encrypted_model_params`），代理侧只拿"模型引用 + prompt"做转发；响应以 JSON-RPC 2.0 分发为 SSE 四段帧 `SseOpen → SseDelta* → SseEnd/SseError`。密钥不进代理内存布局，也不出现在任何帧里。

**picoclaw 现状**：网关主进程自己加载凭据并拼装出网请求；部署链路的包装脚本负责替换配置占位符。这与"sidecar 保障 LLM API 密钥安全"是同一个问题的两种解法。

**嫁接方案**：
1. 新增 `pkg/llmtunnel/`（client/server/帧协议，Go 标准库即可：Windows 命名管道或 `127.0.0.1` 端口），帧格式直接对齐四段语义——`open`（模型引用、请求元数据）→ `delta`*（SSE 增量透传）→ `end`/`error`。
2. `pkg/providers` 增加 tunnel-backed provider；网关只传模型配置引用，sidecar 侧才 `SecureStore.SetString`、拼 Authorization。
3. `fallback.go`/`cooldown.go`/`error_classifier.go` 留在网关侧——只消费错误语义，不需要密钥。
4. **做成可选**：`providers.tunnel.enabled` 默认关。多一个进程与"单二进制、极低内存"的定位冲突，先跑基准（内存增量、首 token 延迟）再谈默认。
5. 硬约束：帧里绝不回传密钥；日志过 `MaskAPIKeys`；保留 `streamRoundTripper` 响应头超时语义；**禁止给 `picoclaw.service` 加 sandbox 指令**（AGENTS.md），隔离靠进程边界。

测试锚点建议：`TestTunnelNeverExposesKeyInFrames`、`TestTunnelSseFourPhaseFrames`、`TestTunnelPreservesStreamHeaderTimeout`、`TestTunnelDisabledKeepsLegacyPath`、`TestTunnelFrameRoundTripUnderCancel`。

## 三、工具输出分页游标（优先级：中，锦上添花）

**来源**：工具结果帧内 `output_offset` / `max_output_bytes` / `output_priority` / `tool_call_id`——模型可以按需续读大输出，而不用一次性吞下或依赖宿主截断策略。

**picoclaw 现状**：`output_budget.go` 保头保尾截断 + 完整原文落盘 + 模型自主 `read_file` 回读。语义等价且更灵活，代价是模型多做一次决策；收益有限，别当主线。

**嫁接方案**：
1. 截断结果带 `total_bytes` / `next_offset` / `tool_call_id`，让模型可以精确续读而不是重新 `read_file` 全文。
2. `read_file` 加 offset 参数（或新增 `read_output`），按 `tool_call_id` 定位落盘文件读指定区间。
3. **回读路径必须同样过 `MaskAPIKeys`**——2026-10-13 事故的教训：落盘全文文件先掩再截断，offset 新路径不能漏。

测试锚点建议：`TestOutputBudgetEmitsNextOffset`、`TestReadOutputAppliesMask`、`TestReadOutputOutOfRange`。

## 四、评估后不借鉴的部分

| 项 | 理由 |
|---|---|
| 链接/嵌入/分发 `harness.dll` | 专有商业软件（EV 签名、MSVC + V8 运行时依赖、Rust cdylib），与 MIT fork 许可冲突、Go/Rust ABI 不通 |
| 静态链接重依赖（V8/libgit2/AWS SDK/图像栈） | 248 MB、178 MB 代码段、66 万函数——与单二进制低内存定位正相反 |
| 984 导出的 cdylib 导出面 | 940 个是 libgit2 静态库符号泄漏，是事故不是设计 |
| 新旧双实现灰度（`ai_agent.dll` vs `harness.dll`） | 商业 IDE 的发布保险；个人 fork 用 `gateway.hot_reload` + 配置开关足够 |
| 会话存储换 SQLite 主库 | JSONL + FTS5（`pkg/session/jsonl_backend.go` + `pkg/seahorse`）已覆盖；迁移风险远大于对齐收益 |
| 照抄 Trae 字段拼写进现有协议 | `PicoMessage` 已上线，扩展字段一律 `omitempty` 向后兼容，不改已有帧语义 |
| `ai_agent_ipc_*` 导出 | 反汇编确认是只打日志的兼容空桩（`[harness-legacy-ipc] ai_agent_ipc_init called`），无传输语义可借鉴 |

## 落地顺序建议

1. **§一 事件序号 + 补发** —— 纯增量、零迁移、直接补上 refresh 丢流的老问题（**2026-11-07 已落地**，见上）。
2. **§二 LLM tunnel** —— 与 sidecar 方向一致；先做可选开关 + 基准测试，确认多进程代价可接受再谈默认。
3. **§三 工具输出分页** —— 有余力再做；落地时回读路径先过掩码。

每步都不得触碰 turn 全程模型读锁纪律与 deny patterns 硬拒语义；补发是 `turn.status` 语义的叠加而非替换，这两件事合起来才构成完整的重连体验。
