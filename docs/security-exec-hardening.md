# picoclaw exec 安全加固方案 — 设计文档

> 作者：小爱马
> 日期：2026-09-07
> 关联 commit: `9cc5a3b1`（fork 改动已实施；config.json 见第四节）
> 背景: Peter 要求加强 exec tool 的安全检查, 但又不能误伤日常命令
> 实施说明: 文中 3.2 的结构体定义示意按实际代码落在 `pkg/config/config.go` 的 `ExecConfig`（非 schema.go）；shell.go 实际改动见 commit diff。

## 一、问题陈述

Peter 当前 `/home/dddpeter/.picoclaw/config.json` 配置:

```json
"exec": {
  "enabled": true,
  "enable_deny_patterns": false,           ← 默认 deny 关闭
  "allow_remote": true,
  "custom_deny_patterns": [ ... ],          ← 但 custom deny 写了 14 条
  "custom_allow_patterns": [ ... ]
}
```

但 picoclaw `pkg/tools/shell.go` 第 161-181 行的逻辑:

```go
if enableDenyPatterns {
    denyPatterns = append(denyPatterns, defaultDenyPatterns...)
    if len(execConfig.CustomDenyPatterns) > 0 {
        for _, pattern := range execConfig.CustomDenyPatterns {
            denyPatterns = append(denyPatterns, re)
        }
    }
} else {
    logger.Warn("deny patterns are disabled, all commands will be allowed")
    // ← 整个分支跳过, custom_deny_patterns 不加载!
}
```

**结果**: `enable_deny_patterns: false` → 14 个 `custom_deny_patterns` **形同虚设**, 全部命令都放行。

## 二、目标

| 目标 | 说明 |
|------|------|
| ✅ 真拦截危险命令 | `rm -rf /` / `mkfs /dev/sda` / `mv /dev/null` / `chmod 777 /` 等 |
| ✅ 不误伤日常工作 | `rm -rf build/` / `chmod 755 script.sh` / `kill PID` / `apt install` / `git push` |
| ✅ 保留 Peter 现有控制力 | default deny 不加载 (Peter 偏好) |
| ✅ 向后兼容 | 默认行为不变 (开关默认关) |

## 三、方案: 加 `enable_custom_deny_patterns` 独立开关

### 3.1 改动文件清单

| 文件 | 改动量 | 说明 |
|------|--------|------|
| `pkg/config/schema.go` | +1 字段 | schema 加 `EnableCustomDenyPatterns bool` |
| `pkg/tools/shell.go` | +12 行 / -2 行 | shell.go 第 161-181 行加 elif 分支 |

### 3.2 schema 改动

在 `pkg/config/schema.go` 的 `ExecConfig` 结构体加:

```go
type ExecConfig struct {
    Enabled                   bool     `json:"enabled"`
    EnableDenyPatterns        bool     `json:"enable_deny_patterns"`
    EnableCustomDenyPatterns  bool     `json:"enable_custom_deny_patterns"`  // ← 新
    AllowRemote               bool     `json:"allow_remote"`
    CustomDenyPatterns        []string `json:"custom_deny_patterns"`
    CustomAllowPatterns       []string `json:"custom_allow_patterns"`
    TimeoutSeconds            int      `json:"timeout_seconds"`
}
```

### 3.3 shell.go 改动

**位置**: `pkg/tools/shell.go` 第 161-181 行

**当前代码**:
```go
if enableDenyPatterns {
    denyPatterns = append(denyPatterns, defaultDenyPatterns...)
    if runtime.GOOS == "windows" {
        denyPatterns = append(denyPatterns, windowsDenyPatterns...)
    }
    if len(execConfig.CustomDenyPatterns) > 0 {
        logger.InfoCF("tools", "using custom deny patterns", ...)
        for _, pattern := range execConfig.CustomDenyPatterns {
            re, err := regexp.Compile(pattern)
            if err != nil {
                return nil, fmt.Errorf("invalid custom deny pattern %q: %w", pattern, err)
            }
            denyPatterns = append(denyPatterns, re)
        }
    }
} else {
    logger.WarnCF("tools", "deny patterns are disabled, all commands will be allowed", nil)
}
```

