# 代码审查报告：2026-10-31 模块梳理批次一~四（v2 双评审合并版）

> **v2 说明**：本文档初版为单评审产出；v2 由第二轮独立评审对全部断言逐条核实后合并而成。
> 两轮评审交叉补漏：初版漏报 C-2（spawn_status 跨会话泄露）与 M-2（任务表无界增长）；二轮漏报 C-1（死锁，初版独有发现）。
> 对初版三处断言做了修正（M-3 机理、m-2 机理、m-7 清单），修正处均以「**v2 修正**」标注；初版一处对 whatsapp backoff 的误判已撤回（见 m-1）。
> 核实方式：六环节死锁链逐环节读码、worktree 检出父提交做测试失败集基线对照、LF 归一化后 gofmt 实测、fork 红线逐项比对 AGENTS.md。

## 审查概览

- 审查目标：`dddpeter/picoclaw` 2026-10-31 的 6 个提交（`28cfd0ce` 批次一 → `6282c29d` 批次四收尾），均已进入本地 main，未 push
- 审查范围：当日全量 diff。规模实测：**全范围 132 文件 +2217/−4338**（批次一 +477/−4155；批次二~四代码部分 59 文件 +1771/−250）。初版报告"+1513/−242"统计口径有误，v2 修正
- 技术栈：Go 1.26（网关/工具/渠道）+ React/TypeScript（web 前后端）
- 总体评分：6.5 / 10
- 合并建议：⚠️ 修改后可合并。**C-1 必修**（运行中网关确认微信/企微绑定必现死锁）；C-2/M-1/M-2 修复成本低，强烈建议同批处理；M-3 需要一轮前端返工

## 风险摘要

1. **C-1（初版发现，v2 六环节复核确认）**：批次三 config 写锁收敛（B5）引入同 goroutine 非重入死锁——运行中的网关上确认微信/企微扫码绑定会永久挂起 HTTP handler，并因锁被死锁 goroutine 永久持有而堵死全部 config 写路径。
2. **C-2（v2 新增，初版漏报）**：批次四 spawn_status 接线时 `RegisterTask` 新签名不再接收来源会话标识且两处调用均不填充，会话过滤条件恒为 false——跨会话任务信息泄露，与工具描述承诺相反。
3. 三处资源泄漏/无界增长：deltachat 监护失败路径泄漏进程与 goroutine（M-1）、SubagentManager 任务表只进不出（M-2）。
4. 移动端会话侧栏改造只做了一半：响应式隐藏类缺失 + 共享 observer 失效机理叠加（M-3）。

---

## 关键问题（Critical / Major）

### [C-1] saveWeixinBinding/saveWecomBinding 自死锁（B5 回归） — 🔴 Critical

- 位置：`web/backend/api/weixin.go:211-247`、`web/backend/api/wecom.go:214-257`、`web/backend/api/gateway.go:1415-1419`（RestartGateway → startGatewayLocked）、`web/backend/api/gateway.go:1139`（startGatewayLocked → EnsurePicoChannel）、`web/backend/api/pico.go:252-254`（EnsurePicoChannel 加锁点）
- 问题：完整死锁链（v2 已逐环节独立复核，无 escape path）：
  1. `saveWeixinBinding` 入口 `configWriteMu.Lock()` + `defer Unlock()`，**持锁全程**；
  2. 网关状态为 running 时同步调用 `h.RestartGateway()`；
  3. `gatewayStartReady()`（gateway.go:420）预检只查配置前置（默认模型/凭据），对运行中的网关**必然通过**；
  4. `RestartGateway` 在 `gateway.mu.Lock()` 区间内调 `startGatewayLocked("restarting", 0)`；
  5. `existingPid=0` → 不走 attach 分支提前返回 → 必达 `h.EnsurePicoChannel()`；
  6. `EnsurePicoChannel` 入口无条件 `configWriteMu.Lock()` → **同一 goroutine 对同一把 `sync.Mutex` 二次加锁 → 永久阻塞**。
