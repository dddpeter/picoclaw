# 大文件拆分实现计划（TOP 5 非测试文件）

> **面向 AI 代理的工作者：** 必需子技能：使用 subagent-driven-development（推荐）或 executing-plans 逐任务实现此计划。步骤使用复选框（`- [x]`）语法来跟踪进度。

**目标：** 把五个 1900+ 行的非测试大文件按职责拆成 300–700 行的专注文件，零 API/行为变化。

**架构：** 全部采用"同包文件移动"——新文件与原文件同属一个 Go package，只搬运 type/func/注释，不改任何签名、不新增 import 路径、不动任何测试文件的引用。每个文件独立成一个任务，互不依赖，可乱序执行。

**技术栈：** Go 1.x + 既有 go test 套件 + golangci-lint（0 issues 为门禁）。

**规格：** 2026-11-25 质量审计报告（本会话前段）；`docs/design/fork-overview.zh.md` §同步注意事项（拆分后登记新路径）。

## 全局约束

- 零行为变化：只移动代码块（type/func/const/var/注释），禁止顺手改逻辑、重命名、调整可见性。
- 同包移动：新文件 package 声明与原文件相同；不新建子包（避免 import 环与导出面膨胀）。
- 每个任务结束门禁：`go build ./...` 通过 + 对应包 `go test` 全过 + `golangci-lint run ./...` 0 issues + `gofmt -l` 干净。
- fork 敏感区（shell.go 的 defaultDenyPatterns/execIOWaitDelay、pico.go 的流式语义、config.go 的 `*bool` 三件套）原样搬运，注释与代码一起走。
- 每任务一个 commit，conventional commits；拆分完成后在 fork-overview 登记新文件路径（上游同步时 fork 冲突面以新路径为准）。
- 拆分以"注释分段标记"（`// ===`）与函数组为切割线；单文件目标 ≤700 行。
- 提交顺序内文件间无依赖；任务 1–5 可独立执行、可乱序。

## 审查重点（Review Focus）

1. **搬运丢块**：切出时漏函数/漏注释/漏关联 const——每任务用 `grep -c '^func'` 前后守恒 + 全包编译兜底（漏搬必然 undefined）。
2. **上游同步丢 fork 特性**：config.go/shell.go 是 fork 冲突面，拆散后同步时容易漏——fork-overview 登记新路径，`*bool` 三件套注释跟代码走。
3. **import 残留**：原文件切出后 import 不再使用（`goimports`/`gci` 清理），新文件补齐所需 import——编译+lint 门禁兜底。
4. **测试锚点漂移**：测试引用同包符号不受影响，但 fork-overview 点名的测试锚点（TestShouldCompactNow、TestFallback_AllInCooldown* 等）必须全过——每任务跑对应包全量测试。
5. **注释分段错位**：文件级 doc 注释（`// xxx …` 紧贴 type/func）必须跟随其符号移动——lint 的 godoclint 兜底。

---

### 任务 1：pkg/config/config.go（2559 → 约 5 文件）

**文件：**
- 修改：`pkg/config/config.go`（保留顶层骨架）
- 创建：`pkg/config/config_agents.go`、`pkg/config/config_channels.go`、`pkg/config/config_models.go`、`pkg/config/config_tools.go`

**职责切分（按现有代码段）：**
- `config.go`（保留 ~430 行）：CurrentVersion/init/顶层 `Config`/WebConfig/Memory*/Evolution*/Isolation/ExposePath/Hooks*/BuildInfo + `Config.FilterSensitiveData`/`MarshalJSON`/`GetFileAccessRoots`。
- `config_agents.go`（~950 行）：AgentsConfig/AgentModelConfig/AgentConfig/SubagentsConfig/Dispatch*/SessionConfig/RoutingConfig/SubTurnConfig/ToolFeedbackConfig/IdleCompactConfig/SplitTurnConfig/AgentDefaults/RestartRecoveryConfig/LoopDetectionConfig + 全部 Effective*/Get* 方法。
- `config_channels.go`（~470 行）：GroupTrigger/Typing/Placeholder/StreamingConfig + 20 个渠道 Settings 类型（WhatsApp…MQTT/TeamsWebhook/SlackWebhook）+ 各 SetToken/SetSecret。
- `config_models.go`（~700 行）：Heartbeat/Devices/Voice/ModelStreamingConfig/ModelConfig（含 fork 的 ContextWindow 注释）/SecureModelList 相关 helper + Brave/Tavily/Kagi/DuckDuckGo/Sogou/Gemini/Perplexity 等 API key 配置。
- `config_tools.go`（剩余）：ToolDiscovery/ToolConfig/MessageToolsConfig/ToolsConfig 及 WebToolsConfig 等 tools 子结构。

