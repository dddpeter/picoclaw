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
| 卡片停止按钮（CardKit 回调） | `fc18f452` | `pkg/channels/feishu/` | 本文 §7 |
| Web launcher 主题 | `5d3ad431` | `web/frontend/src/index.css` | 本文 §8 |
| 运维禁令 | `23b1275d` | `AGENTS.md` | 本文 §9 |

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

- `/new`（`125b3287` + `9bab2b25` + `d349a638`）：开新会话——清历史，并把模型重置为**配置文件里当前定义的默认模型**（重新读取磁盘配置，优先级 AGENT.md frontmatter > agents.list > defaults）。回复中携带当前模型名。
- `/status`（`125b3287`）：运行状态总览（版本、模型、通道、活跃任务）。
- **Busy 语义**（`d349a638` / `03471ee6`）：`/switch model` 与 `/new` 的模型重置在 turn 活跃期间**不排队等待**，通过 TryLock 立即返回 skipped 提示。设计动机：turn 全程持有模型状态读锁，阻塞式写锁会在 LLM 调用挂起时死锁命令。
- 未促使内容处理（`8e077180`）：agent 对未被提示的内容做实质性分析而非简单确认。

## 4. 工具输出处理（inline 送模型路径）

- 结构化 head/tail 截断（`a5b71f89`）：超限输出保头保尾并附明确提示，完整原文落盘供模型回读。
- inline 清理管线（`3fce044b`，借鉴 MiMo-Code token-efficient 设计）：进度条折叠（`\r` 重绘只留末帧）→ ANSI 转义剥离 → 超长行压缩（>500 字符压为头部 160 字符 + 省略提示）；never-worse 守门（清理不缩小即回吐原文）；命令含 `--json` / `-o json` / `| tee` / `# nofilter` 时整路放行。**落盘与截断提示措辞不受影响**。
- 背景调研与取舍：`docs/design/mimo-code-borrowing-analysis.zh.md`。

## 5. 可靠性加固

- 流式响应头超时（`03471ee6`）：ChatStream 使用克隆 Transport 并设 `ResponseHeaderTimeout`（默认 90 秒，`WithStreamResponseHeaderTimeout` 可覆盖），覆盖"请求发出→等响应头"这个原本无超时的窗口——中台网关挂起时 turn 不会再永久卡死。
- 低收益循环检测（`3fce044b`，借鉴 MiMo-Code Try-Best 最小版）：per-turn 检测两类信号——`bash_retry`（归一化后同一命令连续失败且输出无变化，默认 3 次）与 `edit_streak`（连续编辑类调用无其他动作穿插，默认 4 次）。命中后在工具结果前注入重规划警示，**不终止 turn**；检测器随即重置。配置见 `agents.defaults.loop_detection`（`enabled` 未配置视为开启）。
- LLM 重试与摘要加固：`4c0640c8`（工具摘要 + light model + fail-fast 重试）、`af8975b2`（工具循环加固）。

## 6. exec 安全加固（custom-only 拦截模式）

上游逻辑：`enable_deny_patterns=false` 时 `custom_deny_patterns` 完全不加载，自定义拦截规则形同虚设；而内置默认规则拦截面太大（`sudo`、`git push`、`kill`、`rm -rf`、`apt install` 等日常命令全部在列），不适合个人部署直接启用。

- 新增独立开关 `tools.exec.enable_custom_deny_patterns`（`9cc5a3b1`，默认 `false`，向后兼容）：`enable_deny_patterns=false` 且该开关为 `true` 时，**只加载** `custom_deny_patterns`，不加载内置默认规则。
- `enable_deny_patterns=true` 时行为与上游完全一致（默认 + 自定义都加载），新开关无额外作用。
- 测试锚点：`TestShellTool_CustomDenyOnly*`、`TestShellTool_CustomDenyInactive*`（同步上游后必须通过）。
- 设计与部署细节：`docs/security-exec-hardening.md`。

## 7. 卡片停止按钮（card.action.trigger 回调）

流式卡片（初始卡与刷新卡）带「⏹ 停止」按钮，点击即停当前回合——替代打字 `/stop`（`fc18f452`，2026-09-07）。

- **回调链路**：ws 长连接可收 `card.action.trigger`（spike 实测三次点击均到达，SDK v3.9.4 走 `message_type=event` 路径）；**schema 2.0 不支持旧 `action` 标签（错误码 200861），按钮必须是独立 `button` 元素 + `behaviors` 回调**——这是硬约束，上游同步或改造时不要改回 action 写法。
- **上下文嵌入**：回调事件不含 chat 上下文，按钮渲染时把 `chat_id` 嵌进 callback value；handler 校验 allowlist + 该 chat 存在未封口流式卡后，合成 `/stop` 入站消息走既有命令管道（确认回复、卡片「⚠ 已中断 · 用户停止」封口全部复用）。
- 测试锚点：`TestStreamingCardsCarryStopButtonWithChatContext`、`TestHandleCardActionStop*`。
- 停止按钮经 1:2 分栏收窄到约 1/3 行宽（`46ffec17`；schema 2.0 按钮是块级元素，裸按钮满行宽易误点）。
- 后续候选：报告翻页。（危险命令「批准/拒绝」审批门禁曾于 `cf9731fc` 实现后按用户决定整体移除，含配置项 `tools.exec.approval_patterns`；如需恢复查该提交。）

