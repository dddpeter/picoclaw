# Web 端对话输出对齐 pi-web-ui：可行性分析与设计

> 状态：已实施（P0+P1 代码落地于 2026-09-20，见 §10 实施记录；决议见 §8）
> 日期：2026-09-20
> 目标：把 picoclaw web 端（`web/frontend`）的对话输出区改成与 `D:\code\pi-web-ui`（下称 pi-web-ui）**一模一样的形式**。

---

## 1. 背景与目标

pi-web-ui 是 pi/DSH agent 的浏览器驾驶舱，其对话输出是"单列时间线"风格：每条消息带头部元信息行（角色/模型/时间），思考块、工具调用卡、正文 markdown、bash 块按发生顺序混排在一条助手消息内，流式输出带稳定块渲染与闪烁光标。

picoclaw web 端现状是"卡片流"风格：thought / tool_calls / tool_feedback / 正文各自是独立的 shadcn 卡片，视觉与交互和 pi-web-ui 差异很大。

**本文回答两个问题**：① 能否做成一模一样（可行性）；② 怎么做（分层设计与实施计划）。

### 1.1 "一模一样"的边界（本设计的范围）

**核心范围（消息输出区）**：
- 消息列表布局：单列时间线、`.msg` 头部 meta 行（ROLE / 模型名 / 时间）
- 用户消息气泡（`.msg-user`：accent-soft 底、12px 圆角、markdown 保留换行）
- 助手消息：blocks 顺序渲染 —— thinking 卡、toolcall 卡、text 块、图片
- 思考块：折叠卡 + 状态文案（流式 `Thinking...` → `× lines · duration`）+ 斜体正文
- 工具调用卡：卡头（图标/工具名/状态图标 spinner·√·×/关键参数预览/耗时/复制键）、参数 JSON 高亮、终端风格输出区（横向滚动）、`Waiting for the model…` 微光、err 红边
- 正文 markdown：react-markdown + GFM + KaTeX + highlight.js；代码块（边框圆角、多行行号、右上圆形复制按钮）、表格/引用/链接样式
- 流式渲染：块级稳定渲染（`StreamMarkdown` 的 chop-until-stable 算法）+ `stream-cursor` 闪烁光标
- 错误消息：红卡样式

**非核心范围（外围功能，默认不做，见 §8 开放问题）**：
编辑重问、排队/插队（steer/followUp）气泡、问题导航 rail、会话内 Ctrl+F 搜索、旧消息折叠行（CollapsedMessage）、惰性窗口化虚拟列表、压缩摘要卡、mermaid/插件 fence 渲染、kill bash 按钮。

理由：这些依赖 pi-web-ui 服务端特有事件（steer 队列、toolStatuses/liveOutputs 实时事件、会话转录格式），picoclaw 协议没有对应信号，强行实现需要大规模服务端改造，超出"对话输出形式"的范畴。

---

## 2. 目标形式规格（pi-web-ui 对话输出解剖）

以下均来自对 pi-web-ui 源码的实测（`web/src/`）。

### 2.1 组件结构

```
MessageList.tsx                 消息列表（滚动容器 .messages）
├─ Message.tsx                  单条消息：.msg > .msg-meta + .msg-body
│  ├─ user                      .msg-user 气泡（Markdown hardBreaks）
│  ├─ assistant                 blocks 顺序渲染：
│  │   ├─ ThinkingBlock.tsx     .thinking 折叠卡
│  │   ├─ ToolCallBlock.tsx     .toolcall 折叠卡
│  │   ├─ Markdown.tsx          .md 全量渲染（最终态）
│  │   ├─ StreamMarkdown.tsx    流式渲染（稳定块 + 活动尾段 + 光标）
│  │   └─ image block           .msg-image
├─ copy-button.tsx              复制按钮（Copied! 反馈）
└─ code-lines.ts                代码块按逻辑行切分（行号 gutter）
```

