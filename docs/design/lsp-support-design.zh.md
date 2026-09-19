# LSP 支持：目标化诊断与源码修复（设计文档）

> 状态：设计稿，待评审后实施。
> 调研基线：`@narumitw/pi-lsp` v0.49.7（pi 编码代理的 LSP 扩展，源码逐文件分析）+ opencode `packages/opencode/src/lsp/`（3311 行，编辑器级实现）+ picoclaw 现有架构（2026-09-19 工作树）。
> 修订：v2——吸收 opencode 的持久会话/配置合并/诊断注入三项设计。
> 维护日期：2026-09-19

## 一、背景与目标

### 1.1 问题

picoclaw 目前的代码反馈回路只有 `exec`（跑 lint/typecheck/build）。对"编辑后快速验证几个文件"的场景：

- 全项目检查慢（Go 项目 `go vet ./...` 尚可，TS/Python 全量 tsc/pyright 常以十秒计）；
- CLI 输出没有精确 range/severity，模型解析靠文本猜；
- 部分修复（import 整理、`source.fixAll`）CLI 没有等价物。

### 1.2 目标

为 picoclaw 增加两个模型可用工具，走 LSP（stdio 传输）：

1. **`lsp_diagnostics`**：按扩展名路由到配置的语言服务器，对指定文件/目录取精确诊断（`path:line:col: severity source code: message`）；
2. **`lsp_fix`**：请求服务器支持的 source action（默认 `source.fixAll`，可选 `source.organizeImports` 等），预览或写回修复后的文本。

定位与 pi-lsp 一致：**中间反馈通道，不是权威验证**——AGENTS.md 里项目的权威命令（build/test）仍是最终裁决。

### 1.3 非目标

- ❌ 常驻语言服务器（不做编辑器式增量会话，见 §3.2 取舍）
- ❌ 格式化（`lsp_format` 已被 pi-lsp 自己移除；项目格式化交给权威命令）
- ❌ 符号导航/引用/重命名/补全
- ❌ 诊断自动注入（模型必须主动调用工具）
- ❌ LSP over TCP/SSE（仅 stdio）
- ❌ 动态能力注册（spawn-per-call 客户端只声明静态能力）

## 二、参考分析：pi-lsp 架构拆解

pi-lsp 是 pi 的第三方扩展（TypeScript，约 2200 行），核心决策逐条核对如下。

### 2.1 配置与路由（routes.ts / adapters.ts）

- **配置三层层级**：受信项目 `<workspace>/.pi/pi-lsp.json` → 用户 `~/.pi/agent/pi-lsp.json` → 内置目录（约 28 个服务器）。**自定义配置整体替换目录，不合并**——语义简单可预测。
- **按扩展名路由**：每个 server 声明 `command[]`（argv 数组）+ `extensions[]`；同一扩展可配多个服务器（互补诊断）。
- **per-server 旋钮**：`env`（环境变量覆盖）、`initialization`（initializationOptions + workspace/didChangeConfiguration settings）、`skipDirectories`（叠加公共跳过表 node_modules/.git/target/...）、三个诊断等待参数 `pushDiagnosticsGraceMs` / `pullDiagnosticsGraceMs` / `diagnosticsSettleMs`（对付不发空集/分析慢的服务器，如 rust-analyzer 拉取空结果需再等 5s、LuaLS 干净文档不发推送）。
- **目录命令缺失的容错**：未显式指定 server 时，内置目录里命令不在 PATH 的服务器**静默跳过**并记原因；显式指定或自定义配置的命令缺失则报错。
- **默认限额**：每次调用最多打开 50 个文件（`DEFAULT_FILE_LIMIT`）。

### 2.2 生命周期（runner.ts / session-lifecycle.ts）

- **spawn-per-call**：`withLspClient` 每次工具调用 起进程 → initialize → 操作 → shutdown（优雅退出 500ms 宽限）→ 兜底 kill。用完即关，无跨调用状态。
- **取消传播**：调用方 AbortSignal 贯穿每个阶段；session 关闭/重载时取消并等待该会话拥有的全部 LSP 调用。
- **状态展示**：服务器运行期间在 UI 上挂一条活动状态，结束清除（失败也不 bypass 资源清理）。

