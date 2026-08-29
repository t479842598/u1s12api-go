import { useState } from "react"
import { NavLink, Outlet, Navigate, useNavigate, Link } from "react-router-dom"
import { useAuth } from "@/hooks/use-auth"
import { useSitefeedSummary } from "@/hooks/use-sitefeed-summary"
import { useDashboardTheme } from "@/components/theme/theme-context"
import { PageLoading } from "@/components/shared/PageLoading"
import { LogoMark } from "@/components/shared/LogoMark"
import {
  LayoutDashboard,
  KeyRound,
  ShieldCheck,
  ScrollText,
  FileText,
  Terminal,
  Settings,
  LogOut,
  Sun,
  Moon,
  Monitor,
  Menu,
  X,
  CalendarCheck,
  Megaphone,
} from "lucide-react"
import { cn } from "@/lib/utils"

const NAV_ITEMS = [
  { to: "/admin/dashboard", label: "概览", icon: LayoutDashboard },
  { to: "/admin/u1s1-keys", label: "U1S1 Key", icon: KeyRound },
  { to: "/admin/keys", label: "API Key", icon: ShieldCheck },
  { to: "/admin/accounts", label: "授权账号", icon: CalendarCheck },
  { to: "/admin/requests", label: "请求记录", icon: FileText },
  { to: "/admin/logs", label: "运行日志", icon: ScrollText },
  { to: "/admin/model-test", label: "模型测试", icon: Terminal },
  { to: "/admin/settings", label: "设置", icon: Settings },
]

const themeIcons = {
  "porcelain-moss": Sun,
  "tungsten-dark": Moon,
  system: Monitor,
}

