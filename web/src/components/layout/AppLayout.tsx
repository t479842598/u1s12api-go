import { useEffect, useRef, useState } from "react"
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
  Terminal,
  Settings,
  LogOut,
  Sun,
  Moon,
  Monitor,
  Menu,
  X,
  ChevronDown,
  Check,
  Palette,
  CalendarCheck,
  FileText,
} from "lucide-react"
import { cn } from "@/lib/utils"

const NAV_ITEMS = [
  { to: "/admin/dashboard", label: "概览", sub: "额度与运行状态", icon: LayoutDashboard },
  { to: "/admin/u1s1-keys", label: "U1S1 Key", sub: "官网密钥与额度", icon: KeyRound },
  { to: "/admin/keys", label: "API Key", sub: "网关访问密钥", icon: ShieldCheck },
  { to: "/admin/accounts", label: "授权账号", sub: "设备与授权状态", icon: CalendarCheck },
  { to: "/admin/requests", label: "请求记录", sub: "调用与账单明细", icon: FileText },
  { to: "/admin/logs", label: "运行日志", sub: "服务实时输出", icon: ScrollText },
  { to: "/admin/model-test", label: "模型测试", sub: "对话能力验证", icon: Terminal },
  { to: "/admin/settings", label: "设置", sub: "通知与系统配置", icon: Settings },
]

// ModeToggle 明暗切换按钮：三态循环 跟随系统→亮→暗→跟随系统，默认跟随系统。
function ModeToggle() {
  const { mode, setMode, dark } = useDashboardTheme()
  const Icon = mode === "system" ? Monitor : dark ? Moon : Sun
  const label = mode === "system" ? "跟随系统" : mode === "light" ? "亮色" : "暗色"
  const next = mode === "system" ? "light" : mode === "light" ? "dark" : "system"
  return (
    <button
      type="button"
      onClick={() => setMode(next)}
      title={`明暗：${label}（点击切换）`}
      aria-label="切换明暗"
      className="flex h-9 w-9 items-center justify-center rounded-lg border border-border/60 text-muted-foreground transition-colors hover:bg-accent hover:text-foreground"
    >
      <Icon className="h-4 w-4" />
    </button>
  )
}

// ThemeDropdown 主题切换器（项目中枢 ThemeSwitcher 同构）：按钮显示当前风格名 + 三色预览点；
// 下拉每行 = 叠放色点组 + 名称 + 描述，当前项高亮带勾。
function ThemeDropdown() {
  const { themeId, setThemeId, themeOptions } = useDashboardTheme()
  const [open, setOpen] = useState(false)
  const ref = useRef<HTMLDivElement>(null)

  useEffect(() => {
    if (!open) return
    const onDocMouseDown = (e: MouseEvent) => {
      if (ref.current && !ref.current.contains(e.target as Node)) setOpen(false)
    }
    document.addEventListener("mousedown", onDocMouseDown)
    return () => document.removeEventListener("mousedown", onDocMouseDown)
  }, [open])

  const active = themeOptions.find((o) => o.id === themeId) ?? themeOptions[0]

  return (
    <div ref={ref} className="relative">
      <button
        type="button"
        onClick={() => setOpen((o) => !o)}
        aria-expanded={open}
        aria-haspopup="listbox"
        aria-label="选择主题风格"
        title="选择主题风格"
        className="flex h-9 items-center gap-2 rounded-lg border border-border/60 px-2.5 text-sm text-muted-foreground transition-colors hover:bg-accent hover:text-foreground"
      >
        <Palette className="h-4 w-4 shrink-0" />
        <span className="hidden max-w-28 truncate sm:inline">{active.label}</span>
        <span className="flex gap-1">
          {active.preview.map((c) => (
            <span key={c} className="h-3 w-3 rounded-full border border-black/10" style={{ background: c }} />
          ))}
        </span>
        <ChevronDown className={cn("h-3 w-3 transition-transform", open && "rotate-180")} />
      </button>
      {open && (
        <div className="floating-panel absolute right-0 top-full z-50 mt-2 w-72 rounded-xl border border-border bg-popover p-2 text-popover-foreground shadow-lg" role="listbox">
          {themeOptions.map((opt) => {
            const isActive = opt.id === themeId
            return (
              <button
                key={opt.id}
                type="button"
                role="option"
                aria-selected={isActive}
                onClick={() => {
                  setThemeId(opt.id)
                  setOpen(false)
                }}
                className={cn(
                  "flex w-full items-center gap-3 rounded-lg px-2.5 py-2 text-left transition-colors",
                  isActive ? "bg-accent text-accent-foreground" : "hover:bg-accent/60",
                )}
              >
                <span className="flex shrink-0 -space-x-1">
                  {opt.preview.map((c) => (
                    <span key={c} className="h-5 w-5 rounded-full border-2 border-popover" style={{ background: c }} />
                  ))}
                </span>
                <span className="min-w-0 flex-1">
                  <span className="block truncate text-sm font-medium">{opt.label}</span>
                  <span className="block truncate text-xs text-muted-foreground">{opt.description}</span>
                </span>
                {isActive && <Check className="h-4 w-4 shrink-0 text-primary" />}
              </button>
            )
          })}
        </div>
      )}
    </div>
  )
}