- 触发入口：`weixin.go:174`（绑定轮询 confirmed 分支）、`wecom.go:182`。运行中网关 + 扫码确认绑定 = 必现。
- 测试盲区：`weixin_test.go:39` 调用 `saveWeixinBinding` 时网关未运行（`gatewayStatusData() != "running"` 在 RestartGateway 前提前返回），故测试全绿未拦截。
- 后果：HTTP handler 永久挂起；`configWriteMu` 永久被占，所有 config 写路径（models.go 五处、tools/pico/wecom/weixin/oauth 七处、config.go 四处）全部堵死。若已部署 Linux 网关，此路径**必现**。
- 回归来源：加锁前两个 save 函数与 `EnsurePicoChannel` 均无锁（同提交 c1642973 引入），非预存问题。
- 修复建议：锁内只做 load-modify-save，重启移到锁外。以 weixin 为例：

```go
func (h *Handler) saveWeixinBinding(token, accountID string) error {
	configWriteMu.Lock()
	// ... load-modify-save 原样 ...
	shouldRestart := false
	status := h.gatewayStatusData()
	if gs, _ := status["gateway_status"].(string); gs == "running" {
		shouldRestart = true
	}
	configWriteMu.Unlock() // 显式释放，移除 defer

	if shouldRestart {
		if _, err := h.RestartGateway(); err != nil {
			logger.ErrorCF("weixin", "failed to restart gateway after saving binding",
				map[string]any{"error": err.Error()})
		}
	}
	return nil
}
```

  更稳妥的替代：拆出 `saveWeixinBindingLocked`（含锁版本），公共入口加锁调用它后解锁再重启；wecom 同样处理。注意不要反过来给 `startGatewayLocked` 里的 `EnsurePicoChannel` 去 lock——该函数还有 `main.go:652`、`handlePicoSetup` 等无锁调用方（v2 已核实全部调用点）。
- 回归测试建议：补一个「网关 running 状态下 saveWeixinBinding 不持锁调 RestartGateway」的用例；更通用的是给 `configWriteMu` 相关函数加获取顺序断言（见架构评估）。

### [C-2] spawn_status 会话隔离失效，跨会话任务信息泄露（v2 新增，初版漏报） — 🔴 Critical（修复成本低）

- 位置：`pkg/tools/subagent.go:72`（`RegisterTask` 新签名）、`pkg/tools/spawn.go:134` 与 `pkg/tools/subagent.go:215`（两处调用）、`pkg/tools/spawn_status.go:80,105`（过滤点）
- 问题：批次四接 C1 断链时，`RegisterTask(task, label, agentID)` 砍掉了旧 `Spawn` 的 `originChannel/originChatID` 参数，两处调用都不填；`SubagentTask.OriginChannel/OriginChatID` 从此恒为空串。spawn_status 的会话过滤写的是 `if callerChannel != "" && taskCopy.OriginChannel != "" && ...`——右值恒空 → **过滤永不生效**：任何会话的 `spawn_status` 都能列出并读取其它会话 spawn 的任务文本与完整结果。
- 加重因素：工具 Description 仍向模型承诺 "Results are scoped to the current conversation's channel and chat ID... preventing cross-conversation task leakage in multi-user deployments"——承诺与实现相反，模型会放心地把 A 会话的任务内容读给 B 会话用户。
- 影响面：多用户/多渠道部署（feishu 群、telegram 多 chat）下跨会话信息泄露；单用户部署无实际影响。
- 修复建议（任选其一，推荐 ①）：
  1. `RegisterTask` 增加 origin 参数，spawn/subagent Execute 内用既有的 `ToolChannel(ctx)`/`ToolChatID(ctx)` 取值填入——过滤管线是现成的，只是没人喂数据；
  2. 若判定单用户部署不需要隔离：删除两处死过滤 + 改写工具描述，诚实化。
- 回归测试建议：伪造带不同 `ToolChannel` ctx 的两次 spawn，断言互相不可见。

### [M-1] deltachat 监护失败路径泄漏进程与 goroutine — 🟠 Major

