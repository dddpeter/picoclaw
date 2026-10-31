# ask_user 卡片体验升级：对齐 Octop 交互设计

> 设计文档（不含代码改动）
> 参考实现：`D:\code\Octop\dashboard\src\pages\Chat\components\AskQuestionCard.tsx` + `AskQuestionCard.module.less`
> 目标文件：`D:\code\picoclaw\web\frontend\src\components\chat\ask-card.tsx`
> 涉及后端文件（协议约束，本次不改）：`pkg/agent/ask_user.go`、`pkg/bus/types.go`
> **评审修订版**（已对照源码核查全部断言）：P1 多题换行错位对策（§3/§8-1）、`isAnswered` 改 `answerText` 单一真相源（§3）、完成态降级（§2.4）、§4 透传确认关闭、shadcn Textarea 更正（§2.3）、§5 locale 覆盖与死键清理、§7 补单测、§8 补 tradeoff 决策。

---

## 1. 现状对比

### 1.1 PicoClaw 现有卡片（`ask-card.tsx`）

| 维度 | 现状 |
|---|---|
| 题目呈现 | **所有题目同时堆叠展开**（N 题卡 = N 段问题 + N 组选项纵向排列），一次全提交 |
| 选项样式 | 胶囊小按钮（`h-7 rounded-lg px-2.5`），横向 wrap 排列；选项说明只在 `title` tooltip，**不可见** |
| 关键字 | A–F 字母角标（`font-mono 10px`，灰字），仅作提示 |
| 自由文本题 | 单行 `input`（maxLength 2000），只在 draft 流程下显示；单题单选时只有一行提示"在输入框作答" |
| "其他/自由文本" | **无内置 "其他" 选项**，用户只能去底部输入框打字 |
| 导航 | 无"下一步/上一步"；只有 `submitAll`（提交全部）和 `/skip` |
| 单题单选 | 点击选项**立即发送**（不进草稿）；单题多选进草稿 |
| 完成态 | 整卡 `opacity-75` + 一行 seal 文字（"已收到回答" / "已跳过"），**不展示所答内容，不可展开** |
| 提交前确认 | 无回顾步骤，提交即发 |
| "你决定" | `/skip` → 发送字面 `/skip` 命令，后端返回"已跳过问询" |
| 视觉 | 左侧 4px 蓝色条 + 浅蓝底 + `IconHelpCircle`，卡片样式与审批卡接近，但**没有"题目卡片化"的层次感** |

### 1.2 Octop `AskQuestionCard`（目标体验）

| 维度 | 行为 |
|---|---|
| 题目呈现 | **步进式（step wizard）**：一次只显示当前一题，`1 / 3` 进度徽标；已完成题目在"回顾"模式下折叠为一行已答摘要 |
| 选项样式 | **全宽选项行**（`min-height: 42px`，带 `A` 圆角字母 chip + 主文本 + 次行描述文本），纵向堆叠，hover 上浮 1px + 阴影 |
| 关键字 | 每个选项左侧 `A/B/C/…` 字母 chip（`18×18`，圆角 4px），选中时变为品牌色填充反白 |
| "其他" | 有选项的题组自动追加一个**"其他…"选项**（下一位字母 key），点开后在下方展开一个 `TextArea`（1–4 行自动增高）；无选项的纯自由文本题直接显示 `TextArea` |
| 自由文本题 | 原生 `TextArea`（非 input），placeholder"输入你的答案" |
| 导航 | **下一步 / 返回**（非最后一题）；最后一题按钮变 **"确认并提交"**（进入回顾模式）；回顾模式下可逐题点回改，按钮变为 **提交 / 返回** |
| 单题单选 | **选完即跳下一题**（无需手动"下一步"）；末题直接进回顾 |
| 多选题 | 可反复勾选，必须手动点"下一步"确认结束 |
| "你决定" | 独立"跳过"按钮（`margin-left: auto`，ghost 样式），文案 **"你决定"**，提交时发送语义化消息（"你决定吧——按最优默认方案继续，并告诉我你依据的假设"）而非字面 `/skip` |
| 完成态 | `<details>` 折叠条：`已回答 N 个问题`（绿）/ `已关闭 N 个问题`（灰）+ `查看` 展开提示，展开后**仅显示每题问题文本**（评审更正：Octop 从不显示用户答案，其封存数据也不含答案；本设计超出 Octop 的"所答摘要"需自行定义降级，见 §2.4） |
| 提交前确认 | 回顾模式（reviewing）：所有已完成题目折叠为一行，可点回任何一题修改；`提交回答` 按钮在所有题都有答案时才可用 |
| 视觉 | 卡片：品牌色 42% 混边 + 7% 品牌色底 + `radius-lg`；`步骤区`最高 `min(420px, 42vh)` 内部滚动；`动作区`顶部分隔线；`移动端`（≤767px）去掉选项左缩进、选项 `min-height: 44px` 适配触控 |

