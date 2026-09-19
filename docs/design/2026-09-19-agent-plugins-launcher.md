# Agent Plugins Launcher 管理 实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让 launcher（web 前后端）具备 Agent Plugins 的管理能力：`/plugins` 页面（列表/安装/启停/移除/预检），skills 页可见插件技能，MCP 页可见插件桥接 server（只读）。

**Architecture:** backend 新增 `web/backend/api/plugins.go`（五端点，全部复用 `pkg/agentplugins`）；`pkg/agentplugins` 增量一个 `ScanPlugins`（含失败插件的扫描视图，`LoadPluginsDir` 内部复用）；skills 可见性由 `pkg/skills.AppendPluginRoots` 统一（gateway `context.go` 与 launcher `api/skills.go` 三处共用）；MCP 可见性为 `GET /api/mcp/config` 只读 `pluginServers` 段（不写 config.json）。前端新增 `/plugins` 路由 + 页面 + i18n，MCP/skills 页加只读展示。

**Tech Stack:** Go 1.25（backend，`CGO_ENABLED=0 -tags goolm,stdjson` 构建不受影响——agentplugins 纯 stdlib）、React 19 + TanStack Router/Query + i18next（zh/en）、pnpm。

**Spec:** 设计文档 `docs/design/2026-09-19-agent-plugins-launcher-design.zh.md`（本计划的上游，先读）；客户端前置 `docs/design/2026-09-19-agent-plugins-client.md`（已落地，只读参考）。

## Global Constraints

- 仓库：`D:\code\picoclaw`；module `github.com/sipeed/picoclaw`
- **硬约束：`pkg/agentplugins` 只 import 标准库**——本计划的增量 API 不得引入 `pkg/config`/`pkg/skills` 依赖；DTO 映射放 `web/backend/api/plugins.go`
- backend 测试：`cd web/backend && go test ./api/ -run <Test>`（httptest + 既有 `doJSON` 助手，照 `mcp_test.go` 模式）；全量 `cd web/backend && go test ./...`
- backend 门禁：`cd web/backend && go vet ./...`；构建验证 `cd web && make build`（CGO_ENABLED=0 + goolm,stdjson tags）
- 前端门禁（无测试设施）：`cd web/frontend && pnpm lint && npx tsc -b --noEmit 2>/dev/null || pnpm build`——以 `pnpm lint` + `pnpm build:backend`（tsc -b && vite build）为准
- 前端风格：`launcherFetch` + react-query；i18n **zh/en 双语同步**（漏 en 会白屏 key）；`pnpm format`（prettier）过的格式
- 后端任务 TDD：先写失败测试 → 最小实现 → 全绿 → commit；前端任务以构建+lint 为门 + 步骤内列手动冒烟点
- commit 主题英文 conventional commits（`feat(web): ...` / `feat(skills): ...`），不 push

---

### Task 1: `agentplugins.ScanPlugins`——含失败插件的扫描视图

**Files:**
- Modify: `pkg/agentplugins/loader.go`
- Test: `pkg/agentplugins/loader_test.go`（增补）

**Interfaces:**
- Produces: `type FailedPlugin struct { Name, Dir string; Err error }`；`func ScanPlugins(installRoot, dataRoot string) ([]*Plugin, []FailedPlugin)`——扫安装根每个子目录（跳过保留字 `data`/`registry.json`），加载失败 → FailedPlugin（含错误），成功 → `*Plugin`（含 `Enabled:false` stub）；registry 语义与现有 `LoadPluginsDir` 完全一致（缺文件/缺条目默认启用）
- Refactor: `LoadPluginsDir` 改为内部调用 `ScanPlugins`，把 FailedPlugin 折叠回 `rep.Warnf`——**对外行为不变**（gateway 桥接与 CLI 零感知）

- [ ] **Step 1: 写失败测试**

用例：
1. 黄金 + 坏目录（缺 plugin.json）并存 → 返回 1 个 `*Plugin` + 1 个 FailedPlugin（Name/Err 非空）
2. 保留字目录（`data/`、文件 `registry.json`）→ 既非 Plugin 也非 FailedPlugin
3. 禁用插件 → `*Plugin{Enabled:false}` 且 Skills/MCPServers 为空（不出现在 Failed）
4. 安装根缺失 → 两者皆空、无 error