- 位置：`pkg/channels/deltachat/rpc.go` `supervise()`（waitReadyInternal 失败分支、onRestart 失败分支的 `continue`）
- 问题：这两个失败点位于「新 server 已 spawn 且已 rewired（`c.cmd/stdin/stdout` 已替换、新 readLoop 已启动）」之后。`continue` 后下一轮重新 spawn，但旧进程无人 `Kill`、旧 stdin 未 `Close`——旧 `deltachat-rpc-server` 进程存活泄漏，其对应 readLoop goroutine 永久阻塞在旧 stdout 上。全函数 grep `Kill|Close` 零命中。次生风险：泄漏的 readLoop 在旧进程日后死亡 EOF 时会再次进入 `supervise()`（此时 `restarting` 已被 rewire 路径置回 false），形成双 supervisor 竞争。反复失败（server 启动坏、账户损坏）会按退避周期持续累积进程。
- 修复建议：两个失败分支 `continue` 前清理当前 rewired 的实例：

```go
if err := c.waitReadyInternal(); err != nil {
	logger.ErrorCF("deltachat", "rpc restart: server never became ready",
		map[string]any{"error": err.Error()})
	c.killCurrent() // 新增：mu 下取 c.cmd.Process.Kill() + c.stdin.Close()
	c.failAll(err)
	continue
}
```

  `killCurrent` 内对 `Process.Kill` 的错误仅记日志（进程可能已退出）。同时建议给 readLoop 引入生成代（generation）标记，旧代 readLoop 检测到代次不符时直接退出，根治双 supervisor 竞争；`restarting` 标志应保持到 supervise 完整退出（rewire 时提前清零是竞争窗口的根源）。

### [M-2] SubagentManager.tasks 无界增长（v2 新增，初版漏报） — 🟠 Major

- 位置：`pkg/tools/subagent.go:77`（`RegisterTask` 唯一写点）
- 问题：spawn（异步）与 subagent（同步）每次执行都注册任务，完成后仅置 `status` 与 `Result`（**完整 LLM 输出文本**）。全仓 grep 确认：无 `delete`、无 TTL、无条数上限、无淘汰——任务表只进不出，长驻网关进程内存无界增长。批次四接线前 `spawn_status` 恒为空因此无此问题，属本次新引入。
- 修复建议：`CompleteTask` 完成写入后按条数淘汰：

```go
const maxFinishedTasks = 50

func (sm *SubagentManager) CompleteTask(taskID, status, result string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if t, ok := sm.tasks[taskID]; ok {
		t.Status = status
		t.Result = result
	}
	if len(sm.tasks) > maxFinishedTasks {
		// 按 Created 升序淘汰最旧的已完成任务
		type idAt struct { id string; at int64 }
		done := make([]idAt, 0, len(sm.tasks))
		for id, t := range sm.tasks {
			if t.Status != "running" {
				done = append(done, idAt{id, t.Created})
			}
		}
		sort.Slice(done, func(i, j int) bool { return done[i].at < done[j].at })
		for i := 0; i < len(done)-maxFinishedTasks && i < len(done); i++ {
			delete(sm.tasks, done[i].id)
		}
	}
}
```

### [M-3] 移动端会话侧栏改造只做一半：响应式缺失 + 无限滚动不可靠 — 🟠 Major

- 位置：`web/frontend/src/components/chat/chat-page.tsx:339,371,386`（`sessionSidebar` 双实例渲染）、`web/frontend/src/hooks/use-session-history.ts:86-110`（observer effect）、`web/frontend/src/components/chat/session-history-sidebar.tsx:249`（sentinel `ref={observerRef}`）
- 问题（两个缺陷叠加，v2 合并两轮评审发现并修正机理）：
  1. **响应式隐藏缺失**：批次四提交信息声称"桌面内联侧栏 hidden md:flex 保留"，但 chat-page.tsx 与 session-history-sidebar.tsx 中**均无该类**（v2 全文 grep 证实）。移动端 320px 内联侧栏照常渲染（近乎占满屏宽）+ Sheet 抽屉按钮并存，聊天区被挤压。初版报告即指出此点。
  2. **共享 observerRef 跨双实例失效**：同一 `sessionSidebar` JSX 渲染两份，共享一个 `observerRef`。observer effect 的 deps（`hasMore/isLoadingMore/loadError/loadSessions`）不含 ref，只在首次绑定了当时的 `ref.current`；抽屉（Radix Sheet，无 forceMount）打开时新 sentinel 挂载会覆写 `ref.current` 但 effect 不重跑——`observer.observe()` 绑定的仍是旧元素。净效果：**抽屉内列表的无限滚动在打开后不可靠**（只有当 hasMore 等依赖恰好变化触发 effect 重跑时才可能重绑到抽屉 sentinel）。初版断言"移动端全程失效"依赖"display:none 前提"，该前提不成立（见第 1 点：根本没有隐藏类），v2 修正为上述准确机理——但一旦按提交本意补上 `hidden md:flex`，display:none 场景即成为现实，此缺陷立即显性化，两个问题必须一起修。
  3. 附带：抽屉打开期间双实例各挂一个轮询 interval（5s 忙时/30s 闲时）+ 各自首次 `onRefresh()`，重复请求；内联实例隐藏后该问题自动消失。
