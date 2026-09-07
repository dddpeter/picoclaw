import {
  IconAlertCircle,
  IconDeviceFloppy,
  IconRefresh,
} from "@tabler/icons-react"

import { cn } from "@/lib/utils"

interface ConfigChangeNoticeProps {
  kind?: "save" | "restart" | "warning"
  title: string
  description?: string
  className?: string
}

// Style map per notice kind; keyed lookup keeps this flat as kinds grow.
const KIND_STYLES: Record<NonNullable<ConfigChangeNoticeProps["kind"]>, string> = {
  save: "border-yellow-200 bg-yellow-50 text-yellow-900",
  restart: "border-amber-200 bg-amber-50 text-amber-900",
  warning: "border-orange-200 bg-orange-50 text-orange-900",
}

const KIND_ICONS: Record<NonNullable<ConfigChangeNoticeProps["kind"]>, typeof IconRefresh> = {
  save: IconDeviceFloppy,
  restart: IconRefresh,
  warning: IconAlertCircle,
}

export function ConfigChangeNotice({
  kind = "save",
  title,
  description,
  className,
}: ConfigChangeNoticeProps) {
  const Icon = KIND_ICONS[kind]

  return (
    <div
      className={cn(
        "flex items-start gap-3 rounded-lg border px-3 py-2 text-sm",
        KIND_STYLES[kind],
        className,
      )}
    >
      <Icon className="mt-0.5 size-4 shrink-0" />
      <div className="min-w-0">
        <p className="font-medium">{title}</p>
        {description && (
          <p className="mt-0.5 text-xs/5 opacity-85">{description}</p>
        )}
      </div>
    </div>
  )
}
