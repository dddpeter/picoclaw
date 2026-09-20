# 代码评审：web chat-pi 渲染管线切换 + 提示词模板功能

| 项目 | 内容 |
|---|---|
| 评审日期 | 2026-09-20 |
| 评审对象 | 提交 `b93389bb`（feat(web): 聊天渲染切换 chat-pi 管线，新增提示词模板功能）+ 工作区未提交改动 |
| 分支 | main |
| 规格说明 | `docs/design/web-chat-pi-rendering.zh.md`（含 §10 实施记录） |
| 评审范围 | 28 文件（+4986 / −549）+ 工作区 3 文件（+35 / −10） |
| 验证手段 | 阅读 diff 与源码、运行 `go test ./web/backend/api/ -run PromptTemplates`、运行 `pnpm run build`、核对依赖与鉴权链路 |
| 结论 | **可合并，但需先补 H1/M1 两项规格承诺项**；其余为稳定性与清理类问题 |

---

## 🟢 已核验通过的基线

- `go test ./web/backend/api/ -run PromptTemplates` **全绿**（含 12 个校验子用例）
- `pnpm run build`（tsc + vite）**通过**
- **鉴权覆盖正确**：`/api/prompt-templates` 不在 `isPublicLauncherDashboardPath` 白名单内，受全局 `LauncherDashboardAuth` 中间件保护；会话 Cookie 为 `HttpOnly + SameSite=Lax`，对跨站 PUT 有基本 CSRF 防护
- 静态消息走一次性 `<Markdown>`、仅流式尾部走 `StreamMarkdown`，最终态不会残留分段近似错误（`pi-message.tsx` 的 `live ? StreamMarkdown : Markdown`）
- 后端文件写入使用 `fileutil.WriteFileAtomic` + `0o600`，无明文权限问题

---

## 严重（High）

### H1. 聚合层 `turns.ts` 零测试，且项目没有前端测试基础设施 —— 规格承诺未兑现

规格 §7「验证」写明「既有 vitest 全绿」「新增聚合层单测：turn 切分、🔧 合并、状态推导、流式中 update 原位替换」，§10 也将其列为待办。实测：

- `web/frontend/package.json` **没有 vitest，也没有 `test` script**
- `src/` 下**没有任何 `*.test.ts(x)`**（唯一的 `__tests__` 位于 `node_modules`）

而 `turns.ts` 正是本次改动中逻辑最复杂、最易出边界 bug 的纯函数：turn 切分、steering 插问、无用户头的合成 turn、状态机推导、时间戳解析、detail 过滤、pending 占位分支。

**建议**：引入 vitest（devDependency + `"test": "vitest run"`），至少补测：

1. 单 user + 单 assistant 的最小 turn
2. steering 插问用户消息切分为独立 turn
3. 首条即 assistant 消息（历史半截加载）的合成 turn
4. `tool_calls` + `extraContent.toolFeedbackExplanation` 的合并与 err 启发式
5. `parseTimestampMs`：秒 / 毫秒 / 字符串数字 / ISO 串 / 非法值
6. `isTyping` 下的 pending 占位卡与 live 块

> 注：`turns.ts` 已导出 `resetFeedbackObservations()` 却无人调用 —— 这是为测试预留的钩子，正好说明测试本应存在。

### H2. `feedbackSeenAt` 模块级 Map 永不回收 —— 内存泄漏

`turns.ts`：

```ts
const feedbackSeenAt = new Map<string, number>();
function observeFeedbackEndedAt(key, now) { ... feedbackSeenAt.set(key, at) ... }
```

key 形如 `${message.id}#${toolCall.id}`。每次 `buildTurns` 遇到带反馈的工具块都会 `set`，**只增不减**，且模块级存活于整个页面生命周期。任何一次流式 turn 产生的 key 都会永久驻留。

