import { useCallback, useEffect, useState } from "react"
import { Link } from "react-router-dom"
import { toast } from "sonner"
import { Megaphone, RefreshCw, ScrollText, ExternalLink } from "lucide-react"
import { api } from "@/lib/api-client"
import { useSitefeedSummary } from "@/hooks/use-sitefeed-summary"
import type { SitePostItem } from "@/types"
import { Button } from "@/components/ui/button"
import {
  Card,
  CardContent,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"
import { cn } from "@/lib/utils"

const DAY_MS = 24 * 3600 * 1000
const NEW_THRESHOLD_MS = 30 * DAY_MS

function fmtUnix(sec: number): string {
  if (!sec) return "—"
  return new Date(sec * 1000).toLocaleString("zh-CN", { hour12: false })
}

function fmtPublished(s: string): string {
  if (!s) return ""
  // 上游 published_at 为 UTC "YYYY-MM-DD HH:MM:SS"
  const d = new Date(s.replace(" ", "T") + "Z")
  if (Number.isNaN(d.getTime())) return s
  return d.toLocaleString("zh-CN", { hour12: false })
}

/** 前端版本比较（与后端 versionGreater 同语义）：a 是否大于 b。 */
function versionGreater(a: string, b: string): boolean {
  const as = a.replace(/^v/, "").split(".")
  const bs = b.replace(/^v/, "").split(".")
  for (let i = 0; i < as.length || i < bs.length; i++) {
    const av = as[i] ?? ""
    const bv = bs[i] ?? ""
    const an = Number.parseInt(av, 10)
    const bn = Number.parseInt(bv, 10)
    if (Number.isNaN(an) || Number.isNaN(bn)) {
      if (av !== bv) return av > bv
    } else if (an !== bn) {
      return an > bn
    }
  }
  return false
}

export default function SitefeedPage({ kind }: { kind: "announcement" | "changelog" }) {
  const { summary, reload } = useSitefeedSummary()
  const [items, setItems] = useState<SitePostItem[] | null>(null)
  const [refreshing, setRefreshing] = useState(false)

  const load = useCallback(async () => {
    const d = await reload()
    setItems(kind === "announcement" ? d.announcements : d.changelog)
  }, [kind, reload])

  useEffect(() => {
    load().catch((err) => toast.error(err instanceof Error ? err.message : "加载失败"))
  }, [load])

  const refresh = async () => {
    setRefreshing(true)
    try {
      const r = await api.sitefeedRefresh()
      const res = r.result
      if (res.error) {
        toast.warning(`检查完成但有错误: ${res.error}`)
      } else {
        const parts: string[] = []
        if (res.new_announcements?.length) parts.push(`公告 +${res.new_announcements.length}`)
        if (res.new_changelog?.length) parts.push(`更新记录 +${res.new_changelog.length}`)
        toast.success(parts.length ? `检查完成：${parts.join("，")}` : "检查完成：没有新内容")
        if (res.cli_pushed) toast.info(`u1s1-cli 有新版本 ${res.npm_version}，请同步 U1S1_VERSION`)
      }
      await load()
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "检查失败")
    } finally {
      setRefreshing(false)
    }
  }

  const isAnn = kind === "announcement"
  const nowMs = Date.now()
  const behind =
    summary && summary.npm_version
      ? versionGreater(summary.npm_version, summary.local_version)
      : false

  const entries = items ?? []
  const tabCls = (active: boolean) =>
    cn(
      "flex items-center gap-2 rounded-lg px-3 py-1.5 text-sm font-medium transition-colors",
      active ? "bg-primary/10 text-primary" : "text-muted-foreground hover:bg-muted hover:text-foreground",
    )

  return (
    <div className="flex flex-col gap-6">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">官网动态</h1>
          <p className="text-sm text-muted-foreground">
            u1s1.io 公告与更新记录（每 {summary?.check_interval_h ?? 24} 小时自动检查，新内容 Bark 推送
            {summary && (summary.bark_configured ? "：已配置" : "：未配置，仅入库展示")}）
          </p>
        </div>
        <div className="flex items-center gap-2">
          <Link to="/admin/sitefeed/announcements" className={tabCls(isAnn)}>
            <Megaphone className="h-4 w-4" /> 公告
          </Link>
          <Link to="/admin/sitefeed/changelog" className={tabCls(!isAnn)}>
            <ScrollText className="h-4 w-4" /> 更新记录
          </Link>
          <Button variant="secondary" disabled={refreshing} onClick={refresh}>
            <RefreshCw className={cn("mr-2 h-4 w-4", refreshing && "animate-spin")} />
            {refreshing ? "检查中…" : "立即检查"}
          </Button>
        </div>
      </div>

      {/* 状态卡 */}
      <Card>
        <CardContent className="flex flex-wrap items-center gap-x-8 gap-y-2 py-4 text-sm">
          <div>
            <span className="text-muted-foreground">客户端版本：</span>
            <span className="font-mono">{summary?.local_version ?? "—"}</span>
            {summary?.npm_version && (
              <span className={cn("ml-2 font-mono", behind ? "font-semibold text-amber-600 dark:text-amber-400" : "text-muted-foreground")}>
                → npm {summary.npm_version}
              </span>
            )}
            {behind && (
              <span className="ml-2 text-xs text-amber-600 dark:text-amber-400">
                ⚠️ CLI 有新版本，请到「设置」同步 U1S1_VERSION
              </span>
            )}
          </div>
          <div>
            <span className="text-muted-foreground">上次检查：</span>
            {fmtUnix(summary?.last_check_at ?? 0)}
          </div>
          <div>
            <span className="text-muted-foreground">下次检查：</span>
            {fmtUnix(summary?.next_check_at ?? 0)}
          </div>
        </CardContent>
      </Card>

      {/* 条目列表 */}
      {entries.length === 0 ? (
        <Card>
          <CardContent className="py-10 text-center text-sm text-muted-foreground">
            {items === null ? "加载中…" : "还没有数据，点击「立即检查」抓取一次"}
          </CardContent>
        </Card>
      ) : (
        <div className="flex flex-col gap-3">
          {entries.map((p) => {
            const isNew = nowMs - p.first_seen_at * 1000 < NEW_THRESHOLD_MS
            return (
              <Card key={p.id} className={cn(isNew && "border-primary/40")}>
                <CardHeader className="pb-2">
                  <div className="flex items-start justify-between gap-3">
                    <CardTitle className="text-base leading-snug">{p.title}</CardTitle>
                    <div className="flex shrink-0 items-center gap-2">
                      {isNew && (
                        <span className="rounded-full bg-primary/10 px-2 py-0.5 text-xs font-medium text-primary">
                          新
                        </span>
                      )}
                      {p.published_at && (
                        <span className="text-xs text-muted-foreground">{fmtPublished(p.published_at)}</span>
                      )}
                    </div>
                  </div>
                </CardHeader>
                <CardContent className="pt-0">
                  <p className="whitespace-pre-wrap text-sm leading-relaxed text-muted-foreground">
                    {p.summary}
                  </p>
                  {p.url && (
                    <a
                      href={`https://u1s1.io${p.url}`.replace("https://u1s1.iohttps://", "https://")}
                      target="_blank"
                      rel="noopener noreferrer"
                      className="mt-2 inline-flex items-center gap-1 text-xs text-primary hover:underline"
                    >
                      <ExternalLink className="h-3 w-3" /> 查看详情
                    </a>
                  )}
                </CardContent>
              </Card>
            )
          })}
        </div>
      )}
    </div>
  )
}
