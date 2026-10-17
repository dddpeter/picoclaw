# 智能功能提示系统设计（Smart Hints）

> 目标：让用户在**正确的时机、正确的渠道、以最低的打扰**发现并学会使用 picoclaw 的功能——重点是聊天命令与配置修改能力。
>
> 状态：设计稿（未实施）。日期：2026-10-06。

## 一、问题与现状

用户（尤其是新用户与跨渠道用户）对功能的发现路径现状：

| 发现路径 | 现状 | 缺口 |
|---|---|---|
| 主动查 `/help` | 存在（`pkg/commands/cmd_help.go`），平铺 usage+描述列表 | 无分组、无渠道可用性标注；用户要先知道有 `/help` |
| 输入未知/拼错命令 | `Executor.Execute`（`pkg/commands/executor.go:45`）对未注册命令返回 passthrough，**直接当普通文本喂给 LLM** | `/swich` 会被模型猜或幻觉出命令；无最近邻纠错 |
| 自然语言问 agent | 模型不知道命令清单，只能靠训练数据猜 | 用户问"怎么重新开始对话"，模型未必知道 `/new` |
| Web 输入框 | 无命令自动补全 | 仓库已有 `cmdk` 依赖（`ui/command.tsx`），聊天侧未用 |
| 场景化时机提示 | 零散：busy/steering 提示、turn 失败通知（`publishTurnError`）、进度心跳已有；但"上下文快满了→该 `/new`"、"步数截断→可调 `auto_continue_turns`"这类**配置改进建议**不存在 | 无统一机制 |
| 安装引导 | CLI `picoclaw onboard` + Web setup-wizard + tour-guide（`src/components/tour/`）已覆盖首次安装 | 只管"第一次"，不管"用了一段时间后仍不知道的功能" |

已有的可复用基建（本设计的地基，不重复造）：

- **结构化命令元数据**：`Definition{Name, Description, Usage, SubCommands, Handler}`（`pkg/commands/definition.go`），Registry 统一注册，Telegram 菜单同源。命令目录是现成的，缺的只是消费。
- **系统通知通道**：`bus.PublishOutbound` + `Context.Raw["message_kind"]` 渐进标记（`progress_note` 先例：`pkg/channels/pico/protocol.go:37` 常量、web `AssistantMessageKind`、TUI 覆盖式状态行、feishu 无感知降级文本）。系统通知不落会话历史是既有约定。
- **频控持久化先例**：cron suggestions（`pkg/cron/suggestions.go`：dedup key 永久门锁、pending 上限、atomic 0600）——hints 的频控存储照此模式。
- **系统提示词槽位机制**：`ContextBuilder` 槽位（skills 摘要 `BuildSkillsSummary`、`project_docs` 槽位优先级 890），加一个 commands 槽位完全同构。
- **事件信号源**：pico 协议已带 `context_usage`；`turnResult.endedByIterationLimit`、`publishTurnError`、`resolveMediaRefs` 图片上限、`collectUnknownJSONFields` 配置诊断均为现成信号点。

## 二、设计原则

"智能"不等于"上模型"。按确定性分三层，每层只做该层擅长的事：

1. **确定性引导（被动、精确、零成本）**——命令元数据已经结构化，把它用满：拼写纠错、用法回显、Web 斜杠面板、`/help` 分组。
2. **确定性时机提示（事件规则引擎）**——网关内已有的信号（上下文水位、失败、截断、长会话）经规则表触发提示，规则可测、可频控、可关闭。
3. **模型内生知识（自然语言问答）**——把命令目录注入系统提示词。用户用自然语言问"你能做什么/怎么换模型"时，**让被问方知道答案**，而不是预测每条消息的意图。

配套纪律（与 fork 既有哲学一致）：

- **低打扰是硬约束**：每 key 每会话只提示一次 + 全局每日上限 + turn 活跃期间不插播（统一在 turn 收尾钩子发送，不与流式卡片/steering 抢屏）。
- **consent-first**：提示只告知，绝不代替用户执行（同 cron suggestions）。
- **通知不落会话历史**：hints 走 outbound 总线，同现有系统通知约定，不污染下次请求的上下文。
- **开放默认**：`agents.defaults.hints.enabled` 用 `*bool`，未配置 = 开（同 `loop_detection`/`cooldown_enabled` 惯例）。

## 三、总体结构

