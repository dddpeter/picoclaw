// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

const getGatewayStatus = vi.fn()

vi.mock("@/api/gateway", () => ({
  getGatewayStatus: (...args: unknown[]) => getGatewayStatus(...args),
}))

import { subscribeGatewayPolling } from "./gateway"

// jsdom defines hidden as a getter on Document.prototype; override per test.
function setHidden(hidden: boolean): void {
  Object.defineProperty(document, "hidden", {
    configurable: true,
    get: () => hidden,
  })
}

describe("gateway polling visibility pause", () => {
  let unsub: (() => void) | null = null

  beforeEach(() => {
    vi.useFakeTimers()
    getGatewayStatus.mockReset()
    setHidden(false)
    getGatewayStatus.mockResolvedValue({
      gateway_status: "running",
      gateway_start_allowed: true,
      gateway_restart_required: false,
    })
  })

  afterEach(() => {
    // Always unsubscribe: a failed assertion must not leak the subscriber
    // into the next test (subscribers gate the whole polling loop).
    unsub?.()
    unsub = null
    setHidden(false)
    vi.useRealTimers()
  })

  it("pauses scheduled polls while the tab is hidden", async () => {
    unsub = subscribeGatewayPolling()
    // The subscriber's immediate first refresh runs through.
    await vi.advanceTimersByTimeAsync(0)
    expect(getGatewayStatus).toHaveBeenCalledTimes(1)

    setHidden(true)
    await vi.advanceTimersByTimeAsync(10_000)
    expect(getGatewayStatus).toHaveBeenCalledTimes(1)
  })

  it("resumes with an immediate refresh on visibilitychange", async () => {
    unsub = subscribeGatewayPolling()
    await vi.advanceTimersByTimeAsync(0)
    expect(getGatewayStatus).toHaveBeenCalledTimes(1)

    setHidden(true)
    await vi.advanceTimersByTimeAsync(10_000)
    expect(getGatewayStatus).toHaveBeenCalledTimes(1)

    setHidden(false)
    document.dispatchEvent(new Event("visibilitychange"))
    await vi.advanceTimersByTimeAsync(0)
    expect(getGatewayStatus).toHaveBeenCalledTimes(2)

    // And the regular cadence continues while visible.
    await vi.advanceTimersByTimeAsync(2000)
    expect(getGatewayStatus).toHaveBeenCalledTimes(3)
  })
})