### 2.3 客户端（lsp-client.ts）

- **JSON-RPC 2.0 over stdio**：`Content-Length` 分帧手工解析；每个请求挂独立超时；stderr 尾部拼接进错误信息（排障关键）。
- **initialize 能力声明全部静态**（`dynamicRegistration: false`）：spawn-per-call 无法跟踪动态注册。声明 `codeAction.resolveSupport: ["edit"]`、诊断 pull 能力、`workspaceEdit.documentChanges`。
- **诊断双通道**：服务器声明 `diagnosticProvider` → 走 `textDocument/diagnostic` 拉取（空结果且配了 pullGrace 时再等推送）；否则等 `publishDiagnostics` 推送（800ms 静默期判定 settle，可按服务器加 pushGrace 假设"还没发=干净"）。
- **URI 归一化**：按规范化后的路径做 key（Windows 大小写折叠、`%3A` 等百分号编码差异），容忍服务器回 URI 与发送 URI 编码不一致。
- **Windows 细节**：`.bat`/`.cmd` 命令包装成 `cmd.exe /d /s /c <cmd> <args>` 启动；命令解析在**工作区目录上下文**里查 PATH（支持 node_modules/.bin 本地服务器）。

### 2.4 修复链路（runner.runFix / text-edits.ts）

didOpen → 拉诊断 → `textDocument/codeAction`（全文档 range + `only: [kind]`）→ 需要时 `codeAction/resolve` 补 edit → 按 kind 前缀过滤 → 收集 WorkspaceEdit 中**仅属于目标文档**的 TextEdit → **重叠检测**（纯插入互不冲突；替换区间严格相交报错，提示换更窄的 kind）→ 从后往前应用 → `write` 为 true 才落盘，否则全文返回给模型预览。

### 2.5 输出契约

- 诊断：`<server> LSP diagnostics: N diagnostic(s) across M file(s).` + 逐条 `path:line:col: severity source code: message`（1-based 行列）。
- 修复：变更状态一句话；未写盘时附**完整新文本**（无输出体积上限——pi-lsp 明确列为限制，要求保持请求聚焦）。

### 2.6 pi-lsp 自认的局限（设计输入）

诊断不持续注入；服务器每调用启停；不做导航；"干净的 LSP 结果不替代仓库权威检查"；"无基准证明 LSP 提升代理任务成功率"（引用 Codex #8745 讨论——**仓库原生检查可能已覆盖大部分价值**）。这些判断我们照单吸收：保持工具面窄、文档明确"中间反馈"定位。

### 2.7 参考分析：opencode 的编辑器式模型（v2 增补）

opencode（SST）的 LSP 子系统（`packages/opencode/src/lsp/`，6 文件 3311 行）走了与 pi-lsp 相反的路线，三个设计值得吸收：

**① 持久懒起会话 + 熔断**：客户端按 `(root, serverID)` 池化，首次触碰匹配文件才 spawn，实例生命周期内复用；`spawning` map 去重并发启动；`(root,server)` 启动失败进 `broken` 集合**永久熔断**（不再重试，避免每次工具调用都撞死进程）。摊销了冷启动成本——这正是 spawn-per-call 在"连续编辑多次"场景的最大短板。

**② 诊断自动注入 edit 结果**（`tool/edit.ts`）：编辑成功后 `touchFile(file, "document")` 等一版诊断，把 **error 级（severity=1）、单文件 ≤20 条**的 `<diagnostics file="...">` 块追加到编辑输出（"LSP errors detected in this file, please fix:"）。**模型不需要记得调用诊断工具**——编辑即反馈闭环。这是 opencode 相对 pi-lsp 最大的架构优势（pi-lsp 把"模型不主动调用"列为已知局限）。

**③ per-server root 解析**：每个服务器声明自己的根解析函数（`NearestRoot(["go.mod"])` 从文件向上搜到工作区边界），monorepo/多模块项目里 gopls/typescript-server 各自拿到正确的 workspace root；且**配置 merge 语义**（`lsp: true` 用全部内置；按名覆盖 `command/extensions/env/initialization`，`disabled: true` 关单个）而非 pi-lsp 的整体替换。

