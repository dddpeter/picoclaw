import { launcherFetch, parseResponseError } from "@/api/http"

export type MCPServerType = "stdio" | "sse" | "http"

export interface MCPDiscoveryPayload {
  enabled: boolean
  ttlSeconds: number
  maxSearchResults: number
  useBM25: boolean
  useRegex: boolean
}

export interface MCPServerPayload {
  name: string
  type: MCPServerType
  enabled: boolean
  deferred: boolean | null
  url: string
  headers: Record<string, string>
  command: string
  args: string[]
  env: Record<string, string>
  envFile: string
}

export interface MCPConfigPayload {
  enabled: boolean
  maxInlineTextChars: number
  discovery: MCPDiscoveryPayload
  servers: MCPServerPayload[]
}

export type MCPConfigResponse = MCPConfigPayload

export interface MCPServerTestResponse {
  ok: boolean
  latencyMs: number
  toolCount: number
  tools: Array<{ name: string; description: string }>
  error?: string
}

export interface MCPServerStatusEntry {
  connected: boolean
  toolCount: number
  tools: Array<{ name: string; description: string }>
  error?: string
}

export interface MCPStatusResponse {
  gateway?: "offline"
  initialized?: boolean
  enabled?: boolean
  servers?: Record<string, MCPServerStatusEntry>
}

async function request<T>(path: string, options?: RequestInit): Promise<T> {
  const res = await launcherFetch(path, options)
  if (!res.ok) {
    throw new Error(await parseResponseError(res, `API error: ${res.status} ${res.statusText}`))
  }
  return res.json() as Promise<T>
}

export async function getMCPConfig(): Promise<MCPConfigResponse> {
  return request<MCPConfigResponse>("/api/mcp/config")
}

export async function putMCPConfig(
  body: MCPConfigPayload,
): Promise<{ status: string }> {
  return request<{ status: string }>("/api/mcp/config", {
    method: "PUT",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  })
}

export async function testMCPServer(
  server: MCPServerPayload,
): Promise<MCPServerTestResponse> {
  return request<MCPServerTestResponse>("/api/mcp/servers/test", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(server),
  })
}

export async function getMCPStatus(): Promise<MCPStatusResponse> {
  return request<MCPStatusResponse>("/api/mcp/status")
}
