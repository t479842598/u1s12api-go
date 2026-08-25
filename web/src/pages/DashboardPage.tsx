import { useCallback, useEffect, useRef, useState } from "react"
import { useNavigate } from "react-router-dom"
import { api, ApiClientError } from "@/lib/api-client"
import type { OverviewData, RequestStats, StatsRange } from "@/types"
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Skeleton } from "@/components/ui/skeleton"
import {
  Select, SelectContent, SelectItem, SelectTrigger, SelectValue,
} from "@/components/ui/select"
import { PageLoading } from "@/components/shared/PageLoading"
import {
  Activity, ArrowRight, BarChart3, CheckCircle2, Coins, FileText, KeyRound,
  Plus, Terminal, Zap,
} from "lucide-react"
import type { ReactNode } from "react"

// ---- 工具函数 ----

function fmtTokens(n: number): string {
  if (n >= 1e9) return `${(n / 1e9).toFixed(2)}B`
  if (n >= 1e6) return `${(n / 1e6).toFixed(2)}M`
  if (n >= 1e3) return `${(n / 1e3).toFixed(1)}K`
  return String(n)
}

// ---- 子组件 ----

function StatCard({ icon, title, value, detail, loading }: {
  icon: ReactNode; title: string; value: ReactNode; detail?: ReactNode; loading: boolean
}) {
  return (
    <Card className="border-border/60 shadow-sm">
      <CardContent className="pt-4">
        <div className="flex items-start justify-between gap-3">
          <div className="min-w-0">
            <p className="text-[11px] font-medium text-muted-foreground">{title}</p>
            {loading ? <Skeleton className="mt-2 h-7 w-24" /> : (
              <div className="mt-1 truncate text-2xl font-bold tracking-tight">{value}</div>
            )}
          </div>
          <div className="flex size-9 shrink-0 items-center justify-center rounded-lg bg-primary/10 text-primary">{icon}</div>
        </div>
        {loading ? <Skeleton className="mt-3 h-4 w-32" /> : detail ? <div className="mt-2 text-xs text-muted-foreground">{detail}</div> : null}
      </CardContent>
    </Card>
  )
}

function ValueMetric({ label, value, color = "default" }: {
  label: string; value: ReactNode; color?: "default" | "success" | "destructive" | "warning"
}) {
  const cls = { default: "text-foreground", success: "text-green-600", destructive: "text-red-600", warning: "text-amber-600" }[color]
  return (
    <div className="rounded-lg border bg-muted/25 px-3 py-2.5">
      <p className="text-[11px] text-muted-foreground">{label}</p>
      <p className={`mt-1 text-lg font-semibold ${cls}`}>{value}</p>
    </div>
  )
}

const STATS_RANGE_OPTIONS: { value: StatsRange; label: string }[] = [
  { value: "1d", label: "当天" },
  { value: "3d", label: "近 3 天" },
  { value: "7d", label: "近 7 天" },
  { value: "30d", label: "近 30 天" },
  { value: "all", label: "全部" },
]

function StatsRangeSelect({ value, onValueChange }: { value: StatsRange; onValueChange: (v: StatsRange) => void }) {
  return (
    <Select value={value} onValueChange={(v) => onValueChange((v ?? "all") as StatsRange)}>
      <SelectTrigger className="w-28 h-8 text-xs">
        <SelectValue />
      </SelectTrigger>
      <SelectContent align="end">
        {STATS_RANGE_OPTIONS.map((opt) => (
          <SelectItem key={opt.value} value={opt.value}>{opt.label}</SelectItem>
        ))}
      </SelectContent>
    </Select>
  )
}

