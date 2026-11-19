// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import {
  resetSessionPollForTest,
  setSessionPollBusy,
  subscribeSessionPolling,
} from "./session-poll"

function setHidden(hidden: boolean): void {
  Object.defineProperty(document, "hidden", {
    configurable: true,
    get: () => hidden,
  })
}

describe("session polling single owner", () => {
  beforeEach(() => {
    vi.useFakeTimers()
    resetSessionPollForTest()
    setHidden(false)
  })

  afterEach(() => {
    resetSessionPollForTest()
    setHidden(false)
    vi.useRealTimers()
  })

  it("first subscriber ticks immediately; second subscriber adds no tick", async () => {
    const tickA = vi.fn()
    const tickB = vi.fn()
    const unsubA = subscribeSessionPolling(tickA)
    expect(tickA).toHaveBeenCalledTimes(1)

    const unsubB = subscribeSessionPolling(tickB)
    expect(tickB).toHaveBeenCalledTimes(0) // no 0→1 transition: no immediate tick

    // Both subscribers receive the next cadence tick.
    await vi.advanceTimersByTimeAsync(30_000)
    expect(tickA).toHaveBeenCalledTimes(2)
    expect(tickB).toHaveBeenCalledTimes(1)

    unsubA()
    unsubB()
  })

  it("cadence switches between idle and busy intervals", async () => {
    const tick = vi.fn()
    const unsub = subscribeSessionPolling(tick)
    await vi.advanceTimersByTimeAsync(0)

    // idle: 30s cadence — 29s passes with no extra tick.
    await vi.advanceTimersByTimeAsync(29_000)
    expect(tick).toHaveBeenCalledTimes(1)
    await vi.advanceTimersByTimeAsync(1_000)
    expect(tick).toHaveBeenCalledTimes(2)

    // busy: 5s cadence.
    setSessionPollBusy(true)
    await vi.advanceTimersByTimeAsync(5_000)
    expect(tick).toHaveBeenCalledTimes(3)

    unsub()
  })

  it("pauses in hidden tabs and resumes on visibilitychange", async () => {
    const tick = vi.fn()
    const unsub = subscribeSessionPolling(tick)
    await vi.advanceTimersByTimeAsync(0)
    expect(tick).toHaveBeenCalledTimes(1)

    setHidden(true)
    await vi.advanceTimersByTimeAsync(60_000)
    expect(tick).toHaveBeenCalledTimes(1)

    setHidden(false)
    document.dispatchEvent(new Event("visibilitychange"))
    await vi.advanceTimersByTimeAsync(0)
    expect(tick).toHaveBeenCalledTimes(2)

    unsub()
  })

  it("unsubscribing the last subscriber stops the loop", async () => {
    const tick = vi.fn()
    const unsub = subscribeSessionPolling(tick)
    unsub()
    await vi.advanceTimersByTimeAsync(60_000)
    expect(tick).toHaveBeenCalledTimes(1) // only the immediate first tick
  })
})
