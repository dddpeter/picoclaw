# Agent Plugins Spec 1.0 — Conformance Matrix

Appendix A（规范本地副本 `workspace\aps_spec100.md` 末尾）逐项 → 测试函数映射。所有适用项均有测试背书；无代码路径的项显式标注 N/A 及理由。

## Plugin loader

| 检查项 | 测试 | 状态 |
|---|---|---|
| Parse and validate `plugin.json`（§5.1/§5.2） | `pkg/agentplugins/manifest_test.go::TestLoadManifest` | PASS |
| Validate required `$schema`/`name`（§5.3） | `TestLoadManifest/missing_schema`、`/missing_name` | PASS |
| Validate plugin name（§5.5） | `manifest_test.go::TestValidatePluginName` | PASS |
| Report and ignore unknown fields（§5.2） | `TestLoadManifest/unknown_top-level_field_warns` | PASS |
| Ignore unimplemented `extensions`（§8.1） | `TestLoadManifest/full_valid`、`/extensions_array_warns_only` | PASS |
| Reject paths resolving outside root（§4.1） | `paths_test.go::TestContains/*`、`loader_test.go::TestLoadPlugin/plugin_json_symlink_escape_rejected` | PASS |
| Windows junction 逃逸遏制（§4.1 专项） | `paths_test.go::TestContains/junction_escape` | PASS |
| Discover file-based extension dirs（§8.2） | PicoClaw 未实现任何扩展命名空间，忽略即符合 §8.1 | N/A |

## Component discovery

| 检查项 | 测试 | 状态 |
|---|---|---|
| Scan fixed locations（§6.1） | `skills_test.go::TestDiscoverSkills`、`mcpconfig_test.go::TestLoadMCPConfig` | PASS |
| Ignore missing locations（§6.2） | `TestLoadMCPConfig_Missing`、`TestDiscoverSkills/missing_skills_dir_is_silent`；非目录形态：`TestLoadMCPConfig_NotAFile`、`TestDiscoverSkills/skills_as_file_warns` | PASS |

## MCP configuration

| 检查项 | 测试 | 状态 |
|---|---|---|
| Select supported `$schema` + closed schema/variants（§7.2.1） | `TestLoadMCPConfig`（bad_json / unknown_top-level / mcpServers_missing / schema_missing / unknown_type / missing_type / unknown_field_inside_entry） | PASS |
| mcp.json 版本与 plugin.json 一致（§10.1） | `TestLoadMCPConfig/schema_version_mismatch_with_manifest`、`loader_test.go::TestLoadPlugin/mcp_schema_mismatch_disables_MCP_but_keeps_skills` | PASS |
| stdio / Streamable HTTP 双支持（§7.2.1） | stdio：`TestLoadMCPConfig/stdio_with_placeholders_and_cwd`；streamable-http：`/streamable-http_valid`；sse（OPTIONAL）：`/sse_valid` | PASS |
| Declared transport for initial connection（§7.2.1） | 桥接原样传递 `Type`，manager 按 `Type` 分派：`agent_plugin_mcp_test.go::TestPluginMCP_MergeBasic` | PASS |
| Remote URL / header requirements（§7.2.1） | `TestLoadMCPConfig/http_non-loopback_rejected`、`/http_loopback_host_allowed`、`/http_loopback_IP_literal_allowed`、`/userinfo_rejected`、`/fragment_rejected`、`/header_case_conflict_rejected` | PASS |

## Environment and expansion

| 检查项 | 测试 | 状态 |
|---|---|---|
| Provide `PLUGIN_ROOT` + writable `PLUGIN_DATA`（§9.1） | `agent_plugin_mcp_test.go::TestPluginMCP_MergeBasic`（PluginRoot/PluginData + data dir 创建） | PASS |
| `command` 单 token 解析（§7.2.1） | `mcpconfig_test.go::TestLoadMCPConfig/command_with_space`、`/command_escapes_root`、`/bat_cmd_script_single_token` | PASS |
| 缺省 cwd = 插件根（§7.2.1） | `loader_test.go::TestLoadPlugin/golden_plugin_components_loaded`、`TestPluginMCP_MergeBasic`（Dir==插件根） | PASS |
| 显式 cwd 形态 + 包含性（§7.2.1） | `TestLoadMCPConfig/cwd_./_form_resolved`、`/cwd_bare_relative`、`/cwd_../_escape`、`/cwd_unknown_placeholder`、`mcpconfig_test.go::TestLoadMCPConfig_PluginDataCWD` | PASS |
| Overlay configured env（§9.1） | `pkg/mcp/manager_plugin_test.go::TestInjectPluginEnv` | PASS |
| 最后写 PLUGIN_ROOT/DATA 并替换等名条目（§9.1） | `TestInjectPluginEnv`（伪造键 + 大小写变体被覆盖） | PASS |
| PATH 不影响裸 command 解析（§7.2.1） | 客户端不改动裸 command 的平台解析；`.bat/.cmd` 经解释器启动 | N/A (client launches via cmd /c on Windows) |
| 仅展开两占位符于 args/env/cwd（§9.2） | `expand_test.go::TestExpand*`、`TestExpandNoRescan`、`TestExpandEnvValues`；url/headers 不展开：`TestLoadMCPConfig/streamable-http_valid` | PASS |

## Resilience

| 检查项 | 测试 | 状态 |
|---|---|---|
| Ignore unsupported component types（§11.3） | v1 之外无组件类型；未知 manifest 顶层字段仅警告 | N/A（无该类组件） |
| Skip unsupported-transport entries（§7.2.2.4） | `TestLoadMCPConfig/unknown_transport_type`、`/missing_type` | PASS |
| Continue on independent component failure（§11.3） | `loader_test.go::TestLoadPluginsDir/mixed_install_root`、`TestLoadMCPConfig/command_escapes_root`（单条剔除）、`TestLoadPlugin/mcp_schema_mismatch_disables_MCP_but_keeps_skills` | PASS |
| Support at least one component type（§11.1） | skills + MCP 双支持：`pkg/agent/agent_plugin_e2e_test.go::TestPluginEndToEndLoadMergeRemove` | PASS |

## 官方 Schema 交叉验证（设计 D3）

黄金插件（`pkg/agentplugins/testdata/golden/`）同时通过手写校验器与官方 JSON Schema（vendored 副本 `testdata/schemas/`，测试期从不联网）：`conformance_test.go::TestGoldenAgainstOfficialSchema`。

注：官方 plugin.schema.json 的 name pattern 使用 Perl 前瞻断言，jsonschema-go 编译不支持；测试加载器把它翻译为语义等价的 Go 正则（见 `conformance_test.go` 注释）。

## 端到端

`pkg/agent/agent_plugin_e2e_test.go::TestPluginEndToEndLoadMergeRemove`：安装根 → loader（skill + MCP 组件）→ SkillsLoader 桥接（`plugin:golden` 来源）→ merge（`plugin/golden/echo`）→ remove 后全部消失。
