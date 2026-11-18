import { describe, expect, it, vi } from "vitest"

import { buildTurns } from "@/chat-pi/turns"
import { loadSessionMessages } from "@/features/chat/history"
import type { AssistantDetailVisibility } from "@/store/chat"

// Reproduce the reported bug (2026-11-18 12:33 screenshot): the persisted
// user message carries attachments with a workspace path (verified in the
// session JSONL), yet the rendered bubble shows only the raw [file:...] tag
// text and no chip. Feed the exact session-API shape through the history
// loader and turn builder to find where the path is lost.

vi.mock("@/api/sessions", () => ({
  getSessionHistory: vi.fn().mockResolvedValue({
    id: "s1",
    channel: "pico",
    messages: [
      {
        role: "user",
        content: "[file:inbound/20261118/e524ef6e_resume.pdf]",
        created_at: "2026-11-18T12:19:00+08:00",
        attachments: [
          {
            type: "file",
            url: "inbound/20261118/e524ef6e_resume.pdf",
            filename: "resume.pdf",
            content_type: "application/pdf",
            path: "inbound/20261118/e524ef6e_resume.pdf",
          },
        ],
      },
      {
        role: "assistant",
        content: "简历读完了。",
        created_at: "2026-11-18T12:20:00+08:00",
      },
    ],
    summary: "",
    created: "2026-11-18T12:19:00+08:00",
    updated: "2026-11-18T12:20:00+08:00",
  }),
}))

describe("uploaded-file attachment replay (bug repro)", () => {
  it("keeps the workspace path through loadSessionMessages", async () => {
    const { messages } = await loadSessionMessages("s1", "pico")
    const user = messages.find((m) => m.role === "user")
    expect(user?.attachments).toBeDefined()
    expect(user?.attachments?.[0]?.path).toBe(
      "inbound/20261118/e524ef6e_resume.pdf",
    )
  })

  it("keeps the path on the built turn's user part", async () => {
    const { messages } = await loadSessionMessages("s1", "pico")
    const detail: AssistantDetailVisibility = "all"
    const turns = buildTurns(messages, { isTyping: false, detail })
    expect(turns).toHaveLength(1)
    expect(turns[0].user?.attachments?.[0]?.path).toBe(
      "inbound/20261118/e524ef6e_resume.pdf",
    )
    expect(turns[0].user?.attachments?.[0]?.type).toBe("file")
  })
})
