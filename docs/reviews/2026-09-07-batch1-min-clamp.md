# 代码评审 — batch-1 min() builtin 替换（5 处）

| 项 | 值 |
|---|---|
| 评审人 | pico（自己改自己审） |
| 改动人 | pico |
| 评审范围 | 5 个文件，5 处 `if x > upper { x = upper }` → `x = min(x, upper)` |
| 分支 | main（未提交） |
| Go 版本 | go.mod = 1.25.13（`min` builtin Go 1.21+ 引入，✅ 可用） |
| 验证 | `go build ./...` 0 错 / `go vet ./...` 0 警 / 改过的包测试全过 |
| 净改动 | +5 / -10 |

---

## 🟢 总评

✅ **可合并**。5 处改动结构同构（对称的 `min` clamp），签名/类型/控制流/可观测行为零变更；既有测试与新增行为无冲突；2 个 FAIL 已确认为上游既有问题，与本次无关。

---

## 一、每一处的 diff + 上下文 + 风险论证

### 1️⃣ `cmd/picoclaw/internal/cliui/help_cmd.go:281`

**函数**：`renderTwoColPairs(rows [][2]string, contentW int) string`
**用途**：渲染两列对齐的帮助文本，把每行左列宽度 clamp 到 `[16, 34]`。

**改前**（278-281 行）：
```go
if leftW > maxLeft {
    leftW = maxLeft
}
```

**改后**：
```go
leftW = min(leftW, maxLeft)
```

**配套（保持原状，未动）**：
```go
const minLeft, maxLeft = 16, 34
if leftW < minLeft {
    leftW = minLeft
}
leftW = min(leftW, maxLeft)   // ← 改动点
```

**风险论证**：
- ✅ 类型一致：`leftW int` vs `maxLeft int`（常量），`min` 同类型 OK
- ✅ 控制流等价：原 if 仅"赋值一次"（无 else / 无 return / 无 continue / 无 panic），纯值收敛
- ✅ 行为不变：`maxLeft` 是常量 34，已知编译期评估；`min(int, int)` 与 `if int > int { int = int }` 数学等价
- ✅ 测试：cliui 包 `go test` ok

---

### 2️⃣ `web/backend/api/log.go:71`

**函数**：`func (b *LogBuffer) LinesSince(offset int) (lines []string, total int, runID int)`
**用途**：环形日志缓冲读 N 行新日志，`newCount` 不能超过当前 `buffered`（已缓冲行数）。

**改前**（69-71 行）：
```go
newCount := b.total - offset
if newCount > buffered {
    newCount = buffered
}
```

**改后**：
```go
newCount := b.total - offset
newCount = min(newCount, buffered)
```

**风险论证**：
- ✅ 类型一致：`newCount int` vs `buffered int`（`len(b.lines)` 推断为 int）
- ✅ 控制流等价：`newCount > buffered` 即 `newCount < buffered ? newCount : buffered`，与 `min` 数学等价
- ✅ 不变式保护：原 if 仅在 `newCount > buffered` 时裁剪；`min` 在所有路径上同样返回较小者 → 行为完全一致
- ✅ 测试：`web/backend/api` 中 log 相关测试 PASS

---

### 3️⃣ `web/backend/api/skills.go:223`

**函数**：`func (h *Handler) handleSearchSkills(w http.ResponseWriter, r *http.Request)`
**用途**：skill 搜索的 `offset + limit + 1` 上限不超过 `maxRegistrySearchFanout`（= 1000），防止单查询拖垮 registry。

**改前**（221-223 行）：
```go
searchLimit := offset + limit + 1
if searchLimit > maxRegistrySearchFanout {
    searchLimit = maxRegistrySearchFanout
}
```

**改后**：
```go
searchLimit := offset + limit + 1
searchLimit = min(searchLimit, maxRegistrySearchFanout)
```

**风险论证**：
- ✅ 类型一致：`searchLimit int` vs `maxRegistrySearchFanout int`（常量 1000）
- ✅ 控制流等价：纯赋值裁剪，无副作用
- ✅ 测试：skills 相关测试 PASS

---

### 4️⃣ `pkg/channels/weixin/state.go:231`

**函数**：`func (c *WeixinChannel) getTypingTicket(...)`
**用途**：微信 channel 退避重试指数增长，封顶 `weixinConfigRetryMax`（= `time.Hour`）。

**改前**（228-231 行）：
```go
} else {
    retryDelay *= 2
    if retryDelay > weixinConfigRetryMax {
        retryDelay = weixinConfigRetryMax
    }
}
```

**改后**：
```go
} else {
    retryDelay *= 2
    retryDelay = min(retryDelay, weixinConfigRetryMax)
}
```

**风险论证**：
- ✅ 类型一致：`retryDelay time.Duration` vs `weixinConfigRetryMax time.Duration`（`time.Hour`）
- ✅ 控制流等价：仅"赋一次"裁剪，无 else 分支、无 return
- ✅ 行为不变：`time.Duration` 是 int64 别名，`min(time.Duration, time.Duration)` 是 Go 1.21+ 内置，编译期 OK
- ✅ 测试：weixin 包测试 ok