**建议**：改为有界缓存 —— 给 `feedbackSeenAt` 设 `MAX_ENTRIES`（如 500）并按插入序淘汰最旧项；或将观测登记表按 turn id 组织、turn 结束即清。至少在注释里说明生命周期，并补一条「历史重建不写入」的断言。

---

## 中（Medium）

### M1. `rawHtml` + `rehype-raw` 的反转 XSS 面：当前安全，但埋了雷

规格 §6.8 / §8-R1 要求「移除 rehype-sanitize，依赖 react-markdown 默认转义防 XSS」，并要求在 PR 内说明 + 测试 `<script>` 注入样本。实现只做了一半：

- 移除了 `rehype-sanitize`（未被 import）✅
- 但**新增了 `rehypeRaw` import**，并通过 `MarkdownProps.rawHtml` 暴露（`markdown.tsx:52-54`）

全仓 grep 确认 `chat-pi/` 内**没有任何调用点传 `rawHtml=true`**，故当前 chat 消息走默认转义路径，**安全状态成立**。但这是仅靠「调用方自律」维持的边界：任何后续调用方为「支持 HTML + markdown 混排」传 `true`，且输入源自模型输出/用户消息，即立刻成为存储型 XSS。

**建议**（二选一）：

1. 若无需求 —— **删除 `rehypeRaw` import 与 `rawHtml` prop**，并把 `rehype-raw` / `rehype-sanitize` 从 `package.json` 移除（现为死依赖）
2. 若保留能力 —— `rawHtml=true` 分支必须强制接 `rehype-sanitize`，并加单测渲染 `<img src=x onerror=...>` 验证被清洗

无论哪种，都应补一条 XSS 回归测试（规格 R1 的承诺）。

### M2. `useEffect(..., [t])` 依赖 i18n `t` —— 切换语言会重新拉取并覆盖本地未保存状态

`PromptTemplatesProvider` 的加载 effect 依赖 `[t]`：

```ts
useEffect(() => { getPromptTemplates().then(list => setStored(list)) ... }, [t])
```

`react-i18next` 切换语言时通常返回新的 `t` 引用，触发重新 GET 并 `setStored(list)`。若此时用户正处于乐观更新失败回滚或刚保存后，会用服务端快照覆盖内存态（多数情况无害，但属意外网络往返 + 状态重置）。

**建议**：依赖数组改为 `[]`（`loadError` 文案在 catch 内现取 `t`；如需语言相关错误文案，用 ref 持有 `t`）。`persist` 的 `[stored, t]` 依赖同理可简化。

### M3. PUT 全量替换 + 读改写无并发保护 —— 多标签/快速连点会丢更新

后端 `handlePutPromptTemplates` 用 `promptTemplatesMu` 串行化**文件写**，但语义是「整表替换」（客户端发全量 `templates`）。前端 `persist` 采用乐观更新 + 失败回滚，且 `saveTemplate/removeTemplate/restoreOverride` 均从闭包里的 `stored` 计算 `next`。两个并发操作（多标签页、或快速双击 Save）会**后写覆盖前写**，先成功的改动静默丢失。

**建议**：① 前端对写操作串行化（单 in-flight Promise 链 / 保存中禁用按钮）；② 或后端改为按 id 的 PATCH 语义。至少在 API 注释中写明「最后写入者胜出」，并在 UI 层做保存中禁用。

### M4. 损坏文件 GET 返回 500 且无自愈路径验证

`loadPromptTemplates` 解析失败即返回 error，GET 返回 500；PUT 侧 `normalize` 只校验入参，PUT 成功会重写文件。因此「损坏」理论上可由一次 PUT 自愈，但现有测试 `TestPromptTemplatesGetCorruptFile` **只断言 500，未验证自愈**。

**建议**：补测试「写入损坏文件 → PUT 合法体 → 再 GET 返回 200 且内容正确」；若该路径确实自愈，可在 GET 的 500 错误里提示用户「保存任意模板即可修复」。