- [ ] **Step 2: 跑测试确认失败**

Run: `cd D:\code\picoclaw && go test ./pkg/agentplugins/ -run TestScanPlugins -v`
Expected: FAIL（未定义）

- [ ] **Step 3: 实现 + LoadPluginsDir 重构**

把 `LoadPluginsDir` 的扫描循环提取为 `ScanPlugins`；`LoadPluginsDir` = `ScanPlugins` + Failed→Warnf 折叠。注意 `readEnabledMap`/保留字逻辑下沉，不复制两份。

- [ ] **Step 4: 全包回归（确认 LoadPluginsDir 行为不变）**

Run: `go test ./pkg/agentplugins/...`
Expected: PASS（既有 TestLoadPluginsDir 用例不破）

- [ ] **Step 5: Commit**

```
git add pkg/agentplugins
git commit -m "feat(agentplugins): ScanPlugins exposing failed plugins for UI views"
```

---

### Task 2: backend `GET /api/plugins`——列表合并视图

**Files:**
- Create: `web/backend/api/plugins.go`
- Modify: `web/backend/api/router.go`（注册处加 `h.registerPluginRoutes(mux)`，放 registerMCPRoutes 之后）
- Test: `web/backend/api/plugins_test.go`

**Interfaces:**
- Consumes: Task 1 `ScanPlugins`；`agentplugins.LoadRegistry`、`DefaultInstallRoot/DefaultDataRoot`
- Produces:
  - `func (h *Handler) registerPluginRoutes(mux *http.ServeMux)`（本任务先挂 GET；后续任务补其余方法）
  - `GET /api/plugins` → `{"installRoot": "...", "plugins": [pluginDTO]}`
  - `type pluginDTO struct { Name, Version, Source, Ref string; InstalledAt string(RFC3339,空若无); Enabled, Registered bool; LoadError string(omitempty); Skills, MCPServers int; Warnings []string }`
  - 合并规则：ScanPlugins 结果 ← registry 条目（version/source/ref/installedAt/enabled 覆盖 DTO；`Registered` = registry 有条目）；registry 有条目但目录已消失 → 生成 `LoadError="installed but directory missing"` 的 DTO（可被 remove）；孤儿目录（无条目）→ `Registered:false`，enabled 取 Plugin.Enabled
  - **安装根可注入**：测试经 `h.setPluginsRoots(installRoot, dataRoot)`（新增小方法，生产路径默认 `Default*`）——避免测试触碰真实 `~/.agents/plugins`

- [ ] **Step 1: 写失败测试（照 mcp_test.go 的 doJSON 模式）**

用例：
1. 空安装根 → 200 + `plugins: []` + installRoot 字段
2. 黄金插件 + registry 条目（source/ref/installedAt）→ 字段齐全、counts 正确
3. 坏插件目录 → LoadError 非空、Skills=0
4. 孤儿目录（无 registry）→ `registered:false`、`enabled:true`
5. registry 条目指向不存在的目录 → LoadError="installed but directory missing"
6. 禁用插件 → `enabled:false`

- [ ] **Step 2: 跑测试确认失败**

Run: `cd web/backend && go test ./api/ -run TestPluginsList -v`
Expected: FAIL

- [ ] **Step 3: 实现 plugins.go（DTO + handler + 注册）+ router.go 挂载**

- [ ] **Step 4: 跑测试确认通过 + vet**

Run: `cd web/backend && go test ./api/ -run TestPlugins -v && go vet ./...`
Expected: PASS

- [ ] **Step 5: Commit**

```
git add web/backend/api
git commit -m "feat(web): GET /api/plugins merged registry and scan view"
```

---

### Task 3: backend 启停 + 移除端点

**Files:**
- Modify: `web/backend/api/plugins.go`
- Test: `web/backend/api/plugins_test.go`（增补）

