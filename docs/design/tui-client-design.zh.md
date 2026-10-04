# TUI 终端客户端设计（A 路径：网关客户端型）

> 状态：**P1 已实现**（2026-10-04）：`pkg/picoclient`（WS 客户端 + 事件解码 + 重连）、`cmd/picoclaw/internal/tui`（聊天视图/状态机/快捷键/`:q :stop :new :clear :help` 本地命令）、main.go 注册 `picoclaw tui`。P2（会话侧栏/markdown 终帧渲染/完整 `:` 命令集/命令提示）与 P3 未实现。日期：2026-10-04。
> 结论先行：新增 `picoclaw tui` 子命令，用 bubbletea 连接**运行中的网关** `/pico/ws`，做"终端版 web 前端"。聊天主链路 100% 复用 pico 协议与既有服务端能力；仅会话列表需要补一条本地读取路径（或 Phase 2 的网关只读 API）。

## 1. 背景与目标

picoclaw 当前有三种人机交互面：飞书 IM、web 前端（launcher 代理 `/pico/ws`）、`picoclaw agent` 的 readline REPL（进程内独立 agent）。缺一个**连接网关的终端界面**——服务器上 ssh 直连、或桌面下不想开浏览器时的轻量入口。

### 目标

- `picoclaw tui` 直连网关：流式聊天（答案打字机 + 推理 + 工具步骤时间线）、斜杠命令直通、会话切换、`/stop` 停止键、steering 提示、context 用量展示。
- 零服务端改动即可跑通 MVP（聊天部分）；会话列表 MVP 允许同机直读磁盘，跨机是 Phase 2 的事。
- 与 web 前端**同时在线**不冲突（pico 渠道 `sessionConnections` 天然支持同 session 多连接广播）。

### 非目标

- 不做进程内 agent runtime（那是 B 路径：升级现有 `picoclaw agent` REPL，独立产品线，本文不覆盖）。
- 不做飞书卡片那种富面板（终端里以行式时间线呈现，信息对齐、形态从简）。
- 音视频媒体发送不做（图片粘贴发送放 Phase 3）。
- 不改 `pkg/commands` 语义、不新增聊天命令。

## 2. 架构总览

```
┌────────────────────── picoclaw tui（终端进程）──────────────────────┐
│  cmd/picoclaw/internal/tui     ← cobra 子命令 + bubbletea Model      │
│      │   视图层：chat / composer / sidebar / statusbar / render      │
│      ↓ 事件流（Go channel）                                          │
│  pkg/picoclient                ← pico 协议 WS 客户端（新包）          │
│      │   Dial/Auth/Reconnect/Send/Typed decode                      │
└──────┼──────────────────────────────────────────────────────────────┘
       │ ws(s)://<host>:<port>/pico/ws?session_id=<uuid>
       │ Authorization: Bearer <pico token>
       ↓
┌────── 网关（picoclaw gateway，常驻）──────────────────────────────┐
│  pkg/channels/pico（服务端渠道，零改动）                              │
│  pkg/bus → pkg/agent（turn/命令/流式/心跳/续段/看门狗，全部既有）      │
└──────────────────────────────────────────────────────────────────┘
```

选型依据（为何不内嵌 agent）：

- **fork 关键行为全部免费继承**：turn 全程持模型读锁、`/switch` `/new` 的 TryLock busy 语义、429 韧性、续段、心跳、stall watchdog、封口语义——这些都在网关进程的 agent 运行时里，TUI 作为客户端天然对齐；内嵌 runtime 则要重新对齐一整遍，且与常驻网关并存时会话状态两个世界。
- **上游同步风险最小**：新代码全部落在新包（`pkg/picoclient`、`cmd/picoclaw/internal/tui`），不动 `pkg/channels/pico` 服务端、不动 agent——冲突面接近零。
- **与 launcher 解耦**：`/pico/ws` 挂在网关自己的 mux 上（`pkg/channels/pico/pico.go:293` ServeHTTP，经 `pkg/channels/manager.go:1246` 挂载），**不经过 launcher 后端**，Linux systemd 无头部署（无 launcher）可用。