### 2.2 消息头部与气泡

- `.msg`：`padding: 6px 0 14px`，无头像、无左右分栏。
- `.msg-meta`：flex baseline，`ROLE`（11px 大写加粗 text-faint）+ 模型名（11px mono，accent 色）+ 时间（右对齐，HH:MM）。
- 用户消息 `.msg-user .msg-body`：`background: var(--accent-soft); border: 1px solid rgba(139,92,246,.25); border-radius: 12px; padding: 10px 14px`，markdown 开启 hardBreaks（忠实保留用户换行）。
- 助手消息无气泡底色，blocks 直接铺排。

### 2.3 思考块（ThinkingBlock）

- 卡片：`.thinking` 圆角 8px、`border-soft`、`card-bg`。
- 头部 `.chead`（与工具卡共用骨架）：`[28px 折叠键][20px 图标][标题 Thinking][flex:1 spacer][28px 复制键]`，min-height 32px。
- 状态文案：流式 `Thinking` + `...` 动画（CSS steps）；完成 `Thinking · ×N lines`（折叠）或 `duration + N chars`（展开）。
- 正文：斜体 13px `text-dim`、`pre-wrap`，顶边框分隔。
- 交互：流式期间自动展开，结束自动折叠（组件内 useState）。

### 2.4 工具调用卡（ToolCallBlock）

- 卡片 `.toolcall`：圆角 8px、border、`card-bg`，`overflow:hidden`；err 态红边框。
- 卡头 `.chead`：折叠键 / 工具图标（react-icons Fi*）/ 工具名（mono 600）/ 状态图标（run=amber spinner、ok=green √、err=red ×）/ 关键参数预览（`.toolcall-path` 文件路径或 `.toolcall-cmd` 命令单行省略）/ spacer / 耗时（11px text-faint）/ 复制键。
- 正文：参数区 `prettyArgs`（JSON.parse + 2 空格缩进 + rehype-highlight，`max-height 200px`）；输出区 `OUTPUT` 小标签 + `pre`（终端风格 `white-space: pre` 横向滚动、`max-height 320px`、err 红字）。
- run 态无输出时：`Waiting for the model…`（呼吸微光）。
- 折叠默认：完成态默认折叠（`defaultOpen=false`），流式 run 态展开。

### 2.5 正文 markdown 与代码块

- 管线：`react-markdown` + `remark-gfm` + `remark-math` → `rehype-katex`（strict/throwOnError false）→ `rehype-highlight`（detect、ignoreMissing）。默认不开 rehype-raw（聊天消息 HTML 转义，天然防 XSS）。
- 代码块 `.codeblock`：pre 圆角 8px 边框背景；多行时行号 gutter（`.code-line` flex 行 + `.code-num` 右对齐）+ 自动换行主体；右上 28px 圆形复制按钮（hover 显现）。
- inline code：mono 12.5px、`bg-elev2` 底、圆角 4px。
- 链接外链 `target=_blank`；KaTeX 长公式横向滚动。

### 2.6 流式渲染（StreamMarkdown）

- `chopUntilStable`：把流式文本切成"已闭合的块级段"（段落/代码围栏完整闭合）+ 活动尾段；稳定段用完整 `Markdown` 渲染，尾段用轻量 `SpanStreamMarkdown`（只做内联 code/加粗/斜体）。
- 末尾 `.stream-cursor`：8px × 1.05em accent 色块，`stream-blink` 1s 闪烁。

### 2.7 样式体系

- 全部为手写 CSS（`styles.css` 约 7000 行中 5600–7260 行为消息区），类名 `msg / thinking / toolcall / chead / codeblock / bashblock / md` 等。
- 主题：CSS 变量（`--bg / --text / --text-dim / --text-faint / --accent / --accent-soft / --border / --border-soft / --bg-elev / --bg-elev2 / --card-bg / --chip-bg / --code-bg / --mono / --green / --red / --amber / --link`），16 个主题文件覆盖变量实现明暗切换。

