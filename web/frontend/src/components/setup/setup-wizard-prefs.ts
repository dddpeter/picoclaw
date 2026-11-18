// Setup wizard 的 localStorage 偏好与自动弹出判定（从 setup-wizard.tsx 抽出：
// 组件文件保持 components-only，react-refresh/HMR 才能保留组件状态）。
import { type ModelInfo } from "@/api/models"

const WIZARD_STORAGE_KEY = "picoclaw-setup-wizard-dismissed"

/** True when the wizard should auto-open: never dismissed AND no usable model. */
export function shouldAutoOpenWizard(
  models: ModelInfo[] | undefined,
  dismissed: boolean,
): boolean {
  if (dismissed) return false
  if (!models) return false // still loading — don't flash the wizard
  return models.every((m) => m.status !== "available")
}

export function getWizardDismissed(): boolean {
  return localStorage.getItem(WIZARD_STORAGE_KEY) === "1"
}

export function setWizardDismissed(v: boolean) {
  localStorage.setItem(WIZARD_STORAGE_KEY, v ? "1" : "0")
}