- [x] 步骤 1：按上述分段把代码块剪切到新文件（Python 按行号段移动，避免手滑）
- [x] 步骤 2：`goimports -w pkg/config/` 清理 import
- [x] 步骤 3：`go build ./...`；`go test ./pkg/config/...` 全过
- [x] 步骤 4：`golangci-lint run ./...` 0 issues；`gofmt -l` 干净
- [x] 步骤 5：`git add` + commit `refactor(config): config.go 按域拆分为五个文件`
- [x] 步骤 6：fork-overview 同步注意事项中 config.go 引用处补注新路径

### 任务 2：pkg/tools/integration/web.go（2324 → 约 7 文件）

**文件：**
- 修改：`pkg/tools/integration/web.go`（保留装配层）
- 创建：`pkg/tools/integration/web_fetch.go`、`search_providers.go`、`search_ranges.go`、`search_kagi.go`、`search_cjk.go`、`search_rest.go`

**职责切分：**
- `web.go`（保留 ~450 行）：APIKeyPool/APIKeyIterator/SearchProvider 接口/SearchResultItem/cleanSearchText/stripTags + WebSearchTool 全部（Options/Resolve/Provider 路由/buildProviderResolver/Execute）。
- `web_fetch.go`（~470 行）：WebFetchTool 全部 + 其 helper。
- `search_ranges.go`（~150 行）：9 个 map*Freshness/TimeRange/RecencyFilter 时间范围映射 + normalizeSearchRange/applySogouRangeHint。
- `search_providers.go`（~250 行）：Brave + Tavily 两个 provider。
- `search_kagi.go`（~230 行）：Kagi provider + APIClient/格式化/fallback 解析族。
- `search_cjk.go`（~260 行）：Sogou + Gemini + Baidu（CJK 路径）。
- `search_rest.go`（~300 行）：DuckDuckGo + Perplexity + SearXNG + GLM。
- 测试文件 `web_test.go`（2418 行）本期不动（同包引用零变化）。

- [x] 步骤 1：按段切分到新文件
- [x] 步骤 2：goimports 清理；`go build ./...`
- [x] 步骤 3：`go test ./pkg/tools/integration/...` 全过
- [x] 步骤 4：lint 0 issues 门禁
- [x] 步骤 5：commit `refactor(tools): integration/web.go 按 provider 拆分`

### 任务 3：pkg/channels/manager.go（2308 → 约 5 文件）

**文件：**
- 修改：`pkg/channels/manager.go`
- 创建：`manager_dispatch.go`、`manager_http.go`、`manager_feedback.go`、`manager_payload.go`

**职责切分：**
- `manager.go`（保留 ~600 行）：Manager/channelWorker struct、ManagerOption、生命周期（Start/Stop/StartAll/StopAll）、Register/Unregister/Reload/GetStatus/GetChannel/GetEnabledChannels/SendMessage/SendMedia/SendToChannel 公共 API。
- `manager_dispatch.go`（~700 行）：newChannelWorker/runWorker/dispatchLoop 泛型/dispatchOutbound(Media)/enqueueOutboundForDispatch/sendWithRetry/sendMediaWithRetry/splitOutboundMessageContent/finalizedStreamActiveForMessage/runMediaWorker/runTTLJanitor。
- `manager_http.go`（~200 行）：SetupHTTPServer(Listeners)/registerHTTPHandlers(Unregister)/registerChannelHTTPHandler。
- `manager_feedback.go`（~500 行）：tool feedback 四接口 + 实现、typing/reaction/placeholder 管理。
- `manager_payload.go`（~150 行）：outboundMessage* 纯函数族 + asyncTask/channelRateConfig/条目 struct。

- [x] 步骤 1：切分（注意 `// ====` 分段注释跟随其组）
- [x] 步骤 2：goimports；`go build ./...`
- [x] 步骤 3：`go test ./pkg/channels/...` 全过
- [x] 步骤 4：lint 门禁
- [x] 步骤 5：commit `refactor(channels): manager.go 按生命周期/派发/反馈/HTTP 拆分`

### 任务 4：pkg/channels/pico/pico.go（2168 → 约 6 文件）

**文件：**
- 修改：`pkg/channels/pico/pico.go`
- 创建：`pico_send.go`、`pico_streamer.go`、`pico_ws.go`、`pico_a2ui.go`、`pico_message.go`