---

## 3. picoclaw web 现状与差距

### 3.1 前端现状（`web/frontend/src/`）

| 维度 | 现状 | 与 pi-web-ui 差距 |
|---|---|---|
| 技术栈 | Vite + React 19 + TS + Tailwind v4 + shadcn/ui + jotai | pi-web-ui 是 Vite + React + 手写 CSS；**markdown 栈完全一致**（react-markdown 10.1.0 / remark-gfm 4 / rehype-highlight 7.0.2 / highlight.js 11） |
| 消息模型 | `store/chat.ts` 的扁平 `ChatMessage[]`：role + kind(normal/thought/tool_calls) + content + toolCalls | pi-web-ui 是 `UiMessage.content: UiContentBlock[]`（text/thinking/toolCall/image 混排） |
| 渲染 | `AssistantMessage` 独立卡片（border + 渐变头条 + prose）；thought/tool_calls 各自成一条消息；工具参数白底 pre；bash 黑色终端块 | 单列时间线 + blocks 混排 + 专用卡样式 |
| 流式 | `message.update` 全量 content 替换，无稳定块切分、无光标 | 有 chop-until-stable + 光标 |
| 数学公式 | 无（rehype-sanitize + typography） | KaTeX |
| 明暗主题 | shadcn CSS 变量 + `.dark` | pi-web-ui 自有变量体系 |

### 3.2 协议现状（Pico WebSocket）

服务端（`pkg/channels/pico/`）在一个 turn 内按序推送，前端（`features/chat/protocol.ts`）逐条追加为独立 `ChatMessage`：

1. `typing.start` / `typing.stop`
2. `message.create` + `message.update`(kind=thought)：思考文本**全量累积**流式（message_id 稳定）
3. `message.create`(kind=tool_calls)：`tool_calls: [{id, type, function:{name, arguments(JSON 字符串)}}]`
4. `message.create`(tool_feedback)：content 形如 `🔧 `tool_name`\n<结果文本>`（带动画帧，`ToolFeedbackAnimator` 周期 edit）；turn 内下一条非 feedback 消息到达时 finalize 成最终 content
5. `message.create` + `message.update`（无 kind = 正文）：markdown **全量累积**流式 + finalize（带 model_name、context_usage）
6. `error` / `message.delete` / `media.create`（图片附件）

历史加载走 REST（`features/chat/history.ts`），模型与 WS 一致（含 kind、tool_calls、model_name）。

### 3.3 协议信息缺口（相对 pi-web-ui 的驱动数据）

| pi-web-ui 依赖 | picoclaw 协议 | 结论 |
|---|---|---|
| 工具状态 run/ok/err（服务端 toolStatuses 事件） | 无显式信号 | **可前端推导**：tool_calls 到达=run；对应 tool_feedback 到达=ok；turn 结束仍无反馈=过期 |
| 工具耗时 durationMs | 无 | **可前端近似**：tool_calls 消息 timestamp → tool_feedback timestamp（两端都有服务器时间戳，误差可接受） |
| 工具 err 态 | 无显式信号 | **启发式**：feedback 内容以 `Error`/`error`/`⛔`/`failed` 开头或工具名匹配失败；可选服务端增强（§6.9） |
| bash 工具专用块 + live output 流式 | exec 反馈一次性到达 | 统一按普通工具卡渲染（picoclaw 的 bash 输出就在 feedback content 里）；不做 live 流 |
| thinking 完成（折叠+耗时） | 无显式结束信号 | **前端推导**：同 turn 内下一条非 thought 消息到达 = 上一 thought 块结束 |
| steer 排队 / 编辑重问 / 压缩摘要 | 协议无 | 非核心范围，不做 |

---

## 4. 可行性结论

**可行，且成本可控。** 依据：

