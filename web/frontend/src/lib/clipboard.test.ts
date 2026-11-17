// [评审 P1-1] clipboard.ts 契约单测：copyText **永不 reject**，成败只以 boolean 表达。
//
// 为什么必须锁这条契约：消费侧统一写作 `void copy(text).then((ok) => …)`，
// 没有 `.catch`。降级路径的 createElement / appendChild / select / removeChild
// 在受限环境（非 HTTPS、受限 WebView、DOM 被策略限制）同样可能抛错；一旦外泄为
// rejection，用户既拿不到失败提示（失败应走 resolve(false) → toast），控制台还会
// 出现未处理的 Promise rejection。此前的 hook 单测 mock 掉了 copyText，
// 恰好绕过了这一分支，故在此直接以假环境锁定库层契约。
import { describe, expect, it, vi } from "vitest"

import { type ClipboardEnvironment, copyText } from "./clipboard"

type Doc = NonNullable<ClipboardEnvironment["document"]>

function fakeDoc(overrides: Partial<Doc> = {}): Doc {
  const node = {
    value: "",
    style: { position: "", left: "" },
    setAttribute: vi.fn(),
    select: vi.fn(),
  }
  return {
    body: { appendChild: vi.fn(), removeChild: vi.fn() },
    createElement: vi.fn(() => node),
    execCommand: vi.fn(() => true),
    ...overrides,
  } as unknown as Doc
}

describe("copyText 契约 [评审 P1-1]", () => {
  it("CT-01 Clipboard API 可用 → resolve true，且不触碰降级路径", async () => {
    const writeText = vi.fn(async () => {})
    const doc = fakeDoc()

    await expect(
      copyText("hi", {
        navigator: { clipboard: { writeText } },
        document: doc,
      }),
    ).resolves.toBe(true)

    expect(writeText).toHaveBeenCalledWith("hi")
    expect(doc.execCommand).not.toHaveBeenCalled()
  })

  it("CT-02 Clipboard API 被拒 → 回落 execCommand 并返回其结果", async () => {
    const doc = fakeDoc({ execCommand: vi.fn(() => false) })

    await expect(
      copyText("hi", {
        navigator: {
          clipboard: {
            writeText: vi.fn(async () => {
              throw new Error("denied")
            }),
          },
        },
        document: doc,
      }),
    ).resolves.toBe(false)

    expect(doc.execCommand).toHaveBeenCalledWith("copy")
  })

  it("CT-03 无 document（非浏览器环境）→ resolve false", async () => {
    await expect(copyText("hi", {})).resolves.toBe(false)
  })

  it("CT-04 execCommand 返回 false → resolve false", async () => {
    await expect(
      copyText("hi", {
        document: fakeDoc({ execCommand: vi.fn(() => false) }),
      }),
    ).resolves.toBe(false)
  })

  it("CT-05 appendChild 抛错 → resolve false（**不 reject**）", async () => {
    const doc = fakeDoc({
      body: {
        appendChild: vi.fn(() => {
          throw new Error("blocked")
        }),
        removeChild: vi.fn(),
      },
    })

    await expect(copyText("hi", { document: doc })).resolves.toBe(false)
  })

  it("CT-06 createElement 抛错 → resolve false（**不 reject**）", async () => {
    const doc = fakeDoc({
      createElement: vi.fn(() => {
        throw new Error("no dom")
      }),
    })

    await expect(copyText("hi", { document: doc })).resolves.toBe(false)
  })

  it("CT-07 select() 抛错 → resolve false（**不 reject**）", async () => {
    const doc = fakeDoc({
      createElement: vi.fn(() => ({
        value: "",
        style: { position: "", left: "" },
        setAttribute: vi.fn(),
        select: vi.fn(() => {
          throw new Error("not selectable")
        }),
      })),
    })

    await expect(copyText("hi", { document: doc })).resolves.toBe(false)
  })

  it("CT-08 removeChild 抛错 → 仍返回 execCommand 结果（清理失败不改成败语义）", async () => {
    const doc = fakeDoc({
      body: {
        appendChild: vi.fn(),
        removeChild: vi.fn(() => {
          throw new Error("already gone")
        }),
      },
    })

    await expect(copyText("hi", { document: doc })).resolves.toBe(true)
  })
})
