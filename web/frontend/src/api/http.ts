import { isLauncherAuthPathname } from "@/lib/launcher-login-path"

function isLauncherAuthPath(): boolean {
  if (typeof globalThis.location === "undefined") {
    return false
  }
  if (isLauncherAuthPathname(globalThis.location.pathname || "/")) {
    return true
  }
  try {
    return isLauncherAuthPathname(
      new URL(globalThis.location.href).pathname || "/",
    )
  } catch {
    return false
  }
}

/**
 * Same-origin fetch that sends cookies; redirects to launcher login on 401 JSON responses.
 * Skips redirect while already on an auth page (login or setup) to avoid reload loops.
 */
export async function launcherFetch(
  input: RequestInfo | URL,
  init?: RequestInit,
): Promise<Response> {
  const res = await fetch(input, {
    credentials: "same-origin",
    ...init,
  })
  if (res.status === 401) {
    const ct = res.headers.get("content-type") || ""
    if (
      ct.includes("application/json") &&
      typeof globalThis.location !== "undefined" &&
      !isLauncherAuthPath()
    ) {
      globalThis.location.assign("/launcher-login")
    }
  }
  return res
}

/**
 * Extract an error message from a non-ok launcher API response.
 * Falls back to the provided message when the body is not JSON or carries no
 * `error` field (e.g. plain-text legacy errors).
 */
export async function parseResponseError(
  res: Response,
  fallback: string,
): Promise<string> {
  const body = (await res.json().catch(() => null)) as { error?: string } | null
  return body?.error ?? fallback
}
