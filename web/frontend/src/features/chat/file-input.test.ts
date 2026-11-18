import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import type { ChatAttachment } from "@/store/chat"

import { buildChatFileAttachments } from "./file-input"

const toastError = vi.fn()
const inlineBuilder = vi.fn()

vi.mock("sonner", () => ({
  toast: { error: (...args: unknown[]) => toastError(...args) },
}))

// The inline image path (FileReader → data URL) is environment-sensitive in
// jsdom; this suite tests the split logic, so stub the inline builder and
// assert on what was routed to it.
vi.mock("./image-input", async (importOriginal) => {
  const actual = await importOriginal<typeof import("./image-input")>()
  return {
    ...actual,
    buildChatImageAttachments: (...args: unknown[]) =>
      inlineBuilder(
        ...(args as Parameters<typeof actual.buildChatImageAttachments>),
      ),
  }
})

// Minimal TFunction stand-in: echoes the key so assertions can check which
// toast fired without pulling i18next test configuration in.
const t = ((key: string, opts?: Record<string, unknown>) =>
  `${key}${opts ? `:${JSON.stringify(opts)}` : ""}`) as never

const inlineSentinel: ChatAttachment = {
  type: "image",
  url: "data:image/png;base64,SENTINEL",
  filename: "shot.png",
  contentType: "image/png",
}

function mockFetch(impl: () => Promise<unknown>) {
  global.fetch = vi.fn().mockImplementation(impl) as unknown as typeof fetch
}

beforeEach(() => {
  inlineBuilder.mockReset().mockResolvedValue([inlineSentinel])
})

afterEach(() => {
  vi.restoreAllMocks()
  toastError.mockClear()
  // @ts-expect-error drop the stub between tests
  delete global.fetch
})

describe("buildChatFileAttachments", () => {
  it("routes small images to the inline data-URL path without uploading", async () => {
    const fetchSpy = vi.fn()
    global.fetch = fetchSpy as unknown as typeof fetch

    const file = new File([new Uint8Array(4)], "shot.png", {
      type: "image/png",
    })
    const result = await buildChatFileAttachments([file], t)

    expect(inlineBuilder).toHaveBeenCalledTimes(1)
    expect(inlineBuilder.mock.calls[0][0]).toEqual([file])
    expect(result).toEqual([inlineSentinel])
    expect(fetchSpy).not.toHaveBeenCalled()
  })

  it("uploads non-image files and carries the workspace path", async () => {
    mockFetch(() =>
      Promise.resolve({
        ok: true,
        json: () =>
          Promise.resolve({
            path: "inbound/20260118/abc_report.pdf",
            filename: "report.pdf",
            content_type: "application/pdf",
            size: 8,
          }),
      }),
    )

    const file = new File(["%PDF-1.4"], "report.pdf", {
      type: "application/pdf",
    })
    const result = await buildChatFileAttachments([file], t)

    expect(inlineBuilder).not.toHaveBeenCalled()
    expect(result).toHaveLength(1)
    expect(result[0]).toMatchObject({
      type: "file",
      url: "",
      path: "inbound/20260118/abc_report.pdf",
      filename: "report.pdf",
      contentType: "application/pdf",
    })
    expect(toastError).not.toHaveBeenCalled()
  })

  it("maps uploaded image content types to the image attachment kind", async () => {
    mockFetch(() =>
      Promise.resolve({
        ok: true,
        json: () =>
          Promise.resolve({
            path: "inbound/20260118/abc_huge.png",
            filename: "huge.png",
            content_type: "image/png",
            size: 99,
          }),
      }),
    )

    // Oversized image: exceeds the 7MB inline cap, so it goes through upload.
    const big = new Uint8Array(7 * 1024 * 1024 + 1)
    const file = new File([big], "huge.png", { type: "image/png" })
    const result = await buildChatFileAttachments([file], t)

    expect(inlineBuilder).not.toHaveBeenCalled()
    expect(result).toHaveLength(1)
    expect(result[0].type).toBe("image")
    expect(result[0].url).toBe("")
    expect(result[0].path).toBe("inbound/20260118/abc_huge.png")
  })

  it("reports 413 as fileTooLarge and drops the attachment", async () => {
    mockFetch(() => Promise.resolve({ ok: false, status: 413 }))

    const file = new File(["x"], "big.bin", {
      type: "application/octet-stream",
    })
    const result = await buildChatFileAttachments([file], t)

    expect(result).toHaveLength(0)
    expect(toastError).toHaveBeenCalledTimes(1)
    expect(toastError.mock.calls[0][0]).toContain("chat.fileTooLarge")
  })

  it("reports network failure as uploadFailed and keeps inline results", async () => {
    mockFetch(() => Promise.reject(new Error("boom")))

    const failing = new File(["x"], "bad.bin", {
      type: "application/octet-stream",
    })
    const inline = new File([new Uint8Array(4)], "shot.png", {
      type: "image/png",
    })
    const result = await buildChatFileAttachments([failing, inline], t)

    expect(result).toEqual([inlineSentinel])
    expect(toastError).toHaveBeenCalledTimes(1)
    expect(toastError.mock.calls[0][0]).toContain("chat.uploadFailed")
  })
})