- 修复建议（二选一）：
  - 方案 A（推荐）：observer 下沉到 `SessionHistorySidebar` 内部，每个实例自建 `ref + effect`（`useInViewport` 小 hook），ChatPage 不再传 `observerRef`；同时给内联实例包 `hidden md:flex`（或组件内响应式类）；
  - 方案 B：单实例 + 响应式切换渲染——内联侧栏仅 `md:` 以上渲染，Sheet 打开时才渲染抽屉侧栏（接受打开时重建列表的开销）。

---

## 一般问题（Minor）

### [m-1] matrix sync 退避不随成功重置 — 🟡 Minor

- 位置：`pkg/channels/matrix/matrix.go`（sync 重启循环）
- 问题：`backoff` 只增不减（goroutine 级捕获），sync 成功运行数天后遇单次瞬断，也要按累计退避等待（最长 5min）才重启。
- **v2 撤回初版关联断言**：初版报告曾把 whatsapp 列为"同款问题"——**错误**。whatsapp 的 `backoff` 是 `reconnect()` 函数内局部变量，每次重连事件从 2s 重新退避（按事件重置，语义合理）。初版引用的两轮独立评审在此点上一错一对，以本条为准：仅 matrix 受影响。
- 修复建议：记录本轮 sync 连续运行时长，超过某阈值（如 10min）后下一轮失败重置 `backoff = matrixSyncBackoffMin`。

### [m-2] glob 绝对路径被静默重定位（v2 修正机理） — 🟡 Minor

