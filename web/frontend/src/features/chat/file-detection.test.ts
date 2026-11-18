import { describe, expect, it } from "vitest"

import type { ChatMessage } from "@/store/chat"

import {
  detectFiles,
  detectUploadedFiles,
  toolCallPath,
} from "./file-detection"

function message(partial: Partial<ChatMessage>): ChatMessage {
  return {
    id: "m1",
    role: "user",
    content: "",
    ...partial,
  } as ChatMessage
}

describe("toolCallPath", () => {
  it("extracts path from complete JSON arguments", () => {
    expect(toolCallPath('{"path": "notes/report.md", "content": "# x"}')).toBe(
      "notes/report.md",
    )
  })

  it("extracts path from streaming half-JSON via the key regex", () => {
    // The path value landed but the rest of the arguments JSON is still
    // streaming in — JSON.parse fails, the key regex still resolves.
    expect(toolCallPath('{"path": "notes/report.md", "con')).toBe(
      "notes/report.md",
    )
  })

  it("returns null for empty or non-path arguments", () => {
    expect(toolCallPath(undefined)).toBeNull()
    expect(toolCallPath("")).toBeNull()
    expect(toolCallPath('{"command": "ls"}')).toBeNull()
  })
})

describe("detectFiles", () => {
  it("collects attachment paths (source a)", () => {
    const files = detectFiles([
      message({
        attachments: [
          { type: "file", url: "", path: "inbound/20260118/a.csv" },
        ],
      }),
    ])
    expect(files).toEqual([
      {
        path: "inbound/20260118/a.csv",
        filename: "a.csv",
        source: "attachment",
      },
    ])
  })

  it("collects write-tool paths (source b)", () => {
    const files = detectFiles([
      message({
        role: "assistant",
        toolCalls: [
          {
            type: "function",
            function: {
              name: "write_file",
              arguments: '{"path": "out/report.md", "content": "# long body"}',
            },
          },
        ],
      }),
    ])
    expect(files.map((file) => file.source)).toEqual(["toolCall"])
    expect(files[0].path).toBe("out/report.md")
  })

  it("collects content tags and full-output pointers (source c)", () => {
    const files = detectFiles([
      message({
        role: "assistant",
        content:
          "已保存 [file:out/notes.md] 和截图 [image:out/shot.png]；详细见 Full output: /tmp/ws/shell-output-1.log 末尾",
      }),
    ])
    const paths = files.map((file) => file.path)
    expect(paths).toContain("out/notes.md")
    expect(paths).toContain("out/shot.png")
    expect(paths).toContain("/tmp/ws/shell-output-1.log")
  })

  it("dedupes across sources and rejects extension-less paths", () => {
    const files = detectFiles([
      message({
        content: "[file:out/report.md] [image:out/report.md] [file:v1.0.2]",
        attachments: [{ type: "file", url: "", path: "out/report.md" }],
      }),
    ])
    expect(files).toHaveLength(1)
  })

  it("does not match write-tool content bodies via the tag regex", () => {
    // The args contain a JSON-encoded tag-looking string in the content body;
    // source b only picks the path field, and source c scans message content
    // (not tool arguments), so no false positive appears.
    const files = detectFiles([
      message({
        role: "assistant",
        toolCalls: [
          {
            type: "function",
            function: {
              name: "write_file",
              arguments:
                '{"path": "out/real.md", "content": "see [file:fake.txt] inside"}',
            },
          },
        ],
      }),
    ])
    expect(files.map((file) => file.path)).toEqual(["out/real.md"])
  })
})

describe("detectUploadedFiles", () => {
  it("keeps only non-image attachments with paths", () => {
    const files = detectUploadedFiles([
      message({
        attachments: [
          { type: "file", url: "", path: "inbound/x.csv", filename: "x.csv" },
          { type: "image", url: "data:image/png;base64,AAA" },
          { type: "file", url: "https://example.com/y.pdf" },
        ],
      }),
    ])
    expect(files).toHaveLength(1)
    expect(files[0]).toMatchObject({
      path: "inbound/x.csv",
      source: "attachment",
    })
  })
})
