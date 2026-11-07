# TUI P3 设计（问询浮层 + 外部编辑器 + bell + 图片粘贴）

> 状态：**已实现**（2026-11-07）：四项全部落地（问询浮层/外部编辑器/bell/图片粘贴），服务端零改动；状态机与平台读取层均有单测锁定。日期：2026-11-07。
> 前置文档：`docs/design/tui-client-design.zh.md`（原始设计）、`docs/design/2026-11-07-tui-p2-design.zh.md`（P2）。本文覆盖 P3 清单中获准的四项（评审意见：问询浮层+外部编辑器+bell 一组，图片粘贴按需）。**全部零服务端改动。**

## 0. 结论先行

| 项 | 交互 | 服务端依赖 |
|---|---|---|
| 问询浮层 | ask 卡到达自动弹出步进式答题（移植 web `ask-flow.ts` 语义），Esc 退回时间线卡，`:ask` 重开 | 无——回复仍是纯文本 `message.send` |
| 外部编辑器 | `Ctrl+G` → `$EDITOR`/`$VISUAL`/notepad|vi 编辑临时文件，存盘退出回填 composer（不自动发送） | 无 |
| bell 通知 | 回合结束（typing.stop）且终端失焦时 `\a`；审批/问询卡到达时必响 | 无 |
| 图片粘贴 | `Ctrl+V` 读系统剪贴板图片 → dataURL → `message.send` 的 media 数组 | 无（服务端 20MB 上限既有） |

## 1. 问询浮层（结构化答题）

### 1.1 协议与语义（对齐 web，跨语言契约）

- 输入：`message.create` payload `ask {questions, timeout_ms}`（P2 已解析为 `picoclient.AskInfo` 并渲染时间线卡）。
- 回复：**普通文本**。多题=每题一行（行 i=题 i，超出折叠末题），单题=整条消息（换行合法）；多选用 `; ` 连接；"其他…"文本追加在所选标签后；内嵌换行压平为 `；`（`collectAskReply` + web `ask-flow.ts` 锁定语义）。
- 跳过 = 发语义化委托消息（zh.json 锁定文案，不发字面 `/skip`）：`你决定吧——按最优默认方案继续，并告诉我你依据的假设`。

### 1.2 状态机（`askflow.go`，纯函数可单测，移植 ask-flow.ts）

```
mode: step（逐题）→ review（回顾）→ submit
每题状态 AskAnswer{picked []string, other string}；sentinel 其他 = "__other__"
```

- **单选**：Enter 选定即前进（末题 → review）；选"其他…"不自动前进（等文本填完）。
- **多选**：Space/Enter 切换选中；「✓ 确认本题」行收题前进。
- **无选项题**：直接自由文本输入。
- **review**：列出各题与答案（未答题显「（未答）」），↑/↓ 选题回改；「✓ 提交」行发送（任一题未答则该行禁用并提示）。
- 已封存（Sealed 非空）或超时的卡不可再交互（时间线卡保持 P2 的封存摘要行）；答题中若回执从另一端（web/IM）封存了卡，浮层自动关闭并提示（防重复作答，迟到回复流入 steering）。

### 1.3 键位与渲染

| 键 | 行为 |
|---|---|
| `↑`/`↓` | 选项行导航（含「其他…」「确认本题」「你决定」「✓ 提交」行） |
| `Enter` | 单选=选定前进；多选=切换；动作行=执行 |
| `Space` | 多选切换（与 Enter 等价） |
| `A`–`F` | 字母快选（有选项时） |
| `Esc` | 输入"其他"文本中=结束输入；step 首题=关浮层（卡留在时间线，`:ask` 重开）；其余=回上一题 |
| `:ask` | 重开最近一张未封存问询卡 |

浮层渲染：题头 `❓ <header> · n/total`，选项行 = 字母 chip + label — description；多选已选 = ✓ 前缀 + accent 底；"其他"输入行复用浮层内嵌文本输入（一行，Enter 结束）。超时秒数静态展示（不做倒计时动画）。

### 1.4 提交拼装（buildSubmitBody 同语义）

```
resolved[i] = picked(去 sentinel) + (其他? [other]) join("; ")；空 = 未答
多题：每题 flattenLine（内嵌换行→"；"）后按行拼接；单题：整条消息原文
```

发送走既有 `AddUser` + `client.Send`（时间线照常显示用户消息，回执封存卡——P2 机制不变）。

## 2. 外部编辑器（Ctrl+G）

- 编辑器解析：`$EDITOR` → `$VISUAL` → 平台默认（Windows `notepad`，其余 `vi`）。值含空格按 shell 字段切分（支持 `code --wait`）。
- Windows 下经 `cmd /c` 包一层（.cmd shim 如 `code.cmd` 无法被 exec 直启；pi 同款 shell 处理）。
- 流程：composer 当前内容写 `%TMP%/picoclaw-ask-<rand>/prompt.md` → `tea.ExecProcess`（TUI 暂停，终端交还编辑器）→ 退出码 0：读回文件（去 BOM、去单个尾换行）回填 composer 并 re-focus，**不自动发送**；非 0 或启动失败：action 行提示「编辑器退出码 N，内容未变」。
- 只读会话禁用（composer 本就禁用）。