### 依赖变更

新增（charm v2 全家桶，`charm.land` module path，2026 年起为稳定线，glab / Grafana Loki 等已迁移；与现有 `github.com/charmbracelet/lipgloss v1.1.0` 不同 module path，可共存）：

- `charm.land/bubbletea/v2` — TUI 框架
- `charm.land/bubbles/v2` — textarea / viewport / spinner / key
- `charm.land/lipgloss/v2` — 样式
- `charm.land/glamour/v2` — markdown 终端渲染（答案正文）

复用既有：`gorilla/websocket`、`pkg/channels/pico` 的导出协议常量与类型、`pkg/config`、`pkg/commands`（仅引用命令清单做补全，Phase 2）。

## 3. 协议映射（完整）

### 3.1 连接与鉴权

| 项 | 值 | 出处 |
|---|---|---|
| URL | `ws(s)://<host>:<port>/pico/ws?session_id=<uuid>` | `pico.go:1040`（缺省 session_id 服务端生成） |
| 鉴权 | `Authorization: Bearer <pico token>`（首选）；或 WS 子协议 `token.<value>`；query `?token=` 需服务端 `AllowTokenQuery` | `pico.go:1068` authenticate |
| token 来源 | config.json `channels.pico.token`（TUI 经 `pkg/config.LoadConfig` 直读，零新配置项） | |
| 连接上限 | 服务端 `pico.max_connections`（默认 100） | `pico.go:1016` |
| 保活 | 服务端每 `ping_interval`（默认 30s）发 WS ping 帧；读超时 `read_timeout`（默认 60s） | `pico.go:1133` |
| Origin 校验 | `allow_origins` 未配置 = 放行（非浏览器客户端不涉 Origin，无碍） | `pico.go:136` |

host 解析：config 的 `gateway.host` 若为 `0.0.0.0` / 空则 TUI 默认连 `127.0.0.1`；`--gateway-url` 可整体覆盖（连远程网关时用，如 `wss://server.example.com/pico/ws`）。

### 3.2 客户端 → 服务端

| type | payload | 说明 |
|---|---|---|
| `message.send` | `{content: string, media?: [dataURL...]}`；顶层 `id`（客户端生成，如 `msg-<n>-<ts>`）、可选 `session_id` | 发消息。turn 活跃时发送 = steering（服务端排队并入当前回合） |
| `media.send` | 同上 | 服务端同一处理路径（`pico.go:1204`） |
| `ping` | — | 协议层 ping → `pong`（WS ping 帧之外的应用层心跳，可选实现） |

发送 `/stop`、`/new`、`/switch model`、`/status`、`/help` 等斜杠命令 = 普通 `message.send`，由 bus → agent 命令路径拦截（`pkg/commands` 18 个内置命令全部直通，TUI 不本地拦截，保证语义与服务端一致）。

### 3.3 服务端 → 客户端（TUI 消费映射）

