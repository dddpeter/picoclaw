# Changelog

本项目的所有显著变更都会记录在此文件。

格式基于 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/)，
版本号遵循 [Semantic Versioning](https://semver.org/lang/zh-CN/)。

## [Unreleased]

### 2026-10-23 评审修复轮（五组并行评审 + 全量核实）

**安全**
- API key 掩码无条件执行：`--json`/`| tee`/`# nofilter` bypass 不再豁免掩码（此前 `cat .env # nofilter` 可把明文 key 送进模型上下文）；`sk-` 模式补 `\b` 边界（`--task-…`/`desk-…` 误掩消除）；格式库新增 `ghp_` 与 `xoxb-`
- 后台 shell 会话 read 出口（executeRead）纳入输出清理管线（此前既无 ANSI 清洗也无掩码）
- kill deny 第三规则改为目标任意位置匹配：`kill 5 -1 123`、`kill -s segv -1 789`、`bash -c 'kill 123 -1'`、`( kill 123 -1 )`、目标后换行等旁路全部闭合；deny 命中文案接入 declined-勿重试语义
- `guardCommand` 热路径的正则提为包级变量

**并发（-race 必红修复）**
- `runAgentLoop` 终答出口与 `PublishResponseIfNeeded`（steering/错误/心跳）改走 `snapshotContextUsage` 读锁快照——runTurn 的 RLock 返回即释放，裸读 `ContextWindow/MaxTokens` 与 `/switch` 写侧竞争
- legacy 摘要 goroutine（retryLLMCall）、`/status` 的 Candidates 读、`scheduleCompact` 的默认 agent 门控（改按归属 agent + CompactionBudget 统一预算基准）、压缩 goroutine 的预算执行期重取

**审批/问询**
- 审批超时竞态闭环（照抄 ask_user 的 mu+resolved 先退役后打捞）：超时回执发布窗口内迟到 `/approve` 不再能注册 allow_all/always 档位或伪造已批准回执
- 空 ask_patterns（无 ask/hard-deny/inherit）时泵侧行为与未启用完全一致：`同意/批准/拒绝` 不再被孤儿吞并
- ask_user 群聊答案钉住提问用户（octop §三-4）：其他成员闲聊不再被当作 The user answered
- 单题多行答案保留全文、多题余行折叠进末题（不再静默丢弃）
- 飞书审批卡过期后点击不再合成 /approve、封卡改为「⏰ 审批已结束」——此前卡片谎报已批准
- 热重载 hook 初始化失败回滚到上一份可用配置（此前仅 warn，审批层静默消失 fail-open）
- web 审批卡 allow_all 与 always 封存态区分（新 verdict + 五语言文案）
- 新增 `/approve off`（撤销放行）：随时清空本会话 allow_all 档与 always 规则，不再只能等 6h TTL
- 审批群聊答案钉住触发用户（与 ask_user 同策）：其他群成员不能再批准别人的危险操作

**上下文计量**
- 分段计量口径对齐实际出站请求（缓存每会话最后一次组装的工具集分段）：渐进加载未启用进 LLM 的 MCP 目录不再全额计入 used，水位门不再提前误判
- skills 段计入 agents.*.skills 持久注入（此前恒为 0）
- `/compact` 空闲分支移入 scheduler goroutine + 60s 有界等待：worker 槽与会话占位符不再被 3 分钟同步压缩占住；引擎失败独立 unavailable 档（不再折叠成 busy）
- tokenizer JSON 估算改 rune 口径（CJK/西里尔不再被 2-3 倍高估）；isCJK 补全角标点与全角形式区间

**其它**
- openai_compat 流式 usage 字段宽松二次解码：网关把展示型 usage 字段打成 `[]`/字符串不再丢弃整回合答案
- 会话迁移器跳过 ask/approval pending 标记（此前崩溃残留被当遗留会话迁出幽灵 `<key>.ask` 会话且永久破坏 pending 恢复）
- 渐进工具加载：`/new` 清空会话 loaded 集、每会话 64 上限、tool_search 限定未加载 MCP 工具（不再污染 builtins）、query 截断、拦截结果过输出预算、目录按 turn 缓存
- 前端：回合开始清上一回合用量残留；无 usage 收尾的回合不再显示旧数据；ask 单题多选支持草稿多选提交
- per-model context_window 条目解析失败改为显式告警（此前静默跳过 cap）

**文档**：审批默认启用的升级冲击与关闭方法、迟到 /approve 实际行为、`tools.mcp.deferred_tool_threshold` 配置节、drain-first 术语更正（retire-first-then-drain）、显示窗口的 per-model cap 例外。

### Added
- web 控制台五套主题切换（参考 metacubexd 调色板模型）：浅色/深色（默认）/海洋/森林/樱花，`data-theme` + 完整 shadcn 变量组驱动，深色主题保留 `.dark` 类兼容 `dark:` 变体与代码高亮联动；调色板下拉（色板圆点预览）替换原日/月切换按钮；localStorage `theme` 键不变，旧值 `light`/`dark` 仍合法；`index.html` 防闪烁引导脚本首帧前恢复主题

### Fixed
- 首次安装按示例模板填写配置即报 "config.json contains unknown field(s)"：① 未知字段诊断白名单 `_comment` 键（注释约定， decoder 本就忽略、下次保存自然消失，真实拼写错误仍拒收）；② brave/tavily/kagi/perplexity 四个搜索 provider 兼容弃用的单数 `api_key`（仅加载期折叠进 `api_keys`，`api_keys` 优先，序列化永不回写）；③ `config/config.example.json` 模板清洗（删除冗余单数 `api_key` 行，tavily 改用 `api_keys`），新增 `TestExampleTemplateLoadsStrict` 钉死模板必须通过严格加载

## [0.4.0] - 2026-09-11

### Added
- exec 子进程 Windows Job Object 整树击杀：启动后挂 job，终止优先 TerminateJobObject（不依赖 taskkill 的 PPID 走查），失败降级 taskkill；不带 KILL_ON_JOB_CLOSE，正常完成时有意存活的 daemon 不被误杀
- /stop 中止语义：会话历史封口保留（给悬空 tool_calls 补合成结果），不再回滚抹除——新会话首 turn 被 /stop 后记录不再丢失
- HardAbort 看门狗：turn goroutine 卡死（如阻塞在忽略取消的 I/O 上）10s 宽限后强制释放会话注册，会话不再永久 busy（此前唯一恢复手段是重启网关）
- 安装包版本注入（iscc /DMyAppVersion=<ver>）与产物命名 PicoClawSetup-<version>.exe

### Fixed
- exec 输出等待有界化（cmd.WaitDelay 5s + exec.ErrWaitDelay 识别）：/stop 或超时击杀进程后不再永久阻塞在管道 EOF 等待；命令正常退出但 daemon 持有输出管道时立即以成功+附注返回（此前 exec 假挂到 daemon 退出，agent-browser 场景"卡住只能重启"的根源）
- 响应式 /stop（LLM 流式中按停止）不再清空新会话历史：abortTurn 与 HardAbort 统一封口语义，删除 restore point 回滚机制（快照在用户消息落盘前捕获，回滚=清空）
- 僵尸 turn 迟到解退不再清掉新 turn 的记录（zombieReleased 守卫）；封口后迟到的真实工具结果不再落盘（8 处工具消息落盘点统一守卫，杜绝同 tool_call_id 双 tool 消息）
- 输出清理管线 CRLF bug：Windows 下 CRLF 行尾的 
 被当进度条重绘，命令输出整行被清空（inline 送模型路径几乎全空）
- turn LLM 失败韧性：429 风暴 UX、错误双发去重、非流式超时校准

### Notes
- 6 commits 自 v0.3.3（含 1 个 packaging chore）
- 对应 tag：`v0.4.0`

## [0.3.3] - 2026-09-09

### Added
- 首启向导（first-run wizard），首次启动检测并引导用户完成必要配置
- `picoclaw doctor` 健康检查命令，覆盖配置 / 渠道 / MCP / 安全档位
- 安全档位（safety tier）机制，按场景分级启用 deny 规则
- 并发默认 P0 四件套（gateway watchdog 探活自愈）
- 聊天反馈 turn 计时 + 停滞警告 + 可靠收尾（避免长 turn 静默卡死）
- 系统目录保护 + 毁灭性拦截集（dangerous patterns 默认启用）
- 流式（streaming）默认开启

### Fixed
- ocr v1.11.6 评审修复（12 findings 全部落地）
- 飞书 ACK：消息确认机制，避免丢消息
- 飞书表情回应回显：用户表情可被 AI 正确读取
- 飞书卡片回调可观测性（card.action.trigger 日志/事件链路）
- 配置页未知字段 500 → 友好提示（schema mismatch 不再直接 crash）
- 流式卡片 fallback 契约（流式失败时降级到普通消息的协议）
- gateway 探测超时误杀（避免 health probe 把健康 gateway 当作 down 重启）
- 测试隔离加固（test 不再依赖真实 cron / 系统时钟等副作用）

### Notes
- 11 commits 自 v0.3.2
- 对应 tag：`v0.3.3`

[Unreleased]: https://example.com/picoclaw/compare/v0.4.0...HEAD
[0.4.0]: https://example.com/picoclaw/compare/v0.3.3...v0.4.0
[0.3.3]: https://example.com/picoclaw/compare/v0.3.2...v0.3.3