# 全仓 Bug 排查评审与修复（2026-10-04）

状态：**已修复（六批次落地，30/31 项）**｜排查、评审与修复 2026-10-04｜排查基线：`b3dab79e`

本文记录一次覆盖五个方向的整仓 bug 排查与逐条评审：`pkg/agent`（2.9 万行）、
`pkg/channels`（2.9 万行）、`pkg/tools`（1.7 万行）、`pkg/providers`（1.1 万行）、
外围包（config/cron/session/bus/gateway/utils/memory/skills，约 2 万行）。共排查出
31 项候选问题，本文逐条给出评审结论（**修复 / 缓修 / 设计内不修**）与修复方案；
已确认项按六个批次落地（执行记录见文末）。

## 0. 排查方法与证据等级

- 五个方向并行深查（每方向一次全量走读），输出均带完整推理链；对高危项做了
  独立复核与实证：
  - **已实证**（本机探针/临时测试实际触发，验证后删除）：A1（steering 重复注入）、
    A2（并发双 turn）、T2（`\\?\` 绕过系统目录保护）；
  - **源码复核确认**（本会话逐行走读关键链路）：C2、F1、P1、T1；
  - **走读确认**（子代理带推理链报告，主审复核关键行）：其余各项。
- 排除项：AGENTS.md 声明的设计行为（runTurn 读锁、TryLock 降级、busy 语义、
  `*bool` 默认开、开放默认三件套、输出清理管线 never-worse 等）一律不报；
  git 历史已修复问题（Windows exec 死锁、B1/B4/B5、429 UX 等）不重复报。
- 行号以基线 `b3dab79e` 为准。

## 1. 总表

| # | 模块 | 定级 | 一句话 | 评审结论 |
|---|------|------|--------|----------|
| A1 | agent | **P0** | steering 消息切片自追加，直接答案路径下重复注入+重复持久化 | 修复 |
| A2 | agent | P1 | system 消息绕过会话占用锁，可并发第二个完整 turn | **缓修**（结构项） |
| A3 | agent | P1 | stall watchdog 的 graceful 收尾在卡点位于 LLM 调用内时反而报错 | 修复 |
| A4 | agent | P2 | 工具落盘 check-then-act 与 HardAbort 封口竞态 → 重复 tool_call_id | 修复 |
| A5 | agent | P2 | mcpRuntime.reset() 无锁替换 sync.Once，可双重初始化 | 修复 |
| A6 | agent | P2 | 心跳/答案写与流式卡封口无互斥，seq 倒置可判可见失败 | 修复（致命形态） |
| C1 | feishu | **P0** | B2 复用矩阵 + chatID 单键 map → 每迭代孤儿新卡且永不封 | 修复 |
| C2 | feishu | **P0** | 最小封卡回退丢 12KB–24KB 区间答案 | 修复 |
| C3 | feishu | P1 | 复用 TTL(2min) < 心跳(180s)，"活着但安静"的答案被吞 | 修复（TTL 提升） |
| C5 | feishu | P2 | ProgressNote 永不清除：封卡残留状态行 + 破坏空卡删除语义 | 修复 |
| D1 | wecom | P1 | 媒体送达后 caption 失败未计数 → B6 重试重复发媒体 | 修复 |
| D2 | telegram | P1 | 超长 caption 的 leading 文本不受部分送达保护 → 重试重复 | 修复 |
| D3 | discord | P2 | 超时路径关闭仍被发送 goroutine 读取的文件句柄 | 修复 |
| T1 | tools | P1 | PTY 后台会话永久泄漏 fd + goroutine | 修复 |
| T2 | tools | **P0** | `\\?\` 扩展路径前缀完整绕过系统目录保护 | 修复 |
| T3 | tools | P1 | runSync 输出无上限缓冲，高输出命令可 OOM 网关 | 修复 |
| T4 | tools | P1 | 非 PTY 后台会话 stderr 不可见 + 孙进程持管道永久卡死 | 修复 |
| T5 | tools | P2 | 开放模式文件工具相对路径按进程 CWD 而非 workspace 解析 | 修复 |
| T6 | tools | P2 | 会话清理按 StartTime，长任务退出即被删（与文档承诺相反） | 修复 |
| T7 | tools | P3 | edit/append 全量读无大小上限 | 修复（守卫） |
| T8 | tools | P2 | 写文件固定 0600，编辑既有脚本静默清掉执行位 | 修复 |
| T9 | tools | P2 | BM25 缓存快路径数据竞争 | 修复 |
| T10 | tools | P2 | Windows 后台会话 kill 不走 Job Object | 修复 |
| P1 | providers | **P0** | finish_reason "length"→"truncated" 归一化使截断护栏失效 | 修复 |
| P2 | providers | P1 | Gemini 流式无响应头超时，上游黑洞时 turn 永久挂死 | 修复 |
| P3 | providers | P1 | anthropic SDK provider 完全无请求超时 | 修复 |
| P4 | providers | P2 | Retry-After 只对 429 生效，5xx 丢弃服务器重试间隔 | 修复 |
| P5 | providers | P2 | anthropic-messages / alibaba-coding 忽略 cfg.Proxy | 修复 |
| P6 | providers | P3 | Gemini SSE 强制 `data: ` 带空格，不合规网关静默丢事件 | 修复 |
| P7 | providers | P3 | 流式 tool_call 组装假定 index 从 0 连续 | 修复 |
| F1 | cron | **P0** | 任务执行期间旧快照覆写 jobs.json，静默回滚外部编辑 | 修复 |
| F2 | cron | P1 | 任务失败恒记 "ok"，LastStatus/LastError 失真 | 修复 |
| F3 | skills | P1 | GitHub 安装器未校验 API 文件名，路径穿越 | 修复 |
| F4 | memory | P1 | `.meta.json` 损坏后 session 永久变砖，无恢复路径 | 修复 |
| F5 | cron | P3 | `CronSchedule.TZ` 被持久化比较但从未生效 | 修复 |
| F6 | utils | P3 | HTML→Markdown 后处理剥掉代码块内行首空格 | 修复 |

## 2. pkg/agent

### A1（P0，已实证）：steering 消息重复注入与重复持久化

`pipeline_llm.go:633`（直接答案路径发现 steering → 写入 `exec.pendingMessages` 并
返回 ControlContinue）+ `turn_coord.go:226`（`pendingMessages = exec.pendingMessages`
**别名同一切片**）+ `turn_coord.go:136`（下一迭代 `append(pendingMessages,
exec.pendingMessages...)` 把切片追加到它自己，元素翻倍）。实证：gating provider
阻塞首个 Chat 期间入队 steering，日志 `Injected steering message` 出现两次，
GetHistory 中 steering 内容 ×2。会话历史永久污染。

**修复**：`turn_coord.go:226` 别名赋值后置 `exec.pendingMessages = nil`（所有权
转移语义，与 `:137` 的置 nil 对齐）。

### A2（P1，已实证并发）：system 消息绕过会话占用锁 —— 缓修

普通消息经 `LoadOrStore` 占位防并发（agent.go:188-194），system 消息完全绕过：
异步工具在原 turn 仍在跑时完成 → 回调发布 system 消息 → `processSystemMessage`
无条件 `registerActiveTurn` 并启动完整 LLM turn。实证：maxInFlight=2 并发飞，
向同一聊天交错发布两条回复。历史损坏仅在主会话被占用时发生（渠道 turn 走
scoped 会话）。

**缓修理由**：正确语义需要设计——system 消息（异步工具结果/subagent 回执）
到达时若原 turn 活跃，应当会合进当前 turn（steering 式）还是排队等 turn 结束，
两条路都影响异步结果投递语义，且涉及 `ProcessDirect`/cron 占用主会话的交互。
**记录方案**：① 最小止血——`processSystemMessage` 对已占用会话返回 busy 并把
消息重新入队（延迟到下个 tick 重投）；② 结构项——异步回执改走 steering 通道
注入当前 turn。两者都需独立评审，本次不动。

### A3（P1）：stall watchdog graceful 收尾退化为 turn 报错

watchdog（progress_heartbeat.go:77-91）graceful 中断时 `cancelProviderCall()`，
但 CallLLM 重试循环（pipeline_llm.go:316）只认 `hardAbortRequested && Canceled`；
graceful 场景 `context.Canceled` 被 ClassifyError 判不重试 → break → CallLLM
返回错误 → runTurn 以 `TurnEndStatusError` 终止。用户先看到"正在请求模型收尾
总结"，随后得到"⚠ 模型调用失败"——与 d38ad30d 承诺相反；watchdog 的主场景
（卡在 LLM HTTP 调用内）恰好全走这条错路。

**修复**：重试循环中 hardAbort 检查之后加 graceful 分支——`gracefulInterruptRequested()
&& errors.Is(err, context.Canceled)` → 记日志后 `return ControlContinue, nil`，
让 turn 循环自然进入下一迭代（providerCtx 每次 CallLLM 新建，模型获得收尾机会；
若继续卡死，watchdog 既有升级路径 hard abort 兜底，循环有界）。

### A4（P2）：工具落盘与 HardAbort 封口的 TOCTOU

`appendToolResultMessage`（pipeline_execute_loop.go:70-75）先查
`persistsToolMessages()`（= !hardAbortRequested）再 `AddFullMessage`；`/stop`
在另一 goroutine `requestHardAbort + sealAbortedTurnSession`（steering.go:535-546，
seal 走 GetHistory→补封→SetHistory）。检查通过后 seal 先完成 → 真实 tool 结果
落在封口 note 之后 → 同一 tool_call_id 两条 tool 消息 → 下次请求 400。

**修复**：给 turnState 加 `persistMu`，`appendToolResultMessage`/
`appendToolNoticeMessage` 在 persistMu 内做 check+persist；`sealAbortedTurnSession`
的 Get/SetHistory 对同样在 persistMu 内。锁序单一（persistMu 不嵌套其它锁），
封口与落盘全序化：先落盘则 seal 读到的历史含真实结果（判不 dangling，不补封），
先封口则落盘检查失败（跳过）。

### A5（P2）：mcpRuntime.reset() 与惰性初始化竞态

reset 在 `r.mu` 下写 `r.initOnce`（agent_mcp.go:30-38），`ensureMCPInitialized`
无锁读 `al.mcp.initOnce.Do`（:117）。/reload 与惰性初始化并发时可双重初始化，
且构成数据竞争；Do 体调用 setInitErr/setManager（取 r.mu），故不能简单用 r.mu
罩住 Do。

**修复**：加 `doMu sync.Mutex`——Do 的调用与 reset 的 Once 替换都串行在 doMu
上（锁序恒为 doMu→r.mu，Do 体内 setManager 取 r.mu 不反向，无死锁）；reset
等待在途 Do 完成后再换新 Once，语义正确（reload 后干净重来）。

### A6（P2，与 C4 同源）：心跳/答案写与封口的 seq 倒置

`Update` 在锁外取 seq 后发 HTTP（feishu_stream.go:479-490），心跳
`AppendToolStep→flushPanelLocked` 持 `publishMu` 同步发全卡刷新可插入其间；
N+1 先到则 N 被判序列回退拒绝 → ErrTemporary → publisher.err 置位 → 整个正常
turn 以可见错误收场（pipeline_streaming.go:159-169）。先前评审（飞书卡片评审
§9.3）以"非致命、有日志"记录不动——本次复核确认后果升级为**可见失败**，且
B3 让心跳成为固定高频调用方，长期运行必然偶发。

**修复（只修致命形态）**：管线侧让 `publisher.Update` 调用点也走 `exec.publishMu`
（与 AppendToolStep 同锁），答案写与面板刷新全序化，消掉现实触发路径；
AppendToolStep-vs-Finalize 的残余竞态错误被吞（非致命），维持既有取舍不修。

## 3. pkg/channels（飞书 + 跨通道）

### C1（P0）：B2 复用矩阵在 map 单键下产生每迭代孤儿卡

`streams` 以 chatID 为唯一键（feishu_stream.go:277 `Store(chatID, s)`），而 agent
**每次 LLM 迭代**都重新 `GetStreamer→BeginStreamForSession`（pipeline_streaming.go:71）。
同 chat 两会话并发 turn：A 存 A1 → B 见"异会话+新鲜"→ 新建 B1 覆盖 → A 迭代 2
见 B1 → 新建 A2 覆盖……交替期间每迭代一张新卡，被覆盖的中间卡不在 map 里，
stale 封卡（只作用于 map 当前条目）永远轮不到，直到飞书服务端 200850 超时；
`NotifySteeringInChat`/`chatHasActiveStreamer` 也只按 chatID 查，提示会打到对方
会话的卡上。正是 B2-2 声称修复的 `dm_scope=sender` 场景。

**修复**：map 键改为 `chatID`（sessionKey 为空时）或 `chatID + "\x00" + sessionKey`
（scoped）；BeginStreamForSession 先查本会话键（同会话复用矩阵不变），再对本
chat 的其它条目做陈旧清扫（≥TTL 未封 → 异步 superseded 封板；新鲜不动——两卡
各有 owner，不再孤儿化）；Finalize/Cancel 的 CompareAndDelete 用同样的键；
`NotifySteeringInChat`/`chatHasActiveStreamer` 改为按 chatID 前缀 Range（活跃
streamer 数量极小，代价可忽略）。

### C2（P0，源码复核确认）：最小封卡回退丢失 12KB–24KB 答案

`FinalizeWithContext`（feishu_stream.go:735-765）：`inCard/remainder` 按 24KB
预算切分；常规封卡被拒后最小封卡只装 `hardIn`（12KB），但
`deliverAnswerRemainder(ctx, remainder)` 仍投递 24KB 起的余量——**[12KB,24KB)
区间任何地方都没送达**，尾注还声称"余下部分见后续消息"。

**修复**：最小封卡成功后，用 12KB 硬预算重算余量（`_, hardRest :=
clampFeishuAnswerForCard(composed, feishuAnswerHardBudget, ...)`），以 hardRest
替换 remainder 再补发。Cancel 路径本就不补发（中断语义），不动。

### C3（P1）：TTL(2min) < 心跳(180s)，安静 turn 的最终答案被吞

`feishuStreamReuseTTL`=2min，进度心跳默认 180s。静默超过 2 分钟的阶段内，同
chat 任何新 turn 的清扫会把这张"stale"卡封成 superseded；原 turn 恢复后
Update 静默丢弃、Finalize 见 done 返回 nil——**完整答案消失且零报错**。

**修复**：TTL 提到 5min——心跳每 180s 必刷一次 lastAt（AppendToolStep 刷新），
存活 turn 的静默间隙 < 3min，TTL=5min 使"活着被判 stale"在心跳开启时不可达；
死卡的封板延迟从 2min 变 5min，可接受。心跳被用户关闭时的残余风险记录在案。

### C4：见 A6（Update 纳入 publishMu）。

### C5（P2）：ProgressNote 永不清除

B3 的 ProgressNote 只在下一次 beat 覆盖，成功封卡后永久残留最后一条"⚠️ 进度"
文案，与"✓ 已完成"矛盾；且 `hasPanelContent()` 因此对只收到过心跳的卡返回
true，`CancelWithReason` 的空卡删除不再成立——预输出失败的 turn 留下"⚠ 已中断"
卡垃圾。

**修复**：① Finalize/Cancel 封卡前清空 `state.ProgressNote`（最终卡不再带状态
行）；② `hasPanelContent()` 排除 ProgressNote（恢复空卡删除语义——只有心跳的
卡在取消时仍按空卡删，有实际内容的卡不受影响）。

### D1（P1，wecom）：caption 失败未计入已送达数

`wecom.go:286-293`：每 part 的媒体发送成功后 `sent++` 在 caption 推送**之后**；
caption 推送失败 → `MediaSendErr(sent=0, ErrTemporary)` → manager 整批重试 →
已送达媒体重发。feishu/slack 都在 caption 失败时报 sent>0，wecom 是十通道中
唯一错位（b3dab79e 修了 caption-only 分支，漏了 media-then-caption）。

**修复**：媒体发送成功后立即 `sent++`，caption 失败时以含本 part 的 sent 上报
（自动降级 ErrSendFailed，不重试）。

### D2（P1，telegram）：leading caption 文本不受部分送达保护

`telegram.go:646-663`：caption >1024 rune 先 `sendCaptionText` 分 chunk 发出，
随后媒体组失败 `return nil, err`——err 的 sent 计数只含组内，leading 文本不计
→ 重试把 caption chunk 再发一遍。`sendCaptionText` 自身第 k 个 chunk 失败同样
裸返回（前 k-1 个已送达）。

**修复**：① `sendCaptionText` 失败时 `MediaSendErr(len(messageIDs), err)`；
② SendMedia 媒体组失败分支：若已有 leadingIDs，以 `MediaSendErr(len(leading)+组内已发,
err)` 上报并返回部分 messageIDs。

### D3（P2，discord）：超时路径关闭在用句柄

`discord.go:356-366`：sendCtx.Done 分支在发送 goroutine 仍在 `Read` 这些文件时
全部 `Close`（Go 的 os.File 不允许 Close 与并发 Read）。

**修复**：文件关闭职责整体移交发送 goroutine（goroutine 内 defer close 三个
reader），select 两分支的关闭循环删除——done 分支关闭本就冗余，超时分支关闭
即数据竞争。

## 4. pkg/tools

### T1（P1）：PTY 后台会话永久泄漏 fd + goroutine

`shell.go:757-771`：`pty.Open()` 的 slave `tty` 赋给 cmd 三个 stdio 后**全文件
无关闭点**；Kill/Remove/cleanupOldSessions 都不关 `ptyMaster`。父进程持有 slave
fd → master Read 永不 EOF → 读 goroutine 永久阻塞，会话对象（含 1MB buffer）
无法 GC，反复使用耗尽 fd。

**修复**：① `isolation.Start` 成功后立即关闭父进程的 `tty`（子进程已 dup 自己
的副本）；② session 的 Kill/清理路径关闭 `ptyMaster`（含 cleanupOldSessions
对已完成会话的回收点）。

### T2（P0，已实证）：`\\?\` 前缀绕过系统目录保护

`system_paths.go:88-133`：`\\?\C:\Windows\...` 经 Clean/GetLongPathName/
EvalSymlinks 后三个 candidate 都带前缀，`hasPathPrefix` 对全部保护前缀 miss →
`IsProtectedSystemPath` 返回 false；`validateWritePath` 同样放行。实证探针：
`EvalSymlinks("\\?\C:\Windows")` 成功且保留前缀。exec 侧 `guardCommand`
（shell.go:1472-1479）显式剥前缀——作者知道该别名，fs 侧遗漏。默认开启的
`protect_system_paths` 被完整穿透（读写皆可）。

**修复**：`hasPathPrefix`/candidate 归一化时剥 `\\?\`（与 guardCommand 同法；
`\\?\UNC\` 前缀同步处理为 `\\`），三条候选路径（cleaned/long/evaluated）统一
过归一化再比对。

### T3（P1）：runSync 输出无上限缓冲

`shell.go:562-564`：`cmd.Stdout/Stderr = &bytes.Buffer{}` 无界收集，截断在
Wait 之后；后台会话有 1MB 封顶，同步路径没有。timeout 窗口内跑 `yes`/
`cat /dev/urandom` 可 OOM 网关。

**修复**：引入有界收集 writer（head+tail 环形保留 + 溢出标记），容量复用后台
会话的 `maxOutputBufferSize` 语义；TruncateTail 在已有界的内容上照常工作，
行为只可能更省内存（never-worse）。

### T4（P1）：非 PTY 后台会话 stderr 饥饿 + 孙进程持管道永久卡死

`shell.go:865-933`：读 goroutine 先读空 stdout 再读 stderr 最后 `cmd.Wait()`——
(a) 常驻进程写 stderr 的内容在 stdout EOF 前不可见；(b) 孙进程继承 stdout 写端
时永不 EOF → Wait 永不调用 → 会话永远 "running" + 僵尸 shell。runSync 已有
`cmd.WaitDelay` 机制（含专项测试），后台路径没有。

**修复**：stdout/stderr 两个并发 copier goroutine 写入同一同步缓冲；cmd 设
`WaitDelay`（与 runSync 同值来源），Wait 返回（或超时强关管道）后会话按既定
状态机收尾。

### T5（P2）：开放模式相对路径按进程 CWD 解析

`filesystem.go:1137-1194`：非 restrict 时 `hostFs{}` 直接 `os.ReadFile/os.Open`，
相对路径按网关进程 CWD（systemd 下通常是 HOME）解析；而 exec 默认 cwd、
send_file/load_image 都按 workspace，工具描述也承诺 workspace——行为不自洽，
`write_file("config.json")` 会静默改写 HOME 下同名文件。

**修复**：hostFs 持有 workspace 根，相对路径 join 到 workspace（绝对路径不受
影响；sandbox 模式路径语义不变）。

### T6（P2）：会话清理按 StartTime

`session.go:211-221`：清理条件 `IsDone() && StartTime < now-30m`——运行超 30
分钟的任务退出后最多 5 分钟就被删，最终输出丢失，与工具描述"退出后保留 30
分钟"相反。

**修复**：会话结束时记录 `finishedAt`（Wait 收尾点），清理按 `finishedAt` 判定。

### T7（P3）：edit/append 全量读无上限

`edit.go:139-186`：`ReadFile` 全量 + string 拷贝 + 替换重编码，多份内存拷贝，
对百 MB 级文件是内存尖峰。

**修复**：读前 `os.Stat` 守卫（超限报错拒绝），阈值对齐输出缓冲上限的量级。

### T8（P2）：写文件固定 0600 清掉既有权限

`fileutil.WriteFileAtomic(path, data, 0o600)` rename 覆盖不保留原 mode——对
0755 脚本做一次 edit 就失执行位。

**修复**：写入前对已存在目标 `os.Stat`，存在则沿用其 mode，仅新文件用 0600。

### T9（P2）：BM25 缓存快路径数据竞争

`search_tool.go:267-271` 无锁读 `cachedEngine/cacheVersion` vs `:292-294` 持锁
写；并行 tool call 时按 Go 内存模型是数据竞争。

**修复**：快路径读也走 `cacheMu`（读多写少，Mutex 足够，无需升级 RWMutex 的
复杂度）。

### T10（P2）：Windows 后台会话 kill 不走 Job Object

`runBackground` 未调 `trackProcessTree`（runSync 在 shell.go:571 调了）——
taskkill /T 漏杀中间父已退出的孤儿，正是 Job Object 要解决的问题。

**修复**：runBackground 同步路径对齐：Start 前调用 `trackProcessTree(cmd)`
（Windows 专属文件，与 PTY 的 Setsid 分支无交集）。

## 5. pkg/providers

### P1（P0，源码复核确认）：finish_reason 归一化使截断护栏失效

`common.go:336-348` 把非流式 `ParseResponse` 的 "length" 改写为 "truncated"，
而唯一消费者护栏是 `ts.GetLastFinishReason() == "length"`（pipeline_execute.go:161，
af8975b2 防截断参数事故）。全仓无代码消费 "truncated"；流式路径保留原始
"length"——非流式（fallback 链必经）护栏整体失效，被 max_tokens 截断的
tool_calls 只要 JSON 恰好可解析就会被执行。

**修复**：删除 `normalizeFinishReason` 的改写（与其余所有 provider 的 "length"
对齐，流式/非流式自洽）；grep 确认无测试断言 "truncated"。

### P2（P1）：Gemini 流式无响应头超时

`gemini_provider.go:162`：无 proxy 时 streamClient 落到 DefaultTransport，
`http.Client{Timeout:0}` 且无 ResponseHeaderTimeout——"连接建立但响应头不返回"
的窗口只有 context 取消能救，而 providerCtx 无 deadline。03471ee6 在
openai_compat 修过同型问题（streamRoundTripper clone + 90s 头超时），未复制到
gemini。

**修复**：复制 openai_compat 模式——clone `p.httpClient.Transport`（nil 则
DefaultTransport）设 `ResponseHeaderTimeout` 为同一 90s 常量。

### P3（P1）：anthropic SDK provider 无任何请求超时

`anthropic/provider.go:47-50` 构造 client 无 timeout option；SDK v1.55.1 默认
`http.DefaultClient`（已核实）。对照 anthropic_messages 用 `common.DefaultRequestTimeout`
并注释"非流式唯一挂死探测器"。

**修复**：API-key 路径统一注入 `option.WithHTTPClient`（common 的带超时 client，
含 proxy 支持——顺带覆盖 P5 的同文件半边）；OAuth/token 路径同法。

### P4（P2）：Retry-After 只对 429 生效

`cooldown.go:93-97`：`MarkFailureWithHint` 仅 `FailoverRateLimit` 用 hint 抬高
冷却下限；503/5xx 是 FailoverTimeout，服务器给的长 Retry-After（过载服务常态）
被丢弃，1 分钟标准退避后提前一个数量级冲击上游。

**修复**：hint 下限逻辑扩展到 FailoverTimeout（billing/auth/format 的既有排除
面不变）。

### P5（P2）：anthropic-messages / alibaba-coding-anthropic 忽略 cfg.Proxy

`factory_provider.go:314-335` 两个分支不透传 proxy（同函数 openai/gemini/azure
都透传），子包构造函数也没有 proxy 参数——必须走代理的网络里表现为莫名其妙的
连接失败。

**修复**：子包构造函数加 proxy 参数，内部 `common.HTTPClientWithProxy`（复用
openai 分支的挂载法）；工厂两分支透传 `cfg.Proxy`。

### P6（P3）：Gemini SSE 强制 `data: ` 带空格

`gemini_provider.go:540` `strings.HasPrefix(line, "data: ")`——SSE 规范允许
`data:` 无空格；自定义 api_base 的兼容网关省略空格时所有事件被静默跳过，返回
空内容 + finish "stop" 的"成功"响应。

**修复**：兼容两种前缀（openai_compat 解析器同款写法）。

### P7（P3）：流式 tool_call 组装假定 index 从 0 连续

`openai_compat/provider.go:873-877`：`activeTools` 按 index 切片 + 越界 continue
——编号不从 0 起或有空洞的兼容网关会静默丢调用。

**修复**：改 map[index] 组装，收尾按 index 排序输出（对合规网关行为不变）。

## 6. 外围包

### F1（P0，源码复核确认）：cron 执行期间旧快照覆写

`executeJobByID`（service.go:327）任务执行完在锁内用**内存旧 store** 全量
`saveStoreUnsafe`，并刷新 `storeMod`——任务执行期（数分钟 agent turn）内 CLI
写入的新任务被回滚，且 mtime 探测（reloadStoreIfChanged）因 storeMod 相等判定
"无外部变更"，回滚被固化，无任何报错。已知接受的 rename 竞争是**报错可见**型，
这里是**成功写入旧数据**型，不同问题。热重载变体：旧服务在途任务完成后的最终
保存同样回滚，随后被新服务采纳。

**修复**：新增锁内版 `reloadStoreIfChangedLocked`（statStoreModTime ≠ storeMod
→ loadStore + recomputeNextRuns），`executeJobByID` 拿锁后、定位 job 前调用——
覆写窗口从"整个任务执行期"缩到"状态更新瞬间"（毫秒级，与既有已知风险同级）；
热重载在途任务的收尾保存同享此修复。

### F2（P1）：cron 失败恒记 "ok"

gateway 唯一生产 handler（gateway.go:911-915）`return result, nil`——error 恒
nil；`ExecuteJob` 的失败全部编码在字符串（前缀 `"Error: "`），到不了
`job.State.LastStatus/LastError`。定时任务失败完全不可观测。

**修复**：handler 检测 `strings.HasPrefix(result, "Error: ")` → 返回
`errors.New(result)`（service 据此记 error 状态；result 文本本身即 LastError）。

### F3（P1，安全）：skills GitHub 安装器路径穿越

`installer.go:494`：GitHub Contents API 返回的 `item.Name` 直接
`filepath.Join(localDir, item.Name)`——git 允许文件名含 `\`（Windows 上 Join
当分隔符逃逸目录）；可配置 `base_url` 指向恶意 registry 时 `../x` 在 Linux 同样
穿越。对照 ClawHub 路径（utils/zip.go）有完整防护，此处是遗漏。

**修复**：对 API 返回名做清洗——`filepath.Base` 后拒绝含分隔符/`..`/盘符的
条目（非法即跳过并告警），对齐 zip.go 的防御标准。

### F4（P1）：`.meta.json` 损坏后 session 变砖

`jsonl.go:123-140` readMeta 解码失败直接返回错误 → `addMsg` 中止追加、
`GetHistory` 整体失败——`.jsonl` 损坏行有"记录并跳过"恢复策略（:597-604 注释
明言 standard recovery pattern），meta 却让所有操作硬失败且无自愈（手动删
meta 即恢复，证明零值回退安全）。

**修复**：readMeta 对解码失败改为记日志 + 返回零值 meta（文件缺失语义不变）；
损坏的 meta 会在下次 SetSessionTitle/保存时被正常数据覆写，自愈。

### F5（P3）：TZ 字段从未生效

`CronSchedule.TZ` 进了 schema 与 `sameSchedule` 比较，但 `computeNextRun` 的
gronx 调用无时区参数——配了 `"tz"` 的任务按服务器本地时区静默偏移。已核实
gronx v1.20.0 全程保留输入 time 的 Location 求值（next.go `loc := ref.Location()`
+ time.Date(loc)）。

**修复**：`computeNextRun` 对非空 TZ `LoadLocation`（失败回落本地并记日志），
以 `now.In(loc)` 求值、结果按 loc 重解释后取 UnixMilli；`ValidateSchedule`
对无效 TZ 直接拒绝。

### F6（P3）：HTML→Markdown 剥掉代码块内行首空格

`markdown.go:405-408` 的 `reLeadingLineSpace` 在整个转换结果上执行，包括 ```
围栏内——代码块中单前导空格的行（YAML/对齐注释）被静默去空格。

