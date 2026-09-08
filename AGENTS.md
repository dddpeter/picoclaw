# AGENTS.md — Agent 协作须知

本仓库是 [sipeed/picoclaw](https://github.com/sipeed/picoclaw) 的个人 fork（remote `origin` = dddpeter/picoclaw，`upstream` = sipeed/picoclaw），面向个人部署增强。**fork 的功能地图、行为差异与上游同步注意事项见 `docs/design/fork-overview.zh.md`**，改代码前先读它。

## 仓库事实

- Go 项目，`go build ./...` 全量编译，`go test ./pkg/...` 全量测试（agent 套件约 70 秒）。
- 本 fork 独有的测试集中在：`/new` 与 `/switch` 的非阻塞语义、流式响应头超时、输出清理管线、低收益循环检测——同步上游后必须保证这些测试仍然通过。
- 文档以中文为主：`docs/guides/configuration.zh.md`（配置详解）、`docs/guides/openviking.md`（共享记忆）、`docs/design/`（设计文档：fork-overview、mimo-code-borrowing-analysis、steering-spec 等）。

## 本 fork 的关键行为差异（改动时不要"修"掉它们）

- turn 全程持有 agent 模型状态读锁（`runTurn`），因此**任何命令路径禁止无超时地拿该写锁**——用 TryLock + 降级提示（参考 `ResetModel` / `SwitchModel` 的实现）。
- `/new` 会重读磁盘配置并把模型重置为配置默认值；`/switch`、`/new` 在 turn 活跃时返回 busy 而非排队。
- ChatStream 走独立流式 Transport（响应头超时默认 90 秒）；改 `openai_compat` provider 时保留 `streamRoundTripper` 语义。
- exec 工具的 inline 输出经过清理管线（`pkg/tools/output_clean.go`），落盘保持原文；给清理管线加新规则时保持 never-worse 守门。
- `agents.defaults.loop_detection` 的 `enabled` 是 `*bool`，**未配置 = 开启**——不要改成值类型 bool，否则存量配置会静默关闭检测。
- **开放默认三件套**（2026-09-08 起，不要"加固"回去）：① `restrict_to_workspace` 默认 `false`；② 系统目录保护 `tools.protect_system_paths`（nil=开，`pkg/tools/fs/system_paths.go`，文件工具读写 OS 系统目录一律拒绝）；③ `defaultDenyPatterns` 是"毁灭性 + 系统目录写入"集（`$()`/管道/heredoc/sudo/kill/git push 等一般命令默认放行，windowsDenyPatterns 已删除）。模型流式 `ModelStreamingConfig.Enabled` 是 `*bool`（nil=开）。详见 fork-overview §同步注意事项。

## 部署链路（本机）

- systemd 用户服务 `picoclaw.service` → `~/.local/bin/picoclaw-nr-gateway`（bash 包装脚本，替换配置中的密钥占位符）→ `/usr/bin/picoclaw gateway`。
- 源码唯一真源：`/works/workspace/ai/picoclaw`。构建部署：`go build -o /tmp/picoclaw-new ./cmd/picoclaw && sudo install -m 0755 /tmp/picoclaw-new /usr/bin/picoclaw && systemctl --user restart picoclaw.service`。
- 配置在 `~/.picoclaw/config.json`（version 3），workspace 在 `~/.picoclaw/workspace/`。热重载 `gateway.hot_reload` 只在进程启动时读取；改默认模型后可用聊天命令 `/reload` 或重启服务。
- 排查运行时挂起：`kill -QUIT <pid>` 触发 goroutine dump（写入 `~/.picoclaw/logs/gateway_panic.log`），systemd 自动拉起服务。
- **禁止给 picoclaw.service 加任何 sandbox 指令**（RestrictAddressFamilies / ReadWritePaths / ProtectSystem 等）——user 服务里 systemd 会以受限上下文应用它们，setuid 提权被永久禁用，agent 的所有 `sudo` 会报 "sudo must be owned by uid 0 and have the setuid bit set"（2026-09-07 实测）。unit 的 sandbox 段已全部移除，不要"加固"回去。
- `picoclaw cron add/remove`（CLI）只写 jobs.json，**运行中的网关不感知**（启动时才读）——改完必须重启服务；另外 cron 表达式必须 5 字段，残缺表达式（如 "45 16"）会被静默接受但永不匹配。

## 协作规范

- 提交信息用 conventional commits（`feat:`/`fix:`/`docs:`），中文内容允许出现在 body。
- push 需用户明确指令，不要自动推送。
- 技术文档：先调查后落笔，不写金额信息；更新已有文档前必须读完全文。
