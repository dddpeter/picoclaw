# Web 端任意文件上传与生成文件预览 —— 技术设计

> 状态：设计定稿（待实施）。调研基准：本仓库 2026-11-16 工作区状态；参考实现为本地 `/docs/myworks/Octop`（TencentCloud/Octop，FastAPI + React18/Vite/AntD）。
>
> 结论先行：两个功能可行，且 picoclaw 底层管线已就位约七成——缺的是"任意文件的 HTTP 上下车通道"（上传/读取端点）和"右侧预览面板"。模型侧消费路径（path tag 机制）无需改动即可承接上传文件；用户侧回放需一条轻量附件链路（D9）。预估总规模：后端 Go 约 760~1060 行（含测试），前端约 1100~1500 行（含预览与文件管理两期）。

---

## 1. 背景与目标

### 1.1 问题

picoclaw web 聊天（pico 通道）当前在文件能力上有两个缺口：

1. **用户只能传图片**：`web/frontend/src/features/chat/image-input.ts` 限定 5 种图片 MIME、单文件 7MB，前端读成 data URL 塞进 WS `payload.media`；任意类型文件（文档/压缩包/数据集）无法进入对话。
2. **生成文件无法在线预览**：agent 产出文件后，用户侧只有"下载链接"（`send_file` 附件 `/pico/media/{id}`）或纯文本工具反馈，没有"点击文件 → 右侧面板看内容"的体验。

### 1.2 目标（参考 Octop 的对应体验）

- **F1 任意文件上传**：web composer 支持选择/拖拽/粘贴任意类型文件，上传后随消息进入对话，agent 能按需读取内容（不强制全量进上下文）。
- **F2 生成文件预览**：对话中 agent 生成的文件（write_file 产物、send_file 附件、exec 全量输出等）可点击，在聊天区右侧面板内预览；MVP 覆盖文本/代码/Markdown/图片，富格式（pdf/docx/xlsx）后续分期。

### 1.3 非目标

- 不做在线编辑保存（Octop 的 `PUT /workspace/file` 对应物），P2 再评估。
- 不改 IM 渠道（feishu 等）的文件收发语义；本设计仅作用于 pico（web）通道。
- 不解决历史会话中已存在的 data URL 图片 token 膨胀问题（记录于 §10 开放问题）。

---

## 2. 现状与差距（调研结论）

### 2.1 picoclaw 现有链路（关键代码事实）

**整体架构（双层）**：

```text
浏览器 ── cookie 会话 + IP 白名单 ──> launcher web 后端（web/backend，Go）
                                        │  /pico/ws        ──WS 代理──┐
                                        │  /pico/media/{id} ──HTTP 代理（注入 Bearer token）──┐
                                        ▼                                │
                                   picoclaw gateway（pkg/channels/pico，挂 /pico/ 前缀）◄──┘
```

- launcher 中间件栈：`web/backend/main.go:641-682`——`LauncherDashboardAuth`（cookie）+ `IPAllowlist` + Recoverer/Logger/ReferrerPolicy。所有 `/pico/*` 代理路由自动受保护。
- gateway 侧 pico channel 是 `http.Handler`，`WebhookPath() == "/pico/"`，由 channels manager mux 挂载（`pkg/channels/manager.go:1251`）。`ServeHTTP`（`pkg/channels/pico/pico.go:398`）目前只分发 `/ws` 和 `/media/`。
- 认证：`authenticate()`（`pico.go:1540`）支持 Bearer token / WS 子协议 `token.<value>` / 查询参数（需 `AllowTokenQuery`）。launcher 的 media 代理（`web/backend/api/pico.go:175` `handlePicoMediaProxy` + `createPicoHTTPProxy`）注入 Bearer token——新 HTTP 端点可照抄该模式。

**上传（现状）**：

- 前端：`controller.ts:393` 发送 `{type:"message.send", payload:{content, media:[dataURL...]}}`；`image-input.ts` 限定图片、7MB。
- gateway：`handleMessageSend`（`pico.go:1666`）→ `parseInlineImageMedia` 校验 data URL → `HandleInboundContext(chatID, content, media, ...)`。
- data URL 不落盘，随 `InboundMessage.Media` 直通 `providers.Message.Media`；`SerializeMessages`（`pkg/providers/common/common.go:107-148`）只识别 `data:image/` 与 data audio，其余字符串静默忽略。
- 会话 JSONL 行上限 `maxSessionJSONLLineSize = 10MB`（`web/backend/api/session.go`）——**大文件走 WS data URL 会直接撞墙，这是不扩展 WS 内联方案的决定性理由**。

**agent 发文件（现状）**：

- `send_file` 工具（`pkg/tools/fs/send_file.go`）：任意本地文件（受 `validatePathWithAllowPaths` 与 `max_media_size` 约束）→ mediaStore 注册 ref（`CleanupPolicyForgetOnly`，借用不拷贝）→ `MediaResult`。
- agent 管线：`buildProviderAttachments`（`pkg/agent/agent_media.go:304`）把 ref 转 `providers.Attachment{Ref, Filename, ContentType, Type}`，挂到 assistant 摘要消息持久化（`pkg/agent/pipeline_execute_loop.go:441,815`）。
- pico `SendMedia`（`pico.go:1161`）：`ResolveWithMeta` → broadcast 附件 `{type, url:/pico/media/{id}, filename, content_type}`——**无源路径字段**。
- `/pico/media/{id}` 下载（`handleMediaDownload`，`pico.go:1303`）：token 认证、`http.ServeContent` 支持 Range、图片 inline 其余 attachment、SVG 禁止 inline。

**mediaStore（现状与局限）**：

- `FileMediaStore`（`pkg/media/store.go`）：ref 表**纯内存**，gateway 重启后旧 `/pico/media/{id}` 全部 404（文件本体不删）。预览功能因此**必须走路径而非 ref**（路径在 workspace 内稳定存在）。
- 临时目录：`media.TempDir()` = `os.TempDir()/picoclaw_media`。

**路径安全（可复用的既有资产）**：

- `validatePathWithAllowPaths(path, workspace, restrict, patterns)`（`pkg/tools/fs/filesystem.go:42`）：Abs 解析 → `IsProtectedSystemPath` 一票否决（fork 默认开，`tools.protect_system_paths` nil=开）→ `restrict=true` 时 workspace 边界 + 双重 symlink 防逃逸 + allowPaths 正则放行。
- `IsProtectedSystemPath`（`pkg/tools/fs/system_paths.go:191`）：OS 系统目录判定。
- 工作区根：`cfg.WorkspacePath()`（`pkg/config/config.go:2317`，`agents.defaults.workspace` 展开 `~`）。
- 大小上限配置体系：`agents.defaults.max_media_size`（默认 20MB，`config.go:760`）。

**前端（现状）**：