## 8. Web launcher 主题（深空紫青科技风）

`5d3ad431`..`24ee54f8`（2026-09-07）。按「全局改一处」原则，**只重写 `web/frontend/src/index.css` 的主题变量与全局特效层，不触碰任何组件**：

- 配色：AI 紫 `#7C3AED`（oklch 0.541/0.606）主色贯穿明暗两态，边框、焦点环、选中态统一带紫；图表 5 色换成紫→青→品红→蓝→teal 渐变族。
- 氛围背景：body 固定三层极光径向光晕（暗态 40%/32%/28% 透明度）+ 44px 网格线，纯 CSS 零开销。
- 玻璃拟态：卡片磨砂 + 悬浮上浮 + 紫色辉光 hover；侧栏与顶栏半透明 + backdrop-blur。
- 对比度三连修（`9ad61051`→`24ee54f8`）：卡片不透明度提到 88%、再提亮至 96% 不透明 + 紫色描边，最终侧栏/顶栏/弹窗/输入框/toast 全部收敛进统一的「面板体系」，避免背景极光吃掉前景内容。
- 附带 `a42971e8`：修复 pnpm-lock.yaml 重复键。

## 9. 运维禁令（AGENTS.md）

`23b1275d` 增补两条 2026-09-07 实测得出的禁令，agent 与维护者都需遵守：

- **禁止给 `picoclaw.service` 加任何 sandbox 指令**（RestrictAddressFamilies / ReadWritePaths / ProtectSystem 等）——user 服务里 systemd 会以受限上下文应用它们，setuid 提权被永久禁用，agent 的所有 `sudo` 会报 `sudo must be owned by uid 0 and have the setuid bit set`。unit 的 sandbox 段已全部移除，不要「加固」回去。
- **`picoclaw cron add/remove`（CLI）只写 jobs.json，运行中的网关不感知**（启动时才读）——改完必须重启服务；另外 cron 表达式必须 5 字段，残缺表达式（如 `45 16`）会被静默接受但永不匹配。



| 场景 | 上游 | 本 fork |
|---|---|---|
| 飞书回复 | 文本消息 | CardKit 流式卡片（过程面板时间线交错 + 工具运行中条目 + 标题实际总数 + 面板默认折叠 + 中途说明灰字轨迹） |
| `/new` | 无此命令 | 清历史 + 重置为配置默认模型（重读磁盘） |
| `/switch model`（turn 活跃时） | 阻塞等待 | 立即返回 busy 提示 |
| 流式 LLM 请求 | 无响应头超时 | 90 秒响应头超时后快速失败 |
| 命令输出（inline） | 尾部截断 | 清理管线 + 尾部截断，原文照旧落盘 |
| 死循环防护 | 仅 MaxToolIterations | 迭代上限 + 低收益检测注入警示 |
| 记忆 | workspace 文件 | 附加 OpenViking recall/commit（可配置关闭） |
| exec deny（`enable_deny_patterns=false`） | `custom_deny_patterns` 一并失效 | 新增 `enable_custom_deny_patterns`，可只加载自定义规则 |
| 卡片交互 | 仅消息文本 | 流式卡「停止」按钮（card.action.trigger 回调，1/3 行宽） |
| 长无写入期间流式卡超时（200850） | 元素写入持续报错 | 自动重开，失败降级全卡更新，turn 不中断 |
| Web launcher 外观 | 上游默认主题 | 深空紫青主题（仅改 index.css，升级时留意该文件冲突） |
| systemd 部署 | 官方 unit | 禁 sandbox 指令（见 §9），unit 变更时不得带回 |

## 同步上游注意事项

- 主要冲突面：`pkg/commands/`、`pkg/agent/pipeline_execute.go`、`pkg/tools/shell.go`、`pkg/channels/feishu/`、`pkg/config/config.go`（AgentDefaults）、`web/frontend/src/index.css`（§8 主题）。
- `pkg/providers/openai_compat/provider.go` 的流式超时如与上游改动冲突，保留 `streamRoundTripper` 语义优先。
- 合并后跑 `go test ./pkg/agent/ ./pkg/tools/ ./pkg/providers/... ./pkg/commands/` 验证 fork 测试（文件名含 `_test.go` 且测试名带 `NewResets`/`NeverBlocks`/`ResponseHeaderTimeout`/`CleanCommandOutput` 的均为 fork 独有）；前端改动需另跑 `pnpm build` 验证。