**修复**：后处理改 fence 感知（逐行扫描，```/~~~ 围栏内外分别处理，仅围栏外
应用正则）。

## 7. 修复批次与不变量

| 批次 | 内容 | 改动面 |
|------|------|--------|
| A | A1、A3、A4、A5、A6/C4（管线 publishMu） | pkg/agent |
| B | P1–P7 | pkg/providers |
| C | C1、C2、C3、C5 | pkg/channels/feishu |
| D | D1–D3 | wecom / telegram / discord |
| E | T1–T10 | pkg/tools、pkg/fileutil |
| F | F1–F6 | pkg/cron、pkg/gateway、pkg/skills、pkg/memory、pkg/utils |

**修复不得违反的既有不变量**（来源：AGENTS.md、fork-overview §同步注意事项、
turn-llm-failure-resilience §3.1、飞书卡片评审 §6）：

- 命令路径禁止无超时拿模型写锁；`/new`、`/switch` busy 语义、`*bool` 默认开、
  开放默认三件套不动。
- 流式卡片失败绝不失败 LLM 调用；不恢复"中断卡刷屏"；空卡删除、seq 单调、
  `enforceFeishuElementLimit` 安全网不动。
- 清理管线 never-worse 守门不动；落盘保持原文。
- cron 热重载（mtime 探测）语义保留；修复只收窄覆写窗口，不改探测协议。
- 心跳 keep-alive 语义（lastAt + 面板活性）保留。

## 8. 执行记录（2026-10-04，六批次全部落地）

按 §7 批次顺序实施，共修 30 项、缓修 1 项（A2）、维持既有取舍 1 项的残余形态
（A6/C4 只修致命形态）。改动面：34 个文件，+746/−164 行（含测试适配）。

**批次 A（pkg/agent）**
- A1：`turn_coord.go` CallLLM 返回后别名赋值处补 `exec.pendingMessages = nil`
  （所有权转移，消除切片自追加）。
- A3：`pipeline_llm.go` 重试循环 hardAbort 分支后新增 graceful 分支——
  `gracefulInterruptRequested() && errors.Is(err, context.Canceled)` → 记日志返回
  `ControlContinue`（providerCtx 每次 CallLLM 新建，模型获得收尾机会；watchdog
  升级路径兜底，循环有界）。
- A4：`turnState` 新增 `persistMu`；`appendToolResultMessage`/
  `appendToolNoticeMessage` 的 gate 检查+落盘、`sealAbortedTurnSession` 的
  Get/SetHistory 全部序列化在 persistMu 下（锁序单一，两种交错序皆安全）。
- A5：`mcpRuntime` 新增 `doMu`，`reset()` 与 `initOnce.Do` 串行其上（锁序恒为
  doMu→mu，Do 体内 setManager 取 mu 不反向）。
- A6/C4：`streamingChunkPublisher.Update/UpdateReasoning` 内持 `publishMu`——
  答案写的 seq 读取与 API 发送不再与心跳面板刷新（AppendToolStep 同锁）交错，
  消掉 CardKit 序列回退 → 可见失败的致命形态。

**批次 B（pkg/providers）**
- P1：删除 `normalizeFinishReason`，非流式 ParseResponse 原样透传 "length"
  （与其余 provider 及流式路径一致，截断护栏恢复生效）。
- P2：Gemini 新增 `streamRoundTripper`（clone Transport + 90s
  ResponseHeaderTimeout，与 openai_compat 同模式）。
- P3：anthropic SDK 非流式分支注入 `option.WithRequestTimeout(common.DefaultRequestTimeout)`。
- P4：`MarkFailureWithHint` 的 hint 下限扩展到 `FailoverTimeout`（503+Retry-After
  不再提前一个数量级冲击上游）。
- P5：`anthropicmessages.NewProviderWithTimeout` 加 proxy 参数（内部改用
  `common.NewHTTPClient`），工厂两分支透传 `cfg.Proxy`。
- P6：Gemini SSE 解析兼容 `data:`（无空格）形式。
- P7：流式 tool_call 组装改按 map 键排序遍历（不再假定 index 从 0 连续）。

**批次 C（pkg/channels/feishu）**
- C1：`streams` map 键改 `streamMapKey(chatID, sessionKey)`（scoped 复合键），
  会话间不再互相覆盖条目——中间卡可被各自 owner 寻址与封板；新增
  `sealStaleStreamersForChat` 清扫（只封其它会话 ≥TTL 的陈旧卡）；
  `NotifySteeringInChat` 签名扩展 sessionKey（唯一实现方+manager+测试同步），
  找不到 scoped 条目时回落到本 chat 最近活跃卡；`chatHasActiveStreamer` 改
  Range 扫描；Finalize/Cancel 的 CompareAndDelete 用同样的键。
- C2：最小封卡成功后以 12KB 硬预算重算 remainder（`hardRest`）再补发，
  [12KB,24KB) 区间不再丢失。
- C3：`feishuStreamReuseTTL` 2min→5min（心跳 180s 保证存活 turn 的静默间隙
  < 3min，活着被判 stale 的窗口关闭；残余风险：用户关闭心跳时仍可能误判）。
- C5：Finalize/Cancel 封卡前清空 `state.ProgressNote`；
  `hasPanelContent()` 不再计入 ProgressNote（恢复空卡删除语义）。
- 测试适配：两处按旧 TTL 构造陈旧卡的用例改为 `feishuStreamReuseTTL` 相对值；
  `NotifySteeringInChat` 调用点签名同步。

**批次 D（跨通道媒体）**
- D1（wecom）：媒体送达后立即 `sent++`，caption 失败以含本 part 的计数上报
  （自动降级永久错误，不再整批重试）。
- D2（telegram）：`sendCaptionText` 第 k 个 chunk 失败时
  `MediaSendErr(len(messageIDs), err)`；媒体组失败分支把 leadingIDs 计入
  sent 并返回部分 messageIDs。
- D3（discord）：文件 reader 的关闭职责移交发送 goroutine（defer），select
  两分支的关闭循环删除（超时分支关闭=与在途 Read 竞争）。

**批次 E（pkg/tools）**
- T1：PTY slave `tty` 在 Start 成功后由父进程关闭（并修 Start 失败路径的
  泄漏）；wait goroutine 在 `cmd.Wait()` 返回后关闭 `ptyMaster`（fd+读
  goroutine 不再永久泄漏）。
- T2：`IsProtectedSystemPath` 入口先 `stripExtendedPathPrefix`（剥
  `\\?\`/`\\?\UNC\`/`\\.\` 前缀，与 exec guardCommand 对齐）；探针实测
  `\\?\C:\Windows\System32\config` 现被拦截。
- T3：新增 `cappedOutputBuffer`（head+tail 环形 + 截断标记，4MB/流），
  runSync 的 stdout/stderr 收集有界化（落盘原文语义改为"有界保留+重跑建议"，
  OOM 风险消除）。
- T4：非 PTY 后台重写——stdout/stderr 并发 copier（stderr 不再饥饿）；改用
  `os.Process.Wait()` 收割（避免 `cmd.Wait` 在进程退出瞬间关闭读端与排水
  竞争）；收割后排空给 `execIOWaitDelay` 宽限，孙进程持管道时关读端兜底，
  会话不再永久 "running"。活体探针实测：done 状态 + 双流输出齐备。
- T5：`hostFs` 持 workspace，相对路径 join 到 workspace（与 exec cwd、
  send_file、工具描述对齐；绝对路径与盘符相对路径不受影响）。
- T6：`ProcessSession.finishedAt` 记录收割时刻，`cleanupOldSessions` 按
  finishedAt 判定（长任务退出后保留窗口兑现承诺）。
- T7：edit/append 读后解码前加 64MB 守卫（避免 4x 内存拷贝尖峰）。
- T8：hostFs/sandboxFs 写文件保留既有目标 mode（新文件仍 0600）。
- T9：BM25 快路径读改持 `cacheMu`（并行 tool call 数据竞争消除）。
- T10：runBackground 补 `trackProcessTree`；session 新增 `terminator`
  （Job Object 优先，kill 不再漏孤儿孙进程）；wait goroutine 收尾
  `releaseProcessTree` 防句柄泄漏。

**批次 F（外围）**
- F1：`executeJobByID` 拿锁后先 `reloadStoreIfChangedLocked()` 再定位/改状态/
  保存——覆写窗口从整个任务执行期缩到状态更新瞬间；热重载在途任务的收尾
  保存同享修复。
- F2：gateway 的 cron handler 对 `"Error: "` 前缀结果返回 error，LastStatus/
  LastError 不再恒为 ok/空。
- F3：`sanitizeRemoteFileName` 校验 GitHub API 返回名（拒分隔符/`..`/盘符/
  UNC），非法条目跳过并告警。
- F4：`readMeta` 解码失败降级为零值 meta + 告警（会话不再变砖，下次
  writeMeta 自愈）。
- F5：`computeNextRun` 对非空 TZ 按目标时区求值（gronx 保留 Location 已
  核实），`ValidateSchedule` 校验 TZ 合法性。
- F6：行首单空格剥离改 fence 感知逐行处理（```/~~~ 围栏内不动），单元探针
  验证三态行为。