- 位置：`pkg/tools/fs/glob_tool.go`（`searchRoot := strings.Trim(searchRoot, "/")`）
- 问题：初版机理描述有误，v2 修正——并非"display 与 searchRoot 锚定错位"（display 链自 walkRoot，锚定自洽）。真实机理：`strings.Trim(searchRoot, "/")` 会把 POSIX 绝对路径的**前导 /**
  一并剥掉（`Trim("/tmp", "/") == "tmp"`），搜索目录被静默重定位到 workspace 下的同名相对目录（`/tmp` → `<workspace>/tmp`）。目录不存在时返回 "No files match"，存在时返回错误目录的结果——无安全影响（fs 抽象守卫仍作用于重定位后的路径），但 Linux 部署机上绝对路径输入不可用；Windows 盘符路径（`D:/x`）不受影响。
- 修复建议：改用 `strings.TrimPrefix(searchRoot, "/")`（只剥尾部分隔符用 `strings.TrimRight`），或在进入 walk 前用与 read 工具相同的路径解析逻辑归一化。

### [m-3] grep 无单文件大小上限 — 🟡 Minor

- 位置：`pkg/tools/fs/grep_tool.go` `searchFile`
- 问题：`t.fs.ReadFile(path)` 全量读入，二进制探测只看前 8KB，GB 级日志文件会整体进内存；`grepMaxFiles=4000` 只限文件数不限总量。
- 修复建议：读入前 `Stat` 跳过超过上限（如 1MB）的文件并在结果尾部注明 skipped 计数。

### [m-4] 按 byte 截断切断 UTF-8 — 🟡 Minor

- 位置：`pkg/tools/fs/grep_tool.go`（`display[:300]`）、`pkg/commands/cmd_drafts.go`（`body[:maxBody]`）
- 问题：byte 切片截断可能把多字节字符切一半，输出经 JSON 编码后显示为乱码替换符。
- 修复建议：统一 rune 截断：`string([]rune(display)[:150])`。

### [m-5] /drafts 跨 workspace 泄露与状态机不一致（条件性） — 🟡 Minor

- 位置：`pkg/evolution/runtime.go`（`CandidateDraftsForReview` / `DraftByID` / `DecideDraft`）
- 问题：三个方法均不校验 `draft.WorkspaceID`。**触发条件**：仅当 `evolution.state_dir` 非空时 drafts 存储为多 workspace 共享单文件（`paths.go:17`：override 为空时路径在 `<workspace>/state/evolution/` 下，per-workspace 无泄漏；v2 补充此条件性说明）。共享场景下 A workspace 的草稿会出现在 B 的 `/drafts list`，且可被 B accept（内容经 `applyDraftWithRollback` 应用进 B 的 skill 库）。注意本 fork 部署若启用共享记忆目录则正中此路径。
- 状态机不一致（无条件触发）：已 `accepted` 的草稿可被 reject——skill 已在库中但状态变 `rejected`；accept 路径可复活 `quarantined/rejected` 草稿（有 scan+rollback 安全网，可解释为 retry，但应显式提示当前状态）。
- 修复建议：三方法加 `draft.WorkspaceID == workspace` 过滤（list/preview/decide 一致）；reject 已 accepted 的草稿返回明确错误；accept 非 candidate/accepted 状态时在回复中提示当前状态（保留 retry 能力）。

### [m-6] spawn_status 注册时序窗口 — 🟡 Minor

- 位置：`pkg/tools/spawn.go:132`（`RegisterTask` 在 goroutine 内调用）
- 问题：spawn 工具返回后立即调用 `spawn_status` 可能查不到任务（goroutine 尚未执行到 RegisterTask），且任务 ID 未返回给调用方。同步 subagent 路径无此问题。
- 修复建议：`RegisterTask` 挪到 `go func()` 之前执行，并在 Execute 的返回文本中带上 `taskID`（"spawned as subagent-N"）。

### [m-7] 会话重命名 API 对幽灵 ID 返回 ok（v2 新增） — 🟡 Minor

- 位置：`web/backend/api/session.go`（`handleRenameSession`）、`pkg/memory/jsonl.go`（`SetSessionTitle`）
- 问题：`readMeta` 对不存在的 session 返回空 meta 无错误，`SetSessionTitle` 直接写入——`PUT /api/sessions/<不存在ID>/title` 返回 `ok:true` 并为幽灵 ID 创建孤儿 meta 文件。侧栏只列真实会话，实际触发面小，但 API 语义应诚实。
- 修复建议：handler 先验证 session JSONL 存在，或 `SetSessionTitle` 对零值 meta（无 CreatedAt）返回 not-found。

### [m-8] 当日触碰的 go 文件存在 gofmt 违规（v2 修正清单） — 🟡 Minor

- v2 实测（CRLF 归一化 LF 后 gofmt）：违规文件为 `pkg/channels/telegram/telegram.go`（import 排序：`unicode/utf8` 插在 `context` 前）、`pkg/config/config.go`（struct tag 对齐，可能含预存漂移）、`pkg/tools/subagent.go`、`pkg/evolution/types.go`（const 块结尾 `\t\t)` 应为 `)`，初版发现、初版清单漏列）。**初版清单中的 `pkg/agent/evolution_bridge.go` 实际干净，系误报，v2 移除。**
- 修复建议：`gofmt -w $(git show 28cfd0ce..HEAD --name-only --pretty=format: -- '*.go' | sort -u)` 一把修复，作为独立 `style:` 提交。

## 优化建议（Nit）

- `grep_tool.go` walk 闭包内 `if err != nil || ...` 检查的外层 `err` 此时恒为 nil（死条件，易误导后来者），建议删除或换成显式的 ctx 取消标志。
- `grep_tool.go` 单文件搜索路径：先 `ReadFile` 探测再 `searchFile` 再读一遍（双倍 IO/内存）；正则重复编译（`reSingle` 与已编译的 `re` 相同）；ctx 取消时静默返回部分结果（glob 会报错，行为不一致）。
- `draftsRejectHandler` 用 `context.Background()` 而非传入的 `ctx`，与 accept 不一致。
- `chat-page.tsx` 内 `const sessionSidebar = (...)` 顶格缩进（组件体内），建议与作用域对齐；`agent_command.go` 的 `evolution` import 未按 stdlib/外部分组惯例放置。
- glob/grep 硬编码跳过 `node_modules`/`.git`，不可配置；深度上限 24 对深目录树可能截断，建议至少在截断消息中注明（grep 的 4000 文件 cap 触发时同样无 truncation 提示，match cap 有）。
- 前端会话重命名失败仅 `console.error`，无用户可见 toast。
- `toleratedLegacyNoopKeys`（`pkg/config/diagnostics.go`）叶名匹配过宽：任意结构下恰好叫 `write_timeout`/`mention_only` 的拼写错误键都会被静默容忍，削弱未知键诊断。已有注释说明，属知情取舍，建议在诊断输出中对被容忍键计数提示。
- C2（custom_allow_patterns）选择了文档化而非修复：测试钉死语义 + 文档标注是合理决策，但用户配置仍静默无效——建议在 config diagnostics 对非空 `custom_allow_patterns` 输出 warning（基建现成）。

## 架构评估

- 模块边界：批次一的死代码清理整体干净——被删符号全仓零残留（`go build`（除 matrix libolm 预存环境问题）与测试全绿佐证）；AGENTS.md 钉死的 fork 行为逐项核验未被动到（见下方红线清单）。
- 依赖方向：glob/grep 复用 `fileSystem` 抽象（hostFs/sandboxFs/whitelistFs）而非绕过，hostFs 每层 `ReadDir`/`ReadFile` 均过系统目录守卫，沙箱语义保持一致，方向正确。
- 主要风险：**锁纪律**。config 写锁收敛本身是对的（`h.configMu` 已全删，v2 已 grep 核验全部 `SaveConfig` 调用点均在 `configWriteMu` 下），但 C-1 暴露了「同锁非重入 + 跨锁调用链」缺系统性审视的问题——同批次内 `EnsurePicoChannel`（加锁）与 `startGatewayLocked`（调用它）分属两文件，初版评审亦只核验了 EnsurePicoChannel 的一级调用方、未追 RestartGateway 的传递调用链（v2 已修正方法：对加锁函数做 inbound 传递闭包分析）。建议在 `configWriteMu` 的注释处登记「持锁期间禁止调用的函数清单」（RestartGateway/startGatewayLocked/EnsurePicoChannel 自身），或考虑以「Locked 后缀约定 + 入口断言」固化。

### fork 红线核验清单（v2 逐项比对 AGENTS.md，全部完好）

- `/new` 重读磁盘配置、`/switch`//`/new` busy 语义、命令路径 TryLock 降级 —— 未触碰
- ChatStream 独立流式 Transport（`streamRoundTripper`）—— 未触碰
- exec 输出清理管线：删除的 `pkg/tools/truncate.go` 仅含零调用 `TruncateHead`；落盘掩码路径 `persistFullOutput(MaskAPIKeys(...))`（`shell.go:839`）完好
- 开放默认三件套（`restrict_to_workspace` 默认 false、`protect_system_paths` nil=开、`defaultDenyPatterns`）—— 未触碰
- `loop_detection.enabled *bool`（nil=开）—— 未触碰（`turn_state.go` −19 行为 LLMPhase 死枚举删除，已核实零引用）
- cron 热重载（jobs.json mtime 探测）—— 未触碰

## 已验证为正确的部分（评审确认项）

- **updater 供应链修复（B4）**：非空且非 github.com 的 release URL 显式报错，不再静默回落上游 latest。
- **EvolutionConfig.MarshalJSON round-trip（B2）**：`cold/archive/delete_after_days` 字段名双向核对一致；v2 补充确认 `0` 值语义为「取默认」（`runtime.go:1155`），`omitempty` 丢 0 无损。
- **error_classifier（B1）**：504 入 transient 集；quota→Billing 提升分支的判定顺序（rateLimit+billing 双命中优先）与 HTTPError 分支语义对齐。
- **telegram rune 节流（B8）**：`lastLen` 两处同步改为 rune 计数，CJK 提前节流修复正确（import 顺序见 m-8）。
- **MQTT `tls_skip_verify`（B7）**：`*bool` nil=维持存量 skip，向后兼容，符合 fork 的 `*bool` 惯例。
- **anthropic_messages 错误通道（B3）**：全部非 200 走 `common.HandleErrorResponse`，Retry-After/配额提升/256B body 上限恢复。
- **/ready channels 检查**：类型断言与 `Manager.GetStatus()` 返回形状（`map[string]any{"running": bool}`）完全匹配（v2 复核）。
- **会话重命名 API**：`io.LimitReader` 8KB + rune 截断 + user 源永不覆盖（同源可覆盖，`titlePriority` 机制核实）；幽灵 ID 问题见 m-7。
- **spawn_status 接线主体（C1）**：async 语义核实——`spawnSubTurn` 阻塞至子 turn 结束，`CompleteTask` 时机正确；问题仅在来源标识缺失（C-2）与注册时序（m-6）。
- **SaveDrafts**：带文件锁的按 key upsert 合并，单条传入不会误删其他草稿（v2 核实 reject 路径安全性）。
- **i18n 死键清理**：抽查 zh.json 仅删键与补 `sessionHistory`，`common.restartRequiredTitle` 保留且仍被引用，无误删。
- **doctor MCP 探测（C9）**：`slices.Sorted(maps.Keys(...))` 现代写法，10s 超时，禁用服务器报 skipped。
- **测试基线**：v2 以 worktree 检出 `28cfd0ce^` 做对照，`go test ./pkg/...` 失败集与父提交**同名同量**（pkg/tools 10 个用例逐一比对一致）——当日改动零新增测试失败。预存失败均为环境性（Windows shell、libolm cgo、home 路径类）。