- 附件渲染：`pi-message.tsx` `AttachmentView`——图片 `<img>`，其余 `<a target="_blank">` 下载 chip。无预览面板。
- 工具调用：`turns.ts` 聚合出 `toolCall` block（`name` + `argumentsText` + feedback），`write_file` 的 `path` 参数可解析——**文件路径检测的数据源已到位**。
- 布局：`chat-page.tsx:386-410` 左侧 session sidebar + 主列（`flex min-w-0 flex-1 flex-col`），右侧无面板，可挂新列。
- 依赖基础：highlight.js、react-markdown 已在 `package.json`；Monaco 未引入。
- i18n：`web/frontend/src/i18n/locales/`（bn-in/cs/en/pt-br/zh 五语言）。

**其他相关事实**：

- exec 工具截断输出时落盘全量文件并附 `Full output: <绝对路径>` 指针（`pkg/tools/shell.go:836-842`，文件在 workspace 下 `shell-output-*.log`）。
- `buildPathTag`（`agent_media.go:338`）产出 `[image:/path]` `[audio:/path]` `[video:/path]` `[file:/path]`；`injectPathTags` 支持 generic placeholder 替换或追加。
- 会话回放：`session.go` 把 `providers.Message.Attachments` 转 `sessionChatAttachment{Type,URL,Filename,ContentType}` 返回前端。

### 2.2 Octop 参考实现要点（可移植的架构模式）

| 模式 | Octop 位置 | picoclaw 对应物 |
| --- | --- | --- |
| multipart 上传 → workspace `inbound/` → 返回 path | `POST /api/agents/{aid}/upload` → `write_inbound` | F1 的 `/pico/upload` |
| 按路径读文件（流式 + 宿主路径白名单） | `GET /workspace/file` / `download`，`is_allowed_host_download_abs_path` | F2 的 `/pico/file` |
| 从工具参数/附件/文本收集文件路径 | `useChatFileDetection.ts`（PATH_KEYS + 正则兜底） | 前端文件检测（§5.4） |
| 消息内文件卡片（预览+下载） | `MessageFileCard.tsx` | 附件 chip 改造（§5.5） |
| 右侧 dock 面板（tab/拖宽/三模式） | `useChatDockPanel` + `ChatDockPanels` | 右侧单面板（§5.5，多 tab 后置） |
| 富格式渲染矩阵 | `DocumentPreviewCore.tsx`（pdfjs/docx-preview/SheetJS） | §5.6 渲染器矩阵 |
| 上传大小限制服务端下发 | `/settings/upload` | 复用 `max_media_size` 配置体系 |

**不能直接抄**：技术栈完全不同（FastAPI+AntD+less vs Go+radix+Tailwind4），UI 层按 picoclaw 现有组件体系重写，只移植信息架构与交互模式。

### 2.3 差距清单

| # | 缺口 | 归属 |
| --- | --- | --- |
| G1 | 任意文件上传端点（multipart → workspace 落盘） | gateway + launcher |
| G2 | WS `message.send` 协议带文件引用 | gateway + 前端 |
| G3 | 按路径读文件的 HTTP 端点（含安全白名单） | gateway + launcher |
| G4 | 附件/工具调用携带源路径（协议链贯通） | gateway + agent 持久化 + launcher 回放 + 前端 |
| G5 | 前端文件检测 + 文件卡片预览入口 | 前端 |
| G6 | 右侧预览面板 + 渲染器 | 前端 |
| G7 | 用户消息附件回放（刷新后文件 chip 不丢） | bus + agent + launcher 回放 |
| G8 | 目录列表端点（文件管理工作区视图） | gateway + launcher |
| G9 | 上传文件索引（区分“上传的”与“生成的”） | gateway |

---

## 3. 总体设计

### 3.1 数据流（目标态）

**上传流（F1）**：

```text
composer 选择/拖拽/粘贴文件
  ├─ 图片（≤7MB）：本地 FileReader → data URL（现状通道不动）
  └─ 其他文件：POST /pico/upload（multipart）
        → launcher 代理（cookie + Bearer token）
        → gateway：大小校验（max_media_size）→ 文件名清洗 → 落盘 <workspace>/inbound/<yyyymmdd>/<uuid>_<name>
        → 响应 {path, filename, content_type, size}
发送消息：payload.files = [{path, filename, content_type}]（图片同时保留 data URL media）
  → gateway handleMessageSend：content 追加 [file:<相对路径>] tag（持久化随行）
  → 同时把 files 转 bus.InboundFile → agent 挂 providers.Attachment（用户气泡 chip 可回放）
  → 模型看到 tag → read_file 按需读取（fs 工具按 workspace 锚定相对路径）
```

**预览流（F2）**：

```text
文件来源检测（前端）：
  a) 消息附件 attachments[].path（send_file 链路新增源路径字段）
  b) tool_calls 解析 write_file/edit_file 的 path 参数
  c) content 正则匹配 [file:/path] tag 与 "Full output: <path>"
点击 → GET /pico/file?path=…（launcher 代理 + token）
  → gateway：白名单校验（workspace 内 ∪ 配置 roots ∪ 系统目录一票否决）
  → 响应头 X-Picoclaw-File-Kind（text/image/audio/video/binary）+ 流式 body
  → 右侧面板按 Kind 分派渲染器
```

### 3.2 关键决策记录