```
┌─ P1 确定性引导 ──────────────────────────────────────────────┐
│ A1 命令纠错（executor→handleCommand 层）                        │
│ A2 /help 分组增强                                              │
│ B  命令目录注入系统提示（ContextBuilder 新槽位）                 │
│ C  Web composer 斜杠命令面板（cmdk）+ GET /api/commands         │
└──────────────────────────────────────────────────────────────┘
┌─ P2 事件规则引擎 ────────────────────────────────────────────┐
│ D  pkg/agent/hints.go：规则表 + 频控 + message_kind=hint        │
└──────────────────────────────────────────────────────────────┘
┌─ P3 远期 ────────────────────────────────────────────────────┐
│ E  evolution 模式提议（复用 cron suggestions 基建）             │
└──────────────────────────────────────────────────────────────┘
```

## 四、组件设计

### A1. 未知命令纠错（后端，小改）

**位置**：`pkg/agent/agent_command.go` `handleCommand`——在 `Executor.Execute` 返回 `OutcomePassthrough` 且首 token 形如命令（`/word`，Telegram `/name@bot` 已由 `request.go` 剥离）时介入。

**例外清单先行（正确性红线）**：`/use`、`/learn`（注册表外，`agent_command.go:35/39` 拦截改写）、`/approve`、`/deny`（`approval_hook.go` `parseApprovalReply`）、`/stop`（活跃 turn 快捷路径）**必须原样放行**——这些命令不在 Registry，是"注册表之外"的合法命令。例外词表从各拦截点抽成共享常量，防止新增注册表外命令时遗漏。

**算法**：对命令名做最近邻——编辑距离 ≤ 2 或前缀包含（`/swich`→`/switch`、`/lst`→`/list`），候选取自 `Registry.ListDefinitions()`：

```
未知命令 /swich。你是想 /switch 吗？发送 /help 查看全部命令。
```

无最近邻且整条消息就是单个命令 token 时，也回一句 `/help` 指引；带参数的未知"命令"（如 `/foo bar baz`）保持 passthrough——用户可能是故意把斜杠文本发给模型。

**测试锚点**：`TestHandleCommand_SuggestsNearestCommand`、`TestHandleCommand_PassesThroughRegistryExternalCommands`（/use /learn /approve /deny /stop 一个都不许拦）、`TestHandleCommand_LongUnknownSlashTextPassesThrough`。

### A2. `/help` 分组增强（后端，小改）

`Definition` 增加可选 `Group string` 字段（不改变上游结构的必填面，omitempty）：`session`（/new /clear /title /context）、`model`（/switch /list /use）、`runtime`（/status /reload /stop）、`automation`（/cron /learn /btw /subagents）、`meta`（/help /start /show /check）。`formatHelpMessage` 按组渲染，未标组的命令归"其他"保持兼容。子命令数 > 3 的命令（/cron /list /show）在帮助里折叠为 `usage` + 「子命令 N 个，输入 /cron 查看详情」——`EffectiveUsage()` 已能生成完整 usage，`/cron` 空参数也已回显用法，无需新逻辑。

不引入描述文案 i18n（现有 Description 均为英文，保持现状，只做结构分组）。

### B. 命令目录注入系统提示（模型侧"智能提示"的根）

**机制**：`ContextBuilder` 新槽位 `commands`（优先级紧邻 skills 槽位之下，如 880），内容一行一命令（usage + description，约 25 行 ≈ 400 token），末尾一句说明：

```
# 可用命令目录
用户可能用自然语言询问平台功能（如何换模型、如何开新会话等）。
回答这类问题时优先推荐上述命令；不确定时建议用户发送 /help。
不要虚构不在此目录中的命令。
```

**接线**：`agent_init.go` 创建 Registry 后，经 `NewContextBuilder(...).WithCommandCatalog(...)` 传入（同 `WithProjectDocs` 模式）；槽位内容随 Registry 定义静态生成，`sourcePaths()` 不需要 mtime 失效（命令集进程内不变，重载即重启 agent）。

**配置门**：`agents.defaults.inject_command_catalog`（`*bool`，nil=开）。目录占用极小且直接决定"用户问功能 agent 能否答对"，默认开符合开放默认精神。

**测试锚点**：`TestContextBuilder_IncludesCommandCatalog`、`TestCommandCatalog_RespectsToggle`、`TestCommandCatalog_ListsAllRegisteredCommands`（钉住新命令注册后目录自动跟进——防止"加命令忘目录"）。

### C. Web composer 斜杠命令面板（前端 + launcher API）

**后端**：`web/backend/api/commands.go` 新增 `GET /api/commands`，返回 `BuiltinDefinitions()` 的 `{name, description, usage, group, subcommands}` 列表（launcher 后端 import `pkg/commands` 无障碍，同 MCP 页读配置模式）。纯只读端点。

