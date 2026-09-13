import { IconCheck, IconPalette } from "@tabler/icons-react"
import { useTranslation } from "react-i18next"

import { Button } from "@/components/ui/button.tsx"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu.tsx"
import { THEMES, useTheme } from "@/hooks/use-theme.ts"

interface ThemeSwitcherProps {
  variant?: "ghost" | "outline"
  className?: string
}

export function ThemeSwitcher({
  variant = "ghost",
  className = "size-8",
}: ThemeSwitcherProps) {
  const { t } = useTranslation()
  const { theme, setTheme } = useTheme()

  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button
          variant={variant}
          size="icon"
          className={className}
          aria-label={t("theme.label")}
        >
          <IconPalette className="size-4.5" />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="min-w-44">
        {THEMES.map((def) => (
          <DropdownMenuItem key={def.id} onClick={() => setTheme(def.id)}>
            <span className="flex items-center">
              {def.swatch.map((color, i) => (
                <span
                  key={`${def.id}-${i}`}
                  className="-mr-1 size-3 rounded-full border border-border/60 last:mr-0"
                  style={{ backgroundColor: color }}
                />
              ))}
            </span>
            <span className="flex-1">{t(def.labelKey)}</span>
            {theme === def.id && <IconCheck className="size-4" />}
          </DropdownMenuItem>
        ))}
      </DropdownMenuContent>
    </DropdownMenu>
  )
}
