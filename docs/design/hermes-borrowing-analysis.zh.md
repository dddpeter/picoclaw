# Hermes-Agent 设计借鉴分析

> 调研对象：`/works/workspace/ai/hermes-agent`（Nous Research 的产品级 agent，Python，~860M，本机同时部署的另一个 agent 生态）。
> 本文档梳理其设计中值得 picoclaw 吸收的要点，按性价比排序，并注明与 picoclaw 现有实现的对照。
> 调研日期：2026-09-08。方法：两个探索代理并行深挖状态层（`hermes_state_*.py`）与运行时能力（脚本 RPC / delegate / 技能闭环 / cron），另查插件体系。

## 背景

Hermes 与 picoclaw 是同一定位（个人部署、IM 通道、cron 自动化）的两代实现，已有互借先例：picoclaw 飞书流式卡片的页脚配色方案即来自 hermes-lark-streaming 插件。本次调研按"机制是否精炼、picoclaw 是否有对应痛点"筛选，而非按功能清单罗列。

一个前提认知：Hermes 的 `hermes_state_*` 状态层围绕 **SQLite 多进程共享单库**生长（WAL 模式协商、读连接预算、跨进程修复锁），而 picoclaw 是 JSONL 单写者模型——JSONL 追加写本身就是"天然 WAL"，Hermes 状态层大部分深水区 picoclaw 天然规避。**要借鉴的是上层语义（标题、轮换、修复账本），不是存储引擎。**

## 一、Python 脚本 RPC 调工具（优先级：高）

**来源**：`tools/code_execution_tool.py`（931 行）+ `code_execution_rpc.py`（183 行）+ `code_kernel.py`（804 行），合计 ~2.2K 行。

核心思想：**多步工具管道不该逐 step 进对话历史**。模型写一段 Python 脚本，`import hermes_tools` 后像普通函数一样调工具（`web_search(...)`、`read_file(...)`），整个脚本在一次 inference turn 内跑完，中间所有工具调用的 request/response 都不进历史，模型只看到最终 stdout。

关键工程细节：

- **动态生成 stub 模块**：按"沙箱允许列表 ∩ 会话启用工具"求交集生成 `hermes_tools`，每个工具是"签名 + docstring + 参数 JSON 表达式"三元素模板，stub 体即 `_call("web_search", {...})`——脚本内可用工具与模型可见工具自动对齐。
- **RPC 通道**：`{token, tool, args}` 换行分隔 JSON 走 Unix domain socket；宿主侧管道 = 常量时间 token 校验 → 工具白名单 → 调用次数预算（默认 50 次，只有真正分发的调用才扣）→ 静默分发（复用既有工具执行路径，继承该次执行的权限上下文）。
- **stdout 截断带恢复语义**：截断至 50KB（40% 头 / 60% 尾），溢出部分落盘并回传路径，下一轮可"恢复不重跑"。
- terminal 工具在脚本沙箱内剥掉 background/pty 等危险参数。

**对 picoclaw 的适配**：直击工具循环上下文膨胀这一最深痛点——现有的输出清理管线（`pkg/tools/output_clean.go`）和过程面板 16K 预算都是缓解手段，这一项是根治。Go 版无需 Python 持久内核与远程文件传输，核心只要"UDS + JSON line 协议 + 白名单 + 次数预算 + 截断"，几百行可复刻，与现有 exec 工具天然亲和（exec 跑脚本，脚本内回调工具）。

## 二、会话标题两阶段生成（优先级：高，性价比之王）

**来源**：`hermes_state_titles.py`（179 行）+ `agent/title_generator.py`。

两阶段：turn 开始时（仅前两个 exchange 且未命名）**同步**落一个确定性标题——从用户首条消息清洗截取（48 字符上限），在模型调用前落库，不可能失败；随后 daemon 线程用便宜小模型升级（max_tokens=64、temperature=0.3、JSON schema 约束），只基于用户开场消息。

关键工程细节：