## 3. bell 通知

- 触发：①`typing.stop`（回合结束）**且终端失焦**（`tea.BlurMsg`/`tea.FocusMsg` 跟踪焦点；未知焦点态=响，宁可多响）；②审批/问询卡到达（打断式，必响）。
- 实现：bubbletea v2 无原生 Bell 命令——tea.Cmd 里直接向 `os.Stdout` 写 `\a`。BEL 不可见、与渲染帧交错无害（最坏提前/推后一帧响）。
- 不做配置开关（终端侧自有 bell 静音设置）；tmux 下需 `set -g bell-on-alert on`（帮助浮层注明）。

## 4. 图片粘贴（Ctrl+V）

- 键：`Ctrl+V`（raw 模式即 `\x16`，bubbletea 映射为 Ctrl+v；文本粘贴走 bracketed paste 的 `tea.PasteMsg`，互不冲突）。
- 平台剪贴板读取（对齐 pi `clipboard-image.ts` 的命令矩阵，Go 实现）：
  - **Windows**：`powershell -NoProfile -Command` + `System.Windows.Forms.Clipboard::GetImage()` 存临时 PNG 再读（STA：powershell.exe v5.1 控制台默认 STA）。
  - **macOS**：`osascript -e 'the clipboard as «class PNGf»'` → hex 解码为 PNG。
  - **Linux X11**：`xclip -selection clipboard -t TARGETS -o` 选型后按型读；**Wayland**：`wl-paste --list-types` / `--type`。两者按可用性自动探测。
  - **ssh/tmux 远程**：无本地剪贴板访问 → action 行提示（OSC52 只覆盖文本写回，读不了图片）。
- 限制：解码后 >20MB 拒绝（服务端 `DefaultMaxMediaSize`，提前在客户端挡）；非图片剪贴板 → 静默提示「剪贴板无图片」。
- 发送：`data:image/png;base64,…` → `client.Send(ctx, "", []string{dataURL})`——对齐 web（content 可空串 + media 数组）；时间线显示用户行 + 📎。

## 5. 键位总表增量

| 键 | 作用 | 备注 |
|---|---|---|
| `Ctrl+G` | 外部编辑器编辑当前输入 | pi 同键位 |
| `Ctrl+V` | 粘贴剪贴板图片并发送 | 文本粘贴仍走终端原生 bracketed paste |
| `:ask` | 重开最近未封存问询卡 | 本地命令 |
| （浮层自动弹出） | ask 卡到达时 | Esc 关闭 |

## 6. 代码落点

```
cmd/picoclaw/internal/tui/
  askflow.go      新增：答题状态机纯函数（移植 ask-flow.ts，可单测）
  overlay.go      + overlayAsk：渲染（选项/chip/review/输入行）与键路由接入
  editor.go       新增：外部编辑器（editor 解析、临时文件、ExecProcess）
  clipboard.go    新增：平台剪贴板图片读取 → dataURL（可注入命令跑单测）
  app.go          + Ctrl+G/Ctrl+V/:ask、焦点跟踪、bell 触发、ask 自动弹层
  state.go        —（无改动；封存/时间线机制复用 P2）
```

## 7. 测试计划

- `askflow_test`：单选即选即进/末题进 review；多选 toggle+确认；其他 sentinel（只点不填=未答）；buildSubmitBody 拼装（多题每题一行、flattenLine、单题原文、未答=null）；已答 review 回改。
- `clipboard_test`：注入假命令（假 powershell/osascript 输出 hex）→ PNG 字节与 dataURL；超限拒绝；无图提示。
- `editor_test`：编辑器命令解析（$EDITOR 带参、回退链、Windows cmd /c 包装）；临时文件读写与 BOM/尾换行清理（不真起编辑器，注入 cmd）。
- 手测：Windows Terminal（Ctrl+V 截图粘贴、notepad、bell 失焦响）；问询卡全流程（单选/多选/其他/你决定/超时后 :ask 不可交互）；ssh+tmux（bell 与图片粘贴的降级提示）。

## 8. 风险与不做

- PowerShell 启动 ~1s（Ctrl+V 有感延迟）：提示「读取剪贴板…」action 行；失败静默降级。
- osascript hex 解析对非 PNG 剪贴板（截图工具写 PNGf，浏览器复制常是 TIFF）→ 检测 `«class PNGf»` 失败时提示，不做格式转换（Photon 级转码不在本轮）。
- 「你决定」文案与 web zh.json 锁定一致，改词要两端同步。
- 不做：bell 配置开关、图片格式转码、粘贴历史、问询卡倒计时动画。
