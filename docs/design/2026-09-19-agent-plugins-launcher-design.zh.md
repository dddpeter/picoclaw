# PicoClaw Launcher 的 Agent Plugins 管理 — 设计文档

- 日期：2026-09-19
- 状态：草案（待用户批准后出实施计划）
- 目标仓库：`D:\code\picoclaw`
- 前置：`docs/design/2026-09-19-agent-plugins-client.md`（客户端实施计划，已全部落地——14 个功能提交 + 评审小修，`pkg/agentplugins` 纯逻辑包 + `picoclaw plugin` CLI + gateway 桥接）

## 1. 背景

Agent Plugins Spec 1.0 客户端已实现，但**只有 CLI 入口**。launcher（`web/`，独立进程：Go backend + React 前端）对插件零感知，缺失分三层：

1. **无管理功能**：backend 无任何 plugins API（`web/backend/api/` 46 个文件无 plugins 路由），前端无对应页面。安装/启停/移除/诊断只能 SSH/终端跑 `picoclaw plugin ...`。
2. **skills 页看不到插件技能**：launcher backend 的 `api/skills.go:519` 用自己的 `newSkillsLoader`（`skills.NewSkillsLoader` 标准 roots），不含插件段——插件 roots 只在 gateway 进程的 `pkg/agent/context.go newDefaultSkillsLoader` 里追加，两处代码各写一份追加逻辑，launcher 侧干脆没写。
3. **MCP 页看不到插件 server**：`api/mcp.go` 只读写 config.json 的 `Tools.MCP`；插件桥接键 `plugin/<name>/<server>` 只存在于 gateway 进程内存（设计 D1：内存合并不落盘）。

## 2. 目标 / 非目标

**目标**：

1. `/plugins` 页面：列表（名称/版本/来源/组件计数/启用态/诊断）、安装（本地路径 / git URL）、移除（含 purge-data）、启停、不安装的 validate 预检。
2. skills 页可见插件技能（来源标注 `plugin:<name>`，且不可在 skills 页删除插件技能）。
3. MCP 页可见插件桥接的 server（只读段，标注来源，引导去插件页管理）。
4. 状态改动生效语义明确：下轮加载生效，复用既有 restart 提示与 `/api/gateway/restart`。

**非目标**：

- 插件市场/注册中心、自动更新（与客户端设计一致，规范不定义分发）。
- 在 web 上编辑 mcp.json/plugin.json 内容（装包/启停/移除即全部管理动作；内容编辑用文件系统）。
- 跨机管理：launcher backend 与 `~/.agents/plugins` 同机，本设计不引入远程协议。

## 3. 现状盘点（复用面）

| 锚点 | 事实 |
|---|---|
| `pkg/agentplugins` | 纯逻辑、**只 import 标准库**（防 import 环的硬约束）；已提供 `LoadPluginsDir`、`LoadPlugin`、`InstallFromLocal/FromGit`、`Remove`、`LoadRegistry/Registry.SetEnabled/Save`、`RegisterIn`、`DefaultInstallRoot/DefaultDataRoot`、`Report` |
| `web/backend/api/router.go` | 路由集中注册：`register*Routes(mux)` 模式（mcp/skills/tools 等 15 组） |
| `web/backend/api/mcp_test.go` | `doJSON(t, mux, method, path, body)` httptest 助手，可复用 |
| backend 构建 | `make build` = `CGO_ENABLED=0 go build -tags goolm,stdjson`；agentplugins 纯 stdlib，无 cgo/依赖影响 |
| `web/frontend` | TanStack Router 文件路由（`routes/mcp.tsx` 三行壳）+ `components/<x>/-page.tsx` + `use-*-page.ts` + `api/http.ts launcherFetch` + react-query + i18n（zh/en）+ `lib/restart-required.ts` toast 助手 |
| 前端测试设施 | **无**（0 个测试文件）——验证靠 `tsc -b`、`pnpm lint`、构建 + 手动冒烟 |
| 信任边界 | 面板已有认证（dashboardauth）；所有 `/api/*` 同一 mux，新路由自动同权 |

## 4. 总体架构

```text
浏览器 /plugins 页 ──launcherFetch──▶ web/backend api/plugins.go
                                          │
                                          ▼
                                   pkg/agentplugins（复用，零改动为主）
                                   （ScanPlugins※新增 / install / remove / registry）
                                          │
              ┌───────────────────────────┼──────────────────────────┐
              ▼                           ▼                          ▼
   ~/.agents/plugins/<name>/    ~/.agents/plugins/registry.json   GET /api/mcp/config
   （安装根，CLI 共用）          （CLI 与 web 双写，原子 rename）   + pluginServers 只读段

gateway 进程（不变）：启动/重载时 LoadPluginsDir → 内存合并 skills/MCP
生效语义：web 改动 → 提示 restart（POST /api/gateway/restart）→ 下轮加载生效
```

※唯一对 `pkg/agentplugins` 的增量：`ScanPlugins`（见 D2）。其余全部复用，与 CLI 同构。

## 5. 关键设计决策