1. **许可证无障碍**：pi-web-ui 为 MIT License，允许逐文件复制/修改，仅需在其源文件保留版权与许可声明（`SPDX-License-Identifier: MIT`）。
2. **技术栈同构**：两边同为 Vite + React + react-markdown 系；markdown/高亮依赖版本一致，无需降级适配。新增依赖仅 4 个（katex、remark-math、remark-breaks、rehype-katex）。
3. **渲染层可整体移植**：pi-web-ui 的消息渲染组件（Message/ThinkingBlock/ToolCallBlock/Markdown/StreamMarkdown/copy-button/code-lines）与它的服务端耦合极弱（仅依赖 UiMessage 数据形状），把数据形状在 picoclaw 侧适配出来即可复用，预计 8 个文件、约 1200 行组件 + 约 900 行 CSS。
4. **协议缺口可推导**：上表所有核心缺口都有纯前端推导方案（时序推导 + 服务器时间戳），视觉效果可达"一模一样"；仅 err 状态与耗时精度是近似（可选 P1 服务端增强补齐）。
5. **风险点可控**：最大结构性工作是"turn 聚合层"（把扁平 ChatMessage[] 重组为 blocks），这是纯前端数据变换，不影响 WS 协议与后端。

**不可行的部分**（明确排除）：不改造服务端就无法 100% 复刻的项 —— steer 排队气泡、编辑重问、问题导航、会话内搜索、压缩卡、mermaid、bash live output。见 §1.1 边界。

---

## 5. 总体设计

### 5.1 架构分层

```
┌─ chat-page.tsx（保留：composer / sidebar / gateway / 滚动逻辑）
├─ PiMessageList（新）               ← 对应 pi-web-ui MessageList（裁剪版：无虚拟化/搜索/折叠行）
│   ├─ PiMessage（新）               ← 对应 Message.tsx（msg-meta + blocks）
│   │   ├─ ThinkingBlock（移植）
│   │   ├─ ToolCallBlock（移植）
│   │   ├─ Markdown / StreamMarkdown（移植）
│   │   └─ image block
│   └─ pi-chat.css（新）             ← styles.css 消息区段落抽取 + 变量桥接
├─ turns.ts（新，聚合层）            ← ChatMessage[] → Turn[]（blocks + 推导状态）
├─ store/chat.ts（微改）             ← 增加 createdAt（接收时刻）供耗时推导
└─ features/chat/protocol.ts（不动）  ← WS 解析保持现状
```

### 5.2 Turn 聚合层（核心新增）

纯函数 `buildTurns(messages: ChatMessage[]): Turn[]`：

- 规则：`user` 消息开启新 Turn；其后连续的 assistant 消息（thought / tool_calls / 含 🔧 的 tool_feedback / normal / media）聚合进同一个 Turn，直到下一条 user 消息或 turn 结束信号（typing.stop / kind=normal 且 placeholder=false）。
- 每 Turn：`{ id, userText, userAttachments, userTimestamp, model, blocks: Block[], ended: boolean }`。
- Block 生成：
  - kind=thought → `{ type:"thinking", text, streamStart(首帧到达), streamEnd(下一非 thought 块出现), charCount }`
  - kind=tool_calls → 每个工具调用一个 `{ type:"toolCall", name, args(raw JSON), callReceivedAt }`；状态初值 `run`
  - 🔧 tool_feedback 消息 → 解析出工具名与结果文本（复用 `features/chat/tool-calls.ts` 的 `parseLegacyToolFeedbackContent`），**按工具名匹配最近的 run 态 toolCall 块**，合并为 `{ status:"ok", outputText, durationMs: feedbackTs − callTs }`；匹配不到则降级为独立 toolCall 块（name + output，无参数）
  - kind=normal / 无 kind 正文 → `{ type:"text", text, streaming: 是否 Turn 仍未结束 && 是最后一块 }`
  - media.create / attachments → `{ type:"image", url }`
