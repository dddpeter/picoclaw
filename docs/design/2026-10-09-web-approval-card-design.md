# Web 端审批卡片设计文档（方案 A：消息流内嵌独立卡片）

- 日期：2026-10-09
- 状态：待批准
- 关联提交：`a4e2cf95`（飞书审批原生交互卡）、`afabb573`（/approve always）、`9a302f66`（单清单配方）
- 否决方案见文末「备选与否决」

## 1. 背景与现状

### 1.1 审批链路（已存在，不改动）

`pkg/agent/approval_hook.go` 的 HITL 审批钩子：

1. 工具调用命中 Ask 规则 → `publishAsk` 向渠道发问
2. 渠道若实现 `channels.ApprovalPromptCapable` 接口 → 渲染原生交互卡（飞书已实现，`a4e2cf95`）
3. 否则 → 纯文本兜底（`【需要你授权】…请回复 /approve /approve always /deny，超时 5m0s 未回应默认拒绝`）
4. 用户回复（或按钮点击合成的 `/approve`）经 steering 前置路径 `tryHandleApprovalReply` 解析，`fail-closed` 超时自动拒绝
5. 回执（receipt）作为普通 assistant 消息外发

**当前各端形态：**

| 端 | 审批呈现 | 操作方式 |
|---|---|---|
| 飞书 | 原生交互卡（黄 header + 三按钮 + 超时提示 + 点击后封卡） | 点按钮（合成 `/approve` 命令） |
| Web | 纯文本兜底消息 | 手动打字 `/approve` |
| TUI/CLI | 纯文本兜底 | 手动打字 `/approve` |

### 1.2 为什么飞书审批卡从未弹出过（排查结论）

排查路径：`gateway.log` 全量搜索 `Approval prompt card sent` / `needs your approval` / `审批` → **零命中**（自 10 月初起从未触发过 `publishAsk`）。

根因在 `~/.picoclaw/config.json`：

```jsonc
"hooks": {
  "builtins": {
    "approval": {
      "enabled": true,
      "config": {
        "inherit_exec_deny_patterns": true,  // 单清单配方：ask 集 = exec custom_deny_patterns
        "timeout_ms": 300000
      }
    }
  }
},
"tools": {
  "exec": {
    "enable_custom_deny_patterns": false,
    "custom_deny_patterns": [
      "\\brm\\s+-[rf]{1,2}\\b",
      "\\bdel\\s+/[fq]\\b",
      "\\brmdir\\s+/s\\b",
      "(^|[^-\\w])\\b(format|mkfs|diskpart)\\b\\s",
      "\\bdd\\s+if=",
      ...
    ]
  }
}
```

`ask_patterns` 默认空、`inherit_exec_deny_patterns` 把 `custom_deny_patterns` 拉进 ask 集——而这 6 条自定义模式全是 `rm -rf` / `del /f` / `format` / `dd` 级别的高危命令，日常开发命令（git/go/编译）全部不命中，**审批钩子从挂载到现在一次都没走到 `publishAsk`**。卡片代码路径完好，只是配置上还没"喂"给它任何 ask 规则。

**推论：Web 端审批卡片做出来后，想日常看到它，需要先给 `ask_patterns` 加规则（或加更多 `custom_deny_patterns`），或在测试环境临时用 `ask_patterns: ["tool:exec"]` 全量问。**

## 2. 目标

- Web 端把审批询问从「纯文本消息 + 手动打字」升级为「消息流内嵌独立审批卡片 + 三按钮一键操作」，交互语义与飞书卡完全对齐
- 操作仍走现有 `/approve` 文字协议（复用 `tryHandleApprovalReply` 全链路，**后端零协议改动**）
- 卡片在消息历史中留痕（封存的只读态），可回溯"当时批准了什么/拒绝了什么"
- 体现 fail-closed 语义：倒计时可视化，超时自动拒绝不是黑盒

## 3. 设计

### 3.1 识别机制（后端最小改动）

审批兜底文本由 `publishAsk` 生成，格式固定。Web 端识别采用**双通道**：