**D1 管理动作全部复用 `pkg/agentplugins`，web/backend 直接 import**
- 与 CLI 走同一份安装/校验/registry 代码（同构，无双实现漂移）
- 不 import `pkg/agent`（gateway 桥接层重且引依赖环）；MCP 可见性所需的 `MCPServerEntry → DTO` 映射在 `api/plugins.go` 自写（约 15 行，DTO 层本就是转换职责）
- 不 import `pkg/config` 到 agentplugins（保持其 stdlib-only 硬约束）

**D2 `agentplugins` 增量 API：`ScanPlugins`——含失败插件的完整扫描视图**
- 现状 `LoadPluginsDir` 把加载失败的目录折叠进 `report.Warnings`（丢结构）；UI 需要每个插件一行（含加载失败的，显示错误）
- 新增 `ScanPlugins(installRoot, dataRoot) ([]*Plugin, []FailedPlugin)`，`FailedPlugin{Name, Dir, Err}`；`LoadPluginsDir` 改为内部复用 `ScanPlugins`（行为不变，gateway/CLI 零感知）
- 保留字目录（`data`/`registry.json`）跳过逻辑下沉到 ScanPlugins

**D3 skills 可见性：`pkg/skills.AppendPluginRoots` 单一实现，三处共用**
- 新 helper `skills.AppendPluginRoots(roots []SkillRoot) []SkillRoot`：内部调 `agentplugins.LoadPluginsDir`，append `Kind: SkillRootPlugin`、`Source: "plugin:"+name` 的启用插件 roots（pkg/skills 已 import agentplugins，无环）
- 三处接入：`pkg/agent/context.go newDefaultSkillsLoader`（删手写循环，改调 helper——顺手消重）、`web/backend/api/skills.go newSkillsLoader`（新增）、skills API 响应自然带出 `plugin:<name>` 来源
- 删除安全性已内置：`handleDeleteSkill` 对 `skill.Source != "workspace"` 返回 400（“only workspace skills can be deleted”）——插件技能（source=`plugin:<name>`）天然不可从 skills 页删除，无需新增守卫，测试锚定即可

**D4 MCP 可见性：`GET /api/mcp/config` 增 `pluginServers` 只读段**
- backend 每次 GET 时 `LoadPluginsDir` 读启用插件，映射为 `[{key: "plugin/<p>/<s>", plugin, server, type, url, command}]` 附在响应里；**绝不写回 config.json**（维持客户端设计 D1 内存合并原则）
- 前端 MCP 页 servers tab 顶部渲染只读分组（badge "来自插件"），编辑按钮替换为"去插件页管理"链接

**D5 生效语义：下轮加载 + restart 提示，不自动重启**
- install/remove/enable/disable 成功后，前端按 gateway 运行态（`GET /api/gateway/status`）决定 `showSaveSuccessOrRestartToast(restartRequired)`；用户点击已有的 restart 流程
- validate 只读不落盘，无提示

**D6 并发与跨进程**
- `Handler` 增 `pluginsMu sync.Mutex`，串行化 install/remove/enable 的 registry read-modify-write 与安装根写操作（照 `configMu`/`oauthMu` 既有模式）
- CLI 与 web 同时写 registry.json 的跨进程竞争窗口仍存在（原子 rename 兜底单次写完整）；文档注明"重要变更后刷新列表确认"

**D7 安装来源安全 = 认证面板后的显式动作，与 CLI 同信任级**
- web install 调用与 CLI 完全相同的 `InstallFromLocal/FromGit`（含保留名守卫、先校验后拷贝、不覆盖已装）；git clone 在 launcher 进程执行，与 CLI 执行环境一致（客户端设计 D6 信任模型不变）
- `{name}` 路径参数（enable/remove）必须过 `ValidatePluginName` + 保留名拒绝，杜绝路径注入

## 6. API 设计

| 方法/路径 | 语义 | 请求/响应要点 |
|---|---|---|
| `GET /api/plugins` | 列表 | 响应 `{installRoot, plugins: [pluginDTO]}`；pluginDTO：`{name, version, source, ref, installedAt(RFC3339), enabled, registered, loadError?, skills(int), mcpServers(int), warnings[]}`；合并 registry 条目与磁盘扫描（孤儿目录→registered:false 默认启用；registry 有但目录缺失→loadError 标注） |
| `POST /api/plugins/validate` | 不安装预检 | 请求 `{path}`；响应 `{ok, name?, skills, mcpServers, warnings[], error?}` |
| `POST /api/plugins/install` | 安装 | 请求 `{source, ref?}`；本地目录存在→FromLocal，`https://`/`git@` 开头→FromGit；响应同 validate + `target`；失败响应带 error（安装已回滚） |
| `PUT /api/plugins/{name}/enabled` | 启停 | 请求 `{enabled}`；name 过校验；写 registry |
| `DELETE /api/plugins/{name}?purgeData=true` | 移除 | 删安装目录（+可选 data 目录）；同步删 registry 条目 |

错误响应统一走既有 `writeErrorf` 模式（HTTP 4xx/5xx + message）。