| # | 决策 | 理由（含否决项） |
| --- | --- | --- |
| D1 | 上传走 HTTP multipart（`POST /pico/upload`），**不**扩展 WS data URL 到任意文件 | WS 内联有 10MB JSONL 行上限（`maxSessionJSONLLineSize`）+ base64 膨胀 33% + 无进度无流式 + 会话历史膨胀。HTTP 上传后历史里只存路径字符串。 |
| D2 | 新端点挂 `/pico/` 前缀（pico channel `ServeHTTP` 分发） | mux 挂载、token 认证、launcher 代理模式全部现成；不引入 gateway 新的顶层路由面。 |
| D3 | 上传文件以 `[file:<相对路径>]` tag **在 gateway 侧拼进 content**，不注册 mediaStore、不改 `InboundMessage.Media` | (a) tag 随 content 持久化，重启后仍有效（mediaStore ref 是内存态，重启即失效）；(b) `SerializeMessages` 对非 data URL 的 Media 本就静默忽略，注册 ref 无收益；(c) 避免与 agent 侧 tag 注入重复打标。**注**：tag 解决模型可见性，用户气泡 chip 靠 D9 的附件链路。 |
| D4 | 图片继续走 data URL（`payload.media` 现状不动），不统一改走 files | `resolveMediaRefs` 只对 **tool 消息**的图片 ref 做 base64 编码（`agent_media.go`，`m.Role == "tool"` 条件），user 消息的 `media://` ref 不会被编码——图片改走 ref 会直接断掉现有视觉链路。File 对象在前端在手，图片零成本转 data URL。 |
| D5 | 附件携带源路径：`providers.Attachment` 加 `Path` 字段，链路四处贯通（§5.3） | 预览必须走稳定路径（D3 同理由）；ref 404 问题在媒体链路重启后已存在，路径化是唯一根治。 |
| D6 | `/pico/file` 路径白名单 = workspace 内 ∪ `web.file_access_roots` 配置 ∪ mediaStore ref 变体，`IsProtectedSystemPath` 一票否决 | 复用 `validatePathWithAllowPaths(restrict=true)` 现成实现（含 symlink 双重防逃逸）；fork 的"开放默认三件套"是 **agent 工具**的取向，web 是网络入口，必须单独收紧（参考 Octop `is_allowed_host_download_abs_path` 的同类设计）。 |
| D7 | 右侧面板 MVP = 单面板栈（后进先出）+ 拖宽 + 移动端全屏 Dialog | Octop 的多 tab/三模式（right/bottom/popup）是成熟形态但非首期必需（KISS）；结构上预留演进。 |
| D8 | 文件类型判定以服务端为准（响应头 `X-Picoclaw-File-Kind`） | 服务端已有 `h2non/filetype` magic 检测（`send_file.go detectMediaType` 同款）；前端扩展名表做 UI 预判，服务端 Kind 做最终分派。 |
| D9 | 上传文件**双挂载**：content 里的 tag（模型可见）+ `providers.Attachment`（用户气泡可回放） | 只挂 tag 时刷新页面用户气泡文件 chip 丢失（回放链路只读 `Attachments`，不解析 content tag）；只挂附件时模型看不到路径（`SerializeMessages` 忽略非 data URL）。两者职责不同，不可互相替代。 |
| D10 | tag 与附件中的路径一律用 **workspace 相对路径**，不用绝对路径 | 绝对路径泄露家目录结构与用户名（进用户可见正文 + 模型上下文）；相对路径被 fs 工具按 workspace 锚定解析，前端检测正则也能直接用。 |
| D11 | 文件管理采用 **(a) 本会话视图 + (b) 工作区浏览** 双入口 | (a) 零后端成本（路径信息已在消息里），刷新后从历史复原；(b) 覆盖跨会话产物与 agent 主动写的文件。两者语义不同不混：默认 (a)，显式切换 (b)。 |

---

## 4. 详细设计 A：任意文件上传（P0）

### 4.1 gateway 上传端点 `POST /pico/upload`

**位置**：`pkg/channels/pico/pico.go`——`ServeHTTP` switch 增加 `case "/upload", "/upload/"` → `handleUpload`。

**请求**（multipart/form-data）：

| 字段 | 必填 | 说明 |
| --- | --- | --- |
| `file` | 是 | 文件本体 |
| `filename` | 否 | 覆盖展示名（缺省取 multipart 文件名） |

**处理流程**（`handleUpload`，预计 ~120 行含错误处理）：

1. `authenticate(r)` → 401。
2. 大小限制：`http.MaxBytesReader(w, r.Body, int64(maxBytes))`；`maxBytes = cfg.Agents.Defaults.GetMaxMediaSize()`（默认 20MB，复用现有配置体系，不新增配置项）。超限返回 413 + 明确错误文本。
3. `r.ParseMultipartForm(maxBytes)` 解析（内存阈值同上限，落盘到 OS 临时区由标准库处理）。
4. 文件名清洗：`filepath.Base`（去路径成分）+ 剔除控制字符与 `:` `\` `/`；清洗后为空则 `upload.bin`。
5. 目标路径：`filepath.Join(workspace, "inbound", time.Now().Format("20060102"), uuid.NewString()[:8]+"_"+safeName)`。workspace 由 pico channel 持有（channel 初始化时从 config 解析，与 `send_file` 一致）；`inbound` 目录 `MkdirAll 0755`。
6. 落盘：`os.CreateTemp` 式安全写（同目录临时文件 + `os.Rename`，避免半写文件被预览读到；参考 `pkg/fileutil/rename*.go` 的 Windows 安全 rename 约定）。
7. **写上传索引**（§11.3）：追加一行到 `<workspace>/inbound/.index.jsonl`（`name` / `stored_name` / `path`（相对）/ `size` / `content_type` / `mtime` / `session`）。索引失败**不影响上传成功**（记 warn 即可）——索引只是元信息，文件系统是真源。
8. 响应 `200 {path, filename, content_type, size}`：`path` 为**workspace 相对路径**（`filepath.Rel(workspace, abs)`，D10）；`content_type` 用 `detectMediaType`（magic 优先，扩展名兜底）。

**错误语义**：401 未认证 / 413 超限 / 400 非 multipart 或缺 file / 500 落盘失败。错误文本 JSON `{"error": "..."}`（与 pico WS 错误风格对齐用纯文本亦可，实现时保持与 `handleMediaDownload` 的 `http.Error` 一致）。

**并发**：无共享状态（每请求独立文件），无需加锁。

### 4.2 launcher 代理路由

`web/backend/api/pico.go` `registerPicoRoutes` 增加一行：

```go
mux.HandleFunc("POST /pico/upload", h.handlePicoMediaProxy())
```

`handlePicoMediaProxy` 是通用带 token HTTP 代理（`createPicoHTTPProxy`），上传转发直接复用；cookie 会话 + IP 白名单由全局中间件栈自动覆盖。multipart body 透传（`httputil.ReverseProxy` 流式转发，无内存放大）。

### 4.3 落盘布局与生命周期

- 目录：`<workspace>/inbound/<yyyymmdd>/<id>_<name>`。按日期分桶便于人工清理与按时间排查。
- **不做自动清理**（YAGNI）：workspace 文件用户自管理，与 agent 写的其他文件同等对待。`inbound` 积累问题记录于 §10 开放问题。
- 上传文件天然可被 agent 的 fs 工具访问（在 workspace 内），模型可读可改——与"发给 agent 的文件"语义一致。

### 4.4 WS 协议扩展（`message.send`）

`payload` 新增可选字段：

```jsonc
{
  "content": "帮我分析这个数据集",
  "media": ["data:image/png;base64,..."],        // 现状字段，图片继续使用
  "files": [                                      // 新增：非图片文件引用
    { "path": "inbound/20261116/a1b2c3d4_sales.csv",   // workspace 相对路径
      "filename": "sales.csv", "content_type": "text/csv" }
  ]
}
```

- 向后兼容：老客户端无 `files` 字段，gateway 侧 `payload["files"]` 缺省即空，零影响。
- `handleMessageSend` 解析 `files`（每项校验 `path` 必须位于 `<workspace>/inbound/` 下——**收紧**：上传引用只认本通道刚落盘的目录，防伪造任意路径注入 tag）。
- 拼接 tag：`content = strings.TrimSpace(content)`；对每个 file 追加 ` [file:<相对路径>]`（图片类 content_type 可用 `[image:<相对路径>]`，对齐 `buildPathTag` 格式，供前端检测复用）。空 content 且有 files 时 content = tag 序列本身（绕过现有"content 与 media 均空则拒绝"校验前先拼接）。
- 幂等：沿用现有 `msg.ID` + `seenIDs` 机制，无新增。

**路径形态决策：用 workspace 相对路径，不用绝对路径。**

绝对路径（如 `/home/dddpeter/.picoclaw/workspace/inbound/...`）会随 tag 一起进用户可见的正文与模型上下文，泄露家目录结构与用户名。相对路径（`inbound/20261116/xxx.csv`）同时满足三件事：(a) 模型按 workspace 根解析（`validatePathWithAllowPaths` 本就以 workspace 锚定相对路径）；(b) 前端检测正则 `\[file:([^\]]+)\]` 拿到即可预览；(c) 不泄露宿主路径。**上传端点直接返回相对路径**（后端 `filepath.Rel(workspace, abs)`），gateway 拼 tag 与写索引均用相对路径；落盘的绝对位置由后端自己掌握，不外传。

### 4.5 agent 消费链路（含附件回放，非零改动）

**4.5.1 模型可见性（路径可解析）**

- tag 进 content → `userPromptMessage(content, media)` 持久化原始 content（含 tag）→ 每轮 prompt 组装时 tag 文本原样可见 → 模型按路径调用 `read_file` / `load_image`（相对路径由 fs 工具按 workspace 锚定解析）。
- 重启后：tag 在历史 content 里（持久化于 JSONL），路径指向 workspace 内稳定文件——**不依赖内存 ref，天然幸存**。
- 大文件不占上下文：只有路径字符串（约几十 token/文件），内容由模型按需读取。

**4.5.2 用户气泡回放（附件化，否则刷新即丢）**

**问题**：若只拼 tag（不挂附件），刷新页面后历史消息的用户气泡**只剩正文里的 `[file:...]` 裸文本，文件 chip 消失**。原因是历史回放链路 `session.go: sessionTranscriptMessages` → `sessionAttachments(msg)` 只读 `msg.Attachments`，**不解析 content 里的 tag**；而 `providers.Message.Attachments` 目前只被 agent 的工具附件链路填充（`pipeline_execute_loop.go:815`），用户消息走 `userPromptMessage` 这条路，没有 Attachments。

**修法**：让上传文件以 `providers.Attachment` 形式挂在用户消息上。

1. **bus 层**：`InboundMessage` 加 `Files []InboundFile`：

```go
// InboundFile describes a user-uploaded file referenced by an inbound message.
type InboundFile struct {
    Path        string `json:"path"`         // workspace 相对路径
    Filename    string `json:"filename"`
    ContentType string `json:"content_type,omitempty"`
}
```

2. **gateway**：`handleMessageSend` 把校验通过的 `files` 转成 `bus.InboundFile`，随 `HandleInboundContext` 传入（`BaseChannel.HandleInboundContext` 需加一个 files 参数或新增 `HandleInboundContextWithFiles`；推荐后者以免改动全部渠道调用点）。

3. **agent 侧**：`pipeline_setup.go:119` 的用户消息持久化入口构造附件：

```go
rootMsg := userPromptMessage(ts.userMessage, ts.media)
if len(ts.files) > 0 {
    rootMsg.Attachments = buildInboundFileAttachments(ts.files)  // Ref=相对路径
}
if len(rootMsg.Media) > 0 || len(rootMsg.Attachments) > 0 {
    ts.agent.Sessions.AddFullMessage(ts.sessionKey, rootMsg)  // 必须走 Full：附件才不会丢
} else {
    ts.agent.Sessions.AddMessage(ts.sessionKey, rootMsg.Role, rootMsg.Content)
}
```

**注意 `AddFullMessage` vs `AddMessage` 分叉**：现状 `pipeline_setup.go:119-124` 只在有 media 时走 `AddFullMessage`；挂上附件后必须同样走 `AddFullMessage`，否则附件字段被裁掉（这正是回放丢失的第二条路径）。

4. **回放透传**：`providers.Attachment` 加 `Path` 字段（§5.3 已列），`sessionAttachments` 透传 `Path`；`sessionAttachmentURL` 对**非 `media://` 的 Ref** 已直接当 URL 返回（`session.go:773`），因此 `Ref: <相对路径>` 能原样到达前端。