---

## 2. 目标体验（改造后 PicoClaw 卡片）

### 2.1 交互模型变更（核心）

| 项目 | 改造前 | 改造后 |
|---|---|---|
| 题目显示 | 全堆叠 | **步进式**，一次一题，`n / total` 进度 |
| 选项样式 | 横向 wrap 胶囊 | **全宽纵向选项行**（主文本 + 描述次行 + 字母 chip） |
| 选项描述 | 仅 tooltip | **直接可见**（次行灰字） |
| "其他" | 无 | **有选项的题组自动追加"其他…"**，展开 TextArea |
| 自由文本题 | 单行 input | **多行 TextArea**（自动增高 1–4 行） |
| 导航 | 无 | **下一步 / 返回 / 确认并提交** |
| 单题单选行为 | 点击即发（末题） | 选完自动进回顾（与 Octop 一致；"两击提交"tradeoff 见 §8-5） |
| 末题确认 | 直接 submitAll | **进入回顾模式**，逐题可改，最后点"提交回答" |
| "你决定" | 发送字面 `/skip` | 发送**语义化消息**（同 Octop `skipMessage` 文案），**不再发 `/skip`** |
| 完成态 | opacity 整卡 + 一行文字 | **可展开 `<details>` 摘要条**，显示"已回答 N 题"，点开展开各题答案（数据来源与降级见 §2.4） |
| 多题进度 | `N Q` 徽标 | **`n / total` 步进计数**，回顾时显示"回顾" |

### 2.2 需要保留的 PicoClaw 特有机制

| 机制 | 处理 |
|---|---|
| `latest` 防错位逻辑（有更新未封存卡时旧卡禁用） | **保留**，按钮/选项 `disabled` 条件不变 |
| `submitting` 30s 恢复机制（回执丢失时） | **保留** |
| IM 通道的 `/skip` 与自由文本回复路径（`collectAskReply`） | **不改**（后端）；Web 卡片"你决定"改发语义文本后，后端 `tryHandleAskReply` 已能把任意文本当答案处理（`normalizeAskAnswer`），`/skip` 路径在 Web 卡片上**不再走**，但 IM 通道保留 |
| `seal` 三态（answered / skipped / done） | **保留**，完成态 `details` 摘要条根据 seal.verdict 显示不同文案/色 |
| `buildTurns` 的 AskInfo/AskSeal 结构 | **不改**，只在 `ask-card.tsx` 内部换渲染逻辑 |
| `Option.description` 可见 | 原“需确认是否透传”已核查关闭（§4）：`AskOption.description` 已透传，卡片直接渲染次行即可 |

### 2.3 视觉规格（对齐 Octop less，翻译到 PicoClaw Tailwind/shadcn 词汇）

**卡片整体**
- 待答：`border` 品牌色 42% 混色（PicoClaw 用 `border-sky-500/50` 替代，语义一致），`bg-sky-500/[0.05]` → 保持现值即可；**保留现有 `border-l-4` 左强调条组合**（四边 1px 品牌色 + 左 4px，评审定案：不改为四边等宽），只改内部布局
- 已完成：去掉蓝色，改为 `border-border/60 bg-muted/30`，`opacity` 由 75% 改为 100%，整体视觉由 `details` 摘要条承担

**标题行**
- 左侧：`IconHelpCircle`（保留）+ 标题（保留 `chat.askTitle`）
- 右侧：进度徽标 `n / total`（替换原 `N Q`）；有 `onDismiss` 场景的保留 ✕

**选项行**
```
+A  [label]                ← 18×18 圆角 chip，默认灰字灰边；选中→品牌色底白字
|  [description]           ← 次行，12px，muted-foreground
```
- 每行 `min-height: 42px`，`padding: 9px 11px`，`rounded-lg`，纵向 `gap: 6px`
- `hover:not(:disabled)`：`border-color` 品牌色，`bg` 浅，`translateY(-1px)` + 阴影
- `active:not(:disabled)`：`scale(0.99)`
- 选中：`border-sky-500` + `bg-sky-500/15`（PicoClaw 品牌色）

**TextArea**
- 评审更正：shadcn `ui/textarea.tsx` 是普通 textarea（无 autoSize prop）；用 `TextareaAutosize`（`react-textarea-autosize` 已在依赖中，`chat-composer.tsx` 先例），`minRows={1} maxRows={4}`，`margin-top: 6px`
- 多题卡提交前把答案内嵌换行压平为 "；"（§3 / §8-1，后端按行切分的协议约束）

