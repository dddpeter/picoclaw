# TUI P2 完善设计（参考 pi 交互模式）

> 状态：**已实现**（2026-11-07）：全部 P2 项落地并通过测试（布局不变量/协议补全/卡片封存/补全状态机/会话扫描/渲染缓存）；键位与命令集以本文为准。日期：2026-11-07。
> 前置文档：`docs/design/tui-client-design.zh.md`（P1 已实现的原始设计；本文是其 P2 的修订与扩展——会话侧栏改为**浮层选择器**，键位与命令集按本文为准）。
> 参考对象：`D:\code\pi`（earendil-works/pi，packages/tui + packages/coding-agent/src/modes/interactive）。本文所有"pi 式"均指该实现的交互模式。

## 0. 结论先行

P1（2026-10-04）交付了可用的网关客户端聊天界面。本轮 P2 参考 pi 的交互设计补齐六类能力：

1. **协议补全**：断线补发（`?last_seq=N`，服务端 89e563aa 已就绪）、`turn.status` 连接态恢复、审批卡（approval）/问询卡（ask）渲染与回执封存；
2. **命令补全浮层**：`/` 服务端命令与 `:` 本地命令的 fuzzy 补全下拉（pi 的核心编辑器体验）；
3. **会话选择浮层**：替代原设计的常驻侧栏——Ctrl+S 打开 fuzzy 可搜的会话列表（pi session-selector 模式）；
4. **Markdown 终帧渲染**：glamour v2 + `:raw` 切换 + 渲染缓存（原设计 P2 项）；
5. **`:命令集扩充与帮助浮层**：`:model`（模型浮层）`:status` `:context` `:title` `:reload` `:usage` `:copy` `:raw` `:ls`；`?`/`:help` 全量帮助浮层；
6. **编辑与浏览体验**：Ctrl+X 复制最近答案（OSC52）、Ctrl+T 工具输出展开（diff 着色）、↑/↓ 输入回溯、鼠标滚轮、Item 级渲染缓存（长会话性能）。

明确不做（及理由）见 §9。

## 1. pi → picoclaw 特性映射与取舍

| pi 特性（代码锚点） | 取舍 | 说明 |
|---|---|---|
| slash 命令补全下拉 + fuzzy（`autocomplete.ts`、`fuzzy.ts`、editor SelectList） | **采纳** | picoclaw 有两层命令（`/` 服务端、`:` 本地），分别补全 |
| session selector 浮层（`session-selector.ts`） | **采纳，替代侧栏** | 聊天主区在窄终端很宝贵；浮层 + fuzzy 搜索信息密度更高，pi 同款交互。原设计 §4.2 的常驻侧栏作废 |
| model selector 浮层（`model-selector.ts`） | **简化采纳** | 客户端无模型目录 API；浮层只列 config 中 default agent 的 primary+fallbacks，选中合成 `/switch model to <id>` |
| Ctrl+O 展开/折叠工具输出（`app.tools.expand`） | **采纳，换键位** | picoclaw 的 Ctrl+O 已是"思考折叠"（P1 文档化的键位，不回改）；工具输出展开用 **Ctrl+T**（pi 的 Ctrl+T 是 thinking 切换，语义族一致：T=Tool/Thinking 详展） |
| 复制最近回复 Ctrl+X（`app.message.copy`） | **采纳** | bubbletea v2 原生 `tea.SetClipboard`（OSC52），ssh/tmux 可用；另有 `:copy [n]` |
| 输入历史 ↑/↓ 回溯（editor history） | **采纳（简化）** | 仅单行 composer 时劫持 ↑/↓；进程内存 ring，不落盘 |
| 鼠标滚轮（TuiAltScreen + wheel-scroll.ts） | **采纳** | `View.MouseMode = MouseModeCellMotion`，bubbles viewport 已支持 wheel；帮助里注明 Shift+拖动选文本 |
| 键位帮助（`keybinding-hints.ts`） | **采纳** | `?`（composer 空时）/`:help` 打开浮层：快捷键 + `:` 命令 + `/` 命令总表（命令表读 `commands.BuiltinDefinitions()`） |
| 流式纯文本 + 终帧 Markdown（AssistantMessageComponent 的 MarkdownSlot 复用） | **采纳** | glamour 终帧一次渲染并缓存；流式期间保持 dim 纯文本（P1 行为） |
| diff 行着色（`diff.ts`） | **轻量采纳** | 不做 word-level diff；展开的工具输出里 `+`/`-` 行首着色（绿/红），足够读 exec 输出 |
| queue follow-up（Alt+Enter 排队） | **不采纳** | fork 的 steering 语义已覆盖"生成中发送"（并入当前回合），排队是另一套心智模型，冲突 |
| bracketed paste 粘贴标记（editor paste markers） | **不采纳** | 聊天场景多行粘贴直接发送是合法输入；标记系统为代码编辑器设计 |
| 外部编辑器 Ctrl+G | P3 | `tea.ExecProcess` 可行，但非高频 |
| 图片粘贴 / OSC 图像（terminal-image.ts） | P3 | 原设计 P3 保留 |
| 命令面板 Ctrl+P、主题切换 | P3 | 原设计 P3 保留 |

## 2. 协议补全（picoclient + state）

### 2.1 断线补发（last_seq）

服务端（89e563aa，trae borrowing §一）已支持：会话级广播帧带 `seq`（per-session 单调）；重连握手 `?last_seq=N` 触发缺口补发；缓冲淘汰时发 `stream.catchup {complete:false, dropped:n}` 边界帧。客户端配套：

- **`pkg/picoclient.Client`**：维护 `lastSeq map[sessionID]uint64`（进程内，随会话切换保留）；`Event.Seq > 0` 时更新当前会话的最大值；dial URL 追加 `&last_seq=N`（N>0 时）。`SetSessionID` 切换后自然带新会话的游标。
- **幂等保险**：`Event.Seq > 0 && Event.Seq <= lastSeq[session]` 的帧直接丢弃（服务端承诺恰好一次；这是双保险，防旧连接残留帧）。
- **`stream.catchup`**：`complete:true` → 无动作（补发完整）；`complete:false` → 时间线插入 action 行「⚠ 断线期间丢失 N 帧」并置 `State.HistoryStale = true`；**非生成中**时自动重读磁盘历史重建时间线（`LoadSessionHistory`，服务端 jsonl 是全量真源）；生成中不重建（避免抹掉流中状态），仅提示，turn 结束后下一次 catchup/操作时重试。

### 2.2 turn.status（连接即知回合状态）

连接后首帧 `turn.status {active:bool}`（octop borrowing §四）。客户端：`active=true` → `Generating=true`（GeneratingSince 若未知取当前时间），避免重连后"看不见正在进行的回合"；`active=false` 且本地 Generating → 清除（服务端视角回合已结束）。seq-less，不入 lastSeq。

### 2.3 审批卡（approval）与问询卡（ask）

协议：`message.create` payload 携带 `approval {tool, preview, timeout_ms}` 或 `ask {questions:[{question, header, options:[{label,description}], multi_select}], timeout_ms}`（`pkg/channels/pico/pico.go:460/480`）。回复通道是**普通文本**：审批回 `/approve`（变体 `always`/`all`/`off`）或 `/deny [原因]`（`pkg/agent/approval_hook.go` 锁定文案）；问询直接回复选项文本（多题每题一行，多选 `;` 分隔——`web/frontend/src/features/chat/ask-flow.ts answerText` 语义）。

TUI 侧（对齐 web 前端 approval.ts/ask.ts 的双通道解析）：

- **v1**：payload 块 → `Item` 挂 `Approval`/`Ask` 信息；**v0 兜底**：无 payload 时按固定文本特征识别（`⚠️ 需要批准：即将执行工具 ` 前缀等，跨语言契约见 approval_hook.go 注释）。
- **渲染**：审批卡 = 警示色边框块：`⚠ exec 审批请求` + preview 首行（截断）+ dim 提示「回复 /approve 或 /deny <原因>（可加 always/all）」+ 超时秒数。问询卡 = accent 边框块：header/question + 选项列表（label — description）+ dim 提示「回复选项文本或自由输入；多选用 ; 分隔」。
- **回执封存**：后续普通消息内容以 `✅`/`⛔`（审批回执）或 `📝 已收到回答`/`⏭️ 已跳过问询`/`⏰ 问询超过`/`❓ 当前没有待回答的问题`（问询回执）开头时：封存最近一张未封存的对应卡片（卡片降为 dim 单行「已批准/已拒绝/已回答」），该回执消息**不单独入时间线**（避免重复噪音，对齐 web seal 语义）。无未封存卡片时回执照常入时间线（孤儿回执）。
- **回答路径**：P2 只做卡片渲染 + 文本回复提示（不做成键选择浮层——结构化问询 UI 放 P3，见 §9）。

### 2.4 picoclient 事件结构扩展

```go
type Event struct {
    // …既有字段…
    Seq uint64 // per-session 单调序号；0 = seq-less 帧
    TurnActive *bool          // turn.status
    Approval   *ApprovalInfo  // v1 审批块
    Ask        *AskInfo       // v1 问询块
    Catchup    *CatchupInfo   // stream.catchup {complete, dropped}
}
```

## 3. 命令补全浮层（`/` 与 `:`）

### 3.1 触发与数据源

- **触发**：composer 内容以 `/` 或 `:` 开头（首字符，允许前导空白后跟随）。输入继续 → 实时过滤；补全激活期间 ↑/↓/Tab/Esc/F1 被补全层消费。
- **`/` 数据源**：`pkg/commands.BuiltinDefinitions()`（20 个，Name/Description/Usage/Aliases/SubCommands）。补全两级：命令名（含别名）→ 一级子命令（`/switch ` 后提示 `model`/`channel`；带 `ArgsUsage` 展示）。**只在首个词补全命令名，光标在其后时不再触发**（聊天内容里出现 `/path/to/file` 不弹补全：要求 `/` 位于行首）。
- **`:` 数据源**：本地命令静态表（§6 全集），含参数提示（`:model [名]`、`:copy [n]`、`:title <文本>`）。

### 3.2 fuzzy 过滤（移植 pi fuzzy.ts）

子序列匹配（query 每字符按序出现即可命中），打分：连续命中奖励（递增 ×5）、词边界命中奖励（-10，边界 = 空格/`-`/`_`/`.`/`/`/`:` 前）、间隙惩罚（×2）、位置轻微惩罚（×0.1）。分数升序排列，前 N=8 条展示。移植为 `fuzzy.go`（纯函数，无依赖，可单测；语义对齐 pi 的 `fuzzy.ts` 权重）。

### 3.3 键盘路由（补全激活时）

| 键 | 行为 |
|---|---|
| `↑`/`↓` | 移动选中项（循环） |
| `Tab` | 用选中项补全（替换当前命令词，光标置末尾），保持激活继续过滤 |
| `Enter` | 补全选中项并**关闭**浮层（不发送——pi 行为：Enter 先收下建议，再按 Enter 才发送；防止误发半截命令） |
| `Esc` | 关闭浮层（本回合输入不再自动触发，直到内容离开 `/`/`:` 前缀再回来） |
| 其他 | 透传 composer 编辑，浮层随内容刷新 |

### 3.4 渲染

composer 上边框线正上方浮层（盖住 timeline 底部），最多 8 行：`选中行 = accent 背景块 + 名称(bold) + description(dim)`，未选中 = muted；底部一行 dim 提示「↑↓ 选择 · Tab 补全 · Enter 选定 · Esc 关闭」。宽度 = 终端宽。

## 4. 会话选择浮层（Ctrl+S / `:ls`）

### 4.1 数据

扫描 `<workspace>/sessions/*.meta.json`（复用并泛化 `history.go` 的发现逻辑）：

- 条目 = `{key, channel, picoID, title, updatedAt, count}`。channel 从 meta.Scope 解析（无 scope 用 key 前缀启发：`agent:*:pico:*` → pico，其余按渠道段）；picoID 沿用 `picoSessionIDFromMeta`。
- 排序：updatedAt 降序；**当前会话置顶高亮 ●**。
- 列表上限 100 条（按更新时间截断），fuzzy 过滤字段 = title + key + channel。

### 4.2 交互

- `Ctrl+S` 或 `:ls`/`:sessions` 打开；生成中允许打开（查看无害），但切换动作在生成中被拒绝并提示（对齐 Ctrl+N 的 busy 语义——切走丢本地流视图，容易误操作）。
- 浮层高度 = min(列表+头尾, viewport 高-2)；内部滚动（↑/↓/PgUp/PgDn），fuzzy 查询输入直接复用 composer 文本（浮层激活时 composer 充当搜索框，placeholder 不变——顶部一行 dim「搜索：」提示）。
- `Enter`：pico 会话 → `SetSessionID(picoID)`（重连带新游标）+ `NewState()` + `LoadSessionHistory` 重建 + 落盘 `tui_session` + 关闭浮层。非 pico 会话 → 同样切换加载历史，但 **composer 只读**（blur + 输入提示「只读会话（<channel> 渠道）：发送禁用，Ctrl+S 切回」），Enter/发送无效。当前会话在列表中 → 标记 dim 不重复切换。
- 读不到 sessions 目录（跨机部署）：浮层只显示当前会话 + 提示行「未找到 workspace 会话目录（远程网关？）」。

## 5. Markdown 终帧渲染与渲染缓存

### 5.1 glamour

- 依赖新增：`charm.land/glamour/v2`（原设计 §2 已批准的选型；带来 goldmark/chroma 等，均为纯 Go）。
- 初始化：`glamour.NewTermRenderer(glamour.WithAutoStyle(), glamour.WithWordWrap(width))`；**按 width 缓存 renderer**（宽度变化才重建）。
- 时机：`ItemAnswer` 的 `Streaming` 从 true → false（typing.stop / error 收尾 / endStreaming）时渲染一次，结果缓存进 item 渲染缓存；此后滚动/重绘零成本。失败（glamour 报错）降级纯文本，不影响流程。
- 流式期间保持现状（dim 纯文本，P1 行为——避免每 delta 一次 markdown 重解析）。
- `:raw` 全局切换（默认渲染）：切换后所有答案按原文显示（复制友好、看裸格式）；实现上切换清空渲染缓存签名。

### 5.2 Item 级渲染缓存

现状：每次 `syncViewport`（每个事件/每 120ms spinner/每次按键）全量重渲 timeline。长会话 + glamour 后不可接受。方案：

- `State` 挂 `renderCache map[string]cachedRender`，key = item ID，签名 = `kind|content 长度+hash|width|flags(streaming/expanded/sealed/raw)`；签名一致直接复用上次的渲染字符串。
- 宽度变化 / `:raw` 切换 / `:clear` → 全量失效。glamour 结果再按 `width|content hash` 二级缓存（跨 item 复用重复内容）。
- 预期：稳态下每帧只重渲 footer/statusbar/editor 与最末 item。

## 6. `:` 本地命令集（终版）与帮助浮层

| 命令 | 类型 | 行为 |
|---|---|---|
| `:help` `:h` | 本地 | 打开帮助浮层（同 `?`） |
| `:q` `:quit` | 本地 | 退出 |
| `:stop` | 合成 | 发送 `/stop` |
| `:new` | 本地 | 新会话（生成中拒绝） |
| `:clear` | 本地 | 清空本地时间线（不动服务端历史） |
| `:ls` `:sessions` | 本地 | 打开会话浮层 |
| `:model [名]` | 合成/本地 | 无参 → 模型浮层；有参 → 发送 `/switch model to <名>` |
| `:status` `:context` `:reload` | 合成 | 直发对应服务端命令，回复入时间线 |
| `:title <文本>` | 合成 | 发送 `/title <文本>` |
| `:usage` | 本地 | 用量浮层：context 各水位（used/history/compress/summarize + 百分比条）+ 最近一轮 in/out/total |
| `:copy [n]` | 本地 | 复制倒数第 n 条答案（默认 1）到剪贴板（`tea.SetClipboard` OSC52；n 无效/无答案 → action 行提示） |
| `:raw` | 本地 | 切换答案 markdown 渲染/原文，action 行回显当前态 |

合成型命令的时间线呈现沿用 P1：dim action 行（「⇄ 切换模型 → xxx」）+ 服务端回复照常入时间线；不做本地拦截（busy 语义由服务端回答回显）。

**帮助浮层**（`?` composer 空闲为空时 / `:help`）：

- 三段：①快捷键表（§7 终版全量）；②`:` 本地命令表；③`/` 服务端命令表（`BuiltinDefinitions` 生成：`name — description`，aliases dim）。
- 底部 dim 行：鼠标（滚轮滚动 · Shift+拖动选择文本）+ `Esc/?` 关闭。
- 高度超出则滚动（↑/↓/PgUp/PgDn）。

## 7. 键位总表（P2 终版，★=新增）

| 键 | 作用 |
|---|---|
| `Enter` | 发送；补全激活时 = 选定补全项（不发送） |
| `Shift+Enter` / `Alt+Enter` | 换行（不变） |
| `Esc` | 生成中停止回合；浮层打开时关闭浮层（优先级：浮层 > 停止）★浮层语义 |
| `Ctrl+C` | 生成中先停回合，再按退出；空闲双击退出（不变） |
| `Ctrl+N` | 新会话（生成中拒绝，不变） |
| `Ctrl+O` | 展开/折叠最近 thought（不变） |
| `Ctrl+T` ★ | 展开/折叠最近工具反馈的完整输出（diff 行着色） |
| `Ctrl+S` ★ | 会话选择浮层 |
| `Ctrl+X` ★ | 复制最近一条答案（OSC52） |
| `?` ★ | composer 为空时打开帮助浮层 |
| `↑`/`↓` ★ | 补全/浮层导航；单行 composer 时回溯/前进输入历史（多行时不劫持） |
| `PgUp`/`PgDn`/`Home`/`End`/`Ctrl+L` | 不变（浮层打开时作用于浮层） |
| 鼠标滚轮 ★ | 时间线滚动（`MouseModeCellMotion`） |

## 8. 代码落点

```
pkg/picoclient/
  events.go        + Seq/TurnActive/Approval/Ask/Catchup 解码
  client.go        + lastSeq 游标与 ?last_seq=N dial、幂等丢弃

cmd/picoclaw/internal/tui/
  fuzzy.go         新增：pi fuzzy.ts 移植（纯函数）
  complete.go      新增：/ 与 : 补全数据源、建议状态机（终端无关，可单测）
  overlay.go       新增：浮层通用件（边框盒、列表行渲染、滚动、在 View 上的叠绘）
  sessions.go      新增：sessions 目录全量扫描（渠道/标题/时间/picoID），泛化 history.go
  markdown.go      新增：glamour 封装 + width/content 缓存
  state.go         + 审批/问询卡与回执封存、turn.status/catchup、输入历史 ring、:raw、渲染缓存
  render.go        + 卡片/补全/浮层渲染、diff 着色、渲染缓存消费
  app.go           + 键路由（补全/浮层/历史/复制/鼠标）、composer 只读态、View 叠绘与 MouseMode

go.mod             + charm.land/glamour/v2
```

不改动：`pkg/channels/pico` 服务端、`pkg/agent`、`pkg/commands`（只读引用）、web 前后端。fork 关键行为对齐清单（原设计 §8）继续有效。

## 9. 不做清单（本轮明确出界）

| 项 | 理由 |
|---|---|
| queue follow-up（pi Alt+Enter） | fork steering 已覆盖"生成中发送"，两套语义并存会混乱 |
| 粘贴标记 / bracketed paste 处理 | 聊天场景多行粘贴是合法直接输入；pi 的标记系统服务代码编辑 |
| 结构化问询浮层（选项点选） | 文本回复已完整可用；选项点选 UI 放 P3（需 multi_select/自定义输入/多题状态机） |
| 外部编辑器（Ctrl+G）、图片粘贴、Ctrl+P 面板、主题、`:cron` | 原设计 P3 保留 |
| 网关只读 sessions API | 原设计 P3；同机直读已覆盖本 fork 部署形态 |
| 消息时间戳显示 | 原设计未要求；时间线密度优先 |

## 10. 测试计划

- **pkg/picoclient**：Decode 新字段（approval/ask/seq/turn.status/catchup/坏 payload 不 panic）；last_seq dial 参数断言（假服务端握手 URL）；幂等丢弃（旧 seq 帧不产生事件）。
- **internal/tui 单测**（终端无关层）：fuzzy 权重语义（对齐 pi fuzzy 测试用例精神：连续>跳跃、词边界>中间）；补全状态机（触发/过滤/Tab/Enter/Esc/行首限制/子命令两级）；会话扫描（临时目录造 meta：渠道解析、picoID、排序、当前标记）；state 新行为（审批卡 v0/v1、回执封存与孤儿回执、turn.status 恢复/清除、catchup complete=false 提示 + 空闲重建、输入历史边界、:raw 缓存失效）；渲染缓存签名命中/失效。
- **手测**：Windows Terminal / conhost / ssh+tmux；OSC52（WT/tmux 需 set-clipboard on）；鼠标滚轮 + Shift 选择；窄终端（60 列）浮层不溢出；长会话（500+ 条目）滚动流畅度；断线补发（杀网关进程重启，TUI 自动补帧或提示）。
- 回归：`go build ./...`、`go test ./pkg/picoclient/... ./cmd/picoclaw/internal/tui/... ./pkg/channels/pico/...`（Windows 本机构建带 `-tags goolm`）。

## 11. 风险

1. **glamour 依赖体量**（chroma 等 ~10 个传递依赖）：原设计已批准；纯 Go、无 cgo，跨平台构建无影响。
2. **鼠标 cell-motion 与终端文本选择冲突**：启用后拖选需 Shift——帮助浮层明示；不做运行时开关（P3 可加 `--no-mouse`）。
3. **浮层叠绘正确性**：overlay 以字符串行替换画在 viewport 区域底部，需与 progress 行/editor 边框对齐；测试锁定布局行数计算。
4. **补发 + 本地状态重建的边界**：catchup complete=false 且生成中不重建——可能短暂缺帧，turn 结束后修正；文档化该取舍。
5. **`?` 触发与输入 `?` 字符冲突**：仅 composer **完全为空**时触发，输入中不劫持。
