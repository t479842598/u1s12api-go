import { useEffect, useMemo, useState, type ReactNode } from "react"
import {
  DEFAULT_MODE,
  DEFAULT_THEME_ID,
  LEGACY_THEME_STORAGE_KEY,
  MODE_STORAGE_KEY,
  THEME_STORAGE_KEY,
  ThemeContext,
  modeOptions,
  themeOptions,
  type ColorMode,
  type ThemeContextValue,
  type ThemeId,
} from "@/components/theme/theme-context"

const SYSTEM_DARK_QUERY = "(prefers-color-scheme: dark)"

function getSystemPrefersDark() {
  return (
    typeof window !== "undefined" &&
    window.matchMedia(SYSTEM_DARK_QUERY).matches
  )
}

function getStoredThemeId(): ThemeId {
  if (typeof window === "undefined") return DEFAULT_THEME_ID
  try {
    const stored = window.localStorage.getItem(THEME_STORAGE_KEY) as ThemeId | null
    if (stored && themeOptions.some((o) => o.id === stored)) return stored
    // v1 单维 key 迁移：旧主题 id 沿用为风格，"system" 保持跟随系统
    const legacy = window.localStorage.getItem(LEGACY_THEME_STORAGE_KEY)
    if (legacy && themeOptions.some((o) => o.id === legacy)) return legacy as ThemeId
    return DEFAULT_THEME_ID
  } catch {
    return DEFAULT_THEME_ID
  }
}

function getStoredMode(): ColorMode {
  if (typeof window === "undefined") return DEFAULT_MODE
  try {
    const stored = window.localStorage.getItem(MODE_STORAGE_KEY) as ColorMode | null
    return stored === "light" || stored === "dark" || stored === "system" ? stored : DEFAULT_MODE
  } catch {
    return DEFAULT_MODE
  }
}

function persist(key: string, value: string) {
  try {
    window.localStorage.setItem(key, value)
  } catch {
    // 隐私模式等存储失败时保持内存态
  }
}

export function ThemeProvider({ children }: { children: ReactNode }) {
  const [themeId, setThemeId] = useState<ThemeId>(getStoredThemeId)
  const [mode, setMode] = useState<ColorMode>(getStoredMode)
  const [systemPrefersDark, setSystemPrefersDark] = useState(getSystemPrefersDark)

  const dark = mode === "system" ? systemPrefersDark : mode === "dark"

  useEffect(() => {
    const mediaQuery = window.matchMedia(SYSTEM_DARK_QUERY)
    const handleChange = () => setSystemPrefersDark(mediaQuery.matches)
    handleChange()
    mediaQuery.addEventListener("change", handleChange)
    return () => mediaQuery.removeEventListener("change", handleChange)
  }, [])

  useEffect(() => {
    const root = document.documentElement
    root.dataset.theme = themeId
    root.dataset.mode = dark ? "dark" : "light"
    // .dark 供 tailwind dark: 变体消费（custom-variant 定义在 index.css）
    root.classList.toggle("dark", dark)
    root.style.colorScheme = dark ? "dark" : "light"
    persist(THEME_STORAGE_KEY, themeId)
  }, [themeId, dark])

  useEffect(() => {
    persist(MODE_STORAGE_KEY, mode)
  }, [mode])

  const value = useMemo<ThemeContextValue>(
    () => ({
      themeId,
      setThemeId,
      mode,
      setMode,
      dark,
      themeOptions,
      modeOptions,
    }),
    [themeId, mode, dark],
  )

  return <ThemeContext.Provider value={value}>{children}</ThemeContext.Provider>
}