**Interfaces:**
- Consumes: `agentplugins.LoadRegistry/Registry.SetEnabled/Save`、`Remove`、`ValidatePluginName`、保留名集合
- Produces:
  - `PUT /api/plugins/{name}/enabled`，body `{"enabled":bool}` → 200 `{"ok":true}`；name 非法（`ValidatePluginName` 失败或保留字）→ 400；registry 无条目 → 404
  - `DELETE /api/plugins/{name}?purgeData=true|false`（缺省 false）→ 200；删安装目录 + 同步删 registry 条目；目录不存在 → 404；name 非法/保留字 → 400（**防路径注入：`..%2F` 等**）
  - `Handler` 增 `pluginsMu sync.Mutex` 串行化本任务与 Task 4 的写操作（照 `configMu` 模式）

- [ ] **Step 1: 写失败测试**

用例：
1. SetEnabled(false) → 200 → `GET /api/plugins` 反映 false → registry.json 落盘内容正确
2. remove（purgeData=false）→ 目录消失、data 目录保留、registry 条目消失
3. remove（purgeData=true）→ data 目录也消失
4. `{name}` = `..`、`data`、`registry.json`、大写非法名 → 400
5. 未知 name → 404

- [ ] **Step 2: 跑测试确认失败**

Run: `cd web/backend && go test ./api/ -run "TestPluginsEnable|TestPluginsRemove" -v`
Expected: FAIL

- [ ] **Step 3: 实现（mutex + 校验 + registry/目录操作）**

- [ ] **Step 4: 跑测试确认通过**

Run: `cd web/backend && go test ./api/ -run TestPlugins -v`
Expected: PASS

- [ ] **Step 5: Commit**

```
git add web/backend/api
git commit -m "feat(web): plugin enable/disable and remove endpoints with name guards"
```

---

### Task 4: backend install + validate 端点

**Files:**
- Modify: `web/backend/api/plugins.go`
- Test: `web/backend/api/plugins_test.go`（增补）

**Interfaces:**
- Consumes: `agentplugins.InstallFromLocal/InstallFromGit/RegisterIn/LoadPlugin`、`DefaultDataRoot`
- Produces:
  - `POST /api/plugins/validate`，body `{"path":"..."}` → `{ok, name?, skills, mcpServers, warnings[], error?}`（不落盘；复用 CLI validate 的 dataDir 约定：`os.TempDir()/picoclaw-plugin-validate/data`）
  - `POST /api/plugins/install`，body `{"source":"...", "ref":""}` → 源形态判定同 CLI（目录存在→Local；`https://`/`git@`→Git；否则 400）→ 安装 + `RegisterIn` + 装后 `LoadPlugin` 出报告 → `{ok, name, version, target, skills, mcpServers, warnings[], error?}`；安装成功但加载失败 → 200 + `ok:true` + `error` 字段（提示可 remove）——与 CLI 行为一致
  - 与 Task 3 共用 `pluginsMu`

- [ ] **Step 1: 写失败测试**

用例：
1. validate 黄金插件目录 → ok:true + counts；validate 坏插件（name 大写）→ ok:false + error 含原因
2. install 本地黄金插件（`h.setPluginsRoots` 注入临时根）→ ok:true、target 在注入根下、registry 落条目、GET 列表可见
3. install 目标已存在 → error 提示先 remove（不覆盖）
4. install 源既非目录也非 git URL → 400
5. install git 来源：测试内 `git init --bare` + worktree 提交黄金插件 → `source` 用 `file://<bare>` → ok:true（git 不可用或 `--depth 1 file://` 不支持时 `t.Skip` 并注明）
6. validate 路径不存在 → 400

- [ ] **Step 2: 跑测试确认失败**

Run: `cd web/backend && go test ./api/ -run "TestPluginsInstall|TestPluginsValidate" -v`
Expected: FAIL

- [ ] **Step 3: 实现**

- [ ] **Step 4: 跑测试确认通过 + backend 全量**

Run: `cd web/backend && go test ./... && go vet ./...`
Expected: PASS

- [ ] **Step 5: Commit**

```
git add web/backend/api
git commit -m "feat(web): plugin install and validate endpoints over agentplugins"
```