1. **主通道（协议扩展，轻量）**：`publishAsk` 的兜底外发路径在 `bus.OutboundMessage` 上附带一个 `Kind: "approval_request"` 元数据字段（现有 `OutboundMessage` 加可选 `Kind string` 与 `ApprovalMeta {Tool, Preview, TimeoutMs}`）。Web 后端把它放进现有 WebSocket 消息流的 payload（新增 `approval` 字段，旧前端忽略即可，向前兼容）。
   - 改动面：`pkg/bus` 消息结构 +1 字段；`approval_hook.publishAsk` +5 行；`web/backend` 转发处 +1 字段透传。
2. **兜底通道（纯前端）**：前端识别文本特征 `【需要你授权】`（`【需要你授权】` 前缀 + 工具名 + 提示语）。协议字段缺失时（如旧网关、历史消息回放）仍能用特征匹配渲染卡片，只是倒计时降级为"未知"。

这样即使只改前端（v0），卡片也能工作；协议扩展（v1）让卡片有精确的 tool/preview/timeout 数据。

### 3.2 卡片形态（待审批态）

渲染位置：`pi-message-list` 中该条消息的位置，整条替换为卡片（不是消息 + 卡片叠加）。

```
┌─────────────────────────────────────────────┐
│ ⚠ 需要你的授权              [sk_xxxx 末4位] │  ← header：琥珀色左边条 + 警示图标
├─────────────────────────────────────────────┤
│ 工具  exec                                   │
│ ┌─────────────────────────────────────────┐ │
│ │ git push origin main                    │ │  ← 预览代码块（preview，≤200 rune）
│ └─────────────────────────────────────────┘ │
│ ━━━━━━━━━━━━━━━━━━━━━━━━░░░░░░░░░░░░  3m12s │  ← 倒计时进度条（amber→red 渐变色）
│ 超时将自动拒绝（fail-closed）                 │
│                                               │
│ [✓ 批准]   [⚡ 批准并保留本会话]   [✕ 拒绝]   │  ← 三按钮，对齐飞书卡
└─────────────────────────────────────────────┘
```

视觉规范（沿用 10-09 markdown 渲染升级的设计语言）：

- 卡片圆角 `rounded-xl`，边框 1px，琥珀色（amber-500/30）左边条 + 极浅 amber 底，与"需要决策"语义绑定
- 主按钮（批准）用 primary 色填充，"批准并保留"用 outline，"拒绝"用 danger 描边——与飞书卡三按钮语义一一对应
- 倒计时：CSS 动画或 rAF 驱动进度条；剩余 60s 转红色 + 数字抖动一次（克制的提醒，不做闪烁）

### 3.3 交互行为

- 点击任一按钮 → 通过现有 composer 发送通道发出对应命令文本（`/approve` / `/approve always` / `/deny`），**不新增 API**
- 发送成功后卡片进入「已提交」中间态：按钮置灰 + loading 图标，防止连点
- 服务端回执（`已批准（下次同类调用将自动放行）` / `已拒绝` / `已批准并在本会话内总是允许`）到达后，按文本特征匹配到未封存卡片 → 卡片封存（seal）：
  - 按钮行移除，替换为结论行：`✅ 已批准 · 12:34:56` 或 `⛔ 已拒绝 · 12:34:56`
  - 卡片降为低视觉权重（opacity 70%，去掉琥珀色强调），留在历史中
- 超时自动拒绝：服务端发拒绝回执（现有行为），卡片按拒绝态封存 + 标注"超时"
- 多卡并存：同一会话多个审批排队时，各卡片独立渲染、独立倒计时、独立封存，互不影响

### 3.4 前端实现落点

| 文件 | 改动 |
|---|---|
| `web/frontend/src/chat-pi/pi-message.tsx` | 审批特征/协议识别 → 渲染 `<ApprovalCard>` 替代普通 markdown 块 |
| `web/frontend/src/components/chat/approval-card.tsx`（新增） | 卡片组件：三态（pending / submitting / sealed）+ 倒计时 hook |
| `web/frontend/src/store/chat.ts` | `approvals: Map<msgId, ApprovalState>`；收到回执消息时更新状态并触发封存 |
| `web/frontend/src/features/chat/protocol.ts` | 识别/转发 `approval` payload 字段（v1 协议扩展后启用） |

