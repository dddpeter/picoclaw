# Changelog

本项目的所有显著变更都会记录在此文件。

格式基于 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/)，
版本号遵循 [Semantic Versioning](https://semver.org/lang/zh-CN/)。

## [Unreleased]

## [0.4.0] - 2026-09-11

### Added
- exec 子进程 Windows Job Object 整树击杀：启动后挂 job，终止优先 TerminateJobObject（不依赖 taskkill 的 PPID 走查），失败降级 taskkill；不带 KILL_ON_JOB_CLOSE，正常完成时有意存活的 daemon 不被误杀
- /stop 中止语义：会话历史封口保留（给悬空 tool_calls 补合成结果），不再回滚抹除——新会话首 turn 被 /stop 后记录不再丢失
- HardAbort 看门狗：turn goroutine 卡死（如阻塞在忽略取消的 I/O 上）10s 宽限后强制释放会话注册，会话不再永久 busy（此前唯一恢复手段是重启网关）
- 安装包版本注入（iscc /DMyAppVersion=<ver>）与产物命名 PicoClawSetup-<version>.exe

### Fixed
- exec 输出等待有界化（cmd.WaitDelay 5s + exec.ErrWaitDelay 识别）：/stop 或超时击杀进程后不再永久阻塞在管道 EOF 等待；命令正常退出但 daemon 持有输出管道时立即以成功+附注返回（此前 exec 假挂到 daemon 退出，agent-browser 场景"卡住只能重启"的根源）
- 响应式 /stop（LLM 流式中按停止）不再清空新会话历史：abortTurn 与 HardAbort 统一封口语义，删除 restore point 回滚机制（快照在用户消息落盘前捕获，回滚=清空）
- 僵尸 turn 迟到解退不再清掉新 turn 的记录（zombieReleased 守卫）；封口后迟到的真实工具结果不再落盘（8 处工具消息落盘点统一守卫，杜绝同 tool_call_id 双 tool 消息）
- 输出清理管线 CRLF bug：Windows 下 CRLF 行尾的  被当进度条重绘，命令输出整行被清空（inline 送模型路径几乎全空）
- turn LLM 失败韧性：429 风暴 UX、错误双发去重、非流式超时校准

### Notes
- 6 commits 自 v0.3.3（含 1 个 packaging chore）
- 对应 tag：`v0.4.0`

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

[Unreleased]: https://example.com/picoclaw/compare/v0.4.0...HEAD
[0.4.0]: https://example.com/picoclaw/compare/v0.3.3...v0.4.0
[0.3.3]: https://example.com/picoclaw/compare/v0.3.2...v0.3.3