function UsageBars({ entries, loading, emptyLabel, kind }: {
  entries: [string, { count: number; prompt_tokens: number; completion_tokens: number; total_tokens: number }][]
  loading: boolean; emptyLabel: string; kind: "model" | "key"
}) {
  const maxTokens = Math.max(...entries.map(([, d]) => d.total_tokens), 1)
  if (loading) return <Skeleton className="h-48 w-full" />
  if (entries.length === 0) return <p className="py-12 text-center text-xs text-muted-foreground">{emptyLabel}</p>
  return (
    <div className="space-y-3">
      {entries.slice(0, 6).map(([label, data], i) => {
        const width = Math.max(6, (data.total_tokens / maxTokens) * 100)
        return (
          <div key={label} className="rounded-xl border border-border/50 bg-muted/10 p-3 transition-colors hover:bg-muted/30">
            <div className="mb-2 flex items-center justify-between gap-3 text-xs">
              <div className="flex min-w-0 items-center gap-2">
                <span className="flex size-5 shrink-0 items-center justify-center rounded-md bg-primary/10 text-[10px] font-semibold text-primary">{i + 1}</span>
                <span className={`min-w-0 truncate ${kind === "model" ? "font-mono" : "font-medium"}`} title={label}>{label}</span>
              </div>
              <div className="flex shrink-0 items-center gap-2 text-muted-foreground">
                <span className="font-semibold text-foreground">{fmtTokens(data.total_tokens)}</span>
                <span>{data.count} 次</span>
              </div>
            </div>
            <div className="h-2 overflow-hidden rounded-full bg-muted">
              <div className="h-full rounded-full bg-gradient-to-r from-primary via-sky-400 to-amber-400 transition-all" style={{ width: `${width}%` }} />
            </div>
            <div className="mt-1.5 flex flex-wrap gap-x-3 gap-y-0.5 text-[10px] text-muted-foreground">
              <span>输入 {data.prompt_tokens.toLocaleString()}</span>
              <span>输出 {data.completion_tokens.toLocaleString()}</span>
              <span>总计 {data.total_tokens.toLocaleString()}</span>
            </div>
          </div>
        )
      })}
    </div>
  )
}

// ---- 主页面 ----