**改为**:
```go
enableDenyPatterns := execConfig.EnableDenyPatterns
enableCustomDenyPatterns := execConfig.EnableCustomDenyPatterns  // ← 新
allowRemote = execConfig.AllowRemote

if enableDenyPatterns {
    // 现有: 加载 default + custom
    denyPatterns = append(denyPatterns, defaultDenyPatterns...)
    if runtime.GOOS == "windows" {
        denyPatterns = append(denyPatterns, windowsDenyPatterns...)
    }
    if len(execConfig.CustomDenyPatterns) > 0 {
        logger.InfoCF("tools", "using custom deny patterns", ...)
        for _, pattern := range execConfig.CustomDenyPatterns {
            re, err := regexp.Compile(pattern)
            if err != nil {
                return nil, fmt.Errorf("invalid custom deny pattern %q: %w", pattern, err)
            }
            denyPatterns = append(denyPatterns, re)
        }
    }
} else if enableCustomDenyPatterns {  // ← 新分支
    // 仅加载 custom, 不加载 default (避免日常命令被误伤)
    if len(execConfig.CustomDenyPatterns) > 0 {
        logger.InfoCF("tools", "using custom deny patterns only (default disabled)", map[string]any{
            "patterns": execConfig.CustomDenyPatterns,
        })
        for _, pattern := range execConfig.CustomDenyPatterns {
            re, err := regexp.Compile(pattern)
            if err != nil {
                return nil, fmt.Errorf("invalid custom deny pattern %q: %w", pattern, err)
            }
            denyPatterns = append(denyPatterns, re)
        }
    }
} else {
    logger.WarnCF("tools", "deny patterns are disabled, all commands will be allowed", nil)
}
```

### 3.4 不动的地方

- `guardCommand()` 函数 (`shell.go:1198-1222`): 已正确 — deny 永远先 check, 然后才 check allow list
- `customAllowPatterns` 加载逻辑 (`shell.go:182-188`): 不变
- `defaultDenyPatterns` 定义 (`shell.go:52-100`): 不变

## 四、Peter 当前配置现状

文件 `/home/dddpeter/.picoclaw/config.json` 已于 2026-09-07 修改（备份 `config.json.bak.pre-deny.20260907_150224`、`config.json.bak.pre-custom-deny-switch.20260907`）。

### ⚠️ 实施时发现的关键问题：Go regexp 是 RE2，不支持 `(?!...)` 前瞻

本文档原稿给出的 8 条新增 pattern 中，`rm` 顶层路径与 `chmod 777` 顶层路径两条使用了 Perl 负向前瞻 `(?!/)`。**Go 的 regexp 包（RE2）不支持任何前瞻/后瞻**——这些规则从未编译通过。此前没有暴露是因为旧代码在 `enable_deny_patterns=false` 时根本不加载 custom 列表；新开关上线后首次真正编译，直接导致 gateway 启动失败崩溃循环（2026-09-07 15:14，日志 `invalid or unsupported Perl syntax: (?!`）。

原文档"已测试 22+ 例全过"的结论不可信——测试没有跑在 Go regexp 上。

### 最终生效的 14 条 pattern（已全部通过 RE2 编译 + 50 例向量验证）

| # | pattern | 拦截 | 备注 |
|---|---------|------|------|
| 1 | `\bdd\s+if=` | `dd if=/dev/zero of=/dev/sda` | 原有 |
| 2 | `>\s*/dev/(sd[a-z]\|hd[a-z]\|vd[a-z]\|xvd[a-z]\|nvme\d\|mmcblk\d\|loop\d\|dm-\d\|md\d)` | 直接写块设备 | 原有 |
| 3 | `:\(\)\s*\{.*\};\s*:` | fork bomb | 原有 |
| 4 | `>>?\s*~/?\.ssh/` | 追加写 `~/.ssh/` | 原有 |
| 5 | `\brm\s+(-\S+\s+)*(--\S+\s+)*/\w*(\s\|$\|\*)` | `rm -rf /`、`rm -rf /tmp`、`rm -rf /etc extra`、`rm --no-preserve-root -rf /` | **重写**（原 `(?!/)` 版不可编译）。等价技巧：`\brm\s+`+flags 前缀锚定匹配起点后，`\w*(\s\|$\|\*)` 与原前瞻版语义等价。注意语义为**只拦第一层路径**：`rm -rf /tmp/build`、`rm -rf /home/dddpeter`（多段路径）放行，与原设计意图一致 |
| 6 | `\brm\s+(-\S+\s+)*(--\S+\s+)*\.\s*$` | `rm -rf .` | 原有 |
| 7 | `\brm\s+(-\S+\s+)*(--\S+\s+)*\*\s*$` | `rm -rf *` | 原有 |
| 8 | `\bmv\b[^|;>]*\s/dev/null\b` | `mv /home /dev/null`、`mv -f /home/x /dev/null` | **修正**：filler 字符类排除 `>`，否则 `mv foo bar 2>/dev/null` 会误伤 |
| 9 | `\bmkfs(\.\w+)?\s+/dev/(sd[a-z]\|...)` | `mkfs.ext4 /dev/sda1` | 原有 |
| 10 | `(^\|[^\|;&\w/])\s*>\s*(/etc/\|/boot/\|/usr/\|/var/\|/bin/\|/sbin/\|/lib/\|/opt/\|/proc/\|/sys/)[^\s;\|]*` | `echo x > /etc/passwd`、`> /boot/grub` | 原有 |
| 11 | `\bchmod\s+(-[a-zA-Z]+\s+)*([0-7]?\s*){1,3}7\s*7\s*7\s+/\w*(\s\|$\|\*)` | `chmod 777 /`、`chmod -R 777 /etc`、`chmod 1777 /` | **重写**（同 #5 技巧）；同样只拦第一层路径 |
| 12 | `^\s*\^[^\s\|;&]+\^[^\s\|;&]+` | `^foo^bar` 自动重执行 | 原有 |
| 13 | `\bcurl\b.*\|\s*(ba\|z)?sh\b` | `curl x \| sh`、`\| bash`、`\| zsh` | **恢复**——2026-09-06 方案 A 确认过的 6 条之一，在后续编辑中丢失 |
| 14 | `\bwget\b.*\|\s*(ba\|z)?sh\b` | `wget x \| sh` 等 | **恢复**（同上） |