**副作用与取舍**：`Attachment.URL` 字段不能填相对路径——前端会把 `url` 直接当 `<img src>` / `<a href>` 用（`pi-message.tsx AttachmentView`）。因此前端改造点：文件类附件**以 `path` 为准**构造 href（`/pico/file?path=…`），`url` 仅用于图片（data URL）与媒体附件。

**安全回查**：`sessionAttachmentURL` 会把任意非 `media://` 的 Ref 当 URL 返回——本次新增的 Ref 是 workspace 相对路径（受上传白名单约束），不引入任意路径外泄面；但需在测试中钉住"Ref 必须是 `inbound/` 前缀的相对路径"。

### 4.6 前端 composer 改造

**`image-input.ts` 泛化为 `file-input.ts`**（保持图片函数导出兼容现有调用）：

- `ACCEPT` 放开为全类型；新增 `classifyFile(file)`：`image/*`（且 ≤7MB）→ "inline"（data URL，现状逻辑）；否则 → "upload"。
- 新增 `uploadFile(file): Promise<{path, filename, content_type}>`：`fetch("/pico/upload", {method:"POST", body: FormData})`，失败 toast（复用 sonner）。
- 上传并发上限 3（顺序队列即可，避免大文件并发挤占）。

**`ChatAttachment` 类型扩展**（`web/frontend/src/store/chat.ts`）：

```ts
export interface ChatAttachment {
  type: "image" | "audio" | "video" | "file"
  url: string            // 图片为 data URL；文件为空字符串（href 走 path 构造）
  filename?: string
  contentType?: string
  path?: string          // 新增：workspace 相对路径（上传响应 / 附件回放 / WS files）
}
```

**`chat-composer.tsx`**：

- 附件 chip 两态：图片缩略图（现状）；文件 chip（图标 + 文件名 + 上传中 spinner / 失败重试）。
- 发送组装（`controller.ts` sendMessage）：`media = 图片 dataURL[]`；`files = 非图片 [{path, filename, content_type}]`；用户气泡本端回显继续用现有 attachments（文件 chip 点击暂为 no-op，P1 接预览）。
- 拖拽/粘贴路径：`hasFileTransfer`/`getTransferredFiles` 已按 File 通用，随 classifyFile 自动获得任意类型支持。
- **上传响应路径形态**：上传端点直接返回 **workspace 相对路径**（后端 `filepath.Rel`），前端不做换算（前端不知道 workspace 根）——存入 `ChatAttachment.path` 与 `payload.files` 的均为相对路径，避免绝对路径进 WS 帧与本地状态。

**i18n**：五个 locale 文件加 `chat.uploadFailed` / `chat.fileTooLarge` / `chat.uploading` 等 key。

---

## 5. 详细设计 B：生成文件预览（P1 MVP + P2 富格式）

### 5.1 gateway 文件读取端点 `GET /pico/file`

**位置**：`ServeHTTP` 增加 `case "/file", "/file/"` → `handleFileRead`（预计 ~150 行）。

**请求**：