**前端**：`chat-composer.tsx` 监听输入——首字符为 `/` 且光标在首个空白前时，弹出 `cmdk` 面板（复用 `ui/command.tsx`），列出命令（带 Group 分组与描述），↑↓ 选择、Tab/Enter 补全命令名；选中带子命令的命令（/cron /list /show /switch）继续列出子命令项。数据用 React Query 缓存（`staleTime: Infinity`——命令集运行期不变）。

**测试注意**：cmdk 是 Radix 系，Playwright 语义点击可能超时（既有经验：用 cua 坐标点击绕行）；面板可用 vite preview 免后端验证（mock /api/commands）。

### D. 事件规则引擎（P2 核心新组件）

**位置**：`pkg/agent/hints.go`。

```go
// HintEngine 评估规则并对命中且未过频控的提示经 outbound 总线发出。
type HintEngine struct {
    mu      sync.Mutex
    seen    map[string]time.Time // key = ruleKey + "/" + sessionKey
    day     hintDayCounter       // 全局每日计数
    store   string               // <workspace>/hints.json，atomic 0600（仿 cron/suggestions.go）
    cfg     config.HintsConfig
}

type HintRule struct {
    Key  string
    Eval func(*HintCtx) (text string, fire bool)
}

type HintCtx struct { // 由触发点填充，规则自取所需
    SessionKey  string
    Channel     string
    ChatID      string
    ContextUsage *bus.ContextUsage
    TurnResult  *TurnResult   // endedByIterationLimit / error 等
    ErrClass    string        // providers 错误分类（RateLimit/Billing/...）
    MsgCount    int           // 会话消息数
    SessionAge  time.Duration
}
```

**发送路径**：统一挂在 **turn 收尾**（`pipeline_finalize` 后的钩子）与 `publishTurnError` 之后——turn 活跃期间绝不插播（不与流式卡片、steering 通知、心跳抢屏；这三者已有各自的展示轨）。发送用 `PublishOutbound` 直发来源 channel/chat（`restart_recovery.go:275` 同款模式），payload 带 `message_kind: "hint"` 新常量（`pkg/agent/agent.go` 的 kind 常量组 + `pkg/channels/pico/protocol.go` + web `AssistantMessageKind` 各加一项，progress_note 的渐进增强先例全套照搬）。

**频控三层**：

1. **每 key 每会话一次**——`seen` 持久化到 `<workspace>/hints.json`，重启不重发（用户最烦的就是重复唠叨）；
2. **全局每日上限**（默认 5 条，`max_per_day`）——所有渠道合计，防多会话连环触发刷屏；
3. **最小间隔**（默认 30 分钟，`min_interval_minutes`）——同一用户连续命中不同规则时合并节奏。

**配置**（`agents.defaults.hints`）：

```json
"hints": {
  "enabled": true,          // *bool，未配置 = 开
  "max_per_day": 5,
  "min_interval_minutes": 30
}
```

**首批规则表**（每条规则独立成函数，便于测试与增删）：

| key | 触发信号（来源） | 提示文案要点 |
|---|---|---|
| `context-high` | turn 收尾时 `ContextUsage` 比例 ≥ 0.85 | 💡 上下文即将占满：`/new` 可归档旧对话并开新会话；或在配置中调大 `context_window` / 降低 `compact_usage_threshold` |
| `model-failure` | `publishTurnError` 触发（fallback 链打穿） | 💡 本轮模型失败已自动切换候选未果：可 `/list models` 查看备选、`/switch <model>` 立即换渠道；配额类失败请检查模型配置的 api_key/quota |
| `iteration-limit` | `TurnResult.endedByIterationLimit` 且已是末段续段 | 💡 本轮因工具步数上限截断：可在配置调大 `agents.defaults.auto_continue_turns`，或把任务拆小 |
| `long-session` | `MsgCount ≥ 400` 或 `SessionAge ≥ 7 天`（turn 收尾检查） | 💡 这个会话已经很长：`/new` 归档轮换可获得更快响应（旧对话保留在归档中） |
| `media-truncated` | `resolveMediaRefs` 触发 `max_context_images` 截断 | 💡 本次图片超出上下文图片上限已截断：可调大 `agents.defaults.max_context_images` |
| `empty-response` | 同一会话第 2 次 `EmptyCompletionError` | 💡 多次出现空回复通常意味着 `context_window` 配置虚高：请核对模型真实窗口（见配置文档） |

