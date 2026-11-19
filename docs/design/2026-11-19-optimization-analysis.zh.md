# 全量功能分析与前后端优化点报告（2026-11-19）

> **进度登记（同日）**：第一批 P0 已全部落地——B6（dc5accd1）、B5（c8b468e4）、B3（d26172a9）、B1（6908c897）、B2（279bd63b），另有 binding 测试 helper 的 -race 修复（94735f59）。**第二批已全部落地**——退出监视器 race（6b26c177）、B7 status 降本（913c731a）、F6 轮询后台暂停（47f531dd）、B4 飞书串行发送队列（627dc937）、B12 dispatcher 分级（07473c63）、F1 memo 链（06c6dceb）、F2 store 熔断（1c37a862）、F3 尾部窗口（9847f3cb）。gateway 退出监视器的 ProcessState 竞争已随 6b26c177 修复。实施中新增发现：① B2 落地后 web 会话列表的 jsonl fallback 分支需补 busy 探测（已随 279bd63b 修复）；② pkg/lsp 两例、pkg/skills 一例 Windows 路径大小写用例在本 Linux 环境基线即失败，与批次无关。**批次三已全部落地（同日晚）**——B8 分页下推（9c97ae1f）、B9 历史计数走 meta（68b27929）、B10 schema 估算去重（4a64b163）、B11 al.cfg 竞争收口（afe1dfd2，turn 内 p.Cfg/turn 外 GetConfig；-race 对照基线未恶化，剩余 DATA RACE 均为既有测试 helper 无锁读写）、B14 单 Timer（e7f10040）、B15 Skills TTL 缓存（3222ef53，插件启停需失效——TestPluginsEndToEnd 抓出）、B16 状态轮询不复活（5b8c4938）、F5 入口包 1.24MB→644KB（e9d9e1a1）、F7 轮询单 owner（1e489fd3）、F8 消息 LRU+SWR（7c0f0b58），另修 restart recovery 测试竞态（04a6f354，裸 text() 断言）。B13（需改上游钉死语义）与 B17（环内替换破坏 replay 契约）经确认跳过。剩余：小项（B18-B29、F9-F13）、单独立项 B30。
>
> 方法：4 路并行子代理逐文件分析（agent 核心 / providers+channels / web 后端 / web 前端），主评审对全部 P0 级结论读码抽验（下文标 ✓ 的条目已亲自核实代码；其余为子代理报告，动手前先复核）。只列真实可验证项，不列泛泛而谈。

## 一、软件功能全景

本仓库（fork 名 Limulus/鲎）是**个人部署的 AI agent 网关**，一个二进制跑通「模型 ↔ 工具 ↔ 消息渠道」全链路：

| 层 | 包 | 职责 |
|---|---|---|
| Agent 核心 | `pkg/agent` | turn 管线（Setup→Assemble→LLM→ExecuteTools→Finalize）、seahorse 树上下文引擎、会话级模型覆盖、审批/问询 HITL、压缩调度、看门狗、restart recovery |
| Provider 层 | `pkg/providers` | openai_compat / anthropic 等，fallback 候选链 + 冷却 + 错误分类 + Retry-After + MiniMax 泄漏清洗 |
| 渠道层 | `pkg/channels` | 飞书 CardKit 流式卡（过程面板/打字机/审批交互卡）、pico WebSocket（web/TUI 共用，seq 补发+入站幂等）、telegram/discord/matrix 等 10+ 通道 |
| 工具面 | `pkg/tools` | exec（deny 规则/清理管线/输出预算）、文件工具（CRLF/GBK/系统目录保护）、LSP、MCP、cron、glob/grep、快照/撤销 |
| 会话存储 | `pkg/memory` + `pkg/session` | JSONL 追加式存储 + scope 路由/别名解析 |
| Launcher 后端 | `web/backend` | Go API：网关生命周期、配置、会话、模型、渠道绑定、插件/技能管理、静态资源服务 |
| Launcher 前端 | `web/frontend` | React 19 + TanStack Router + jotai + Tailwind v4：聊天页（chat-pi 时间线）、管理页、五套主题、i18n×5 |
| 其他 | `pkg/picoclient` + `cmd/picoclaw/internal/tui` | 终端 TUI 客户端；`pkg/cron`/`pkg/evolution` 定时任务与自动化建议 |

