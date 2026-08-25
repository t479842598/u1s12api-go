import { useCallback, useEffect, useState } from "react"
import { api, ApiClientError } from "@/lib/api-client"
import type { OverviewData } from "@/types"
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"
import { Badge } from "@/components/ui/badge"
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import { PageLoading } from "@/components/shared/PageLoading"
import { Activity, Coins, KeyRound, Zap } from "lucide-react"

function fmtTokens(n: number): string {
  if (n >= 1e9) return `${(n / 1e9).toFixed(2)} B`
  if (n >= 1e6) return `${(n / 1e6).toFixed(2)} M`
  if (n >= 1e3) return `${(n / 1e3).toFixed(1)} K`
  return String(n)
}

export default function DashboardPage() {
  const [data, setData] = useState<OverviewData | null>(null)
  const [error, setError] = useState<string | null>(null)

  const load = useCallback(async () => {
    try {
      setData(await api.overview())
      setError(null)
    } catch (err) {
      if (err instanceof ApiClientError && err.status === 401) return
      setError(err instanceof Error ? err.message : String(err))
    }
  }, [])

  useEffect(() => {
    load()
    const t = setInterval(load, 10000)
    return () => clearInterval(t)
  }, [load])

  if (!data) return error ? <p className="text-destructive">{error}</p> : <PageLoading />

  const maxDay = Math.max(1, ...data.daily.map((d) => d.requests))

  const cards = [
    {
      title: "今日请求",
      value: String(data.today.requests),
      sub: `全部 ${fmtTokens(data.totals.requests)} 次`,
      icon: Activity,
    },
    {
      title: "今日 Tokens",
      value: fmtTokens(data.today.total_tokens),
      sub: `累计 ${fmtTokens(data.totals.total_tokens)}`,
      icon: Coins,
    },
    {
      title: "可用 Key",
      value: `${data.keys.active ?? 0} / ${data.keys.total}`,
      sub: `冷却 ${data.keys.cooldown ?? 0} · 禁用 ${data.keys.disabled ?? 0}`,
      icon: KeyRound,
    },
    {
      title: "今日成本估算",
      value: `$${data.today.cost_usd.toFixed(4)}`,
      sub: "按上游标价折算",
      icon: Zap,
    },
  ]

  return (
    <div className="flex flex-col gap-6">
      <div>
        <h1 className="text-2xl font-semibold tracking-tight">概览</h1>
        <p className="text-sm text-muted-foreground">
          上游 {data.upstream_base_url} · 客户端版本 x-u1s1-version: {data.u1s1_version}
        </p>
      </div>

      <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-4">
        {cards.map(({ title, value, sub, icon: Icon }) => (
          <Card key={title}>
            <CardHeader className="flex flex-row items-center justify-between pb-2">
              <CardTitle className="text-sm font-medium text-muted-foreground">
                {title}
              </CardTitle>
              <Icon className="h-4 w-4 text-muted-foreground" />
            </CardHeader>
            <CardContent>
              <div className="text-2xl font-semibold">{value}</div>
              <p className="mt-1 text-xs text-muted-foreground">{sub}</p>
            </CardContent>
          </Card>
        ))}
      </div>

      {/* 指纹信息 */}
      <Card>
        <CardContent className="flex flex-wrap items-center gap-x-6 gap-y-2 py-4 text-sm">
          <span className="font-medium">当前指纹档案</span>
          <Badge variant="secondary">{data.fingerprint.label}</Badge>
          <code className="rounded bg-muted px-2 py-0.5 text-xs">
            {data.fingerprint.user_agent}
          </code>
          <span className="text-muted-foreground">Node {data.fingerprint.runtime}</span>
        </CardContent>
      </Card>

      <div className="grid grid-cols-1 gap-4 lg:grid-cols-5">
        {/* 近 14 天趋势 */}
        <Card className="lg:col-span-3">
          <CardHeader>
            <CardTitle className="text-base">近 14 天请求量</CardTitle>
          </CardHeader>
          <CardContent>
            <div className="flex h-40 items-end gap-1.5">
              {data.daily.map((d) => (
                <div
                  key={d.date}
                  className="group relative flex flex-1 flex-col items-center justify-end"
                  style={{ height: "100%" }}
                >
                  <div
                    className="w-full rounded-t bg-primary/70 transition-colors group-hover:bg-primary"
                    style={{ height: `${Math.max(2, (d.requests / maxDay) * 100)}%` }}
                  />
                  <div className="pointer-events-none absolute -top-8 z-10 hidden whitespace-nowrap rounded bg-foreground px-2 py-1 text-[10px] text-background group-hover:block">
                    {d.date}: {d.requests} 次 / {fmtTokens(d.total_tokens)}
                  </div>
                </div>
              ))}
            </div>
          </CardContent>
        </Card>

        {/* 模型分布 */}
        <Card className="lg:col-span-2">
          <CardHeader>
            <CardTitle className="text-base">模型用量 Top5（30 天）</CardTitle>
          </CardHeader>
          <CardContent className="flex flex-col gap-2">
            {data.models.length === 0 && (
              <p className="text-sm text-muted-foreground">暂无数据</p>
            )}
            {data.models.map((m) => (
              <div key={m.model} className="flex items-center justify-between text-sm">
                <code className="text-xs">{m.model}</code>
                <span className="text-muted-foreground">
                  {m.requests} 次 · {fmtTokens(m.total_tokens)}
                </span>
              </div>
            ))}
          </CardContent>
        </Card>
      </div>

      {/* 最近请求 */}
      <Card>
        <CardHeader>
          <CardTitle className="text-base">最近请求</CardTitle>
        </CardHeader>
        <CardContent>
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>时间</TableHead>
                <TableHead>模型</TableHead>
                <TableHead>状态</TableHead>
                <TableHead className="text-right">Tokens</TableHead>
                <TableHead className="text-right">耗时</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {data.recent.length === 0 && (
                <TableRow>
                  <TableCell colSpan={5} className="text-center text-muted-foreground">
                    暂无请求
                  </TableCell>
                </TableRow>
              )}
              {data.recent.map((r) => (
                <TableRow key={r.id}>
                  <TableCell className="whitespace-nowrap text-xs">
                    {new Date(r.ts * 1000).toLocaleString("zh-CN")}
                  </TableCell>
                  <TableCell>
                    <code className="text-xs">{r.model}</code>
                  </TableCell>
                  <TableCell>
                    <Badge variant={r.status === "success" ? "secondary" : "destructive"}>
                      {r.status === "success" ? r.http_status : r.error.slice(0, 30) || r.http_status}
                    </Badge>
                  </TableCell>
                  <TableCell className="text-right text-xs">{r.total_tokens}</TableCell>
                  <TableCell className="text-right text-xs">{r.duration_ms}ms</TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </CardContent>
      </Card>
    </div>
  )
}
