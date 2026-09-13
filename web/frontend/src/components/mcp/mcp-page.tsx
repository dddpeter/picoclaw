import { useTranslation } from "react-i18next"

import { PageHeader } from "@/components/page-header"

export function MCPPage() {
  const { t } = useTranslation()

  return (
    <div className="bg-background flex h-full flex-col">
      <PageHeader title={t("navigation.mcp", "MCP")} />
      <div className="flex-1 overflow-y-auto px-6 pb-8">
        <p className="text-muted-foreground text-sm">
          {t("pages.mcp.placeholder", "MCP settings page")}
        </p>
      </div>
    </div>
  )
}