另有：服务器目录 38 项 + auto-install（`go install gopls`/npm/dotnet tool/gem，装进全局 bin，`disableLspDownload` 门控）；编辑器级客户端（动态注册跟踪、按 identifier 并行拉取、版本感知推送等待——注释标记"LATENCY-CRITICAL"并引用 PR #23771）；导航工具面（definition/references/hover/symbols/callHierarchy 独立 `lsp` 工具）。

**不采纳**：导航工具面（IDE 场景，模型用 grep/知识图谱替代即可，工具面通胀）；动态注册/identifier 拉取（编辑器级复杂度，gopls/tsserver 静态声明够用）；38 项目录（个人部署）；auto-install 列 P2 且默认关。

## 三、picoclaw 设计

### 3.1 总体结构

```
pkg/lsp/                     # 新包：LSP 协议与客户端（不含业务）
├── client.go                # JSON-RPC stdio 客户端：分帧、pending、超时、stderr 捕获
├── protocol.go              # 最小消息类型集（手写，不引第三方 LSP 库）
├── position.go              # LSP 位置(UTF-16 code unit) ↔ Go offset(UTF-8 byte) 换算
├── edits.go                 # WorkspaceEdit 收集/重叠检测/应用
├── spawn_windows.go         # .bat/.cmd 包装 + 整树击杀（复用既有 Job Object 资产）
├── spawn_other.go
└── client_test.go / position_test.go / edits_test.go / fakeserver_test.go

pkg/tools/lsp.go             # 工具层：LspDiagnosticsTool / LspFixTool（注册/路由/输出格式）
pkg/tools/lsp_routes.go      # 配置加载（内置目录+自定义替换）、按扩展路由、文件收集
pkg/tools/lsp_test.go        # 工具级测试（fake server + 临时文件）
```

### 3.2 关键取舍

| 决策 | 选择 | 理由（含 pi-lsp 佐证） |
|---|---|---|
| 服务器生命周期 | **idle-TTL 持久会话**（v2 修订：spawn-per-call → 池化 + 空闲回收） | 诊断注入 edit 后每次编辑都要诊断反馈，spawn-per-call 会让连续编辑每次付 1-3s 冷启动；opencode 验证了池化模型。TTL 空闲回收（默认 5 分钟）+ broken 熔断 + 并发 spawn 去重，比 opencode 的实例级常驻温和（网关常驻多 agent，不能只增不减） |
| 协议库 | **手写最小集**（~300 行） | fork 惯例少依赖（x/text 之外零新外部依赖）；只用 initialize/didOpen/didClose/diagnostic/codeAction/resolve/shutdown/exit 八个方法 |
| 配置载体 | **config.json `tools.lsp` 内嵌，merge 语义**（v2 修订：整体替换 → 合并） | picoclaw 单文件配置是既有习惯；opencode 的 merge + `disabled` 模式比 pi-lsp 的"自定义=整体替换"友好——为关一个服务器不必抄全目录 |
| 目录规模 | **内置精简 6 项** | gopls、typescript-language-server、pyright、ruff、rust-analyzer、clangd。fork 是个人部署，全量 28 项目录徒增误配面；自定义=整体替换（沿用 pi-lsp 语义） |
| 位置编码 | **显式 UTF-16↔UTF-8 换算** | LSP 规范位置是 UTF-16 code unit；Go string 是 UTF-8 byte。JS 天然对齐、Go 必须换算（中文 BMP 1:3、emoji 代理对 2:4）。这是与 pi-lsp 实现差异最大的正确性点 |
| 文件内容 | **原样字节进出，不走过 text_compat** | 服务器按 didOpen 发送的原文计算位置；任何解码/换行归一化都会让 range 错位。读写都用原始字节（含 CRLF/BOM 原样） |
| 进程击杀 | **Windows 复用 Job Object 整树击杀** | `npx`/`.cmd` 类命令派生 cmd→node 进程树，单进程 Kill 留孤儿占管道。`pkg/tools/shell_process_windows.go` 的 track/terminate/releaseProcessTree 已验证，提取共用 |
| 写回 | **fileutil.WriteFileAtomic** | 与 edit_file/write_file 同一原子写路径（含瞬态重试）；系统目录保护复用 `validatePathWithAllowPaths`（读用 allow-read、fix 写用 allow-write） |