export default function DashboardPage() {
  const navigate = useNavigate()
  const [overview, setOverview] = useState<OverviewData | null>(null)
  const [stats, setStats] = useState<RequestStats | null>(null)
  const [statsRange, setStatsRange] = useState<StatsRange>("all")
  const [loading, setLoading] = useState(true)
  const [statsLoading, setStatsLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const pollRef = useRef<ReturnType<typeof setInterval>>(undefined as unknown as ReturnType<typeof setInterval>)

  const loadOverview = useCallback(async () => {
    try {
      const d = await api.overview()
      setOverview(d)
      setError(null)
    } catch (err) {
      if (err instanceof ApiClientError && err.status === 401) return
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setLoading(false)
    }
  }, [])

  const loadStats = useCallback(async () => {
    setStatsLoading(true)
    try {
      setStats(await api.requestStats(statsRange))
    } catch { /* ignore */ }
    setStatsLoading(false)
  }, [statsRange])

  useEffect(() => {
    loadOverview()
    loadStats()
    pollRef.current = setInterval(loadOverview, 15000)
    return () => clearInterval(pollRef.current)
  }, [loadOverview, loadStats])

  if (loading && !overview) return <PageLoading />

  const o = overview
  const s = stats
  const today = o?.today ?? { requests: 0, input_tokens: 0, output_tokens: 0, total_tokens: 0, cost_usd: 0 }
  const totals = o?.totals ?? { requests: 0, input_tokens: 0, output_tokens: 0, total_tokens: 0, cost_usd: 0 }
  const keyStats = o?.keys ?? { total: 0 }
  const daily = o?.daily ?? []
  const recent = o?.recent ?? []
  const fp = o?.fingerprint ?? { profile: "", label: "", user_agent: "", runtime: "" }
  const maxDay = Math.max(1, ...daily.map((d) => d.requests))

  // 统计条
  const statsTotal = s?.total ?? 0
  const statsSuccess = s?.success ?? 0
  const statsError = s?.error ?? 0
  const successPct = statsTotal > 0 ? Math.round((statsSuccess / statsTotal) * 100) : 0
  const errorPct = statsTotal > 0 ? Math.round((statsError / statsTotal) * 100) : 0
  const quietPct = Math.max(0, 100 - successPct - errorPct)

  // 模型/Key 统计条目
  const modelEntries = Object.entries(s?.by_model ?? {}).sort((a, b) => b[1].total_tokens - a[1].total_tokens)
  const rangeLabel = STATS_RANGE_OPTIONS.find((o) => o.value === statsRange)?.label ?? "全部"

  return (
    <div className="space-y-5">
      {/* 错误提示 */}
      {error && (
        <div className="rounded-lg border border-destructive/50 bg-destructive/5 px-4 py-3 text-sm text-destructive">{error}</div>
      )}

      {/* 顶部卡片 */}
      <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 xl:grid-cols-4">
        <StatCard icon={<Activity className="size-4" />} title="今日请求" value={today.requests}
          detail={`累计 ${totals.requests} 次`} loading={loading} />
        <StatCard icon={<Coins className="size-4" />} title="今日 Tokens" value={fmtTokens(today.total_tokens)}
          detail={`输入 ${fmtTokens(today.input_tokens)} · 输出 ${fmtTokens(today.output_tokens)}`} loading={loading} />
        <StatCard icon={<KeyRound className="size-4" />} title="可用 Key" value={`${keyStats.active ?? 0} / ${keyStats.total}`}
          detail={`冷却 ${keyStats.cooldown ?? 0} · 禁用 ${keyStats.disabled ?? 0}`} loading={loading} />
        <StatCard icon={<Zap className="size-4" />} title="今日成本" value={`$${today.cost_usd.toFixed(4)}`}
          detail={`累计 $${totals.cost_usd.toFixed(4)}`} loading={loading} />
      </div>

      {/* 请求统计 + 模型用量 */}
      <div className="grid grid-cols-1 gap-5 xl:grid-cols-[1fr_360px]">
        <div className="space-y-5">
          {/* 请求统计 */}
          <Card className="border-border/60 shadow-sm">
            <CardHeader className="pb-1">
              <div className="flex flex-wrap items-center justify-between gap-2">
                <CardTitle className="flex items-center gap-2 text-sm font-medium">
                  <Activity className="size-4 text-primary" />请求统计
                  <Badge variant="outline" className="text-[10px] font-normal">{rangeLabel}</Badge>
                </CardTitle>
                <StatsRangeSelect value={statsRange} onValueChange={setStatsRange} />
              </div>
            </CardHeader>
            <CardContent className="space-y-4">
              {statsLoading ? (
                <div className="space-y-3"><Skeleton className="h-20 w-full" /><Skeleton className="h-10 w-full" /></div>
              ) : (
                <>
                  <div className="grid grid-cols-2 gap-2 sm:grid-cols-5">
                    <ValueMetric label="总请求" value={statsTotal} />
                    <ValueMetric label="成功" value={statsSuccess} color="success" />
                    <ValueMetric label="失败" value={statsError} color="destructive" />
                    <ValueMetric label="总 Token" value={fmtTokens(s?.total_tokens ?? 0)} color="warning" />
                    <ValueMetric label="平均耗时" value={s?.avg_duration_ms ? `${Math.round(s.avg_duration_ms)}ms` : "-"} />
                  </div>
                  <div>
                    <div className="mb-1.5 flex items-center justify-between text-[11px] text-muted-foreground">
                      <span>成功率 {successPct}%</span>
                      <span>失败率 {errorPct}%</span>
                    </div>
                    <div className="flex h-2.5 overflow-hidden rounded-full bg-muted">
                      <div className="bg-green-500 transition-all" style={{ width: `${successPct}%` }} />
                      <div className="bg-red-500 transition-all" style={{ width: `${errorPct}%` }} />
                      <div className="bg-muted-foreground/15" style={{ width: `${quietPct}%` }} />
                    </div>
                  </div>
                </>
              )}
            </CardContent>
          </Card>

          {/* 模型用量 */}
          <Card className="border-border/60 shadow-sm">
            <CardHeader className="pb-1">
              <div className="flex items-center justify-between gap-3">
                <CardTitle className="flex items-center gap-2 text-sm font-medium">
                  <BarChart3 className="size-4 text-primary" />模型用量
                </CardTitle>
                <Button variant="ghost" size="sm" className="h-7 text-xs" onClick={() => navigate("/admin/requests")}>
                  查看记录 <ArrowRight className="ml-1 size-3" />
                </Button>
              </div>
            </CardHeader>
            <CardContent>
              <UsageBars entries={modelEntries} loading={statsLoading} emptyLabel="暂无模型用量数据" kind="model" />
            </CardContent>
          </Card>
        </div>

        <div className="space-y-5">
          {/* 快捷操作 */}
          <Card className="border-border/60 shadow-sm">
            <CardHeader className="pb-1">
              <CardTitle className="flex items-center gap-2 text-sm font-medium">
                <Zap className="size-4 text-primary" />快捷操作
              </CardTitle>
            </CardHeader>
            <CardContent className="grid grid-cols-2 gap-2">
              <Button variant="outline" size="sm" className="h-11 justify-start text-xs" onClick={() => navigate("/admin/u1s1-keys")}>
                <Plus className="mr-1 size-4" />导入 Key
              </Button>
              <Button variant="outline" size="sm" className="h-11 justify-start text-xs" onClick={() => navigate("/admin/keys")}>
                <KeyRound className="mr-1 size-4" />创建 API Key
              </Button>
              <Button variant="outline" size="sm" className="h-11 justify-start text-xs" onClick={() => navigate("/admin/requests")}>
                <FileText className="mr-1 size-4" />请求记录
              </Button>
              <Button variant="outline" size="sm" className="h-11 justify-start text-xs" onClick={() => navigate("/admin/model-test")}>
                <Terminal className="mr-1 size-4" />模型测试
              </Button>
            </CardContent>
          </Card>

          {/* 14 天趋势 */}
          <Card className="border-border/60 shadow-sm">
            <CardHeader className="pb-1">
              <CardTitle className="flex items-center gap-2 text-sm font-medium">
                <BarChart3 className="size-4 text-primary" />近 14 天趋势
              </CardTitle>
            </CardHeader>
            <CardContent>
              {loading ? <Skeleton className="h-32 w-full" /> : (
                <div className="flex h-32 items-end gap-1.5">
                  {daily.map((d) => (
                    <div key={d.date} className="group relative flex flex-1 flex-col items-center justify-end" style={{ height: "100%" }}>
                      <div className="w-full rounded-t bg-primary/70 transition-colors group-hover:bg-primary"
                        style={{ height: `${Math.max(2, (d.requests / maxDay) * 100)}%` }} />
                      <div className="pointer-events-none absolute -top-8 z-10 hidden whitespace-nowrap rounded bg-foreground px-2 py-1 text-[10px] text-background group-hover:block">
                        {d.date}: {d.requests} 次 / {fmtTokens(d.total_tokens)}
                      </div>
                    </div>
                  ))}
                </div>
              )}
            </CardContent>
          </Card>

          {/* 指纹信息 */}
          <Card className="border-border/60 shadow-sm">
            <CardHeader className="pb-1">
              <CardTitle className="flex items-center gap-2 text-sm font-medium">
                <CheckCircle2 className="size-4 text-primary" />请求头指纹
              </CardTitle>
            </CardHeader>
            <CardContent className="text-xs text-muted-foreground space-y-1">
              <div>档案：<Badge variant="secondary" className="text-[10px]">{fp.label}</Badge></div>
              <code className="block rounded bg-muted px-2 py-1 text-[10px]">{fp.user_agent}</code>
              <div>Node {fp.runtime}</div>
            </CardContent>
          </Card>
        </div>
      </div>

      {/* 最近请求 */}
      <Card className="border-border/60 shadow-sm">
        <CardHeader className="pb-1">
          <div className="flex items-center justify-between">
            <CardTitle className="flex items-center gap-2 text-sm font-medium">
              <FileText className="size-4 text-primary" />最近请求
            </CardTitle>
            <Button variant="ghost" size="sm" className="h-7 text-xs" onClick={() => navigate("/admin/requests")}>
              查看全部 <ArrowRight className="ml-1 size-3" />
            </Button>
          </div>
        </CardHeader>
        <CardContent>
          {loading ? <Skeleton className="h-32 w-full" /> : recent.length === 0 ? (
            <p className="py-8 text-center text-xs text-muted-foreground">暂无请求</p>
          ) : (
            <div className="overflow-x-auto">
              <table className="w-full min-w-[500px] border-collapse text-xs">
                <thead>
                  <tr className="border-b border-border/60">
                    <th className="py-1.5 pr-3 text-left font-medium text-muted-foreground">时间</th>
                    <th className="py-1.5 pr-3 text-left font-medium text-muted-foreground">模型</th>
                    <th className="py-1.5 pr-3 text-left font-medium text-muted-foreground">状态</th>
                    <th className="py-1.5 pr-3 text-right font-medium text-muted-foreground">Tokens</th>
                    <th className="py-1.5 pr-3 text-right font-medium text-muted-foreground">耗时</th>
                    <th className="py-1.5 text-right font-medium text-muted-foreground">来源</th>
                  </tr>
                </thead>
                <tbody>
                  {recent.map((r) => (
                    <tr key={r.id} className="border-b border-border/40 last:border-0">
                      <td className="py-1.5 pr-3 whitespace-nowrap">{new Date(r.ts * 1000).toLocaleString("zh-CN")}</td>
                      <td className="py-1.5 pr-3"><code className="text-[10px]">{r.model}</code></td>
                      <td className="py-1.5 pr-3">
                        <Badge variant={r.status === "success" ? "secondary" : "destructive"} className="text-[10px]">
                          {r.status === "success" ? r.http_status : r.error.slice(0, 30) || r.http_status}
                        </Badge>
                      </td>
                      <td className="py-1.5 pr-3 text-right">{r.total_tokens}</td>
                      <td className="py-1.5 pr-3 text-right">{r.duration_ms}ms</td>
                      <td className="py-1.5 text-right text-muted-foreground">{r.client_ip || "—"}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </CardContent>
      </Card>
    </div>
  )
}