| type | payload 关键字段 | TUI 渲染 |
|---|---|---|
| `typing.start` | — | 进入 generating 态（spinner 启动、时间戳清零） |
| `typing.stop` | — | generating 态结束（**权威的 turn 结束信号**） |
| `message.create`（占位） | `placeholder: true`, `message_id` | 忽略或渲染临时占位行（后续会被 delete） |
| `message.create`（推理流首帧） | `kind: "thought"`, `message_id`, `content`, `model_name` | 灰色折叠块「💭 思考中…」，独立消息 ID |
| `message.update`（推理流后续） | 同 ID, `content` 全量快照 | 更新该折叠块 |
| `message.create`（工具调用） | `kind: "tool_calls"`, `tool_calls: [{id, type, function:{name, arguments}, extra_content:{tool_feedback_explanation}}]` | 时间线插入绿 ✓ 行「⚙ tool_name」 |
| `message.create`（工具反馈） | 普通消息, `content`（🔧 前缀格式） | 单行工具执行中动画；后续 `message.update` 就地更新（服务端 `ToolFeedbackAnimator` 驱动） |
| `message.create`（心跳） | `kind: "progress_note"` | **不进时间线**，覆盖式状态行（对齐 web 端 `AssistantMessageKind` 语义，防止误判回合完成） |
| `message.create`（答案首帧） | 无 kind, `message_id`, `content`, `model_name` | 答案气泡创建，进入流式打字机 |
| `message.update`（答案流） | 同 ID, `content` **全量快照**（非增量）, 可带 `usage` / `context_usage` | 节流合并（客户端 ~80ms）后全量重渲染 |
| 末帧 update | 附 `context_usage` / `usage` | 渲染后定稿（markdown 高亮），顶栏用量更新 |
| `message.delete` | `message_id` | 移除对应消息（取消时服务端删流式消息；也用于删工具反馈行） |
| `media.create` | `attachments: [{type, url, filename, content_type}]` | 媒体行（URL 任意格式，TUI 终端内显示文件名 + 可打开） |
| `error` | `code`, `message`, `request_id` | 错误行 + generating 态结束；`request_id` 命中本地 pending 用户消息时移除之（对齐前端 `protocol.ts:245`） |
| `pong` | — | 忽略 |

`context_usage`：`{used_tokens, total_tokens, history_tokens, compress_at_tokens, summarize_at_tokens, used_percent}`（`pico.go:1435`）。
`usage`（本轮真实 token）：`{input_tokens, output_tokens, total_tokens}`，两计数均为零时整个字段省略（`pico.go:1452`）。

### 3.4 一个 turn 的典型事件序列

```
（客户端）message.send {content}
← typing.start
← message.create  kind=thought            （推理流，可无）
← message.update  thought…               （节流打字机）
← message.create  kind=tool_calls         （可无）
← message.create  🔧 …                    （工具反馈行，随后 update 动画）
← message.create  kind=progress_note      （长静默心跳，可无）
← message.create  答案首帧                （流式开启时）
← message.update  答案快照 ×N
← message.update  答案 + context_usage    （终帧）
← typing.stop
```

- **流式关闭时**（`channels.pico.streaming.enabled=false`，BeginStream 失败走降级）：无答案首帧/快照，直接一条完整 `message.create`。TUI 两种形态都要兼容（状态机按消息驱动，不依赖流式开启）。
- `kind` 缺省视为普通答案流（前端 `assistant-message-state.ts` 同语义）。
- 取消（`/stop` 或 stall watchdog hard abort）：`typing.stop` + 流式消息 `message.delete`；历史封口由服务端负责（fork 封口不抹除语义），TUI 无感知。

## 4. 界面布局

### 4.1 MVP 布局（单会话 + 状态栏）

```
┌──────────────────────────────────────────────────────────────────────┐
│ picoclaw tui · ● connected · pico · glm-4.7 · ctx 34% (87k/256k)      │  ← statusbar（1 行）
├──────────────────────────────────────────────────────────────────────┤
│ ▊ 你: 帮我看下 gateway 的日志为什么报 429                              │
│                                                                        │
│ 💭 思考 · 已折叠 312 字                                       [Ctrl+O] │  ← thought 折叠块（灰）
│ ⚙ exec  journalctl --user -u picoclaw -n 50                     ✓     │  ← tool_calls 行（绿 ✓）
│ 🔧 exec 运行中…                                                        │  ← 工具反馈行（动画）
│                                                                        │
│ 模型回复──────────────────────────────────────────────                 │
│ 从日志看，429 来自中台网关的配额窗口。已经做了两件事：                    │  ← 答案（流式期间低亮度纯文本；
│   1. 触发了 fallback 到备用候选…                                        │    终帧 glamour 渲染）
│                                                                        │
│ ⏱ 本轮 1m42s · in 12.3k / out 1.1k tok                                 │  ← usage 行（dim）
├──────────────────────────────────────────────────────────────────────┤
│ ❯ 输入消息（Enter 发送 · Shift+Enter 换行 · Esc 停止回合）              │  ← composer（3 行起，自适应）
└──────────────────────────────────────────────────────────────────────┘
```