**动作区**
- 顶部分隔线（`border-t border-border/60`）
- 步进模式：`[下一步]`（主按钮，当前题无答案时 disabled）+ `[返回]`（次按钮，首题隐藏）+ `[你决定]`（ghost，`ml-auto`）
- 回顾模式：`[提交回答]`（主按钮，所有题有答案时可用）+ `[返回]`（次按钮）
- "你决定"按钮文案：`t("chat.askSkip")` → 新 key `chat.askYouDecide`（默认中文"你决定"，英文"You decide"）
- 点击"你决定"：发送 `t("chat.askSkipMessage")`（"你决定吧——按最优默认方案继续，并告诉我你依据的假设"），**不再发 `/skip`**；后端会把这段文字当自由文本答案，与 `skipMessage` 语义一致

**完成态（seal 存在时）**
```html
<details class="…completed-card">
  <summary>
    <span>›</span>
    <span>已回答 3 个问题（绿） / 已跳过（灰） / 已完成（灰）</span>
    <span class="hint">查看</span>
  </summary>
  <div>
    <div>1. [header] question → 用户答案摘要</div>
    <div>2. …</div>
  </div>
</details>
```
- `details` 默认收起，摘要条 `border rounded-lg bg-muted/30 py-2 px-3`
- `::before` 显示 `›` 旋转箭头（`details[open]` 时 90°）
- 无本地答案（页面刷新 / IM 端作答）时"→ 用户答案摘要"部分不渲染，只显示问题行（§2.4）

**移动端（≤767px）**
- 选项行左缩进改为 0（`.options padding-left: 0`）
- 选项行 `min-height: 44px`
- 卡片 `padding: 12px`

### 2.4 完成态的数据来源与降级（评审新增）

`AskSeal` 只含 `verdict + at`，用户答案不在封存数据里，只存在于组件 `answers` state。三种情形：

| 情形 | 摘要数据 | 展示 |
|---|---|---|
| Web 卡作答后收到回执封存（组件未卸载） | 有 | 问题 + 所答摘要 |
| 页面刷新 / 历史重载后查看旧卡 | 无 | 只显示问题文本 |
| 用户从 IM 通道作答，Web 卡被封存 | 无 | 只显示问题文本 |

降级规则：`answerText(q, answers[i])` 为空或 state 缺失 → 只渲染问题行，不渲染答案摘要行（与 Octop 完成卡行为一致——它从不显示答案）。

---

## 3. 状态机（组件内部）

```
questions[]
  ↓
state: "step" | "review"
current: number（step 模式下当前题 index）
answers: Answer[]（与 Octop 一致：{ picked: string[], other: string }）
  picked 存 label 或 "__other__"；other 存自由文本
  ↓
answerText(q, answer) → string | null（评审修订：替代旧稿 isAnswered，
  与 Octop 同实现，是"该题是否已答"的唯一真相源）
  labels = picked 中滤掉 "__other__" 的 label
  (picked 含 "__other__" 或该题无选项) 且 other.trim() 非空 → labels.push(other)
  labels 为空 → 返回 null（未答）
  ⚠ 旧稿用 picked.length > 0 判"有选项已答"，会把"只点'其他…'未填文本"
  放行为已答 → 提交空行 → 模型收到 "(unanswered)"，属规格 bug，废弃。
  ↓
advance() = isLast ? setState("review") : current++
back()    = current > 0 ? current-- : (state === "review") ? setState("step") : null
  ↓
单选自动跳题（onAnswer 携带 changed 参数，评审补齐）：
  changed 是普通选项 → 选完即 advance（末题直接进 review）
  changed 是 "__other__" → 不自动跳（等自由文本填完，手动"下一步"）
  多选 → 不自动跳（手动"下一步"确认结束）
  ↓
submit()  = answers.every(answerText 非空) ? send(lines) : noop
  lines = 每题 answerText 逐行 join("\n")
  ⚠ 多题卡 join 前必须把每行内部的换行压平为 "；"（评审新增，P1）：
  后端 collectAskReply 对多题按 \n 切行、行 i = 题 i、超出行折叠进末题，
  中间题的"其他…"文本内嵌换行会让后续所有答案错位（§8-1）。
  单题卡例外：整条消息就是答案，换行是合法内容，不压平。
skip()    = send(skipMessage)
```

