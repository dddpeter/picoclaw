# picoclaw 开箱即用改进 — 需求与可行性评估

- 日期：2026-09-09
- 状态：**P0 已实施（09-09）：P0-4 并发默认 3 ✅ / P0-3 deny_profile 档位 ✅ / P0-2 doctor 命令 ✅ / P0-1 首启向导 ✅**
- 项目：dddpeter/picoclaw fork（main @ 60e7a307，upstream bbf6893c）
- 总纲：让 picoclaw 开箱即用，减少用户使用成本
- 文档位置：本项目 `docs/design/out-of-box-improvements.zh.md`（2026-09-09
  经 Peter 决定从 vault 迁入，需求文档随项目走）

---

## 1. 背景与问题

新用户从「下载 picoclaw-launcher」到「第一次聊上天」之间存在体验断点：

1. **onboard 只生成骨架**：`picoclaw onboard` 生成 config.json 后，提示语是
   「Add your API key to <config-path>」——要求用户手改 JSON。对非开发者
   用户这是最大流失点。
2. **WebUI 有引导无代办**：tour（welcome→models→gateway→docs）只是指路
   气泡，不代填配置；新用户还是要自己摸索 Models 页各字段。
3. **故障排查无入口**：模型不通/频道凭据错/端口占用时，用户只能翻日志。
4. **默认模型单点**：主模型挂了 = 服务不可用（fork 已有 watchdog 拉起进程，
   但模型层面无降级）。

## 2. 现状盘点（可行性的事实基础）

已核实的代码底子（均已在 main 分支）：

| 能力 | 位置 | 状态 |
|---|---|---|
| 模型连通性探测 | `web/backend/api/models.go` `probeModelConnectivity`（真实网络探测）+ 前端三态 `available/unconfigured/unreachable` | ✅ 现成 |
| 拉取上游模型列表 | `POST /api/models/fetch` + model_catalog.go（按 provider+base+key 缓存目录） | ✅ 现成 |
| Provider 预设包 | `pkg/config/defaults.go` **32 个 ModelConfig 预设**：智谱/月之暗面/DeepSeek/OpenAI/Anthropic/Gemini…全带 key 申请链接注释、api_base、推荐 model id | ✅ 现成（含国内主流） |
| Provider 选项 API | `ModelProviderOption`（id/display_name/default_api_base/create_allowed…） | ✅ 现成 |
| 配置写入 | `PATCH /api/config`（RFC 7396 merge-patch，含安全字段恢复+校验+写锁） | ✅ 现成 |
| 首启检测 | launcher `EnsureOnboarded`（无 config 时自动跑 onboard） | ✅ 现成 |
| Tour 引导 | `web/frontend/src/components/tour/`（4 步指路气泡） | ✅ 现成（仅指路） |
| Fallback 链 | upstream `bbf6893c`「configurable default fallback chain」已合入 fork | ✅ 已可用 |
| 更新器 | `picoclaw update` 命令（updater） | ✅ 现成 |
| JSON 诊断 | `pkg/config/diagnostics.go`（解码错误行号/列号定位） | ✅ 现成（仅 config 解析层） |

**缺的**：把上述能力串成「一条流水线」的 UI/CLI 编排层，以及跨子系统
的健康聚合检查（doctor）。

## 3. 候选举措与可行性

### P0-1 WebUI 首启向导（Setup Wizard）⭐ 核心

**内容**：launcher 检测「无任何 available 模型」→ 聊天页/首页弹引导卡：
选 provider（复用预设包，展示 32 个含国内的选项）→ 填 API key →
一键「测试连接」（probe）→ 拉模型列表选一个设为默认 → 当场试聊。

**可行性：高（纯前端）**
- 后端 API 零改动：probe / models fetch / PATCH config / provider options 全现成
- 前端新增 wizard 组件 + 一个「无可用模型」状态检测（models API 三态已给）
- 与 tour 不冲突：wizard 是首次配置代办，tour 是界面指路

**工作量**：1.5~2 天（含 5 语言 i18n、移动端适配）
**风险**：key 输入安全（仅内存传递、不落日志）；已配置用户不应被骚扰
（检测条件要精确：`所有模型 unconfigured` 才弹，可关闭）。

### P0-2 `picoclaw doctor` 一键体检

**内容**：新 CLI 子命令，聚合输出：
config.json 合法性（复用 diagnostics）→ 默认模型可达性（probe）→
已启用频道凭据存在性 → gateway 端口占用 → workspace 可写 →
二进制版本/更新提示。

