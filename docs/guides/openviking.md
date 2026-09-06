# OpenViking 共享记忆接入

[OpenViking](https://github.com/volcengine/OpenViking) 是火山引擎开源的 agent 上下文数据库：把记忆、资源、技能组织成 `viking://` 文件系统，支持语义检索，会话提交后异步萃取长期记忆。多个 picoclaw 实例连同一个 OpenViking 服务即共享记忆。

## 阶段 0：MCP 接入（零代码）

### 1. 部署 OpenViking

```bash
pip install openviking --upgrade
openviking-server init     # 交互式配置（LLM provider 等）
openviking-server doctor   # 自检
openviking-server          # 启动，默认提供 /mcp 端点
```

也可用 Docker / Helm 自部署，或使用火山云托管版。

### 2. 配置 picoclaw

OpenViking 的 MCP 端点是标准 streamable HTTP，在配置中加入：

```json
{
  "tools": {
    "mcp": {
      "servers": {
        "openviking": {
          "enabled": true,
          "type": "http",
          "url": "http://127.0.0.1:8000/mcp",
          "headers": {
            "Authorization": "Bearer <your-token>"
          }
        }
      }
    }
  }
}
```

重启后模型即获得 `mcp_openviking_*` 工具（search / read / ls / store 等），可以直接读写共享记忆。这些调用会以 🔵 蓝色 MCP 标识显示在飞书过程面板里。

> 注意：OpenViking 为 AGPLv3 许可。通过网络（MCP/HTTP）与之交互不影响 picoclaw 的 MIT 许可，但不要把 OpenViking 的代码复制进本仓库。

## 阶段 1：自动召回注入

阶段 0 的局限是召回靠模型自觉调工具。开启自动召回后，每轮开始时 picoclaw 会用用户消息作为查询调用 OpenViking 的 `search` 工具，把命中的共享记忆注入系统提示词的 memory 槽位：

```json
{
  "memory": {
    "recall": {
      "enabled": true,
      "server": "openviking",
      "tool": "search",
      "max_chars": 2400,
      "timeout_ms": 3000
    }
  }
}
```

| 字段 | 默认 | 说明 |
|---|---|---|
| `server` | 必填 | `tools.mcp.servers` 里的 MCP 服务名 |
| `tool` | `search` | 该服务上的召回工具名 |
| `max_chars` | `2400` | 注入内容的字符上限 |
| `timeout_ms` | `3000` | 单次召回超时，超时/失败静默跳过 |

行为要点：

- **尽力而为**：服务未启动、超时、空结果都会静默跳过，绝不阻塞或中断回合；
- **有界**：注入内容截断到 `max_chars`，召回调用有独立超时；
- **提示词定位**：注入内容带"approximate references only"声明，与本地 MEMORY.md 共存于 memory 槽位。

## 多实例共享

两台机器上的 picoclaw 连同一个 OpenViking 服务、使用同一用户命名空间即可共享记忆。会话级写入目前由模型通过 `store` 工具完成；自动会话提交（turn 结束后推送摘要）规划中。

## 参考

- OpenViking 文档：<https://docs.openviking.ai>
- MCP 客户端接入：<https://docs.openviking.ai/en/agent-integrations/06-mcp-clients>
