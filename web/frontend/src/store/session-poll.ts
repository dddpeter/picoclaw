// 会话列表轮询的单一 owner（F7）：sidebar 曾在桌面内联 + 移动抽屉两个
// 实例里各跑一套 interval + mount 全量拉取（抽屉打开期间请求翻倍、
// 分页进度被第二次首载折叠）。本模块引用计数订阅、单 timer，节拍随
// busy 加速（5s/30s），后台标签页暂停 + 回到可见/窗口聚焦立即刷新——
// 结构复用 store/gateway.ts 的成熟范式（注释同源）。

const SESSION_POLL_INTERVAL_MS = 30_000
const SESSION_POLL_BUSY_INTERVAL_MS = 5_000

const sessionPollSubscribers = new Set<() => void>()
let sessionPollingTimer: ReturnType<typeof setTimeout> | null = null
let sessionPollBusy = false
let sessionVisibilityHooked = false

function nextSessionPollInterval(): number {
  return sessionPollBusy
    ? SESSION_POLL_BUSY_INTERVAL_MS
    : SESSION_POLL_INTERVAL_MS
}

function scheduleSessionPoll(delay = nextSessionPollInterval()) {
  if (sessionPollSubscribers.size === 0) {
    return
  }
  // Pause in background tabs (the gateway poller precedent); the
  // visibilitychange hook resumes with an immediate refresh.
  if (typeof document !== "undefined" && document.hidden) {
    return
  }
  if (sessionPollingTimer !== null) {
    clearTimeout(sessionPollingTimer)
  }
  sessionPollingTimer = setTimeout(() => {
    sessionPollingTimer = null
    if (typeof document !== "undefined" && document.hidden) {
      return
    }
    tickSessionPollSubscribers()
    scheduleSessionPoll()
  }, delay)
}

function tickSessionPollSubscribers(): void {
  for (const onTick of sessionPollSubscribers) {
    onTick()
  }
}

function ensureSessionVisibilityHook(): void {
  if (sessionVisibilityHooked || typeof document === "undefined") {
    return
  }
  sessionVisibilityHooked = true
  document.addEventListener("visibilitychange", () => {
    if (!document.hidden && sessionPollSubscribers.size > 0) {
      tickSessionPollSubscribers()
      scheduleSessionPoll()
    }
  })
  // The sidebar refreshed on window focus; keep that affordance.
  window.addEventListener("focus", () => {
    if (sessionPollSubscribers.size > 0) {
      tickSessionPollSubscribers()
      scheduleSessionPoll()
    }
  })
}

/**
 * Register one poll tick handler (typically ChatPage's refreshSessions).
 * The first subscriber fires an immediate tick (mount freshness) and the
 * cadence starts; further subscribers (the drawer's second sidebar
 * instance) add nothing — the list state is shared upstream anyway.
 * Returns the unsubscribe function.
 */
export function subscribeSessionPolling(onTick: () => void): () => void {
  sessionPollSubscribers.add(onTick)
  if (sessionPollSubscribers.size === 1) {
    ensureSessionVisibilityHook()
    onTick()
    scheduleSessionPoll()
  }
  return () => {
    sessionPollSubscribers.delete(onTick)
    if (sessionPollSubscribers.size === 0 && sessionPollingTimer !== null) {
      clearTimeout(sessionPollingTimer)
      sessionPollingTimer = null
    }
  }
}

/** busy 节拍开关：任一已列会话 mid-turn 时加速到 5s。 */
export function setSessionPollBusy(busy: boolean): void {
  if (sessionPollBusy === busy) {
    return
  }
  sessionPollBusy = busy
  if (sessionPollSubscribers.size > 0) {
    scheduleSessionPoll()
  }
}

/** 测试辅助：清空全部轮询状态（订阅/timer/busy）。 */
export function resetSessionPollForTest(): void {
  sessionPollSubscribers.clear()
  if (sessionPollingTimer !== null) {
    clearTimeout(sessionPollingTimer)
    sessionPollingTimer = null
  }
  sessionPollBusy = false
}