export default function AppLayout() {
  const [mobileNavOpen, setMobileNavOpen] = useState(false)
  const { isAuthenticated, isLoading, logout } = useAuth()
  const navigate = useNavigate()
  const { mode, setMode, options } = useDashboardTheme()
  const { summary } = useSitefeedSummary()

  // npm 最新版本落后于本地适配版本 → 高亮提示同步指纹
  const verBehind = (() => {
    if (!summary?.npm_version) return false
    const gt = (a: string, b: string) => {
      const as = a.replace(/^v/, "").split(".")
      const bs = b.replace(/^v/, "").split(".")
      for (let i = 0; i < as.length || i < bs.length; i++) {
        const an = Number.parseInt(as[i] ?? "", 10)
        const bn = Number.parseInt(bs[i] ?? "", 10)
        if (Number.isNaN(an) || Number.isNaN(bn)) return (as[i] ?? "") > (bs[i] ?? "")
        if (an !== bn) return an > bn
      }
      return false
    }
    return gt(summary.npm_version, summary.local_version)
  })()
  const newAnnouncements = summary
    ? summary.announcements.filter(
        (a) => Date.now() - a.first_seen_at * 1000 < 30 * 24 * 3600 * 1000,
      ).length
    : 0

  if (isLoading) return <PageLoading />
  if (!isAuthenticated) return <Navigate to="/admin/login" replace />

  const handleLogout = async () => {
    await logout()
    navigate("/admin/login", { replace: true })
  }

  const cycleTheme = () => {
    const idx = options.findIndex((o) => o.mode === mode)
    setMode(options[(idx + 1) % options.length].mode)
  }

  const CurrentThemeIcon = themeIcons[mode] ?? Monitor

  const nav = (
    <nav className="flex flex-1 flex-col gap-1 px-3">
      {NAV_ITEMS.map(({ to, label, icon: Icon }) => (
        <NavLink
          key={to}
          to={to}
          onClick={() => setMobileNavOpen(false)}
          className={({ isActive }) =>
            cn(
              "flex items-center gap-3 rounded-lg px-3 py-2 text-sm font-medium transition-colors",
              isActive
                ? "bg-primary/10 text-primary"
                : "text-muted-foreground hover:bg-muted hover:text-foreground",
            )
          }
        >
          <Icon className="h-4 w-4" />
          {label}
        </NavLink>
      ))}

      {/* 官网动态：侧边栏左下角双入口（公告 / 更新记录，叠放） */}
      <div className="mt-auto flex flex-col gap-1 border-t pt-3 pb-2">
        <Link
          to="/admin/sitefeed/announcements"
          onClick={() => setMobileNavOpen(false)}
          className="flex items-center gap-3 rounded-lg px-3 py-1.5 text-xs font-medium text-muted-foreground transition-colors hover:bg-muted hover:text-foreground"
        >
          <Megaphone className="h-3.5 w-3.5" />
          官网公告
          {newAnnouncements > 0 && (
            <span className="ml-auto rounded-full bg-primary/15 px-1.5 text-[10px] font-semibold text-primary">
              {newAnnouncements}
            </span>
          )}
        </Link>
        <Link
          to="/admin/sitefeed/changelog"
          onClick={() => setMobileNavOpen(false)}
          className="flex items-center gap-3 rounded-lg px-3 py-1.5 text-xs font-medium text-muted-foreground transition-colors hover:bg-muted hover:text-foreground"
        >
          <ScrollText className="h-3.5 w-3.5" />
          更新记录
          <span
            className={cn(
              "ml-auto font-mono text-[10px]",
              verBehind
                ? "font-semibold text-amber-600 dark:text-amber-400"
                : "text-muted-foreground/70",
            )}
          >
            {summary?.local_version ?? "—"}
            {summary?.npm_version && verBehind ? ` → ${summary.npm_version}` : ""}
          </span>
        </Link>
      </div>
    </nav>
  )

  const brand = (
    <div className="flex items-center gap-2.5 px-6 py-5">
      <LogoMark className="h-8 w-8" />
      <div className="flex flex-col">
        <span className="text-sm font-semibold tracking-tight">U1S12API</span>
        <span className="text-[11px] text-muted-foreground">U1S1 免费额度网关</span>
      </div>
    </div>
  )

  return (
    <div className="flex h-screen bg-background">
      {/* 桌面侧栏 */}
      <aside className="hidden w-60 shrink-0 flex-col border-r bg-card md:flex">
        {brand}
        {nav}
        <div className="flex items-center justify-between border-t px-4 py-3">
          <button
            onClick={cycleTheme}
            className="rounded-md p-2 text-muted-foreground transition-colors hover:bg-muted hover:text-foreground"
            title="切换主题"
          >
            <CurrentThemeIcon className="h-4 w-4" />
          </button>
          <button
            onClick={handleLogout}
            className="flex items-center gap-2 rounded-md p-2 text-sm text-muted-foreground transition-colors hover:bg-muted hover:text-foreground"
            title="退出登录"
          >
            <LogOut className="h-4 w-4" />
          </button>
        </div>
      </aside>

      {/* 移动端抽屉 */}
      {mobileNavOpen && (
        <div className="fixed inset-0 z-40 md:hidden">
          <div
            className="absolute inset-0 bg-black/40"
            onClick={() => setMobileNavOpen(false)}
          />
          <aside className="absolute left-0 top-0 flex h-full w-64 flex-col bg-card shadow-xl">
            <div className="flex items-center justify-between pr-3">
              {brand}
              <button onClick={() => setMobileNavOpen(false)} className="p-2">
                <X className="h-5 w-5" />
              </button>
            </div>
            {nav}
            <button
              onClick={handleLogout}
              className="m-3 flex items-center gap-2 rounded-md px-3 py-2 text-sm text-muted-foreground hover:bg-muted"
            >
              <LogOut className="h-4 w-4" /> 退出登录
            </button>
          </aside>
        </div>
      )}

      <main className="flex min-w-0 flex-1 flex-col overflow-y-auto">
        <header className="sticky top-0 z-30 flex items-center gap-3 border-b bg-background/95 px-6 py-3 backdrop-blur md:hidden">
          <button onClick={() => setMobileNavOpen(true)} className="p-1">
            <Menu className="h-5 w-5" />
          </button>
          <LogoMark className="h-6 w-6" />
          <span className="font-semibold">U1S12API</span>
        </header>
        <div className="mx-auto w-full max-w-[1400px] px-4 py-4">
          <Outlet />
        </div>
      </main>
    </div>
  )
}
