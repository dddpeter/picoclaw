# [BUG] `exec.Command(...).Start()` 未调用 `Wait()`，导致子进程退出后残留僵尸进程

## Quick Summary

`OpenBrowser` 等函数用 `exec.Command(...).Start()` 启动 `xdg-open` 后**从不调用 `Wait()`**。Go 运行时不会自动回收子进程，父进程（`picoclaw-launcher`）也没有 reap 逻辑，导致每次打开浏览器链接都在进程表里留下一条 `<defunct>` 僵尸记录。

**实测**：机器连续运行 1 天 12 小时后，`picoclaw-launcher` 下累积 2 条僵尸：

```
32915   3882296 Z    [xdg-open] <defunct>   12:32:19
103341  3882296 Z    [xdg-open] <defunct>   08:19:11
```

僵尸不占内存（RSS = 0），但**永久占用 PID 号**且永不回收——只有重启父进程才会消失。长期运行的服务进程上会持续累积。

## Environment & Tools

- **PicoClaw Version:** v0.6.1（commit `043870ce`）
- **Go Version:** go1.26.8
- **Operating System:** Ubuntu 22.04 / Linux x86_64（Wayland 会话）
- **父进程:** `/usr/bin/picoclaw-launcher -port 18800 -no-browser`（PID 3882296）
- **复现路径:** 任何触发 OAuth 浏览器授权、或 launcher 前端「打开浏览器」的动作

## 📸 Steps to Reproduce

1. 启动 `picoclaw-launcher`（Web UI，:18800）
2. 触发任意 `OpenBrowser` 调用（OAuth 登录跳转 / 前端打开外部链接）
3. 等待 `xdg-open` 进程退出（通常 1-2 秒）
4. 观察进程表：

```bash
ps -eo pid,ppid,stat,etime,args | awk '$2==<launcher_pid>'
#   PID  PPID  STAT  ELAPSED  CMD
# 32915 38822 6 Z    12:32:19 [xdg-open] <defunct>
```

5. 重复步骤 2-4，僵尸数单调递增，**只有重启 launcher 才会清零**

## ❌ Actual Behavior

- 每调用一次 `OpenBrowser`，进程表永久增加一条 `[xdg-open] <defunct>`
- `kill -CHLD <launcher_pid>` **无效**——launcher 没有 `Wait()`/reap 循环，收不到 SIGCHLD 也不会回收
- 进程数单调增长，无自愈路径

## ✅ Expected Behavior

调用 `OpenBrowser` 后不残留任何僵尸进程。

## 🔍 Root Cause

Go 的 `os/exec` 语义：`Cmd.Start()` 只启动进程，**不负责回收**。必须显式调用 `Cmd.Wait()`（或设置 `Cmd.WaitDelay` + 后续 `Wait()`）来释放系统资源并避免僵尸。

### 受影响位置（短期子进程，漏了 `Wait()`）

| 文件 | 行 | 代码 | 备注 |
|---|---|---|---|
| `pkg/auth/oauth.go` | 678 | `exec.Command("open", url).Start()` | macOS 分支，同样漏 |
| `pkg/auth/oauth.go` | 680 | `exec.Command("xdg-open", url).Start()` | **Linux — 实测残留源** |
| `pkg/auth/oauth.go` | 682 | `exec.Command("cmd", "/c", "start", url).Start()` | Windows 分支，同样漏 |
| `web/backend/utils/runtime.go` | 151 | `exec.Command("xdg-open", url).Start()` | **Linux — launcher 实际调用点（实测僵尸父进程即 launcher）** |
| `web/backend/utils/runtime.go` | 152 | `LauncherExecCommand("rundll32", ...).Start()` | Windows 分支，同样漏 |
| `web/backend/utils/runtime.go` | 154 | `exec.Command("open", url).Start()` | macOS 分支，同样漏 |

> 两个 `OpenBrowser` 实现重复（`pkg/auth/oauth.go` 与 `web/backend/utils/runtime.go`），修复时建议一并处理。

### 已核查、**不是**本问题的位置

| 文件 | 结论 |
|---|---|
| `pkg/lsp/spawn.go:51` | LSP server 是**长期进程**（生命周期跟随 gateway），非本 bug |
| `pkg/channels/telegram/telegram.go:182` | `bh.Start()` 是 telego 的长驻 handler，非 exec.Cmd |
| `pkg/channels/deltachat/rpc.go` / `pkg/isolation/runtime.go` / `pkg/devices/sources/usb_linux.go` | 已有 `Wait()` 配套或属长驻进程 |

## 💡 Suggested Fix

在 `Start()` 之后异步 `Wait()`，避免阻塞调用方（浏览器启动是 fire-and-forget 语义）：

```go
// pkg/auth/oauth.go
func OpenBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "linux":
		cmd = exec.Command("xdg-open", url)
	case "windows":
		cmd = exec.Command("cmd", "/c", "start", url)
	default:
		return fmt.Errorf("unsupported platform: %s", runtime.GOOS)
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	// Reap the child so it does not linger as a zombie.
	go func() { _ = cmd.Wait() }()
	return nil
}
```

`web/backend/utils/runtime.go` 的 `OpenBrowser` 同样处理。

**补充建议**：给 launcher 加一个 `SIGCHLD` handler 或周期性 `wait4(-1, …)` 做兜底 reap，避免其它路径再产生未回收子进程。

## 🎯 Verification

修复后执行：

```bash
# 反复触发 10 次 OpenBrowser，然后检查
for i in $(seq 10); do <触发一次打开浏览器>; done
ps -eo pid,ppid,stat,args | awk '$2==<launcher_pid> && $3 ~ /^Z/'
# 期望输出：空（0 条僵尸）
```

## 📎 Related

- 现象与本仓库 `docs/operations/troubleshooting.zh.md` 的「进程残留」排查项相关，可补充僵尸进程条目。
- 该问题影响所有以 `picoclaw-launcher` 为父进程长期运行的服务实例。
