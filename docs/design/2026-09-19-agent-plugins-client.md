# Agent Plugins Spec 1.0 兼容客户端 实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让 PicoClaw 成为 Agent Plugins Specification 1.0.0 的兼容客户端——加载、校验、安装插件包，并把插件的 skills 与 MCP 服务器动态桥接进现有体系。

**Architecture:** 新增独立包 `pkg/agentplugins`（纯逻辑，零 gateway 依赖），负责 manifest/mcp.json 校验、skill 发现、路径遏制、占位符展开与安装注册。加载期由 bridge 将插件产出动态合并进 `SkillsLoader` roots 与 `MCPConfig.Servers`（内存合并，不落盘 config.json）。CLI 挂在 `picoclaw plugin` 子命令下。

**Tech Stack:** Go 1.25（`D:\code\picoclaw`，module `github.com/sipeed/picoclaw`）、标准库 `encoding/json`、`os/exec`、cobra CLI（仓库已有依赖）。

**Spec:** `C:\Users\dddpe\.picoclaw\workspace\docs\design\2026-09-19-agent-plugins-client-design.md`（含规范逐条映射）；规范原文 `C:\Users\dddpe\.picoclaw\workspace\aps_spec100.md`。执行者两份都要读。

## Global Constraints

- 仓库：`D:\code\picoclaw`；所有 go 命令在该目录跑；module 路径 `github.com/sipeed/picoclaw`
- 现有代码风格：表驱动测试；`errors.Join`/`fmt.Errorf("%w")`；中文 commit 主题用 `git commit -F <msgfile>`（Windows cmd 转义坑）
- 运行单包测试：`go test ./pkg/agentplugins/...`；全量回归：`go build ./...` + `go test ./pkg/config/... ./pkg/skills/... ./pkg/agent/...`
- 安装根（用户已拍板）：`~/.agents/plugins`；数据根 `~/.agents/plugins/data`；registry 文件 `~/.agents/plugins/registry.json`
- `$schema` canonical ID（verbatim，§5.2/§7.2.1）：
  - manifest: `https://agent-plugins.org/schemas/1.0.0/plugin.schema.json`
  - mcp: `https://agent-plugins.org/schemas/1.0.0/mcp.schema.json`
- plugin.json 顶层字段白名单（verbatim §5.2）：`$schema, name, version, description, author, homepage, repository, license, keywords, extensions`
- plugin name 规则（§5.5）：1–64 字符，`a-z` `0-9` `-` `.`，首尾字母数字，禁 `--` 与 `..`
- mcp.json 顶层字段白名单（§7.2.1）：`$schema, mcpServers`；传输类型仅 `stdio` / `streamable-http` / `sse`
- 保留 env 键（§9.1）：`PLUGIN_ROOT`、`PLUGIN_DATA`——出现于插件 env 配置即该 server 条目无效
- 占位符（§9.2）：仅 `${PLUGIN_ROOT}` 与 `${PLUGIN_DATA}`；仅展开 `args` 元素、`env` 值、`cwd`；单遍非递归；`command`/`url`/`headers` 不展开
- 禁止运行时联网取 schema（§5.2）
- 每个任务：先写失败测试 → 最小实现 → 全绿 → commit。不跳步。

---

### Task 1: 包骨架与规范常量（spec.go）

**Files:**
- Create: `pkg/agentplugins/spec.go`
- Test: `pkg/agentplugins/spec_test.go`

**Interfaces:**
- Produces: `const SpecVersion = "1.0.0"`；`const ManifestSchemaURL = "https://agent-plugins.org/schemas/1.0.0/plugin.schema.json"`；`const MCPConfigSchemaURL = "https://agent-plugins.org/schemas/1.0.0/mcp.schema.json"`；`func SupportedManifestSchema(u string) bool`（仅精确等于 ManifestSchemaURL 为 true）；`type Report struct { Warnings []string; Fatals []string }`，方法 `(r *Report) Warnf(format string, a ...any)`、`(r *Report) Fatalf(format string, a ...any)`、`(r *Report) OK() bool`

- [ ] **Step 1: 写失败测试**

```go
package agentplugins

import "strings"

func TestSupportedManifestSchema(t *testing.T) {
	if !SupportedManifestSchema(ManifestSchemaURL) {
		t.Fatal("canonical URL must be supported")
	}
	if SupportedManifestSchema("https://agent-plugins.org/schemas/1.1.0/plugin.schema.json") {
		t.Fatal("1.1.0 draft must not be accepted")
	}
}

func TestReportSeverity(t *testing.T) {
	var r Report
	r.Warnf("ignored field %q", "bogus")
	r.Fatalf("manifest invalid: %s", "bad name")
	if r.OK() {
		t.Fatal("report with fatal must not be OK")
	}
	var clean Report
	if !clean.OK() {
		t.Fatal("empty report must be OK")
	}
	if !strings.Contains(r.Fatals[0], "bad name") {
		t.Fatal("fatalf must preserve args")
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `cd /d D:\code\picoclaw && go test ./pkg/agentplugins/ -run TestSupported -v`
Expected: FAIL（未定义）

- [ ] **Step 3: 最小实现**

```go
package agentplugins

import "fmt"

const (
	SpecVersion         = "1.0.0"
	ManifestSchemaURL   = "https://agent-plugins.org/schemas/1.0.0/plugin.schema.json"
	MCPConfigSchemaURL  = "https://agent-plugins.org/schemas/1.0.0/mcp.schema.json"
)

func SupportedManifestSchema(u string) bool { return u == ManifestSchemaURL }

type Report struct {
	Warnings []string
	Fatals   []string
}