### 3.3 配置设计

```jsonc
"tools": {
  "lsp": {
    // ToolConfig 风格总开关（enabled 省略=开启，与 read_file 等一致）
    "enabled": true,
    "timeout_seconds": 20,        // 单个 LSP 请求超时（对齐 pi-lsp 默认 20s）
    "max_files": 50,              // 单次诊断最多打开文件数
    "inject_on_edit": true,        // 编辑成功后自动注入 error 级诊断（省略=开）
    "idle_ttl_seconds": 300,       // 服务器会话空闲回收
    "servers": {                   // 与内置目录按名合并（v2），disabled 可关单个内置
      "gopls": {
        "command": ["gopls"],              // argv 数组（可选，覆盖内置）
        "extensions": [".go"],             // 可选
        "disabled": false,                 // true = 关闭该服务器（含内置项）
        "env": {},                         // 可选，环境变量覆盖
        "initialization": {},              // 可选，initializationOptions
        "skip_dirs": [],                   // 可选，叠加公共跳过表
        "push_diagnostics_grace_ms": 0,    // 可选，推送通道"没发=干净"宽限
        "pull_diagnostics_grace_ms": 0     // 可选，拉取空结果后再等推送的宽限
      }
    }
  }
}
```

结构体落点：`ToolsConfig.Lsp LspToolsConfig`；`LspToolsConfig.EffectiveServers()` 返回"自定义或内置目录"，内置目录硬编码在 `lsp_routes.go`（含 gopls `pull_diagnostics_grace_ms: 5000`——gopls 首次拉取可能先返回空，与 rust-analyzer 同款问题）。

公共跳过目录表照搬 pi-lsp：`.git node_modules vendor dist build target out coverage __pycache__ .venv venv` 等。

### 3.4 诊断注入编辑结果（v2 新增，opencode 模式）

`edit_file` / `write_file` / `append_file` 成功后，若 `tools.lsp.enabled` 且 `inject_on_edit`（省略=开）：

1. 对被改文件做 `touchFile(path, "document")`（持久会话内增量同步，version 递增）；
2. 等待该文件一版诊断（文档级，超时 2s 上限，超时静默放弃——编辑结果不因诊断慢而拖延）；
3. **仅 error 级**（severity=1）且单文件 ≤20 条时，在编辑输出尾部追加：

```
LSP errors detected in this file, please fix:
<diagnostics file="pkg/agent/agent.go">
ERROR [47:12] undefined: Foo
</diagnostics>
```

无 error 不追加任何内容（零 token 浪费）；fix 写回后同样注入，形成"编辑→诊断→修复"闭环。注入路径与 lsp_diagnostics **共享同一会话池**，编辑反馈延迟≈增量诊断时间（gopls 亚秒级），冷启动只在首个编辑付一次。

### 3.5 工具 Schema

**`lsp_diagnostics`**（读类工具）：

```
path?     : string|string[]   // 文件或目录；缺省=工作区根
server?   : string|string[]   // 指定服务器名；缺省=按扩展名路由全部匹配者
limit?    : int               // 本次最多打开文件数（缺省用配置 max_files）
```

输出（ForLLM 走既有截断管线，这点修正 pi-lsp 的"无上限"局限）：

```
gopls LSP diagnostics: 3 diagnostic(s) across 2 file(s).

pkg/agent/agent.go:47:12: error go compiler: undefined: Foo
pkg/tools/lsp.go:10:1: warning gopls: ...
pkg/tools/lsp.go: no diagnostics
```

行为细节：内置目录命令缺失的服务器静默跳过（附注一行）；显式指定的服务器命令缺失=报错（含安装提示）；无匹配文件报错并提示扩展名路由规则。