| 参数 | 说明 |
| --- | --- |
| `path` | workspace 相对路径或绝对路径 |
| `download` | `1` 强制 attachment 下载；缺省按类型 inline |

**处理流程**：

1. `authenticate(r)` → 401。
2. 路径解析与白名单校验（§5.2，复用 `validatePathWithAllowPaths`）。不过 → 403（**统一 404/403 防探洞**：路径不存在返回 404；越权返回 403 带最小信息）。
3. `os.Stat`：目录 → 400；不存在 → 404。
4. 大小上限：读取端点不设新上限（流式 `http.ServeContent` 本身支持 Range，大文件浏览器侧由渲染器决定是否全取；文本预览的超大保护在响应头层面给出 `Content-Length`，前端自行截断提示）。
5. Kind 判定：`detectMediaType`（magic + 扩展名）→ 归类 `text/image/audio/video/binary`（文本判定：MIME `text/*`、`application/json|xml|yaml|javascript|sql|csv` 或扩展名白名单）。
6. 响应：

```text
X-Picoclaw-File-Kind: text|image|audio|video|binary
X-Picoclaw-File-Name: <清洗后文件名>（URL 编码）
Content-Type: <MIME；文本类补 charset=utf-8>
Content-Disposition: inline|attachment; filename=...（复用 handleMediaDownload 的 mime.FormatMediaType 模式；SVG 强制 attachment）
```

body 用 `http.ServeContent`（Range/If-Modified-Since 免费获得，视频拖动进度条依赖此）。

**CSP 注意**：`/pico/file` 返回的 HTML 类内容禁止 inline（防存储型 XSS——`text/html` 归 binary + attachment，与 SVG 同待遇）。文本类仅 `text/plain` 直出。

### 5.2 路径安全设计（白名单三档）

**放行集合**（按序判定，任一命中即放行）：

1. **workspace 内**（默认）：`validatePathWithAllowPaths(path, workspace, restrict=true, nil)` 的完整语义——边界校验 + symlink 双重防逃逸（先 `EvalSymlinks` 目标本体，不存在则解析最近存在祖先）。
2. **配置扩展目录**：新配置 `web.file_access_roots`（字符串数组，默认空）。每个 root 转为锚定正则（`^` + `regexp.QuoteMeta(root)` + `(/|$)`）作为 `allowPaths` 传入同一函数——复用 send_file 已有的正则放行通道，**不新写路径判定逻辑**。相对 root 按 workspace 解析后取绝对。
3. **mediaStore ref 变体**：`?ref=media://<id>`（可选实现，P1 可缓）——已注册媒体（含 `send_file` 发送的 workspace 外文件）经显式 ref 授权读取，走 `ResolveWithMeta` 不走路径白名单。

**一票否决**：`IsProtectedSystemPath(absPath)` 命中即 403（`validatePathWithAllowPaths` 内部已含此检查，无需重复调用，但测试需钉住）。

**配置示例**（`~/.picoclaw/config.json`）：

```jsonc
{
  "web": {                       // 新增顶层节（launcher/web 语义域）
    "file_access_roots": ["/works/workspace/projects"]   // agent 常写、需预览的 workspace 外目录
  }
}
```

**与 fork 开放默认的关系**：`restrict_to_workspace=false` 是 agent 文件工具的取向（信任本机 agent）；`/pico/file` 是**网络入口**（launcher 可 public + CIDR 白名单），两者安全基线不同，后者默认收紧、显式配置才放开——与 Octop `is_allowed_host_download_abs_path` 的设计意图一致。该差异写入 fork-overview §同步注意事项，防止未来被"统一"掉。

### 5.3 附件源路径贯通（协议链，五处改动）

| 层 | 改动 | 文件 |
| --- | --- | --- |
| 协议类型 | `Attachment` 加 `Path string \`json:"path,omitempty"\`` | `pkg/providers/protocoltypes/types.go:106` |
| agent 填充（工具附件） | `buildProviderAttachments`：`ResolveWithMeta` 已返回 localPath，赋给 `attachment.Path` | `pkg/agent/agent_media.go:304` |
| agent 填充（上传附件） | 新增 `buildInboundFileAttachments(files)`：`Ref`/`Path` 均为 workspace 相对路径，`Type: "file"` | `pkg/agent/prompt_turn.go`（§4.5.2 步骤 3） |
| gateway 广播 | `SendMedia` attachments map 加 `"path": localPath` | `pkg/channels/pico/pico.go` SendMedia 循环内 |
| 历史回放 | `sessionChatAttachment` 加 `Path` 字段并透传 | `web/backend/api/session.go` |
| 前端 | `ChatAttachment.path`（§4.6 已定义）+ `parseAttachments` 解析 `attachment.path` | `web/frontend/src/features/chat/protocol.ts` |

**前端协议解析**（`protocol.ts parseAttachments`）：`path = typeof attachment.path === "string" ? attachment.path : undefined`，进入 store。

**路径形态不一致的处理**：工具附件的 `Path` 是**绝对路径**（agent 的 `send_file` 落盘位置在 workspace 内但以绝对路径记录，与 mediaStore meta 一致），上传附件的 `Path` 是**相对路径**（D10）。前端预览时两者都能用——`/pico/file?path=` 端点接受相对或绝对路径（§5.1），白名单校验统一处理；因此前端**不需要**区分，只在展示时优先用 `Path` 而非 `URL`。

### 5.4 前端文件检测（`features/chat/file-detection.ts` 新建）

移植 Octop `useChatFileDetection` 的三来源模式（纯函数 + 单测友好）：

```ts
export interface DetectedFile { path: string; filename: string; source: "attachment" | "toolCall" | "tag" }
export function detectFiles(messages: ChatMessage[]): DetectedFile[]
```

- **来源 a（附件）**：`message.attachments[].path`（§5.3 链路）。
- **来源 b（工具调用）**：`toolCalls[].function.name ∈ {write_file, edit_file, append_file, send_file}` 时 `JSON.parse(arguments)` 取 `path`（Octop 的 `PATH_KEYS` 集合：`path/file_path/filepath/dest/target_path/output_path`）；JSON 解析失败静默跳过。
- **来源 c（文本 tag）**：content 正则 `/\[(file|image|audio|video):([^\]\s]+)\]/g`（覆盖 §4.4 上传 tag 与 agent 侧 `injectPathTags` 产出）+ `Full output: <path>`（对齐 `pkg/tools/shell.go:842` 格式，路径特征 `shell-output-*.log`）。
- 去重键：规范化路径（`normalizeDockFilePath` 同义：去 `./`、统一分隔符）。
- 输出消费点：① 用户/助手消息内文件 chip 的预览按钮（§5.5）；② （可选）P2 的文件列表面板。

**渲染挂点**（`pi-message.tsx`）：

