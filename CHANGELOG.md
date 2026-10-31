# Changelog

本项目的所有显著变更都会记录在此文件。

格式基于 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/)，
版本号遵循 [Semantic Versioning](https://semver.org/lang/zh-CN/)。

## [0.5.2] - 2026-10-31

### Added

- **ask_user 问询卡步进化（对齐 Octop 交互，设计文档 `docs/design/2026-10-31-ask-user-octop-redesign.zh.md` 评审修订版）**：堆叠式改步进式——一次一题 + `n / total` 进度徽标，单选选完自动前进（点"其他…"与多选等手动确认），末题进回顾模式逐题确认、可点回任一题修改；全宽选项行（字母 chip + 选项描述次行直接可见）+ 有选项题自动追加"其他…"展开自由文本（`TextareaAutosize` 1–4 行自动增高）；"你决定"改发语义化委托消息（不再发字面 `/skip`，IM 通道 `/skip` 不受影响）；完成态改 `<details>` 摘要条（可展开各题答案，刷新/IM 端作答后无本地数据时降级只显示问题）；保留 `latest` 防错位、提交 30s 恢复、seal 三态机制
- **安装向导多语言**：Inno Setup `ShowLanguageDialog=no` 随 Windows UI 语言自动选择（简中/英语），三处硬编码中文迁移至 `[CustomMessages]` 双语，未匹配语言兜底英语，`/LANG=` 仍可强制指定

### Fixed

- 多题卡提交协议防错位（评审 P1）：后端 `collectAskReply` 按行切分（行 i = 题 i、超出行折叠末题），前端提交前把每题答案内嵌换行压平为"；"，中间题多行"其他"文本不再导致后续答案错位；单题卡保留多行原文
- 选项行选中态 hover 边框变淡（Tailwind `enabled:hover:` 变体特异度高于选中态类）；补 `aria-pressed` 选中态语义

### Changed

- 状态机纯逻辑抽离 `web/frontend/src/features/chat/ask-flow.ts`（`answerText` 单一真相源——只点"其他"未填文本视为未答；`autoAdvance` 自动跳题规则）+ 22 例 vitest 单测
- i18n：zh/en 新增 11 个 ask key（你决定/下一步/回顾/完成态摘要等），5 个 locale 同步删除 7 个死键（IM 回执为 Go 硬编码不引用前端 i18n）

## [0.5.1] - 2026-10-31

全仓模块梳理与加固：8 个并行评审代理逐模块分析 + 20 项 P0/P1 断言亲自复核（实施报告 `docs/design/2026-10-31-module-review.zh.md`），二轮独立评审交叉核实并当场修复（报告 v2 `docs/design/2026-10-31-code-review-report.zh.md` 含全部机理与测试映射）。四批次 + 修复轮共 16 提交，净删约 4000 行，新增 14 个回归测试。功能地图详见 fork-overview §26。

### Added

- **glob / grep 文件工具**：进程内文件名模式与内容搜索（`**` 递归、include 过滤、大小写开关；三层上限：深度 24/匹配 200/文件 4000，单文件 8MiB 跳过并计数），走既有 fileSystem 抽象，沙箱与系统目录守卫语义不变；`tools.glob.enabled` / `tools.grep.enabled` 独立开关（默认开）
- **`/drafts` 草稿审阅命令**：闭环 evolution 技能草稿回路（此前持续产出草稿但无任何用户入口）——list/preview/accept/reject，consent-first；accept 复用冷路径全套校验+扫描+原子写回滚；WorkspaceID 过滤（共享 state_dir 不跨工作区泄露），已 accepted 拒绝 reject
- **doctor MCP 探测**：逐 server 报可达性与工具数（复用运行时同 transport/env 路径），此前 launcher 专用的 ProbeServer 63 行零调用
- **spawn_status 真实接线**：此前永远空（v0.2.3 宣传的功能已随 SubTurn 重构静默失效）——spawn/subagent 同步注册任务生命周期，ack 回带 `task_id`；**按发起会话隔离**（OriginChannel/ChatID 过滤，跨会话任务不互见）
- **会话重命名 API**：`PUT /api/sessions/{id}/title`（user 源永不覆盖、幽灵 ID 404）+ web 侧栏行内编辑
- **web 停止回合按钮**：生成中 composer 显示停止键，发 `/stop` 走既有 busy 拦截序
- MQTT `tls_skip_verify`（`*bool`，nil=维持存量 skip）可开启证书校验；`/ready` 接入 channels 真实检查（配置渠道全停 503）；AGENT.md frontmatter `maxTurns` 未强制执行时加载告警

### Changed

- **subagent 参数收口**：删 Temperature/MaxContextRunes 死管线（schema 未暴露/转换层丢弃），`SetLLMOptions` 收窄单参；`custom_allow_patterns` 语义钉死为仅扩展程序化 allowlist（测试+文档标注）
- **`agent.llm.delta` 诚实下线**：payload/日志 case/hook kinds 映射/Kind 常量全删（逐 token 事件会淹没有限事件日志）
- 移动端会话侧栏抽屉化：内联实例 `hidden md:flex`，无限滚动 observer 下沉单实例（修复共享 ref 跨实例失效）
- web 后端 config 写锁三分收敛到 `configWriteMu`：models.go 五处切换 + tools/pico/wecom/weixin/oauth 七个无锁 load-modify-save 路径加锁

### Fixed

- **死锁**：运行中网关确认微信/企微扫码绑定时 `save*Binding` 持 `configWriteMu` 调 `RestartGateway → startGatewayLocked → EnsurePicoChannel` 二次加锁同把非重入 Mutex——HTTP handler 永久挂起并堵死全部 config 写路径（必现，批次三 B5 回归）；重启移出锁外，watchdog+TryLock canary 回归测试钉死
- **spawn_status 跨会话泄露**：接线时 RegisterTask 不填来源标识，会话过滤恒不生效——补 ctx 来源后隔离生效
- **deltachat rpc 监护**：bootstrap 失败分支泄漏进程+goroutine（killCurrent 收尸）+ 代次标记防双监护；matrix sync 退避随稳定运行重置；whatsapp bridge 断线重连（弃用告警指向 whatsapp_native）
- **SubagentManager 任务表无界增长**（完整 LLM 结果文本只进不出）：50 条封顶按创建序淘汰，running 永不淘汰
- **聊天页渲染即炸**：移动端抽屉触发按钮在 Radix Sheet 树外（`DialogTrigger must be used within Dialog`）——tsc 查不出上下文错误，受控模式直接 onClick
- error_classifier 504 入 transient（不再中止 fallback 链）+ 429+配额消息提升 Billing（对齐 HTTPError 分支）；anthropic_messages 错误通道走 common.HandleErrorResponse（恢复 Retry-After/中文提示/256B 上限）；updater 非 github release URL 显式报错（不再静默回落上游——供应链）；EvolutionConfig.MarshalJSON 补三个生命周期字段（此前配置回写静默抹除）；telegram 流式节流改 rune 计数（CJK 提前 ~3 倍修正）；glob POSIX 绝对路径不再被重定位进 workspace；grep 单文件 8MiB 上限 + rune 截断不出替换符；重命名幽灵 ID 404

### Removed

- 净删约 4000 行：死文件（agent/adapters、evolution organizer+preview、utils/context、legacy 事件层、tools/toolloop 传递性死链、web/backend/model、前端 api/pico.ts）、死导出群（steering 死 API、LLMPhase、registry 三方法等）、**6 个假旋钮**（discord mention_only、line webhook_host/port、onebot group_trigger_prefix、pico write_timeout、voice elevenlabs_api_key）+ `toleratedLegacyNoopKeys` 容错（存量配置继续加载）、i18n 死键 18 组 ×5 locale（评审曾断言 PUT /api/config 无测试应删，复核推翻——端点保留）

## [0.5.0] - 2026-10-25

### Added
（v0.4.0 以来 87 个功能提交的浓缩，按域分组；细节见 docs/design/fork-overview.zh.md）

**HITL 审批门禁**（agentscope-go/octop 借鉴）
- 内置审批钩子：ask_patterns 匹配的命令/工具先问后执行，超时 fail-closed；单清单配方 `inherit_exec_deny_patterns`（exec deny 表直接作为审批范围）与 `hard_deny_patterns` 不可批准地板
- web 消息流内嵌审批卡（三按钮 + 倒计时 + 封存留痕 + 刷新恢复）与飞书原生交互卡；会话策略三档 `/approve always`（规则提升）/`/approve all`（会话全局放行）/`/approve off`（随时撤销）；`/deny` 尾随理由回注模型；群聊答案钉住触发用户
- 输出清理管线新增第三方 API key 格式掩码（sk-/AKIA/AIza/hf_/ghp_/xoxb-），`cat .env` 场景的明文 key 不再进模型上下文

**ask_user 结构化问询**（octop 借鉴 §三）
- agent 被用户决策卡住时发起结构化问题（1-5 题×6 选项）并阻塞等待；web 问询卡（多题草稿统一提交、单题多选、刷新恢复）；作答先于 steering 路由、永不进模型上下文、永不形成审批策略

**上下文工程**
- 按模型上下文窗口：`model_list[].context_window` 条目 min 钳制生效窗口，`/switch`/`/new`/重启恢复三条路径跟随；显示（declared）与门控（gating）分离
- `/compact` 手动强制压缩（done/scheduled/nothing/busy/unavailable 五档契约，空闲分支异步化 + 有界等待）；上下文分段计量：used 按来源拆六段（system_prompt/skills/tool_definitions/mcp/subagents/conversation），口径镜像实际出站请求
- 渐进式工具加载（octop 借鉴 §六）：MCP 工具数超阈值（默认 20）时未加载工具以延迟引用进 LLM，`tool_search` 元工具按需装载、会话粘性、`/new` 清空
- 长任务上下文防线：迭代边界压缩 + 动态 max_tokens 钳制（pi 借鉴）、split-turn 前缀摘要、空闲会话后台压缩扫描器、截断答案自动续写（auto_continue_turns 默认 2 段）

**工具**
- LSP 诊断与源码修复（参考 pi-lsp：`lsp_diagnostics`/`lsp_fix`，路由到配置的 language server）
- exec 超时返回引导改用 background 会话 + per-call `timeout` 参数；文件系统工具 Windows 兼容性五件套

**可靠性**
- 重启恢复：网关重启后封口中断会话并提示可继续（turn-in-flight 标记 + 悬空 tool_calls 补合成结果），全 agent 会话遍历，启动后异步执行
- 卡死三层防护：stall watchdog 强制释放 + 长任务进度心跳（闲置超阈值发布进度步骤）+ 前端卡死提示；`turn_status` 重连信号（页面刷新不再丢失回合状态）

**Web / TUI / 飞书**
- `picoclaw tui` 终端客户端（网关客户端型，参考 pi 设计语言：token 色板/背景色块消息/会话历史）
- web：`/mcp` 管理页（卡片编辑/草稿/连接测试/运行徽章）、回合状态栏（耗时/模型/API 次数/用量/上下文）、聊天 markdown 渲染升级、会话列表全渠道、频道侧边栏改版、五套主题切换
- 飞书：流式卡片过程面板（续段标识、上下文用量条、LLM 缓存命中 footer、width_mode=fill）

**其它**
- cron 热重载（改任务不再重启网关）与唤醒门、自动化建议、蓝图模板与 `/learn` 命令；`/new` 改为归档轮换（旧对话独立成档）；品牌层重设计 PicoClaw → Limulus（鲎）；web 控制台五套主题切换（参考 metacubexd 调色板模型，含防闪烁引导与旧值兼容）

### Changed
- **审批 builtin 默认启用**（升级冲击）：存量配置若从未写 `hooks.builtins.approval`，升级后 `git push`/`git reset --hard`/`git clean`/brew/pip/npm uninstall 六条模式开箱即弹出审批卡（fail-closed，cron/心跳回合同样生效）；显式 `"enabled": false` 关闭（见 docs/guides/configuration.zh.md「升级冲击」）

### Fixed — 2026-10-23 评审修复轮（五组并行评审 + 全量核实）

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