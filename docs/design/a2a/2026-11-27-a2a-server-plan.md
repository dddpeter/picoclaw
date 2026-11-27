# PicoClaw A2A Server 实现计划

> **面向 AI 代理的工作者：** 按任务顺序逐个实现，步骤使用复选框（`- [ ]`）跟踪。每任务 TDD：先写测试→确认失败→实现→确认通过→commit。

**目标：** 给 PicoClaw gateway 增加标准 A2A v1.0 JSON-RPC HTTP 端点，让外部调用方（GroupBridge 桥、未来任何 agent）用标准协议派活，替代自研 Pico WS 适配器。

**架构：** 在 gateway 既有共享 HTTP mux（现挂 `/health` `/ready` `/reload`，端口 `cfg.Gateway.Port`）上新增 `pkg/a2a` 包，提供 Agent Card 发现 + `message/send` 同步调用。A2A 请求映射到既有 agent loop 入口 `(prompt, channel="a2a", chatID=contextId)`，会话上下文复用 picoclaw 现有 session 持久化——**不新建任何会话机制**。

**技术栈：** Go 1.26（`module github.com/sipeed/picoclaw`），stdlib `net/http` + `encoding/json`，零新依赖。

**规格要点（背景，读这一节就不用翻别的文档）：**
- A2A v1.0 = HTTP + JSON-RPC 2.0。发现：`GET /.well-known/agent-card.json`。调用：`POST <base-url>`，method `message/send`。
- 2026 现状：MCP（agent↔工具）+ A2A（agent↔agent，Linux Foundation）是事实标准栈。picoclaw 当前只有自研 Pico WS（seq ring/会话语义私有），这是 GroupBridge 被迫手搓适配器、出现丢帧/失忆缺陷的根因。
- 验收 oracle：GroupBridge 的 A2A 客户端 `/works/workspace/tools/groupbridge/internal/agents/a2a.go` 已经按 A2A v1.0 说话——实现完拿它当测试客户端，通了就是标准兼容。

## 全局约束

- **不破坏原有功能（铁律）**：Pico WS 通道零改动；`/health` `/ready` `/reload` 响应不变；不加配置时 A2A 完全关闭（`a2a.enabled: false` 默认值）。
- 零新依赖：JSON-RPC 用 stdlib 手搓（就两个方法，不值得引库）。
- 鉴权：Bearer token；未配 token 时拒绝非本机（127.0.0.1）请求。
- 同步模式 MVP：`message/send` 阻塞到 agent 出最终答案。A2A 的 tasks/异步/push-notification 留 P2（见文末升级路径）。
- 每任务独立 commit，格式 `feat(a2a): ...` / `test(a2a): ...`。

## 审查重点（Review Focus）

1. **长思考超时**：agent 执行工具链 >3 分钟时，HTTP 请求不能被服务端中间件/写超时掐断——期待：服务端无固定写超时，时长由客户端 ctx 控制。→ 任务 2 测试钉住。
2. **contextId 复用**：同一 contextId 两次调用，第二次必须带上第一次的会话上下文（追问语义）。→ 任务 2 测试钉住。
3. **无 contextId 首调**：服务端生成 contextId 并在响应中回传，客户端据此复用。→ 任务 2 测试钉住。
4. **鉴权旁路**：配置了 token 时，无 Authorization 头必须 401，不能落到 agent loop。→ 任务 3 测试钉住。
5. **并发同 contextId**：同一会话并发两个请求不能交叉污染上下文——期待：串行化（per-context mutex）或明确拒绝第二个请求（429/JSON-RPC error）。→ 任务 2 测试钉住。

---

## 文件结构

```
pkg/a2a/
  types.go        # JSON-RPC 信封 + A2A message/task 结构体
  card.go         # Agent Card 构建 + GET /.well-known/agent-card.json
  server.go       # message/send handler + 鉴权 + per-context 串行化
  server_test.go  # 任务 1-3 的全部测试
  card_test.go
pkg/gateway/gateway.go  # 修改：mux 注册 a2a server（约 :224 health 注册处旁边）
pkg/config/config.go    # 修改：新增 Gateway.A2A { Enabled bool; Token string }
docs/design/a2a/        # 本计划所在目录
```

---

### 任务 1：类型 + Agent Card 端点

**文件：** 创建 `pkg/a2a/types.go`、`pkg/a2a/card.go`、`pkg/a2a/card_test.go`

- [ ] **步骤 1：编写失败的测试**（`card_test.go`）