- `AttachmentView` 非 media 分支升级为 `MessageFileCard`：文件名 + 预览（Eye 图标，path 存在时显示）+ 下载（`url` 或 `/pico/file?path=..&download=1`）。
- `ToolCallBlock`（`tool-call-block.tsx`）：write_file 类工具的反馈行附加"预览文件"链接（从 argumentsText 提取 path，复用来源 b 的解析函数）。
- content 内 `[file:...]` tag：`markdown.tsx` 渲染层把 tag 替换为内联文件 chip（正则替换为占位组件；不可解析时保持原文本）。

### 5.5 右侧预览面板

**布局**（`chat-page.tsx`）：

```tsx
<div className="flex h-full">                    // 现有最外层（:386）
  <sessionSidebar />                              // 现有左侧
  <div className="flex min-w-0 flex-1">           // 新：主区行容器（原 flex-col 上提一层）
    <div className="flex min-w-0 flex-1 flex-col">// 原主列（消息列表 + composer）不动
      …
    </div>
    <FilePreviewPanel />                          // 新：右侧面板（条件渲染）
  </div>
</div>
```

**`FilePreviewPanel` 组件**（`components/chat/file-preview-panel.tsx`，预计 ~200 行）：

- 状态：jotai `filePreviewAtom: { path, filename } | null`（`store/chat.ts` 或独立 atoms 文件）。
- 结构：头部（文件名 + Kind 徽标 + 下载按钮 + 关闭）+ 内容区（按 Kind 分派 §5.6 渲染器）+ 底部状态行（大小/路径 tooltip）。
- 宽度：默认 420px，`min 320 / max min(720, 50vw)`；左缘拖拽调宽（pointer events，参考现有 `useDragOffset` 思路自实现 ~40 行，不引库）。宽度持久化 localStorage（`picoclaw:file-preview:width`）。
- 打开来源：`setFilePreview({path, filename})`（§5.4 全部点击点调用）。
- 关闭：X 按钮 / Esc；再次点击同文件不重复入栈（MVP 单文件栈，无历史导航——多 tab 留 P2）。
- 移动端（`< md`）：不渲染侧列，改为 radix `Dialog` 全屏承载同一内容组件。
- 空态/错误：404/403/网络失败 → 面板内联错误卡（含"下载试试"兜底按钮）。

**加载**：`@tanstack/react-query`（已在依赖）`useQuery(["file", path])` fetch `/pico/file?path=…`，text 类取 body 文本（超 1MB 截断 + 提示），媒体类直接把 URL 交给 `<img>/<video>/<audio>` 元素由浏览器流式拉取。

### 5.6 渲染器矩阵

| Kind | 判定（服务端 `X-Picoclaw-File-Kind` 为准） | P1 渲染 | P2 渲染 |
| --- | --- | --- | --- |
| text | MIME `text/*`、json/xml/yaml/js/ts/go/py/sql/csv/md 等 | `<pre>` + highlight.js（已有依赖；超 1MB 截断）| — |
| text + `.md` | 扩展名 | `react-markdown`（复用 `chat-pi/markdown.tsx` 组件，含代码高亮） | — |
| image | `image/*`（SVG 除外，见 §5.1） | `<img>`（`/pico/file?path=` 直链） | 点击放大 |
| audio / video | MIME 前缀 | `<audio controls>` / `<video controls>`（Range 由 ServeContent 支持） | — |
| binary | 其余（含 pdf/docx/xlsx/pptx/html/svg） | 下载兜底卡（图标 + 大小 + 下载按钮） | pdf：`pdfjs-dist`；docx：`docx-preview`；xlsx：`SheetJS(xlsx)`；三者均为纯前端渲染、license 兼容（Apache-2.0/MIT），动态 `import()` 按需加载不增首屏包 |

P2 依赖引入走 `vite` 动态导入 + `package.json` devDependencies 评审（docx-preview MIT、xlsx Apache-2.0、pdfjs-dist Apache-2.0）。

---

## 6. 安全设计汇总

| 面 | 措施 | 依据/复用 |
| --- | --- | --- |
| 传输认证 | launcher cookie 会话 + IP 白名单（全局中间件）；gateway Bearer token（代理注入） | 现有栈，零新增 |
| 上传 | 大小上限（`max_media_size`，20MB 默认）；文件名清洗（`filepath.Base` + 控制字符剔除）；同目录临时文件 + rename 原子落盘；落盘限定 `inbound/<date>/` | §4.1 |
| WS 注入 | `files[].path` 强制校验位于 `<workspace>/inbound/` 前缀内（clean 后），防伪造路径打 tag | §4.4 |
| 读取白名单 | workspace ∪ `web.file_access_roots` ∪ ref；`IsProtectedSystemPath` 一票否决；symlink 双重防逃逸 | §5.2，复用 `validatePathWithAllowPaths` |
| XSS | `text/html`、SVG 一律 attachment 不 inline；面板内文本渲染不使用 `dangerouslySetInnerHTML`（react-markdown 默认安全） | §5.1/5.6 |
| 探洞 | 越权 403 / 不存在 404 分离且错误体最小化 | §5.1 |
| 日志 | 上传/读取失败记 warn（含 path 与原因），不记文件内容 | 现有 logger 约定 |

---

## 7. 测试计划

### 7.1 Go 单测（新增/扩展）

**`pkg/channels/pico/pico_upload_test.go`**（新增，照 `pico_test.go` 的 httptest 模式）：

- 上传成功：落盘路径/响应字段/文件内容一致。
- 超限 413；未认证 401；非 multipart 400。
- 恶意文件名（`../../etc/cron.d/x`、空名、控制字符）清洗结果。
- `handleMessageSend` files 注入：tag 拼接格式；`files[].path` 越 `inbound/` 前缀被拒；空 content + files 合法。

**`pkg/channels/pico/pico_file_test.go`**（新增）：

- workspace 内相对/绝对路径 200 + Kind 头正确（文本/图片/二进制三分支）。
- workspace 外路径 403；`file_access_roots` 命中放行；系统目录（`/etc/passwd`、`/Windows/System32` 按平台）403。
- symlink 逃逸（workspace 内 symlink 指向外部）403。
- `download=1` 强制 attachment；SVG/HTML 强制 attachment；Range 请求 206。
- 目录路径 400；不存在 404。

**`SendMedia` path 字段**：`pico_test.go` 补断言 attachments 含 `path`。

**`pkg/agent/agent_media_test.go`**：`buildProviderAttachments` 填充 `Path`。

**`web/backend/api/`**：代理路由测试（照 `gateway_test.go`/auth 测试模式）——`POST /pico/upload`、`GET /pico/file` 经代理转发携带 token、gateway 不可用时 503；`sessionChatAttachment` 回放含 path。

**既有回归**：`go test ./pkg/...` 全量（agent 套件约 70s）必须绿——D9 的 agent 侧改动集中在用户消息持久化入口（`pipeline_setup.go:119`），影响面可控。

### 7.2 前端 vitest

