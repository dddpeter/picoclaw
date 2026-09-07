# MiMo-Code 设计借鉴分析

> 调研对象：`/docs/myworks/MiMo-Code`（小米 MiMoCode，opencode 的 fork，TS/Bun 技术栈）。
> 本文档梳理其设计中值得 picoclaw 吸收的要点，按优先级排序，并注明与 picoclaw 现有实现的对照。
> 调研日期：2026-09-07

## 背景

MiMoCode 的价值不在代码本身（技术栈、产品形态与 picoclaw 均不同），而在 `docs/harness/` 与 `docs/architecture/` 下的几篇设计文档：

| 文档 | 主题 |
|---|---|
| `Mix of Harness and Hand-off.md` | 低收益循环检测（Try-Best）与跨 harness 交接 |
| `MiMo Token Efficient Mode.md` | Bash 输出 token 清理管线 |
| `Agent Multi-Skill Workflow Orchestration Design.md` | 多 skill 协同 Reminder 注入 |
| `retry-coordinator.md` | Provider 重试的分层语义 |
| `MiMo Orchestrator Mode.md` | Orchestrator 子会话编排 |
| `codex-microkernel-runtime.md` | 模型专属工具 ABI + QuickJS exec 微内核 |

## 一、Try-Best 低收益循环检测（优先级：高）

**来源**：Mix of Harness and Hand-off。

核心思想：agent 失败的前兆不是"某一步错了"，而是**继续烧 token 也换不来进展**。失败模式在工具调用轨迹上有可观测的形状，挑三类最强信号即可：

| 信号 | 判定方式 | 默认阈值 |
|---|---|---|
| `edit_repeat` | 对同一文件的近似编辑：diff 抽 3-shingle 集合，Jaccard 相似度 > 0.8 | 滑动窗口 12 次内累计匹配 ≥ 2 次 |
| `bash_retry` | 归一化后的命令连续失败，且失败输出无变化 | 连续 3 次 |
| `action_streak` | 连续同类动作（edit / verify）无可观察改善 | 连续 4 次 |

关键工程细节：

- **命令归一化**：`/tmp/...` → `<TMP>`、6 位以上纯数字 → `<NUM>`、`--seed=xxx` → `<SEED>`；结果抹掉 `Ns / Nms` 时长、超 2000 字符头尾各留一半。避免时间戳/临时路径/随机 seed 造成误判。
- **verify 类命令**（test / typecheck / lint / build / go test 等）同时参与 `bash_retry` 与 `action_streak` 计数。
- **命中后**：重置检测器 → 暂停当前 turn → 将原因与证据写入持久化记录（part 是事实来源，事件仅作低延迟通知）→ 通知用户决策（换执行器 / 原模型换策略重规划）。
- **幂等**：同一 turn 反复触发需 reset；不向对话无限追加提示文本。

**对 picoclaw 的适配**：实现成本极低（滑动窗口 + Jaccard，零外部依赖）。picoclaw 具备 heartbeat/cron 后台任务与 IM 通道，"检测到死循环 → 主动止损 + 发消息到 channel 附证据"是天然落点。不引入跨 harness 交接（依赖其 skill 生态，与 picoclaw 定位不符），仅吸收检测部分。

## 二、Bash 输出 Token Efficient 管线（优先级：高）

**来源**：MiMo Token Efficient Mode。

picoclaw 现状：`pkg/tools/shell.go` 已有尾部截断 + 原始输出落盘（`persistFullOutput`），但 inline（送模型）这一路没有清理。MiMo 的三路分流原则——**仅清 inline、不清落盘、预览不动**——与 picoclaw 现有结构天然对齐，只差 inline 清理：

### 2.1 通用清理管线（按序）

