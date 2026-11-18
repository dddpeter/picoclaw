import { launcherFetch } from "@/api/http"

// API client for the session-scoped model (fork: session-scoped model
// selection). The model is bound to ONE conversation — config.json keeps
// its own default model and is never touched.

export interface SessionModelResponse {
  /** The session the model belongs to. */
  session: string
  /** The model bound to the session ("" = the session follows the config default). */
  model: string
  /** The model's effective context window (0 = unknown / config default). */
  context_window?: number
  /** The threshold at which the session starts compressing. */
  compress_at?: number
  /** Present on failures. */
  error?: string
}

/**
 * Read the model bound to a session.
 *
 * Returns the model name ("" when the session follows the config default)
 * together with the model's context window.
 */
export async function getSessionModel(
  sessionId: string,
): Promise<SessionModelResponse> {
  const res = await launcherFetch(
    `/pico/session-model?session=${encodeURIComponent(sessionId)}`,
  )
  const data = (await res
    .json()
    .catch(() => null)) as SessionModelResponse | null
  if (!res.ok) {
    throw new Error(data?.error || `API error: ${res.status} ${res.statusText}`)
  }
  if (data?.error) {
    throw new Error(data.error)
  }
  return data ?? { session: sessionId, model: "" }
}

/**
 * Bind a model to a session. Pass an empty model name to clear the
 * binding (the session returns to the configured default model).
 */
export async function setSessionModel(
  sessionId: string,
  model: string,
): Promise<SessionModelResponse> {
  const res = await launcherFetch(
    `/pico/session-model?session=${encodeURIComponent(sessionId)}`,
    {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ model }),
    },
  )
  const data = (await res
    .json()
    .catch(() => null)) as SessionModelResponse | null
  if (!res.ok) {
    throw new Error(data?.error || `API error: ${res.status} ${res.statusText}`)
  }
  if (data?.error) {
    throw new Error(data.error)
  }
  return data ?? { session: sessionId, model: "" }
}
