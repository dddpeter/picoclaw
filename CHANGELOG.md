# Changelog

本项目的所有显著变更都会记录在此文件。

格式基于 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/)，
版本号遵循 [Semantic Versioning](https://semver.org/lang/zh-CN/)。

## [Unreleased]

## [0.5.7] - 2026-11-13

### Added

- **A2UI Web 渲染（展示型 MVP，设计 `docs/design/2026-11-13-a2ui-web-render.zh.md`）**：agent 在回复中途调用新工具 `render_ui` 产出 A2UI v0.9 JSON，pico web 聊天流里即时渲染结构化卡片（表格/分组数据/带标签字段），替代长篇纯文本堆砌。本版本只做展示（看、hover），交互回传与图表是二期：
  - 工具侧三道门：`json.Valid` → v0.9 分帧校验（每条 `version` 前缀 + 恰好一个非空 union 分支，批内至少一条 `createSurface`）→ 256KB 批次上限；成功反馈静默（不刷工具流）；Go 侧不解析 A2UI 结构，`bus.UIMeta` 原样透传到 `message.create` 的 `payload["a2ui"]`，schema 权威校验在前端 zod 层
  - 渲染：官方 `@a2ui/react@0.15` + `@a2ui/web_core@0.15` basicCatalog（Lit Web Components，light DOM）；每卡独立 MessageProcessor（surfaceId 撞车/一 turn 多卡天然隔离）；畸形封包双层兜底（zod 拒 → 错误占位卡，渲染期崩溃 → error boundary），任何情况不炸聊天流；basicCatalog Text 的 markdown 走 app 同一渲染管线
  - 历史回放：刷新页面卡片原样重现——从 session JSONL 已持久化的 render_ui 工具调用参数提取（与 live 相同校验门 + 兜底推导），无需 marker 文件
  - 非 pico 渠道（飞书/TUI）降级为零新代码：`OutboundMessage.Content` 携带纯文本兜底（summary 参数优先，缺省从分帧生成摘要），现有渠道逻辑直接显示
  - 开关：注册走 `cfg.Tools.IsToolEnabled("render_ui")` 门（无独立配置键，默认分支放行 = 默认开启）

### Fixed

- **`render_ui` 工具描述与校验门对齐 v0.9 strict schema（fix round 1，端到端验证发现）**：messages 参数描述由 components/dataModel 内联在 `createSurface` 的错误形态（会被前端 strict zod 拒、渲染错误占位卡）改为分帧示例——首条 `createSurface` 封包（仅 surfaceId/catalogId/theme?/sendDataModel?）→ `updateComponents` 分帧 → 可选 `updateDataModel`，顶层只允许 `version` + 四分支之一；工具校验门同步修正——旧门要求每条消息都带 `createSurface`，会拒绝合法分帧批次，现改为每条消息恰好一个非空 union 分支 + 批内至少一条 `createSurface`；纯文本兜底摘要改从分帧组件与数据模型字符串生成（path 绑定文本也进兜底）。详见设计文档 §3.1
- **`render_ui` 校验门加严 + description 教全组件 props（部署后首测发现）**：真实模型调用产出 `createSurface` 多 `"title"` 键、`Card{style,title}` 等 React 风格 props——strict schema 拒绝后前端渲染死的错误占位卡。门 2 新增分支载荷键集校验（四分支各自只收 strict 键集）与组件名白名单 + `id` 必查，错误信息列出合法键集/组件名供模型重试；description 补全 Row/Column 的 justify/align、List 的 direction、Text 的 variant/weight 枚举与"NO styling props——标题用 Text variant"警告；旧内联形态（components 内联 createSurface）从"不预拒"改为拒绝（生产无预修复批次，容忍只会让模型拿死卡）；组件 props 不做 Go 侧校验（防上游 catalog 演进时误拒合法批次，props 精度由 description 承担）。详见设计文档 §3.2

## [0.5.6] - 2026-11-08

### Changed

- **TUI P4：默认改为主屏滚动模式 + pi 布局一致性（设计 `docs/design/2026-11-07-tui-p4-pi-layout-design.zh.md`）**——行为变更，升级注意：
  - **渲染模式**：默认主屏滚动（对话沉淀进终端 scrollback，滚轮/复制/翻页用终端原生）；`--fullscreen` 保留原 alt-screen 全屏模式
  - **键位变更**：`Ctrl+O`↔`Ctrl+T` 对调（O=展开工具输出、T=展开 Thinking，对齐 pi）；`Ctrl+C` 不再停止回合——改为清空输入框/双击退出（停止回合只走 `Esc`）；新增 `Ctrl+D` 空输入退出
  - **布局**：顶部 statusbar 删除；footer 改 pi 式两行（L1 网关·标题·连接态，L2 tokens·ctx%+右对齐模型）；启动时打印一次性启动头（brand+快捷键提示）；thinking 折叠标签改「Thinking…」

### Changed（2026-11-08 使用反馈）

- **思考/工具输出默认完整展开**（对齐 web wrap=true）：流式思考全量实时可见，完成后不再折叠；`Ctrl+T`/`Ctrl+O` 变为"折叠/展开"切换
- **footer 模型常显**：生成中显示 `spinner · 模型`（不再顶掉）；空闲回退 config 默认模型
- **配色丰富化**：思考紫、工具名青、用户块文字提亮、action 行 accent 圆点

## [0.5.5] - 2026-11-08

### Added

- **工作区快照 + turn 级撤销（trae borrowing 增量清单 §2.1，`tools.checkpoint`，默认关）**：turn 结束把本 turn 改过的文件写前内容存档为版本，`/revert` 一键恢复
  - 覆盖面：`write_file`/`edit_file`/`append_file`/`lsp_fix` 精确拦截写前内容；exec 经增量 mtime 扫描环绕覆盖建/改/删（`track_exec` 默认开）；噪声目录（.git/node_modules/sessions/tmp）不跟踪
  - 撤销：`/revert` 撤最近版本、`/revert <id>` 级联回滚、`/revert list` 列版本；撤销即消费（重复执行继续回溯）；turn 活跃时拒绝恢复、list 始终可用；完成向会话 pico 客户端广播 `revert.status`（带 seq 入补发环）
  - 诚实降级：超限/预算耗尽条目记录 `stored=false` + 原因，revert 时如实列出"内容未保留"；版本数/磁盘预算淘汰最老版本（最新版本软保护）；walk 不完整（错误/2 万文件截断）不产生任何归因
  - Web 只读接口：`GET /api/sessions/{id}/versions` 与 `.../versions/{version}/diff`（diff 已过 API key 掩码；存储原文不掩码保证字节级恢复）
  - 存储在 `~/.picoclaw/checkpoints/`（工作区之外）；配置与行为详见 `docs/guides/configuration.zh.md`「工作区快照与撤销」
- **入站 message.send 幂等去重（trae borrowing §2.2）**：服务端按 (session, 帧 ID) 记录最近接受的请求，重连/重发同 ID 回 `message.ack` 而不再起第二个 turn；投递失败回滚窗口记录；空 ID 客户端行为不变
- **出站事件序号与断线补发——网关侧（trae borrowing §一，配套 TUI P2）**：session 广播帧单调 seq + 环形缓冲，重连 `?last_seq=N` 精确重放缺口，缺口过大如实报 `stream.catchup`
- **TUI P3：问询答题浮层 + 外部编辑器 + bell + 图片粘贴（设计 `docs/design/2026-11-07-tui-p3-design.zh.md`，零服务端改动）**：
  - 问询浮层：ask 卡到达自动弹出步进式答题（移植 web `ask-flow.ts` 语义：单选即选即进、多选 toggle+确认行、「其他…」自由文本、无选项题直接输入、回顾模式回改、未答禁提交）；字母 A–F 快选；「你决定」发送与 web zh.json 锁定一致的委托文案；Esc 最小化 + `:ask` 重开；回答按协议拼装（多题每题一行、内嵌换行压平"；"、单题整条原文）
  - 外部编辑器（Ctrl+G）：$EDITOR → $VISUAL → notepad|vi；Windows 经 `cmd /c`（.cmd shim）；存盘退出回填 composer（去 BOM/单个尾换行，不自动发送），非 0 退出码保持原内容
  - bell：回合结束（typing.stop）且终端失焦时响铃（`tea.BlurMsg`/`FocusMsg` 跟踪，未知焦点态=响）；审批/问询卡到达必响
  - 图片粘贴（Ctrl+V）：Windows PowerShell GetImage→临时 PNG；macOS osascript «class PNGf» hex 解码；Linux wl-paste/xclip 按型探测；解码后 >20MB 客户端拦截；dataURL 走 message.send media；本地回显用户块 + 📎

- **TUI P2 完善（参考 pi 交互模式，设计文档 `docs/design/2026-11-07-tui-p2-design.zh.md`）**：
  - 断线补发：`pkg/picoclient` 维护 per-session seq 游标，重连握手 `?last_seq=N` 触发服务端缺口重放（配套网关 §一）；旧 seq 帧幂等丢弃；`turn.status` 重连即知回合状态（恢复 generating 态）；`stream.catchup` 丢帧提示 + 空闲时自动重读磁盘历史重建
  - 命令补全浮层：`/` 服务端命令（含子命令两级）与 `:` 本地命令 fuzzy 补全（pi fuzzy.ts 算法移植）；↑↓ 选择 · Tab 补全 · Enter 选定 · Esc 关闭；行首触发，聊天正文里的 `/path` 不弹
  - 会话选择浮层（Ctrl+S/:ls）：sessions 目录全量扫描（渠道徽标/标题/相对时间/当前标记），fuzzy 搜索，Enter 切换；非 pico 会话只读加载（composer 禁用）；切回同 id 会话靠重连补发重建漏帧
  - 帮助浮层（?/:help）：快捷键 + `:` 命令 + `/` 服务端命令（`BuiltinDefinitions` 生成）总表，可滚动
  - 用量浮层（:usage）：context 各水位 + 百分比条 + 最近一轮 token
  - 模型浮层（:model 无参）：清单取 config agents 模型并集，选中合成 `/switch model to <id>`
  - 复制（Ctrl+X/:copy [n]）：倒数第 n 条答案 → OSC52（`tea.SetClipboard`，ssh/tmux 可用）
  - 工具输出展开（Ctrl+T）：完整输出 + diff 行着色（+绿/-红）
  - 输入回溯：单行 composer 时 ↑/↓ 回溯已发送输入（内存环）
  - 鼠标滚轮滚动（Shift+拖动选文本）
  - 审批卡/问询卡：v1 payload + v0 文本兜底（对齐 approval_hook/ask_user 锁定契约文案）；回执（✅/⛔/📝/⏭️/⏰）自动封存卡片，孤儿回执照常入时间线；卡片带回复语法提示（/approve /deny、选项文本）
  - 终帧 markdown：glamour v2（dark 风格 + WordWrap），流式期间仍纯文本；`:raw` 切换原文显示
  - `:` 命令集终版：help/quit/stop/new/clear/ls/model/status/context/title/reload/usage/copy/raw（合成型直发服务端命令，回复入时间线）
  - Item 级渲染缓存：长会话不再每帧全量重渲（签名含 kind/width/flags/content hash）

### Fixed

- 快照实现评审批（独立复审 + 逐条亲证）：exec 扫描 walk 不完整（错误/文件数截断）时不再产生 created/deleted 归因（原缺陷可使 /revert 误删真实文件）；TurnCapture 并发锁（hook 超时放弃 goroutine 与 turn_end 提交的 fatal map race）；diff 先 stat 后读（turn 内文件膨胀的 OOM 向量）+ 版本 diff 总量 8MB 封顶；版本号数值排序（v1000+ 字典序翻转会使 /revert 盖错内容）；/revert busy 分支不再漏进 steering 队列；网关 pico 回调接线移到 reload 之后；缓存失效诚实降级等 8 项（详见 d2e058a4）
- `TestNewPicoclawCommand` 补上 P1 遗漏的 `tui` 子命令断言（P1 引入子命令时未同步测试清单）

## [0.5.4] - 2026-11-01

### Fixed

- **空闲压缩成功记账持久化——修"每次网关启动全量重压闲置会话"**：门⑤"不重压"的记账原是进程内 records map（重启即清零），而 seahorse 路径压缩只写 SQLite、JSONL 水位不回落 → 每次重启对全部高水位闲置会话发起真实摘要 LLM 调用（实测一次启动 7 会话、idle 19~43 天）；设计稿"Compact 对已压状态是廉价 no-op"的前提被实测证伪。成功 marker 现持久化进 SessionMeta（`IdleCompactedAt`/`IdleCompactedSeen`，不经手 `UpdatedAt`——记账不是活动，不重排会话列表）；门⑤叠加持久判定（同一条活性规则：UpdatedAt 无前进即跳过，新活动后恢复资格）；SessionManager 保留内存回退，best-effort；marker 在压缩 result 回流前落盘（result 可见 ⇒ marker 已落盘），marker store 在 goroutine 外 resolve 避开 P3-5 锁语义。回归测试 +2（重启模拟不重压；marker 落盘回读 + UpdatedAt 不动）；设计文档 §3.6 修订 + §9 修订记录

## [0.5.3] - 2026-10-31

### Fixed

- **会话重命名兼容 legacy pico 会话**（v0.5.2 评审报告核实的遗留项）：`handleRenameSession` 此前用列表裸 UUID 直调 `SessionExists`，而 legacy 会话在盘上是带 `agent:main:pico:direct:pico:` 前缀的完整 key → 列表页对 legacy 会话改名必 404。现先经 `findJSONLSession` 解析完整 key（支持 `?channel=` 消歧，与 GET 会话端点一致）；legacy `.json` 老会话因无法携带存储标题（列表固定显示首条消息预览）诚实返回 409 而非静默写永不显示的 meta；裸键直接存在时保留旧语义。前端改名请求带上 `channel`（`SessionSummary.channel`）。回归测试 +2（legacy 前缀 .jsonl 改名成功且 meta 落完整 key；legacy .json 409 且无 meta 副作用）

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

[Unreleased]: https://github.com/dddpeter/picoclaw/compare/v0.5.6...HEAD
[0.5.6]: https://github.com/dddpeter/picoclaw/compare/v0.5.5...v0.5.6