## 验证记录

- `go test ./pkg/commands/ ./pkg/evolution/ ./pkg/tools/fs/ ./pkg/config/ ./pkg/providers/` — 全绿（两轮独立验证一致）。
- `go test ./pkg/...` 全量 + worktree 父提交基线对照 — 失败集完全一致，零回归。
- `go build ./...` — 本机因 mautrix libolm cgo（缺 `olm/olm.h`，预存 Windows 环境问题，与今日改动无关）无法编译 matrix 包；其余包通过。
- 静态扫描（技能 analyze.sh，pkg/tools/fs + web/backend/api）：仅测试文件假阳性与预存超长文件，无新增安全问题。
- C-1 死锁：v2 六环节代码链独立复核确认（含触发入口 `weixin.go:174`/`wecom.go:182` 与测试盲区定位）；初版另做过同构最小程序实验（`DEADLOCK CONFIRMED`）。
- C-2 隔离失效：v2 读码核实 `OriginChannel` 全仓无写入点。
- M-1/M-2/M-3：两轮交叉复核；M-3 影响机理以 v2 修正版为准。
- m-7 gofmt：LF 归一化副本实测（规避全仓 CRLF 噪音）。

## 建议动手顺序

1. **C-1**（先出修复提交，重启逻辑移出锁外 + 补运行中重启的回归测试）；
2. **C-2 / M-1 / M-2**（各约十行级别，可并入同一提交；C-2 顺带修 m-6 注册时序）；
3. **M-3**（前端单实例化或 observer 下沉 + 补 `hidden md:flex`，单独一轮）；
4. m-8 gofmt 一把过 + m-1~m-7 按顺手顺序清；
5. Nit 与架构建议（锁纪律清单登记）随后跟进。