- **statusbar**：连接态（`● connected / ○ reconnecting / × error`）、渠道、当前模型（取 `model_name` payload，会话中随 `/switch` 刷新）、context 用量（`context_usage.used_percent` + tokens）。
- **时间线**（viewport 滚动，PgUp/PgDn / 滚轮）：
  - 用户消息：▊ 前缀 + 原文；steering 发送（generating 期间）加「↩ 并入当前回合」徽标（对齐 web 端 steering badge）。
  - thought：折叠块，流式期间展开**尾部 3 行**，`Ctrl+O` 切换展开/折叠（对齐 fork「平铺推理轮次可读」的精神，但终端从简）。
  - tool_calls：单行 `⚙ name` + arguments 首行截断 + 绿 ✓。
  - 工具反馈：单行动画行，定稿后保留一行摘要。
  - progress_note：**不进时间线**，statusbar 旁或 composer 上方一行覆盖式状态（「⏱ 3m 无新进展 · 600s 无响应将自动收尾」）。
  - 答案：流式期间纯文本低亮度（避免每 delta 一次 markdown 重解析的闪烁与开销），终帧 glamour 渲染 + 复制友好。
- **composer**：bubbles textarea，多行；generating 期间仍可输入（发送即 steering），placeholder 提示切换为「（回合进行中，发送将并入当前回合）」。
- **快捷键与指令**：完整清单见 §4.3（`Esc` 停止、`Ctrl+N` 新会话、`Ctrl+O` thought 折叠、`Ctrl+C` 语义、`:` 本地命令集等）。

### 4.2 Phase 2：会话侧栏

```
┌ sessions ──┐┌──────────────────────────────────────────────┐
│ ● pico     ││  （时间线，同 MVP）                            │
│   …今天     ││                                              │
│ ◆ feishu   ││                                              │
│   …会话标题 ││                                              │
│ ◆ telegram ││                                              │
└────────────┘└──────────────────────────────────────────────┘
```

`Ctrl+S` 开关；`↑↓` 选择、`Enter` 切换（断开当前 WS → 载入历史 → 以新 session_id 重连，对齐 web 端 `switchChatSession` 流程）；跨渠道会话带徽标（fork 全渠道会话语义），非 pico 会话**只读**（composer 禁用，对齐 web 端 `nonPicoSession`）。

### 4.3 指令集（快捷键 + `:` 本地命令）

TUI 有两层指令：**快捷键**覆盖高频动作；**`:` 本地命令**覆盖低频/参数化动作。与服务端斜杠命令（`/` 前缀，经 `message.send` 直通 bus→agent 命令路径）互不劫持——`/` 提示服务端命令（`commands.BuiltinDefinitions` 18 个），`:` 提示本地命令，两套补全独立。

#### 快捷键