- **溯源优先级 `derived < llm < user`**：用户手动命名永不被覆盖；读写在一个 CAS 事务里（`WHERE title IS ? AND title_source IS ?`），手工命名与在途生成竞争时不被清掉。
- **Answer-shaped guard**：小模型有时"回答"而非"命名"，结果超过 12 词直接拒绝重试（不截断——截断存的仍是半个回答）。
- **机器开场白黑名单**：compaction handoff、model-switch marker、`[System note:` 等前缀不触发标题，否则会话会被命名为系统提示。
- 标题语言默认跟随用户消息语言。

**对 picoclaw 的适配**：几乎零基础设施依赖——JSONL 会话文件加 `title`/`title_source` 字段即可。launcher 的会话历史菜单（`session-history-menu.tsx`）现在显示裸 session key，这是最直接可感知的体验提升。约半天工作量。

## 三、技能自改进闭环·轻量版（优先级：中高）

**来源**：`agent/background_review.py`（1211 行）+ `agent/curator.py`（1084 行）+ `tools/skills_tool.py` 等约 30 个模块。**只借核心闭环，护栏体系不搬。**

闭环机制（非显式触发）：

- 每 turn 结束检查计数器：距上次技能审查 ≥ 10 次工具迭代 → 后台 fork 一个小代理，重放对话快照，问"该保存/更新什么技能？"，**直写技能库，绝不碰主对话与 prompt cache**。
- fork 继承父的 provider/model/凭据，复用同一 prefix cache 命中以省 token。
- 提示词要求"活跃更新"：多数会话至少一次小更新，无所事事视为错失学习；**用户纠正语气/流程是一等信号**；优先 patch 本会话刚加载过的技能。
- cron 等无人工场景用 `skip_background_review` 关闭。

**对 picoclaw 的适配**：picoclaw 已有五级技能加载（fork 增强）和 OpenViking 记忆蒸馏（recall/commit）——OpenViking 存"事实"，技能库存"可复用操作流程"，二者互补。轻量版 = 迭代计数器 nudge + 轮末后台小模型审查 + SKILL.md patch 工具，几百行。审计账本（`skill_ledger.py`）、curator 生命周期、hub 同步、防注入 guard 均不搬。

## 四、压缩轮换谱系（优先级：中）

**来源**：`hermes_state_compression.py`（663 行）+ `agent/context_compressor.py`。

不是"原地压缩消息"，而是**会话轮换**：上下文逼近阈值时把 parent 会话以 `end_reason='compression'` 关闭，LLM 生成摘要，发布一个继承元数据（cwd/git/user/chat 绑定）的 child 会话。picoclaw 已有 `contextManager.Compact`（summarize），缺的是轮换语义：

- **原子发布点**："关 parent + 建 child + 写 handoff"在一个事务里，读者要么看到活 parent 要么看到完整 child（文件系统层用"写 tmp + rename"可得同等语义）。
- **Watermark 补尾**：慢摘要期间 parent 新落的 appends 事后克隆进 child。
- **失败冷却退避**：`compression_failure_cooldown_until` 等计数器，压缩失败后冷却，不无限 thrash。
- **谱系查询**：递归 CTE 走 parent/child 链找 tip，原文永远留在已关 parent 供回溯。

## 五、仅借概念的部分