func (r *Report) Warnf(format string, a ...any) { r.Warnings = append(r.Warnings, fmt.Sprintf(format, a...)) }
func (r *Report) Fatalf(format string, a ...any) { r.Fatals = append(r.Fatals, fmt.Sprintf(format, a...)) }
func (r *Report) OK() bool { return len(r.Fatals) == 0 }
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./pkg/agentplugins/ -v`
Expected: PASS

- [ ] **Step 5: Commit**

```
git add pkg/agentplugins
git commit -m "feat(agentplugins): spec constants + diagnostic report core"
```

---

### Task 2: plugin name 校验（§5.5）

**Files:**
- Create: `pkg/agentplugins/manifest.go`（name 部分先行）
- Test: `pkg/agentplugins/manifest_test.go`

**Interfaces:**
- Produces: `func ValidatePluginName(name string) error`

- [ ] **Step 1: 写失败测试（正反例 verbatim 抄规范 §5.5）**

```go
package agentplugins

import "testing"

func TestValidatePluginName(t *testing.T) {
	cases := []struct {
		name    string
		wantErr bool
	}{
		{"my-plugin", false}, {"acme.tools", false}, {"lint3r", false}, {"a", false},
		{"My-Plugin", true}, {"-start", true}, {"end-", true}, {".dot", true},
		{"has--double", true}, {"too.many..dots", true}, {"", true},
		{"has_underscore", true}, {"has space", true},
		{strings.Repeat("a", 65), true}, {strings.Repeat("a", 64), false},
	}
	for _, c := range cases {
		err := ValidatePluginName(c.name)
		if (err != nil) != c.wantErr {
			t.Errorf("ValidatePluginName(%q) err=%v wantErr=%v", c.name, err, c.wantErr)
		}
	}
}
```

（`import "strings"` 记得加。）

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./pkg/agentplugins/ -run TestValidatePluginName -v`
Expected: FAIL

- [ ] **Step 3: 最小实现**

```go
var pluginNameRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]*[a-z0-9])?$|^[a-z0-9]$`)

func ValidatePluginName(name string) error {
	if len(name) < 1 || len(name) > 64 {
		return fmt.Errorf("plugin name must be 1-64 chars, got %d", len(name))
	}
	if strings.Contains(name, "--") || strings.Contains(name, "..") {
		return fmt.Errorf("plugin name %q must not contain consecutive -- or ..", name)
	}
	if !pluginNameRe.MatchString(name) {
		return fmt.Errorf("plugin name %q must match [a-z0-9.-], start/end alphanumeric", name)
	}
	return nil
}
```

注意单字符 `"a"` 两条分支都覆盖（正则第二分支）；用 `a.b` 这类含点的用例再自测一轮。

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./pkg/agentplugins/ -run TestValidatePluginName -v`
Expected: PASS

- [ ] **Step 5: Commit**

```
git add pkg/agentplugins
git commit -m "feat(agentplugins): plugin name validation per spec 5.5"
```

---

### Task 3: manifest 解析与封闭校验（§5）

**Files:**
- Modify: `pkg/agentplugins/manifest.go`
- Test: `pkg/agentplugins/manifest_test.go`

**Interfaces:**
- Consumes: Task 1 `Report`、`SupportedManifestSchema`；Task 2 `ValidatePluginName`
- Produces: `type Manifest struct { SchemaURL, Name, Version, Description, License, Homepage, Repository string; Keywords []string; Author *Author; ignoredTopFields []string }`；`type Author struct { Name, Email, URL string }`；`func LoadManifest(root string, r *Report) (*Manifest, error)`——返回非 nil error 表示整插件拒绝（fatal）；warn 进 Report

- [ ] **Step 1: 写失败测试**

用例清单（每个一条 `t.Run`）：
1. 最小合法：`{"$schema":"...1.0.0/plugin.schema.json","name":"hello-plugin"}` → 通过，Name=="hello-plugin"
2. 全字段合法（含 author 三字段、keywords 数组）
3. 缺 `$schema` → error
4. 缺 `name` → error
5. `$schema` 指向 1.1.0 → error（拒绝插件）
6. name 违反 §5.5 → error
7. 未知顶层字段 `{"bogus": true}` → 不 error，但 Report 含 warning
8. `author` 含未知子字段 `{"nick":"x"}` → error（author 对象封闭：仅 name/email/url）
9. `author` 值为 string → error（必须 object）
10. `keywords` 为 string → error（必须 array of string）
11. 非 JSON 文件 → error；`plugin.json` 不存在 → error
12. `extensions` 为 array → 不 error，Report warning（§8.1 非对象时报告并忽略）

测试辅助：`t.TempDir()` 里 `os.WriteFile("plugin.json", []byte(tc.json), 0o644)`，调 `LoadManifest(dir, &r)` 断言。

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./pkg/agentplugins/ -run TestLoadManifest -v`
Expected: FAIL（未定义）

- [ ] **Step 3: 实现**

要点：
- 解析进 `map[string]json.RawMessage` 保留键序无关；先查白名单外键 → `r.Warnf("unknown top-level field %q ignored")` 继续解析其余
- `$schema` 必须存在且 `SupportedManifestSchema` → 否则 `error`
- `name` 存在且 `ValidatePluginName` → 否则 `error`
- `author`：必须是 object；用 `map[string]json.RawMessage` 检查只有 `name/email/url` 三个 string 字段
- `extensions`：非 object 时 `r.Warnf` + 忽略
- version/description/license/homepage/repository：string 或 error；keywords：`[]string` 或 error

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./pkg/agentplugins/ -v`
Expected: PASS

- [ ] **Step 5: Commit**

```
git add pkg/agentplugins
git commit -m "feat(agentplugins): closed manifest validation per spec 5"
```

---

### Task 4: 路径遏制（§4.1）

**Files:**
- Create: `pkg/agentplugins/paths.go`
- Test: `pkg/agentplugins/paths_test.go`

**Interfaces:**
- Produces: `func Contains(root, candidate string) bool`——candidate 解析（含 EvalSymlinks，跟随至最深层存在的祖先）后仍必须在 root 内；`func IsPluginRelative(p string) bool`——`strings.HasPrefix(p, "./")` 且无 `..` 段

