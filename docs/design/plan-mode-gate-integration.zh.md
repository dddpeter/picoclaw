# P2-1 设计：Plan Mode 协议化 —— Modes 状态机 × 审批闸门联动

> 状态：设计稿（未实施）｜ 提案来源：docs/reviews/2026-10-09-prompt-enhancement-proposal.zh.md §四
> 参考：Devin Prompt（planning/standard 协议级硬状态）
> 结论预览：**不需要新建执行机制**——turn profile 基础设施已具备全部物理能力（`tools.mode: custom` 白名单 + `filterToolsByTurnProfile` 物理过滤），Plan Mode = 在其上加一层会话级模式状态机 + 审批闸门确认切换。

## 一、现状与缺口

### 已有的物理能力（2026-10-09 读码实测）

| 能力 | 位置 | 说明 |
|---|---|---|
| 工具白名单物理过滤 | `pkg/agent/turn_profile_policy.go` `filterToolsByTurnProfile()` | `ToolsMode=custom` 时只放行 `AllowedTools`，**在 tool defs 层面移除**（模型根本看不见别的工具） |
| 白名单二次拦截 | 同文件 `turnProfileToolAllowed()` / `denyToolByTurnProfile()` | tool loop 内兜底，防 provider 层漏过滤 |
| 技能白名单 | 同文件 `filterNamesByTurnProfile()` | planning 态可同步收窄技能 |
| 配置结构 | `pkg/config/turn_profile.go` `TurnProfileConfig` | `enabled` + history/system_prompt/skills/tools 四块，各自 default/off/custom |
| prompt 注入点 | `pkg/agent/prompt_turn.go` `promptBuildRequestForTurn()` | turn 级 PromptBuildRequest 已按 profile 设置 `AllowedTools`/`SuppressToolUseRule` 等 |

### 缺口

1. turn profile 是**配置级静态**的（config.json），非**会话级动态**——一个 session 无法在 planning/standard 间切换
2. 模式切换无审批确认——用户说"开始规划"模型就自己切
3. 提示词不知道当前模式——模型无法被告知"你在 planning 态"

## 二、设计

### 2.1 数据模型

```
pkg/agent/agent_mode.go（新文件）

type AgentMode string

const (
    AgentModeStandard AgentMode = "standard"
    AgentModePlanning AgentMode = "planning"
)

// ModeState 挂在 turnState 上（ts.mode），会话级生命周期。
type ModeState struct {
    Current     AgentMode
    Transitions int          // 本会话切换次数（防抖：上限 5）
    PendingTo   AgentMode    // 待审批的目标模式（非空 = 有切换在途）
}
```

### 2.2 PromptBuildRequest 扩展

```go
// pkg/agent/prompt.go PromptBuildRequest 增加字段：
type PromptBuildRequest struct {
    ...
    AgentMode AgentMode // planning/standard；空 = standard（兼容旧调用方）
}
```

turn 层注入模式说明（`prompt_turn.go` `promptBuildRequestForTurn()`）：

```go
if ts.mode.Current == AgentModePlanning {
    req.AgentMode = AgentModePlanning
}
```

prompt 渲染时 planning 态追加 turn 层 part（`PromptLayerTurn` + `PromptSlotRuntime`，注册新 source `runtime.mode`，registry placement 校验通过）：

```
# CURRENT MODE: PLANNING
You are in planning mode. Only read-only tools are available (this is
enforced physically — you cannot call write tools even if you try).
Gather information, then propose a plan. When the plan is ready, ask
the user to approve execution; mode switching requires their approval.
```

### 2.3 物理收窄（复用 turn profile 管线）

planning 态生效的等效 profile（**不改 config，代码内构造**）：

```go
// planningModeProfile 是 planning 态的物理白名单：只读集。
var planningModeProfile = config.EffectiveTurnProfile{
    Enabled:    true,
    ToolsMode:  config.TurnProfileModeCustom,
    AllowedTools: []string{
        // 读类
        "fs_read", "read_file", "list_dir", "search",
        "web_search", "grep", "glob",
        // 交互类（planning 需要问用户）
        // 无——问用户走正常 LLM 回复，不是工具
    },
    SkillsMode: config.TurnProfileModeDefault, // 技能不收窄（技能可能含读操作）
}
```

生效路径：`ts.profile` 解析处（pipeline_setup.go:50 一带）加一层：

```go
effective := resolveEffectiveProfile(cfg, ts.mode)
toolDefs := filterToolsByTurnProfile(ts.agent.Tools.ToProviderDefs(), effective)
```

