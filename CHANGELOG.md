# Changelog

本项目的所有显著变更都会记录在此文件。

格式基于 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/)，
版本号遵循 [Semantic Versioning](https://semver.org/lang/zh-CN/)。

## [Unreleased]

## [0.3.3] - 2026-09-09

### Added
- 首启向导（first-run wizard），首次启动检测并引导用户完成必要配置
- `picoclaw doctor` 健康检查命令，覆盖配置 / 渠道 / MCP / 安全档位
- 安全档位（safety tier）机制，按场景分级启用 deny 规则
- 并发默认 P0 四件套（gateway watchdog 探活自愈）
- 聊天反馈 turn 计时 + 停滞警告 + 可靠收尾（避免长 turn 静默卡死）
- 系统目录保护 + 毁灭性拦截集（dangerous patterns 默认启用）
- 流式（streaming）默认开启

### Fixed
- ocr v1.11.6 评审修复（12 findings 全部落地）
- 飞书 ACK：消息确认机制，避免丢消息
- 飞书表情回应回显：用户表情可被 AI 正确读取
- 飞书卡片回调可观测性（card.action.trigger 日志/事件链路）
- 配置页未知字段 500 → 友好提示（schema mismatch 不再直接 crash）
- 流式卡片 fallback 契约（流式失败时降级到普通消息的协议）
- gateway 探测超时误杀（避免 health probe 把健康 gateway 当作 down 重启）
- 测试隔离加固（test 不再依赖真实 cron / 系统时钟等副作用）

### Notes
- 11 commits 自 v0.3.2
- 对应 tag：`v0.3.3`

[Unreleased]: https://example.com/picoclaw/compare/v0.3.3...HEAD
[0.3.3]: https://example.com/picoclaw/compare/v0.3.2...v0.3.3