## 二、后端优化点

### P0（正确性/热路径，建议第一批做）

**B1. 会话键解析每操作全目录扫描** ✓
- 位置：`pkg/memory/jsonl.go:432-490`（`ResolveSessionKey`）、`:492-498`（短路条件）
- 问题：含 `:` 的会话键（所有渠道会话 `agent:main:telegram:123` 这类）被 `shouldShortCircuitSessionResolve` 判为不短路，即使直接会话文件存在也走 `os.ReadDir` + 逐个读取反序列化**全部** `.meta.json`。
- 影响：每次 AddMessage/GetHistory/Save 都触发；40 次工具迭代的 turn ≈ 80+ 条消息 × O(会话总数) 文件读取，且发生在 runTurn 持锁期间。会话上百后是最大 I/O 热点。
- 建议：带 `:` 的键先查直接文件存在即短路；或加 (key→canonical) 进程内缓存（含负缓存）。

**B2. JSONL 每条消息 = 1 次 fsync + meta 原子重写** ✓
- 位置：`pkg/memory/jsonl.go:752-788`（`addMsg`：append+Sync，随后 readMeta+writeMeta）；`writeMeta`（`:157-166`）用 `MarshalIndent` + WriteFileAtomic（temp+fsync+rename）。
- 影响：40 迭代 turn ≈ 120+ 次 fsync 串行在 turn 关键路径上（工具结果落盘是同步调用）。
- 建议：meta 的 Count/UpdatedAt 内存累积、turn 尾或 `Save()` 一次落盘；追加路径改周期性 fsync（按 turn 边界）；`MarshalIndent` 换 `Marshal`。取舍：弱化单条崩溃持久性——restart recovery 已有悬空 tool_calls 封口兜底，可接受。

**B3. SSE 流式热路径 O(N²)** ✓
- 位置（多层叠加）：
  - `pkg/providers/openai_compat/provider.go:804-836`：每 delta 把**全量累积文本** `.String()` 发给 onChunk，每 chunk 对全文跑 `sanitizeMinimaxToolLeak`（`minimax_clean.go:47` 全文 Contains）✓
  - `pkg/providers/openai_compat/provider.go:629`：每 chunk 对全量 `TrimSpace`
  - `pkg/agent/pipeline_streaming.go:136-141,445-452`：再两次全量 TrimSpace + 每 chunk `stripToolCallParrot`（出现标记后全文 Split）
  - `pkg/channels/pico/pico.go:1114,1134`：每 chunk `len([]rune(content))`（O(N) 分配，且在节流判断之前）
- 影响：答案越长 chunk 越密，CPU/GC 平方级增长；200KB 答案 × 数百 chunk 是可观的纯浪费。
- 建议：chunk 级改增量（TrimSpace 换「见过非空白」布尔、rune 计数换 `utf8.RuneCountInString` 零分配）；minimax/parrot 清洗下沉到渠道 flush 边界做一次——feishu 已是这个模式（`feishu_stream.go:636-638` 注释），推广到 pipeline 层。

**B4. 飞书 streamer 持锁做 CardKit 网络调用** ✓
- 位置：`pkg/channels/feishu/feishu_stream.go:815-820`（注释自认 "API call is made while holding the lock"）→ `refreshPanelLocked` → `updateCard`，超时 10s。UpdateReasoning/AppendToolStep 全部持锁调用。
- 影响：publisher 在 provider onChunk 内同步调用（`pipeline_streaming.go:462-467`），飞书 API 一慢，LLM SSE 读循环被卡最长 10s/次，打字机流被面板刷新串行阻塞。
- 建议：锁内只做状态快照+序号分配，API 调用移到锁外（每 streamer 单飞 goroutine 串行化保序）。

**B5. pico WebSocket 写无 deadline，僵死客户端可挂住整个会话广播**
- 位置：`pkg/channels/pico/pico.go:106-113`（writeJSON 前 `SetWriteDeadline` 缺失）、`120-128`（deliver 持 deliveryMu 跨 writeJSON）、`1453-1482`（逐连接同步广播）。
- 影响：对端 TCP 零窗口/半死连接时 WriteJSON 无限阻塞 → 该会话所有 Send/流式/ack 全挂，pending 无界增长。
- 建议：每写前 `SetWriteDeadline(now+10s)`，失败即关连接；pending 设上限。