**可行性：高（新增但底料全有）**
- 各项检查函数均已有实现（probeModelConnectivity、config 校验、status
  框架），doctor 只是编排 + 格式化输出（✅/❌/⚠️ + 修复建议）
- CLI 框架（cobra + cliui 面板样式）现成，与既有命令观感一致

**工作量**：0.5~1 天
**收益**：支持场景剧增——用户报障先跑 doctor 自助定位；我们远程支持
也让用户贴 doctor 输出即可。

### P0-3 安全等级选择器（Security Profile）⭐ 新增高优

**痛点**：新安装默认一刀切严格（上游行为），用户要「好用」必须逐项放开
大量安全配置；想收紧的用户同样只能手改散落的开关。缺「用户自选宽松度」
的机制。

**现状事实（已核实代码）**：
- 上游默认 = `restrict_to_workspace: true` + **41 条 deny 模式**——含
  `sudo`、`$()`/`${}` 展开、`|sh` 管道、`chmod/chown`、`pkill/kill`、
  `apt/yum/dnf install`、`npm -g`/`pip --user`、`docker run/exec`、
  `git push`、`ssh user@`、`eval`、`source .sh`、heredoc——对编码 agent
  基本不可用
- fork 默认（`b09347cc`）= restrict 关 + 毁灭性-only 拦截集（rm -rf 组合、
  磁盘抹除、块设备写、fork 炸弹、关机、系统目录写、curl|sh、ssh 凭据
  覆写）——「安全是护栏非门槛」
- 两套 pattern 集都在代码库里（fork 集在 main，上游集在 git 历史）
- `restrict_to_workspace`、`enable_deny_patterns`、`isolation.enabled`
  均为独立散开关，无档位概念

**内容**：定义命名档位 + 三个入口让用户自选：

| 档位 | deny 集 | 文件系统 | 定位 |
|---|---|---|---|
| `open`（开放，fork 默认） | 毁灭性-only（~20 条） | 全盘漫游（OS 目录写保护） | 编码主力机 |
| `balanced`（平衡） | 毁灭性-only | 读写限 workspace | 共享机/谨慎用户 |
| `strict`（严格，上游等价） | 上游 41 条全集 | workspace 沙箱 | 高敏环境 |

三个入口：
1. **onboard CLI**：交互问一句安全等级（默认 open），一步写入档位
2. **WebUI 首启向导**：P0-1 向导中加一步「安全等级」三卡片选择（含
   各档影响清单说明 + 推荐标记）
3. **设置页**：安全区显示当前档位胶囊 + 一键切换（切换前弹影响 diff
   确认）

**技术形态**：
- config 新增 `tools.exec.deny_profile: "open"|"balanced"|"strict"` 枚举
- pattern 装载器按档位选集；`enable_deny_patterns` 保留做迁移兼容
  （`true`+无 profile → 视为 open）
- 档位映射：`balanced`/`strict` 顺带置 `restrict_to_workspace: true`；
  `strict` 可选叠 `isolation.enabled`（二期评估）

**可行性：高**
- 两套 pattern 集零新写（fork 集现役，上游集从 git 历史复活为 strict 集）
- restrict/isolation 开关现成，档位只是编排层
- 与 P0-1 向导同批落地最顺（同一次 UI 编排工作）

**工作量**：后端 0.5 天（枚举+装载+迁移）+ 前端 0.5 天（向导步+设置区）+
onboard CLI 0.5 天 ≈ **1.5 天**

**风险与边界**：
- 档位选择器只能**收紧**不能低于 fork 底线（毁灭性拦截永远在）——
  「别配置绕法关 deny」红线不破
- 与上游 rebase 冲突面：shell.go/defaults.go 本就是 fork 差异化文件，
  profile 枚举会略扩大，但这是核心 fork 价值，值得
- 对上游提 PR 视角：profile 机制对上游是纯增量（上游可默认 strict），
  有被接受的可能

### P0-4 并发默认值放开（max_parallel_turns）

**痛点**：新安装默认串行，用户感知为「agent 反应慢、排队」：
1. **跨 session 串行**：`max_parallel_turns` 未写 → 0 → 兜底 1，A 会话
   跑长任务时 B 会话干等（上游/fork 新装皆如此）
2. **同 session 后续消息排队**：turn 进行中再发消息进 steering 队列，
   等 turn 结束才消费（`drainQueuedSteeringContinuations`）；这是
   语义正确的设计（避免上下文互踩），不是 bug