- 同一 message_id 的 `message.update` 在 store 层已是原位替换（现状逻辑），聚合层天然拿到最新快照。
- 历史消息（`history.ts`）走同一聚合函数：历史 tool_calls 已带 tool_calls 数组，历史 feedback 已是 🔧 最终格式，聚合结果一致。

### 5.3 渲染组件层（移植清单）

| pi-web-ui 源文件（`web/src/`） | 目标（`web/frontend/src/chat-pi/`） | 改动 |
|---|---|---|
| `components/Message.tsx` | `pi-message.tsx` | 去掉 customType/bashExecution/skill/plugin/编辑重问分支；数据源换成 Turn + Block |
| `components/ThinkingBlock.tsx` | `thinking-block.tsx` | 原样移植（react-icons → 换 picoclaw 已有的 lucide-react 图标或引入 react-icons） |
| `components/ToolCallBlock.tsx` | `tool-call-block.tsx` | 去掉 kill/onKill/liveOutput/delegate 分支；状态与耗时改由 Block 字段驱动 |
| `components/Markdown.tsx` | `markdown.tsx` | 去掉 plugin-fence/mermaid 宿主；保留管线、PreWithCopy、行号、外链处理 |
| `components/StreamMarkdown.tsx` + `stream-markdown.ts` | `stream-markdown.tsx` | 原样移植（chopUntilStable + 轻量内联渲染 + cursor） |
| `components/copy-button.tsx`、`code-lines.ts` | 同名 | 原样移植 |
| `styles.css` L5605–7260 消息区段 | `pi-chat.css` | 抽取 + 变量桥接（§5.4），文案挂 i18n |

图标：pi-web-ui 用 react-icons（Fi*）。picoclaw 已有 lucide-react + @tabler/icons-react；为保视觉一致引入 `react-icons`（仅 fi 打包很小），或逐个映射到 lucide 等价图标（视觉略有差异，不建议）。

### 5.4 样式层与主题桥接

- 新建 `chat-pi/pi-chat.css`，内容为 pi-web-ui 消息区 CSS 原样拷贝（类名加前缀与否：**不加前缀**，`.msg`/`.toolcall` 等类名在 picoclaw 前端无冲突，保持"一模一样"最省事；该文件只在消息区组件树内使用这些类）。
- 变量桥接：文件头部把 pi-web-ui 变量映射到 shadcn 现有变量 + 字面量兜底，作用域限定在 `.pi-chat-root` 内：

```css
.pi-chat-root {
  --bg: var(--background);
  --text: var(--foreground);
  --text-dim: var(--muted-foreground);
  --text-faint: color-mix(in srgb, var(--muted-foreground) 60%, transparent);
  --accent: var(--primary);
  --accent-soft: color-mix(in srgb, var(--primary) 12%, transparent);
  --border: var(--border);
  --border-soft: color-mix(in srgb, var(--border) 60%, transparent);
  --bg-elev: var(--card);  --bg-elev2: var(--muted);
  --card-bg: var(--card);  --chip-bg: var(--muted);
  --code-bg: var(--muted); --code-text: var(--foreground);
  --mono: ui-monospace, "Cascadia Code", Consolas, monospace;
  --green: #22c55e; --red: #ef4444; --amber: #f59e0b;
  --link: var(--primary);
}
```

  这样明暗主题沿用 picoclaw 现有 `.dark` 切换，消息区观感与 pi-web-ui 默认主题一致；后续若要 pi-web-ui 全套 16 主题再扩展。
- KaTeX：`import "katex/dist/katex.min.css"`。

### 5.5 数据流

```
WS message.create/update ──▶ features/chat/protocol.ts（不动）
                              └▶ store/chat.ts ChatMessage[]（微改：记录 receivedAt）
chat-page ──▶ useMemo(buildTurns(messages)) ──▶ PiMessageList(turns)
流式中：text block streaming=true ──▶ StreamMarkdown（稳定块 + cursor）
thought 流式：同 message_id 的 update 触发 block.text 变化 ──▶ ThinkingBlock live 态
工具状态：聚合层按 §3.3 规则推导 run/ok/err + durationMs
```