---

### Task 5: backend MCP 可见性——`pluginServers` 只读段

**Files:**
- Modify: `web/backend/api/mcp.go`（`buildMCPConfigResponse` 或新 helper）
- Test: `web/backend/api/mcp_test.go`（增补）

**Interfaces:**
- Consumes: `agentplugins.LoadPluginsDir`（或 Task 1 `ScanPlugins`）、Task 2 的 `setPluginsRoots` 注入
- Produces: `mcpConfigResponse` 增 `PluginServers []pluginServerDTO json:"pluginServers"`：`{key:"plugin/<p>/<s>", plugin, server, type, url(远程), command(stdio, 仅文件名或绝对路径尾段)}`——**只读**，`PUT /api/mcp/config` 忽略该字段（文档注明）；启用插件 only；`MCPServerEntry → DTO` 映射就地写（~15 行，不引 pkg/agent）

- [ ] **Step 1: 写失败测试**

用例：
1. 注入根放黄金插件（1 stdio + 1 streamable-http）→ GET /api/mcp/config 含 2 条 pluginServers，key/type/url 正确
2. 禁用插件 → pluginServers 空
3. PUT /api/mcp/config 带 pluginServers 字段 → 不报错且不写进 config.json（回读 config 无 plugin/ 键）

- [ ] **Step 2: 跑测试确认失败 → Step 3: 实现 → Step 4: 全绿**

Run: `cd web/backend && go test ./api/ -run "TestMCP|TestPlugins" -v`
Expected: PASS

- [ ] **Step 5: Commit**

```
git add web/backend/api
git commit -m "feat(web): expose plugin-bridged mcp servers read-only in config response"
```

---

### Task 6: `pkg/skills.AppendPluginRoots` + gateway 重构消重

**Files:**
- Modify: `pkg/skills/loader.go`（新 helper，放 ResolveSkillRoots 附近）
- Modify: `pkg/agent/context.go`（`newDefaultSkillsLoader`：删除手写 LoadPluginsDir 循环，改调 helper）
- Test: `pkg/skills/loader_test.go`（增补）

**Interfaces:**
- Produces: `func AppendPluginRoots(roots []SkillRoot) []SkillRoot`——内部 `agentplugins.LoadPluginsDir(DefaultInstallRoot, DefaultDataRoot)`，每个启用插件 append `SkillRoot{Dir: p.Root, Source: "plugin:"+p.Name, Kind: SkillRootPlugin}`；出错静默返回原 roots（与现 context.go 行为一致）；**注入点**：为可测性提供 `appendPluginRootsWith(roots []SkillRoot, installRoot, dataRoot string) []SkillRoot` 内部变体（导出函数包装之）
- 行为不变式：`pkg/agent` 侧桥接结果与重构前完全一致（顺序：标准 roots 在前、插件在后）

- [ ] **Step 1: 写失败测试（pkg/skills）**

用例：
1. 临时安装根 + 黄金插件 → append 后含 `Kind:"plugin"`、`Source:"plugin:golden"` 的 root
2. 禁用插件 → 不 append
3. 空安装根 → roots 原样返回

- [ ] **Step 2: 跑测试确认失败 → Step 3: 实现 + context.go 重构**

- [ ] **Step 4: 回归（skills + agent 两包）**

Run: `go test ./pkg/skills/... ./pkg/agent/ -run "TestListSkills|TestPlugin" -v && go test ./pkg/skills/...`
Expected: PASS（含既有 TestPluginEndToEndLoadMergeRemove）

- [ ] **Step 5: Commit**

```
git add pkg/skills pkg/agent
git commit -m "refactor(skills): shared AppendPluginRoots helper for gateway and launcher"
```

---

### Task 7: launcher skills API 接入（skills 页可见插件技能）

**Files:**
- Modify: `web/backend/api/skills.go`（`newSkillsLoader`：改用 `ResolveSkillRoots + AppendPluginRoots(内部变体) + NewSkillsLoaderFromRoots`，镜像 context.go 结构）
- Test: `web/backend/api/skills_test.go`（增补）