**B6. web 后端 ABBA 死锁链（configWriteMu ↔ gateway.mu）** ✓
- 位置：正向 `web/backend/api/gateway.go:1295`（handleGatewayStart 持 gateway.mu）→ `startGatewayLocked` → `EnsurePicoChannel`（`pico.go:269` 取 configWriteMu）✓；反向 `web/backend/api/config.go:322`（handleResetConfig 持 configWriteMu）→ `gatewayStatusData()`（`gateway.go:1511` 取 gateway.mu）✓。
- 影响：POST /api/config/reset 与 gateway start/restart/watchdog 并发时可能永久死锁，全部 gateway/代理请求挂死（需要并发窗口才触发，但 30s watchdog 使窗口长期存在）。
- 建议：统一锁序——reset 的状态读取移到释放 configWriteMu 之后；EnsurePicoChannel 的配置写挪到 gateway.mu 临界区外。附带：`gatewayStartReady` 里的本地模型网络探测（拨号 8s）不应在持 gateway.mu 时执行，否则阻塞 /pico/ws、/pico/media 代理。

**B7. GET /api/gateway/status：每 2s 一次 fork + 2 次全量 config 加载 + 反射签名** ✓
- 位置：`web/backend/api/gateway.go:1493-1570`——`:1502` config.LoadConfig、`:1511`→`sanitizeGatewayPidData`→`gateway.go:213` `exec "ps"`（每轮询 fork 一个进程）、`:1549` computeConfigSignature 全配置反射遍历+JSON marshal、`:1559` 再次 LoadConfig。前端 `store/gateway.ts:29-30` 2000ms/1000ms 轮询 ✓。
- 影响：dashboard 开着时每秒 fork/exec + 多次 config.json 解析；config 越大越糟。
- 建议：pid 存活检查改 `syscall.Kill(pid,0)` + 短 TTL 缓存（ps 签名匹配只在 attach 时做一次）；config 用 mtime 驱动缓存；签名只在 config 写入后失效重算。

**B8. GET /api/sessions 全量解析所有会话 JSONL**
- 位置：`web/backend/api/session.go:1013-1021`——每会话 `readJSONLSession` 完整解析（行上限 10MB），只为截 60 rune 预览；分页在全部构建之后（`:1044-1067`）；另加 `sessionRuntimeSettings` 的 config.LoadConfig + 3 次 ReadDir。
- 影响：会话列表接口 O(全部转录字节) 的磁盘 IO+JSON 解析；UI 每次开侧栏全量付出。
- 建议：列表只需 meta + 首条 user 行（流式读即停）；分页下推到文件枚举阶段。同型问题：`session.go:410-428` 单会话查找先全目录扫 meta——改 stat 直查 + fallback。

### P1（重复计算/并发卫生，第二批）

**B9. GetHistory 全量重读每 turn 3-5 次**
- `pkg/memory/jsonl.go:791-811` 无缓存整文件 scan+unmarshal。调用点：`turn_state.go:364`（只为取 `len()` 做 initialHistoryLength）、`context_usage.go:68`（finalize 每回合）、`agent.go:852`、`idle_compact.go:241,246`（同一 tick 连续两次全量解析）。
- 建议：initialHistoryLength 读 `meta.Count`；usage 复用 SetupTurn 已 Assemble 的历史或按 (size,mtime) 缓存；idle scanner 合并两次读取。

**B10. 工具 schema token 估算每迭代重复 marshal 5-6 次**
- `pkg/tokenizer/estimator.go:220`；调用点 `pipeline_llm.go:122,127,224,226`、`context_usage.go:213`、`iteration_compact.go:57`（无视已缓存的 `exec.toolDefTokens`）、`context_budget.go:152`。MCP 工具 schema 可达几十 KB/个。
- 建议：按工具名+registry version 缓存 marshal 结果；`iteration_compact.go:57` 改用 `exec.toolDefTokens`。

**B11. `al.cfg` 裸读与 reload 写交换构成数据竞争**
- `pkg/agent/agent.go:545` 持 `al.mu` 写 `al.cfg`，但包内 42 处裸读（`pipeline_llm.go:84-85,197,307`、`turn_coord.go:168,394,405`、`pipeline_execute_loop.go:269-278,501,637-638` 等）运行在另一把锁下。
- 建议：统一走 `p.Cfg`（`pipeline.go:31` 已在锁内取好）或 `al.GetConfig()`。