滚动钉底沿用 chat-page 现有 `isAtBottom` 逻辑（pi-web-ui 的 stickBottom 机制等价，不必移植）。

---

## 6. 关键设计决策

1. **不动 WS 协议与后端**（P0/P1 纯前端）：所有状态推导在聚合层完成，保证零后端风险、历史会话天然兼容。
2. **turn 边界信号**：`kind=normal` 且非 placeholder 的消息 = Turn 的最后一块（与服务端 finalize 语义一致，`protocol.ts` 现状就用它清 `isTyping`）。
3. **thought 块流式态**：块内用 message_id 判定"仍在本 thought 上 update"= live；出现任何其他块 = 结束。与服务端 `reasoningID` 稳定 + FinalizeReasoning 行为吻合。
4. **工具反馈合并**：优先按工具名匹配最近 run 态块；同名并发（罕见）按 FIFO 配对。参数卡与反馈卡合并后，`parseLegacyToolFeedbackContent` 提取的 `extra_content.tool_feedback_explanation` 显示在输出区上方（对应 pi-web-ui 的 output 前正文）。
5. **耗时显示**：`durationMs = tool_feedback.timestamp − tool_calls.timestamp`（两者均为服务端 UnixMilli 时间戳，排除前端时钟问题）。误差：不含模型生成 tool_calls 前的排队时间——与 pi-web-ui 语义一致（它也只计工具执行段）。
6. **err 推导启发式**（P0）：feedback 文本首行匹配 `/^(error|failed|⛔|panic)/i` 或含 `exit code` 非零标记 → err。P1 可选服务端在 tool_feedback payload 增加 `error: true`（新增字段向后兼容，老客户端忽略）。
7. **思考/工具卡折叠默认**：与 pi-web-ui 一致——live 展开、结束折叠；历史消息一律折叠。现有 `assistantDetailVisibility` 下拉（none/thought/tool_calls/all）映射为"默认全折叠 + 按类型自动展开"的预设开关，保留但语义降级为初始展开策略（开放问题 §8-Q3）。
8. **markdown 安全**：沿用 pi-web-ui 默认——不开 rehype-raw，react-markdown 默认转义 HTML，无 XSS 面；移除现 `rehype-sanitize`（它会把 KaTeX 输出洗坏，且转义语义下冗余）。
9. **服务端增强（可选 P1，二选一）**：a) tool_feedback payload 加 `error` bool 与 `duration_ms`；b) 不改。建议先纯前端上线，误差可接受再补 a。
10. **i18n**：pi-web-ui 文案（`Thinking`、`Copied!`、`Waiting for the model…` 等）照搬进 picoclaw 的 en/zh 资源文件；zh 默认与 pi-web-ui 英文一致的键值（保持"一模一样"视觉，中文翻译仅影响文字内容，用户可选）。
11. **TypingIndicator**：picoclaw 现有输入中指示器保留在 Turn 外（pi-web-ui 等价物是 thinking-wait 占位点动画，由 thought live 态覆盖，turn 首块到达后自动消失）。

---

## 7. 实施计划

### P0 —— 骨架跑通（消息区换成 pi 形式，核心块可用）

1. `chat-pi/` 目录 + 依赖（katex、remark-math、remark-breaks、rehype-katex、react-icons）
2. `pi-chat.css`（变量桥接 + 消息区 CSS 拷贝）+ `pi-message-list.tsx`（无虚拟化，直接 map）
3. 移植 Markdown / copy-button / code-lines（代码块 + 行号 + 复制）
4. `turns.ts` 聚合层（thought/toolCall/text/image 四种块 + 状态推导）+ 单测
5. `chat-page.tsx` 切换渲染到 `PiMessageList`；保留 composer/sidebar/滚动

### P1 —— 完整 parity