| 键 | 作用 | 阶段 |
|---|---|---|
| `Enter` | 发送 | P1 |
| `Shift+Enter` | 换行（依赖 kitty keyboard protocol / Windows Terminal；不支持的终端降级 `Alt+Enter`） | P1 |
| `Esc` | 生成中：停止回合（合成 `/stop`）；有浮层：关闭浮层；否则无操作 | P1 |
| `Ctrl+C` | 生成中：第一次停回合（等同 `Esc`）、再按退出；空闲：连按两次退出 | P1 |
| `Ctrl+N` | 新会话（生成中禁用） | P1 |
| `Ctrl+O` | 展开/折叠最近一条 thought 折叠块 | P1 |
| `PgUp`/`PgDn`/`Home`/`End` | 时间线滚动/跳转 | P1 |
| `Ctrl+L` | 重绘（终端标准习惯） | P1 |
| `Ctrl+A`/`E`/`U`/`K`/`W` | 输入行编辑（readline 惯例：行首/行尾/删到行首/删到行尾/删前词） | P1 |
| `?`（composer 为空时） | 帮助浮层（键位 + 命令总览） | P2 |
| `Ctrl+S` | 会话侧栏开关 | P2（随侧栏） |
| `Ctrl+R` | 输入框回溯本会话已发送历史（本地缓存，非服务端） | P3 |
| `Ctrl+P` | 命令面板（快捷键 + `:` 命令 + `/` 命令统一模糊搜索） | P3 |

#### `:` 本地命令

`:` 前缀 + Tab 补全 + `↑↓` 历史循环。命令分两类：**本地型**（纯客户端动作）与**合成型**（透明地发送一条服务端斜杠消息，回复照常入时间线）。

| 命令 | 作用 | 类型 | 阶段 |
|---|---|---|---|
| `:help` / `:h` | 键位与命令帮助浮层 | 本地 | P2 |
| `:q` / `:quit` | 退出 | 本地 | P2 |
| `:stop` | 停止当前回合 | 合成 `/stop` | P2 |
| `:new` | 新会话（重置 session_id 重连，等同 `Ctrl+N`） | 本地 | P2 |
| `:ls` / `:sessions` | 打开会话侧栏 | 本地 | P2（随侧栏） |
| `:model [名]` | 无参显示当前模型；有参切换 | 合成 `/switch model to 名`（`cmd_switch.go` 实际语法） | P2 |
| `:status` | 运行状态总览 | 合成 `/status` | P2 |
| `:context` | 上下文构成详情 | 合成 `/context` | P2 |
| `:title <文本>` | 设置会话标题 | 合成 `/title` | P2 |
| `:reload` | 重载配置 | 合成 `/reload` | P2 |
| `:usage` | 最近 `context_usage`/`usage` 详情浮层 | 本地 | P2 |
| `:copy [n]` | 复制第 n 条（默认最后一条）答案到剪贴板（OSC52，ssh/tmux 远程可用；终端不支持时提示降级） | 本地 | P2 |
| `:raw` | 答案「markdown 渲染 / 原始文本」全局切换（复制友好） | 本地 | P2 |
| `:cron …` | 定时任务管理（list/suggest/accept/dismiss） | 合成 `/cron …` | P3 |

设计规则：

- **合成型命令的呈现**：时间线里显示为 dim 动作行（如「⏹ 已请求停止」「⇄ 切换模型 → xxx」），不伪装成用户消息；服务端回复照常入时间线。同一会话的其他客户端（web/另一 TUI）能否看到原始斜杠文本，以服务端命令拦截与历史落盘行为为准，TUI 不额外广播。
- **生成中可用性**：`:stop` 生成中可用；合成型管理命令（`:model`/`:new`）在生成中发送会受 fork busy 语义约束（`/switch`、`/new` 返回 busy 提示），TUI 如实展示回复即可，不做本地拦截。
- **`Esc` 与 `:stop` 等价**：同一合成路径，时间线呈现一致。
- `:copy` 用 OSC52 转义序列，不依赖系统剪贴板进程（ssh 会话友好）；tmux 下需 `set-clipboard` 支持，检测失败仅提示不报错。

## 5. 会话列表与历史（唯一的补短板项）

pico 协议没有历史拉取消息；web 前端靠 launcher 的 `/api/sessions`、`/api/sessions/{id}`（读 `<workspace>/sessions/*.jsonl`，`web/backend/api/session.go`）。网关自身没有该 API（mux 上只有 `/health` `/ready` `/reload` `/mcp/status` + 渠道 webhook）。

分两期：

