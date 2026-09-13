import { useCallback, useEffect, useState } from "react"

export type ThemeMode = "light" | "dark"

export interface ThemeDefinition {
  id: string
  mode: ThemeMode
  labelKey: string
  /** [background, primary, accent] preview dots shown in the switcher. */
  swatch: [string, string, string]
}

// ids "light"/"dark" are the legacy values stored by the old two-way toggle;
// keeping them valid means existing users keep their palette after upgrade.
export const THEMES: ThemeDefinition[] = [
  {
    id: "light",
    mode: "light",
    labelKey: "theme.light",
    swatch: ["#f5f1e8", "#d0653a", "#ecd9b8"],
  },
  {
    id: "dark",
    mode: "dark",
    labelKey: "theme.dark",
    swatch: ["#262019", "#e08a3c", "#3a3025"],
  },
  {
    id: "ocean",
    mode: "dark",
    labelKey: "theme.ocean",
    swatch: ["#12222e", "#54b6dc", "#254760"],
  },
  {
    id: "forest",
    mode: "dark",
    labelKey: "theme.forest",
    swatch: ["#101f16", "#4fbb7e", "#1f402b"],
  },
  {
    id: "sakura",
    mode: "light",
    labelKey: "theme.sakura",
    swatch: ["#fbf0f3", "#d9567a", "#f3d5dd"],
  },
]

const DEFAULT_THEME = "dark"
const THEME_IDS = new Set(THEMES.map((t) => t.id))

export function isKnownTheme(id: string | null): id is string {
  return id !== null && THEME_IDS.has(id)
}

export function isDarkTheme(id: string): boolean {
  return THEMES.find((t) => t.id === id)?.mode === "dark"
}

function getStoredTheme(): string {
  if (typeof window === "undefined") return DEFAULT_THEME
  const stored = localStorage.getItem("theme")
  return isKnownTheme(stored) ? stored : DEFAULT_THEME
}

function applyTheme(id: string) {
  const root = document.documentElement
  root.setAttribute("data-theme", id)
  root.classList.toggle("dark", isDarkTheme(id))
}

export function useTheme() {
  const [theme, setThemeState] = useState<string>(getStoredTheme)

  useEffect(() => {
    applyTheme(theme)
    localStorage.setItem("theme", theme)
  }, [theme])

  const setTheme = useCallback((id: string) => {
    if (isKnownTheme(id)) setThemeState(id)
  }, [])

  return { theme, setTheme }
}
