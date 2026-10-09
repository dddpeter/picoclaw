> 返回 [README](../../project/README.zh.md)

# 飞书

飞书（国际版名称：Lark）是字节跳动旗下的企业协作平台。它通过事件驱动的 Webhook 同时支持中国和全球市场。

## 配置

```json
{
  "channel_list": {
    "feishu": {
      "enabled": true,
      "type": "feishu",
      "app_id": "cli_xxx",
      "app_secret": "xxx",
      "encrypt_key": "",
      "verification_token": "",
      "allow_from": [],
      "is_lark": false
    }
  }
}
```

| 字段                  | 类型   | 必填 | 描述                                                                                             |
| --------------------- | ------ | ---- | ------------------------------------------------------------------------------------------------ |
| enabled               | bool   | 是   | 是否启用飞书频道                                                                                 |
| app_id                | string | 是   | 飞书应用的 App ID(以cli\_开头)                                                                   |
| app_secret            | string | 是   | 飞书应用的 App Secret                                                                            |
| encrypt_key           | string | 否   | 事件回调加密密钥                                                                                 |
| verification_token    | string | 否   | 用于Webhook事件验证的Token                                                                       |
| allow_from            | array  | 否   | 用户ID白名单，空表示所有用户                                                                     |
| random_reaction_emoji | array  | 否   | 随机添加的表情列表，空则使用默认 "Pin"                                                           |
| is_lark               | bool   | 否   | 是否使用 Lark 国际版域名（`open.larksuite.com`），默认为 `false`（使用飞书域名 `open.feishu.cn`） |

## 设置流程

1. 前往 [飞书开放平台](https://open.feishu.cn/)（国际版用户请前往 [Lark 开放平台](https://open.larksuite.com/)）创建应用程序
2. 获取 App ID 和 App Secret
3. 在「开发配置 → 事件与回调 → 事件配置」中，订阅方式选择**使用长连接接收事件**（picoclaw 走 WebSocket 长连接，无需公网 Webhook URL），并添加事件「接收消息 im.message.receive_v1」
4. 在「事件与回调 → 回调配置（消息卡片回调）」中，同样选择长连接订阅方式，并添加回调「**卡片回传交互 card.action.trigger**」——流式卡片停止按钮、工具审批卡的按钮点击都依赖此回调；**漏配后点击按钮会弹出「该应用尚未配置卡片回调」**（参考[处理卡片回调](https://open.feishu.cn/document/uAjLw4CM/ukzMukzMukzM/feishu-cards/handle-card-callbacks)）
5. 按需申请权限（`im:message` 收发消息、`im:resource` 下载媒体等），然后**创建版本并发布应用**——订阅配置发布后才生效
6. 将 App ID、App Secret 填入配置文件（长连接模式下 Encrypt Key / Verification Token 不参与校验，可留空）
7. 自定义你希望 PicoClaw react 你消息时的表情（可选, Reference URL: [Feishu Emoji List](https://open.larkoffice.com/document/server-docs/im-v1/message-reaction/emojis-introduce))

## 平台限制

> ⚠️ **飞书通道不支持 32 位设备。** 飞书官方 SDK 仅提供 64 位构建，armv6 / armv7 / mipsle 等 32 位架构无法使用飞书通道。如需在 32 位设备上接入即时通讯，请改用 Telegram、Discord 或 OneBot 等通道。
