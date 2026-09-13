import type { MCPConfigPayload, MCPServerPayload, MCPServerType } from "@/api/mcp"

export type DeferredMode = "inherit" | "deferred" | "eager"

export interface MCPServerDraft {
  id: string
  name: string
  type: MCPServerType
  enabled: boolean
  deferred: DeferredMode
  url: string
  headersText: string
  command: string
  argsText: string
  envText: string
  envFile: string
}

export interface MCPConfigForm {
  enabled: boolean
  maxInlineTextChars: string
  discoveryEnabled: boolean
  discoveryTTL: string
  discoveryMaxResults: string
  discoveryUseBM25: boolean
  discoveryUseRegex: boolean
  servers: MCPServerDraft[]
}

export function makeServerDraftID(name: string): string {
  const encoded = encodeURIComponent(name.trim())
  if (encoded) return `mcp-${encoded}`
  return `mcp-${Math.random().toString(36).slice(2, 10)}`
}

export function serverPayloadToDraft(dto: MCPServerPayload): MCPServerDraft {
  return {
    id: makeServerDraftID(dto.name),
    name: dto.name,
    type: dto.type,
    enabled: dto.enabled,
    deferred:
      dto.deferred === null ? "inherit" : dto.deferred ? "deferred" : "eager",
    url: dto.url,
    headersText: JSON.stringify(dto.headers ?? {}, null, 2),
    command: dto.command,
    argsText: (dto.args ?? []).join("\n"),
    envText: Object.entries(dto.env ?? {})
      .map(([k, v]) => `${k}=${v}`)
      .join("\n"),
    envFile: dto.envFile,
  }
}

export function parseJSONMapText(
  text: string,
  label: string,
): Record<string, string> {
  const trimmed = text.trim()
  if (trimmed === "") return {}
  let parsed: unknown
  try {
    parsed = JSON.parse(trimmed)
  } catch {
    throw new Error(`INVALID_JSON:${label}`)
  }
  if (typeof parsed !== "object" || parsed === null || Array.isArray(parsed)) {
    throw new Error(`INVALID_JSON:${label}`)
  }
  const out: Record<string, string> = {}
  for (const [k, v] of Object.entries(parsed as Record<string, unknown>)) {
    out[k] = String(v)
  }
  return out
}

export function parseLinesToMap(text: string): Record<string, string> {
  const out: Record<string, string> = {}
  for (const line of text.split("\n")) {
    const trimmed = line.trim()
    if (trimmed === "") continue
    const eq = trimmed.indexOf("=")
    if (eq <= 0) continue
    out[trimmed.slice(0, eq).trim()] = trimmed.slice(eq + 1)
  }
  return out
}

export function parseLinesToList(text: string): string[] {
  return text
    .split("\n")
    .map((l) => l.trim())
    .filter((l) => l !== "")
}

export function draftToServerPayload(
  d: MCPServerDraft,
  invalidJSONLabel: string,
): MCPServerPayload {
  return {
    name: d.name.trim(),
    type: d.type,
    enabled: d.enabled,
    deferred: d.deferred === "inherit" ? null : d.deferred === "deferred",
    url: d.url.trim(),
    headers: parseJSONMapText(d.headersText, invalidJSONLabel),
    command: d.command.trim(),
    args: parseLinesToList(d.argsText),
    env: parseLinesToMap(d.envText),
    envFile: d.envFile.trim(),
  }
}

function parseIntStrict(value: string): number {
  const n = Number.parseInt(value, 10)
  return Number.isFinite(n) && n > 0 ? n : 0
}

export function formToPayload(
  form: MCPConfigForm,
  invalidJSONLabel: string,
): MCPConfigPayload {
  return {
    enabled: form.enabled,
    maxInlineTextChars: parseIntStrict(form.maxInlineTextChars),
    discovery: {
      enabled: form.discoveryEnabled,
      ttlSeconds: parseIntStrict(form.discoveryTTL),
      maxSearchResults: parseIntStrict(form.discoveryMaxResults),
      useBM25: form.discoveryUseBM25,
      useRegex: form.discoveryUseRegex,
    },
    servers: form.servers.map((s) => draftToServerPayload(s, invalidJSONLabel)),
  }
}

export function emptyServerDraft(): MCPServerDraft {
  return {
    id: makeServerDraftID(""),
    name: "",
    type: "stdio",
    enabled: true,
    deferred: "inherit",
    url: "",
    headersText: "",
    command: "",
    argsText: "",
    envText: "",
    envFile: "",
  }
}