- **MVP（同机直读）**：TUI 进程与网关同机（本 fork 的实际部署形态），直接读 `agents.defaults.workspace` 下的 `sessions/`：
  - 定位目录：`config.LoadConfig` → `agents.defaults.workspace`（空则 `~/.picoclaw/workspace`）+ `/sessions`（与 `resolveSessionsDir` 同规则，`web/backend/api/session.go:855`）。
  - 列表与去重：参照 `findJSONLSessions` + `(channel, id)` 去重语义（fork 修过「幽灵 pico 会话遮蔽」bug，照抄最新实现，勿自行发明）。
  - 历史读取：`readSessionMessages` 同语义（providers.Message → 时间线消息，thought/kind 还原同 §3.3 映射）。
  - 跨机降级：读不到 sessions 目录时侧栏只显示当前会话，聊天功能不受影响。
  - 只读边界：TUI 对 sessions 目录**只读**，不写任何文件。
- **Phase 2（网关只读 API）**：在网关 mux 挂 `GET /pico/api/sessions`、`GET /pico/api/sessions/{id}`（复用 pico token 鉴权），扫描逻辑从 `web/backend/api/session.go` 抽成公共包（建议 `pkg/session/history` 或并入 `pkg/memory`），launcher 与 TUI 共用单一实现。此项独立评审——涉及 web/backend 依赖方向调整，不阻塞 TUI。

## 6. 代码落点

```
pkg/picoclient/                    ← 新包：pico 协议 WS 客户端（可独立于 TUI 复用）
  client.go                          Dial/Close/Send/SendMedia/Events/重连循环
  events.go                          PicoMessage → typed 事件（Event{Kind, ...}），状态无关纯解码
  client_test.go                     对 httptest 假服务端 + 组合真实 PicoChannel 的事件序列断言

cmd/picoclaw/internal/tui/         ← 新包：cobra 子命令 + bubbletea
  command.go                         NewTUICommand()：flags（--gateway-url/--token/--session/--no-color 透传）
  app.go                             根 Model：Update 循环、消息路由到各视图
  state.go                           turn 状态机（generating/steering/事件归并），终端无关，可单测
  chatview.go                        时间线 viewport
  composer.go                        输入框
  statusbar.go                       状态栏
  render.go                          markdown（glamour）/工具行/thought 折块的样式与渲染
  sidebar.go                         （Phase 2）

cmd/picoclaw/main.go               ← 注册 tui 子命令（1 行）
```

import 方向：`internal/tui → pkg/picoclient → pkg/channels/pico`（仅取 `protocol.go` 导出常量/类型；cmd/picoclaw 本就 blank-import 全部渠道，无体积与环问题）→ `pkg/config`。

不改动：`pkg/channels/pico` 服务端、`pkg/agent`、`pkg/commands`、web 前后端。

## 7. 配置与安全

- 零新配置项：token / host / port / workspace 全部来自既有 `~/.picoclaw/config.json`（version 3）。
- token 等价网关完全权限（可代理命令执行）。设计约束：
  - TUI 不写 token 到任何新文件；`--token` flag 会进 shell history，文档标注仅调试用。
  - 远程网关场景强制 wss 提示（`--gateway-url` 为 ws:// 且非 localhost 时打印警告）。
- 会话目录直读保持只读；不触碰 `protect_system_paths` 语义（那是 agent 文件工具的防护，TUI 不经过）。

## 8. 与 fork 关键行为的对齐清单（实现时勿破坏）