**`lsp_fix`**（写类工具，write=true 时走写路径校验）：

```
path     : string   // 目标文件
kind?    : string   // source action kind，缺省 source.fixAll
write?   : bool     // 缺省 false=只返回修复后全文；true=原子写回
server?  : string   // 多服务器匹配同一扩展时必须指定
```

输出：变更一句话；未写盘时附完整新文本；无变更时明确 "left unchanged"。

### 3.6 生命周期与并发（v2 修订：TTL 会话池）

```
lspSessionPool（每个 AgentLoop 一个，镜像 compactScheduler 模式）:
  key = root + "\x00" + serverID
  acquire(adapter, root, ctx):
    池中有 → 刷新 lastUsed，返回
    启动中 → 等待启动结果（spawning map 去重并发启动）
    broken 集合命中 → 返回不可用（该 root+server 进程内熔断）
    否则 → spawn + initialize + 入池
  后台回收协程: 每 30s 扫描，空闲 > idle_ttl（默认 5min）→ shutdown（优雅 500ms → 整树击杀兜底）
  AgentLoop.Close: drain 全部会话（在拆 MCP/engine 之前，与 compact drain 同区）
```

- **启动**：Windows `.bat/.cmd` → `cmd.exe /d /s /c` 包装；`trackProcessTree` 挂 Job Object（npx 类命令派生 cmd→node 树，必须整树杀）。
- **取消**：工具 `ctx` 取消只中断当前请求（会话保留复用）；会话级 shutdown 才杀进程。诊断注入 edit 的等待自带 2s 上限，与 turn ctx 解耦。
- **增量同步**：会话内文件用 didChange（version 递增）而非每次 didOpen——gopls 增量同步快；不支持增量的服务器回退全量重发。
- **诊断双通道**：push/pull 选择逻辑对齐 pi-lsp（§2.3），推送 800ms 静默 settle + per-server 宽限；不实现 opencode 的 identifier 级拉取（编辑器级复杂度，静态声明够用）。
- **熔断**：spawn 或 initialize 失败 → `broken` 记录（进程内永久），后续调用跳过该服务器并附注原因——与 opencode 一致，防每次编辑都撞死进程。

### 3.7 正确性要点（实现时的红线）

1. **UTF-16 换算**：`position.go` 提供 `utf16OffsetToByte(text string, line, character int) int`——按行定位后逐 rune 推进，rune > 0xFFFF 计 2 个 UTF-16 单位。单测必须含中文（BMP）与 emoji（astral）用例。
2. **URI**：Windows 路径 → `file:///C:/dir/a%20b.go`（正斜杠、空格百分号编码）；诊断结果按规范化路径 key 归并（大小写折叠 + 解码后比较），容忍服务器回异编码 URI。
3. **原文进出**：didOpen 发送文件原始字节（UTF-8 字符串化即可，不解码不归一）；修复写回同样原样（BOM/CRLF 保留——服务器编辑基于原文，写回原文编码不会引入混合换行）。
4. **重叠检测**：TextEdit 转成 byte offset 后做区间冲突判定（纯插入点互不冲突，pi-lsp 同款语义）；冲突报错引导换更窄 kind。
5. **管道**：stdout 持续读取 goroutine（分帧状态机）+ stdin 独立写；stderr 独立捕获，拼进超时/崩溃错误信息。

### 3.8 与既有设施的接入

| 设施 | 接入方式 |
|---|---|
| 工具注册 | `instance.go` 按 `IsToolEnabled("lsp_diagnostics")` 注册（与 read_file 同款模式）；默认关闭（新能力不惊扰存量配置） |
| 路径安全 | 读走 `validatePathWithAllowPaths(…, allowRead)`；fix 的 write 走 `validateWritePath`（Windows 保留名等校验自然生效）+ 系统目录保护 |
| 写回 | `fileutil.WriteFileAtomic`（瞬态重试/进程内串行化自动受益） |
| 输出 | 诊断结果走 `truncate.go` 头尾截断管线（修正 pi-lsp 无上限的局限） |
| Windows 进程 | 提取 `shell_process_windows.go` 的 Job Object 辅助为共享（`pkg/tools/sysproc_windows.go` 扩展或上移 `pkg/fileutil`），LSP 与 exec 共用 |
| /lsp 命令 | P1：列出配置服务器 + PATH 可用性（仿 `/status`），方便排查"为什么没诊断" |