**Interfaces:**
- Consumes: Task 6 helper
- Produces: skills 列表 API 返回插件技能（`source:"plugin:<name>"`）；删除安全性已内置：`handleDeleteSkill` 对非 workspace 来源返回 400 "only workspace skills can be deleted"，插件技能天然不可删——测试锚定

- [ ] **Step 1: 写失败测试**

用例：
1. 注入安装根 + 黄金插件（skills/alpha）→ GET skills 列表含 alpha、source=`plugin:golden`
2. `DELETE /api/skills/alpha` → 400 "only workspace skills can be deleted"（既有守卫，锚定不变式）
3. 禁用插件 → 列表不含

- [ ] **Step 2: 跑测试确认失败 → Step 3: 实现 → Step 4: backend 全量**

Run: `cd web/backend && go test ./...`
Expected: PASS

- [ ] **Step 5: Commit**

```
git add web/backend/api
git commit -m "feat(web): skills API includes plugin skills with plugin source"
```

---

### Task 8: 前端 `/plugins` 页面

**Files:**
- Create: `web/frontend/src/api/plugins.ts`（types + `fetchPlugins/togglePlugin/removePlugin/installPlugin/validatePlugin`，launcherFetch 封装）
- Create: `web/frontend/src/components/plugins/plugins-page.tsx`
- Create: `web/frontend/src/components/plugins/use-plugins-page.ts`
- Create: `web/frontend/src/routes/plugins.tsx`（三行壳，照 routes/mcp.tsx）
- Test: 无（构建 + lint 为门）——冒烟点列在 Step 5

**Interfaces:**
- Consumes: Task 2-4 的五端点；`lib/restart-required` 的 `showSaveSuccessOrRestartToast`；`GET /api/gateway/status`（判 restartRequired）
- Produces: 页面 = PageHeader + 安装/验证对话框 + 插件卡片列表（enabled Switch、warnings 折叠、移除确认含 purgeData 复选、loadError 置灰卡片）+ 空/加载态 + gateway 离线时不弹 restart 提示

