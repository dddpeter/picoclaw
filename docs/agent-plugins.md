# Agent Plugins（规范 1.0.0 兼容客户端）

PicoClaw 实现了 [Agent Plugins Specification 1.0.0](https://github.com/agentplugins/agent-plugins-spec) 的兼容客户端：加载、校验、安装插件包，并把插件内的 skills 与 MCP 服务器桥接进现有体系。

## 是什么

一个插件包就是一个目录 + `plugin.json` manifest + 固定位置的组件：

```
my-plugin/
├── plugin.json        # 必需：manifest（封闭 schema）
├── skills/<name>/SKILL.md   # 可选：Agent Skills
└── mcp.json           # 可选：MCP 服务器（stdio / streamable-http / sse）
```

PicoClaw 在每次加载时扫描安装根 `~/.agents/plugins/`，把启用插件的 skills 并入 skill 体系、MCP 服务器并入 MCP manager（内存合并，不写 config.json）。

## 用户怎么用

```bash
# 从本地目录安装（安装前自动做规范校验，目标已存在不会覆盖）
picoclaw plugin install D:\path\to\my-plugin

# 从 git 安装（浅克隆；--ref 只支持分支/tag，不支持 SHA）
picoclaw plugin install https://github.com/example/my-plugin --ref v1

# 预检（不安装，CI 可用；非法包 exit 1）
picoclaw plugin validate D:\path\to\my-plugin

picoclaw plugin list                 # 名称/版本/skills/MCP/启用态
picoclaw plugin enable|disable <name>   # 写 registry.json，下轮加载生效
picoclaw plugin remove <name> [--purge-data]   # 默认保留数据目录
```

安装即启用；`registry.json`（安装根下）记录 `{name, version, source, ref, installedAt, enabled}`。安装后可热重载或重启服务生效。

## 插件作者怎么写包

- `plugin.json`：`$schema` 必须精确等于 `https://agent-plugins.org/schemas/1.0.0/plugin.schema.json`；`name` 规则：1–64 字符，`a-z0-9.-`，首尾字母数字，禁 `--`/`..`。其余字段见规范 §5（封闭 schema：未知顶层字段会被报告并忽略，但 `author` 子字段封闭）。
- `skills/`：只有顶层子目录的 `SKILL.md` 会被发现（不递归）。
- `mcp.json`：`$schema` 必须精确等于 `https://agent-plugins.org/schemas/1.0.0/mcp.schema.json`，且与 plugin.json 版本一致（不一致 → 该插件 MCP 被禁用，skills 照常加载）。
- 路径遏制：所有包内路径解析后必须在插件根内（符号链接/junction 逃逸会被拒绝）；stdio `command` 是单个 token（裸名或 `./` 开头）；`cwd` 缺省为插件根。
- 占位符：只有 `${PLUGIN_ROOT}` / `${PLUGIN_DATA}`，仅展开 `args` 元素、`env` 值、`cwd`，单遍非递归；`command`/`url`/`headers` 不展开。`env` 键不得叫 `PLUGIN_ROOT`/`PLUGIN_DATA`（含大小写变体，客户端最后强制覆盖）。
- 非回环 URL 必须 https；`localhost`/回环 IP 字面量可用 http。

## 限制（宿主策略，非规范要求）

- **skill frontmatter `name` 必须等于目录名**，且目录名满足 `^[a-zA-Z0-9]+(-[a-zA-Z0-9]+)*$`——这是 PicoClaw 的 D2 寻址策略；规范合法但 name 不一致的 skill 会被跳过（加载时产生 warning）。
- **agent 的 `mcpServers` 允许名单非空时，插件服务器需显式放行**：键形如 `plugin/<插件名>/<服务器名>`。
- 插件 skill 与用户自有 skill 同名时，用户自有（workspace > global > builtin）永远赢；插件之间按安装根扫描顺序先到先得。
- 1.1.0 工作草案未支持（精确识别 1.0.0，未知版本拒绝并报告）；hooks/commands 等 v1 未定义组件类型按规范忽略。
- Windows：`.bat`/`.cmd` 的 `command` 保持单 token（可能经解释器启动）；安装根 `data/` 与 `registry.json` 是保留名，安装期一律拒绝。

## 一致性

规范 Appendix A 检查清单逐项到测试函数的映射见 `docs/agent-plugins-conformance.md`。

## 实现布局（与设计文档 §4 的漂移）

- `pkg/agentplugins/`：`spec.go`（常量 + **Report**，无独立 report.go）、`manifest.go`、`paths.go`、`expand.go`、`mcpconfig.go`、`skills.go`、`loader.go`（含 registry 读取语义）、`registry.go`、`install.go`、`testdata/`。**无 bridge.go**：桥接在宿主侧完成——skills 并入在 `pkg/skills/loader.go`（`SkillRoot.Kind=="plugin"` 委托 `agentplugins.DiscoverSkills`）+ `pkg/agent/context.go`（roots 追加），MCP 合并在 `pkg/agent/agent_mcp.go`（`mergePluginServers`）。`pkg/agentplugins` 仅依赖标准库。
