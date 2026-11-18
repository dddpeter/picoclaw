// Web 文件端点的纯工具（从组件文件抽出：组件文件保持 components-only，
// react-refresh/HMR 才能保留组件状态）。URL 形状是服务端契约：
// GET /pico/file?path=<workspace-relative>&download=1。
/** Preview/download URL for a workspace-relative path. */
export function filePreviewUrl(path: string, download = false): string {
  const params = new URLSearchParams({ path })
  if (download) params.set("download", "1")
  return `/pico/file?${params.toString()}`
}

/** Rich-format support probe (design §5.6 P2): pdf / docx / xlsx. */
export function supportsRichPreview(
  filename: string,
): "pdf" | "docx" | "xlsx" | null {
  const lower = filename.toLowerCase()
  if (lower.endsWith(".pdf")) return "pdf"
  if (lower.endsWith(".docx")) return "docx"
  if (lower.endsWith(".xlsx") || lower.endsWith(".xls")) return "xlsx"
  return null
}