`resolveEffectiveProfile`：`ts.mode.Current == planning` 时返回 planning 白名单与用户配置的**交集**（用户显式关掉的工具不能被 plan mode 复活）；standard 态返回用户配置原样。

### 2.4 模式切换协议（审批闸门联动）

```
用户/模型请求切换（"plan 就绪，请批准执行"）
        │
        ▼
ModeTransition 请求 → 走既有 ApproveTool 闸门
        │              （ToolApprovalRequest{Tool: "mode.switch", Arguments: {to: "standard"}}）
        ▼
审批通过 ──→ ts.mode.Current = standard，Transitions++
            denied tool result 写回："Mode switch approved. You are now in standard mode."
        │
审批拒绝 ──→ 不切换，denied result 带话（hookDeniedDeclinedSuffix 已有）：
            "Mode switch declined — stay in planning mode. Adjust the plan instead."
```

- 复用 `hooks.ApproveTool` 闸门：零新审批通道，高风人审 hook 自动覆盖模式切换
- planning→standard 必须过闸门；standard→planning **免审批**（收紧方向永远自由，放宽方向必须过闸——与 deny_profile "只能收紧"哲学一致）
- 切换上限 5 次/会话，防抖

### 2.5 状态机

```
                 免审批（收紧）
    ┌─────────┐ ─────────────► ┌─────────┐
    │standard │                │planning │
    └─────────┘ ◄───────────── └─────────┘
                 审批闸门（放宽）
                 ApproveTool("mode.switch")
                 拒绝 = 留在 planning
                 （denied 带话：调整计划，别原样重试）
```

### 2.6 时序（闸门交互）

```
模型         turnState      ApproveTool闸门      用户
 │ ts.mode=planning            │                │
 │ 提议: <suggest_plan/>       │                │
 │ 请求 mode.switch ──────────►│ 发起审批 ──────►│
 │                             │                │ 点击批准/拒绝
 │ ◄────────── approved/denied ┤◄───────────────│
 │ approved: profile 解除收窄，  │                │
 │   prompt 注入 STANDARD 说明  │                │
 │ denied: 留在 planning，      │                │
 │   denied result 带 declined  │                │
 │   语义（勿原样重试）          │                │
```

## 三、影响面清单

| 文件 | 改动 | 风险 |
|---|---|---|
| `pkg/agent/agent_mode.go` | 新增：ModeState + 白名单常量 + resolveEffectiveProfile | 低（纯新增） |
| `pkg/agent/prompt.go` | PromptBuildRequest 加 `AgentMode` 字段；registry 注册 `runtime.mode` source | 低（空值兼容） |
| `pkg/agent/prompt_turn.go` | promptBuildRequestForTurn 注入 AgentMode | 低 |
| `pkg/agent/pipeline_setup.go` | profile 解析加 planning 分支 | 中（核心管线，需测试覆盖） |
| `pkg/agent/agent_command.go` 或新 command | `/plan`、`/execute` 命令入口 | 低 |
| `pkg/agent/hooks.go` | mode.switch 走 ApproveTool | 低（复用） |

**显式不做**：
- 不加 config 字段（mode 是会话态不是配置态；遵循"禁用插件功能优先改源码默认值，不加 config.yaml 项"的项目纪律）
- 不动 turn profile 的配置语义（用户配置的收紧在 planning 态继续生效，取交集）
- 不做模式持久化（会话结束即失效；跨会话记忆走 MEMORY.md 自然的记忆机制）

## 四、测试计划

1. `TestPlanningModePhysicallyNarrowsTools`：planning 态下 `filterToolsByTurnProfile` 输出不含写类工具（`exec`/`fs_write`/消息发送类）
2. `TestModeSwitchRequiresApproval`：planning→standard 无审批时 `ts.mode.Current` 不变
3. `TestModeSwitchDeniedCarriesDeclinedSemantics`：闸门拒绝后 denied result 含 `hookDeniedDeclinedSuffix()`
4. `TestStandardToPlanningNoApproval`：收紧方向免审批直切
5. `TestPlanningModePromptInjection`：planning 态 prompt 含 `CURRENT MODE: PLANNING` 说明
6. `TestModeTransitionCap`：第 6 次切换被拒
7. 交集语义：用户配置 off 的工具在 planning 态仍不可见

## 五、验收（对齐提案 §六-5）

- 本文档为唯一交付物，含状态机图（§2.5）与闸门交互时序（§2.6）✅
- 实施另立 PR，按 §四测试计划先行 TDD
