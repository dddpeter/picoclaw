import { launcherFetch, parseResponseError } from "@/api/http"

export interface PluginItem {
  name: string
  version?: string
  source?: string
  ref?: string
  installedAt?: string
  enabled: boolean
  registered: boolean
  loadError?: string
  skills: number
  mcpServers: number
  warnings?: string[]
}

export interface PluginsResponse {
  installRoot: string
  registryError?: string
  plugins: PluginItem[]
}

export interface PluginReportResponse {
  ok: boolean
  name?: string
  version?: string
  target?: string
  skills: number
  mcpServers: number
  warnings?: string[]
  error?: string
}

async function request<T>(path: string, options?: RequestInit): Promise<T> {
  const res = await launcherFetch(path, options)
  if (!res.ok) {
    throw new Error(
      await parseResponseError(
        res,
        `API error: ${res.status} ${res.statusText}`,
      ),
    )
  }
  return res.json() as Promise<T>
}

export async function getPlugins(): Promise<PluginsResponse> {
  return request<PluginsResponse>("/api/plugins")
}

export async function setPluginEnabled(
  name: string,
  enabled: boolean,
): Promise<{ ok: boolean }> {
  return request<{ ok: boolean }>(
    `/api/plugins/${encodeURIComponent(name)}/enabled`,
    {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ enabled }),
    },
  )
}

export async function removePlugin(
  name: string,
  purgeData: boolean,
): Promise<{ ok: boolean }> {
  return request<{ ok: boolean }>(
    `/api/plugins/${encodeURIComponent(name)}?purgeData=${purgeData}`,
    {
      method: "DELETE",
    },
  )
}

export async function validatePlugin(
  path: string,
): Promise<PluginReportResponse> {
  return request<PluginReportResponse>("/api/plugins/validate", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ path }),
  })
}

export async function installPlugin(
  source: string,
  ref: string,
): Promise<PluginReportResponse> {
  return request<PluginReportResponse>("/api/plugins/install", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ source, ref }),
  })
}
