import { useTranslation } from "react-i18next"

import { ConfigChangeNotice } from "@/components/config-change-notice"

/**
 * Banner listing config fields the backend tolerated but did not recognize.
 * Shared by the visual config page and the raw JSON editor.
 */
export function UnknownFieldsNotice({ warnings }: { warnings: string[] }) {
  const { t } = useTranslation()
  if (warnings.length === 0) {
    return null
  }
  return (
    <ConfigChangeNotice
      kind="warning"
      title={t("pages.config.unknown_fields_title")}
      description={t("pages.config.unknown_fields_desc", {
        fields: warnings.join(", "),
      })}
      className="shrink-0"
    />
  )
}
