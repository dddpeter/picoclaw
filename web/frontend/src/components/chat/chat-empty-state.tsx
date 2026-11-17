import {
  IconPlugConnectedX,
  IconRobot,
  IconRobotOff,
  IconStar,
} from "@tabler/icons-react"
import { Link } from "@tanstack/react-router"
import type { ReactNode } from "react"
import { useTranslation } from "react-i18next"

import { EmptyTemplateCards } from "@/components/chat/prompt-templates"
import { Button } from "@/components/ui/button"

interface ChatEmptyStateProps {
  hasAvailableModels: boolean
  defaultModelName: string
  isConnected: boolean
}

function EmptyStateLayout({
  icon,
  title,
  description,
  tone = "warning",
  hero = false,
  children,
}: {
  icon: ReactNode
  title: string
  description: string
  tone?: "warning" | "info" | "danger"
  hero?: boolean
  children?: ReactNode
}) {
  const toneClass = {
    warning: "text-[var(--lobe-warning)]",
    info: "text-[var(--lobe-info)]",
    danger: "text-[var(--lobe-danger)]",
  }[tone]

  return (
    <div className="flex min-h-[420px] flex-col items-center justify-center px-4 py-16 text-center">
      <div
        className={`mb-5 flex size-14 items-center justify-center rounded-2xl bg-[var(--lobe-fill-2)] ${toneClass}`}
      >
        {icon}
      </div>
      <h3
        className={`font-semibold tracking-[-0.01em] text-[var(--lobe-text)] ${
          hero ? "text-[28px] leading-[1.2]" : "text-[22px] leading-tight"
        }`}
      >
        {title}
      </h3>
      <p className="mt-2 max-w-md text-sm leading-6 text-[var(--lobe-text-desc)]">
        {description}
      </p>
      {children}
    </div>
  )
}

export function ChatEmptyState({
  hasAvailableModels,
  defaultModelName,
  isConnected,
}: ChatEmptyStateProps) {
  const { t } = useTranslation()

  if (!hasAvailableModels) {
    return (
      <EmptyStateLayout
        icon={<IconRobotOff className="size-6" aria-hidden />}
        title={t("chat.empty.noConfiguredModel")}
        description={t("chat.empty.noConfiguredModelDescription")}
      >
        <Button asChild variant="outline" size="sm" className="mt-6 px-4">
          <Link to="/models">{t("chat.empty.goToModels")}</Link>
        </Button>
      </EmptyStateLayout>
    )
  }

  if (!defaultModelName) {
    return (
      <EmptyStateLayout
        icon={<IconStar className="size-6" aria-hidden />}
        title={t("chat.empty.noSelectedModel")}
        description={t("chat.empty.noSelectedModelDescription")}
      >
        <Button asChild variant="outline" size="sm" className="mt-6 px-4">
          <Link to="/models">{t("chat.empty.goToModels")}</Link>
        </Button>
      </EmptyStateLayout>
    )
  }

  if (!isConnected) {
    return (
      <EmptyStateLayout
        icon={<IconPlugConnectedX className="size-6" aria-hidden />}
        title={t("chat.empty.notRunning")}
        description={t("chat.empty.notRunningDescription")}
        tone="danger"
      />
    )
  }

  return (
    <EmptyStateLayout
      icon={<IconRobot className="size-6" aria-hidden />}
      title={t("chat.welcome")}
      description={t("chat.welcomeDesc")}
      tone="info"
      hero
    >
      <EmptyTemplateCards />
    </EmptyStateLayout>
  )
}
