# Web 端 MCP 设置独立页面设计

状态：已实现 ｜ 2026-09-13 ｜ 决策记录：§8（深度选 B 方案，入口放 Agent 组，不做常驻轮询）

本文记录把 Web 端（launcher dashboard）的 MCP 设置从 `/config` 页独立为专门页面 `/mcp`
的完整设计：页面结构与交互、Web backend 专属 API、gateway 运行状态端点、连接测试、
迁移兼容与测试计划。

## 1. 背景与现状

### 1.1 现状

- MCP 设置目前是 `/config` 页的一个 section（`web/frontend/src/components/config/config-sections.tsx`
  的 `MCPSection`）：全局开关 + 发现设置（TTL / max_search_results / use_bm25 ⊕ use_regex）+
  服务器平铺表单（stdio / sse / http 三类，含 headers / args / env / env_file / deferred 覆盖）。
- 保存走整份 `PUT /api/config`，与页面其它 section 共用一次全量写盘。
- 侧边栏分组：聊天 / 模型（模型、凭据）/ 通道 / Agent（hub、技能、工具）/ 服务（配置、日志）。
- 后端已有"专属端点"先例：`GET/PUT /api/tools/web-search-config`。

### 1.2 进程拓扑（决定设计形态的硬约束）

```
web backend（launcher dashboard，独立进程）
  · 读写 config.json（lenient loader，保留未知字段）
  · 已知 gateway 发现方式：PID 文件 + health 探测（getGatewayHealth，bearer token）
gateway（主进程）
  · mcp.Manager 在 turn 时懒初始化（pkg/agent/agent_mcp.go ensureMCPInitialized）
  · health server（pkg/health）暴露 /health /ready /reload（token 保护）
```

两个关键推论：

1. **运行时状态（连接、工具清单）只存在于 gateway 进程**，web backend 看不到；
2. **Manager 是懒初始化的**——turn 没跑过之前是 nil。因此"看不到连接"不等于"配置坏了"，
   状态语义必须区分（见 §3）。

## 2. 总体架构（三层）

```
┌─ Frontend (/mcp 路由) ──────────────────────────────┐
│  服务器列表 + 发现与高级两个 Tab + 运行状态徽章      │
└──────────────┬──────────────────────────────────────┘
┌─ Web backend ─▼──────────────────────────────────────┐
│  GET/PUT /api/mcp/config    读写 config.json          │
│  POST /api/mcp/servers/test 拨号测试（进程内复用 pkg/mcp）│
│  GET  /api/mcp/status       代理 gateway 状态端点      │
└──────────────┬──────────────────────────────────────┘
┌─ Gateway ────▼───────────────────────────────────────┐
│  health mux 新增 GET /mcp/status（bearer token）       │
│  从 mcp.Manager 读连接状态 / 工具清单快照             │
└──────────────────────────────────────────────────────┘
```

关键取舍：

- **测试连接在 web backend 进程内做**：`pkg/mcp` 新增导出函数 `ProbeServer(ctx, cfg, workspacePath)`，
  spawn stdio / 拨 http / sse，15s 超时后关闭，返回工具清单。web backend 与 gateway 同一 Go module，
  直接复用 `pkg/mcp` 现有连接代码，gateway 零改动。envFile 相对路径以 gateway workspace 为基准解析
  （与 `LoadFromMCPConfig` 的 `workspacePath` 语义一致）。
- **运行状态走 gateway**：health server 新增 `GET /mcp/status`，web backend 复用
  `getGatewayHealth` 的地址发现逻辑转发。

## 3. 运行状态四态语义

| 状态 | 含义 | UI 表现 |
|---|---|---|
| `offline` | gateway 进程不在线 / health 不可达 | 顶部灰色提示「gateway 未运行，以下为配置值」；所有服务器徽章置灰 |
| `not_initialized` | gateway 在线但 Manager 尚未初始化（turn 未跑过 / 全局关闭 / 无启用服务器） | 徽章灰 ○ 未加载；提示「gateway 将在下一次对话时加载 MCP」 |
| `connected` | Manager 已加载且该服务器连接正常 | 绿 ● 在线 + 工具数；卡片内展示只读工具 chips |
| `error` | Manager 已加载但该服务器连接失败 | 红 ● 错误 + 错误信息（截断） |

禁止把 `not_initialized` 渲染成"错误"——这是懒初始化的正常态，不是配置问题。

## 4. API 设计

### 4.1 Web backend