```go
func TestAgentCard(t *testing.T) {
    card := AgentCard("虾酱", "PicoClaw 虾酱", "http://127.0.0.1:18790", true)
    // 断言：protocolVersion=="1.0"，url==base，capabilities.streaming==false，
    // securitySchemes 含 bearer，defaultInputModes==["text"]，defaultOutputModes==["text"]
}
func TestCardHandler(t *testing.T) {
    // httptest GET /.well-known/agent-card.json → 200，body 可反序列化为 AgentCard
}
```

- [ ] **步骤 2：运行 `go test ./pkg/a2a/ -run TestAgent`，预期 FAIL（未定义）**
- [ ] **步骤 3：实现**

```go
// types.go
type JSONRPCRequest struct {
    JSONRPC string          `json:"jsonrpc"` // 恒 "2.0"
    ID      json.RawMessage `json:"id"`
    Method  string          `json:"method"`
    Params  json.RawMessage `json:"params"`
}
type JSONRPCResponse struct {
    JSONRPC string          `json:"jsonrpc"`
    ID      json.RawMessage `json:"id,omitempty"`
    Result  any             `json:"result,omitempty"`
    Error   *JSONRPCError   `json:"error,omitempty"` // {code, message}
}
type A2AMessage struct {
    Role      string    `json:"role"` // "user" | "agent"
    Parts     []A2APart `json:"parts"`
    MessageID string    `json:"messageId"`
    ContextID string    `json:"contextId,omitempty"`
}
type A2APart struct {
    Kind string `json:"kind"` // MVP 仅 "text"
    Text string `json:"text"`
}
// AgentCard 按 A2A v1.0 spec：name, description, protocolVersion, url,
// capabilities{streaming:false, pushNotifications:false}, securitySchemes, defaultInputModes/OutputModes
```

- [ ] **步骤 4：`go test ./pkg/a2a/` 预期 PASS**
- [ ] **步骤 5：Commit** `feat(a2a): types + agent card endpoint`

### 任务 2：message/send → agent loop 映射

**文件：** 创建 `pkg/a2a/server.go`、`pkg/a2a/server_test.go`；本任务需要 gateway 传入的注入点

**注入点签名（实现前与 gateway 现状核对，gateway.go:976 的 heartbeat handler 就是同型入口）：**

```go
// AskFunc 是 A2A server 需要的唯一 agent loop 入口。
type AskFunc func(ctx context.Context, prompt, channel, chatID string) (string, error)
// channel 恒传 "a2a"；chatID == contextId
```

- [ ] **步骤 1：编写失败的测试**（`server_test.go`，AskFunc 用 stub）

```go
func TestMessageSend(t *testing.T) {
    // stub AskFunc 记录 (prompt, channel, chatID)，返回 "答案文本"
    // POST message/send：params.message{role:"user",parts:[{kind:"text",text:"你好"}],messageId:"m1"}
    // 无 contextId → 断言 stub 收到非空 chatID（服务端生成的 uuid）
    //              → 响应 result 是 A2AMessage：role=="agent"，parts[0].text=="答案文本"，contextId 回传
}
func TestMessageSendContextReuse(t *testing.T) {
    // 带 contextId:"ctx-1" 两次调用 → stub 两次都收到 chatID=="ctx-1"
}
func TestMessageSendSerialization(t *testing.T) {
    // stub AskFunc 阻塞 200ms；并发两个同 contextId 请求
    // 断言：第二个请求串行等待（总耗时 ≥ 200ms）或收到 JSON-RPC error（实现者二选一，写明选了哪个）
}
func TestMessageSendError(t *testing.T) {
    // AskFunc 返回 error → 响应含 JSONRPCError（code -32000 级别，message 带根因链）
}
```

- [ ] **步骤 2：运行 `go test ./pkg/a2a/ -run TestMessage`，预期 FAIL**
- [ ] **步骤 3：实现 `server.go`**——handler 逻辑：解析 JSONRPCRequest → method 不符返回 -32601 → 提取 message → 无 contextId 生成 uuid → 调 AskFunc → 包装 A2AMessage 回 result。per-context 串行化用 `map[string]*sync.Mutex` + 全局锁保护 map。**服务端 http.Server 不设 WriteTimeout**（审查重点 1）。
- [ ] **步骤 4：`go test ./pkg/a2a/` 全 PASS**
- [ ] **步骤 5：Commit** `feat(a2a): message/send mapped to agent loop via AskFunc`

