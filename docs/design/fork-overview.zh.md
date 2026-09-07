# Fork 功能地图（dddpeter/picoclaw）

> 本仓库 fork 自 [sipeed/picoclaw](https://github.com/sipeed/picoclaw)，在保留上游全部能力的基础上做了面向个人部署（飞书 IM + 中台网关模型 + systemd 用户服务）的定向增强。上游通过 `upstream` remote 跟踪，合并上游时本文档列出的文件是主要冲突面。
>
> 维护日期：2026-09-07（对应提交 `9cc5a3b1` 附近的 fork 状态，落后上游 33 个自有提交）

## 功能块总览

| 功能块 | 起始提交 | 核心代码 | 文档 |
|---|---|---|---|
| 飞书 CardKit 流式卡片 | `9a99dcd4` | `pkg/channels/feishu/` | 本文 §1 |
| OpenViking 共享记忆 | `87850045` / `06d3aab5` | `pkg/agent/memory_*.go` | `docs/guides/openviking.md` |
| 命令增强（/new、/status） | `125b3287` | `pkg/commands/` | 本文 §3 |
| 工具输出处理 | `a5b71f89` / `3fce044b` | `pkg/tools/truncate.go`、`pkg/tools/output_clean.go` | 本文 §4 |
| 可靠性加固 | `9bab2b25`…`3fce044b` | `pkg/agent/turn_health.go` 等 | 本文 §5 |
| exec 安全加固 | `9cc5a3b1` | `pkg/tools/shell.go`、`pkg/config/config.go` | 本文 §6、`docs/security-exec-hardening.md` |

## 1. 飞书 CardKit v2 流式卡片

用飞书卡片（CardKit v2）承载 agent 回复的流式渲染，替代纯文本回复：

- 过程面板：工具调用步骤、嵌套推理轮次（reasoning rounds）、kind 着色图标、动态状态行、面板自动折叠；工具输出以行内代码渲染以控制卡片体积。
- 打字机效果跨面板刷新保持存活；中止/取消时显示原因（cancel reason）与 steering 通知；turn 在 LLM 调用中被中止时流式卡片保持可达。
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

## 与上游的行为差异速查

| 场景 | 上游 | 本 fork |
|---|---|---|
| 飞书回复 | 文本消息 | CardKit 流式卡片（含过程面板） |
| `/new` | 无此命令 | 清历史 + 重置为配置默认模型（重读磁盘） |
| `/switch model`（turn 活跃时） | 阻塞等待 | 立即返回 busy 提示 |
| 流式 LLM 请求 | 无响应头超时 | 90 秒响应头超时后快速失败 |
| 命令输出（inline） | 尾部截断 | 清理管线 + 尾部截断，原文照旧落盘 |
| 死循环防护 | 仅 MaxToolIterations | 迭代上限 + 低收益检测注入警示 |
| 记忆 | workspace 文件 | 附加 OpenViking recall/commit（可配置关闭） |
| exec deny（`enable_deny_patterns=false`） | `custom_deny_patterns` 一并失效 | 新增 `enable_custom_deny_patterns`，可只加载自定义规则 |

## 同步上游注意事项

- 主要冲突面：`pkg/commands/`、`pkg/agent/pipeline_execute.go`、`pkg/tools/shell.go`、`pkg/channels/feishu/`、`pkg/config/config.go`（AgentDefaults）。
- `pkg/providers/openai_compat/provider.go` 的流式超时如与上游改动冲突，保留 `streamRoundTripper` 语义优先。
- 合并后跑 `go test ./pkg/agent/ ./pkg/tools/ ./pkg/providers/... ./pkg/commands/` 验证 fork 测试（文件名含 `_test.go` 且测试名带 `NewResets`/`NeverBlocks`/`ResponseHeaderTimeout`/`CleanCommandOutput` 的均为 fork 独有）。