```jsonc
// GET /api/mcp/config
{
  "enabled": true,
  "maxInlineTextChars": 16384,
  "discovery": { "enabled": false, "ttlSeconds": 5, "maxSearchResults": 5,
                 "useBM25": true, "useRegex": false },
  "servers": [
    { "name": "openviking", "type": "http", "enabled": true, "deferred": null,
      "url": "https://…", "headers": {"Authorization": "…"},
      "command": "", "args": [], "env": {}, "envFile": "" }
  ]
}

// PUT /api/mcp/config ← 同结构
// 校验：名称非空且唯一；type ∈ {stdio, sse, http}；stdio 必填 command；sse/http 必填 url
// 写回：lenient load → mutate cfg.Tools.MCP → validate → SaveConfig，
//   与 PATCH /api/config 共用同一套 merge-validate-save 语义（configWriteMu 串行化）。
//   注意：仓库既定行为是保存时按已知结构重写、丢弃未知字段（healing 语义，
//   见 web/backend/api/config.go applyConfigPatch 注释），本端点遵循同一语义，
//   不另行实现保留逻辑。

// POST /api/mcp/servers/test ← 单个 server 对象（不落盘）
// → { "ok": true, "latencyMs": 342, "toolCount": 8,
//      "tools": [{ "name": "…", "description": "…" }], "error": "" }

// GET /api/mcp/status → 代理 gateway；gateway 不可达 → { "gateway": "offline" }
```

一致性说明：`GET /api/mcp/config` 中 headers 明文返回——与现有 `GET /api/config` 行为一致，
不单独新增脱敏造成两处不一致。

### 4.2 Gateway

```jsonc
// GET /mcp/status（挂 pkg/health 现有 mux，自动继承 bearer token 保护）
{
  "initialized": true,
  "enabled": true,
  "servers": {
    "openviking": { "connected": true, "toolCount": 8,
                    "tools": [{ "name": "…", "description": "…截断至200字" }], "error": "" }
  }
}
```

实现：`pkg/health.Server` 增加 `SetMCPStatusFunc(func() any)`，handler 序列化其返回值；
`pkg/gateway/gateway.go` 注入闭包——从 `mcpRuntime` 加锁拷贝 Manager 快照
（`GetServers` / `GetAllTools`），Manager 为 nil 时返回 `initialized: false`。
description 截断 200 字控制响应体积。

鉴权：health server 的 token 即 PID 文件的 `Token` 字段（`pkg/pid/pidfile.go`，
gateway 启动时 `pidData.Token` 传入 `health.NewServer`），web backend 本就加载同一
PID 文件做 host:port 发现——转发 `/mcp/status` 时从 `gateway.pidData.Token` 取值，
附加 `Authorization: Bearer` 头。注意：现有 `/health` 探测（`gatewayHealthGet`）
不带鉴权头（该端点无保护），带 token 转发是本设计新增的动作；pidData 缺失或
Token 为空时按 `offline` 降级。

### 4.3 ProbeServer（pkg/mcp/probe.go）

```go
type ProbeResult struct {
    LatencyMS  int64
    ToolCount  int
    Tools      []*mcp.Tool
}
func ProbeServer(ctx context.Context, cfg config.MCPServerConfig, workspacePath string) (*ProbeResult, error)
```

内部走 NewManager + LoadFromMCPConfig（单服务器）+ GetAllTools + Close，
复用现有 connectServer / loadEnvFile / headerTransport 逻辑，不新增协议实现。

## 5. 页面设计

### 5.1 入口与路由

- 路由 `/mcp`（`routes/mcp.tsx`，TanStack 文件路由）。
- 侧边栏 **Agent 分组**（hub / 技能 / 工具之后）新增「MCP」，图标 `IconPlugConnected`。

### 5.2 顶部状态条（常驻）

- 全局开关 `mcp.enabled` 镜像 switch（编辑草稿的一部分，随保存生效）。
- 运行摘要：gateway 在线时「N/M 台服务器在线 · 共 K 个工具」+ 手动刷新按钮；
  离线时灰色提示「gateway 未运行，以下为配置值」。

### 5.3 Tab 1 — 服务器（默认）

**服务器卡片**（折叠态展示摘要，展开编辑，替代现有平铺表单）：

- 卡片头：名称、类型徽章（stdio/sse/http）、enabled switch、运行状态徽章（§3 四态），
  在线时显示工具数。
- 展开区：
  - deferred 发现模式三选（继承 / 延迟 / 即时），映射 `deferred: null | true | false`；
  - 类型相关字段——stdio → command / args / env / env_file；sse、http → url / headers；
  - 「测试连接」按钮：调 `POST /api/mcp/servers/test`，结果内联展示——延迟 ms、工具数、
    可折叠工具清单（name + 截断 description）；失败显示错误；
  - 在线状态下展示该服务器当前注册的工具 chips（来自 gateway status，只读）。
- 卡片操作：删除（带确认）。
- 「添加服务器」按钮 → 追加一张展开的空白卡片；空状态给引导文案 + 添加按钮。

### 5.4 Tab 2 — 发现与高级

