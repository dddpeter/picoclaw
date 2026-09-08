# 配置参数总表（Config Parameters Reference）

> 本文由源码自动梳理生成，对照版本：`pkg/config/config.go`、`config_struct.go`、`config_channel.go`、`defaults.go`、`gateway.go`、`events.go`、`turn_profile.go`、`security.go`（schema `CurrentVersion = 3`）。
>
> 配套阅读：[Configuration Guide](../guides/configuration.md)（上手教程）、[Security Configuration](../security/security_configuration.md)（密钥管理）、[Tools Configuration](tools_configuration.md)（工具细节）、[Config Versioning](config-versioning.md)（版本迁移）。

## 目录

- [加载机制](#加载机制)
- [顶层结构](#顶层结构)
- [gateway](#gateway)
- [agents](#agents)
- [session](#session)
- [evolution](#evolution)
- [isolation](#isolation)
- [channel_list](#channel_list)
- [model_list](#model_list)
- [events](#events)
- [hooks](#hooks)
- [tools](#tools)
- [heartbeat / devices / voice / memory](#heartbeat--devices--voice--memory)
- [环境变量速查](#环境变量速查)
- [.security.yml](#securityyml)

---

## 加载机制

| 机制 | 说明 |
|---|---|
| 文件路径 | 默认 `~/.picoclaw/config.json`，可用 `PICOCLAW_CONFIG` 覆盖；数据根目录可用 `PICOCLAW_HOME` 覆盖 |
| 打底逻辑 | `DefaultConfig()` 先构建完整默认值，再用用户 JSON 覆盖（`loadConfigLenient`）。**文档中的"默认值"即 `DefaultConfig()` 中的值** |
| 严格模式 | 未知字段 → 加载失败并回退旧配置（错误日志 `unknown field`）；launcher 面板走宽松模式（降级为 warning） |
| 版本迁移 | `version` 字段缺省/0/1/2 时自动迁移到 V3 并写回 + 自动备份（`MakeBackup`） |
| 热加载 | `gateway.hot_reload: true` 时文件变更自动 reload，无需重启 |
| 密钥分离 | `.security.yml` 中的敏感值优先于 config.json 同名字段 |
| 环境变量 | 几乎所有字段都有 `PICOCLAW_*` 环境变量别名（见各表"Env"列），**环境变量优先于配置文件** |

---

## 顶层结构

| JSON 键 | 类型 | 说明 |
|---|---|---|
| `version` | int | schema 版本，当前 `3` |
| `gateway` | object | 网关服务 |
| `agents` | object | agent 与模型 |
| `session` | object | 会话维度 |
| `channel_list` | object | 渠道（JSON 键为 `channel_list`，YAML/security 文件兼容 `channels`） |
| `model_list` | array | 模型供应商清单 |
| `tools` | object | 工具开关与策略（YAML 中 inline 展开） |
| `events` | object | 运行时事件 |
| `hooks` | object | 钩子 |
| `heartbeat` | object | 心跳任务 |
| `devices` | object | 硬件设备 |
| `voice` | object | 语音（STT/TTS） |
| `memory` | object | 记忆召回/提交 |
| `isolation` | object | 子进程隔离 |
| `evolution` | object | agent 自进化 |
| `build_info` | object | 只读构建信息（version/git_commit/build_time/go_version），写入无效 |

---

## gateway

源码：`pkg/config/gateway.go`

| 键 | 类型 | 默认值 | Env | 说明 |
|---|---|---|---|---|
| `gateway.host` | string | `localhost` | `PICOCLAW_GATEWAY_HOST` | 监听地址 |
| `gateway.port` | int | `18790` | `PICOCLAW_GATEWAY_PORT` | 监听端口 |
| `gateway.hot_reload` | bool | `false` | `PICOCLAW_GATEWAY_HOT_RELOAD` | 配置热加载 |
| `gateway.log_level` | string | `warn` | `PICOCLAW_LOG_LEVEL` | 可选 `debug/info/warn/error/fatal`，非法值回落 `warn` |

---

## agents

### agents.defaults

源码：`pkg/config/config.go`（`AgentDefaults`）、`defaults.go`、`turn_profile.go`

| 键 | 类型 | 默认值 | Env | 说明 |
|---|---|---|---|---|
| `workspace` | string | `~/.picoclaw/workspace` | `PICOCLAW_AGENTS_DEFAULTS_WORKSPACE` | 工作区根目录 |
| `restrict_to_workspace` | bool | `true` | `PICOCLAW_AGENTS_DEFAULTS_RESTRICT_TO_WORKSPACE` | 限制写入仅在工作区内（主 agent / subagent / 心跳任务同一边界） |
| `allow_read_outside_workspace` | bool | `false` | `PICOCLAW_AGENTS_DEFAULTS_ALLOW_READ_OUTSIDE_WORKSPACE` | 允许读工作区外路径 |
| `provider` | string | `""` | `PICOCLAW_AGENTS_DEFAULTS_PROVIDER` | 旧版全局 provider（建议用 model_name） |
| `model_name` | string | `""` | `PICOCLAW_AGENTS_DEFAULTS_MODEL_NAME` | **默认模型**，取 `model_list[].model_name` 之一 |
| `model_fallbacks` | string[] | `[]` | — | 主模型失败后的回退链 |
| `image_model` | string | `""` | `PICOCLAW_AGENTS_DEFAULTS_IMAGE_MODEL` | 图像模型 |
| `image_model_fallbacks` | string[] | `[]` | — | 图像模型回退链 |
| `max_tokens` | int | `32768` | `PICOCLAW_AGENTS_DEFAULTS_MAX_TOKENS` | 单次回复上限；显式设 `0` 时运行时回落 `8192` |
| `context_window` | int | `0`（自动 = max_tokens × 4） | `PICOCLAW_AGENTS_DEFAULTS_CONTEXT_WINDOW` | 上下文窗口；0 表示按启发式推导 |
| `temperature` | float\|null | `null`（供应商默认） | `PICOCLAW_AGENTS_DEFAULTS_TEMPERATURE` | 采样温度 |
| `max_tool_iterations` | int | `50` | `PICOCLAW_AGENTS_DEFAULTS_MAX_TOOL_ITERATIONS` | 单 turn 工具调用上限 |
| `summarize_message_threshold` | int | `20` | `PICOCLAW_AGENTS_DEFAULTS_SUMMARIZE_MESSAGE_THRESHOLD` | 触发摘要的消息条数 |
| `summarize_token_percent` | int | `75` | `PICOCLAW_AGENTS_DEFAULTS_SUMMARIZE_TOKEN_PERCENT` | 触发摘要的上下文占比 |
| `max_media_size` | int | `20971520`（20 MB） | `PICOCLAW_AGENTS_DEFAULTS_MAX_MEDIA_SIZE` | 媒体大小上限 |
| `steering_mode` | string | `one-at-a-time` | `PICOCLAW_AGENTS_DEFAULTS_STEERING_MODE` | 同会话排队策略：`one-at-a-time` / `all` |
| `max_parallel_turns` | int | `0`（=1 串行） | `PICOCLAW_AGENTS_DEFAULTS_MAX_PARALLEL_TURNS` | 跨会话 turn 并发数；0/1 串行 |
| `split_on_marker` | bool | `false` | `PICOCLAW_AGENTS_DEFAULTS_SPLIT_ON_MARKER` | 按 `<|[SPLIT]|>` 标记拆分消息 |
| `context_manager` | string | `""`（= `legacy`） | `PICOCLAW_AGENTS_DEFAULTS_CONTEXT_MANAGER` | 上下文管理器；已注册 `legacy` / `seahorse`（seahorse 仅非 mipsle/netbsd/freebsd-arm 平台） |
| `context_manager_config` | object | — | `PICOCLAW_AGENTS_DEFAULTS_CONTEXT_MANAGER_CONFIG` | 上下文管理器附加参数（raw JSON） |
| `max_llm_retries` | int | `2` | `PICOCLAW_AGENTS_DEFAULTS_MAX_LLM_RETRIES` | LLM 调用重试次数 |
| `llm_retry_backoff_secs` | int | `2` | `PICOCLAW_AGENTS_DEFAULTS_LLM_RETRY_BACKOFF_SECS` | 重试退避秒数 |
| `project_docs` | string[] | `["AGENTS.md", "README.md", "CLAUDE.md"]` | — | 注入系统提示的工作区文档；显式 `[]` 可关闭 |

### agents.defaults.routing（智能路由）

| 键 | 类型 | 默认值 | 说明 |
|---|---|---|---|
| `routing.enabled` | bool | `false` | 按消息复杂度打分分流 |
| `routing.light_model` | string | `""` | 简单任务使用的 `model_list` 模型名 |
| `routing.threshold` | float | `0.35`（0 时自动） | 复杂度 ∈ [0,1]；`score >= threshold` → 主模型，否则轻模型。一轮 turn 内不换档 |

### agents.defaults.subturn

| 键 | 类型 | 默认值 | Env | 说明 |
|---|---|---|---|---|
| `subturn.max_depth` | int | `3` | `PICOCLAW_AGENTS_DEFAULTS_SUBTURN_MAX_DEPTH` | 子 turn 最大嵌套深度 |
| `subturn.max_concurrent` | int | `5` | `PICOCLAW_AGENTS_DEFAULTS_SUBTURN_MAX_CONCURRENT` | 并发子 turn 上限 |
| `subturn.default_timeout_minutes` | int | `5` | `PICOCLAW_AGENTS_DEFAULTS_SUBTURN_DEFAULT_TIMEOUT_MINUTES` | 子 turn 默认超时 |
| `subturn.default_token_budget` | int | `0`（不限） | `PICOCLAW_AGENTS_DEFAULTS_SUBTURN_DEFAULT_TOKEN_BUDGET` | 子 turn token 预算 |
| `subturn.concurrency_timeout_sec` | int | `30` | `PICOCLAW_AGENTS_DEFAULTS_SUBTURN_CONCURRENCY_TIMEOUT_SEC` | 等待并发槽位超时 |

### agents.defaults.tool_feedback

| 键 | 类型 | 默认值 | 说明 |
|---|---|---|---|
| `tool_feedback.enabled` | bool | `false` | 向聊天推送工具执行进度 |
| `tool_feedback.max_args_length` | int | `300` | 工具参数预览截断长度 |
| `tool_feedback.separate_messages` | bool | `false` | true = 每次进度独立消息；false = 编辑同一条进度消息 |

### agents.defaults.loop_detection（低进度循环检测）

| 键 | 类型 | 默认值 | 说明 |
|---|---|---|---|
| `loop_detection.enabled` | bool | `true`（缺省即开） | 命中时不杀 turn，只在工具结果中注入告警让模型自行调整 |
| `loop_detection.bash_retry_threshold` | int | `3` | 连续相同失败命令阈值 |
| `loop_detection.edit_streak_threshold` | int | `4` | 连续 edit 类调用阈值 |

### agents.defaults.session_titles（会话标题，fork 新增）

两阶段自动命名：turn 开始落确定性标题，后台轻模型升级；`/title` 手动命名优先级最高（user > llm > derived）。

| 键 | 类型 | 默认值 | 说明 |
|---|---|---|---|
| `session_titles.enabled` | bool | `true`（缺省即开） | 显式 `false` 关闭自动命名与 `/title` 后端 |

### agents.defaults.turn_profile（请求上下文策略）

`enabled: true` 时对每个新 turn 生效；四个 block（`history` / `system_prompt` / `skills` / `tools`）共用 `mode`：

| mode | 含义 |
|---|---|
| `default`（缺省） | 保持默认行为 |
| `off` | 关闭该 block |
| `custom` | 使用 `allow` 白名单；**当前版本仅 `skills` / `tools` 支持**，`history`/`system_prompt` 用 custom 会校验报错 |

### agents.list[]（多 agent）

| 键 | 类型 | 说明 |
|---|---|---|
| `id` | string | agent ID（必填） |
| `default` | bool | 是否默认 agent |
| `name` | string | 显示名 |
| `workspace` | string | 独立工作区 |
| `model` | string 或 `{"primary","fallbacks"}` | 该 agent 的模型（支持字符串简写） |
| `skills` | string[] | 允许的 skill |
| `subagents` | `{"allow_agents": [], "model": ...}` | 子 agent 白名单与模型 |

### agents.dispatch（按来源分发）

`rules[]`：`{name, agent, when: {channel, account, space, chat, topic, sender, mentioned}, session_dimensions}` —— 命中 `when` 条件的消息交给指定 agent。

---

## session

| 键 | 类型 | 默认值 | 说明 |
|---|---|---|---|
| `session.dimensions` | string[] | `["chat"]` | 会话隔离维度：`chat` / `sender` 任意组合 |
| `session.identity_links` | map | `{}` | 身份跨渠道绑定 |
| `session.dm_scope` | string | `""` | 便捷写法：`per-channel-peer`（=chat+sender）/ `per-channel`（=chat）/ `per-peer`（=sender）/ `global`；显式 `dimensions` 优先 |

---

## evolution

| 键 | 类型 | 默认值 | 说明 |
|---|---|---|---|
| `evolution.enabled` | bool | `false` | 自进化总开关 |
| `evolution.mode` | string | `observe` | `observe`（只观察）/ `draft`（产出草稿）/ `apply`（自动应用） |
| `evolution.state_dir` | string | `""` | 状态目录 |
| `evolution.min_task_count` | int | `2` | 提炼 skill 的最少任务数（旧名 `min_case_count` 已废弃仍兼容） |
| `evolution.min_success_ratio` | float | `0.7` | 最少成功率（旧名 `min_success_rate` 已废弃仍兼容） |
| `evolution.cold_path_trigger` | string | `after_turn` | `after_turn` / `scheduled` / `manual`（或 `none/off`）；仅 draft/apply 模式生效 |
| `evolution.cold_path_times` | string[] | `[]` | scheduled 模式的触发时刻 |
| `evolution.cold_after_days` | int | `90` | skill 转冷天数 |
| `evolution.archive_after_days` | int | `180` | 归档天数 |
| `evolution.delete_after_days` | int | `365` | 删除天数 |

---

## isolation

| 键 | 类型 | 默认值 | 说明 |
|---|---|---|---|
| `isolation.enabled` | bool | `false` | 子进程隔离（Linux 实现），opt-in |
| `isolation.expose_paths[]` | array | `[]` | 隔离后仍暴露给子进程的宿主路径 `{source, target?, mode}` |

---

## channel_list

源码：`pkg/config/config_channel.go`、`config.go` 各 `*Settings` 结构体、`defaults.go`（`defaultChannels()`）

每个渠道条目的公共字段（`Channel`）：

| 键 | 类型 | 默认值 | 说明 |
|---|---|---|---|
| `enabled` | bool | 渠道相关（见下） | 启用开关 |
| `type` | string | = 键名 | 渠道类型；不填则用键名 |
| `allow_from` | string[] | 空 = 不限制 | 发送者白名单（兼容数字/字符串混排、中英文逗号） |
| `reasoning_channel_id` | string | `""` | 推理过程转发到的渠道 |
| `group_trigger.mention_only` | bool | `false` | 群聊仅 @ 时响应 |
| `group_trigger.prefixes` | string[] | `[]` | 群聊触发前缀 |
| `typing.enabled` | bool | 渠道相关 | "正在输入"指示 |
| `placeholder.enabled` | bool | 渠道相关 | 处理中占位消息 |
| `placeholder.text` | string[] | `["Thinking..."]` | 占位文案（多条随机取） |
| `settings` | object | 渠道专属 | 渠道专属配置（JSON 嵌套格式；旧扁平格式仍兼容） |

支持 23 种渠道类型（type 常量）：`pico`、`pico_client`、`telegram`、`discord`、`feishu`、`weixin`、`wecom`、`dingtalk`、`slack`、`matrix`、`deltachat`、`line`、`onebot`、`qq`、`irc`、`vk`、`maixcam`、`whatsapp`、`whatsapp_native`、`teams_webhook`、`mqtt`、`slack_webhook`。
`pico` 与 `pico_client` 为**单例类型**，同类只允许一个启用实例。

### 各渠道 settings 速查

| 渠道 | 键（settings.*） | 类型 | 默认值 | Env |
|---|---|---|---|---|
| **pico** | `token` | secret | 必填 | `PICOCLAW_CHANNELS_PICO_TOKEN` |
| | `allow_token_query` / `allow_origins` | bool / string[] | — | — |
| | `streaming.enabled` | bool | `true` | — |
| | `ping_interval` / `read_timeout` / `write_timeout` / `max_connections` | int | `30` / `60` / `10` / `100` | — |
| **pico_client** | `url` / `token` / `session_id` | string | — | `PICOCLAW_CHANNELS_PICO_CLIENT_URL` / `_TOKEN` |
| | `ping_interval` / `read_timeout` | int | — | — |
| **telegram** | `token` | secret | 必填 | `PICOCLAW_CHANNELS_TELEGRAM_TOKEN` |
| | `base_url` / `proxy` | string | — | `..._BASE_URL` / `..._PROXY` |
| | `use_markdown_v2` | bool | `false` | `..._USE_MARKDOWN_V2` |
| | `media_group_delay_ms` | int | `500` | `..._MEDIA_GROUP_DELAY_MS` |
| | `streaming.*` | object | 关 | — |
| **feishu** | `app_id` / `app_secret` | string | 必填 | `PICOCLAW_CHANNELS_FEISHU_APP_ID` / `_APP_SECRET` |
| | `encrypt_key` / `verification_token` | secret | — | `..._ENCRYPT_KEY` / `..._VERIFICATION_TOKEN` |
| | `is_lark` | bool | `false` | `PICOCLAW_CHANNELS_FEISHU_IS_LARK` |
| | `random_reaction_emoji` | string[] | — | `..._RANDOM_REACTION_EMOJI` |
| | `streaming.*` | object | 关 | — |
| **discord** | `token` | secret | 必填 | `PICOCLAW_CHANNELS_DISCORD_TOKEN` |
| | `proxy` / `mention_only` | string / bool | — | `..._PROXY` / `..._MENTION_ONLY` |
| **weixin** | `token` | secret | 必填 | `PICOCLAW_CHANNELS_WEIXIN_TOKEN` |
| | `account_id` / `base_url` / `cdn_base_url` / `proxy` | string | `https://ilinkai.weixin.qq.com/` / `https://novac2c.cdn.weixin.qq.com/c2c` | `..._ACCOUNT_ID` / `_BASE_URL` / `_CDN_BASE_URL` / `_PROXY` |
| **wecom** | `bot_id` / `secret` | string | 必填 | `BOT_ID` / `SECRET`（注意：无 PICOCLAW 前缀） |
| | `websocket_url` | string | `wss://openws.work.weixin.qq.com` | `WEBSOCKET_URL` |
| | `send_thinking_message` | bool | `true` | `SEND_THINKING_MESSAGE` |
| | `streaming.*` | object | 关 | — |
| **dingtalk** | `client_id` / `client_secret` | string | 必填 | `PICOCLAW_CHANNELS_DINGTALK_CLIENT_ID` / `_CLIENT_SECRET` |
| **slack** | `bot_token` / `app_token` | secret | 必填 | `PICOCLAW_CHANNELS_SLACK_BOT_TOKEN` / `_APP_TOKEN` |
| **matrix** | `homeserver` / `user_id` / `access_token` | string | `https://matrix.org` | `PICOCLAW_CHANNELS_MATRIX_HOMESERVER` / `_USER_ID` / `_ACCESS_TOKEN` |
| | `device_id` / `join_on_invite` / `message_format` | — | `join_on_invite` 默认 `true` | — |
| | `crypto_database_path` / `crypto_passphrase` | string | — | — |
| **deltachat** | `email` / `password` | string | `email` 必填 | `PICOCLAW_CHANNELS_DELTACHAT_EMAIL` / `_PASSWORD` |
| | `display_name` / `avatar_image` / `data_dir` / `rpc_server_path` / `invite_link` / `allow_crosspost` | — | — | 同名 Env |
| | `imap_server` / `imap_port` / `smtp_server` / `smtp_port` | — | — | — |
| **line** | `channel_secret` / `channel_access_token` | secret | 必填 | `PICOCLAW_CHANNELS_LINE_CHANNEL_SECRET` / `_CHANNEL_ACCESS_TOKEN` |
| | `webhook_host` / `webhook_port` / `webhook_path` | — | `0.0.0.0` / `18791` / `/webhook/line` | `..._WEBHOOK_HOST` / `_PORT` / `_PATH` |
| **onebot** | `ws_url` / `access_token` | string | `ws://127.0.0.1:3001` | `PICOCLAW_CHANNELS_ONEBOT_WS_URL` / `_ACCESS_TOKEN` |
| | `reconnect_interval` | int | `5` | `..._RECONNECT_INTERVAL` |
| | `group_trigger_prefix` | string[] | — | `..._GROUP_TRIGGER_PREFIX` |
| **qq** | `app_id` / `app_secret` | string | 必填 | `PICOCLAW_CHANNELS_QQ_APP_ID` / `_APP_SECRET` |
| | `max_message_length` | int | `2000` | `..._MAX_MESSAGE_LENGTH` |
| | `max_base64_file_size_mib` | int64 | — | `..._MAX_BASE64_FILE_SIZE_MIB` |
| | `send_markdown` | bool | — | `..._SEND_MARKDOWN` |
| **irc** | `server` / `tls` / `nick` / `user` / `real_name` / `password` | — | `tls` 默认 `true`，`nick` 默认 `picoclaw` | `PICOCLAW_CHANNELS_IRC_*` |
| | `nickserv_password` / `sasl_user` / `sasl_password` / `channels` / `request_caps` | — | — | 同名 Env |
| **vk** | `token` / `group_id` | — | 必填 | `PICOCLAW_CHANNELS_VK_TOKEN` / `_GROUP_ID` |
| **maixcam** | `host` / `port` | — | `0.0.0.0` / `18790` | `PICOCLAW_CHANNELS_MAIXCAM_HOST` / `_PORT` |
| **whatsapp** | `bridge_url` | string | `ws://localhost:3001` | `PICOCLAW_CHANNELS_WHATSAPP_BRIDGE_URL` |
| | `use_native` / `session_store_path` | — | — | `..._USE_NATIVE` / `..._SESSION_STORE_PATH` |
| **teams_webhook** | `webhooks.<name>.webhook_url` | secret | — | — |
| | `webhooks.<name>.title` | string | — | — |
| **slack_webhook** | `webhooks.<name>.webhook_url` | secret | — | — |
| | `webhooks.<name>.username` / `icon_emoji` | string | — | — |
| **mqtt** | `broker` / `agent_id` | string | 必填 | `PICOCLAW_CHANNELS_MQTT_BROKER` / `_AGENT_ID` |
| | `topic_prefix` / `username` / `password` / `client_id` | — | — | 同名 Env |
| | `keep_alive` / `qos` | int | — | `..._KEEP_ALIVE` / `..._QOS` |

> 表中 `streaming.*` 均为 `{enabled: bool, throttle_seconds: int, min_growth_chars: int}`；仅 `enabled: true` 时 throttle/min_growth 才生效（缺省用渠道内建值）。

---

## model_list

源码：`pkg/config/config.go`（`ModelConfig`）

数组型，每条是一个"模型别名"。**核心规则：`model_name`（用户起的别名）+ `model`（真实模型 ID）必填**。

| 键 | 类型 | 默认值 | 说明 |
|---|---|---|---|
| `model_name` | string | 必填 | 用户侧别名，`agents.defaults.model_name` / routing 等都引用它 |
| `provider` | string | 空 = 从 model 前缀推断 | 供应商路由名（openai/anthropic/zhipu/deepseek/gemini/groq/qwen/moonshot/nvidia/ollama/lmstudio/openrouter/vllm/cerebras/volcengine/azure/minimax…，全表见 [configuration.md §All Supported Vendors]） |
| `model` | string | 必填 | 真实模型 ID，可带 provider 前缀（如 `openai/gpt-5.4`）；禁空格、禁 `/` 开头、禁 `//` |
| `api_keys` | string[] | `[]` | API key 数组（多 key 自动轮询/故障切换；写 `.security.yml` 更安全）。**注意是复数 `api_keys`** |
| `api_base` | string | — | API endpoint |
| `proxy` | string | — | HTTP 代理 |
| `fallbacks` | string[] | — | 引用其它 `model_name` 的故障切换链 |
| `enabled` | bool | 自动推断 | 缺省时有 key（或名为 `local-model`）即启用 |
| `auth_method` | string | — | `oauth` / `token`（CLI 型供应商用） |
| `connect_mode` | string | — | `stdio` / `grpc` |
| `workspace` | string | — | CLI 型供应商工作区 |
| `rpm` | int | — | 每分钟请求限速 |
| `max_tokens_field` | string | — | 上游字段名兼容（如 `max_completion_tokens`） |
| `request_timeout` | int | `120` 秒（0 时自动） | 请求超时 |
| `thinking_level` | string | — | `off/low/medium/high/xhigh/adaptive` |
| `tool_schema_transform` | string | — | 工具 schema 兼容变换（如 `simple`） |
| `streaming.enabled` | bool | `false` | 该条目启用流式 |
| `extra_body` | object | — | 注入请求体的额外字段（如 `reasoning_split`） |
| `custom_headers` | object | — | 注入每个 HTTP 请求的额外头 |
| `user_agent` | string | — | 自定义 UA |

> **常见坑**：单数 `api_key` 不是合法字段（会报 unknown field）；context_window 声明值大于供应商实际值时超出部分会被截断。

---

## events

源码：`pkg/config/events.go`

| 键 | 类型 | 默认值 | 说明 |
|---|---|---|---|
| `events.logging.enabled` | bool | `true` | 运行时事件打印开关 |
| `events.logging.include` | string[] | `["agent.*"]` | 包含的事件 kind / glob（`*` = 全部） |
| `events.logging.exclude` | string[] | `[]` | include 命中后再排除 |
| `events.logging.min_severity` | string | `info` | `debug/info/warn/error` |
| `events.logging.include_payload` | bool | `false` | 日志附带原始 payload（诊断用） |

---

## hooks

| 键 | 类型 | 默认值 | 说明 |
|---|---|---|---|
| `hooks.enabled` | bool | `true` | 钩子总开关 |
| `hooks.defaults.observer_timeout_ms` | int | `500` | 观察型钩子超时 |
| `hooks.defaults.interceptor_timeout_ms` | int | `5000` | 拦截型钩子超时 |
| `hooks.defaults.approval_timeout_ms` | int | `60000` | 审批等待超时 |
| `hooks.builtins.<name>` | object | — | `{enabled, priority, config}` |
| `hooks.processes.<name>` | object | — | `{enabled, priority, transport, command[], dir, env, observe[], intercept[]}` |

---

## tools

源码：`pkg/config/config.go`（`ToolsConfig`）、`defaults.go`

### tools 顶层

| 键 | 类型 | 默认值 | Env | 说明 |
|---|---|---|---|---|
| `tools.allow_read_paths` | string[] | `[]` | `PICOCLAW_TOOLS_ALLOW_READ_PATHS` | 额外允许读的路径 |
| `tools.allow_write_paths` | string[] | `[]` | `PICOCLAW_TOOLS_ALLOW_WRITE_PATHS` | 额外允许写的路径 |
| `tools.filter_sensitive_data` | bool | `true` | `PICOCLAW_TOOLS_FILTER_SENSITIVE_DATA` | 工具结果回传 LLM 前过滤敏感值 |
| `tools.filter_min_length` | int | `8` | `PICOCLAW_TOOLS_FILTER_MIN_LENGTH` | 短于此长度不做过滤（性能） |

### 单工具开关（enabled 型）

每个键都是 `{enabled: bool}`，Env 前缀见括号，默认值如下：

| 键 | 默认 | Env 前缀 |
|---|---|---|
| `tools.append_file` | `true` | `PICOCLAW_TOOLS_APPEND_FILE_` |
| `tools.edit_file` | `true` | `PICOCLAW_TOOLS_EDIT_FILE_` |
| `tools.find_skills` | `true` | `PICOCLAW_TOOLS_FIND_SKILLS_` |
| `tools.install_skill` | `true` | `PICOCLAW_TOOLS_INSTALL_SKILL_` |
| `tools.list_dir` | `true` | `PICOCLAW_TOOLS_LIST_DIR_` |
| `tools.load_image` | `true` | `PICOCLAW_TOOLS_LOAD_IMAGE_` |
| `tools.message` | `true` | `PICOCLAW_TOOLS_MESSAGE_` |
| | （`message.media_enabled` 默认 `false`，`..._MEDIA_ENABLED`） | |
| `tools.read_file` | `true` | `PICOCLAW_TOOLS_READ_FILE_` |
| `tools.send_file` | `true` | `PICOCLAW_TOOLS_SEND_FILE_` |
| `tools.send_tts` | `false` | `PICOCLAW_TOOLS_SEND_TTS_` |
| `tools.spawn` | `true` | `PICOCLAW_TOOLS_SPAWN_` |
| `tools.spawn_status` | `false` | `PICOCLAW_TOOLS_SPAWN_STATUS_` |
| `tools.subagent` | `true` | `PICOCLAW_TOOLS_SUBAGENT_` |
| `tools.web_fetch` | `true` | `PICOCLAW_TOOLS_WEB_FETCH_` |
| `tools.write_file` | `true` | `PICOCLAW_TOOLS_WRITE_FILE_` |
| `tools.i2c` | `false`（Linux 硬件） | `PICOCLAW_TOOLS_I2C_` |
| `tools.spi` | `false`（Linux 硬件） | `PICOCLAW_TOOLS_SPI_` |
| `tools.serial` | `false`（需串口） | `PICOCLAW_TOOLS_SERIAL_` |

`tools.read_file` 额外字段：`mode`（`bytes` 默认 / `lines`）、`max_read_file_size`（默认 `65536` = 64KB）。

### tools.web

| 键 | 类型 | 默认值 | Env | 说明 |
|---|---|---|---|---|
| `tools.web.enabled` | bool | `true` | `PICOCLAW_TOOLS_WEB_ENABLED` | 搜索/抓取总开关 |
| `tools.web.provider` | string | `auto` | `PICOCLAW_TOOLS_WEB_PROVIDER` | `auto/brave/tavily/kagi/sogou/duckduckgo/gemini/perplexity/searxng/glm/baidu` |
| `tools.web.prefer_native` | bool | `true` | `PICOCLAW_TOOLS_WEB_PREFER_NATIVE` | LLM 支持原生搜索时隐藏客户端 web_search 避免重复 |
| `tools.web.proxy` | string | `""` | `PICOCLAW_TOOLS_WEB_PROXY` | http/https/socks5/socks5h |
| `tools.web.fetch_limit_bytes` | int64 | `10485760`（10MB） | `PICOCLAW_TOOLS_WEB_FETCH_LIMIT_BYTES` | 抓取体积上限 |
| `tools.web.format` | string | `plaintext` | `PICOCLAW_TOOLS_WEB_FORMAT` | 抓取输出格式 |
| `tools.web.private_host_whitelist` | string[] | `[]` | `PICOCLAW_TOOLS_WEB_PRIVATE_HOST_WHITELIST` | 允许抓取的内网主机 |
| `tools.web.brave` | object | 关，max_results 5 | `PICOCLAW_TOOLS_WEB_BRAVE_*` | `{enabled, api_keys[], max_results}` |
| `tools.web.tavily` | object | 关，max_results 5 | `PICOCLAW_TOOLS_WEB_TAVILY_*` | 同上 + `base_url` |
| `tools.web.kagi` | object | 关（base_url `https://kagi.com/api/v1/search`） | `PICOCLAW_TOOLS_WEB_KAGI_*` | 同上 |
| `tools.web.sogou` | object | **开**，max_results 5 | `PICOCLAW_TOOLS_WEB_SOGOU_*` | `{enabled, max_results}`（免 key） |
| `tools.web.duckduckgo` | object | 关，max_results 5 | `PICOCLAW_TOOLS_WEB_DUCKDUCKGO_*` | 免 key |
| `tools.web.gemini` | object | 关（model `gemini-2.5-flash`） | `PICOCLAW_TOOLS_WEB_GEMINI_*` | `{enabled, api_key, model, max_results}` |
| `tools.web.perplexity` | object | 关，max_results 5 | `PICOCLAW_TOOLS_WEB_PERPLEXITY_*` | `{enabled, api_keys[], max_results}` |
| `tools.web.searxng` | object | 关 | `PICOCLAW_TOOLS_WEB_SEARXNG_*` | `{enabled, base_url, max_results}` |
| `tools.web.glm_search` | object | 关（base_url bigmodel，engine `search_std`） | `PICOCLAW_TOOLS_WEB_GLM_*` | engine 可选 `search_std/search_pro/search_pro_sogou/search_pro_quark` |
| `tools.web.baidu_search` | object | 关（base_url 千帆，max_results 10） | `PICOCLAW_TOOLS_WEB_BAIDU_*` | `{enabled, api_key, base_url, max_results}` |

### tools.exec

| 键 | 类型 | 默认值 | Env | 说明 |
|---|---|---|---|---|
| `tools.exec.enabled` | bool | `true` | `PICOCLAW_TOOLS_EXEC_ENABLED` | exec 总开关 |
| `tools.exec.enable_deny_patterns` | bool | `true` | `PICOCLAW_TOOLS_EXEC_ENABLE_DENY_PATTERNS` | 内置危险命令拦截 |
| `tools.exec.enable_custom_deny_patterns` | bool | `false` | `PICOCLAW_TOOLS_EXEC_ENABLE_CUSTOM_DENY_PATTERNS` | 启用自定义正则规则 |
| `tools.exec.custom_deny_patterns` | string[] | `[]` | `PICOCLAW_TOOLS_EXEC_CUSTOM_DENY_PATTERNS` | 自定义拦截正则 |
| `tools.exec.custom_allow_patterns` | string[] | `[]` | `PICOCLAW_TOOLS_EXEC_CUSTOM_ALLOW_PATTERNS` | 自定义放行正则（优先于 deny） |
| `tools.exec.allow_remote` | bool | `true` | `PICOCLAW_TOOLS_EXEC_ALLOW_REMOTE` | 允许远程渠道执行命令 |
| `tools.exec.timeout_seconds` | int | `60` | `PICOCLAW_TOOLS_EXEC_TIMEOUT_SECONDS` | 0 = 用默认 |

> 另有 symlink 解析防逃逸（默认开启）；guard 只检查直接命令行，不递归查 make/go run 等拉起的子进程。

### tools.cron

| 键 | 类型 | 默认值 | Env | 说明 |
|---|---|---|---|---|
| `tools.cron.enabled` | bool | `true` | `PICOCLAW_TOOLS_CRON_ENABLED` | 定时任务开关 |
| `tools.cron.exec_timeout_minutes` | int | `5` | `PICOCLAW_TOOLS_CRON_EXEC_TIMEOUT_MINUTES` | 0 = 不限时 |
| `tools.cron.allow_command` | bool | `true` | `PICOCLAW_TOOLS_CRON_ALLOW_COMMAND` | 允许定时跑 shell |
| `tools.cron.command_allowed_remotes` | string[] | `[]` | `PICOCLAW_TOOLS_CRON_COMMAND_ALLOWED_REMOTES` | 允许 command 型任务投递的远程渠道 |

### tools.skills

| 键 | 类型 | 默认值 | Env | 说明 |
|---|---|---|---|---|
| `tools.skills.enabled` | bool | `true` | `PICOCLAW_TOOLS_SKILLS_ENABLED` | skills 总开关 |
| `tools.skills.registries` | object/array | `clawhub`（clawhub.ai）+ `github`（github.com）双开 | — | 兼容 map 与数组两种写法；每项 `{name, enabled, base_url, auth_token, param}`，未识别键进 `param`；`_` 开头键不落盘 |
| `tools.skills.github` | object | — | `PICOCLAW_TOOLS_SKILLS_GITHUB_*` | 旧版写法（base_url/token/proxy），被 registries.github 取代 |
| `tools.skills.max_concurrent_searches` | int | `2` | `PICOCLAW_TOOLS_SKILLS_MAX_CONCURRENT_SEARCHES` | 并发搜索数 |
| `tools.skills.search_cache.max_size` | int | `50` | `PICOCLAW_SKILLS_SEARCH_CACHE_MAX_SIZE` | 搜索缓存条数 |
| `tools.skills.search_cache.ttl_seconds` | int | `300` | `PICOCLAW_SKILLS_SEARCH_CACHE_TTL_SECONDS` | 缓存 TTL |

### tools.media_cleanup

| 键 | 类型 | 默认值 | Env | 说明 |
|---|---|---|---|---|
| `tools.media_cleanup.enabled` | bool | `true` | `PICOCLAW_MEDIA_CLEANUP_ENABLED` | 媒体清理 |
| `tools.media_cleanup.max_age_minutes` | int | `30` | `PICOCLAW_MEDIA_CLEANUP_MAX_AGE` | 超龄清理 |
| `tools.media_cleanup.interval_minutes` | int | `5` | `PICOCLAW_MEDIA_CLEANUP_INTERVAL` | 巡检间隔 |

### tools.mcp

| 键 | 类型 | 默认值 | Env | 说明 |
|---|---|---|---|---|
| `tools.mcp.enabled` | bool | `false` | `PICOCLAW_TOOLS_MCP_ENABLED` | MCP 总开关 |
| `tools.mcp.discovery.enabled` | bool | `false` | `PICOCLAW_TOOLS_DISCOVERY_ENABLED` | 工具发现（延迟注册）模式 |
| `tools.mcp.discovery.ttl` | int | `5` | `PICOCLAW_TOOLS_DISCOVERY_TTL` | 发现结果缓存 TTL |
| `tools.mcp.discovery.max_search_results` | int | `5` | `PICOCLAW_MAX_SEARCH_RESULTS` | 搜索返回上限 |
| `tools.mcp.discovery.use_bm25` | bool | `true` | `PICOCLAW_TOOLS_DISCOVERY_USE_BM25` | BM25 检索 |
| `tools.mcp.discovery.use_regex` | bool | `false` | `PICOCLAW_TOOLS_DISCOVERY_USE_REGEX` | 正则检索 |
| `tools.mcp.max_inline_text_chars` | int | `16384`（16KB） | `PICOCLAW_TOOLS_MCP_MAX_INLINE_TEXT_CHARS` | 超出转 artifact |
| `tools.mcp.servers.<name>` | object | `{}` | — | 见下 |

`tools.mcp.servers.<name>` 字段：

| 键 | 说明 |
|---|---|
| `enabled` | 启用开关 |
| `deferred` | null=跟随全局 discovery；true/false=覆盖该 server |
| `command` / `args[]` / `env` / `env_file` | stdio 型进程 |
| `type` | `stdio`（默认，有 command）/ `sse` / `http` / `streamable-http`（http 与 streamable-http 等价，sse 保留服务端推送） |
| `url` | sse/http 型地址 |
| `headers` | sse/http 请求头 |

---

## heartbeat / devices / voice / memory

### heartbeat

| 键 | 类型 | 默认值 | Env | 说明 |
|---|---|---|---|---|
| `heartbeat.enabled` | bool | `true` | `PICOCLAW_HEARTBEAT_ENABLED` | 心跳总开关 |
| `heartbeat.interval` | int（分钟） | `30` | `PICOCLAW_HEARTBEAT_INTERVAL` | 最小 5 分钟（<5 自动钳到 5；0 = 关闭） |

### devices

| 键 | 类型 | 默认值 | Env |
|---|---|---|---|
| `devices.enabled` | bool | `false` | `PICOCLAW_DEVICES_ENABLED` |
| `devices.monitor_usb` | bool | `true` | `PICOCLAW_DEVICES_MONITOR_USB` |

### voice

| 键 | 类型 | 默认值 | Env |
|---|---|---|---|
| `voice.model_name` | string | `""` | `PICOCLAW_VOICE_MODEL_NAME` |
| `voice.tts_model_name` | string | `""` | `PICOCLAW_VOICE_TTS_MODEL_NAME` |
| `voice.echo_transcription` | bool | `false` | `PICOCLAW_VOICE_ECHO_TRANSCRIPTION` |
| `voice.elevenlabs_api_key` | string | `""` | `PICOCLAW_VOICE_ELEVENLABS_API_KEY` |

### memory

| 键 | 类型 | 默认值 | 说明 |
|---|---|---|---|
| `memory.recall.enabled` | bool | `false` | turn 开始时用用户消息做语义检索并注入系统提示 |
| `memory.recall.server` | string | — | `tools.mcp.servers` 中的 server 名 |
| `memory.recall.tool` | string | `search` | 召回工具名 |
| `memory.recall.max_chars` | int | `2400` | 注入文本上限 |
| `memory.recall.timeout_ms` | int | `3000` | 单次调用超时 |
| `memory.commit.enabled` | bool | `false` | turn 完成后自动提交记忆 |
| `memory.commit.server` | string | — | 同上 |
| `memory.commit.tool` | string | `remember` | 提交工具名 |
| `memory.commit.timeout_ms` | int | `5000` | 单次调用超时 |

---

## 环境变量速查

### 运行时路径（os.Getenv 直读）

| 变量 | 作用 | 默认 |
|---|---|---|
| `PICOCLAW_HOME` | 数据根目录 | `~/.picoclaw` |
| `PICOCLAW_CONFIG` | config.json 完整路径 | `$PICOCLAW_HOME/config.json` |
| `PICOCLAW_BUILTIN_SKILLS` | 内置 skill 目录 | `<cwd>/skills` |
| `PICOCLAW_BINARY` | picoclaw 可执行文件路径（launcher 用） | 当前可执行文件同目录 |
| `PICOCLAW_GATEWAY_HOST` | 网关地址 | `localhost` |
| `PICOCLAW_LOG_LEVEL` | 网关日志级别（优先于 config） | `warn` |

### 字段级 Env 命名规则

- 顶层 + 字段路径拼接：`agents.defaults.model_name` → `PICOCLAW_AGENTS_DEFAULTS_MODEL_NAME`
- 工具组：`tools.web.brave.max_results` → `PICOCLAW_TOOLS_WEB_BRAVE_MAX_RESULTS`
- 例外：`wecom` 渠道用裸变量名（`BOT_ID` / `SECRET` / `WEBSOCKET_URL` / `SEND_THINKING_MESSAGE`）
- 所有字段级 Env 都**优先于** config.json 中的同名值

---

## .security.yml

源码：`pkg/config/security.go`、`example_security_usage.go`

与 config.json 同目录（`~/.picoclaw/.security.yml`），YAML 格式，**值优先于 config.json**：

```yaml
model_list:
  <model_name>:
    api_keys:            # 必须数组格式，单 key 也用数组
      - "sk-..."
channels:                # 兼容 channel_list
  <channel_name>:
    token: "..."
web:
  brave:
    api_keys: ["..."]
  glm_search:
    api_key: "..."       # GLM/Baidu 用单数字符串
```

- 值支持明文 / `enc://...`（加密引用）/ `file://...`（文件引用），由 credential resolver 解析
- YAML 序列化时 SecureString 自动加密落盘（配 passphrase 时）；序列化到 JSON 时脱敏为 `[NOT_HERE]`
- 字段名带 `:0` 后缀（如 `glm/glm-5.3:0`）是多 key 池的标准写法

---

*来源：源码结构体梳理（2026-09-08）。字段若有增删，以 `pkg/config/config.go` + `pkg/config/defaults.go` 为准。*