- `file-detection.test.ts`：三来源解析 + 去重 + 坏 JSON 容错（覆盖 `[file:]` tag、`Full output:`、PATH_KEYS 各分支）。
- `protocol.test.ts` 补：`parseAttachments` 解析 `path` 字段。
- `file-preview-panel.test.tsx`：打开/关闭/错误态/移动端 Dialog 分支。
- `chat-composer` 相关：classifyFile 分流（图片 inline / 文件 upload）、上传失败重试态。

### 7.3 端到端验证点（每期验收门）

| 期 | 验证点 |
| --- | --- |
| P0 | web 上传 20MB csv → 消息发出 → agent 回答能引用该文件内容（read_file 成功）；刷新页面历史消息文件 chip 仍在 |
| P1 | 让 agent `write_file` 生成 md → 工具卡点"预览" → 右侧面板渲染 Markdown；`send_file` 发 png → 附件点预览 → 面板显示图片；workspace 外文件点击 → 明确 403 提示 |
| P2 | `send_file` 发 pdf → 右侧分页预览 |

---

## 8. 分期与工作量

| 期 | 内容 | 后端 | 前端 |
| --- | --- | --- | --- |
| P0 上传 | §4 全部（端点/代理/协议/composer） | ~340 行（含测试） | ~200 行（含测试与 i18n） |
| P1 预览 MVP | §5.1~5.5 + 文本/MD/图片/音视频渲染 | ~250 行（含测试） | ~500 行（面板+检测+卡片改造） |
| P1' 文件管理 | §11 全部（tree 端点 + 上传索引 + 双视图面板） | ~220 行（含测试） | ~310 行（含测试） |
| P2 富格式 | §5.6 pdf/docx/xlsx + 多 tab dock（可选） | — | ~500+ 行 |

依赖顺序：P0 → P1（P1 的下载兜底与文件 chip 依赖上传期的 path 数据）；P1' 紧随 P1（文件管理复用 P1 的预览面板与检测逻辑）；P2 独立可插。

**后端规模说明**：P0 从 ~300 上调至 ~340 行，因为 D9/D10 的附件回放链路新增了 bus 类型（`InboundFile`）、`HandleInboundContextWithFiles` 变体与 agent 侧 `AddFullMessage` 分叉处理——这部分不可省，否则刷新后用户气泡文件 chip 丢失（见 §4.5.2）。

---

## 9. 上游同步注意事项（fork-overview 增补条目）

- `pkg/channels/pico/pico.go`：上游活跃大文件（60K），本次在 `ServeHTTP`/`handleMessageSend`/`SendMedia` 三处增量——合并冲突高发区，改动保持局部、不重排既有代码。
- `pkg/providers/protocoltypes/types.go`：`Attachment` 加 `Path` 字段——上游若重构 Attachment 需保字段。
- `pkg/agent/agent_media.go`：`buildProviderAttachments` 单点填充。
- `web/backend/api/pico.go`、`session.go`：launcher 侧路由与回放。
- 新配置节 `web.file_access_roots`：合并上游后需确认无同名节冲突。
- fork 独有测试基线（§7.1 新增）同步上游后必须保持通过。

---

## 10. 开放问题（实施前需拍板）

1. **workspace 外生成文件的预览授权**：默认 403 体验是否可接受（替代：引导模型多用 `send_file`，其附件走 ref 变体可预览）？`web.file_access_roots` 是否够用，还是需要"会话内临时授权"（点击时 agent 确认）？——倾向 P1 先用配置项，观察实际使用再迭代。
2. **`inbound/` 积累治理**：是否需要按容量/年龄的自动清理（复用 media cleanup 的配置形态）？——P0 不做，积累速率未知，避免过早设计。
3. **历史 data URL 图片 token 膨胀**：现状问题（历史消息 media data URL 每轮进 prompt），上传功能落地后图片仍走 data URL 会延续；是否顺带把图片也路径化（需先改 `resolveMediaRefs` 对 user 图片的编码条件）？——独立议题，另立设计。
4. **`/pico/file` 与多 agent workspace**：当前 gateway 单 workspace（`agents.defaults.workspace`）；未来多 agent 并存时端点需带 agent 维度（Octop 的 `/agents/{aid}/workspace/*` 形态）——接口留 `path` 语义，届时加前缀参数，不提前实现。
5. **上传文件与 IM 渠道的一致性**：`files` 仅 pico 通道生效；feishu 等渠道的文件消息已有各自管线，不互通。若未来要求"任意渠道上传进 workspace"，需在 channels 层抽象 inbound 落盘——超出本设计范围。
6. **上传文件是否允许删除/重命名**：当前设计只读（上传、预览、下载）。用户可能需要删掉误传的大文件、或重命名后再发给 agent。删除涉及 workspace 写入权限（agent 的 delete 工具已有先例，但 web 端直写 workspace 是新暴露面）——建议 P2 再说，MVP 保持只读。

---

## 11. 文件管理（已上传文件 + 生成文件）

### 11.1 定位与形态

在右侧面板增加 **[文件]** 视图（与 [预览] 同壳，§5.5 的 `FilePreviewPanel` 扩展为双 tab），提供两个入口：

| 入口 | 语义 | 数据源 | 后端成本 |
| --- | --- | --- | --- |
| **(a) 本会话** | 当前会话的上传文件 + 生成文件 | 上传：会话消息附件（§4.5.2 链路）；生成：消息内容里的路径线索 | **零**（路径信息已在消息里） |
| **(b) 浏览工作区** | workspace 全量文件树 | `GET /pico/tree` 递归 | ~120 行（§11.2） |

**默认展示 (a)**；(b) 作为视图切换入口（面板头部一个 segmented control）。两者语义不混：(a) 是"这次对话涉及的"，(b) 是"机器上 agent 能看到的"。

### 11.2 目录列表端点 `GET /pico/tree`

**位置**：`pkg/channels/pico/pico.go`——`ServeHTTP` 增加 `case "/tree", "/tree/"` → `handleTree`（~120 行）。

**请求参数**：

| 参数 | 默认 | 说明 |
| --- | --- | --- |
| `path` | `.` | 目录路径（workspace 相对或绝对），受白名单约束 |
| `depth` | `1` | 递归深度，上限 3（防大目录爆炸） |
| `filter` | 无 | 可选：仅列出文件（不含目录） |

**响应**：

```jsonc
{
  "path": "inbound/20261116",
  "entries": [
    { "name": "sales.csv", "path": "inbound/20261116/sales.csv", "is_dir": false,
      "size": 20480, "mtime": "2026-11-18T10:30:00Z" },
    { "name": "20261117", "path": "inbound/20261117", "is_dir": true, "size": 0, "mtime": "..." }
  ],
  "truncated": false
}
```

**实现要点**：

1. `authenticate(r)` → 401。
2. 路径校验：**复用 §5.2 同一套白名单**（`validatePathWithAllowPaths(restrict=true)` + `web.file_access_roots`），不过 → 403。
3. 目录列举：`os.ReadDir`（路径已通过白名单校验，无穿越风险）；`depth>1` 时递归，**总条目上限 2000**，超出置 `truncated: true` 并停止（防超大 workspace 卡住请求）。
4. 隐藏项：跳过以 `.` 开头的条目（`.git`、`.picoclaw` 等）与 `node_modules`（除非显式请求该路径）。
5. 条目路径**统一返回 workspace 相对路径**（与 D10 一致），便于前端直接拼 `/pico/file?path=`。
6. `mtime` 用 RFC3339；`size` 对目录为 0。