不挡的日常命令（已验证）：`ls -la` / `cat /etc/hosts` / `rm /tmp/x.log` / `rm -rf build/` / `rm -rf /tmp/build/nested` / `echo x > /tmp/log` / `chmod 755 script.sh` / `chmod 777 script.sh` / `mv /tmp/foo /tmp/bar` / `mv foo bar 2>/dev/null` / `sudo systemctl status nginx` / `git push origin main` / `kill 1234` / `apt install -y htop` / `grep -r x /usr/share/doc`

**今后给此列表加 pattern 的硬约束：先过 `regexp.Compile`（RE2 语法，无前瞻/后瞻/反向引用），再跑拦截/放行向量，最后才能写入配置**——任何一条编译失败都会导致 gateway 启动失败（hard-fail 语义与上游一致，崩溃循环即发现机制）。

## 五、Peter 启动配置 (deploy 后)

```json
"exec": {
  "enabled": true,
  "enable_deny_patterns": false,
  "enable_custom_deny_patterns": true,    ← 新增
  "allow_remote": true,
  "custom_deny_patterns": [ 14 个 pattern ],
  "custom_allow_patterns": [ 92 个 pattern ],
  "timeout_seconds": 60
}
```

## 六、部署流程

按 Peter MEMORY 标准 fork flow:

```
1. 在 /works/workspace/ai/picoclaw 加 fork 改动 (上面 config.go + shell.go)
2. go build -o /tmp/picoclaw-new ./cmd/picoclaw
3. go test ./pkg/tools/... (验证 shell.go 没破坏)
4. sudo install -m 0755 /tmp/picoclaw-new /usr/bin/picoclaw (md5sum 校验前后一致)
5. systemctl --user restart picoclaw.service
6. 验证: systemd is-active + live 配置跑 guardCommand 端到端确认
   "using custom deny patterns only (default deny patterns disabled)" 生效
   (注意: 该 INFO 行不落 gateway.log/journal, 文件日志级别过滤; 用端到端验证代替)
```

2026-09-07 已按此流程部署完成 (binary md5 `9c30a927f1f6f62e50f77bb9f11b8ee6`)。

## 七、风险评估

| 风险 | 概率 | 影响 | 缓解 |
|------|------|------|------|
| fork 改动破坏现有 exec 行为 | 低 | 高 | go test 全过（tools+config 套件）+ restart 后 live 配置端到端验证通过 |
| regex 误伤 Peter 实际命令 | 中 | 中 | 50 例向量覆盖（24 拦 + 26 放）；真正误伤可 `enable_custom_deny_patterns: false` 一键回滚 |
| regex 编译失败导致 gateway 起不来 | 已发生 | 高 | hard-fail 语义（与上游一致）；2026-09-07 已修复两条 `(?!` 前瞻 pattern；今后新增 pattern 必须先过 RE2 编译验证 |
| regex 没挡住某些变种 | 低 | 中 | 14 条 pattern 覆盖主要高危类；后续可补 |
| upstream picoclaw merge conflict | 中 | 低 | fork 只动 2 个文件（config.go / shell.go）；改动局部, 易 rebase |

## 八、回滚方案

```bash
# 回滚 config (仅关闭 custom-only 模式, 不动 pattern 列表)
# 将 enable_custom_deny_patterns 改为 false 即可, 无需重启二进制

# 完整回滚 config
cp ~/.picoclaw/config.json.bak.pre-custom-deny-switch.20260907 ~/.picoclaw/config.json

# 回滚 fork 改动
cd /works/workspace/ai/picoclaw
git revert 9cc5a3b1

# 重建部署
go build -o /tmp/picoclaw-new ./cmd/picoclaw && sudo install -m 0755 /tmp/picoclaw-new /usr/bin/picoclaw
systemctl --user restart picoclaw.service
```

## 九、长期建议

- 向 upstream picoclaw 提 PR: `enable_custom_deny_patterns` 独立开关 — 这其实是通用需求, 不只 Peter fork 需要
- `custom_allow_patterns` 同问题: 当 `enable_deny_patterns=false` 时, 加载 `customAllowPatterns` 也不强制检查 — 看是否也需要独立 `enable_custom_allow_patterns` 开关 (暂未要求, 不在本次 scope)