**与现有 `draft` / `textDraft` 的迁移**：
- `draft: Record<number, string[]>` → `answers[i].picked`
- `textDraft: Record<number, string>` → `answers[i].other`
- 现有 `useDraftFlow` 的判断逻辑废弃，改为统一步进流（单题单选：选完自动进 review；末题：进入 review 等"提交回答"）

---

## 4. 字段透传确认（评审已核查，✅ 关闭）

- `web/frontend/src/store/chat.ts` 的 `AskOption` 已含 `description?: string`
- `chat-pi/turns.ts` 对 `message.ask` 整包透传（无字段级解析，不裁剪）
- 现卡片 `title={opt.description}` tooltip 已在使用该字段

**结论：`description` 已透传，无需任何代码改动；§6 边界与 §7 Task 1 同步关闭。**

---

## 5. 文案 i18n 变更（`locales/zh.json` + `en.json`）

| 新 key | 中文 | English |
|---|---|---|
| `chat.askYouDecide` | 你决定 | You decide |
| `chat.askSkipMessage` | 你决定吧——按最优默认方案继续，并告诉我你依据的假设 | You decide — proceed with the best default and tell me your assumption. |
| `chat.askOther` | 其他… | Other… |
| `chat.askNext` | 下一步 | Next |
| `chat.askBack` | 返回 | Back |
| `chat.askReview` | 确认并提交 | Review & submit |
| `chat.askReviewTag` | 回顾 | Review |
| `chat.askSubmit`（沿用现有 key 与文案） | 提交作答 | Submit answers |
| `chat.askTextPlaceholder`（沿用现有 key 与文案，不新增） | 输入答案… | Type your answer… |
| `chat.askAnsweredSummary` | 已回答 {{count}} 个问题 | Answered {{count}} question(s) |
| `chat.askDismissedSummary` | 已跳过 | Skipped |
| `chat.askExpand` | 查看 | View |
| `chat.askDoneSummary` | 已完成 | Done |

沿用 key：`askTitle`、`askSubmit`、`askTextPlaceholder`（文案不改）。

**删除死键（评审修订）**：`askSkip`、`askFreeTextHint`、`askInputHint`、`askDraftHint`、`askSealAnswered`、`askSealSkipped`、`askSealDone`——旧稿称它们"仍被 IM 通道引用"不成立：IM 回执文案是 Go 硬编码（`publishAskReceipt`），不走前端 i18n；这些 key 的唯一消费者就是 `ask-card.tsx`，重构后不再引用，5 个 locale 文件同步删除。

**Locale 覆盖（评审新增决策）**：仓库共 5 个 locale（`zh`/`en`/`bn-in`/`cs`/`pt-br`），`fallbackLng: "en"`（`src/i18n/index.ts:48`）。新 key 先补 zh + en，其余三个 locale 暂走英文 fallback，后续有翻译再补。

---

## 6. 不需要改动的部分（明确边界）

| 文件 | 说明 |
|---|---|
| `pkg/agent/ask_user.go` | 后端协议、`/skip` 路径、`collectAskReply`、`normalizeAskAnswer` 全部保留；Web"你决定"发的语义文本会被当自由答案处理，模型能理解，不需要后端配合 |
| `pkg/bus/types.go` | `AskQuestion`/`AskOption` 结构不变 |
| `web/frontend/src/store/chat.ts` | `AskInfo`/`AskSeal` 类型不变（§4 已核查：`description` 已透传，无需加字段） |
| `web/frontend/src/chat-pi/turns.ts` | 不动（`message.ask` 整包透传，已含 `description`） |
| IM 通道（`pkg/channels/…`） | 保持 `/skip` + 逐行文本回复路径，与 Web 卡片互不影响 |

---

## 7. 实施任务清单

### Task 1 — 字段透传确认（评审已核查，✅ 关闭）
- [x] `AskOption.description` 已透传（`chat.ts` 类型 + `turns.ts` 整包透传），无代码改动

### Task 2 — i18n 文案
- [x] `locales/zh.json` + `en.json` 新增 §5 表列的 key（`bn-in`/`cs`/`pt-br` 靠英文 fallback）
- [x] 删除 §5 列出的死键（5 个 locale 文件同步）
- [x] "你决定" 的 skipMessage 文案定稿（与模型侧工具描述对齐，确保模型收到后知道"这是用户放弃让 agent 自主决定"）

