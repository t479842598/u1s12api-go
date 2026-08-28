import { useState } from "react"
import { NavLink, Outlet, Navigate, useNavigate } from "react-router-dom"
import { useAuth } from "@/hooks/use-auth"
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
} from "lucide-react"
import { cn } from "@/lib/utils"

const NAV_ITEMS = [
  { to: "/admin/dashboard", label: "概览", icon: LayoutDashboard },
  { to: "/admin/u1s1-keys", label: "U1S1 Key", icon: KeyRound },
  { to: "/admin/keys", label: "API Key", icon: ShieldCheck },
  { to: "/admin/accounts", label: "签到管理", icon: CalendarCheck },
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