## 7. 前端设计

- 路由 `/plugins`（`routes/plugins.tsx` 三行壳，照 `routes/mcp.tsx`）
- `components/plugins/plugins-page.tsx` + `use-plugins-page.ts` + `api/plugins.ts`（types + launcherFetch CRUD）
- 页面结构：
  - PageHeader + 安装按钮（对话框：source 输入 + ref 输入（git 时显示）+ 提交后展示加载报告：skills/MCP 计数、warnings）
  - 插件卡片列表：名称/版本/来源（本地路径或 git URL 截断显示）/ref、skills 与 mcp 计数、enabled Switch（PUT）、诊断 warnings 折叠区、移除按钮（确认对话框 + purge-data 复选）
  - 加载失败的插件（loadError 非空）以置灰卡片 + 错误文案展示（仍可移除）
  - validate 入口：安装对话框内"仅验证"按钮（不落盘）
  - gateway 运行时的改动 → restart toast（复用 `showSaveSuccessOrRestartToast`）
- 侧边栏：Agent 分组内、MCP 项后加 Plugins（`navigation.plugins`）
- i18n：`pages.plugins.*` 全量 zh/en 双语
- skills 页：来源为 `plugin:` 前缀的技能显示插件 badge；删除按钮禁用（`plugin:<name>` 不可删）
- MCP 页：servers tab 顶部只读插件段（见 D4）

## 8. 失败边界

| 场景 | 行为 |
|---|---|
| 安装根缺失 | 列表空、无错误（§6.2 静默，与 gateway 一致） |
| 坏插件目录 | 列表出现、loadError 展示、可移除（ScanPlugins 结构化返回） |
| install 校验失败/拷贝失败 | error 返回；拷贝失败已自动回滚（客户端实现保证） |
| registry 损坏 | 读取容错（缺条目默认启用——沿用 `readEnabledMap` 语义）；列表 warning 提示 |
| validate 路径不存在/非法 | 4xx + message |
| gateway 未运行 | 列表照常（读盘即可）；改动后 toast 只报保存成功，不要求 restart |

## 9. 测试策略

1. **backend（表驱动 httptest，照 mcp_test.go 的 doJSON 模式）**：列表合并视图（registry+孤儿目录+坏插件）、启停、移除（purgeData 两态）、validate 正反例、install 本地黄金插件、install git 来源（`git init --bare` + `file://` 本地仓，规避网络）、`{name}` 路径注入拒绝、mcp config 的 pluginServers 段、skills API 含插件技能且不可删
2. **pkg/agentplugins 增量**：ScanPlugins 单测（成功/失败/保留字目录）
3. **pkg/skills**：AppendPluginRoots 单测（启用/禁用过滤、Kind/Source 正确）
4. **pkg/agent 回归**：context.go 重构后插件桥接测试（TestPluginEndToEnd 等）不破
5. **前端**：无测试设施——门禁 = `tsc -b` + `pnpm lint` + `pnpm build:backend`；每前端任务附手动冒烟清单，最终收口一轮完整冒烟（装→启停→MCP/skills 可见→移除→restart）

## 10. 实施阶段

| 阶段 | 内容 | 出口标准 |
|---|---|---|
| P1 backend | ScanPlugins 增量 + plugins API 五端点 + mcp pluginServers + skills 接入 | `cd web/backend && go test ./...` 全绿；`make lint` 过 |
| P2 前端 | /plugins 页 + 侧边栏 + i18n | tsc/eslint/build 全绿 + 冒烟清单过 |
| P3 可见性收口 | MCP 页只读段 + skills 页 badge（后端已在 P1 备好数据） | 手动冒烟：插件产物在两个页面正确显示/标注 |
| P4 文档 | web/README Dashboard Capabilities、fork-overview.zh.md 增补、根 README 如有插件段落 | 文档与行为一致 |

## 11. 已决问题（2026-09-19）

1. ✅ 复用 `pkg/agentplugins` 而非独立实现（同构 CLI/Web，单一规范实现）
2. ✅ `ScanPlugins` 作为唯一增量 API（`LoadPluginsDir` 内部复用，行为不变）
3. ✅ MCP 可见性走只读 `pluginServers` 段，绝不写 config.json（维持内存合并原则）
4. ✅ skills 可见性 = `AppendPluginRoots` 三处共用（gateway 重构消重 + launcher 接入）
5. ✅ web 安装 = 认证后的显式动作，与 CLI 同信任级（D6 信任模型不变）
6. ✅ 改动生效 = 下轮加载 + restart 提示，不自动重启 gateway

## 12. 风险

1. `git clone --depth 1 file://` 需 git ≥ 2.5（本机满足；测试若 skip 则标注）
2. 前端无测试设施，可见性类回归靠冒烟清单兜底
3. CLI 与 web 跨进程并发写 registry.json 的竞争窗口（原子 rename 保证单次写完整；提示刷新确认）
4. `api/skills.go` 接入后 skills 列表变长——需确认现有前端对未知 source 的渲染不炸（source 字段自由文本，理论安全；冒烟覆盖）
