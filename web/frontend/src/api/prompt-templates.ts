import { launcherFetch } from "@/api/http"

/** One persisted entry on the gateway: a full template (custom, or an
 * override of a built-in matched by id) or a hidden marker {id, hidden:true}
 * removing a built-in from the library. Mirrors the backend
 * storedPromptTemplate shape. */
export interface StoredPromptTemplate {
  id: string
  icon?: string
  title?: string
  desc?: string
  prompt?: string
  hidden?: boolean
}

interface PromptTemplatesResponse {
  templates: StoredPromptTemplate[]
}

async function request<T>(path: string, options?: RequestInit): Promise<T> {
  const res = await launcherFetch(path, options)
  if (!res.ok) {
    throw new Error(await extractErrorMessage(res))
  }
  return res.json() as Promise<T>
}

export async function getPromptTemplates(): Promise<StoredPromptTemplate[]> {
  const resp = await request<PromptTemplatesResponse>("/api/prompt-templates")
  return Array.isArray(resp.templates) ? resp.templates : []
}

export async function savePromptTemplates(
  templates: StoredPromptTemplate[],
): Promise<StoredPromptTemplate[]> {
  const resp = await request<PromptTemplatesResponse>("/api/prompt-templates", {
    method: "PUT",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ templates }),
  })
  return Array.isArray(resp.templates) ? resp.templates : []
}

async function extractErrorMessage(res: Response): Promise<string> {
  try {
    const body = (await res.json().catch(() => null)) as { error?: string } | null
    if (body?.error) {
      return body.error
    }
  } catch {
    // ignore invalid body
  }
  return `API error: ${res.status} ${res.statusText}`
}