**launcher 代理**：`registerPicoRoutes` 加一行 `mux.HandleFunc("GET /pico/tree", h.handlePicoMediaProxy())`（复用通用带 token 代理）。

### 11.3 上传文件索引（精确区分"上传的"与"生成的"）

**问题**：agent 能写 workspace 任意位置（包括 `inbound/`），因此"列 `inbound/` 目录"会把 agent 生成的文件混入"用户上传"分区。

**方案**：上传时落盘同时写一条索引。

- 索引文件：`<workspace>/inbound/.index.jsonl`（**追加写**，每行一条 JSON）。
- 记录字段：`{"id","name","stored_name","path","size","content_type","mtime","session"}`。
  - `path` = workspace 相对路径（与 tag / 附件一致）
  - `session` = 上传时的 pico sessionID（用于 (a) 视图按会话过滤）
- 写入时机：上传落盘成功后（同目录临时文件 + rename 原子落盘后），追加一行。
- 读取：`GET /pico/tree?path=inbound` 额外返回索引信息；或新增轻量端点 `GET /pico/uploads?session=` 直接返回该会话的上传清单。

**取舍**：索引是**尽力而为**（append-only、无事务）。索引与文件不一致时（用户手工删文件、手工改目录）以**文件系统为准**——列表端点先列真实目录，再用索引补充 `original_name` / `session` 等元信息；索引里的孤儿条目不展示。理由：文件系统是唯一真源，索引只是"把上传的文件和 agent 生成的文件区分开"这一层元信息。

### 11.4 前端：文件管理视图

**组件**：`web/frontend/src/components/chat/file-manager-panel.tsx`（新建，~250 行）。

**结构**：

```text
文件管理面板
├─ 头部：视图切换（[本会话] [浏览工作区]）+ 刷新按钮
├─ (a) 本会话视图
│   ├─ 分区「上传的文件」：来自消息附件（attachments[].path 且 type=file）
│   │   └─ 行：图标 + 文件名 + 大小 + [预览] [下载]
│   └─ 分区「生成的文件」：来自消息路径线索（§5.4 检测逻辑）
│       └─ 行：同上
└─ (b) 工作区视图
    └─ 树形列表（递归 depth≤3）：目录可折叠，点击文件 → 预览
```

**数据来源**：

| 分区 | 来源 | 刷新后是否可得 |
| --- | --- | --- |
| 上传的文件 | 消息附件 `attachments[].path`（§4.5.2 已挂载） | **是**（附件随历史回放） |
| 生成的文件 | 消息内容中的路径线索：工具调用参数（`write_file` / `edit_file` / `send_file` 的 `path`）+ `[file:...]` tag + `Full output: <path>` | **是**（历史消息含工具调用与正文） |
| 工作区视图 | `GET /pico/tree?path=&depth=` | 实时 |

**路径线索提取**：`web/frontend/src/chat-pi/tool-args.ts` 已有 `PATH_RE`（正则提取工具参数里的 path，支持流式半截 JSON），**直接复用**，不另写解析器。`[file:]` tag 与 `Full output:` 用正则补齐。

**已知限制（需在 UI 上如实告知用户）**：

工具调用的可见参数文本被截断到 **300 字符**（`config.AgentDefaults.GetToolFeedbackMaxArgsLength`）。`write_file` 的参数含整个文件内容，因此**大文件写入时，参数截断可能把 `path` 一并切掉**（路径在参数 JSON 前部，通常幸存，但 content 极长时存在风险）。后果：这类生成文件**不出现在 (a) 视图**，但仍能在 (b) 工作区视图看到。

缓解（P1 可选）：服务端在工具调用可见参数里**优先保留 path 字段**（`utils.VisibleToolCallArgumentsPreview` 截断前先把 `path` 摘出来单独保留），这是对现有截断策略的最小侵入式改进——需改动 `pkg/utils/visible_tool_calls.go` + 对应测试，列入 P1 尾巴。

### 11.5 与预览面板的整合

`FilePreviewPanel`（§5.5）从"单面板"升级为"双 tab 壳"：

```text
┌─ 右侧面板 ────────────────┐
│ [预览] [文件]              │  ← tab 切换（jotai atom 持有 activeTab）
├────────────────────────────┤
│ (预览) 当前文件内容渲染区    │
│ (文件) 文件管理视图          │
└────────────────────────────┘
```

- 状态：`filePreviewAtom = { tab: "preview" | "files", path?, filename? }`。
- 从文件管理视图点击文件 → `setFilePreview({tab:"preview", path})` → 切到预览 tab 并加载内容（复用 §5.6 渲染器）。
- 从消息里的文件 chip 点击 → 同样切到预览 tab（§5.5 已定义）。
- 移动端：同一内容组件在 Dialog 内呈现，tab 结构不变。

### 11.6 测试计划（增补）

**Go**：

- `pkg/channels/pico/pico_tree_test.go`（新增）：单层列举（目录/文件混合、隐藏项过滤）、`depth=2` 递归、条目上限截断（`truncated=true`）、workspace 外路径 403、系统目录 403、目录不存在 404、非目录路径 400。
- `pkg/channels/pico/pico_upload_test.go` 增补：上传后 `.index.jsonl` 追加一条记录且字段正确；索引损坏行不影响其他行（跳过而非报错）。
- `pkg/channels/pico/pico.go` 的 `handleUpload` + `handleTree` 共享白名单校验——抽一个 `resolveWebPath()` 辅助函数，**单测覆盖两处调用**（避免两套判定漂移）。

**前端**：

- `file-manager-panel.test.tsx`：两个视图切换、(a) 两个分区分组正确、点击文件切到预览 tab、索引缺失时的降级展示。
- `file-detection.test.ts` 扩补：300 字符截断场景下 path 丢失 → 该文件**不**出现在 (a) 视图（断言此行为，避免回归成静默错误）。

### 11.7 工作量（增补）

| 项 | 后端 | 前端 |
| --- | --- | --- |
| `/pico/tree` 端点 + 代理 | ~120 行（含测试） | — |
| 上传索引（`.index.jsonl` 写入 + 读取） | ~60 行（含测试） | — |
| 文件管理面板（双视图 + 树） | — | ~250 行（含测试） |
| `FilePreviewPanel` 升级为双 tab | — | ~60 行 |
| 工具参数 path 保留（§11.4 缓解，P1 尾巴） | ~40 行（含测试） | — |

**分期归属**：P1 收尾时一并落地（文件管理是 P1 预览能力的自然延伸：预览需要"文件从哪来"的清单）。建议顺序：先 `/pico/tree` → 再上传索引 → 最后前端面板。