---

## 实施记录（2026-10-31 修复轮）

> 全部按上述顺序落地，每个修复提交均先以 RED 测试复现问题再修复（TDD）。
> 验证基线：全量 `go test ./pkg/...` 失败集与修复前完全一致（预存 shell/路径环境失败 + 全量并发下的已知 flaky，单独复跑均 PASS）；前端 `tsc -b && vite build` 通过；提交均在本机 main，未 push。

| 项 | 状态 | 提交 | 要点 |
|---|---|---|---|
| C-1 死锁 | ✅ 已修 | `ebc34637` | 拆 `save*BindingLocked`（持锁版）+ `restartGatewayAfterBinding`（锁外重启）；回归测试用真实 sleeper 子进程伪造 running 网关 + `TryLock` canary 断言重启时锁已释放，watchdog 把死锁挂起转为显式失败（修复前 RED 复现：`still held = true` + 挂起） |
| C-2 会话隔离 | ✅ 已修 | `63351d21` | `RegisterTask` 增加 origin 参数（ctx 的 ToolChannel/ToolChatID），两处调用补填；跨会话泄露 RED 测试实测复现后转绿 |
| m-6 注册时序 | ✅ 已修 | 同上 | 注册移到 goroutine 外，ack 回带 `task_id` |
| M-2 任务表无界 | ✅ 已修 | 同上 | `CompleteTask` 按 Created 淘汰最旧已完成任务，封顶 50 条，running 永不淘汰 |
| M-1 deltachat 泄漏 | ✅ 已修 | `a5c0ad0e` 前的 `fix(channels)` 提交 | `killCurrent()`（Kill+Close+Wait 收尸）+ generation 代次标记（stale readLoop 静默退出）+ restarting 全程持有；三个回归测试钉死收尸/失败代回收/双监护防护 |
| M-3 移动端侧栏 | ✅ 已修 | `7d6f208b` | observer 下沉 `SessionHistorySidebar`（每实例自建 sentinel+effect），chat-page 移除 observerRef；内联实例包 `hidden h-full md:flex` |
| m-1 matrix 退避 | ✅ 已修 | 小修批次 | 稳定运行超 `matrixSyncStableResetAfter`(10min) 重置 backoff；Linux 部署机验证（本机 libolm 无法编译 matrix） |
| m-2 glob 绝对路径 | ✅ 已修 | 小修批次 | 重构 `normalizeSearchRoot` 纯函数，`TrimRight` 替代双侧 `Trim`；表驱动测试覆盖 POSIX/Windows/相对形态 |
| m-3 grep 大小上限 | ✅ 已修 | 小修批次 | walker 用 `DirEntry.Info()` 跳过 >8MiB，尾部注明 skipped；`grepMaxFileSize` 转 var 供测试注入 |
| m-4 rune 截断 | ✅ 已修 | 小修批次 | grep 行截断与 drafts body 截断改 rune 边界；测试断言 300 CJK rune 完整保留无替换符 |
| m-5 drafts workspace | ✅ 已修 | 小修批次 | 三方法加 `WorkspaceID == workspace` 过滤（对齐冷路径既有语义）；已 accepted 拒绝 reject；共享 state_dir 场景回归测试 |
| m-7 rename 幽灵 ID | ✅ 已修 | 小修批次 | 导出 `JSONLStore.SessionExists`，handler 先验存在（404）；HTTP 级测试断言不产生孤儿 meta |
| m-8 gofmt | ✅ 已修 | `ed94b0d9` | LF 归一化逐文件检测 58 个当日触碰文件，仅真违规 7 文件执行 `gofmt -w`；agent_command import 归组 |
| Nits | ✅ 已修 | 随各批次 | grep walk 死条件移除/ctx 取消显式报错/单文件双读与重复编译消除/draftsRejectHandler 用传入 ctx/sessionSidebar 缩进/glob 深度截断提示（`fix(tools)` 提交） |

**未实施（纯建议级，不影响正确性）**：① `custom_allow_patterns` 非空时 config diagnostics 警告；② `toleratedLegacyNoopKeys` 被容忍键的计数提示；③ `configWriteMu` 持锁禁调清单登记（C-1 修复已在两个 save 入口与 `restartGatewayAfterBinding` 注释中登记锁纪律，系统性清单可在下次触碰时补）。

**回归测试新增清单**：`binding_lock_test.go`（2）、`spawn_scoping_test.go`（3）、`rpc_leak_test.go`（3）、`glob_root_test.go`（1）、`grep_caps_test.go`（2）、`draft_review_test.go`（1）、`session_rename_test.go`（2）——共 14 个回归测试钉住本轮全部修复。