6. 移植 ThinkingBlock（live/collapse/耗时）与 ToolCallBlock（参数高亮、输出区、waiting、err、耗时）
7. 移植 StreamMarkdown + chop-until-stable + stream-cursor；正文与 thought 流式接入
8. 历史会话回归（`history.ts` → `buildTurns` 一致性单测：🔧 合并、turn 切分）
9. 图片块（media.create + 用户附件缩略）
10. 错误卡（`.msg-error` 样式替换现有 toast-only 行为中的消息区部分）
11. i18n 键 + zh/en 资源

### P2 —— 可选增强（单独评估，不在本次"一模一样"承诺内）

12. 服务端 tool_feedback payload 增强（error/duration_ms）
13. 旧消息折叠行 / 问题导航 / 会话内搜索 / mermaid / bash live —— 均需服务端信号或转录格式支持，另行立项

### 验证

- `cd web/frontend && npm run build` + 既有 vitest 全绿
- 新增聚合层单测：turn 切分、🔧 合并、状态推导、流式中 update 原位替换
- 手工回归：流式 turn（thought → tool → feedback → thought → tool → 正文）、错误 turn、纯文本 turn、历史加载、明暗主题、非 pico channel 只读会话

---

## 8. 风险与开放问题

**风险**：
- R1 `rehype-sanitize` 移除后依赖 react-markdown 默认转义防 XSS——需在 PR 里明确说明并测试 `<script>` 注入样本。
- R2 Tailwind v4 的 preflight 与拷贝 CSS 的相互作用（`pre` 默认 margin 已 reset，预期无冲突，需目检）。
- R3 🔧 合并依赖 feedback 文本格式稳定（服务端 `InitialAnimatedToolFeedbackContent` 契约）；格式变更需同步聚合层——建议在 `pkg/channels` 相关测试旁加注释锚点。
- R4 react-icons 新依赖（tree-shaking 后仅引入所用图标，体积影响 <10KB gzip）。

**已决议（2026-09-20 用户确认）**：
- Q1 范围 → **仅消息输出区（P0+P1）**：消息时间线、思考卡、工具卡、代码块、流式光标等输出形式一模一样；外围功能（排队/插队、编辑重问、问题导航、会话内搜索、折叠行、mermaid）不做。
- Q2 工具状态 → **纯前端推导**：消息时序 + 服务器时间戳推导 run/ok/耗时，错误启发式识别；零后端改动。
- Q5 文案语言 → **随 i18n 中文化**：接入现有 i18n 资源，中文界面显示中文文案（英文界面保持 pi-web-ui 原文）。

**沿用建议方案（无需再确认）**：
- Q3 `assistantDetailVisibility` 降级为"初始展开策略"预设保留。
- Q4 模型名 Turn 级兜底（Turn 内任意消息的 model_name）。

---

## 9. 附录：源码事实索引