| fork 行为 | TUI 侧对策 |
|---|---|
| turn 活跃发送 = steering（并入当前回合） | composer 生成中不禁用，发送后加「↩ 并入当前回合」徽标 |
| `/stop` 封口保留（不抹除历史） | `Esc` → 发送 `/stop` 文本，无本地取消逻辑 |
| progress_note 非终答 | 不入时间线、不清 generating 态（防「假完成」，同 web 端 B 修复） |
| `/new` 归档轮换 + 模型重置 | `Ctrl+N` 发送 `/new` 后**重生成 session_id 重连**（pico 会话由客户端 UUID 定界，`/new` 的 agent 归档与 TUI 新会话各自成立，互不冲突） |
| turn 卡死 stall watchdog（600s） | 服务端负责收尾；TUI 仅展示 progress_note 文案，**不实现本地看门狗**（避免双watchdog误报） |
| 断线重连 | 指数退避（1s 起 5s 封顶，同 web 端 `scheduleReconnect`）；重连后 session_id 不变，服务端续接会话 |
| 清理管线/输出预算 | 服务端出口已处理，TUI 不再做工具输出清理 |

## 9. 测试计划

- **pkg/picoclient 单测**（CI 可跑，Windows/Linux 双平台）：
  - 组合真实 `PicoChannel`（httptest 起 mux，参照 `pico_test.go` 既有模式）：鉴权三方式、事件序列断言（§3.4）、多连接广播（web + TUI 同 session）、`message.update` 快照语义、error request_id 关联。
  - 重连：服务端断开后指数退避重连成功。
- **tui state 单测**（终端无关）：状态机对 §3.3 每种事件的归并结果；progress_note 不清 generating；error 移除 pending。
- **手测清单**：Windows Terminal / 老式 conhost / ssh + tmux（Linux）；流式开关两种配置；`/stop` 中止；stall watchdog 触发时文案；窗口缩放（bubbletea WindowSizeMsg 重排）。
- 回归：`go build ./...`、`go test ./pkg/picoclient/... ./pkg/channels/pico/...`；不触碰 fork 既有测试锚点。

## 10. 分期与工作量

| 阶段 | 内容 | 预估 |
|---|---|---|
| P1 MVP | pkg/picoclient + chat 视图（流式/thought/tool_calls/工具反馈/progress_note）+ composer + statusbar + Esc 停止 + 重连 + §4.3 基础快捷键（Enter/Shift+Enter/Esc/Ctrl+C/Ctrl+N/Ctrl+O/滚动/Ctrl+L/行编辑） | 2–3 天 |
| P2 | 会话侧栏（同机直读历史 + 全渠道徽标 + 只读态）+ markdown 终帧渲染 + `/` 命令提示（读 `commands.BuiltinDefinitions`）+ usage 行 + `:` 本地命令集（help/quit/stop/new/ls/model/status/context/title/reload/usage/copy/raw）+ `?` 帮助浮层 | +2~3 天 |
| P3（可选） | 网关只读 sessions API（抽公共包，跨机）+ 图片粘贴发送 + 通知（回合完成 bell）+ Ctrl+R 输入回溯 / Ctrl+P 命令面板 / `:cron` | 按需 |

## 11. 风险与开放问题

1. **bubbletea v2 相对年轻**（v2.0.x）：glab / Loki 已生产使用，风险可控；若实现期遇阻塞，降级 v1 线（`github.com/charmbracelet/bubbletea v1.3.x`）的成本限于 import 路径与少数 API。
2. **老式 conhost（Windows）**：bubbletea 在 Windows Terminal 下体验最佳；conhost 降级可用但渲染降质（ANSI 能力弱）。设计不专为 conhost 优化。
3. **glamour 长文本渲染开销**：答案每帧重渲染在长回答下可能卡顿——已用「流式纯文本、终帧才 markdown」规避；若终帧仍慢，退化为段落级渲染。
4. **sessions 直读与 launcher 的实现漂移**：MVP 复制语义存在双实现漂移风险——P3 抽公共包是根治项，优先级应视 TUI 使用频率决定。
5. **开放问题**：`gateway.host` 配置为 `0.0.0.0` 时 TUI 默认连 `127.0.0.1` 的推导是否需要显式配置位（倾向不加，`--gateway-url` 兜底）；多 agent 配置（`agents.list`）下 TUI 是否需要 agent 选择器（当前 pico 渠道默认 agent，暂不做）。