- [ ] **Step 1: api/plugins.ts（types 与后端 DTO 一一对应）**
- [ ] **Step 2: use-plugins-page.ts（react-query：list query + install/enable/remove mutation + invalidate）**
- [ ] **Step 3: plugins-page.tsx + 路由（布局参照 mcp-page.tsx：PageHeader/卡片/对话框组件复用 ui/*）**
- [ ] **Step 4: 构建门禁**

Run: `cd web/frontend && pnpm lint && pnpm build:backend`
Expected: 全绿（tsc -b 零错误）

- [ ] **Step 5: 手动冒烟（dev 后端 + 前端）**

清单：空列表态 → CLI 装一个黄金插件后刷新出现 → enable/disable Switch 生效 → 移除（两种 purgeData）→ validate 预检报告展示 → 坏插件置灰卡片 + loadError。结果记入 commit body。

- [ ] **Step 6: Commit**

```
git add web/frontend/src
git commit -m "feat(web): /plugins page with install, validate, toggle and remove"
```

---

### Task 9: 侧边栏 + i18n（zh/en）

**Files:**
- Modify: `web/frontend/src/components/app-sidebar.tsx`（`baseNavGroups[2]`（skills/tools/mcp 所在组）内、MCP 项后加 Plugins，icon 用 tabler）
- Modify: i18n zh/en locale 文件（`navigation.plugins` + `pages.plugins.*` 全量键：标题/安装对话框/validate/移除确认/toast/空态/来源标签）

**Interfaces:**
- Produces: 双语完整；无裸 key（`pnpm build` 后手查一遍页面无 `pages.plugins.` 字样残留）

- [ ] **Step 1: i18n 键（zh 先行，en 同步；键清单在实现时从 plugins-page.tsx 的 t() 调用反推）**
- [ ] **Step 2: 侧边栏项 + 路由可达**
- [ ] **Step 3: 构建门禁 + 冒烟（切语言各看一遍 /plugins）**

Run: `cd web/frontend && pnpm lint && pnpm build:backend`
Expected: 全绿

- [ ] **Step 4: Commit**

```
git add web/frontend/src
git commit -m "feat(web): plugins navigation and zh/en i18n"
```

---

### Task 10: MCP 页只读插件段 + skills 页插件 badge

**Files:**
- Modify: `web/frontend/src/components/mcp/mcp-page.tsx`（servers tab 顶部：pluginServers 只读分组——badge 标"来自插件 / from plugin"，无编辑/删除按钮，链接跳 `/plugins`）
- Modify: `web/frontend/src/components/agent/skills/*`（来源列表处：`source` 以 `plugin:` 开头的技能显示插件 badge 且删除按钮禁用）
- Modify: i18n（mcp/skills 两页新增键，zh/en）

**Interfaces:**
- Consumes: Task 5 的 pluginServers（mcp config 响应）、Task 7 的 plugin source

- [ ] **Step 1: MCP 页只读段渲染（types.ts 增 pluginServers 字段）**
- [ ] **Step 2: skills 页 badge + 删除禁用**
- [ ] **Step 3: 构建门禁 + 冒烟（装含 MCP server 的插件 → MCP 页显示只读段；skills 页显示 badge）**

Run: `cd web/frontend && pnpm lint && pnpm build:backend`
Expected: 全绿

- [ ] **Step 4: Commit**

```
git add web/frontend/src
git commit -m "feat(web): read-only plugin sections on mcp and skills pages"
```

---

### Task 11: 端到端冒烟 + 文档收尾

**Files:**
- Modify: `web/README.md`（Dashboard Capabilities 增 `/plugins` 条目；架构图不变）
- Modify: `docs/design/fork-overview.zh.md`（功能地图增补 launcher 插件管理一节）
- Test: 无新测试；跑全部既有门禁

**Interfaces:**
- Produces: 文档与行为一致；完整冒烟通过

- [ ] **Step 1: 全量门禁**

Run: `cd web/backend && go test ./... && go vet ./... && cd ../frontend && pnpm lint && pnpm build:backend && cd .. && make build`
Expected: 全绿

- [ ] **Step 2: 完整冒烟（对照设计文档 §9.5 清单）**

装（本地+git）→ 列表/启停 → skills 页 badge → MCP 页只读段 → restart gateway 后聊天会话中插件技能可用（`/skill:` 触发）→ 移除后全消失。结果记入 commit body。

- [ ] **Step 3: 文档更新（README 两处 + fork-overview）**
- [ ] **Step 4: Commit**

```
git add web/README.md docs/design/fork-overview.zh.md
git commit -m "docs: launcher agent plugins management in readme and fork overview"
```

---

## 阶段出口对照（设计文档 §10）

- P1 = Task 1-5 + Task 7（backend 全绿：五端点 + mcp/skills 可见性数据就绪）
- P2 = Task 8-9（`/plugins` 页可用，双语）
- P3 = Task 10（MCP/skills 页只读展示收口）
- P4 = Task 11（端到端冒烟 + 文档）

## 风险与注意

1. **`git clone --depth 1 file://`**：需 git ≥2.5；不可用则测试 `t.Skip` 注明（Task 4 用例 5）
2. **前端无测试设施**：每任务构建门禁 + Step 内冒烟清单；最终 Task 11 收口完整冒烟
3. **`{name}` 路径注入**：Task 3 的 ValidatePluginName + 保留名双守卫是安全底线，测试必须锚定 `..`/`data`/`registry.json`/大写名
4. **跨进程 registry 竞争**：web pluginsMu 只保进程内；CLI 并发写靠原子 rename 兜底——列表页提供手动刷新即可，不做锁协议
5. **i18n 漏键白屏风险**：en/zh 必须同一 commit 内同步；Task 9 Step 3 显式双语冒烟
6. **`LoadPluginsDir` 行为不变式**：Task 1 重构后 gateway（pkg/agent）与 CLI 测试必须全绿才算过
7. **install 成功但加载失败**：返回 `ok:true` + `error` 字段（与 CLI 同语义）——前端需同时渲染成功态与错误提示（Task 8 冒烟点）