- [ ] **Step 1: 写失败测试**

用例：
1. `Contains(root, root+"/sub/file.txt")`（文件真实存在）→ true
2. `Contains(root, root+"/../outside.txt")` → false
3. `Contains(root, "C:\\Windows\\notepad.exe")` → false
4. 符号链接指向 root 外 → false（`os.Symlink`；`t.Skip` if Windows 上无特权——用 `os.Symlink` 返回的 error 判断，Windows 普通用户 symlink 失败则跳过，junction 场景 P4 再补专项）
5. root 自身 → true
6. candidate 不存在但祖先存在，最深层存在祖先在 root 内 → true（对不存在的文件按最近存在祖先判定）
7. `IsPluginRelative("./bin/server")` → true；`IsPluginRelative("../bin")` → false；`IsPluginRelative("data")` → false；`IsPluginRelative("./a/../b")` → false（含 `..` 段）

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./pkg/agentplugins/ -run TestContains -v`
Expected: FAIL

- [ ] **Step 3: 实现**

要点：candidate 逐级向上找最近存在祖先 → `filepath.EvalSymlinks(ancestor)` → 拼回剩余段 → `filepath.Abs` 归一 → 前缀比较（`root` 也做同样归一；Windows 大小写不敏感比较用 `strings.EqualFold` 处理盘符差异——保守做法：归一后 `strings.HasPrefix` 用 `filepath.Clean` + `filepath.ToSlash` 比较）。

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./pkg/agentplugins/ -run "TestContains|TestIsPluginRelative" -v`
Expected: PASS

- [ ] **Step 5: Commit**

```
git add pkg/agentplugins
git commit -m "feat(agentplugins): plugin-root containment per spec 4.1"
```

---

### Task 5: 占位符展开（§9.2）

**Files:**
- Create: `pkg/agentplugins/expand.go`
- Test: `pkg/agentplugins/expand_test.go`

**Interfaces:**
- Consumes: 无
- Produces: `type Vars struct{ Root, Data string }`；`func Expand(s string, v Vars) string`；`func ExpandArgs(args []string, v Vars) []string`；`func ExpandEnvValues(env map[string]string, v Vars) (map[string]string, error)`（键含保留字即 error）

- [ ] **Step 1: 写失败测试**

