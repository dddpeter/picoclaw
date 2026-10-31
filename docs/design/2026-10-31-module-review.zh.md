# 全仓模块梳理评审（2026-10-31）

> 方法：8 个并行评审代理逐模块分析（agent / tools / providers+config / channels 大五 / channels 冷门 16 渠道 / web 前端 / web 后端+网关 / 中小包批次），全部只读。
> 复核：20 项 P0/P1 级断言由主评审**亲自读码验证**，其中 2 项修正细节（见 §3）。标注「✅已复核」的条目均有独立 grep/读码证据；未标注的为代理发现（附行号证据，采信前建议复核）。
> 范围：**冗余（A）/ 不必要的设计（B）/ 缺失功能（C）**三类，不含既有 bug 评审清单（见 2026-10-04 全仓 bug 评审、octop 评审等）。

## 0. 结论摘要

- 仓库整体接线质量高：24 个中小包无整包死包、事件总线 46 个 Kind 全有 emit 点、19 个斜杠命令与 76 条 API 路由基本对齐、前端 routeTree 16 条路由全活。
- 冗余集中在**被取代未删的旧实现**（约 2200+ 行可整删）与**跨渠道复制样板**（约 800 行可收敛）。
- 最有价值的新发现是**功能断链**：spawn_status 永远空、custom_allow_patterns 静默无效、evolution 草稿无用户入口、web 无法停止回合、会话不可重命名。
- 建议实施分四批，见 §4。

---

## 1. P0/P1 级发现（建议优先处理）

### 1.1 功能断链（C 类，最高价值）

| # | 发现 | 证据 | 复核 |
|---|---|---|---|
| C1 | **spawn_status 工具永远输出空**：唯一数据源 `SubagentManager.tasks` 只能由零调用的 `Spawn` 填充；真实 spawn 走 SubTurn 机制不登记任务。v0.2.3 发布说明宣传的功能已随 SubTurn 重构静默失效 | `pkg/tools/spawn_status.go:40`；`pkg/tools/subagent.go:136` Spawn 零调用 | ✅ |
| C2 | **`tools.exec.custom_allow_patterns` 静默无效**：编译进 `customAllowPatterns`，但唯一消费点被 `len(t.allowPatterns) > 0` 门禁，而 `allowPatterns` 只能由零调用的 `SetAllowPatterns` 置非空。用户配了放行规则不生效（且坏正则会让启动失败，双重误导） | `pkg/tools/shell.go:356-362, 1556-1589, 1842` | ✅ |
| C3 | **evolution 草稿审阅回路断裂**：draft 模式持续产出技能草稿到 `skill-drafts.json`，但全仓无任何用户入口（无命令无 API）；配套的 diff 预览（preview.go 154 行）也从未接线 | `pkg/evolution/paths.go:31`、`runtime.go:533`；preview.go 零调用 | 部分（organizer 死字段已复核） |
| C4 | **web 无法停止进行中回合**：前端无停止按钮。**修正**：不需要扩后端协议——`/stop` 作为普通消息发送即可（busy 分支拦截顺序 审批>stop>compact>ask>steering 已有），前端加按钮+确认即小改动 | `pkg/channels/pico/pico.go:1270` 仅 ping/send 帧；`pkg/commands/cmd_stop.go` | ✅ |
| C5 | **会话不可重命名（全栈缺口）**：后端有 user-source title 机制（/title 命令），但 web API 只有 list/get/delete；加 `PUT /api/sessions/{id}/title` + 前端入口即闭环 | `web/backend/api/session.go:24`（已 grep 无 PUT/PATCH） | ✅ |
| C6 | **subagent 参数三件套半接线**：schema 的 `Temperature` 在 tools→agent 转换层静默丢弃；`MaxContextRunes` 文档承诺软截断但拷贝后无人消费（utils 侧工具函数吃灰）；AGENT.md 的 `maxTurns` 解析后全仓零读取 | `pkg/agent/subturn.go:226-239`（已读，无 Temperature）；`definition.go:35,181,198` | ✅ |
| C7 | **`agent.llm.delta` 事件半接线**：payload 类型/日志 case/hook kinds 映射齐备，但全仓无 emit 点 | `pkg/events/kind.go:12`、`hook_mount.go:339`、`runtime_event_logger.go` | ✅ |
| C8 | **长连接渠道断线假活**：matrix sync 退出后状态仍 running 永不重连；deltachat rpc-server 崩溃后无监护；whatsapp(bridge) 读循环 2s 无限刷错误永不重建 | `pkg/channels/matrix/matrix.go:290-296`、`deltachat/rpc.go:102`、`whatsapp/whatsapp.go:166-172` | — |
| C9 | **doctor 未接入 mcp.ProbeServer**：为 launcher 预留的连通性探测 63 行零调用，doctor 是天然宿主 | `pkg/mcp/probe.go:13` | — |