| 概念 | 来源 | 对 picoclaw 的落点 |
|---|---|---|
| cron wakeAgent 脚本门 | `cron/scheduler_script.py`：tick 脚本输出 `{"wakeAgent": false}` 则整体跳过（不跑 LLM 不投递） | 心跳/巡检类任务先跑一个廉价脚本判定"有无事可做"，无事直接返回——当前心跳每次都烧一次完整 turn |
| cron 与 gateway 的进程关系 | `gateway/run.py` 内 `InProcessCronScheduler`：gateway 活着时每 60s 从后台线程 tick + 文件锁 | 直接解 picoclaw 已知坑"CLI 改 jobs.json 运行中网关不感知"（见 fork-overview） |
| 投递 at-most-once | `cron/delivery_queue.py`：gateway claim 后死掉标记 unknown 永不重试（宁可丢一条不重复发） | cron 投递语义参考 |
| delegate 预算化 summary | `tools/delegate_tool*.py`：子代理结果聚合为 summary 回传，中间工具调用/推理**永不进父上下文**；0.5s 轮询 `wait(FIRST_COMPLETED)` 防 wedged child 卡死父级 | picoclaw 已有 spawn 工具，对照升级两点：结果预算化回传 + 防卡死轮询 |
| JSONL 损坏修复账本 | `hermes_state_repair.py`：修复前 raw 备份 + 按文件指纹限尝试 3 次（曾有不可修复损坏每次重启重跑、累计 105 次备份的教训） | JSONL 半行损坏 → 备份 + 截断到最后完整行 + 尝试上限账本，~50 行 |
| 测试不开生产库 | `hermes_state_guard.py`：env + 祖先进程链双检 | 任何有持久化的 agent 都值得，~50 行 |
| 轨迹压缩策略 | `trajectory_compressor.py`：head/tail 保护（system/首条/最近 N 轮）、中间按需摘要、**绝不拆开工具调用对** | picoclaw `Compact` 的策略校准 |
| 用量记账归因 | `hermes_state_usage.py`：per-model 归因表（session × model × billing × task），中途切模型时 token 记到当次活模型；辅助调用（标题/压缩）分离记账不进会话汇总 | picoclaw 已有 turn usage 统计，缺 per-model 归因与辅助调用分离；轻量版在 JSONL 消息级记 usage 对象即可 |

## 六、评估后不借鉴的部分

| 项 | 理由 |
|---|---|
| WAL 模式管理 / 读连接池（`hermes_state_wal/readpool`） | SQLite 多进程专深水区（macOS fsync 语义、NFS/ZFS 兼容矩阵、EMFILE 预算）；JSONL 单写者天然规避。仅留三条原则：永不在线降级持久化格式、信任操作返回值而非异常缺席、变更前探测现状 |
| 20 平台投递 lane（`scheduler_delivery.py` 1714 行） | picoclaw 只用飞书，按生产规模长出来的肉 |
| 技能 curator 全套 / 审计账本 / hub | 重量级护栏，轻量闭环稳定后再评估 |
| startup watchdog（`hermes_startup_watchdog.py`） | 解决 Python gateway 事件循环死锁且 s6 不重启的问题域；Go 单进程 + systemd 无此问题 |
| context_engine 插件 | 仅扩展点脚手架（93 行 discovery），本 checkout 无实体引擎 |
| 三层 FTS 全文搜索（词级 BM25 + trigram + CJK bigram） | 对轻量 agent 过度；若将来做会话搜索，取"external-content 索引 + fail-open 降级 LIKE + 查询净化"的模式即可，最小版一个 trigram 索引 |

## 落地建议

1. **第一项**：会话标题两阶段生成（新文件如 `pkg/session/title.go` + JSONL 头部字段），半天，launcher 会话历史直接可感知。
2. **第二项**：脚本 RPC 调工具（`pkg/tools/` 下新增 code 工具 + UDS RPC server），2-3 天，根治工具循环上下文膨胀；与 exec 安全体系（custom-only 拦截、白名单）对齐后再上。
3. **第三项**：技能自改进轻量版（`pkg/agent/` 轮末 hook + 计数器 nudge + 后台小模型审查），2-3 天。
4. 压缩轮换谱系随下一次 context manager 改造顺带；第五节概念各自随对应模块迭代参考，不单独立项。

## 实施状态

- 2026-09-08：调研完成，本文档存档。
- 2026-09-08：**第一项（会话标题两阶段生成）已实施**。分层：`pkg/memory/jsonl.go`（SessionMeta 增 title/title_source + SetSessionTitle CAS 优先级 user>llm>derived）、`pkg/session/jsonl_backend.go`（TitleAwareSessionStore 可选能力）、`pkg/agent/session_title.go`（两阶段核心：turn 开始派生标题 + 后台轻模型升级，answer-shaped guard、机器消息黑名单、进程内单次升级去重）、`/title` 手动命令、launcher `GET /api/sessions` 优先返回存储标题。配置 `agents.defaults.session_titles.enabled`（未配置=开启）。测试锚点：`TestSetSessionTitlePriorityMatrix`、`TestMaybeTitleSession*`、`TestTitleCommand*`。
- 第二项（脚本 RPC）、第三项（技能闭环）未实施。