按钮发送复用 `chat-composer` 的 send 函数（与用户打字同路径），保证 steering 解析、M1 同 chat 校验、回执发布全链路不变。

### 3.5 后端改动清单（v1 协议扩展，可拆两期）

1. `bus.OutboundMessage` 加 `Kind` + `ApprovalMeta`（可选字段）
2. `approval_hook.publishAsk` 兜底外发时填充
3. `web/backend` WS 转发透传字段
4. 旧版前端对未知 `Kind` 按普通消息渲染（兼容）

v0 可先纯前端（3.2 的兜底通道 + 按钮打字），一天内可落地；v1 补协议精度。

## 4. 测试

- 前端：`approval-card` 三态快照测试；倒计时 hook 单测（vi.useFakeTimers）；识别函数对真实兜底文本（含工具名/预览/超时文案）的解析用例
- 手动验证：测试环境 `ask_patterns: ["tool:exec"]` 全量问 → 走三个按钮各一次 → 确认卡片封存 + 会话 always 规则生效（再发一次同类命令不再询问）→ 超时场景（timeout_ms 设 10s）确认拒绝封存
- 回归：`/approve` 打字路径不受影响（卡片与打字并存可用）

## 5. 备选与否决理由

| 方案 | 否决理由 |
|---|---|
| B：右下角弹出层（toast/dock） | 需要 web 后端新协议 + 前端 store 大改，工作量 2-3×；视觉权重收益可用「全局小红点」低成本近似，留作观察项 |
| C：A + 顶栏全局提示 | 有价值但非必须，A 跑起来后再叠加（成本 +20%） |
| 全屏 modal/AlertDialog | 审批需要上下文（工具名/命令预览），modal 打断对话且卡片留痕需求满足不了 |
| 去掉倒计时 | fail-closed 超时是核心语义，必须可视化 |
| 按钮直连新审批 API（绕过文字协议） | 要动 `tryHandleApprovalReply` 的解析链路 + M1 校验 + 回执发布，收益（少一次消息往返）极小；复用文字路径零后端改动、语义统一 |

## 6.1 实现状态（2026-10-09 晚，评审后修订）

已实现并超出原设计：v0+v1 一次做完（协议扩展 + 文本兜底）、审批卡三态、
历史回放配对、**pending 持久化标记**（`<key>.approval.json`，评审发现的
P1：ask/回执均 outbound 不入转录，刷新即失卡——现由标记经
`GET /api/sessions/{id}` 的 `pending_approval` 字段还原）、超时回执补齐
（此前 fail-closed 超时无任何用户可见反馈）、孤儿回执封存悬空卡、
submitting 30s 自恢复、非最新 pending 卡按钮禁用（服务端单 waiter，
旧卡按钮会错位作用到最新审批）。

评审遗留（未做，按需再启）：
- 飞书交互卡超时不封卡（超时回执以文本消息到达，卡面三按钮保留至手动
  点击；封卡需 ShowApprovalPrompt 记录卡 id 并在 Send 识别超时回执）
- 审批 ask/回执不进会话转录（模型上下文）：已解决的审批在后端无留痕，
  仅 web 端 live 会话内可见封存卡
- 倒计时锚点用服务器时间戳，客户端时钟偏差影响精度
- ask/回执是 kind=normal 终结消息，会冻结回合状态栏"已完成"（沿袭旧文本
  ask 行为；可考虑 progress_note 语义化）

## 6. 遗留问题（需拍板）

1. **v0/v1 节奏**：先纯前端 v0 上线，还是等 v1 协议扩展一次做完？（建议 v0 先行）
2. **历史消息回放**：刷新页面后，旧的纯文本审批消息要不要也按特征识别渲染成已封存卡片？（建议要，v0 兜底通道天然支持）
3. 触发验证：要不要顺手在 config 里加一条 `ask_patterns`（如 `git push`），让飞书卡 + Web 卡都能日常看到？（当前配置下两端都静默）