### 3.9 文档

- `docs/guides/configuration.zh.md` 新增"语言服务器 (lsp)"章节（配置示例 + 内置目录 + "中间反馈定位"的使用指引，含权威命令优先的告诫——照搬 pi-lsp 的务实表述）；
- fork-overview 新功能块行 + 同步注意事项。

## 四、测试计划（TDD）

| 层 | 锚点 | 覆盖 |
|---|---|---|
| position | `TestUtf16PositionRoundTrip` | 中文 BMP、emoji 代理对、行尾/越界钳制 |
| edits | `TestApplyTextEdits_*` / `TestOverlappingTextEdits_*` | 从后往前应用、纯插入不冲突、替换相交报错 |
| 协议 | `TestJsonRpcFraming` | 粘包、半包、Content-Length 大小写、非法头 |
| fake server | `fakeserver_test.go` 内嵌 mini LSP server（stdin/stdout JSON-RPC，可编程返回诊断/codeAction） | 全链路：initialize→didOpen→diagnostic→shutdown |
| 工具 | `TestLspDiagnosticsTool_*` | 路由/命令缺失跳过 vs 报错/限额/输出格式/截断 |
| 工具 | `TestLspFixTool_*` | kind 过滤、预览 vs 写回（原子写）、重叠报错、CRLF 文件位置正确性（回归锚点） |
| Windows | `TestSpawnCommandWrapsBat`（tag windows） | .bat/.cmd 包装、整树击杀（复用 shell 测试手法） |
| 会话池 | `TestLspSessionPool_*` | 复用（同 key 不重复 spawn）、并发 acquire 去重、idle TTL 回收、broken 熔断、Close drain |
| 注入 | `TestEditInjectsErrorDiagnostics` | error≤20 注入格式、无 error 零追加、诊断超时不拖编辑、inject_on_edit=false 关闭 |

## 五、分期实施

- **P0（可用）**：`pkg/lsp` 客户端核心（**含 TTL 会话池**）+ `lsp_diagnostics` 工具 + 内置目录（gopls 优先）+ fake server 测试 + 配置/文档。预计 ~1400 行（含测试）。
- **P1（完整）**：`lsp_fix`（codeAction/resolve/edits/原子写）+ **edit 诊断注入** + per-server root 解析（NearestRoot）+ `/lsp` 命令 + Job Object 提取共用。
- **P2（可选）**：auto_install（仅 gopls `go install`，默认关）、workspace 级配置覆盖、目录扩充、didChange 增量同步优化（全量重发兜底先行）。

## 六、风险与开放问题

1. **中台网关模型是否善用 LSP 输出**：诊断格式设计为逐行 `path:line:col: severity: message`（模型最易消费）；P0 落地后用真实会话观察调用率，若模型不主动调用，考虑在 edit_file 成功结果尾部追加一行提示（"用 lsp_diagnostics 快速验证"）——P1 决策点。
2. **spawn-per-call 冷启动**：gopls 每次起进程 + 首次分析 ~1-3s（大仓库更久）。对"查 3 个文件"场景仍远快于全量 build；不可接受时 P2 缓存。
3. **Windows 管理员路径**：语言服务器装在 PATH（本机 gopls 已有）；`.cmd` shim（npm 全局装的服务器）依赖 cmd.exe 包装 + 整树击杀，P0 必测。
4. **诊断时序差异**：不同服务器推送/拉取行为差异大，三个宽限参数已覆盖 pi-lsp 已知案例；新服务器踩坑按 per-server 参数解决，不动机制。

---

评审要点（建议确认）：① 内置目录精简为 6 项是否够用；② 默认关闭是否合意（还是默认开、靠 PATH 缺失静默跳过 + broken 熔断兜底）；③ **edit 诊断注入默认开**（opencode 模式的核心价值）还是先默认关观察；④ P0/P1 切分。