**B12. 渠道出站单 dispatcher 阻塞式扇出**
- `pkg/channels/manager.go:1755-1771` 对满队列阻塞投递；共享 bus outbound（`pkg/bus/bus.go:235-275`，容量 64，timeout=0）。与 B4 叠加：feishu 慢 → 队列满 → dispatcher 阻塞 → 所有渠道/回合 PublishOutbound 阻塞，网关级队头阻塞。
- 建议：非阻塞投递 + 有界丢弃/遥测（仿 audio 150ms drop），或每渠道独立 dispatch goroutine。

**B13. HTTP Transport 未调优**
- `pkg/providers/common/common.go:54-77`：proxy 为空时直接用 `http.DefaultTransport`（MaxIdleConnsPerHost 默认 2）。多会话并发打同一 LLM host 时重复 TCP+TLS 握手。
- 建议：进程级共享 Clone Transport，`MaxIdleConnsPerHost` 32-100 + IdleConnTimeout。

**B14. 流式空闲超时包装器每 Read 分配 timer + 竞态**
- `pkg/providers/openai_compat/provider.go:700-717`：每次 Read `make(chan)`+`time.AfterFunc`；且 timer 恰在读返回时触发会把有效数据当超时丢弃。
- 建议：复用单个 `time.Timer`（Read 前 Reset 读后 Stop）。

**B15. Skills API 每请求全量重扫 5 根目录 + markdown AST 解析**
- `web/backend/api/skills.go:137-170`（handleGetSkill 先建全量目录）、`pkg/skills/loader.go:196-322`（深度 8 递归 + 每 SKILL.md 完整解析 + 每请求 LoadPluginsDir）。
- 建议：loader 加 mtime 失效缓存；单 skill 查询走已有 `LoadSkill` flat 快路径（`loader.go:324-335`，未被 API 使用）。

**B16. GET 状态轮询会"复活"gateway 状态**
- `gateway.go:1511-1524`：pid 文件存在即无条件 `setGatewayRuntimeStatusLocked("running")` + attach；优雅退出期间 pid 文件到进程退出才删（`pkg/gateway/gateway.go:187`），userStopped 在 status 路径未检查。停止/重启期间的 2s 轮询把状态翻回 running、覆盖 pidData。
- 建议：status 路径遵循 userStopped/restarting 标志，GET 中不做 attach。

**B17. seq 环缓冲合并几乎永不命中**
- `pkg/channels/pico/seq.go:79-92`：只对环尾连续同 message_id 帧合并；流式时 answer 编辑与 reasoning 更新交错（`pico.go:1165-1218`），相邻帧 message_id 不同 → 512 帧几乎全保留全量累积 payload，单活跃会话可累积数百 MB。
- 建议：按 message_id 索引环内帧做原地替换合并，或对内容帧设保留字节上限。

### P2（可排第三批或顺手修）

**B18.** `/ready` 检查注册时算一次快照永久陈旧（`pkg/health/server.go:104-115`）——readyHandler 内现算或 ticker 刷新。
**B19.** 前端静态资源无 Cache-Control/ETag/压缩（`web/backend/embed.go:39-78`，embed FS 无 modtime）——hash 资产 `immutable` + index.html no-cache + 预压缩。
**B20.** 渠道启动串行且全程持 Manager.mu（`pkg/channels/manager.go:1299-1340`）——一个慢 channel 拖整体就绪；改 errgroup 并发 Start（MCP manager 已是此做法）。
**B21.** 全后端 45 处 handler 直接 `config.LoadConfig` 磁盘读 + `DefaultConfig()` 每次全量构建——Handler 内加写失效缓存；DefaultConfig 包级单例。
**B22.** 登录限流器 `byIP` map 只增不删（`web/backend/api/auth_login_limiter.go:21,39-43`）——kept 空时 `delete`。
**B23.** seahorse Assemble N+1 查询 + Ingest 无事务（`pkg/seahorse/short_assembler.go:38-55`、`short_engine.go:246-289`）——批量取消息+parts、GetOrCreateConversation 按 sessionKey 缓存、单事务。
**B24.** `resolveMediaRefs` 每迭代对全消息表重复读盘+base64 编码图片（`agent_media.go:109-144`，`pipeline_llm.go:70-72` 每迭代重跑）——按 ref 缓存解析结果。
**B25.** `trimHistoryToFitContextWindow` 每个 binary-search probe 全量重建 prompt（`context_budget.go:162-172`）——先建 token 前缀和数组，只对最终切点重建一次。
**B26.** pico `lastUsage` map 无淘汰（`pico.go:223-224,302-316`，同文件 seqStore 有 256 上限而它没有）——仿 seqStore 加 LRU。
**B27.** feishu 流卡 sync.Map 对被废弃会话泄漏（`feishu_stream.go:305`，删除只在 Finalize/Cancel/同 chat 复用）——全局 TTL janitor（manager 已有先例）。
**B28.** 每次 LLM 调用急切格式化全量日志参数（`pipeline_llm.go:251-256`，Debug 关闭也照付）——加级别判断。
**B29.** `waitForActiveRequests` 每次调用泄漏一个等 ctx.Done 的 goroutine（`agent_utils.go:656-661`）——drain 成功即返回前取消内部 ctx。

