// 移植自 pi-web-ui（MIT License）web/src/components/copy-button.tsx。
// SPDX-License-Identifier: MIT
// 适配点：i18n 由 pi-web-ui 的 useT() 换成 react-i18next 的 useTranslation()。
import { IconCheck, IconCopy } from "@tabler/icons-react"
import { memo } from "react"
import { useTranslation } from "react-i18next"
import { toast } from "sonner"

import { useCopyToClipboard } from "@/hooks/use-copy-to-clipboard"

export const CopyButton = memo(function CopyButton({ text }: { text: string }) {
  const { t } = useTranslation()
  const { copy, isCopied: copied } = useCopyToClipboard()
  if (!text) return null
  return (
    <button
      type="button"
      className="copy-btn"
      title={t("piChat.copy")}
      onClick={() => {
        void copy(text).then((ok) => {
          if (!ok) toast.error(t("piChat.copyFailed"))
        })
      }}
    >
      {copied ? <IconCheck /> : <IconCopy />}
    </button>
  )
})