### 1.2 韧性/数据安全（B 类高危）

| # | 发现 | 证据 | 复核 |
|---|---|---|---|
| B1 | **error_classifier 缺 504**：504 不入 transient 集合→按 unclassified 立即中止整条 fallback 链且不记冷却。同函数 message 分支缺「配额语义优先于 429」提升（HTTPError 分支有，message 分支没有——非 HTTPError 来源的 429+quota 错误仍走 1min 冷却撞死配额） | `pkg/providers/error_classifier.go:158-162, 393-401` | ✅（504 缺失已读） |
| B2 | **EvolutionConfig.MarshalJSON 丢 3 个生命周期字段**：`cold/archive/delete_after_days` 运行时读取但序列化遗漏——任何配置回写（web UI 保存/迁移备份）静默抹除用户值，重启回退默认 90/180/365 | `pkg/config/config.go:126-152`（已读，out struct 确无三字段） | ✅ |
| B3 | **anthropic_messages 错误通道绕过 common.HandleErrorResponse**：手写错误字符串丢失 Retry-After/中文重试 hint/无界 body 读取 | `pkg/providers/anthropic_messages/provider.go:136-153` | — |
| B4 | **updater 非 GitHub URL 静默回落上游**：`buildReleaseAPIURL` 对非 github.com 返回 ""，`findAssetInfo` 随即回落 `GetProdReleaseAPIURL()`（sipeed）。**修正**：github.com 托管的 fork URL 解析正常，仅非 GitHub 源（镜像/自托管）触发；仍应显式报错 | `pkg/updater/updater.go:220-225, 400-402` | ✅ |
| B5 | **config 写锁三分**：`configWriteMu`（PATCH/PUT/reset/tray）与 `h.configMu`（models.go 等 5 处）互不互斥，wecom/weixin/oauth/pico/tools 等 SaveConfig 路径完全无锁——并发保存可互相覆盖 | `web/backend/api/config.go:23`、`router.go:25`、`models.go:706` | ✅（双锁已证实） |
| B6 | **流式能力矩阵不齐**：ChatStream 仅 openai_compat/gemini/bedrock 实现；anthropic(SDK)/anthropic_messages/azure/cli 系配 `streaming.enabled` 被静默降级（provider_not_streaming）；anthropic_messages 还缺 thinking_level 与 SystemParts/cache_control | `pipeline_streaming.go:60-66` | — |
| B7 | **MQTT TLS 校验硬编码关闭**且无配置出口 | `pkg/channels/mqtt/mqtt.go:83` | — |
| B8 | **telegram 流式节流按字节计**：CJK 下 growth 虚高 ~3 倍，节流比设计激进（feishu B4 同类问题未同步） | `pkg/channels/telegram/telegram.go:1707` | ✅ |

### 1.3 死配置假旋钮（用户可见误导）

逐个 grep 证实字段名在 config 包外零读取（✅ 抽样已复核）：

- `voice.elevenlabs_api_key`（`config.go:1132`，实际 key 从 model_list 取）
- pico `settings.write_timeout`（`config.go:1043`，实际硬编码 30s 于 manager.go:1233）
- onebot `group_trigger_prefix`（`config.go:1004`，已被 migration 迁走，仅残留吸收）
- discord `mention_only`（`config.go:930`，渠道走 GroupTrigger）
- line `webhook_host/webhook_port`（`config.go:995-996`）
- weixin `account_id`（`config.go:1025`）
- `ModelConfig.Enabled` 语义未在模型解析处执行（仅 CLI 列表隐藏用）——`enabled:false` 的条目仍可被 /switch、fallback 命中（`config.go:1200`）

---

## 2. 冗余清单（A 类，可批量清理）

### 2.1 死文件（约 2200 行生产代码可整删）