用例：
1. `${PLUGIN_ROOT}/config.json` + v{Root:`C:\p`,Data:`C:\d`} → `C:\p/config.json`
2. `${PLUGIN_DATA}/x` → 数据目录替换
3. `${PLUGIN_ROOT}${PLUGIN_ROOT}` → 两次都替换
4. 替换产物不再扫描：把 Root 设为 `${PLUGIN_DATA}` 字面值（用故意构造的 Vars{Root:"${PLUGIN_DATA}"}) → 输出含 `${PLUGIN_DATA}` 不再展开
5. `${UNKNOWN_VAR}` 保持字面量
6. `${PLUGIN_ROOT`（残缺）保持字面量
7. `command` 不展开由调用方保证（本函数不处理）——仅注释性用例
8. `ExpandEnvValues`：键 `PLUGIN_ROOT` → error；键 `plugin_root`（大小写变体）→ error（平台环境名语义，保守按大小写不敏感拦）
9. 空串 → 空串

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./pkg/agentplugins/ -run TestExpand -v`
Expected: FAIL

- [ ] **Step 3: 实现**

`strings.ReplaceAll(s, "${PLUGIN_ROOT}", v.Root)` 后接 `strings.ReplaceAll(s, "${PLUGIN_DATA}", v.Data)`——注意：单遍要求"替换产物不再扫描"，两次 ReplaceAll 的顺序语义要先写测试用例 4 锁死（Root 值含 `${PLUGIN_DATA}` 字面量时，先替换 ROOT 再扫描 DATA 会二次展开——**必须用单遍扫描器**：`strings.Builder` + `strings.Index` 找 `${`，逐段拷贝）。实现用单遍，勿用两次 ReplaceAll。

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./pkg/agentplugins/ -run TestExpand -v`
Expected: PASS

- [ ] **Step 5: Commit**

```
git add pkg/agentplugins
git commit -m "feat(agentplugins): single-pass placeholder expansion per spec 9.2"
```

---

### Task 6: mcp.json 校验（§7.2）

**Files:**
- Create: `pkg/agentplugins/mcpconfig.go`
- Test: `pkg/agentplugins/mcpconfig_test.go`

**Interfaces:**
- Consumes: Task 1 `Report`；Task 4 `Contains`/`IsPluginRelative`；Task 5 `Vars`
- Produces: `type MCPServerEntry struct { Name, Type, Command, URL string; Args []string; Env map[string]string; Headers map[string]string; CWD string }`；`func LoadMCPConfig(root, manifestSchemaURL string, v Vars, r *Report) (map[string]MCPServerEntry, error)`——error 表示该插件 MCP 整体禁用（含版本不一致：与 manifest `$schema` 版本不同 → error，由 Task 8 转 warning+禁 MCP 继续加载）。报告分工：**文件级失败**（缺失/坏 JSON/`$schema` 问题/顶层违规）只返回 error、**不写 Report**（由 Task 8 统一转 warning，避免双报）；**条目级**问题 Warnf + 从返回 map 剔除。返回条目的规范化（§7.2.1 MUST）：占位符已展开（args/env 值/cwd）；`./` 形态 `command` 已解析为**绝对路径**（`filepath.Join(root, command)` 后 `Contains` 复检，逃逸即条目剔除）；裸名 `command` 与 `url`/`headers` 原样未展开；**缺省 `cwd` 已填充为解析后的插件根**

- [ ] **Step 1: 写失败测试**

用例清单：
1. 缺失 `mcp.json` → `(nil, nil)`：无 error、无 warning、返回 nil map（规范 §6.2 静默）
2. 空对象 `{"$schema":...,"mcpServers":{}}` → 空 map，无 warning
3. 合法 stdio：command `./bin/validator`、args 含 `${PLUGIN_DATA}/x`、cwd `${PLUGIN_ROOT}` → 展开生效，CWD==v.Root
4. stdio 裸名 command `npx` → 合法，且 `cwd` 未配置时返回条目 CWD == v.Root（缺省 cwd=插件根，§7.2.1 MUST）
5. stdio command `../bin/server` → 条目剔除 + warning
6. stdio command 含空格 `node server.js` → 条目剔除（单 token）
7. cwd 为 `data`（非 `./`、非占位符）→ 条目剔除
8. cwd `${OTHER}/x` → 条目剔除
9. env 键 `PLUGIN_ROOT` → 条目剔除
10. streamable-http 合法：`https://a.com/mcp` + headers
11. url `http://a.com/mcp`（非回环 http）→ 条目剔除
12. url `http://localhost:8080/mcp` → 合法（回环例外）
13. url `http://127.0.0.1:9000/x` → 合法（回环 IP 字面量）
14. url 含 userinfo `https://u:p@a.com/mcp` → 剔除
15. url 含 fragment `https://a.com/mcp#frag` → 剔除
16. headers 大小写冲突 `{"X-Tenant":"a","x-tenant":"b"}` → 条目剔除
17. `sse` 合法用例 → 合法
18. `$schema` 版本与 manifest 不一致（mcp.json 用 1.1.0）→ 整体 error
19. 坏 JSON → 整体 error（本函数不 Warnf；warning 由 Task 8 统一转出，防双报）
20. 未知顶层字段 → 整体 error（mcp.json 顶层封闭：仅 `$schema`+`mcpServers`）
21. 未知 type `websocket` → 条目剔除
22. `type` 缺失 → 条目剔除
23. `mcpServers` 缺失 → 整体 error（required 字段，§7.2.1）
24. `$schema` 缺失 → 整体 error（required 字段，§7.2.1）
25. stdio command `./bin/server` → 返回条目 Command 为绝对路径（== root 拼接结果）
26. stdio command `./escape` 经符号链接指向 root 外 → 条目剔除 + warning（`./` 解析后 Contains 复检；Windows symlink 无特权时 `t.Skip`）

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./pkg/agentplugins/ -run TestLoadMCPConfig -v`
Expected: FAIL

- [ ] **Step 3: 实现**

要点：
- 顶层 `map[string]json.RawMessage`：只允许 `$schema`/`mcpServers`，多余 → `error`
- `$schema` 必须精确等于 MCPConfigSchemaURL，且**与 manifest 版本一致性**在此函数比对：`manifestSchemaURL` 参数与 mcp `$schema` 的版本段（URL 中的 `schemas/<ver>/`）不同 → `error`（语义 = 该插件 MCP 禁用；Task 8 捕获此 error 转 warning + 继续加载 skills）
- stdio：`command` 校验单 token（不含空白、不为空）；`./` 开头 → `filepath.Join(root, cmd)` 得绝对路径再 `Contains` 复检（通过则条目 Command 存绝对路径，逃逸剔除），裸名原样；`cwd` 三形态校验后 `Contains` 复检；`cwd` 缺省 → 填充为解析后的插件根（v.Root）
- 远程：`url.Parse` 检查 scheme/ host 回环集合 = {`localhost`} ∪ {`127.0.0.0/8`、`::1` 字面量}（用 `net.ParseIP`+`IsLoopback`）；userinfo/fragment 检查；headers 名 lowercase 去重
- 每个失败分支：`r.Warnf` + 从 map 剔除该条，继续下一条

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./pkg/agentplugins/ -run TestLoadMCPConfig -v`
Expected: PASS

- [ ] **Step 5: Commit**

```
git add pkg/agentplugins
git commit -m "feat(agentplugins): mcp.json closed validation per spec 7.2"
```

---

### Task 7: 插件 skill 严格发现（§7.1）

**Files:**
- Create: `pkg/agentplugins/skills.go`
- Test: `pkg/agentplugins/skills_test.go`

**Interfaces:**
- Consumes: Task 4 `Contains`
- Produces: `type PluginSkill struct { Name, Dir string }`；`func DiscoverSkills(root string, r *Report) []PluginSkill`——仅 `root/skills/*/SKILL.md` 顶层子目录（不递归）；SKILL.md 不是常规文件 → 跳过+warning；目录内 frontmatter `name` 与目录名不一致或违反 picoclaw skill 名规则 → 跳过+warning

- [ ] **Step 1: 写失败测试**

用例：
1. `skills/a/SKILL.md`、`skills/b/SKILL.md` 都存在 → 2 个，名字 a、b
2. `skills/a/nested/SKILL.md`（嵌套）→ **不**发现 nested（平铺约束）
3. `skills/a` 无 SKILL.md → 跳过，warning
4. `SKILL.md` 是目录 → 跳过+warning
5. `skills/` 不存在 → 返回空，无 warning（§6.2）
6. `skills` 是文件 → 返回空 + warning（§6.2 "不解析为目录即类型无效"）
7. SKILL.md frontmatter `name: a` 与目录名 `b` 不一致 → 跳过+warning
8. frontmatter 合法 → Name 取目录名

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./pkg/agentplugins/ -run TestDiscoverSkills -v`
Expected: FAIL

- [ ] **Step 3: 实现**

要点：`os.ReadDir(filepath.Join(root, "skills"))` → 每个子目录检查 `SKILL.md` 是否 regular file（`entry.Type()==0` 且非 dir；用 `stat` 复核）→ 简易 frontmatter 解析（读首 4KB，`---` 包裹的 `name:` 行）→ 校验目录名本身过 picoclaw `skills` 包的名字规则可简化为复用本包 `ValidatePluginName` 的字符集思路——**决定：skill 目录名规则 = `^[a-zA-Z0-9]+(-[a-zA-Z0-9]+)*$`**（与 picoclaw 现有 skill name 校验一致，保证桥接后不被主 loader 拒掉），内嵌同款正则。

> **宿主加严声明（非规范要求）**：frontmatter `name` 必须等于目录名是 PicoClaw 的 D2 寻址策略，不是 Agent Skills 规范的要求——规范合法但 name 不一致的 skill 会被本客户端跳过。此限制必须在 Task 14 的 `docs/agent-plugins.md` 中显式标注为宿主策略，不得表述为规范要求。

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./pkg/agentplugins/ -run TestDiscoverSkills -v`
Expected: PASS

- [ ] **Step 5: Commit**

```
git add pkg/agentplugins
git commit -m "feat(agentplugins): flat skill discovery per spec 7.1"
```

---

### Task 8: 插件根加载器（组装 manifest+skills+mcp+遏制+版本一致性）

**Files:**
- Create: `pkg/agentplugins/loader.go`
- Test: `pkg/agentplugins/loader_test.go`

**Interfaces:**
- Consumes: Task 3 `LoadManifest`；Task 6 `LoadMCPConfig`；Task 7 `DiscoverSkills`；Task 5 `Vars`
- Produces: `type Plugin struct { Name, Version, Root string; Manifest *Manifest; Skills []PluginSkill; MCPServers map[string]MCPServerEntry; Report *Report; Enabled bool }`；`func LoadPlugin(root string, dataDir string, enabled bool) (*Plugin, error)`——error = 整插件拒绝；dataDir = `<dataRoot>/<插件名>`（由调用方拼好传入）。`func LoadPluginsDir(installRoot, dataRoot string) ([]*Plugin, *Report)`——扫安装根所有子目录，坏目录跳过并进 Report。**registry 语义**：隐式读取 `<installRoot>/registry.json`；registry 文件缺失、或插件目录无对应条目 → 默认 `Enabled:true`（安装即启用）；条目 `enabled:false` → 返回 `Plugin{Enabled:false}` 且**不发现组件**。安装根本身缺失 → 空 list + nil error（静默，类比 §6.2）

- [ ] **Step 1: 写失败测试**

用例：
1. 黄金最小插件 → Plugin.Name/Skills/MCPServers 正确
2. manifest fatal（name 大写）→ LoadPlugin error
3. mcp.json `$schema` 与 manifest 版本不一致 → LoadPlugin **不** error（规范 §7.2.2 第 2 条：禁用该插件 MCP 但继续加载其他组件）；断言：Report 含 warning、Plugin.MCPServers 为空、Skills 正常
4. `plugin.json` 路径经符号链接逃逸出根 → error（Task 4 Contains 拦 manifest 路径本身）
5. LoadPluginsDir：一个黄金 + 一个坏目录 + 一个非目录文件 → 返回 1 个 Plugin，Report 记录坏目录
6. `enabled=false` → 组件不加载（Skills/MCPServers 为空），Enabled false

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./pkg/agentplugins/ -run "TestLoadPlugin|TestLoadPluginsDir" -v`
Expected: FAIL

- [ ] **Step 3: 实现**

流程：`Contains(root, filepath.Join(root,"plugin.json"))` → `LoadManifest` → enabled 则 `DiscoverSkills` + `LoadMCPConfig(root, m.SchemaURL, Vars{Root:rootResolved, Data:dataDir}, rep)` → 组装。

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./pkg/agentplugins/...`
Expected: PASS 全绿

- [ ] **Step 5: Commit**

```
git add pkg/agentplugins
git commit -m "feat(agentplugins): plugin root loader composing spec checks"
```

---

### Task 9: P1 收口——官方 schema 交叉验证（test-only）

**Files:**
- Create: `pkg/agentplugins/conformance_test.go`
- Create: `pkg/agentplugins/testdata/golden/`（minimal、full 两个黄金插件目录）

**Interfaces:**
- Consumes: Task 3/6 的加载函数；官方 schema（curl 到 `pkg/agentplugins/testdata/schemas/` 下两个 JSON，提交进仓库）
- Produces: 黄金用例通过手写校验器 + 官方 JSON Schema 双重验证

- [ ] **Step 1: 下载官方 schema 进 testdata**

Run: `curl.exe -sL https://raw.githubusercontent.com/agentplugins/agent-plugins-spec/main/schemas/1.0.0/plugin.schema.json -o pkg/agentplugins/testdata/schemas/plugin.schema.json`（同法 mcp.schema.json）
Expected: 两个文件存在且非空

- [ ] **Step 2: 写交叉验证测试**

用 `github.com/google/jsonschema-go`（go.mod 已有依赖，`require github.com/google/jsonschema-go v0.4.3`）把两个黄金 plugin.json/mcp.json 跑 schema 校验，断言与手写校验器结论一致（双绿）。仅此一个测试文件 import 该库。

- [ ] **Step 3: 跑测试确认通过**

Run: `go test ./pkg/agentplugins/ -run TestGoldenAgainstOfficialSchema -v`
Expected: PASS

- [ ] **Step 4: 全包回归**

Run: `go build ./... && go test ./pkg/agentplugins/...`
Expected: 全绿

- [ ] **Step 5: Commit**

```
git add pkg/agentplugins
git commit -m "test(agentplugins): golden plugins cross-validated against official schemas"
```

---

### Task 10: registry.json 与安装/卸载逻辑

**Files:**
- Create: `pkg/agentplugins/registry.go`
- Create: `pkg/agentplugins/install.go`
- Test: `pkg/agentplugins/registry_test.go`

**Interfaces:**
- Consumes: Task 8 `LoadPlugin`
- Produces:
  - `type Registry struct { Path string; Entries map[string]RegistryEntry }`
  - `type RegistryEntry struct { Name, Version, Source, Ref string; InstalledAt time.Time; Enabled bool }`
  - `func LoadRegistry(path string) (*Registry, error)`（文件不存在 → 空 registry，nil error）
  - `func (r *Registry) Save() error`
  - `func DefaultInstallRoot() (string, error)` → `~/.agents/plugins`；`func DefaultDataRoot() (string, error)` → `~/.agents/plugins/data`
  - `func InstallFromLocal(src string, installRoot string) (string, error)`——copy 目录树到 `<installRoot>/<manifest name>`；目标已存在 → error（报"先执行 plugin remove"，**不自动覆盖**）；**保留名守卫**：manifest name 为 `data` 或 `registry.json`（或目标路径与数据根/registry 文件冲突）→ error 拒绝安装——两者都是 §5.5 合法插件名，会撞安装根特殊路径
  - `func InstallFromGit(gitURL string, ref string, installRoot string) (string, error)`——`git clone --depth 1`（ref 非空加 `--branch <ref>`；**ref 仅支持分支/tag**，SHA 不支持，CLI 帮助文本注明）到临时目录 → 校验 manifest（含保留名守卫）→ copy
  - `func Remove(name string, installRoot string, purgeData bool) error`
  - `func (r *Registry) SetEnabled(name string, enabled bool) error`

- [ ] **Step 1: 写失败测试**

用例（全部在 `t.TempDir()`）：
1. 空 registry 加载 + Save + 重读 → roundtrip 一致
2. InstallFromLocal：源目录造黄金插件 → 安装后 `<root>/<name>/plugin.json` 存在，LoadPlugin 通过
3. InstallFromLocal 目标已存在 → error
4. Remove 后目录消失；purgeData=true 时 data 目录也消失
5. SetEnabled(false) → Save → LoadRegistry 读回 false
6. 源目录没有 plugin.json → InstallFromLocal error（先校验后拷贝）
7. manifest name 为 `data` → error（保留名守卫：撞数据根）
8. manifest name 为 `registry.json` → error（保留名守卫：撞 registry 文件）

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./pkg/agentplugins/ -run "TestRegistry|TestInstall|TestRemove" -v`
Expected: FAIL

- [ ] **Step 3: 实现**

要点：copy 用 `io/fs.WalkDir` + `os.MkdirAll`/`os.CopyFS`（Go 1.23+ 有 `os.CopyFS`，直接用）；git clone 用 `exec.Command("git", ...)`，Windows 下 PATH 已有 git。

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./pkg/agentplugins/ -v`
Expected: PASS

- [ ] **Step 5: Commit**

```
git add pkg/agentplugins
git commit -m "feat(agentplugins): registry + local/git install/remove"
```

---

### Task 11: CLI 子命令 `picoclaw plugin`

**Files:**
- Create: `cmd/picoclaw/internal/plugin/command.go`（root：`NewPluginCommand()`）
- Create: `cmd/picoclaw/internal/plugin/install.go`
- Create: `cmd/picoclaw/internal/plugin/remove.go`
- Create: `cmd/picoclaw/internal/plugin/list.go`
- Create: `cmd/picoclaw/internal/plugin/enable.go`（enable/disable 合一文件，两个函数）
- Create: `cmd/picoclaw/internal/plugin/validate.go`
- Modify: `cmd/picoclaw/main.go:131-146`（AddCommand 列表加 `plugin.NewPluginCommand()`）
- Test: `cmd/picoclaw/internal/plugin/command_test.go`

**Interfaces:**
- Consumes: Task 8 `LoadPlugin/LoadPluginsDir`；Task 10 registry/install 函数
- Produces: cobra 命令树 `picoclaw plugin {install,remove,list,enable,disable,validate,info}`；`info` 若时间紧可并入 `list -v`（P2 出口允许）

- [ ] **Step 1: 参照现有命令风格写 root**

读 `cmd/picoclaw/internal/skills/command.go` 与 `internal/mcp/command.go` 照抄结构（Use/Short/加子命令方式）。root Use=`plugin`，Short=`Manage Agent Plugins Spec 1.0 packages`。

- [ ] **Step 2: 写失败测试（validate 命令为锚点）**

`command_test.go`：造临时黄金插件 → 执行 `validate <path>` → exit 0 + stdout 含 "OK"；造坏插件（name 大写）→ 命令返回 error 且 stderr 含原因。测试模式参照 `cmd/picoclaw/internal/skills/command_test.go` 的既有写法（读一遍再写）。

- [ ] **Step 3: 跑测试确认失败**

Run: `go test ./cmd/picoclaw/internal/plugin/ -v`
Expected: FAIL

- [ ] **Step 4: 实现六个命令 + main.go 挂载**

- `install <path|git-url> [--ref]`：判断 source 形态（本地路径存在→InstallFromLocal；`https://`/`git@`→InstallFromGit）→ 注册 registry → 打印校验报告（warnings/fatals）
- `remove <name> [--purge-data]`
- `list`：LoadPluginsDir + registry enabled 状态 → 表格输出 name/version/skills/MCP/enabled
- `enable|disable <name>`：registry.SetEnabled + Save
- `validate <path>`：LoadPlugin，OK 打印 `OK: <name> (<N> skills, <M> mcp servers)`；fatal 逐条打印，exit 1

- [ ] **Step 5: 跑测试确认通过 + 全量回归**

Run: `go build ./... && go test ./cmd/picoclaw/... ./pkg/agentplugins/...`
Expected: PASS

- [ ] **Step 6: Commit**

```
git add cmd/picoclaw pkg/agentplugins
git commit -m "feat(cli): picoclaw plugin install/remove/list/enable/disable/validate"
```

---

### Task 12: 桥接——skills 并入 SkillsLoader

**Files:**
- Modify: `pkg/skills/loader.go`：`SkillRoot` 加 `Kind string` 字段（`"standard"`|`"plugin"`，空串视同 standard 以保兼容）；`NewSkillsLoaderFromRoots` 不变；`ListSkills` 对 `Kind=="plugin"` 的 root **委托 `agentplugins.DiscoverSkills(root.Dir, rep)` 做平铺发现**（`pkg/skills` import `agentplugins`——规范 §7.1 只留这一份实现，防双实现漂移；rep 的 warnings 逐条 `slog.Warn`，与 loader 现有告警风格一致）。禁用插件的过滤**不在 loader 做**（`pkg/skills` 无从得知 enabled）——由 context.go 接线侧只 append 启用插件保证
- Modify: `pkg/agent/context.go:128-136`（`skillsDirCandidates` 或 NewSkillsLoader 调用处）：构建 roots 时追加插件段——`agentplugins.LoadPluginsDir(defaultInstallRoot, defaultDataRoot)` → 每个启用插件 append `SkillRoot{Dir: p.Root, Source: "plugin:"+p.Name, Kind: "plugin"}`
- Test: `pkg/skills/loader_test.go` 增补 plugin-kind root 用例；`pkg/agent/` 集成用例（若已有 loader 构建测试则扩展之）

**Interfaces:**
- Consumes: `pkg/agentplugins.LoadPluginsDir`
- Produces: 插件 skills 出现在 `ListSkills()`，Source 形如 `plugin:<name>`；同名冲突按 root 顺序（workspace/global/builtin 在前，plugins 段在后 = 插件永远输给用户自有；插件间按安装根扫描的字典序，先扫到者赢，后者被跳过——与设计文档 D2 一致）

- [ ] **Step 1: 写失败测试（pkg/skills/loader_test.go 增补）**

```go
func TestListSkills_PluginKindFlatDiscovery(t *testing.T) {
	// root 布局：<tmp>/plug/skills/alpha/SKILL.md + <tmp>/plug/skills/alpha/deep/SKILL.md
	// 断言：发现 alpha；不发现 deep（平铺）
	// 再放一个 standard root 同名 alpha → standard 赢（顺序在前）
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./pkg/skills/ -run TestListSkills_PluginKind -v`
Expected: FAIL

- [ ] **Step 3: 实现 loader.Kind 分支**

`discoverSkillDirs` 拆两路径：`Kind=="plugin"` 时改调 `agentplugins.DiscoverSkills(root.Dir, rep)`（平铺一层，规范语义只在 agentplugins 维护一份），rep 的 warnings 逐条 `slog.Warn`；standard 原逻辑不动。

- [ ] **Step 4: 跑测试确认通过 + 既有 skills 测试回归**

Run: `go test ./pkg/skills/...`
Expected: PASS（原有用例不破）

- [ ] **Step 5: context.go 接线 + 编译回归**

`pkg/agent/context.go` 构建处追加插件 roots；`go build ./...` 确认无环（`pkg/agent` → `pkg/agentplugins` 单向，agentplugins 不 import agent/skills——**约束：agentplugins 只 import 标准库**，桥接由调用方完成，保住零依赖设计）。

- [ ] **Step 6: Commit**

```
git add pkg/skills pkg/agent
git commit -m "feat(skills): plugin-kind roots bridge agent plugins into skill loader"
```

---

### Task 13: 桥接——MCP 合并进 agent 初始化

**Files:**
- Modify: `pkg/agent/agent_mcp.go:78-96`（`ensureMCPInitialized`）：**抽取独立函数 `mergePluginServers(cfg *config.MCPConfig) *config.MCPConfig`**（可单测）：调用 `agentplugins.LoadPluginsDir`，把启用插件的 `MCPServers` 以键 `plugin/<plugin>/<server>` 合并进副本（不回写 cfg；已有同名键（用户手配）不覆盖）。**顺序约束：合并必须先于现有空判**——当前 `al.cfg.Tools.MCP.Servers == nil || len == 0` 的早退（82-85 行）改用合并后配置判断，否则"用户零 MCP 配置 + 插件有 server"会静默丢全部插件 server；之后合并结果再走既有 `filterMCPConfigServers` 流程。**启动前置动作**：对每个含 stdio server 的启用插件 `os.MkdirAll(dataDir, 0o755)`（§9.1 MUST create before launch）。每个条目映射为 `config.MCPServerConfig{Enabled:true, Type:entry.Type, Command:entry.Command, Args:entry.Args, Env:entry.Env, URL:entry.URL, Headers:entry.Headers, Dir:entry.CWD}`——`Dir` 无条件设置（`LoadMCPConfig` 已保证 stdio 条目 CWD 恒非空：显式值或缺省插件根，§7.2.1）
- Modify: `pkg/mcp/manager.go:404-445`（stdio 分支）：`cmd.Dir` 目前未设置——补 `cmd.Dir = cfg.Dir`（桥接保证 stdio 插件条目 Dir 恒非空；普通用户配置 Dir 为空串时行为与现状一致）；`MCPServerConfig` 增加 `Dir string json:"dir,omitempty"` 字段（插件桥接写入解析后的绝对 cwd）；env 注入末尾追加 `PLUGIN_ROOT`/`PLUGIN_DATA`（来自合并条目携带的 Vars——实现：`MCPServerConfig` 增加非序列化字段 `PluginRoot`/`PluginData`（`json:"-"`，注释标明仅供插件桥接的隐藏通道），桥接时填，manager 在应用配置 env 之后最后写这两个键，覆盖任何同名残留）
- Test: `pkg/agent/agent_mcp_test.go`（或新增 `agent_plugin_mcp_test.go`）：临时插件（含一个 stdio server + 一个 streamable-http server）→ 初始化后工具可用/连接尝试被发起；`plugin/` 前缀键存在；用户配置同名服务器不被覆盖

**Interfaces:**
- Consumes: `agentplugins.LoadPluginsDir`、`MCPServerEntry`
- Produces: 插件 MCP 服务器与原生配置同权参与启动；`PLUGIN_ROOT`/`PLUGIN_DATA` 注入顺序在配置 env 之后（规范 §9.1），伪造的这两个键被覆盖；`PLUGIN_DATA` 目录在启动前已创建（§9.1）

- [ ] **Step 1: 写失败测试（锚在 `mergePluginServers` 与 manager 注入，不戳内部状态）**

用例：
1. 临时安装根放一个黄金插件（mcp.json：`{"echo":{"type":"stdio","command":"node","args":["-e","process.exit(0)"]}}`）→ `mergePluginServers` 返回的 map 含 `plugin/<name>/echo`，`Dir`/`PluginRoot`/`PluginData` 已填（Dir==插件根——缺省 cwd 填充的验证点）
2. **空配置 + 插件有 server**：`cfg.Tools.MCP.Servers` 为 nil → `mergePluginServers` 结果非空，`ensureMCPInitialized` 不再走 "no servers configured" 早退
3. **用户同名键不被覆盖**：用户配置里已有键 `plugin/foo/echo` → 用户值赢（插件合并不覆盖已有键）
4. **allowlist 交互**：agent 的 `mcpServers` 允许名单非空且不含 `plugin/<name>/echo` → 被过滤；名单含之 → 通过（现有 `filterMCPConfigServers` 语义，加测试锚定并写进文档"配了 allowlist 的 agent 需显式放行插件 server"）
5. **data 目录创建**：`mergePluginServers`（或紧随其后的启动前置）后，`<dataRoot>/<name>` 目录存在
6. 工具名冒烟：含 `/` 的服务器键 `plugin/<name>/echo` 注册出的工具全名无 panic/空段（人工核对一次命名格式即可）

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./pkg/agent/ -run TestPluginMCP -v`
Expected: FAIL

- [ ] **Step 3: 实现（按 Files 描述的三处改动）**

- [ ] **Step 4: 跑测试确认通过 + 回归**

Run: `go test ./pkg/agent/... ./pkg/mcp/... ./pkg/config/...`
Expected: PASS

- [ ] **Step 5: Commit**

```
git add pkg/agent pkg/mcp pkg/config
git commit -m "feat(mcp): bridge plugin mcp.json servers with PLUGIN_ROOT/DATA injection"
```

---

### Task 14: 一致性矩阵 + 文档收尾（P4）

**Files:**
- Create: `docs/agent-plugins.md`（仓库文档：功能介绍、CLI 用法、conformance 状态表）
- Create: `docs/agent-plugins-conformance.md`——Appendix A 检查清单逐项 → 测试函数名映射表
- Test: 无新测试（本任务是把已有测试映射成矩阵；发现缺口则回补测试并计入本任务 commit）

**Interfaces:**
- Consumes: 全部前序测试
- Produces: 可对外宣称的 conformance 文档

- [ ] **Step 1: 通读 Appendix A（规范本地副本 workspace\aps_spec100.md 末尾），逐项填表**

每行：检查项 | 对应测试文件::函数 | 状态。发现无测试背书的项 → 回补一个测试或显式标注。已知专项在此收口（不再是隐式 catch-all）：① Windows `junction` 逃逸遏制（Task 4 symlink 用例的平台补全）；② `.bat/.cmd` command 经解释器启动且保持单 token；③ 其他 N/A 项标注格式 "N/A (client launches via cmd /c on Windows)"。

- [ ] **Step 2: 端到端集成冒烟（对应设计文档 §9.4）**

临时安装根 install 黄金插件 → 新会话 `ListSkills()` 含插件 skill、`mergePluginServers` 后 MCP 初始化含插件 server → `plugin remove` 后新会话两者消失。（gateway 层手动冒烟可，自动测试至少覆盖 loader+merge 链路。）

- [ ] **Step 3: 写 docs/agent-plugins.md**

内容：是什么、用户怎么用（install/validate 示例）、插件作者怎么写包、限制（不支持的组件类型、1.1.0 草案未支持、**frontmatter name==目录名是 PicoClaw 宿主策略**、**agent allowlist 非空时需显式放行 `plugin/<p>/<s>`**）。顺手同步上游设计文档 §4 文件布局与本计划的漂移（bridge.go/report.go → 实际拆分：Report 在 spec.go、无 bridge.go，桥接在 pkg/agent）。

- [ ] **Step 4: 全量回归**

Run: `go build ./... && go test ./...`（至少 `./pkg/agentplugins/... ./pkg/skills/... ./pkg/agent/... ./pkg/mcp/... ./pkg/config/... ./cmd/...`）
Expected: PASS

- [ ] **Step 5: Commit**

```
git add docs
git commit -m "docs: agent plugins client conformance matrix and user guide"
```

---

## 阶段出口对照（设计文档 §10）

- P1 = Task 1-9（核心包全绿 + 黄金交叉验证）
- P2 = Task 10-11（registry + CLI 可用）
- P3 = Task 12-13（宿主桥接端到端）
- P4 = Task 14（一致性矩阵收尾）

## 风险与注意

1. **单遍展开顺序**：Task 5 用例 4 是最容易写错的点，先写测试锁死再实现
2. **Windows symlink 特权**：Task 4 用例 4 可能需要跳过；junction 专项放 P4
3. **import 环风险**：agentplugins 严禁 import picoclaw 内部包（Task 12 Step 5 显式约束）；pkg/skills 与 pkg/agent 可以 import agentplugins
4. **go.mod 无新依赖**：交叉验证用已有 `github.com/google/jsonschema-go`
5. **中文 commit**：本计划 commit 主题用英文规避 cmd 转义坑
6. **保留名**：插件名 `data`/`registry.json` 会撞安装根特殊路径，安装期一律拒绝（Task 10）；name 规则强制全小写，Windows 无大小写碰撞之虞
7. **三处规范 MUST 已锚定**：`PLUGIN_DATA` 启动前创建、`./` command 绝对解析、缺省 cwd=插件根（Task 6 规范化 + Task 13 桥接/manager 各有测试）；`ensureMCPInitialized` 空判必须改用合并后配置（Task 13）
