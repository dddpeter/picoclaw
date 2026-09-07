import { launcherFetch } from "@/api/http"

export type ChannelConfig = Record<string, unknown>
export type AppConfig = Record<string, unknown>

/**
 * Unknown-field warnings attached by the backend when it loaded the config
 * leniently (GET /api/config may respond with the config plus a
 * `config_warnings` array). Extract them and return a clean config object.
 */
export function extractConfigWarnings(
  config: AppConfig,
): { clean: AppConfig; warnings: string[] } {
  if (!config || typeof config !== "object") {
    return { clean: config, warnings: [] }
  }
  const warnings = Array.isArray(config.config_warnings)
    ? (config.config_warnings as unknown[])
        .filter((w): w is string => typeof w === "string")
        .filter((w) => w.length > 0)
    : []
  if (warnings.length === 0) {
    return { clean: config, warnings: [] }
  }
  const clean: AppConfig = { ...config }
  delete clean.config_warnings
  return { clean, warnings }
}

export interface SupportedChannel {
  name: string
  display_name?: string
  config_key: string
  variant?: string
}

export interface ChannelConfigResponse {
  config: ChannelConfig
  configured_secrets: string[]
  config_key: string
  variant?: string
}

interface ChannelsCatalogResponse {
  channels: SupportedChannel[]
}

interface ConfigActionResponse {
  status: string
  errors?: string[]
}

const BASE_URL = ""

async function request<T>(path: string, options?: RequestInit): Promise<T> {
  const res = await launcherFetch(`${BASE_URL}${path}`, options)
  if (!res.ok) {
    let message = `API error: ${res.status} ${res.statusText}`
    try {
      const body = (await res.json()) as {
        error?: string
        errors?: string[]
        status?: string
      }
      if (Array.isArray(body.errors) && body.errors.length > 0) {
        message = body.errors.join("; ")
      } else if (typeof body.error === "string" && body.error.trim() !== "") {
        message = body.error
      }
    } catch {
      // Keep default fallback message if response body is not JSON.
    }
    throw new Error(message)
  }
  return res.json() as Promise<T>
}

export async function getChannelsCatalog(): Promise<ChannelsCatalogResponse> {
  return request<ChannelsCatalogResponse>("/api/channels/catalog")
}

export async function getAppConfig(): Promise<AppConfig> {
  return request<AppConfig>("/api/config")
}

export async function getChannelConfig(
  channelName: string,
): Promise<ChannelConfigResponse> {
  return request<ChannelConfigResponse>(
    `/api/channels/${encodeURIComponent(channelName)}/config`,
  )
}

export async function patchAppConfig(
  patch: Record<string, unknown>,
): Promise<ConfigActionResponse> {
  return request<ConfigActionResponse>("/api/config", {
    method: "PATCH",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(patch),
  })
}

export async function resetAppConfig(): Promise<ConfigActionResponse> {
  return request<ConfigActionResponse>("/api/config/reset", {
    method: "POST",
  })
}

// WeChat QR login flow API

export interface WeixinFlowResponse {
  flow_id: string
  status: "wait" | "scaned" | "confirmed" | "expired" | "error"
  qr_data_uri?: string
  account_id?: string
  error?: string
}

export interface WecomFlowResponse {
  flow_id: string
  status: "wait" | "scaned" | "confirmed" | "expired" | "error"
  qr_data_uri?: string
  bot_id?: string
  error?: string
}

export async function startWeixinFlow(): Promise<WeixinFlowResponse> {
  return request<WeixinFlowResponse>("/api/weixin/flows", { method: "POST" })
}

export async function pollWeixinFlow(
  flowID: string,
): Promise<WeixinFlowResponse> {
  return request<WeixinFlowResponse>(
    `/api/weixin/flows/${encodeURIComponent(flowID)}`,
  )
}

export async function startWecomFlow(): Promise<WecomFlowResponse> {
  return request<WecomFlowResponse>("/api/wecom/flows", { method: "POST" })
}

export async function pollWecomFlow(
  flowID: string,
): Promise<WecomFlowResponse> {
  return request<WecomFlowResponse>(
    `/api/wecom/flows/${encodeURIComponent(flowID)}`,
  )
}

export type { ChannelsCatalogResponse, ConfigActionResponse }