**验证**
- `go build -tags goolm ./...` 全量通过；`go vet`（改动包）全绿；gofmt 差集
  无新增（本机 CRLF 检出所致的存量格式告警与本改动无关）。
- 全量 `go test ./pkg/...`：失败集与 §C.6 白名单完全一致（isolation×4、
  migrate×3、asr×1、pid×1、deltachat×1、tools 7×TestShellTool_*；另
  matrix/gateway 无 goolm 标签时 libolm 构建失败属环境），**零新增失败**。
- 分批回归：pkg/agent（89s 全绿）、pkg/providers 全绿、pkg/channels 及
  feishu 全绿、pkg/cron、pkg/memory、pkg/utils、pkg/skills、pkg/tools/fs 全绿。
- 临时探针（steering 重复注入、`\\?\` 拦截、后台会话生命周期、fence 行为）
  均验证后删除，工作树仅剩本评审文档与代码改动。

**缓修项交接（A2）**：system 消息绕过会话占用锁——建议后续单独评审
①"已占用会话上 system 消息重投队列"的止血方案，或 ②异步回执改走 steering
通道注入当前 turn 的结构方案；两者都涉及异步结果投递语义与 cron
ProcessDirect 的交互，不宜顺手改。

## 9. 活体验证（2026-10-04，本机安装版 D:\apps\PicoClaw）

构建 `v0.4.0-106-gb3dab79e-dirty` 装入本机并启动后，对可在本机验证的 6 项
修复做了端到端测试（其余项依赖外部渠道/网络黑洞/竞态时序，由单测覆盖）：

| 项 | 测试方法 | 结果 |
|----|----------|------|
| F5 | jobs.json 外部编辑给 cron 任务加 `tz=America/New_York`（表达式 `51 4 * * *`），网关热重载 | ✅ 任务在北京 16:51:00 触发（=纽约 04:51）；TZ 无效时按旧行为应永远不会在当天触发 |
| F2 | 同批任务之一 payload.script=`exit 1`（wake gate 失败路径） | ✅ jobs.json 落盘 `lastStatus:"error"` + `lastError:"Error: pre-run script failed…"`；ok 路径任务记录 `"ok"` |
| F1 | 90s 阻塞脚本制造执行窗口（`wakeAgent:false` 免模型调用），窗口内 CLI 插入金丝雀任务，等待任务完成保存 | ✅ 金丝雀在完成保存后存活（旧构建会被旧快照覆写静默抹掉），任务自身状态亦正确落盘 |
| T2 | agent 对话驱动 `read_file("\\?\C:\Windows\win.ini")`（网关日志核实真实前缀传入） | ✅ `access denied: path is inside a protected system directory` |
| T5 | agent 对话驱动 `list_dir(".")`（明确相对路径） | ✅ 返回 workspace 内容（.agents/AGENT.md/42end.txt…），非进程 CWD |
| T4 | agent 对话驱动 exec：background 运行双流命令 → poll → read | ✅ 会话到达 done，read 同时拿到 `stdout-line` 与 `stderr-line` |

测试任务/会话已清理（6 个 bugfix-* cron 任务全数移除）。另外：本批改动已被
本机 picoclaw agent 自查后提交为 `58a2244e`（fix: whole-repo bug sweep）。
