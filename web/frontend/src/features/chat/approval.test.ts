import { describe, expect, it } from "vitest"

import {
  matchApprovalReceipt,
  pairApprovalHistory,
  parseApprovalFromPayload,
  parseApprovalFromText,
  parseGoDuration,
  sealLastApproval,
} from "@/features/chat/approval"
import type { ChatMessage } from "@/store/chat"

// publishAsk 的真实兜底文本（pkg/agent/approval_hook.go publishAsk）。
const REAL_ASK = `⚠️ 需要批准：即将执行工具 exec
git push origin main --force

回复 /approve 允许、/approve always 本会话内不再询问、/deny 拒绝（超过 5m0s 未回复将按拒绝处理）`

describe("parseApprovalFromPayload", () => {
  it("parses v1 approval payload", () => {
    expect(
      parseApprovalFromPayload({
        approval: { tool: "exec", preview: "rm -rf /tmp/x", timeout_ms: 300000 },
      }),
    ).toEqual({ tool: "exec", preview: "rm -rf /tmp/x", timeoutMs: 300000 })
  })

  it("rejects missing tool and non-object payloads", () => {
    expect(parseApprovalFromPayload({})).toBeUndefined()
    expect(parseApprovalFromPayload({ approval: { tool: "  " } })).toBeUndefined()
    expect(parseApprovalFromPayload({ approval: "exec" })).toBeUndefined()
  })
})

describe("parseApprovalFromText", () => {
  it("parses tool, preview and timeout from the real fallback text", () => {
    expect(parseApprovalFromText(REAL_ASK)).toEqual({
      tool: "exec",
      preview: "git push origin main --force",
      timeoutMs: 300_000,
    })
  })

  it("ignores unrelated text", () => {
    expect(parseApprovalFromText("普通回复")).toBeUndefined()
    expect(parseApprovalFromText("⚠️ 需要批准：")).toBeUndefined()
  })
})

describe("parseGoDuration", () => {
  it("parses go durations", () => {
    expect(parseGoDuration("5m0s")).toBe(300_000)
    expect(parseGoDuration("30s")).toBe(30_000)
    expect(parseGoDuration("1h0m0s")).toBe(3_600_000)
    expect(parseGoDuration("1m30s")).toBe(90_000)
    expect(parseGoDuration("x")).toBeUndefined()
  })
})

describe("matchApprovalReceipt", () => {
  it("matches all receipt forms", () => {
    expect(matchApprovalReceipt("✅ 已批准，继续执行。")).toBe("approved")
    expect(matchApprovalReceipt("✅ 已批准；本会话内命中规则 \"x\" 的调用将自动放行（约 6 小时后或重启失效）。")).toBe("always")
    expect(matchApprovalReceipt("⛔ 已拒绝本次工具执行。")).toBe("denied")
    expect(matchApprovalReceipt("⛔ 审批超时（5m0s），已按拒绝处理（fail-closed）。")).toBe("timeout")
  })

  it("does not match other messages", () => {
    expect(matchApprovalReceipt("✅ 部署完成")).toBeUndefined()
    expect(matchApprovalReceipt("当前没有待批准的操作，本次回复已忽略。")).toBeUndefined()
  })
})

function assistantMessage(partial: Partial<ChatMessage>): ChatMessage {
  return {
    id: Math.random().toString(36).slice(2),
    role: "assistant",
    content: "",
    timestamp: 1_700_000_000_000,
    ...partial,
  }
}

describe("sealLastApproval", () => {
  it("seals the latest unsealed approval and keeps the rest untouched", () => {
    const messages = [
      assistantMessage({ id: "a", approval: { tool: "exec" }, approvalSeal: { verdict: "denied", at: 1 } }),
      assistantMessage({ id: "b", approval: { tool: "exec" } }),
    ]
    const sealed = sealLastApproval(messages, "approved", 42)
    expect(sealed).toBeDefined()
    expect(sealed![1].approvalSeal).toEqual({ verdict: "approved", at: 42 })
    expect(sealed![0]).toBe(messages[0])
    expect(messages[1].approvalSeal).toBeUndefined() // 不可变：原数组不动
  })

  it("returns undefined when nothing is sealable", () => {
    expect(sealLastApproval([assistantMessage({ content: "hi" })], "approved", 1)).toBeUndefined()
  })
})

describe("pairApprovalHistory", () => {
  it("pairs ask with receipt, suppresses the receipt text", () => {
    const messages = [
      assistantMessage({ id: "ask", content: REAL_ASK }),
      assistantMessage({ id: "receipt", content: "✅ 已批准，继续执行。", timestamp: 1_700_000_060_000 }),
    ]
    const paired = pairApprovalHistory(messages)
    expect(paired).toHaveLength(1)
    expect(paired[0].id).toBe("ask")
    expect(paired[0].approval?.tool).toBe("exec")
    expect(paired[0].approvalSeal).toEqual({ verdict: "approved", at: 1_700_000_060_000 })
  })

  it("marks unpaired asks as done and keeps orphan receipts as text", () => {
    const paired = pairApprovalHistory([
      assistantMessage({ id: "ask", content: REAL_ASK }),
      assistantMessage({ id: "orphan", content: "当前没有待批准的操作，本次回复已忽略。" }),
    ])
    expect(paired).toHaveLength(2)
    expect(paired[0].approvalSeal?.verdict).toBe("done")
    expect(paired[1].id).toBe("orphan")
  })
})
