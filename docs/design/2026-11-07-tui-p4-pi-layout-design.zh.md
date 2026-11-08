# TUI P4 设计（pi 布局一致性改造：主屏滚动 + pi 式布局）

> 状态：**已实现**（2026-11-07）：主屏滚动为默认（活区 + tea.Println 提交泵）、statusbar 删除、pi 式两行 footer、启动头、键位对调与 Ctrl+C/D 新语义全部落地；fullscreen 路径保留（--fullscreen）。实机冒烟验证：无 alt-screen 帧、无鼠标捕获、启动头正确打印。日期：2026-11-07。
> 前置：P2（`2026-11-07-tui-p2-design`）、P3（`2026-11-07-tui-p3-design`）。用户决策（2026-11-07）：①布局/渲染与 pi **一模一样**；②UI 文案**保留中文**；③键位**对调对齐 pi**（Ctrl+O=工具输出、Ctrl+T=thinking）；④Esc/Ctrl+C **照搬 pi**（Esc=打断、Ctrl+C=清空输入框、双击 Ctrl+C 退出、Ctrl+D 空输入退出）。

## 0. 结论先行

| 维度 | 现状（P1–P3） | P4 目标（pi 同款） |
|---|---|---|
| 渲染模式 | alt-screen 全屏应用，退出即清空 | **主屏滚动**（默认）：对话沉淀进终端 scrollback，滚轮/复制用终端原生；`--fullscreen` 保留旧模式 |
| 顶部 statusbar | 1 行（连接/会话/模型/ctx） | **删除**——信息并入 footer（pi 无顶栏） |
| footer | 2 行：stats+model / 快捷键提示 | **pi 式 2 行**：L1=`网关 · 会话标题 · 连接态`（dim）；L2=`↑in ↓out ctx%/total`（左，dim）+ 右对齐 `model` |
| 启动头 | 欢迎面板（空时间线时占屏） | pi 式**一次性启动头**打印进 scrollback：brand + 版本 + 一行 compact 快捷键提示（`?` 展开完整帮助浮层） |
| 滚动 | 应用内 viewport（PgUp/PgDn/滚轮） | 主屏=终端原生（不绑定键位、不开鼠标捕获）；fullscreen=保留现状 |
| 键位 | Ctrl+O=思考、Ctrl+T=工具输出；Ctrl+C=先停再退 | **对调** Ctrl+O/Ctrl+T；**Esc=停止回合（唯一打断键）**；Ctrl+C=清空输入框、双击退出；Ctrl+D=空输入退出 |
| thinking 折叠块 | 「已思考 N 字（Ctrl+O 展开）」 | pi 同款「Thinking…」（italic muted 折叠标签，无字数），Ctrl+T 切换 |

## 1. 主屏滚动架构（核心）

### 1.1 原理

bubbletea v2 主屏模式（`View.AltScreen=false`）+ **`tea.Println`（Cmd）**：打印在活区之上、不参与重绘、永久留在 scrollback（altscreen 下无效）。这正是经典"聊天流 + 固定底部输入"架构，行为与 pi 的 TuiMainScreen 一致：已滚出的内容不可变，活区内容可实时更新。

### 1.2 活区与提交边界

- **活区**（View 返回的内容，恒 ≤ 终端高）：`[未提交 timeline 尾部] + editor（上下边框，上边框嵌生成中状态）+ footer 2 行`。
- **提交规则**（`State.commitIndex`）：
  - item 进入终态（`Streaming=false` 的 thought/answer、tool_calls、action、error、media、已封存卡片）**且**活区行数超过 `终端高 - editor(2+composerLines) - footer(2) - 2` 时，从顶部按整 item 提交（`tea.Println` 逐行）；
  - turn 结束（typing.stop/error）：剩余可提交 item 全部提交，活区只留空隙 + 输入区；
  - 提交过的 item 从活区渲染范围剔除（`commitIndex` 之前的不再进 View）。
- **不可变约束**（与 pi 相同）：折叠/展开（Ctrl+O/T）、审批卡封存、`:raw` 切换只影响活区 item；已提交行不可改。帮助浮层注明。
- **切会话 / `:clear` / 历史重建**：`tea.ClearScreen` + 将磁盘历史一次性 Println 提交（pi 的 resume 同款行为），随后活区为空。
- **progress_note 覆盖行**：保留在活区 editor 上边框之上（生成中），随活区重绘。

### 1.3 与 fullscreen 的关系

`--fullscreen` flag（保留旧行为）：AltScreen=true、全量 timeline 进 viewport、应用内滚动（PgUp/PgDn/滚轮/CellMotion 鼠标）——即 P1–P3 的渲染路径原样保留。两模式共享：timeline 渲染管线（renderTimeline/渲染缓存）、editor、浮层、状态机；差异集中在 `syncViewport`（提交 vs SetContent）与 `View`/`relayout`/键位绑定两处分支。

### 1.4 主屏模式的键位/输入

- 去掉 PgUp/PgDn/Home/End 绑定与鼠标捕获（`MouseMode=MouseModeNone`）——终端原生滚动与文本选择。
- 其余键位、浮层（会话/模型/帮助/问询/补全）在两模式下一致：浮层在主屏下覆盖活区渲染（pi 的 selector 同款：占据底部区域，上方 scrollback 不动）。

## 2. 布局与渲染细节（对齐 pi 逐项）