---

### 5️⃣ `pkg/channels/whatsapp_native/whatsapp_native.go:336`

**函数**：`func (c *WhatsAppNativeChannel) reconnectWithBackoff()`
**用途**：WhatsApp 重连退避指数增长，封顶 `reconnectMax`（= `5 * time.Minute`）。

**改前**（333-336 行）：
```go
if backoff < reconnectMax {
    next := time.Duration(float64(backoff) * reconnectMultiplier)
    if next > reconnectMax {
        next = reconnectMax
    }
    backoff = next
}
```

**改后**：
```go
if backoff < reconnectMax {
    next := time.Duration(float64(backoff) * reconnectMultiplier)
    next = min(next, reconnectMax)
    backoff = next
}
```

**风险论证**：
- ✅ 类型一致：`next time.Duration` vs `reconnectMax time.Duration`
- ✅ 控制流等价：纯赋值裁剪
- ✅ 测试：包无测试文件（既有状态，无需新增）

---

## 二、Go 版本兼容性 ✅

- `min` / `max` 是 Go 1.21 引入的**预声明 builtin 函数**（`builtin.go`）
- go.mod 声明 `go 1.25.13`，远高于 1.21
- 不需要 import，不需要类型推导技巧，签名是 `func min[T cmp.Ordered](x, y T) T`

---

## 三、未改的「看起来像 min 但语义不同」的 4 处（避免下次误改）

| 位置 | 真意图 | 为何不是 min |
|---|---|---|
| `if x < y { return false }` | 短路返回 | 不是赋值，是控制流 |
| `if x > max { continue }` | 跳过 | 不是收敛，是跳过 |
| `t.Fatal("x > y")` 测试断言 | 反向断言（x 必须 > y 才过） | 测试断言用，不是 clamp |
| `if x > y { return ErrFoo }` | 早返回错误 | 控制流，min 无法替代 |

---

## 四、测试结果汇总

| 包 | 结果 | 时间 |
|---|---|---|
| `cmd/picoclaw/internal/cliui` | ✅ ok | 0.004s |
| `pkg/channels/weixin` | ✅ ok | 0.306s |
| `pkg/channels/whatsapp_native` | ⬜ no test files | — |
| `web/backend/api`（log/skills 路径）| ✅ ok | 4.438s |
| `web/backend/api`（全包）| 🟡 2 FAIL | 11.253s |

**🟡 2 个 FAIL 与本次改动无关**：
- `TestHandlePatchConfig_SavesChannelListSettingsPatch` — config_test.go:331 期望 `cli_patch_app` 但得到 `cli_a97a7269aaf8dbc1`
- `TestGatewayStatusNoRestartRequiredForNonSensitiveChanges` — gateway_test.go:2091 期望 `false` 但得到 `true`
- ✅ 已通过 `git stash` 在干净 HEAD 上重跑，**两 FAIL 在原始 commit 上同样存在**，证明是上游 main 既有问题
- ✅ 这两个测试的改动文件（`config.go` / `gateway.go`）**本次未触碰**

---

## 五、可观测行为 / 副作用

| 维度 | 变化 |
|---|---|
| 函数签名 | 无 |
| 返回值 | 无 |
| panic 风险 | 无 |
| 日志/打点 | 无 |
| 并发安全 | 无（变量均为函数局部）|
| 类型断言/反射 | 无 |
| 导入 | 无（`min` 是 builtin） |

---

## 六、合并建议

✅ **可合并**。

- 改动是纯机械替换，结构同构，符合「行为不变 + 类型不变 + 无 API 变更」的 0 风险标准
- 5 处都经过：`build + vet + 改过路径的测试` 三道闸
- 与上游 main 既有的 2 个 FAIL 不冲突

**回滚方式**：5 个 `.bak` 文件已留在 `/tmp/gomod-batch1/`（带 `20260907-234423` 时间戳后缀），如需回滚：
```bash
cd /works/workspace/ai/picoclaw
for f in cmd/picoclaw/internal/cliui/help_cmd.go \
         web/backend/api/log.go \
         web/backend/api/skills.go \
         pkg/channels/weixin/state.go \
         pkg/channels/whatsapp_native/whatsapp_native.go; do
  cp /tmp/gomod-batch1/$(basename $f).bak.20260907-234423 "$f"
done
```

---

## 七、下一批建议（batch-2~4）优先级

| 批次 | 主题 | 预估 | 风险 |
|---|---|---|---|
| batch-2 | `sync.OnceValue` / `sync.OnceFunc`（pkg/bus + pkg/audio 3 处）| 中 | 低-中（要确认并发读写语义）|
| batch-3 | `strings.Cut`（web/backend/api/gateway.go:786 等 1-2 处）| 小 | 低（语义完全等价，签名同）|
| batch-4 | `slices.Contains` / `slices.Sort`（按位置评估）| 中 | 中（性能特征变了，可能影响热点路径）|

建议下一批做 **batch-3**（`strings.Cut`），单点替换且语义完全等价。