export default function AppLayout() {
  const [mobileNavOpen, setMobileNavOpen] = useState(false)
  const { isAuthenticated, isLoading, logout } = useAuth()
  const navigate = useNavigate()
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

  if (isLoading) {
    return <PageLoading />
  }

  if (!isAuthenticated) {
    return <Navigate to="/admin/login" replace />
  }

  const handleLogout = async () => {
    await logout()
    navigate("/admin/login", { replace: true })
  }

  // 官网动态：公告数徽章 + 版本落后提示（侧栏底部文字链，官网风）
  const sitefeedLinks = (
    <div className="flex shrink-0 items-center justify-between px-1.5 text-xs text-muted-foreground">
      <Link
        to="/admin/sitefeed/announcements"
        className="flex items-center gap-1.5 underline decoration-border underline-offset-4 transition-colors hover:text-foreground"
      >
        官网公告
        {newAnnouncements > 0 && (
          <span className="rounded-full bg-brand/15 px-1.5 text-[10px] font-semibold text-brand">
            {newAnnouncements}
          </span>
        )}
      </Link>
      <Link
        to="/admin/sitefeed/changelog"
        className="flex items-center gap-1 underline decoration-border underline-offset-4 transition-colors hover:text-foreground"
      >
        更新记录
        <span
          className={cn(
            "font-mono text-[10px]",
            verBehind
              ? "font-semibold text-warning"
              : "text-muted-foreground/70",
          )}
        >
          {summary?.local_version ?? "—"}
          {summary?.npm_version && verBehind ? ` → ${summary.npm_version}` : ""}
        </span>
      </Link>
    </div>
  )

  return (
    <div className="admin-shell flex h-screen flex-col overflow-hidden bg-background">
      {/* 通栏固定顶栏：logo 左，主题/明暗/退出右；仅主内容区滚动 */}
      <header className="surface-side z-30 flex h-14 shrink-0 items-center justify-between gap-2 px-4">
        <div className="flex min-w-0 items-center gap-2.5">
          <button
            onClick={() => setMobileNavOpen((open) => !open)}
            className="flex h-10 w-10 shrink-0 items-center justify-center rounded-md text-muted-foreground hover:bg-accent lg:hidden"
            aria-label={mobileNavOpen ? "关闭导航菜单" : "打开导航菜单"}
            aria-expanded={mobileNavOpen}
          >
            {mobileNavOpen ? <X className="h-5 w-5" /> : <Menu className="h-5 w-5" />}
          </button>
          <LogoMark className="h-8 w-8 shrink-0" />
          <span className="shrink-0 text-sm font-semibold tracking-tight">U1S12API</span>
          <span className="hidden truncate text-xs text-muted-foreground sm:inline">
            U1S1 免费额度网关
          </span>
        </div>
        <div className="flex shrink-0 items-center gap-2">
          <ModeToggle />
          <ThemeDropdown />
          <button
            onClick={handleLogout}
            className="flex h-9 items-center gap-1.5 rounded-lg border border-border/60 px-2.5 text-sm text-muted-foreground transition-colors hover:bg-accent hover:text-foreground"
            title="退出登录"
          >
            <LogOut className="h-4 w-4" />
            <span className="hidden sm:inline">退出</span>
          </button>
        </div>
      </header>

      <div className="flex min-h-0 flex-1 gap-4 lg:p-4">
        {/* 固定悬浮侧栏：卡片按内容自然高度；内容超高时侧栏自身纵滚 */}
        <aside className="hidden h-full w-60 shrink-0 flex-col gap-3 overflow-y-auto pb-2 lg:flex">
          {/* 账号卡（对齐 U1S1 官网侧栏顶部）：标签 + 会话主体 + 登录状态点 */}
          <div data-slot="sidebar-account" className="floating-panel shrink-0 rounded-2xl border border-border p-3.5">
            <p className="font-mono text-[10px] uppercase tracking-[0.18em] text-muted-foreground">
              U1S12API Account
            </p>
            <p className="mt-1.5 truncate font-mono text-sm font-semibold">管理控制台</p>
            <p className="mt-2 flex items-center gap-1.5 text-xs">
              <span className="h-1.5 w-1.5 shrink-0 rounded-full bg-success" />
              会话已登录
            </p>
          </div>

          <nav data-slot="sidebar-nav" className="floating-panel shrink-0 rounded-2xl border border-border bg-card p-2">
            <div className="flex flex-col gap-0.5">
              {NAV_ITEMS.map(({ to, label, sub, icon: Icon }, i) => (
                <NavLink
                  key={to}
                  to={to}
                  className={({ isActive }) =>
                    cn(
                      "flex items-center gap-2.5 rounded-xl px-2.5 py-1.5 text-sm transition-colors",
                      isActive
                        ? "bg-sidebar-accent font-medium text-sidebar-accent-foreground"
                        : "text-sidebar-foreground/70 hover:bg-accent/60 hover:text-sidebar-foreground",
                    )
                  }
                >
                  {({ isActive }) => (
                    <>
                      {/* 序号徽章：激活项 brand 底（u1s1-official 下为橙红） */}
                      <span
                        className={cn(
                          "w-7 shrink-0 rounded-md py-0.5 text-center font-mono text-[10px] font-semibold tabular-nums",
                          isActive
                            ? "bg-brand text-brand-foreground"
                            : "bg-muted text-muted-foreground",
                        )}
                      >
                        {String(i + 1).padStart(2, "0")}
                      </span>
                      <Icon className="h-4 w-4 shrink-0" />
                      <span className="min-w-0 flex-1">
                        <span className="block truncate leading-snug">{label}</span>
                        <span
                          className={cn(
                            "block truncate text-[11px] leading-tight",
                            isActive ? "text-sidebar-accent-foreground/65" : "text-muted-foreground",
                          )}
                        >
                          {sub}
                        </span>
                      </span>
                    </>
                  )}
                </NavLink>
              ))}
            </div>
          </nav>

          {/* 官网动态：卡片外下划线灰字（对齐 U1S1 官网侧栏底部） */}
          {sitefeedLinks}
        </aside>

        {/* 主内容：唯一纵滚区域 */}
        <main className="min-w-0 flex-1 overflow-y-auto p-4 lg:p-5">
          <Outlet />
        </main>
      </div>

      {/* 移动端抽屉导航 */}
      {mobileNavOpen && (
        <div className="fixed inset-0 z-50 lg:hidden">
          <div
            className="mobile-overlay-enter absolute inset-0 bg-black/45 backdrop-blur-[2px]"
            onClick={() => setMobileNavOpen(false)}
          />
          <aside className="mobile-drawer-enter absolute inset-y-0 left-0 flex w-72 max-w-[85vw] flex-col border-r border-border bg-sidebar shadow-2xl">
            <div className="flex h-14 shrink-0 items-center gap-2 border-b border-sidebar-border px-4">
              <LogoMark className="h-8 w-8" />
              <span className="text-sm font-semibold text-sidebar-foreground">U1S12API</span>
              <button
                onClick={() => setMobileNavOpen(false)}
                className="ml-auto flex h-8 w-8 items-center justify-center rounded-md text-sidebar-foreground/60 hover:bg-sidebar-accent/50 hover:text-sidebar-foreground"
                aria-label="关闭菜单"
              >
                <X className="h-4 w-4" />
              </button>
            </div>

            <nav className="flex-1 overflow-y-auto px-3 py-3">
              <div className="flex flex-col gap-0.5">
                {NAV_ITEMS.map(({ to, label, icon: Icon }) => (
                  <NavLink
                    key={to}
                    to={to}
                    onClick={() => setMobileNavOpen(false)}
                    className={({ isActive }) =>
                      cn(
                        "flex items-center gap-2.5 rounded-md px-3 py-2.5 text-sm transition-colors",
                        isActive
                          ? "bg-sidebar-accent font-medium text-sidebar-accent-foreground"
                          : "text-sidebar-foreground/70 hover:bg-sidebar-accent/50 hover:text-sidebar-foreground",
                      )
                    }
                  >
                    <Icon className="h-4 w-4 shrink-0" />
                    {label}
                  </NavLink>
                ))}
              </div>
            </nav>

            <div className="border-t border-sidebar-border p-3">
              <div className="flex flex-col gap-1 text-xs text-muted-foreground">
                <Link
                  to="/admin/sitefeed/announcements"
                  onClick={() => setMobileNavOpen(false)}
                  className="rounded-md px-3 py-1.5 hover:bg-sidebar-accent/50"
                >
                  官网公告{newAnnouncements > 0 ? `（${newAnnouncements} 条新公告）` : ""}
                </Link>
                <Link
                  to="/admin/sitefeed/changelog"
                  onClick={() => setMobileNavOpen(false)}
                  className="rounded-md px-3 py-1.5 hover:bg-sidebar-accent/50"
                >
                  更新记录{summary?.local_version ? `（当前 ${summary.local_version}）` : ""}
                </Link>
              </div>
              <button
                onClick={() => {
                  setMobileNavOpen(false)
                  handleLogout()
                }}
                className="mt-1.5 flex w-full items-center gap-2.5 rounded-md px-3 py-2 text-sm text-sidebar-foreground/60 hover:bg-sidebar-accent/50 hover:text-sidebar-foreground"
              >
                <LogOut className="h-4 w-4" />
                退出登录
              </button>
            </div>
          </aside>
        </div>
      )}
    </div>
  )
}