| 文件 | 行数 | 判定依据 |
|---|---|---|
| `pkg/agent/adapters/`（2 文件） | ~80 | 整包零引用；`*bus.MessageBus`/`*channels.Manager` 隐式实现接口，适配器从未构造 ✅ |
| `pkg/evolution/organizer.go` | 397 | Organizer 被 PatternClusterer 取代；`Runtime.organizer` 赋值后零读取（冷路径走 patternClusterer）✅ |
| `pkg/evolution/preview.go` | 154 | BuildDraftPreview 零生产调用（或作为 C3 修复的一部分接线） |
| `pkg/utils/context.go` | 173 | TruncateContextSmart 等四函数被 seahorse CompactUntilUnder 取代，零调用 |
| `pkg/agent/legacy_events.go` | 186 | SubscribeEvents/UnsubscribeEvents 生产零调用（自述 tests & compatibility）；连带 events.go 的 Event 旧信封与 evolutionBridge.OnEvent |
| `pkg/mcp/probe.go` | 63 | 半接线（见 C9） |
| `web/backend/model/` | ~10 | 整包死（StatusResponse 服务的端点已不存在），零 import ✅ |
| `web/backend/api` 的 `PUT /api/config` 死端点 | ~80 | 前端只用 GET/PATCH；systray 走 PatchConfigFile；handleUpdateConfig 无调用方无测试 |
| `pkg/health` 的 Start/StartContext/Stop + s.server 字段 | ~40 | 端点实际经 RegisterOnMux 挂共享 mux，gateway.go 注释自证 "never called"；RegisterCheck 从未接线（/ready 恒空 checks）——接线与否批次三定，死方法先删 |
| `web/frontend/src/api/pico.ts` | 40 | 零 importer ✅ |
| `pkg/tools/toolloop.go`（连带 subagent 旧链） | ~220 | RunToolLoop 唯一调用点在不可达的 runTask legacy 分支内（传递性死代码） |

### 2.2 死导出符号（各包，均可降级/删除）

- **pkg/agent**：`steering.go` 死 API 群约 150 行（SteeringMode/SetSteeringMode/dequeueSteeringMessages/InterruptGraceful/InterruptHard(已标 Deprecated)/dequeuePendingSubTurnResults/InjectFollowUp/InjectSteering，生产零调用）；`turn_state.go` LLMPhase 枚举 9 常量+phase 字段（只赋值不读）；`pipeline.go:23` `Steering any` 死字段（TODO 自我声明未接完）；ContextBuilder 四死方法（BuildMessages/AddToolResult/AddAssistantMessage/RegisterPromptSource）；`MemoryStore` 六方法中三个零调用（WriteLongTerm/ReadToday/AppendToday）；散件：populateCandidateProvidersFromNames、AgentRegistry.ListAgents/GetAgentDescriptor、RuntimeEvents()/RuntimeEventStats()、mcpRuntime.hasManager()、WithRuntimeEvents、NamedHook、cancelConfiguredStreamingLLM、interceptDeferredTool、newRuntimeEventLogger
- **pkg/tools**：TruncateHead（50 行零调用）、SetAllowPatterns/SetRestrictToWorkspace（零调用）、registry GetDefinitions/GetSummaries/Count（仅测试）、session 的 stdoutPipe 死字段/ErrPTYNotSupported/SetStatus/SetExitCode、SearchBM25（被缓存版取代）、NewExecTool/NewReadFileTool 包装/NewWebFetchTool 包装、WebSearchProviderReady/ResolveWebSearchProviderName
- **pkg/providers**：10+ 个死构造器/变体（NewProviderWithMaxTokensField 族、httpapi 3 参/4 参、anthropic NewProviderWithClient、azure 变参 NewProvider+选项、bedrock WithProfile、ResolveCandidates、cooldown MarkFailure/ErrorCount/FailureCount）；httpapi 中间层纯透传可收缩
- **pkg/channels**：telegram `chatIDs` map 只写不读（带锁）✅；`asrCapableChannels` 旧查找表（8 渠道已全部迁 VoiceCapabilityProvider 接口，fallback 不可达）；base.go GetPlaceholderRecorder 零调用；weixin UploadMediaTypeVoice；feishu resetTrackedToolFeedbackAfterEditFailure 与 DismissToolFeedbackMessage 函数体逐字相同；pico truncate 重复 utils.Truncate、broadcastFn 死测试缝
- **pkg/evolution/seahorse/utils/logger/events/config**：RunLifecycleOnce、NewColdPathRunner、IngestMessages、RecordKindCase/Rule、Store 六个导出方法生产零调用、NormalizeSkillName、DownloadFileSimple、logger 六个包级函数、events And/Or 组合子、config AutoAppliesDrafts、migrate HandlerFactory

### 2.3 跨模块复制粘贴（收敛候选）