### 架构债（单列，属取舍而非 bug）

**B30. runTurn 全程持有 modelMu 读锁与 Close 阻塞写锁的互锁面**
- `pkg/agent/turn_coord.go:17-22` vs `instance.go:940-943`。Go RWMutex 写者优先：Close 等写锁时新 turn 的 RLock 全部阻塞。`/switch`、`ResetModel` 已被迫 TryLock（AGENTS.md 锁纪律即源于此）。
- 现状是**文档化的有意取舍**（AGENTS.md/fork-overview §24/§28 反复强调锁纪律），但「turn 内禁止嵌套 RLock」的脆弱纪律已被新代码踩过一次（§28 第一版挂死 600s）。
- 若要做：可变状态其实只有少数几个字段（Model/Candidates/ContextWindow/providers），可换 `atomic.Pointer[modelSnapshot]`，turn 开始时加载一次快照，全程无锁——Close 与 /switch 即刻完成，嵌套锁纪律整体消失。属大改，建议单独立项评审，不与上述批次混做。

## 三、前端优化点

### P0（流式渲染热路径）

**F1. 每个流式 token 重渲整个消息列表** ✓
- 证据链：`src/features/chat/protocol.ts:305-347` 每条 message.update 生成新 messages 数组 → `src/chat-pi/turns.ts:224-427` buildTurns 每次从头重建所有 PiTurn/PiBlock（无结构共享）✓ → `pi-message-list.tsx:20-22` TurnItem memo 按 turn 引用比较，turn 恒为新对象 → memo 永不命中 ✓；`pi-message.tsx:246-259` PiToolView 每渲染新建 → ToolCallBlock memo 同样失效；ThinkingBlock 无 memo；`pi-message.tsx:91-93` AssistantCard 每次 flatMap().join() 重建 actionText。
- 影响：长会话每 token O(N) 组件重渲，token 频率越高越卡。
- 建议：buildTurns 结构共享（按 user message id 缓存已完成 turn，只有末 turn 重建）；或 TurnItem 自定义比较器；PiToolView 字段拆平。

**F2. ChatPage 订阅全量 chatAtom 且无相等性熔断**
- `src/hooks/use-pico-chat.ts:11-20` 全量取 state；`src/store/chat.ts:194-209` 恒返回新对象（对比 `store/gateway.ts:52-68` 有熔断，chat 没有）。任何字段变化（lastTurnActivityAt、connectionState…）令 ChatPage 整树重渲；`chat-page.tsx:190` 每次渲染 `[...messages].reverse().find()` 整数组拷贝。
- 建议：拆原子或 selectAtom；updateChatStore 加浅比较熔断。

**F3. 消息列表零虚拟化**
- `pi-message-list.tsx:33-39` 全量 map；`@tanstack/react-virtual` 已在依赖里（只用于文件列表）。
- 建议：turn 列表 useVirtualizer（动态高度），或先做窗口裁剪。

**F4. 滚动处理器每帧 3 次 setState**
- `chat-page.tsx:228-237` syncScrollState 无 rAF 节流；流式跟滚期间每帧全页重渲，与 F2 叠加。

### P1（包体积/网络）