### 任务 3：鉴权 + 配置

**文件：** 修改 `pkg/config/config.go`、`pkg/a2a/server.go` + 测试

- [ ] **步骤 1：编写失败的测试**

```go
func TestAuthRequired(t *testing.T) {
    // server 配 token="s3cret"：无 Authorization 头 → 401 + body JSONRPCError；
    // Authorization:"Bearer s3cret" → 通过到 AskFunc。
}
func TestLocalBypass(t *testing.T) {
    // 配 token 且请求来自 httptest（非 127.0.0.1 判定按 RemoteAddr）→ 按 spec 实现者核对：
    // 本机豁免仅在 RemoteAddr 为 127.0.0.1/::1 时生效，写明行为。
}
```

- [ ] **步骤 2：预期 FAIL**
- [ ] **步骤 3：实现**——config 新增（跟随 picoclaw config 拒未知字段的惯例，确认 tag 名）：

```go
type A2AConfig struct {
    Enabled bool   `yaml:"enabled"` // 默认 false
    Token   string `yaml:"token"`   // 空 = 仅本机可调
}
// 挂在 GatewayConfig 下：Gateway.A2A
```

- [ ] **步骤 4：全 PASS**
- [ ] **步骤 5：Commit** `feat(a2a): bearer auth + gateway.a2a config`

### 任务 4：gateway 接线

**文件：** 修改 `pkg/gateway/gateway.go`（health 注册点 :224 附近）、`config/` 示例、`README.md`

- [ ] **步骤 1：接线**——`cfg.Gateway.A2A.Enabled` 为 true 时，在共享 mux 注册 `card.go` + `server.go` 的两个 handler，AskFunc 传既有 agent loop 入口（与 heartbeat handler :976 同型，channel="a2a"）；disabled 时零注册。
- [ ] **步骤 2：验证不破坏原功能**

```bash
go build ./... && go test ./...   # 全仓回归，全绿
# 本地起 gateway（a2a.enabled 默认 false）：/health /ready 响应与改动前逐字节一致
curl -s localhost:18790/health && curl -s localhost:18790/ready
```

- [ ] **步骤 3：手测开闸**——`a2a.enabled: true` + token 后：

```bash
curl -s localhost:18790/.well-known/agent-card.json | jq .protocolVersion   # "1.0"
curl -s -X POST localhost:18790 -H 'Authorization: Bearer <token>' \
  -d '{"jsonrpc":"2.0","id":1,"method":"message/send","params":{"message":{"role":"user","parts":[{"kind":"text","text":"冒烟：报出你的名字"}],"messageId":"smoke-1"}}}' | jq .result.parts[0].text
```

- [ ] **步骤 4：README 增补 A2A 一节**（端点、配置项、curl 示例）
- [ ] **步骤 5：Commit** `feat(a2a): gateway wiring + docs`

### 任务 5：端到端验收（用 GroupBridge 当 oracle）

**文件：** 无代码；产出验收记录贴回协作群

- [ ] **步骤 1：桥切通道**——`/works/workspace/tools/groupbridge/configs/groupbridge.yaml` 把 xiajiang/ailoshi 的类型从 picoclaw 改 a2a（endpoint 不变，token 填 gateway.a2a.token），重建 `bin/groupbridge`
- [ ] **步骤 2：四路冒烟**——`./bin/groupbridge-smoke -agent <key>` × 4（xiajiang/ailoshi 走新 A2A 路径，xiaoaima/xiazai 原 A2A 不动），全部 SMOKE_OK
- [ ] **步骤 3：上下文验收**——smoke 连发两问：「我叫什么名字」（先报名字）→「我刚才说我叫什么」——第二问答案必须含第一问的名字（contextId 复用生效）
- [ ] **步骤 4：Pico WS 回归**——切回 picoclaw 类型再跑一次冒烟，确认旧通道未受影响
- [ ] **步骤 5：验收记录 commit** `docs(a2a): e2e acceptance record`

## 升级路径（P2，本期不做）

- `message/stream`（SSE）替代同步阻塞——治长任务客户端超时
- `tasks/get` + push notification（A2A 异步任务语义）
- GroupBridge 桥删除 `internal/agents/picoclaw.go`（A2A 全覆盖后，桥瘦身为纯飞书↔A2A 转发器）

---
*计划人：小爱马 🐴 · 2026-11-27 · 侦察基线：picoclaw HEAD `3a6966d9`，验收 oracle `groupbridge/internal/agents/a2a.go`*