| 簇 | 位置 | 建议 |
|---|---|---|
| 工具反馈追踪八件套 ×5 渠道（~70-80 行/份） | feishu_64.go:366、telegram.go:524、pico.go:456、discord.go:402、matrix.go:676（diff 证实逐行一致）| 提取可嵌入 `channels.ToolFeedbackTracker` ✅（计数复核） |
| `outboundMessageIsToolFeedback` 判定 ×6 份 | manager.go:171（私有致渠道重写） | 导出为 channels.IsToolFeedback |
| weixin/wecom 出站媒体解析镜像 ×2（~150 行） | weixin/media.go:654-764 vs wecom/media.go:174-542；临时目录还不一致 | 提公共 resolver |
| slack_webhook ↔ teams_webhook 脚手架（~200 行） | 构造校验/Start/Stop/Send/错误脱敏 + markdownTableRe 一字不差 + splitContentWithTables | 提公共 webhookout 基座 |
| weixin/wecom/oauth flow map 六件套 ×3 | web/backend/api/weixin.go:270、wecom.go:370、oauth.go:603（TTL/GC 常量都相同） | 泛型 flowStore[T] |
| anthropic ↔ anthropic_messages 映射重复 | toolName/args 回退解析逐字同（:191-211 vs :243-265）、stop_reason 映射、cache 口径、工具 schema 翻译 | 提 anthropic_common |
| config 版本迁移三连复制 | config.go:1857-2009 case 0/1/2 各 ~45 行同构 | 抽 runMigration |
| 两套 CJK 感知估算器 | routing/features.go:58（CJK=1 token/字）vs tokenizer/estimator.go:72（CJK=1.6 字/token）——**校准已漂移** | routing 复用 tokenizer |
| 两套上下文溢出模式表 | pipeline_llm.go:460-467 内联表 vs error_classifier.go:139 正则表（覆盖面已漂移：max_tokens/invalidparameter/下划线形态）✅ | 统一引用一张表 |
| sanitizeIdentifierComponent 双胞胎 | tools/identifier_compat.go:5 vs integration/mcp_tool.go:92 | 合一 |
| 会话 cookie 校验 ×3 | web/backend auth.go:225/281 vs middleware/launcher_dashboard_auth.go:290 | middleware 导出 helper |
| 媒体落盘闭包 ×3、typing 循环 ×3、消息去重 ×2 | discord/slack/line、discord/qq/matrix、qq/onebot | 低优先级提取 |
| 手工 Content-Type 头 94 处 | middleware 已统一设置 | 随触随清 |

---

## 3. 代理断言修正记录（复核未全盘采信项）

1. **updater**（原断言「传 fork release URL 会刷入上游二进制」）：实测 `buildReleaseAPIURL` 对 github.com host 正常解析 owner/repo——fork 的 GitHub release URL 不受影响；仅**非 GitHub 域名**静默回落 sipeed 上游。结论保留但范围收窄。
2. **web 停止回合**（原断言「需要先扩后端协议、成本大」）：`/stop` 作为普通聊天消息即可被 busy 分支拦截生效（fork 既有拦截顺序），前端按钮属小改动。结论保留但成本从大改小。
3. **chat-pi vs features/chat「新旧两套并存」疑云**：实际是渲染层（chat-pi，pi-web-ui 移植）/协议层（features/chat）/输入区（components/chat）三层分工，无死套件；真正的旧遗产是 components/chat/message-code-block（~600 行，仅剩 2 个边缘消费者）。

---

## 4. 建议实施批次

| 批次 | 内容 | 预估 |
|---|---|---|
| **一（清理批）** | 死文件/死导出全量删除（§2.1+§2.2，~2400 行）+ 死配置假旋钮删除（§1.3）+ i18n 死键清理 | 小，纯删除，全测试兜底 |
| **二（断链修复批）** | C1 spawn_status（接通或删工具+旧链）、C2 custom_allow_patterns 生效门、C4 web 停止按钮、C5 会话重命名 API+入口、C7 delta 事件（emit 或删）、B1 504+配额提升、B2 MarshalJSON 补字段 | 中 |
| **三（韧性批）** | B3 anthropic_messages 错误通道、B4 updater 显式报错、B5 config 单锁、B8 telegram rune 节流、C8 三个断线假活渠道补重连、B7 MQTT TLS 开关 | 中 |
| **四（功能增强批）** | C3 /skills drafts 命令、C9 doctor 接 probe、C6 subagent 参数收口（补实现或删 schema）、前端剪贴板统一+移动端侧栏+pico surface 复用、glob/grep 工具补缺 | 大，需逐项设计 |