- 发现模式：`discovery.enabled` 开关 → 展开 ttl / max_search_results /
  use_bm25 ⊕ use_regex（保持现有互斥禁用逻辑：至少留一个）。
- 高级：`max_inline_text_chars`（新增暴露；现有 config 页没有此字段）。

### 5.5 保存模型

整页草稿 + 脏检测 + 显式「保存」按钮（沿用 config 页与 web-search tab 既有模式）。
保存成功后自动刷新运行状态；放弃修改需确认。

## 6. 迁移与兼容

- 原 `/config` 页 MCP section **移除**，原位置放提示卡：「MCP 设置已独立为专门页面 →」跳转链接。
- `form-model.ts` 中 MCP 相关类型与映射代码迁到新模块 `components/mcp/`，config 页瘦身。
- i18n：zh / en 全量新增 key，cs / pt-br / bn-in 同步补齐。
- type 选项维持 stdio / sse / http 三项（后端 `http` 即 streamable-http，与现状一致，不做变更）。
- 行为红线：本设计不改 `tools.mcp` 配置 schema、不改 gateway MCP 加载/发现语义，
  仅新增只读状态暴露与独立编辑入口。

## 7. 文件清单与测试

### 7.1 文件清单

| 层 | 文件 | 动作 |
|---|---|---|
| gateway | `pkg/health/server.go` | +`SetMCPStatusFunc`、`/mcp/status` handler |
| gateway | `pkg/gateway/gateway.go` | 注入 manager 快照闭包 |
| pkg | `pkg/mcp/probe.go` | 新增 `ProbeServer` |
| backend | `web/backend/api/mcp.go` | 新增：4 个端点 |
| backend | `web/backend/api/router.go` | 注册路由 |
| backend | `web/backend/api/mcp_test.go` | 新增测试 |
| 前端 | `routes/mcp.tsx` | 新增路由 |
| 前端 | `api/mcp.ts` | 新增 API client |
| 前端 | `components/mcp/{mcp-page,server-card,discovery-settings,use-mcp-page}.tsx` | 新增 |
| 前端 | `app-sidebar.tsx` | Agent 组加入口 |
| 前端 | `config-sections.tsx`、`config-page.tsx`、`form-model.ts` | 移除 MCP section，留跳转卡 |
| 前端 | `i18n/locales/*.json` | 新增 key |

### 7.2 测试计划

- **backend 单测**（`web/backend/api/mcp_test.go`）：
  config GET/PUT 往返（含未知字段保留、校验失败矩阵）、test 端点注入 fake probe、
  status 代理的 gateway 离线降级路径。
- **gateway 单测**：`/mcp/status` 快照输出（fake status func）、Manager 未初始化 /
  已关闭语义、token 保护继承。
- **pkg/mcp 单测**：ProbeServer 用内存/httptest MCP server 验证 stdio 与 http 两路
  （参照现有 `manager_integration_test.go` 的桩法）。
- **前端**：仓库无前端测试设施，本设计不新增；以下方手动验收清单为准。

### 7.3 手动验收清单

1. `/config` 页 MCP section 已移除、跳转卡可用；侧边栏 Agent 组出现 MCP 入口。
2. `/mcp` 页：新增 stdio 服务器（command 填错）→ 测试连接报错且不影响保存；
   新增 http 服务器 → 测试连接返回延迟与工具清单。
3. 校验：重名 / 空名 / stdio 无 command / http 无 url，保存被拦截并提示。
4. gateway 运行时：跑过一次对话后刷新状态 → 在线徽章 + 工具 chips；
   gateway 停止 → offline 提示 + 徽章置灰。
5. 含未知字段的配置在 `/mcp` 页加载时出现 warning 提示；保存后文件按已知结构重写
   （仓库既定 healing 语义，与 PATCH /api/config 一致）。
6. i18n：zh / en 切换无缺 key。

## 8. 设计决策记录

| 决策点 | 选择 | 理由 |
|---|---|---|
| 实现深度 | B：独立页面 + 专属端点 + gateway 状态；不做运行时启停/重连 | 页面体验完整，且避免"运行时状态 vs 磁盘配置"双写源复杂度 |
| 测试连接执行方 | web backend 进程内（ProbeServer） | 同 module 复用 pkg/mcp，gateway 零改动；一次性进程测完即关 |
| 状态刷新 | 进页拉取 + 手动刷新 + 保存后刷新，不做常驻轮询 | YAGNI；MCP 拓扑低频变化 |
| 工具浏览位置 | 服务器卡片内（chips + 测试连接清单），不设独立 Tab | 归属清晰，避免页面过重 |
| 侧边栏位置 | Agent 组（工具之后） | MCP 本质是给 agent 接外部工具 |
| headers 脱敏 | 明文返回（与 GET /api/config 一致） | 避免两处行为不一致；本地部署场景 |