**F5. 入口 chunk 1.89MB（gzip 555KB）**
- 实测 dist；构成：5 个语言包共 378KB 全静态打进 bundle（`i18n/index.ts:13-47`，运行时只用 1 种）；`chat-pi/markdown.tsx:35-40` `rehypeHighlight({detect:true})` 用 lowlight common（~40 语言）；`main.tsx:16` 启动即 initA2uiBasicCatalog（A2UI 只在收到 render_ui 时才需要）。
- 建议：i18n 按需动态 import；只注册 message-code-block.tsx 已声明的 10 语言并关 detect；A2UI catalog 延迟到首块挂载。

**F6. Gateway 状态 2s HTTP 轮询全页面常驻**（与 B7 成对）
- `store/gateway.ts:29-30` + `hooks/use-gateway.ts:20-22` 挂在 `__root.tsx:93` AppLayout → 所有页面 30 req/min。
- 建议：状态随已有 chat WS 下发；仅 disconnected 瞬态轮询；后台标签页暂停（sidebar 已有 document.hidden 先例）。

**F7. 会话列表轮询双实例 + 每 tick 全量重排**
- `session-history-sidebar.tsx:74-92` setInterval；ChatPage 渲染两份侧栏（`chat-page.tsx:434` 桌面 + `:449` Sheet），Sheet 打开时两个 interval；`use-session-history.ts:69-85` 每 tick fetch+Map 重建+全量 sort。
- 建议：轮询单一 owner；忙态从 WS 事件推导。

**F8. 会话历史无缓存，react-query 闲置**
- `features/chat/controller.ts:461-464` 每次切会话全量重拉；QueryClient 只在 8 个页面用，sessions API 绕开。
- 建议：迁 react-query（key=sessionId，staleTime>0），二次切换秒开。

### P2（交互/代码质量）

**F9.** 每字符输入重渲 ChatPage 整树（input state 在页面组件 `chat-page.tsx:130`，ChatComposer 未 memo）——input 下沉 + memo。
**F10.** 每张审批卡独立 1s timer（`approval-card.tsx:98-102`，N 卡 = N timer + 每秒 N 次重渲）——共享单一 tick。
**F11.** 双 CodeBlock 组件 + formatDuration 三处重复（`message-code-block.tsx` 297 行唯一消费方是 mqtt-form；聊天用 `markdown.tsx:118-159` 另写一套）——抽共享组件。
**F12.** `updateChatStore` 在 store setter 内同步写 localStorage（`store/chat.ts:199-208`）——改订阅副作用。
**F13.** `useTurnStall` 全量订阅 + 每渲染 O(n) 扫 messages（`use-turn-stall.ts:23-46`）——pendingUserGate 计数字段 O(1) 读。

### 已核实无需处理

主题切换无闪烁（首屏内联脚本）；路由级 code splitting 已启用（vite autoCodeSplitting）；xlsx/pdfjs/docx-preview/react-vega 均已动态 import；WS 重连有指数退避且防僵尸 socket。后端 MCP manager 质量良好（并行连接/RWMutex/单连接去重）；seahorse PRAGMA 设置合理；feishu 错误处理路径扎实。

## 四、建议落地批次

| 批次 | 内容 | 理由 |
|---|---|---|
| 第一批 | B6（死锁）、B5（WS deadline）、B3（流式 O(N²)）、B1+B2（会话 I/O） | 正确性 + 最大热路径收益；均为局部改动，互不依赖 |
| 第二批 | B4+B12（慢渠道连锁）、B7+F6（状态轮询成对改）、F1-F3（前端渲染） | 跨层联动项一起做避免半改；B7 与 F6 必须同批否则前端行为漂移 |
| 第三批 | B8、B9-B17、F5、F7-F8 | 重复计算与网络卫生，收益平稳 |
| 顺手修 | B18-B29、F9-F13 | 逐个独立小改 |
| 单独立项 | B30（锁快照化） | 架构级重构，需独立设计评审，动 AGENTS.md 锁纪律契约 |

> 风险注记：B2 改动弱化单条崩溃持久性（fork-overview §12 的 restart recovery 与悬空封口已兜底，可接受，但需在提交信息注明取舍）；B3 的清洗下沉需保 MiniMax 清洗语义（fork-overview §5，证据触发而非模型名门控）与 parrot 拦截语义（§21）不变，锚点测试必须全过。