### Task 3 — 组件重构（`ask-card.tsx`）
按 §3 状态机重写渲染逻辑：
- [x] `Answer` 类型 + `answers[]` 状态（替代 `draft`/`textDraft`）
- [x] 步进模式：当前题 `QuestionRow` 子组件（全宽选项行 + 字母 chip + 描述次行 + 其他 TextArea）
- [x] `answerText` 单一真相源："其他…"未填文本 = 未答；单选点"其他…"不自动跳题（§3）
- [x] 多题卡提交前把每题答案内嵌换行压平为 "；"（单题卡不压平，§3/§8-1）
- [x] TextArea 用 `TextareaAutosize`（`react-textarea-autosize` 已在依赖，`chat-composer.tsx` 先例）`minRows={1} maxRows={4}`——shadcn `ui/textarea.tsx` 无 autoSize，旧稿"shadcn 自带"说法有误
- [x] 回顾模式：所有题折叠摘要 + 可点回
- [x] 完成态：`<details>` 摘要条
- [x] "你决定" 按钮发 `skipMessage` 而非 `/skip`
- [x] 保留 `latest` / `submitting` / 30s 恢复逻辑
- [x] 移动端适配

### Task 4 — 视觉
- [x] 选项行样式对齐 §2.3 视觉规格
- [x] hover/active/focus-visible 状态
- [x] `steps` 区 `max-height: min(420px, 42vh)` 内部滚动

### Task 5 — 验证
- [x] 单元测试：状态机纯函数（`answerText` / 自动跳题规则 / 多题换行压平）vitest 单测，`src/features/chat/ask.test.ts` 已有 seal 测试先例；**压平逻辑无测试必回归（评审新增）**（实施：`src/features/chat/ask-flow.test.ts`，22 例全过）
- [ ] 单题单选 → 点选 → 自动进回顾 → 点"提交回答"
- [ ] 多题 → 步进 → 返回 → 回顾 → 提交
- [ ] 多选题 → 勾多 → 下一步
- [ ] "其他…" → TextArea 填写 → 提交（答案含自定义文本）
- [ ] "其他…"只点选不填文本 → "下一步"不可点；填完文本后可点（评审新增）
- [ ] 多题卡中间题的"其他"填多行文本 → 提交后各题答案不错位（压平生效，评审新增）
- [ ] "你决定" → 模型收到语义文本，继续执行
- [ ] 完成态展开 → 各题摘要可见
- [ ] 有更新未封存卡时，旧卡禁用
- [ ] IM 通道 `/skip` 行为不受影响（回归测试）

---

## 8. 风险与说明

1. **多题卡换行错位（评审新增，P1，唯一会造成数据损坏的项）**：后端 `collectAskReply` 对多题按 `\n` 切行、行 i = 题 i、超出行数折叠进末题。"其他…" TextArea 允许 1–4 行，中间题的内嵌换行会让该题之后的所有答案错位（例：3 题卡第 2 题填两行 → 第 3 题答案被并进多余行）。**对策：前端 join 前把每题答案内部换行压平（§3）**；单题卡整条消息即答案、换行合法保留。Octop 原版存在同样缺陷（其 submit 按 `header: answer` 拼行），不能以其为凭。
2. **"你决定"不再发 `/skip`（已识别 tradeoff）**：机制属实——`tryHandleAskReply` 把语义文本当自由答案，`normalizeAskAnswer` 匹配不到选项 label 时原样透传。但多题卡下这段文字只落到第 1 题，其余题模型收到 `(unanswered)`，封存显示"已回答"而非"已跳过"：信号弱化但方向仍可读（模型读得出"用户委托决策"）。对比 `/skip` 给出干净的 "The user skipped the questions. Decide how to proceed…" 工具结果。**决策：接受语义文本（与 Octop 一致），IM `/skip` 不变**；若实测模型误读，回退只需把 `send(skipMessage)` 改回 `send("/skip")` 一处。
3. **步骤式 vs 堆叠式的模型工具描述同步**：LLM 工具描述（`askUserToolDef`）里写的是"options become clickable"，没有说"步进"，**不需要改工具描述**——步进是纯前端渲染，对模型透明。
4. **`AskInfo` 的 `latest` 字段**：步进改造不改变"哪张卡可操作"的判定，`buildTurns` 逻辑不变。
5. **单题单选从"点击即发"变"两击提交"（已识别 tradeoff）**：单题单选是 ask_user 最高频形态，改造后多一步回顾。**决策：接受两击，换取交互模型统一**——回顾步骤提供反悔机会，且避免"单题即发"与"多题步进"两套路径并存（v1.5/v1.6 的封卡 bug 均源于即发路径）。备选：保留单题单选即发，实测高频不适再启用。
6. **完成态答案摘要的可用性**：`AskSeal` 只含 `verdict + at`，答案只在组件 state。Web 作答后封存可显示摘要；页面刷新或 IM 端作答则无数据，**降级为只显示问题文本**（§2.4）。