> 取舍说明：`config-diagnostics`（未知字段）不进规则表——Web 端已有 `unknown-fields-notice.tsx` banner 消费同一信号，聊天侧再提示属于重复打扰。规则表刻意保持小：每条规则都是真实发生过的事故/摩擦（fork 历史上空响应、图片上限、迭代截断都踩过），不做"泛泛功能安利"。

**渠道渲染矩阵**：

| 渠道 | 渲染 |
|---|---|
| feishu / telegram / 其他 IM | 普通文本消息（kind 标记忽略，天然降级） |
| web（pico） | `kind=hint` → 消息流中的浅色提示条（icon 💡 + 可关闭），不占答案区 |
| TUI | 同 progress_note 模式：覆盖式状态行短暂显示，不入时间线（防"假完成"教训） |

### E. 远期：evolution 模式提议（本期不实施）

复用 `pkg/evolution` + cron suggestions 基建：从重复行为模式提议**配置改进**（如"你连续 5 天在 23 点手动跑同一命令，要不要建个 cron 任务"）。`/cron suggest` 链路已存在，只需扩 suggester 的信号源，不新建存储。仅在 P2 落地且实际使用反馈后评估。

## 五、分期实施

| 期 | 内容 | 规模 |
|---|---|---|
| P1 | A1 纠错 + A2 help 分组 + B 目录注入 + C 斜杠面板（含 /api/commands） | 后端三小改 + 前端一组件；一次提交可承载，或 A/B、C 分两次 |
| P2 | D 引擎 + 六规则 + kind 全链路 + web/TUI 渲染 + 配置项 | 后端为主，一个功能块 |
| P3 | E evolution 提议 | 视 P2 反馈 |

## 六、测试要点汇总

- 命令层：`TestHandleCommand_SuggestsNearestCommand`、`TestHandleCommand_PassesThroughRegistryExternalCommands`（例外红线）、`TestHandleCommand_LongUnknownSlashTextPassesThrough`、`TestHelpGroupedOutput`。
- 目录注入：`TestContextBuilder_IncludesCommandCatalog`、`TestCommandCatalog_RespectsToggle`、`TestCommandCatalog_ListsAllRegisteredCommands`。
- 引擎：`TestHintsEngine_RuleFiresOncePerSession`、`TestHintsEngine_DailyCapSuppressed`、`TestHintsEngine_MinIntervalMerges`、`TestHintsEngine_StatePersistsAcrossRestart`（hints.json 回读）、`TestHintsEngine_SilentWhenDisabled`、`TestHintOutboundCarriesKind`、各规则独立测试（`TestHintRule_ContextHigh` 等）。
- 既有回归：`pkg/commands` 全套（executor 两态语义不破坏）、`TestProgressHeartbeat_*`（kind 常量组改动不碰 progress_note）、前端 `pnpm build`。

## 七、同步上游注意事项

- `pkg/commands/definition.go`（Group 字段）、`executor.go`（不改动其两态语义，纠错在 agent_command 层）、`pkg/agent/context.go`（commands 槽位）、`pkg/agent/hints.go`（全新文件）、`pkg/config`（hints/inject_command_catalog 两配置项）、`web/backend/api/commands.go`（新端点）、`web/frontend/src/components/chat/chat-composer.tsx`——均为 fork 增量，上游同步冲突面小。
- 上游若重构 `handleCommand` 拦截链（/use /learn /approve 例外清单），例外清单常量必须跟着迁移，`TestHandleCommand_PassesThroughRegistryExternalCommands` 是守门。
- `message_kind` 常量组新增 `hint` 后，上游若改 pico 协议常量表，保留 `hint`。

## 八、被否方案

1. **每条消息过 LLM 意图分类器选提示**——延迟与成本不成比例，误判噪音无法频控；命令目录注入（B）以 400 token 常驻成本一次性解决"自然语言问功能"，更便宜且更准。
2. **agent 每次答复自动附"相关功能"尾巴**——上下文污染 + 学舌风险（fork 刚完成工具调用学舌根治链，§21），且绝大多数回合与功能发现无关。
3. **提示落会话历史**——破坏"系统通知不落历史"约定，且会让 hints 进入下次请求上下文（自我强化噪音）。
4. **独立推送/通知子系统**——outbound 总线 + message_kind 渐进增强已覆盖全渠道，无需新通道。
5. **hints 做成可交互命令（/hints list/dismiss）**——配置项 + 每会话一次频控已把管理需求压到最低，多余命令面不值。