### M5. 空态 `EMPTY_VISIBLE = 11` 的「填满整行」假设脆弱

工作区新增的注释称「11 + 新建磁贴 = 12，可为 2/3/4 列 auto-fill 网格填满整行」。`remaining = templates.length - visible.length` 计算本身正确（`ordered` 与 `templates` 等长），但「整行」只在该网格恰好落成 2/3/4 列时成立；窄屏单列时 12 个磁贴偏高。

**建议**：去掉「填满整行」注释以免误导，或由实际列数推导 `EMPTY_VISIBLE`。非阻塞。

---

## 低（Low）

### L1. `segmentStream` 的 setext 标题 / 前向引用定义跨段为已知近似

`stream-segments.ts` 注释已自认该近似，且最终态由一次性 `<Markdown>` 纠正。**可接受**。

### L2. `ActiveTail` 的 200ms 节流依赖 `Date.now()` + `lastRef`，跨块 freeze 靠 `key={frozen.length}` 重置

逻辑成立且有注释。`inFence` 由 `true→false` 时首次 flush 立即执行（`Date.now() - 0` 远大于 200），行为正确。**可接受**。

### L3. 旧组件删除后的资源残留

`assistant-message.tsx / user-message.tsx / typing-indicator.tsx` 已删（`D`），但 `features/chat/assistant-message-state.ts` 仍被 `protocol.ts` 引用（**另一个模块，保留合理**）。i18n diff 为纯新增 178 行 0 删除，疑似残留旧组件文案键。**建议**：全仓搜一次 `assistantMessage.*` / `typing.*` 未被引用的键并清理。

### L4. 后端按 rune 计数与前端 `maxLength`（UTF-16 code unit）不一致

`normalizePromptTemplates` 用 `len([]rune(x))`，前端 `maxLength` 按 UTF-16 计。包含 emoji/代理对时，前端更严格、后端更宽松，无安全后果。**建议**：统一口径或标注差异。

### L5. `toolArgHints` 的 `SCAN_LIMIT=256KB` 与 `commandHint` 的关系

`commandHint(argsText)` 内部对超长参数直接返回 undefined（不显示命令行），属合理退化，注释已说明。**可接受**。

---

## 规格符合度总结

| 规格条款 | 实现 | 结论 |
|---|---|---|
| §5.1 分层（turns.ts / pi-* 组件 / pi-chat.css） | 落地于 `src/chat-pi/` | ✅ |
| §5.2 🔧 按工具名配对合并 | 作废，改为读 `extraContent.toolFeedbackExplanation` | ⚠️ 有记录（§10-1），合理偏差 |
| §5.3 移植清单 8 文件 | 落地 11 文件 | ✅ |
| §6.8 移除 rehype-sanitize | 移除，但**新增 rehype-raw** | ⚠️ 见 M1 |
| §7 聚合层单测 + vitest 全绿 | **未做**，无测试设施 | ❌ 见 H1 |
| §8-R1 XSS 注入测试 | **未做** | ❌ 见 M1 |
| §7 手工回归清单 | §10 标注「待手工回归」 | ⏳ 未知 |
| 提示词模板持久化/校验/去重/上限 | 后端齐全 + 测试齐全 | ✅ |

---

## 优先处理建议

1. **H1**（补 vitest + `turns.ts` 测试）与 **M1**（消除 `rehype-raw` 死面 + XSS 回归测试）—— 直接对应规格承诺，成本低、收益高
2. **H2**（`feedbackSeenAt` 加上界）—— 长会话稳定性，改法很小
3. **M3**（写操作串行化）与 **M4**（损坏文件自愈测试）—— 数据正确性
4. 其余为清理/一致性，可随手带上

---

## 复现与验证命令

```powershell
# 后端模板测试
cd D:\code\picoclaw
go test ./web/backend/api/ -run PromptTemplates -v

# 前端构建（tsc + vite）
cd D:\code\picoclaw\web\frontend
pnpm run build
```