**现状事实（已核实代码）**：
- 默认值链：`defaults.go` 不设置 → `agent_init.go:62-64` `<=0 → 1`
  （串行）；上游同样不设置
- 并发机制健全（`docs/design/steering-spec.md`）：同 session 用
  `LoadOrStore` 原子保留 key 严格串行；跨 session 走 worker pool
  并行；steering 队列注入既有 turn——并发放开**不会**打乱会话内顺序
- 本机已手动配 `max_parallel_turns: 4`（fork 实践值）
- 语义：worker 池大小 = 最大并发 turn 数，非「每会话并发」

**内容**（两层）：
1. fork 默认值改 `max_parallel_turns: 3`——跨 session 并行开箱即用
2. 同 session 第二条消息的体验兜底：turn 结束后 steering 队列自动
   续跑（已有），WebUI 侧配 steering 徽章（已上线 `60e7a307`），
   无需额外改动

**可行性：高（一行默认值 + 文档 + 测试）**
- 改 `pkg/config/defaults.go` 一行；`max(1, …)` 兜底逻辑现成
- 现有 `-race` 测试已覆盖并发路径（此前三处竞争修复过）
- 用户想回串行：config 写 1 即可（语义文档已有）

**工作量**：0.5 小时
**风险**：并发 turn 各自起 LLM 调用，低配机器/低速率限制的 provider
可能触发 429——profile 机制（P0-3）落地后可考虑把该值纳入档位
（open=3 / balanced=2 / strict=1），二期再定。

### P1-1 报错可点击修复

**内容**：聊天页模型报错（unconfigured/unreachable）时，错误条内嵌
「去配置」按钮直达 Models 页对应 provider 编辑态。

**可行性：高（前端小改）** — 错误消息结构已含 requestId/kind，路由跳转
现成。工作量 0.5 天。

### P1-2 默认 fallback 链预置

**内容**：onboard/向导完成时，若用户配了 ≥2 个模型，自动生成
「主模型 → 备用」fallback 链配置。

**可行性：中** — upstream 能力已合入，但默认链的配置 schema 与生成策略
需按 `bbf6893c` 的实现细读后定（估计 0.5 天细读 + 0.5 天实现）。
与 P0-1 联动：向导最后一步「加个备用模型？」。

### P1-3 launcher 设置页「检查更新」

**内容**：设置页 About 区加「检查更新」按钮，调 updater 逻辑展示
版本对比 + 一键升级。

**可行性：中** — updater 是 CLI 命令，Web 侧需要薄封装 API（后端小改
~50 行 + 前端按钮）。工作量 1 天。

### P2（锦上添花，暂不展开）

- Docker 镜像预烘焙默认链 + healthcheck
- README 中文 Quick Start（国内 provider 优先）
- onboard CLI 交互化（终端内选 provider 填 key，替代手改 JSON）

## 4. 修正记录（对早前口头清单的勘误）

- ❌ 早前提的「国内 provider 预设包」：**上游已实现**（defaults.go 32 预设
  含智谱/月暗/DeepSeek），无需再做，向导直接复用即可。
- ✅ 早前提的「首启向导」「doctor」经代码核实成立，且后端依赖零改动/
  底料齐全，可行性比预估更高。

## 5. 建议实施顺序

1. **P0-1 首启向导 + P0-3 安全等级选择器**（同批落地：向导里含安全等级
   步骤，一次 UI 编排覆盖两个痛点）
2. **P0-4 并发默认值放开**（0.5 小时速赢，随首批一起出）
3. **P0-2 doctor**（半天级、自助排障刚需）
4. P1-1 报错可点击 → P1-2 fallback 预置 → P1-3 检查更新
5. P2 视精力

## 6. 开放问题（待决策）

1. 向导是否默认跳过已配置用户之外的「主动重配」入口（如 设置→重新运行
   设置向导）？——建议要，成本低。
2. doctor 是否同时提供 Web 版（设置页「运行体检」按钮）？——建议二期，
   先 CLI。
3. P1-2 fallback 链默认策略：同 provider 降级还是跨 provider？——待细读
   upstream 实现后给方案。
4. 是否将 P0-1/P0-2/P0-3 提 PR 回上游？——向导/doctor 无个人定制色彩；
   profile 机制对上游是纯增量，均符合回馈标准。
5. 安全档位是否记入诊断信息（doctor 输出当前档位）？——建议要，排障
   第一问就是「你什么档位」。
6. 新装默认档位用 `open`（fork 现状延续）还是 `balanced`（更保守）？
   ——待拍板。
