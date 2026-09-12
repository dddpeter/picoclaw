import { launcherFetch } from "@/api/http"

export interface SessionSummary {
  id: string
  title: string
  preview: string
  message_count: number
  /** Originating chat channel; "pico" for web chat, omitted/unknown for legacy sessions. */
  channel?: string
  created: string
  updated: string
}

export interface SessionDetail {
  id: string
  channel?: string
  messages: {
    role: "user" | "assistant"
    content: string
    created_at?: string
    kind?: "normal" | "thought" | "tool_calls"
    model_name?: string
    media?: string[]
    attachments?: {
      type?: "image" | "audio" | "video" | "file"
      url: string
      filename?: string
      content_type?: string
    }[]
    tool_calls?: {
      id?: string
      type?: string
      function?: {
        name?: string
        arguments?: string
      }
      extra_content?: {
        tool_feedback_explanation?: string
      }
    }[]
  }[]
  summary: string
  created: string
  updated: string
}

export async function getSessions(
  offset: number = 0,
  limit: number = 20,
): Promise<SessionSummary[]> {
  const params = new URLSearchParams({
    offset: offset.toString(),
    limit: limit.toString(),
  })

  const res = await launcherFetch(`/api/sessions?${params.toString()}`)
  if (!res.ok) {
    throw new Error(`Failed to fetch sessions: ${res.status}`)
  }
  return res.json()
}

export async function getSessionHistory(
  id: string,
  channel?: string,
): Promise<SessionDetail> {
  const params = new URLSearchParams()
  if (channel) {
    params.set("channel", channel)
  }
  const query = params.toString()
  const res = await launcherFetch(
    `/api/sessions/${encodeURIComponent(id)}${query ? `?${query}` : ""}`,
  )
  if (!res.ok) {
    throw new Error(`Failed to fetch session ${id}: ${res.status}`)
  }
  return res.json()
}

export async function deleteSession(
  id: string,
  channel?: string,
): Promise<void> {
  const params = new URLSearchParams()
  if (channel) {
    params.set("channel", channel)
  }
  const query = params.toString()
  const res = await launcherFetch(
    `/api/sessions/${encodeURIComponent(id)}${query ? `?${query}` : ""}`,
    { method: "DELETE" },
  )
  if (!res.ok) {
    throw new Error(`Failed to delete session ${id}: ${res.status}`)
  }
}