| 项 | 实现 |
|---|---|
| footer L1 | dim：`<gateway host:port> · <会话标题截断> · <● connected/○ connecting/○ disconnected>`；断线时附 dial error 尾巴（现状保留，挪到此行） |
| footer L2 | dim：`↑in ↓out`（有值时）+ `ctx%/total (auto)`（≥70% warning、≥90% error 变色）+ 右对齐 `<model>`；生成中：右对齐改 `<spinner> <elapsed>` |
| editor | 上下全宽 `─` 边框（现状已是 pi 式）；上边框嵌 `⠋ 生成中 · Esc 停止`（主屏下同样成立）；只读态嵌只读提示（现状保留） |
| 启动头 | 主屏一次性打印：`鲎 Limulus <version>` + 一行 compact 提示（dim 键名 + muted 说明，` · ` 连接：`Esc 打断 · Ctrl+C 清空 · / 命令 · ? 帮助 · Ctrl+S 会话`）；fullscreen 模式保留现有欢迎面板 |
| 用户消息 | 全宽低饱和 bg 块（现状已是 pi 式，不动） |
| thinking | 折叠标签改 `Thinking…`（italic muted）；展开显示内容（Ctrl+T）；流式期间显示尾部 3 行（现状）+ 标签 `Thinking…` |
| 工具行 | `⚙ name args…`（bold name + dim args，现状接近，统一去掉 ✓ 后缀改 pi 式无状态单行）；工具反馈行维持 🔧 解析（服务端动画协议决定，不改） |
| 答案 | 流式 dim 纯文本 → 终帧 glamour（P2 决策不变；pi 流式全程 markdown 依赖其自研增量渲染器，glamour 每帧全量渲染会闪烁卡顿——文档记录偏差） |
| 空隙 | 用户块间 Spacer(1)（现状已是） |

## 3. 键位终版（P4）

| 键 | 作用 | 变更 |
|---|---|---|
| `Esc` | 停止回合（生成中）；浮层打开时关浮层 | 语义不变（= pi interrupt） |
| `Ctrl+C` | **清空输入框**；1s 内双击退出 | **变更**（原：生成中先停再退） |
| `Ctrl+D` | 输入为空时退出 | **新增**（pi `app.exit`） |
| `Ctrl+O` | 展开/折叠最近工具输出（diff 着色） | **对调**（原=思考） |
| `Ctrl+T` | 展开/折叠 thinking | **对调**（原=工具输出） |
| `Ctrl+S/X/G/V/N/L`、`?`、`:命令`、↑/↓ 回溯、浮层键 | 不变 | — |
| `PgUp/PgDn/Home/End`、滚轮 | 主屏=终端原生（不绑定）；fullscreen=应用内滚动 | 主屏下变更 |

生成中的停止入口：Esc（唯一打断键，提示语同步）。Ctrl+C 不再停止回合——启动头与帮助浮层的文案同步。

## 4. 代码落点

```
cmd/picoclaw/internal/tui/
  view.go        新增：主屏渲染编排（活区组装、footer、启动头、提交泵）
  app.go         View/relayout 双模式分支；键位变更（C/D/O/T）；syncViewport 主屏分支
  state.go       + commitIndex 提交边界与可提交行查询；thinking 标签
  render.go      footer 重排（pi 式 2 行）；statusbar 删除（fullscreen 欢迎面板保留）
  command.go     + --fullscreen flag
```

不改动：协议层（picoclient）、浮层/补全/问询状态机、渲染缓存、主题。

## 5. 测试计划

- state：提交边界推进（终态 item、turn 结束、活区超阈值）、切会话重置、不可变约束（活区外 item 的 Expanded 变更不触发重渲）。
- view：主屏活区高度恒 ≤ 终端高；footer 两行宽度约束；fullscreen 布局不变量回归（现有 TestView* 适配双模式）。
- 键位：Ctrl+C 清空/双击退出、Ctrl+D 空退出、O/T 对调后的折叠路由。
- 手测：Windows Terminal / ssh+tmux（滚轮=原生滚动、Shift 选择、tmux copy-mode 下滚轮回滚）；窄终端活区裁剪；长回合流式期间活区更新与提交节奏；切会话刷屏观感。

## 6. 风险与不做

1. **已提交行不可变**：pi 同款约束；`:raw`/折叠对滚出内容无效。帮助浮层注明。
2. **切会话全量历史打印**：长会话刷屏（pi resume 同款）；可接受，不做分页。
3. **流式纯文本 vs pi 全程 markdown**：维持 P2 决策（glamour 全量渲染性能不可行），文档记录偏差。
4. **Ctrl+C 不再停止回合**：行为变更，启动头/help 第一眼可见（`Esc 打断`），CHANGELOG 显著标注。
5. 不做：pi 的 `!` bash 模式、`/` 命令面板、cwd/git（无本地对应物）、英文文案（用户决策：中文）。

## 7. 修订记录（2026-11-08 使用反馈）

1. **思考/工具输出默认完整展开**（对齐 web `ThinkingBlock`/`ToolCallBlock` 的 `wrap=true` 语义）：`Item.Expanded` 改 `Item.Folded`（默认 false=全量），流式思考不再只显尾部 3 行；`Ctrl+T`/`Ctrl+O` 语义变为"折叠/展开"。
2. **模型常显**：footer L2 右侧模型位永在——生成中显示 `⠋ elapsed · model`（spinner 不再顶掉模型）；空闲回退 config 种子（`agents.defaults.model_name`）。
3. **色彩丰富化**：思考紫罗兰（italic，colThink 176）、工具名青色（bold，colTool 80）、用户块文字提亮（stText）、action 行 accent 圆点 + muted 正文、📎 accent。