**职责切分（包内已有 pico_file/upload/myfiles/context_usage 先例，沿用命名法）：**
- `pico.go`（保留 ~480 行）：picoConn/PicoChannel struct、NewPicoChannel、连接管理族（createAndAdd/remove/takeAll/snapshot/count）、Start/Stop/WebhookPath、getTurnActiveProbe/usage 缓存、model controller。
- `pico_send.go`（~620 行）：Send/EditMessage(WithPayload)/DeleteMessage/BroadcastRevert/tool feedback 跟踪族（Record/Clear/Dismiss/Finalize/take/current）/StartTyping/SendPlaceholder/BeginStream 入口。
- `pico_streamer.go`（~330 行）：picoStreamer 全部（Set*/Update/Finalize*/Cancel/*Locked 族）。
- `pico_ws.go`（~360 行）：ServeHTTP/handleWebSocket/readLoop/pingLoop/authenticate/matchedSubprotocol/parseLastSeqQuery/replayCatchup/broadcast/broadcastToSession/handleMessage(Send)/endCatchup。
- `pico_a2ui.go`（~160 行）：a2uiSurfaceKey/State/Registry + merge/overwrite 批处理。
- `pico_message.go`（~200 行）：outboundMessageIs* 判定 + setContextUsagePayload/setTurnUsagePayload/picoToolCallsPayload/appendFileTags/truncate + 媒体 helper（download URL/refID/attachment type/inline image 校验族）。

- [x] 步骤 1：切分
- [x] 步骤 2：goimports；`go build ./...`
- [x] 步骤 3：`go test ./pkg/channels/pico/...` 全过
- [x] 步骤 4：lint 门禁
- [x] 步骤 5：commit `refactor(pico): pico.go 按连接/发送/流式/WS/A2UI 拆分`
- [x] 步骤 6：fork-overview §28 引用的 pico.go 位置登记新路径

### 任务 5：pkg/tools/shell.go（1911 → 约 4 文件）

**文件：**
- 修改：`pkg/tools/shell.go`
- 创建：`shell_run.go`、`shell_jobs.go`、`shell_guard.go`

**职责切分：**
- `shell.go`（保留 ~520 行）：ExecTool struct + 全部字段注释、New*/setter、Name/Description/Parameters/Execute/executeRun/resolveRunTimeout/getSessionManager。
- `shell_run.go`（~700 行）：runSync/runBackground/persistFullOutput/cappedOutputBuffer/formatBytes。
- `shell_jobs.go`（~660 行）：executeList/Poll/Read/Write/Kill/SendKeys + detectPtyKeyMode/encodeKeyToken/encodeKeySequence。
- `shell_guard.go`（~450 行）：guardCommand/commandMatchesAllowPattern/commandPathAbs/commandPathTextFromMatch/shellTokenBounds/边界判定 helper 族（含 defaultDenyPatterns 注释整体搬运）。

- [x] 步骤 1：切分（**fork 红线**：exec 卡死三连修、deny patterns 语义注释原样搬运）
- [x] 步骤 2：goimports；`go build ./...`
- [x] 步骤 3：`go test ./pkg/tools/...` 全过（约 50s，含 shell 套件）
- [x] 步骤 4：lint 门禁
- [x] 步骤 5：commit `refactor(tools): shell.go 按执行/作业/守卫拆分`
- [x] 步骤 6：fork-overview「主要冲突面」条目更新 shell.go → shell*.go

---

## 收尾

- [x] 全量门禁复跑：`go build ./...` + `go test ./pkg/... ./web/... ./cmd/...` + `golangci-lint run ./...` 0 issues
- [x] `wc -l` 复查五组文件均 ≤700 行
- [x] fork-overview 登记（任务 1/4/5 的步骤 6 汇总一次提交 `docs: fork-overview 登记拆分后路径`）

---

## 执行结果（2026-11-25 回填）

五个任务全部执行完成，由 5 个 worktree 隔离子代理并行执行、主线程逐个合并。实际行数与计划估计的偏差均已披露：

| 任务 | 计划 | 实际 | 提交 |
|---|---|---|---|
| 1 config.go | 5 文件 | 6 文件：334/533/271/485/477/489（config_load.go 为合并后补充整理，LoadConfig 族从 tools 域分离） | 子代理 `f8504703` + 主线程 `22a554f5` |
| 2 integration/web.go | 7 文件 | 7 文件：631/349/156/247/277/334/415（web.go 高于估计：装配层符号按规格完整保留） | `2efabb89` |
| 3 channels/manager.go | 5 文件 | 6 文件：812/538/462/107/352/121（manager_streamer.go 为评审 I-1 补充拆分） | `d45c5200` + `598d4346` |
| 4 pico.go | 6 文件 | 6 文件：312/507/239/646/167/355 | `d66b63d5` |
| 5 shell.go | 4 文件 | 4 文件：366/576/400/598 | `c600d979` |

**等价性验证（主线程独立复核，不依赖子代理自检）**：go/ast 规范化比对五包全部 func/type/const/var 声明零差异（唯一差异为 import 重组与 1 个 regex var 块按消费者拆分，RHS 经 AST ValueSpec 级核对逐字一致）；测试文件零改动；fork 红线 7 项逐字抽查全过；合并后全局门禁 build/lint(0 issues)/gofmt/97 包测试全过。详见同日代码评审报告。
