import { createContext, useContext } from "react"

// 二维主题模型（devhub/freebuff 同构）：
//   themeId = 风格（8 种，各自定义明/暗两套变量）
//   mode    = 明暗（system/light/dark，独立开关，默认跟随系统）
export type ThemeId =
  | "u1s1-official"
  | "porcelain-moss"
  | "tungsten-dark"
  | "neo-brutalism"
  | "apple-glass"
  | "clear-glass"
  | "paper-ink"
  | "terminal"

export type ColorMode = "system" | "light" | "dark"

export interface ThemeOption {
  id: ThemeId
  label: string
  description: string
  /** 预览色板 [背景, 主色, 强调色]（devhub ThemeSwitcher 同构） */
  preview: [string, string, string]
}

export interface ModeOption {
  mode: ColorMode
  label: string
}

export interface ThemeContextValue {
  themeId: ThemeId
  setThemeId: (id: ThemeId) => void
  mode: ColorMode
  setMode: (m: ColorMode) => void
  /** 解析后的实际暗色状态（system 时取系统偏好） */
  dark: boolean
  themeOptions: ThemeOption[]
  modeOptions: ModeOption[]
}

export const THEME_STORAGE_KEY = "u1s12api.admin.theme.v2"
export const MODE_STORAGE_KEY = "u1s12api.admin.mode"
/** v1 单维 key（mode 即主题），仅用于迁移读取 */
export const LEGACY_THEME_STORAGE_KEY = "u1s12api.admin.theme"
export const DEFAULT_THEME_ID: ThemeId = "u1s1-official"
export const DEFAULT_MODE: ColorMode = "system"

export const themeOptions: ThemeOption[] = [
  { id: "u1s1-official", label: "U1S1 官网", description: "暖纸米底 · 黑面板 · 橙红硬投影", preview: ["oklch(0.953 0.014 88)", "oklch(0.24 0.012 70)", "oklch(0.62 0.22 33)"] },
  { id: "porcelain-moss", label: "青瓷苔原", description: "瓷白灰绿", preview: ["oklch(0.966 0.007 155)", "oklch(0.55 0.12 148)", "oklch(0.888 0.027 142)"] },
  { id: "tungsten-dark", label: "黑钨", description: "钨钢深石墨", preview: ["oklch(0.145 0.011 165)", "oklch(0.79 0.145 127)", "oklch(0.275 0.03 145)"] },
  { id: "neo-brutalism", label: "新旷野主义", description: "黑描边硬投影 · 方格纸 · 朱红", preview: ["oklch(0.968 0.014 87)", "oklch(0.56 0.21 27)", "oklch(0.19 0.012 62)"] },
  { id: "apple-glass", label: "苹果磨砂玻璃", description: "macOS 风 mesh 光斑", preview: ["oklch(0.95 0.008 260)", "oklch(0.55 0.2 265)", "oklch(0.82 0.13 330)"] },
  { id: "clear-glass", label: "透明玻璃", description: "高透霓虹 · 炫彩渐变", preview: ["oklch(0.17 0.03 285)", "oklch(0.78 0.15 220)", "oklch(0.55 0.24 285)"] },
  { id: "paper-ink", label: "纸上墨", description: "暖纸极简 · 衬线 · 朱砂", preview: ["oklch(0.977 0.011 85)", "oklch(0.5 0.16 32)", "oklch(0.91 0.025 80)"] },
  { id: "terminal", label: "终端", description: "OLED 黑 · 荧光绿等宽 · 扫描线", preview: ["oklch(0.13 0.008 150)", "oklch(0.8 0.22 145)", "oklch(0.78 0.17 90)"] },
]

export const modeOptions: ModeOption[] = [
  { mode: "system", label: "跟随系统" },
  { mode: "light", label: "亮色" },
  { mode: "dark", label: "暗色" },
]

export const ThemeContext = createContext<ThemeContextValue | null>(null)

export function useDashboardTheme() {
  const context = useContext(ThemeContext)
  if (!context) {
    throw new Error("useDashboardTheme must be used within ThemeProvider")
  }
  return context
}