## 附：核对无误（非问题）的排查结论

- pkg/utils vs fileutil、state vs session、media vs agent 互补无重叠；24 中小包全部有生产引用。
- slack vs slack_webhook 互补不重叠；teams_webhook 与通用 webhook（接口）无冲突。
- pico/telegram **不是** feishu B2 同病（surface 不复用无抢卡），真缺口是「每 LLM 迭代新 surface」的体验不对齐（telegram 旧 draft 残留、pico 旧消息残留）。
- memory recall/commit 关闭路径无死结构；seahorse 除 1 个死别名外无死码；cron/commands/skills 接线完整。
- CORS/路径穿越/会话 key 净化初查无缺口（/api/update 换二进制仅单层会话，列为已知取舍）。
- pkg/events 46 Kind 全有 emit（17 个仅日志消费——可观测性设计，建议文档标注消费面）。

## 附二：批次一实施记录（2026-10-31 当日落地）

**已删（死文件/死导出/死端点，约 2400+ 行）**：pkg/agent/adapters 整包、evolution organizer.go+preview.go（live helper 迁入 cluster_helpers.go）、utils/context.go、agent legacy_events 层（Event 信封/EventKind 别名/OnEvent/SubscribeEvents，测试改走 RuntimeEventBus）、tools toolloop.go+subagent 旧执行链（SubagentManager 收窄为播种+spawn_status 数据源）、TruncateHead、registry GetDefinitions/GetSummaries/Count、session 死字段死方法、steering 死 API 群（InterruptGraceful/Hard/InjectFollowUp/InjectSteering/SteeringMode 存取/dequeueSteeringMessages，测试改内部路径或 HardAbort）、LLMPhase 枚举、Pipeline.Steering、ContextBuilder 四死方法、MemoryStore 三死方法、populateCandidateProvidersFromNames、RuntimeEvents/RuntimeEventStats、cancelConfiguredStreamingLLM、telegram chatIDs、base.go GetPlaceholderRecorder、weixin UploadMediaTypeVoice、evolution RunLifecycleOnce/RecordKind 别名、seahorse IngestMessages、utils NormalizeSkillName/DownloadFileSimple、logger 五函数、events And/Or、migrate HandlerFactory、openai_compat 与 httpapi 死构造器 4 个、`PUT /api/config` 死端点、web/backend/model 包、前端 api/pico.ts/showAssistantDetailsAtom/formatMessageTime、@radix-ui/react-popover 依赖统一进伞包。

**死配置假旋钮**（6 项）：字段删除 + `toleratedLegacyNoopKeys` 容错清单（diagnostics.go，仿 _comment 模式——未知键在严格加载下是硬错误，存量配置必须可继续加载，下次保存自然消失）。

**复核推翻代理断言 6 项（保留/恢复）**：
- `pkg/mcp/probe.go` **活**（web MCP 页「测试连接」调用 ProbeServer）；
- `asrCapableChannels` **活**（8 渠道全都没实现 VoiceCapabilityProvider，查找表就是现行机制）；
- `WebSearchProviderReady`/`ResolveWebSearchProviderName` 门面**活**（web/backend api/tools.go 消费）；
- `WeixinSettings.AccountID` **活**（api/weixin.go 登录回写）；
- pico `broadcastFn`、agent `WithRuntimeEvents` 为**测试缝**（保留）；
- `ListAgents`/`GetAgentDescriptor`/`NamedHook`/`BuildMessages`/`MarkFailure`/`ResolveCandidates`/`NewExecTool`/`SetRestrictToWorkspace`/`NewProviderWithClient`/`GetLevel`/`hasManager`/`interceptDeferredTool`/`newRuntimeEventLogger`/`InterruptGraceful 事件语义` 等为测试保活或合理便利 API（保留）。

**顺带修复**：chat-pi 两处剪贴板改走 `copyText`（HTTP 部署降级）、i18n 四个缺失键补齐 5 locale（zh 界面露英文问题）、`agent_command.go` ListSkillNames 重复赋值、agent_init 每次 spawn 白构建工具切片（随 SetSpawner 闭包删除）。

**验证**：go vet 全绿；前端 pnpm build 通过；`go test ./pkg/...` 失败集 = 预存环境失败集（audio/asr、deltachat、isolation、migrate、8 个 shell 路径守卫用例——stash 对照确认预存），无新增失败。