| 层 | 职责 | 要点 |
|---|---|---|
| progress | 按行折叠 `\r` 进度条 | 每行只留最后一个 `\r` 之后的片段；必须先于 ANSI 剥离 |
| ansi | 剥 ANSI CSI/OSC/DCS、退格 overstrike、控制字节 | 4 条 ESC 正则 + 控制字节字符类（保留 `\t \n \r`） |
| redact | 密钥脱敏 | PEM 整块、Bearer/JWT、AWS `AKIA|ASIA`、GitHub `gh[pousr]_`、OpenAI/Anthropic `sk-`、Slack `xox`、通用 `KEY=VALUE` |
| longline | 单行超 500 字符压成 head 160 字符 + 省略提示 | 放最后兜底 |
| never-worse | 清理后字节数没变小则回吐原文 | 管线尾部守门 |

### 2.2 启发式形状剪裁（可选增强）

按"命令名通道 + 内容指纹通道"双路识别输出形状，针对性剪裁（预期减量）：git diff 85%、pytest 90%、npm install 65%、make 53%、Traceback 69%、tsc 80%、go test -json 90%（NDJSON 按 pkg 聚合 pass/fail/skip）。

扩展契约：`Shape { id, match, apply }` 接口，主入口零侵入。

命令层 passthrough：命令含 `--json` / `-o json` / `| tee` / `# nofilter` 时直接放行。

**收益**：直接节省上下文 token；redaction 部分另有安全收益（防止 API key/JWT 进入模型上下文）。

## 三、动态指令下沉到 message 层（优先级：中高）

**来源**：Agent Multi-Skill Workflow Orchestration Design。价值在两条通用工程原则，而非 skill 场景本身：

1. **注入位置**：system-injected 消息附加在 user message 之后，不改写 system prompt。
   - 靠近 query，指令遵循率更高；
   - 不污染前缀，prefix cache 保持稳定；
   - 可做 turn 级按需注入。picoclaw 有 `pkg/agent/context_cache` 相关实现，此原则对 `pkg/agent/agent_inject.go` 的注入设计是直接参考。
2. **精确条件触发**：如多 skill 场景仅在引用数 ≥ 2 时注入编排 Reminder；单 skill 或无 skill 完全不感知。避免简单任务被诱发过度规划。

## 四、Retry Coordinator 补强（优先级：中）

**来源**：retry-coordinator。

picoclaw 已有 `pkg/providers/error_classifier.go`（错误分类）+ `pkg/providers/cooldown.go`（failover 冷却），大方向已覆盖。可补两个细节：

- **副作用边界（replaySafe）**：收到 tool-call 之后再遇到 stream 错误，禁止自动重放整个 model step——只能保存当前工具状态并终止本轮，防止工具副作用重复执行。
- **persistent network retry**：仅针对 request 建立阶段的纯连接失败，允许长时间等待网络恢复（5s 起步、60s 封顶、无 jitter、受 deadline/AbortSignal 约束），区别于"换 provider"的 failover 路径；quota/auth/context overflow 不适用。
- 次要：Retry-After header 优先于指数退避；每次重试更新同一状态而非向 transcript 追加文本。

## 五、评估后不借鉴的部分

| 项 | 理由 |
|---|---|
| Orchestrator 子会话模式（session 工具 8 verb、worktree 隔离、审批转发） | 工程量大；picoclaw 的多 agent 注册 + IM 通知模型已部分覆盖 |
| QuickJS `exec` 微内核 / GPT 专属工具 ABI | Go 等价物为 goja，收益不明显；可留作备忘："按模型路由系统提示词 + 工具集裁剪" |
| 跨 harness hand-off（拉起 Codex/Claude CLI 子进程） | 依赖其 skill 生态与 CLI 形态，与 picoclaw 定位不符 |

## 落地建议

1. **第一项**：Try-Best 检测器（新文件，如 `pkg/agent/turn_health.go`），检测命中后通过 channel 发送止损通知。
2. **第二项**：shell 输出清理管线（改造 `pkg/tools/shell.go` 的 inline 路径，redact 层可独立复用），保持落盘原始输出不变。
3. **第三、四项**：作为 `agent_inject.go` 与 provider retry 现有改造的设计校准，随相关迭代顺带落地。
