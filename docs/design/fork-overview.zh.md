# Fork 功能地图（dddpeter/picoclaw）

> 本仓库 fork 自 [sipeed/picoclaw](https://github.com/sipeed/picoclaw)，在保留上游全部能力的基础上做了面向个人部署（飞书 IM + 中台网关模型 + systemd 用户服务）的定向增强。上游通过 `upstream` remote 跟踪，合并上游时本文档列出的文件是主要冲突面。
>
> 维护日期：2026-09-07（对应提交 `539c046f` 的 fork 状态；fork 领先上游 53 个自有提交、落后 0，上游 main 已停滞——合并方向为单往上 fork 里 merge 上游）

## 功能块总览

| 功能块 | 起始提交 | 核心代码 | 文档 |
|---|---|---|---|
| 飞书 CardKit 流式卡片 | `9a99dcd4` | `pkg/channels/feishu/` | 本文 §1 |
| OpenViking 共享记忆 | `87850045` / `06d3aab5` | `pkg/agent/memory_*.go` | `docs/guides/openviking.md` |
| 命令增强（/new、/status） | `125b3287` | `pkg/commands/` | 本文 §3 |
| 工具输出处理 | `a5b71f89` / `3fce044b` | `pkg/tools/truncate.go`、`pkg/tools/output_clean.go` | 本文 §4 |
| 可靠性加固 | `9bab2b25`…`3fce044b` | `pkg/agent/turn_health.go` 等 | 本文 §5 |
| exec 安全加固 | `9cc5a3b1` | `pkg/tools/shell.go`、`pkg/config/config.go` | 本文 §6、`docs/security-exec-hardening.md` |
| 卡片停止按钮（已移除渲染） | `fc18f452` | `pkg/channels/feishu/` | 本文 §7 |
| Web launcher 主题 | `5d3ad431` | `web/frontend/src/index.css`、`web/frontend/src/hooks/use-theme.ts`、`web/frontend/src/components/theme-switcher.tsx` | 本文 §8 |
| 运维禁令 | `23b1275d` | `AGENTS.md` | 本文 §9 |
| 技能目录扩展与项目文档注入 | `<本次>` | `pkg/skills/loader.go`、`pkg/agent/project_docs.go` | 本文 §10 |
| cron 增强 + 自动化建议 + /learn | `<2026-09-13>` | `pkg/cron/service.go`、`pkg/cron/suggestions.go`、`pkg/cron/blueprints.go`、`pkg/tools/cron.go`、`pkg/evolution/cron_suggester.go`、`pkg/commands/cmd_cron.go`、`pkg/commands/cmd_learn.go` | 本文 §11、`docs/guides/configuration.zh.md` 定时任务/学习章节 |
| 会话标题两阶段生成 | `48bf141f` | `pkg/agent/session_title.go`、`pkg/memory/jsonl.go` | `docs/design/hermes-borrowing-analysis.zh.md` §二 |
| Turn 韧性（429/LLM 失败） | `<2026-09-09>` | `pkg/agent/pipeline_streaming.go`、`pkg/providers/error_classifier.go`、`pkg/providers/cooldown.go` | 本文 §5、`docs/design/turn-llm-failure-resilience.zh.md` |
| Windows exec 卡死与会话丢失三连修 | `<2026-09-11>` | `pkg/tools/shell.go`、`pkg/tools/shell_process_windows.go`、`pkg/agent/steering_abort.go` | 本文 §4、§5 |
| Web MCP 独立页面 | `<2026-09-13>` | `web/backend/api/mcp.go`、`pkg/health/server.go`、`pkg/agent/agent_mcp.go`、`web/frontend/src/components/mcp/` | `docs/design/web-mcp-page-design.zh.md` |
| 配置模板兼容（_comment + api_key 别名） | `<2026-09-14>` | `pkg/config/diagnostics.go`、`pkg/config/config.go`（四搜索 provider）、`config/config.example.json` | 本文 §5、同步注意事项 |

## 1. 飞书 CardKit v2 流式卡片

用飞书卡片（CardKit v2）承载 agent 回复的流式渲染，替代纯文本回复：

- 过程面板：工具调用步骤、嵌套推理轮次（reasoning rounds）、kind 着色图标、动态状态行、**面板默认折叠**（思考文字照常流式写入面板内并归档，点开可回看；整卡刷新会重放 `expanded=false`，流式期间手动展开会被下一次刷新压回，封卡后展开才稳定）；工具输出以行内代码渲染以控制卡片体积。
- **行为对齐 hermes-lark-streaming**（`539c046f`，2026-09-07）：①面板条目按事件到达序**时间线交错**渲染（R→T→R→T），不按类型分组；②面板标题始终显示**裁剪前的实际总数**（含运行中条目），超 20 上限不缩水，标题耗时 = 推理 + 工具之和；③工具执行中显示 `⏳ …（运行中）` amber 条目（`bus.ToolStep.Running`，pipeline 执行前发布，完成即替换）。配套修复：`streamingChunkPublisher` 放行无工具名的 KindText 归档步骤（`280ffb8a` 的"本轮说明"此前被空名门禁拦截，从未到达面板）。测试锚点：`TestFeishuPanelTimelineInterleave`、`TestFeishuPanelHeaderShowsActualTotals`、`TestFeishuPanelRunningTool`、`TestStreamerRunningToolStepLifecycle`、`TestAppendToolStepAllowsUnnamedTextArchives`。
- 打字机效果跨面板刷新保持存活；中止/取消时显示原因（cancel reason）与 steering 通知；turn 在 LLM 调用中被中止时流式卡片保持可达。
- **中途说明追加式呈现**：带工具调用的迭代，其正文在管线归档（`KindText` 步骤，`ExecuteTools` 入口）时被钉为答案区上方一行灰字（空白折叠为单行、200 字截断、只保留最近 5 行，`feishuStreamState.Narration`），下一轮在轨迹下方继续打字，不再整块覆盖；封卡后「灰字轨迹 + 最终答案」一起保留，卡片摘要只取最终答案；面板里的「💬 本轮说明」归档不变。管线零改动。测试锚点：`TestNarrationLinesAppendAboveLiveAnswer`、`TestNarrationLineCaps`、`TestNarrationTrailSurvivesCancel`。
- **流式模式超时韧性（200850）**：回合长时间无元素写入（如长工具运行）时飞书会服务端自动关闭流式模式，元素写入随即报 `card streaming timeout`。处理顺序：按官方补救用 settings 接口重开 `streaming_mode=true` 并重试一次；重开失败则降级为全卡更新（config 换 `update_multi`，不再带流式 config），**绝不因失去打字机而失败整个 LLM 调用**（此前的故障形态：长阻塞后续流失败直接报 "LLM call failed after retries"）。测试锚点：`TestUpdateRecoversFromStreamingTimeout*`、`TestUpdateDegradesWhenReopenFails`、`TestDegradedUpdateSkipsElementWrites`、`TestFinalizeSkipsCloseWhenDegraded`。
- 细节加固：rune 安全截断、无效图片 key 清洗（规避 CardKit 200570）、CardKit 调用次数上限。

相关提交：`9a99dcd4`、`e9e656cd`、`009fec8f`、`7b8f21d9`、`b0ba0c49`、`8f31514a`、`1150de27`、`ac9be327`、`d9d113b5`、`7338eac0` 等。

## 2. OpenViking 共享记忆

三阶段接入 OpenViking 记忆服务（详见 `docs/guides/openviking.md`）：

- 阶段 0：OpenViking 的 streamable-HTTP `/mcp` 端点接入 `tools.mcp.servers`，模型获得 `mcp_openviking_*` 工具。
- 阶段 1（recall）：turn 开始时以用户消息为查询调用记忆服务 `search` 工具，命中内容注入系统提示词记忆槽（`memory.recall` 配置）。
- 阶段 2（commit）：完成的 turn（用户消息 + 最终答复）推送到记忆服务 `remember` 工具，异步蒸馏为长期记忆（`memory.commit` 配置）。

相关提交：`87850045`（recall）、`06d3aab5`（commit）、`0f88fa1b`（评审修复）。

## 3. 命令增强与语义差异

- `/new`（`125b3287` + `9bab2b25` + `d349a638`；**2026-09-12 改为归档轮换**）：开新会话——把当前对话**归档**为独立会话（历史/摘要/标题/scope 元数据迁入新归档 key，`pkg/agent/session_rotate.go`），活会话原地清空并清掉标题（下轮重新起名），模型重置为**配置文件里当前定义的默认模型**（重新读取磁盘配置，优先级 AGENT.md frontmatter > agents.list > defaults）。回复中携带归档提示与当前模型名；空会话无可归档时跳过归档。`/clear` 仍是纯清空（`Runtime.ClearHistory`），`/new` 走 `Runtime.NewSession`（nil 时回退 ClearHistory）。
- `/status`（`125b3287`）：运行状态总览（版本、模型、通道、活跃任务）。
- **Busy 语义**（`d349a638` / `03471ee6`）：`/switch model` 与 `/new` 的模型重置在 turn 活跃期间**不排队等待**，通过 TryLock 立即返回 skipped 提示。设计动机：turn 全程持有模型状态读锁，阻塞式写锁会在 LLM 调用挂起时死锁命令。
- 未促使内容处理（`8e077180`）：agent 对未被提示的内容做实质性分析而非简单确认。

## 4. 工具输出处理（inline 送模型路径）

- 结构化 head/tail 截断（`a5b71f89`）：超限输出保头保尾并附明确提示，完整原文落盘供模型回读。
- inline 清理管线（`3fce044b`，借鉴 MiMo-Code token-efficient 设计）：进度条折叠（`\r` 重绘只留末帧）→ ANSI 转义剥离 → 超长行压缩（>500 字符压为头部 160 字符 + 省略提示）；never-worse 守门（清理不缩小即回吐原文）；命令含 `--json` / `-o json` / `| tee` / `# nofilter` 时整路放行。**落盘与截断提示措辞不受影响**。
- **CRLF 行尾修复（2026-09-11）**：进度条折叠此前把行尾 `\r`（Windows CRLF 行尾）也当重绘、只保留"最后一个 `\r` 之后的内容"（=空），Windows 下命令输出几乎全部被清成空行。现先剥离行尾 CR 再匹配行中重绘。测试锚点：`TestCleanCommandOutput_PreservesCRLFLineContent`。
- 背景调研与取舍：`docs/design/mimo-code-borrowing-analysis.zh.md`。

## 5. 可靠性加固

- 流式响应头超时（`03471ee6`）：ChatStream 使用克隆 Transport 并设 `ResponseHeaderTimeout`（默认 90 秒，`WithStreamResponseHeaderTimeout` 可覆盖），覆盖"请求发出→等响应头"这个原本无超时的窗口——中台网关挂起时 turn 不会再永久卡死。
- 低收益循环检测（`3fce044b`，借鉴 MiMo-Code Try-Best 最小版）：per-turn 检测两类信号——`bash_retry`（归一化后同一命令连续失败且输出无变化，默认 3 次）与 `edit_streak`（连续编辑类调用无其他动作穿插，默认 4 次）。命中后在工具结果前注入重规划警示，**不终止 turn**；检测器随即重置。配置见 `agents.defaults.loop_detection`（`enabled` 未配置视为开启）。
- LLM 重试与摘要加固：`4c0640c8`（工具摘要 + light model + fail-fast 重试）、`af8975b2`（工具循环加固）。
- **Turn 韧性（429 风暴与 LLM 失败处理，2026-09-09，详见 `docs/design/turn-llm-failure-resilience.zh.md`）**：
  - **流式失败保卡承接（对齐 hermes-lark-streaming）**：ChatStream 出字前失败（429 等）不再封"已中断"卡，卡片保持存活，fallback 链的答案在 finalize 时写入同一张卡；纯文本消息只是卡片 finalize 也失败时的最后兜底。turn 内一次流式失败后 sticky 降级（后续迭代不再试流式）；流式首跳前检查主候选冷却，限流模型直接跳过。
  - **回合失败必通知**：fallback 链打穿、turn 以 error 结束时向用户发"⚠ 模型调用失败，本轮已中止：<原因>"，不再沉默；可见输出后的流式失败不重复通知（卡片已说明）。
  - **feishu 空卡删除**：从未展示内容的流式卡在取消时删消息而非封中断标记。
  - **Retry-After 尊重**：`HTTPError`/`FailoverError` 携带 429 响应的 `Retry-After`（解析上限 10 分钟），冷却取 `max(指数退避, hint)`——候选不会被早于服务器允许的时间重试。
  - **配额错误归 Billing**：`exceeded your current quota`/`quota exceeded`/`usage limit`/`insufficient_quota`/`out of budget` 等**账户级配额耗尽**从 RateLimit（1min 冷却反复撞）迁到 Billing（5h 起长冷却，快速 failover 换渠道）；`rpm/tpm exhausted` 等明确按分钟限流的保持 RateLimit；429 状态码 + 配额语义 body 时消息语义优先。
- **Windows exec 卡死与会话丢失三连修（2026-09-11，agent-browser 验证事故驱动）**：
  - **exec 层有界 io 等待**（`pkg/tools/shell.go` `runSync`）：`cmd.WaitDelay = execIOWaitDelay`（5s）+ 识别 `exec.ErrWaitDelay`——进程退出/被杀后输出管道最多再等 5 秒即放弃并返回已收集输出。消灭两处挂死：①`/stop` 取消（或超时击杀）后 `err = <-done` 无界等待（buffer stdout 下 `cmd.Wait` 要等管道 EOF，Windows 需所有继承写句柄的进程退出，taskkill /T 的 PPID 走查够不着孤儿）；②命令正常退出但长驻子进程（daemon/浏览器）持有管道导致 exec 假挂到子进程死——现以"成功 + 附注"及时返回。
  - **Windows Job Object 整树击杀**（`pkg/tools/shell_process_windows.go`）：启动后 `trackProcessTree` 把命令挂进 job（**不带** KILL_ON_JOB_CLOSE），`terminateProcessTree` 优先 `TerminateJobObject` 一次杀全树（不依赖 PPID 链），失败降级 taskkill；正常完成 `releaseProcessTree` 只关句柄，**有意存活的 daemon 不被误杀**。挂 job 失败为非致命（保留 taskkill 路径）。
  - **HardAbort 看门狗**（`pkg/agent/steering_abort.go` `watchHardAbortUnwind`）：abort 后宽限 `hardAbortUnwindGrace`（10s，包级 var）等 turn goroutine 自行收尾，超时强制 `releaseSessionTurnState` + zombie 日志——turn 卡死在任何工具/钩子上时会话不再永久 busy（此前唯一恢复手段是重启网关）。
  - **/stop 语义：回滚抹除 → 封口保留**：旧 HardAbort 回滚 `SetHistory(history[:initialHistoryLength])` 对新会话（起点 0）等于把 JSONL 整文件重写为空（实测 0 字节文件 + meta count=0，重启后"会话记录丢失"）；现 `sealDanglingToolCalls` 给末尾悬空 tool_calls 补合成结果（`abortedToolResultNote`），历史对下次请求有效且记录保留。`TestHardAbortSessionRollback`/`TestHardAbortOrderOfOperations` 已改为断言新语义。
  - **评审加固（同日）**：中止语义统一——`abortTurn` 同样走封口（旧 `restoreSession` 按 turn 前快照整段回滚，快照在用户消息落盘前捕获，**响应式 /stop（流式中按停止）依然清空新会话**）；看门狗强制释放时置 `zombieReleased`，迟到解退的 turn 跳过一切会话写入（防清掉新 turn 的记录）；ExecuteTools 全部工具消息落盘点（主结果/hook 供结果/deny/skip 共 8 处）加 hardAbort 守卫——封口后迟到的真实结果不再落盘，避免同 tool_call_id 出现双 tool 消息使下次请求 400；restore point 机制（captureRestorePoint/refreshRestorePointFromSession/restoreSession）整体删除。
  - 测试锚点：`TestShellTool_CancelReturnsDespiteOrphanedPipeHolder`、`TestShellTool_DaemonHoldingPipesReturnsPromptly`（tools，Windows-only）；`TestSealDanglingToolCalls`、`TestHardAbort_ForceReleasesWedgedTurnRegistration`、`TestRunTurn_HardAbortDuringLLMCall_PreservesHistory`、`TestZombieTurn_LateUnwindPreservesNewTurnHistory`、`TestRunTurn_HardAbortMidTool_LateResultNotDuplicated`、`TestAgentLoop_InterruptHard_SealsAndPreservesSession`（agent）。
- **首次安装配置模板兼容（2026-09-14）**：上游示例模板 `config.example.json` 自带 `_comment` 键和单数 `api_key` 字段，但结构体均不认识——首次安装照模板填配置即被严格加载拒绝（网关启动失败 + web 端 "Failed to load config"），宽松路径则静默丢弃密钥。fork 三连修：① 未知字段诊断白名单 `_comment`（`pkg/config/diagnostics.go` `collectUnknownJSONFields`，仅跳过不报错；decoder 本就忽略，下次保存自然消失，真实拼写错误仍拒收）；② brave/tavily/kagi/perplexity 的 `LegacyAPIKey`（`json:"api_key"`）字段 + 自定义 `UnmarshalJSON` 折叠进 `APIKeys`（`api_keys` 优先；SecureString `IsZero` 在 JSON 上下文恒真，序列化永不回写）；③ 模板清洗（删冗余单数 `api_key`，tavily 改 `api_keys`）。`TestExampleTemplateLoadsStrict` 钉死模板必须过严格加载，`TestWebSearchLegacyAPIKeyAliasFold` 钉住别名语义。

## 6. exec 安全加固（custom-only 拦截模式）

上游逻辑：`enable_deny_patterns=false` 时 `custom_deny_patterns` 完全不加载，自定义拦截规则形同虚设；而内置默认规则拦截面太大（`sudo`、`git push`、`kill`、`rm -rf`、`apt install` 等日常命令全部在列），不适合个人部署直接启用。

- 新增独立开关 `tools.exec.enable_custom_deny_patterns`（`9cc5a3b1`，默认 `false`，向后兼容）：`enable_deny_patterns=false` 且该开关为 `true` 时，**只加载** `custom_deny_patterns`，不加载内置默认规则。
- `enable_deny_patterns=true` 时行为与上游完全一致（默认 + 自定义都加载），新开关无额外作用。
- 测试锚点：`TestShellTool_CustomDenyOnly*`、`TestShellTool_CustomDenyInactive*`（同步上游后必须通过）。
- 设计与部署细节：`docs/security-exec-hardening.md`。

## 7. 卡片停止按钮（已移除渲染，回调保留）

流式卡片（初始卡与刷新卡）曾带「⏹ 停止」按钮，点击即停当前回合（`fc18f452`，2026-09-07；1:2 分栏收窄 `46ffec17`）。**2026-09-09 按用户要求移除按钮渲染**（视觉干扰）：`buildFeishuStreamingCard`/`buildFeishuRefreshCard` 不再输出按钮元素（两函数的 chatID 参数随之删除）。

- **回调 handler 保留**（`feishu_stream.go` `handleCardAction` + `feishuStopCmd`）：移除前已送达聊天的历史流式卡仍带按钮，点击后 allowlist 校验、/stop 合成、toast 反馈的链路照常工作；注释已注明渲染移除。
- 停止能力不受影响：`/stop` 命令与 turn 中止路径完整保留。
- 回调链路要点（schema 2.0 不支持旧 `action` 标签，错误码 200861；按钮必须是独立 `button` 元素 + `behaviors` 回调）如未来恢复按钮渲染仍是硬约束。
- 测试锚点：`TestStreamingCardsOmitStopButton`（钉住"不带按钮"）、`TestHandleCardActionStop*`（回调链路）。
- 后续候选：报告翻页。（危险命令「批准/拒绝」审批门禁曾于 `cf9731fc` 实现后按用户决定整体移除，含配置项 `tools.exec.approval_patterns`；如需恢复查该提交。）

## 8. Web launcher 主题（五套命名主题）

演变：`5d3ad431`..`24ee54f8`（2026-09-07）深空紫青科技风 → `30bc9da6` 暖琥珀极光改版 → `31ed68fc`（2026-09-13）**五套命名主题**（参考 metacubexd 的调色板切换模型，适配本仓库 shadcn/Tailwind v4 变量体系）：

- **机制**：`<html data-theme="...">` 属性驱动完整 shadcn 变量组（含 aurora 背景、chart 5 色、sidebar 全家）；深色主题同时保留 `.dark` 类，`dark:` 变体与 highlight.js 联动（`use-highlight-theme.ts` 读类名）不受影响。命名主题块声明在 `.dark` 之后，靠同特异性后者胜出完成覆盖。
- **主题清单**：`light` 浅色 / `dark` 深色（默认）/ `ocean` 海洋（青蓝深色）/ `forest` 森林（翠绿深色）/ `sakura` 樱花（粉调浅色）。
- **渐变变量化**：`.bg-primary` 与激活侧栏项渐变里的琥珀混入色抽为 `--primary-glow/deep/alt`，各主题独立覆盖，避免新主题主按钮残留琥珀渐变。
- **切换 UI**：`ThemeSwitcher` 组件（调色板按钮 + 色板圆点下拉），替换 `app-header` 与 launcher 登录/设置页的日/月切换按钮；localStorage `theme` 键不变，旧值 `light`/`dark` 仍是合法主题 id，存量用户无感。
- **防闪烁**：`index.html` 内联引导脚本在首帧前恢复存储的主题（id 集合与 `use-theme.ts` 保持同步，改主题清单时两处都要动）。
- i18n 五个 locale 的 `theme.*` 标签。

## 9. 运维禁令（AGENTS.md）

`23b1275d` 增补两条 2026-09-07 实测得出的禁令，agent 与维护者都需遵守：

- **禁止给 `picoclaw.service` 加任何 sandbox 指令**（RestrictAddressFamilies / ReadWritePaths / ProtectSystem 等）——user 服务里 systemd 会以受限上下文应用它们，setuid 提权被永久禁用，agent 的所有 `sudo` 会报 `sudo must be owned by uid 0 and have the setuid bit set`。unit 的 sandbox 段已全部移除，不要「加固」回去。
- **`picoclaw cron add/remove`（CLI）只写 jobs.json，运行中的网关不感知**（启动时才读）——改完必须重启服务；另外 cron 表达式必须 5 字段，残缺表达式（如 `45 16`）会被静默接受但永不匹配。

## 10. 技能目录扩展与项目文档注入

面向"把 workspace 当项目根"的个人部署形态，三项自动上下文增强（2026-09-07）：

- **技能根目录扩到五级**（`pkg/skills/loader.go`）：`<ws>/skills`（source=workspace）> `<ws>/.skills`（source=project，手工维护）> `~/.picoclaw/skills`（global）> `~/.agents/skills`（global，**跨工具约定目录**，跟随 `os.UserHomeDir` 而非 `PICOCLAW_HOME`）> 内置。`NewSkillsLoader` 签名不变（6 处调用方零改动），根目录派生收敛到 `skills.ResolveSkillRoots`；同名首见者胜。description 超 1024 字节截断而非丢弃技能（跨工具技能描述普遍很长）。identity 提示与提示缓存失效（`SkillRoots`）自动跟随。
- **技能根目录只读放行**（`pkg/agent/instance.go` `appendSkillRootReadPatterns`）：restrict 模式下工作区外的技能根自动加入 allow-read 前缀模式（复用 media 临时目录的写法），模型能 `read_file` 目录给出的 `SKILL.md` 路径；只放开读。
- **对齐 Agent Skills 规范/pi 的两点**（参考 earendil-works/pi `packages/coding-agent/src/core/skills.ts`）：① frontmatter `disable-model-invocation: true` 的技能照常加载（`/use` 显式调用可用）但不进模型可见目录（`BuildSkillsSummary` 与 allow-list 两条路径都过滤）；② 技能提示加一句"技能内相对路径按技能目录（SKILL.md 的父目录）解析为绝对路径"，解决 `references/` 子文件读不到的问题。
- **项目文档注入**（`pkg/agent/project_docs.go`）：`agents.defaults.project_docs`（默认 `AGENTS.md/README.md/CLAUDE.md`，空数组关闭）列出的工作区根目录文档注入系统提示词——新槽位 `project_docs`（优先级 890，紧跟 workspace 900）、来源 `workspace.project_docs`。单文件 6000B / 整段 12000B 截断；只认裸文件名，bootstrap 文件（AGENT/SOUL/USER/IDENTITY.md）排除；**AGENT.md 缺失时 AGENTS.md 已是旧版 agent 定义，跳过防重复注入**；路径进 `sourcePaths()` 走 mtime 热失效。经 `NewContextBuilder(...).WithProjectDocs(...)` 接线。
- 测试锚点：`TestListSkillsProjectDotSkillsDir`、`TestListSkillsHomeDotAgentsDir`、`TestListSkillsClampsLongDescription`、`TestSkillRootsTrimsWhitespaceAndDedups`（更新）、`TestProjectDocs*`、`TestAppendSkillRootReadPatternsAllowsOutsideRoots`；`pkg/skills` 与 `web/backend/api` 测试用 `TestMain`/setup helper 隔离 HOME+USERPROFILE，防止开发者真实的 `~/.agents/skills` 泄入断言。
- 配置文档：`docs/guides/configuration.zh.md` 的"技能来源"与"项目文档注入"两节。

## 11. cron 增强 + 自动化建议 + /learn（2026-09-13，借鉴 hermes-agent 第二/三梯队）

对应调研文档：`docs/design/hermes-borrowing-analysis.zh.md`（第一梯队=evolution 打开；本节=第二/三梯队落地）。四块能力全部可选、不影响上游默认行为：

- **cron 热重载**：`CronService.runLoop` 每次迭代探测 jobs.json mtime（`reloadStoreIfChanged`），外部 CLI 写入自动生效；sleep 上限 `storePollInterval`（包级 var，默认 1 分钟）。配套修复：`checkJobs` 只在有 due job 时落盘（原实现每 tick 无条件 save，既烧 flash 又会在高频轮询下用旧内存态覆盖外部编辑——Windows 下 rename 竞争实测复现）。`loadStore`/`saveStoreUnsafe` 维护 `storeMod` 防自身写入误触发。
- **表达式校验**：`cron.ValidateSchedule`（包级函数）在 `AddJob`/`UpdateJob`（schedule 变更时才校验，存量坏 job 不被无关改名押持）拒绝残缺 cron 表达式/过去时间/非法 kind；工具与 CLI 两条路径同时受益。`UpdateJob` 仅在 schedule 实际变化时校验，防存量坏表达式押持无关更新。
- **唤醒门 + 蓝图 + 建议**（`pkg/tools/cron.go`）：payload 新增 `script`（预执行脚本 + `wakeAgent` 唤醒门，fail-open，解析在 `parseWakeGate`；执行复用 exec 工具，与 `command` 同一套 GHSA 通道安全约束）；`action=blueprints` 与 `blueprint`/`blueprint_values` 参数（目录在 `pkg/cron/blueprints.go`，槽位校验+模板展开，让用户永不写 cron 表达式）；`action=suggestions`/`accept_suggestion`/`dismiss_suggestion`（存储在 `pkg/cron/suggestions.go`：pending 上限 5、总上限 100、dedup key 永久门锁、atomic 0600）。**consent-first：建议绝不自动建任务**。
- **evolution → 建议**：`Runtime.CronSuggester`（可选接口）挂入冷路径 pattern 聚类后，`LLMCronSuggester` 对合格模式（EventCount ≥ min_task_count 且成功率达标、有摘要）逐个提议；单个失败不断链。配置门 `evolution.suggestions_enabled`（`*bool`，nil=mode≥draft 开启）。bridge 按 workspace 惰性解析 suggestion store（`cron.NewSuggestionManager(filepath.Join(workspace,"cron","jobs.json"))`）。
- **/cron 命令**（`pkg/commands/cmd_cron.go`）：list/suggest/accept/dismiss/blueprint 子命令；Runtime 新增 `CronJobs`/`CronSuggestions`/`AcceptCronSuggestion`/`DismissCronSuggestion` 回调，`agent_command.go` 经 `cronToolFromRegistry`（agent.Tools 找 `*tools.CronTool`）接线；工具未启用时优雅降级为 unavailableMsg。接受的任务绑定到接受发生的通道。
- **/learn 命令**（`pkg/commands/cmd_learn.go` + `agent_command.go` `applyLearnCommand`）：仿 `/use` 在 registry 之前拦截，把 `/learn <来源>` 改写为完整技能编写回合（`BuildLearnPrompt`：CHECK FIRST/来源逐字原则/hardline 章节规范/Verification），落盘路径 `<workspace>/skills/<name>/SKILL.md`；纯提示词改写，无新工具面。
- 测试锚点：`pkg/cron/hot_reload_test.go`、`pkg/cron/suggestions_test.go`、`pkg/tools/cron_wakegate_test.go`、`pkg/tools/cron_suggestions_test.go`、`pkg/evolution/cron_suggester_test.go`、`pkg/commands/cmd_cron_test.go`、`pkg/agent/learn_command_test.go`、`config_test.go` `TestEvolutionConfig_EffectiveSuggestionsEnabled`。
- 配置文档：`docs/guides/configuration.zh.md` 定时任务/evolution 章节。


| 场景 | 上游 | 本 fork |
|---|---|---|
| 飞书回复 | 文本消息 | CardKit 流式卡片（过程面板时间线交错 + 工具运行中条目 + 标题实际总数 + 面板默认折叠 + 中途说明灰字轨迹） |
| `/new` | 无此命令 | 归档旧对话（历史/标题迁入归档会话）+ 清空活会话 + 重置为配置默认模型（重读磁盘） |
| `/switch model`（turn 活跃时） | 阻塞等待 | 立即返回 busy 提示 |
| 流式 LLM 请求 | 无响应头超时 | 90 秒响应头超时后快速失败 |
| 流式默认值 | 双开关默认关（省略=关） | 模型侧 `*bool` **省略=开**；安装默认 pico+feishu 渠道出厂开（`TestModelStreamingConfigDefaultOn`、"model omitted still streams" 钉住） |
| 工作区沙箱 | 默认 `restrict_to_workspace: true` | 默认 `false`——任意目录读写 + 一般命令脚本可执行；以 `tools.protect_system_paths`（nil=开）拒 OS 系统目录读写，exec 仅拦毁灭性命令与系统目录写入（`TestSystemPathProtection`、`TestOpenByDefaultSandboxDefaults` 钉住） |
| 命令输出（inline） | 尾部截断 | 清理管线 + 尾部截断，原文照旧落盘 |
| 死循环防护 | 仅 MaxToolIterations | 迭代上限 + 低收益检测注入警示 |
| 记忆 | workspace 文件 | 附加 OpenViking recall/commit（可配置关闭） |
| exec deny（`enable_deny_patterns=false`） | `custom_deny_patterns` 一并失效 | 新增 `enable_custom_deny_patterns`，可只加载自定义规则 |
| 卡片交互 | 仅消息文本 | 流式卡「停止」按钮已移除（2026-09-09）；回调 handler 保留兼容历史卡片 |
| 长无写入期间流式卡超时（200850） | 元素写入持续报错 | 自动重开，失败降级全卡更新，turn 不中断 |
| LLM 429（出字前流式失败） | 封"已中断"卡 + fallback 答案降级为纯文本消息 | 卡片保持存活承接 fallback 答案（一张卡到底）；sticky 降级 + 冷却门控防反复开卡 |
| 回合失败（链打穿） | 静默（仅内部事件） | 向用户发明确报错"⚠ 模型调用失败，本轮已中止：<原因>" |
| 429 Retry-After 头 | 忽略 | 解析并作为冷却下限（上限 10 分钟），不早于服务器允许的时间重试 |
| 账户配额耗尽（quota/usage limit） | RateLimit（1min 冷却反复撞死配额） | Billing（5h 起长冷却，快速 failover 换渠道）；`rpm/tpm exhausted` 仍按 RateLimit |
| 技能来源 | 3 级（workspace/global/builtin） | 5 级（+`<ws>/.skills`、`~/.agents/skills`），restrict 下技能根只读放行 |
| 项目文档 | 无（README/CLAUDE.md 完全忽略） | `project_docs` 自动注入（AGENTS.md/README.md/CLAUDE.md，截断保护） |
| 会话标题 | 无（launcher 列表显示首条消息截断） | 两阶段自动命名（派生→轻模型升级）+ `/title` 手动，user>llm>derived 优先级 |
| Web 会话列表 | 仅 pico（web 聊天）会话 | 全渠道会话（带 `channel` 徽标，如 feishu）；非 pico 会话只读查看（输入框禁用 `nonPicoSession`），删除跨渠道放行（`web/backend/api/session.go`） |
| `/stop` 中止的会话历史 | 回滚到 turn 前（新会话=整文件清空） | 封口悬空 tool_calls 并保留记录；turn 卡死 10s 后看门狗强制释放会话注册 |
| exec 子进程击杀（Windows） | taskkill /T（孤儿逃逸→管道挂死→会话卡死） | Job Object 整树击杀 + 5s WaitDelay 有界 io 等待；干净退出的存活 daemon 不误杀 |
| Web launcher 外观 | 上游默认主题 | 五套命名主题（`data-theme` + `.dark` 联动，见 §8；`index.css`/`use-theme.ts`/`theme-switcher.tsx`/`index.html` 升级时留意冲突） |
| systemd 部署 | 官方 unit | 禁 sandbox 指令（见 §9），unit 变更时不得带回 |
| cron 外部编辑感知 | 启动时读一次 jobs.json，CLI 改动需重启 | 运行循环每分钟 mtime 探测自动重载（§11），且仅在有 due job 时落盘 |
| cron 残缺表达式 | 静默接受，永不匹配 | `ValidateSchedule` 在 AddJob/UpdateJob 立即拒绝（AGENTS.md 运维禁令已相应改写） |
| 定时任务预检查 | 每次触发都烧完整 LLM 回合 | payload `script` 唤醒门：脚本输出 `{"wakeAgent": false}` 整体跳过（借鉴 hermes wakeAgent 门） |
| 自动化建议 | 无 | evolution 重复模式 → suggestions.json 提案 → `/cron accept` 显式接受（consent-first，借鉴 hermes suggestions） |
| 技能固化 | 无（只有 hub 安装） | `/learn <来源>` 把做过的事/文档改写为技能编写回合（借鉴 hermes /learn） |

## 同步上游注意事项

- 主要冲突面：`pkg/commands/`、`pkg/agent/pipeline_execute.go`、`pkg/agent/agent_command.go`（/learn 拦截 + /cron Runtime 回调）、`pkg/tools/shell.go`、`pkg/tools/cron.go`（§11 script/蓝图/建议动作）、`pkg/channels/feishu/`、`pkg/config/config.go`（AgentDefaults + EvolutionConfig.SuggestionsEnabled）、`pkg/skills/loader.go`（§10 五级根目录）、`web/frontend/src/index.css`（§8 主题）、`web/frontend/src/hooks/use-theme.ts`、`web/frontend/src/components/theme-switcher.tsx`、`web/frontend/index.html`（防闪烁脚本）。
- `pkg/providers/openai_compat/provider.go` 的流式超时如与上游改动冲突，保留 `streamRoundTripper` 语义优先。
- `pkg/config/config.go` 的 `ModelStreamingConfig.Enabled` 是 `*bool`（nil=开启，fork 默认开流式）；上游若改回值 bool，同步时保留 `*bool` + `EffectiveEnabled()` 语义，消费点走 `EffectiveEnabled()` 而非直接读字段。`defaults.go` 里 feishu 渠道出厂带 `streaming.enabled: true`。
- 开放默认三件套（不要"加固"回去）：`restrict_to_workspace` 默认 `false`；`pkg/tools/fs/system_paths.go` 的系统目录保护（`tools.protect_system_paths` nil=开，校验入口在 `validatePathWithAllowPaths` 最前）；`defaultDenyPatterns` 为毁灭性+系统目录写入集（一般命令/脚本/$()/管道/heredoc 放行，windowsDenyPatterns 已删除）。同步上游时若上游改动这三处，保留 fork 语义优先。
- exec 卡死三连修（2026-09-11）：`pkg/tools/shell.go` 的 `execIOWaitDelay`/`ErrWaitDelay` 处理、`pkg/tools/shell_process_windows.go` 的 Job Object 击杀（`trackProcessTree`/`terminateProcessTree`）、`pkg/tools/output_clean.go` 的 CRLF 折叠修复、`pkg/agent/steering_abort.go` 的封口（`sealDanglingToolCalls`）+ 看门狗（`watchHardAbortUnwind`）、`pkg/agent/steering.go` HardAbort 的"封口不抹除"——均为 fork 行为，上游同步时保留 fork 语义；`subturn_test.go` 的 `TestHardAbortSessionRollback`/`TestHardAbortOrderOfOperations` 断言的是封口语义，不要按上游回滚语义"修"回去。
- Turn 韧性（2026-09-09，详见 `docs/design/turn-llm-failure-resilience.zh.md`）：`pkg/agent/pipeline_streaming.go` 的出字前失败**保卡承接 + sticky 降级 + 冷却门控**、`pkg/agent/pipeline_finalize.go` 的纯文本兜底条件、`pkg/agent/agent.go` 的 `publishTurnError`、`pkg/providers/error_classifier.go` 的**配额→Billing 模式迁移与 429+配额 body 判定**、`pkg/providers/cooldown.go` 的 `MarkFailureWithHint`、`pkg/providers/common/common.go` 的 `HTTPError.RetryAfter`——均为 fork 行为，上游同步时保留 fork 语义。turn 重试边界是结构性保证（无预算机制），不要重新引入"失败计数预算"类加固。
- cron 增强三件套（2026-09-13，§11）：`pkg/cron/service.go` 的 mtime 热重载（`reloadStoreIfChanged`/`storePollInterval`）+ due-job-only 落盘 + `ValidateSchedule`、`pkg/cron/suggestions.go`（consent-first 提案存储）、`pkg/cron/blueprints.go`、`pkg/tools/cron.go` 的 `script` 唤醒门与 `suggestions`/`blueprints` 动作、`pkg/evolution/cron_suggester.go` + `Runtime.SetCronSuggester`、`pkg/commands/cmd_cron.go`/`cmd_learn.go`、`pkg/agent/agent_command.go` 的 `applyLearnCommand`——均为 fork 行为，上游同步时保留 fork 语义；`hot_reload_test.go`/`cron_wakegate_test.go`/`cron_suggestions_test.go`/`learn_command_test.go` 钉住行为。
- 合并后跑 `go test ./pkg/agent/ ./pkg/tools/ ./pkg/providers/... ./pkg/commands/ ./pkg/cron/ ./pkg/evolution/` 验证 fork 测试（文件名含 `_test.go` 且测试名带 `NewResets`/`NewArchives`/`NeverBlocks`/`ResponseHeaderTimeout`/`CleanCommandOutput`/`ReloadsStore`/`WakeGate`/`ApplyLearn`/`Suggest` 的均为 fork 独有）；前端改动需另跑 `pnpm build` 验证。
- 配置模板兼容（2026-09-14）是 fork 对上游缺陷的修复：上游结构与模板均未改。同步上游时若 `config.example.json` 被上游改动，同步后必须保证 `TestExampleTemplateLoadsStrict` 仍过（模板不得引入结构体不认识的字段，`_comment` 除外）；`pkg/config/diagnostics.go` 的 `_comment` 白名单与四 provider 的 `LegacyAPIKey` 折叠保留 fork 语义，不要按上游"修"掉。
