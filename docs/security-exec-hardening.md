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

文件 `/home/dddpeter/.picoclaw/config.json` 已于 2026-09-07 修改:
- `custom_deny_patterns`: 6 → **14** 个 (备份 `config.json.bak.pre-deny.20260907_150224`)
- `custom_allow_patterns`: 5 → **92** 个 (备份 `config.json.bak.20260907_145156`)

新增的 8 个 `custom_deny_patterns` (覆盖 zhihu 10 个危险命令):

| # | pattern | 拦截 |
|---|---------|------|
| 7 | `\brm\s+(-\S+\s+)*(--\S+\s+)*/(?!/)\w*(?!/)(?:\s\|$|\*)` | `rm -rf /` `rm -rf /tmp` `rm -rf /etc` |
| 8 | `\brm\s+(-\S+\s+)*(--\S+\s+)*\.\s*$` | `rm -rf .` |
| 9 | `\brm\s+(-\S+\s+)*(--\S+\s+)*\*\s*$` | `rm -rf *` |
| 10 | `\bmv\b[^\|;]*\s+/dev/null\b` | `mv /home /dev/null` |
| 11 | `\bmkfs(\.\w+)?\s+/dev/(sd[a-z]\|...)` | `mkfs.ext3 /dev/sda` `mkfs.xfs /dev/nvme0n1` |
| 12 | `(^\|[^\|;&\w/])\s*>\s*(/etc/\|/boot/\|...)[^\s;\|]*` | `> /etc/passwd` `> /boot/grub` |
| 13 | `\bchmod\s+(-[a-zA-Z]+\s+)*([0-7]?\s*){1,3}7\s*7\s*7\s+/(?!/)\w*(?!/)(?:\s\|$|\*)` | `chmod 777 /` `chmod -R 777 /etc` |
| 14 | `^\s*\^[^\s\|;&]+\^[^\s\|;&]+` | `^foo^bar` 自动重执行 |

不挡的命令 (已测试 22+ 例, 全过): `ls -la` / `cat /etc/hosts` / `rm /tmp/x.log` / `rm -rf /tmp/build` / `echo x > /tmp/log` / `chmod 755 script.sh` / `mv /tmp/foo /tmp/bar`

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
1. 在 /works/workspace/ai/picoclaw 加 fork 改动 (上面 schema + shell.go)
2. go build → 产出 binary
3. go test ./pkg/tools/... (验证 shell.go 没破坏)
4. install 到 /usr/bin/picoclaw (= picoclaw-nr-gateway)
5. md5sum 验证 binary hash
6. systemctl --user restart picoclaw.service
7. journalctl --user -u picoclaw.service -n 50 确认启动 + 看到 "using custom deny patterns only" log
```

## 七、风险评估

| 风险 | 概率 | 影响 | 缓解 |
|------|------|------|------|
| fork 改动破坏现有 exec 行为 | 低 | 高 | go test 全过 + restart 后立刻确认 picoclaw 响应 |
| regex 误伤 Peter 实际命令 | 中 | 中 | 22+ 测试用例覆盖, Peter 实际命令都过; 真正误伤可 disable 开关回滚 |
| regex 没挡住某些变种 | 低 | 中 | 14 个 pattern 覆盖 zhihu 文章所有 10 类; 后续可补 |
| upstream picoclaw merge conflict | 中 | 低 | fork 只动 2 个文件; 改动局部, 易 rebase |

## 八、回滚方案

```bash
# 回滚 config
cp ~/.picoclaw/config.json.bak.pre-deny.20260907_150224 ~/.picoclaw/config.json

# 回滚 fork 改动
cd /works/workspace/ai/picoclaw
git checkout HEAD -- pkg/config/schema.go pkg/tools/shell.go

# 重建
go build -o /usr/bin/picoclaw-nr-gateway
systemctl --user restart picoclaw.service
```

## 九、长期建议

- 向 upstream picoclaw 提 PR: `enable_custom_deny_patterns` 独立开关 — 这其实是通用需求, 不只 Peter fork 需要
- `custom_allow_patterns` 同问题: 当 `enable_deny_patterns=false` 时, 加载 `customAllowPatterns` 也不强制检查 — 看是否也需要独立 `enable_custom_allow_patterns` 开关 (暂未要求, 不在本次 scope)