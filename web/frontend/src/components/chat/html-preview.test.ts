import { describe, expect, it } from "vitest"

import { HTML_PREVIEW_SANDBOX, isolateHtmlPreview } from "./html-preview"

describe("isolateHtmlPreview", () => {
  it("injects the guard into an existing head", () => {
    const html = "<html><head><title>x</title></head><body>hi</body></html>"
    const out = isolateHtmlPreview(html)
    expect(out).toContain("data-picoclaw-preview-guard")
    expect(out).toContain('<base href="about:srcdoc">')
    // The original markup is preserved.
    expect(out).toContain("<title>x</title>")
    expect(out).toContain("hi")
  })

  it("creates a head when the document has none", () => {
    const out = isolateHtmlPreview("<html><body>hi</body></html>")
    expect(out).toContain("data-picoclaw-preview-guard")
    expect(out).toContain("hi")
  })

  it("wraps a fragment", () => {
    const out = isolateHtmlPreview("<p>fragment</p>")
    expect(out).toContain("<!DOCTYPE html>")
    expect(out).toContain("<p>fragment</p>")
  })

  it("is idempotent", () => {
    const once = isolateHtmlPreview("<html><head></head><body>x</body></html>")
    const twice = isolateHtmlPreview(once)
    expect(twice).toBe(once)
    // Only one guard marker.
    expect(twice.split("data-picoclaw-preview-guard").length - 1).toBe(1)
  })

  it("neutralizes external relative URLs via the base tag", () => {
    const out = isolateHtmlPreview(
      '<html><head></head><body><img src="secret.png"></body></html>',
    )
    expect(out).toContain('href="about:srcdoc"')
  })
})

describe("HTML_PREVIEW_SANDBOX", () => {
  it("omits allow-same-origin so the frame gets an opaque origin", () => {
    expect(HTML_PREVIEW_SANDBOX).not.toContain("allow-same-origin")
  })

  it("omits escape hatches (top navigation, downloads)", () => {
    expect(HTML_PREVIEW_SANDBOX).not.toContain("allow-top-navigation")
    expect(HTML_PREVIEW_SANDBOX).not.toContain("allow-downloads")
  })
})