**pi-web-ui**（`D:\code\pi-web-ui\web\src\`）：
- 消息渲染：`components/Message.tsx`（blocks 分发）、`MessageList.tsx`（列表/折叠/虚拟化/搜索）、`ThinkingBlock.tsx`、`ToolCallBlock.tsx`、`Markdown.tsx`（管线 L41–50、PreWithCopy L107）、`StreamMarkdown.tsx` + `stream-markdown.ts`（chopUntilStable）、`copy-button.tsx`、`code-lines.ts`
- 样式：`styles.css` L5605–7260（.messages/.msg/.msg-meta/.msg-user/.thinking/.toolcall/.chead/.codeblock/.bashblock/.stream-cursor/.msg-error）、`themes/*.css`（16 主题变量）
- 数据模型：`types.ts`（UiMessage / UiContentBlock: text|thinking|toolCall|image|bash / ToolCallBlock.status run|ok|err）
- 数据流：`use-chat.ts`（服务端 60ms 节流 UiState 快照推送）
- 许可证：MIT

**picoclaw**：
- 前端：`web/frontend/src/store/chat.ts`（ChatMessage/kind）、`features/chat/protocol.ts`（WS 解析）、`assistant-message-state.ts`（kind 状态机）、`tool-calls.ts`（tool_calls 与 🔧 解析）、`history.ts`（历史合并）、`components/chat/chat-page.tsx`（页面/循环 L414–445）、`assistant-message.tsx`、`user-message.tsx`
- 服务端：`pkg/channels/pico/protocol.go`（消息类型/kind 常量）、`pico.go`（Send L298–360、picoStreamer L532–741 全量累积流式、ToolFeedbackAnimator、FinalizeToolFeedbackMessage L459–477）

---

## 10. 实施记录（2026-09-20，P0+P1）

落地位置：`web/frontend/src/chat-pi/`（11 个文件）+ `chat-page.tsx` 接入 + i18n（zh/en `piChat.*` 共 23 键，文案取 pi-web-ui 原文）。

| 文件 | 对应 pi-web-ui 源 | 说明 |
|---|---|---|
| `pi-chat.css` | `styles.css` 消息区段 | 变量桥接作用域为 `.pi-chat`（挂在消息列容器上）；响应式段；`.msg-attach-chip` 为 picoclaw 补充（非图片附件） |
| `markdown.tsx` / `stream-markdown.tsx` | `Markdown.tsx` / `StreamMarkdown.tsx` | KaTeX 管线 + 代码块行号/复制；流式前缀缓存渲染 |
| `stream-segments.ts` | `stream-markdown.ts`（工具） | 改名避免与组件 `.ts/.tsx` 解析歧义 |
| `thinking-block.tsx` / `tool-call-block.tsx` | 同名 | ToolCallBlock 的状态改由 `PiToolView` 视图模型驱动（见下） |
| `tool-args.ts` / `code-lines.ts` / `copy-button.tsx` | 同名 | `toolArgHints` 去掉了 delegate_task 解析 |
| `turns.ts` | —（picoclaw 新增） | `buildTurns` 聚合 + 状态机推导 |
| `pi-message.tsx` / `pi-message-list.tsx` | `Message.tsx` / `MessageList.tsx` | 卡结构（`.msg-meta` + blocks + cursor + thinking-wait） |

与设计稿的实现偏差（均为实现期确认的更优解）：

1. **工具反馈数据源**：实测 `message.update` 会把反馈就地写进 tool_calls 消息的 `toolCalls[i].extraContent.toolFeedbackExplanation`（非设计稿假设的独立 🔧 消息流），`turns.ts` 直接读取，无需按工具名配对合并（§5.2 该规则作废）。
2. **耗时观测**：`message.update` 不改消息时间戳，反馈到达时刻由前端首见观测（模块级注册表），仅用于流式中的 waitingModel 阶段耗时；历史 turn 一律 done 态、不显示耗时。
3. **状态机**：`running`（无反馈且 turn 流式中）/ `waitingModel`（有反馈且 turn 流式中，对应 pi 的 tool_status）/ `done`（turn 结束且有反馈）/ `idle`（turn 结束无反馈）。
4. **detail-visibility**：从消息级过滤改为 block 级过滤（`shouldShowAssistantMessage` 复用，thought→"thought"、toolCall→"tool_calls"），选项语义不变。
5. **旧组件**：`assistant-message.tsx` / `user-message.tsx` / `typing-indicator.tsx` 已无引用（typing-wait 占位替代 TypingIndicator），暂保留在仓库中未删除。
6. **turn 切分**：steering 用户消息自成一张用户卡并开启新 turn（其后助手块并入该 turn），插问视觉语义清晰。

验证：`pnpm run build`（tsc + vite）通过；`CC=clang go build -tags goolm ./web/...`（含 dist embed）通过。待手工回归：流式 turn 全链路、错误 turn、历史加载、明暗主题（§7 验证清单）。
