import { useCallback, useEffect, useState } from "react"
import { toast } from "sonner"
import { api, ApiClientError } from "@/lib/api-client"
import type { UpstreamKeyItem, UpstreamKeysData } from "@/types"
import { Button } from "@/components/ui/button"
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"
import { Badge } from "@/components/ui/badge"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { Textarea } from "@/components/ui/textarea"
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import { PageLoading } from "@/components/shared/PageLoading"
import {
  CircleCheck,
  CircleX,
  Clock3,
  Download,
  RefreshCw,
  Trash2,
} from "lucide-react"

function statusBadge(k: UpstreamKeyItem) {
  if (k.status === "active")
    return (
      <Badge className="gap-1 bg-emerald-600 hover:bg-emerald-600">
        <CircleCheck className="h-3 w-3" /> 可用
      </Badge>
    )
  if (k.status === "cooldown") {
    const until = k.cooldown_until ? new Date(k.cooldown_until * 1000).toLocaleString("zh-CN") : ""
    return (
      <Badge variant="outline" className="gap-1 border-amber-500 text-amber-600">
        <Clock3 className="h-3 w-3" /> 冷却{until && ` 至 ${until}`}
      </Badge>
    )
  }
  return (
    <Badge variant="destructive" className="gap-1">
      <CircleX className="h-3 w-3" /> 禁用
    </Badge>
  )
}

function fmtUSD(v: number): string {
  if (v < 0) return "—"
  return `$${v.toFixed(2)}`
}

function fmtTime(unix: number): string {
  if (!unix) return "—"
  return new Date(unix * 1000).toLocaleString("zh-CN")
}

export default function UpstreamKeysPage() {
  const [data, setData] = useState<UpstreamKeysData | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [importOpen, setImportOpen] = useState(false)
  const [importText, setImportText] = useState("")
  const [busy, setBusy] = useState<string | null>(null)

  const load = useCallback(async () => {
    try {
      setData(await api.u1s1Keys())
      setError(null)
    } catch (err) {
      if (err instanceof ApiClientError && err.status === 401) return
      setError(err instanceof Error ? err.message : String(err))
    }
  }, [])

  useEffect(() => {
    load()
    const t = setInterval(load, 15000)
    return () => clearInterval(t)
  }, [load])

  if (!data) return error ? <p className="text-destructive">{error}</p> : <PageLoading />

  const handleImport = async () => {
    if (!importText.trim()) return
    setBusy("import")
    try {
      const r = await api.importU1s1KeysText(importText)
      toast.success(`导入完成：新增 ${r.added}，跳过重复 ${r.skipped}，无效 ${r.invalid}`)
      setImportOpen(false)
      setImportText("")
      load()
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "导入失败")
    } finally {
      setBusy(null)
    }
  }

  const handleCheckOne = async (id: number) => {
    setBusy(`check-${id}`)
    try {
      await api.checkU1s1Quota(id)
      toast.success("配额已刷新")
      load()
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "查询失败")
    } finally {
      setBusy(null)
    }
  }

  const handleCheckAll = async () => {
    if (data.keys.length === 0) return
    setBusy("check-all")
    toast.info("开始批量检查配额（每把间隔 0.3 秒）…")
    try {
      const r = await api.checkAllQuotas()
      toast.success(`批量检查完成：${r.ok}/${r.total} 成功`)
      load()
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "批量检查失败")
    } finally {
      setBusy(null)
    }
  }

  const toggleStatus = async (k: UpstreamKeyItem) => {
    const target = k.status === "disabled" ? "active" : "disabled"
    setBusy(`toggle-${k.id}`)
    try {
      await api.setU1s1KeyStatus(k.id, target)
      load()
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "操作失败")
    } finally {
      setBusy(null)
    }
  }

  const removeKey = async (id: number) => {
    setBusy(`del-${id}`)
    try {
      await api.deleteU1s1Key(id)
      toast.success("已删除")
      load()
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "删除失败")
    } finally {
      setBusy(null)
    }
  }

  const stats = data.stats

  return (
    <div className="flex flex-col gap-6">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">U1S1 Key 池</h1>
          <p className="text-sm text-muted-foreground">
            共 {stats.total} 把 · 可用 {stats.active ?? 0} · 冷却 {stats.cooldown ?? 0} · 禁用{" "}
            {stats.disabled ?? 0}；额度耗尽的 Key 自动冷却到北京时间 0 点并切换下一把
          </p>
        </div>
        <div className="flex gap-2">
          <Button variant="outline" onClick={handleCheckAll} disabled={busy === "check-all" || data.keys.length === 0}>
            <RefreshCw className={`mr-2 h-4 w-4 ${busy === "check-all" ? "animate-spin" : ""}`} />
            批量查配额
          </Button>
          <Button onClick={() => setImportOpen(true)}>
            <Download className="mr-2 h-4 w-4" />
            一键导入
          </Button>
        </div>
      </div>

      <Card>
        <CardHeader>
          <CardTitle className="text-base">Key 列表</CardTitle>
          <CardDescription>密钥以掩码显示；「查配额」调用上游 /me 接口</CardDescription>
        </CardHeader>
        <CardContent>
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Key</TableHead>
                <TableHead>状态</TableHead>
                <TableHead>账号</TableHead>
                <HeadRight>今日剩余</HeadRight>
                <HeadRight>永久余额</HeadRight>
                <HeadRight>累计请求</HeadRight>
                <TableHead>配额更新时间</TableHead>
                <TableHead className="text-right">操作</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {data.keys.length === 0 && (
                <TableRow>
                  <TableCell colSpan={8} className="py-10 text-center text-muted-foreground">
                    还没有 Key。点击右上角「一键导入」，每行粘贴一把 u1s1-
                    开头的 Key（可带备注），或到 u1s1.io/dashboard 注册领取免费额度。
                  </TableCell>
                </TableRow>
              )}
              {data.keys.map((k) => (
                <TableRow key={k.id}>
                  <TableCell>
                    <div className="flex flex-col">
                      <code className="text-xs">{k.key_masked}</code>
                      {k.note && (
                        <span className="text-xs text-muted-foreground">{k.note}</span>
                      )}
                    </div>
                  </TableCell>
                  <TableCell>{statusBadge(k)}</TableCell>
                  <TableCell className="text-xs">{k.email || "—"}</TableCell>
                  <RightCell>{fmtUSD(k.daily_free_remaining_usd)}</RightCell>
                  <RightCell>{fmtUSD(k.remaining_usd)}</RightCell>
                  <RightCell>{k.total_requests}</RightCell>
                  <TableCell className="whitespace-nowrap text-xs text-muted-foreground">
                    {fmtTime(k.quota_checked_at)}
                  </TableCell>
                  <TableCell className="text-right">
                    <div className="flex justify-end gap-1">
                      <Button
                        size="sm"
                        variant="ghost"
                        title="查询配额"
                        disabled={busy === `check-${k.id}`}
                        onClick={() => handleCheckOne(k.id)}
                      >
                        <RefreshCw
                          className={`h-4 w-4 ${busy === `check-${k.id}` ? "animate-spin" : ""}`}
                        />
                      </Button>
                      <Button
                        size="sm"
                        variant="ghost"
                        title={k.status === "disabled" ? "重新启用" : "禁用"}
                        disabled={busy === `toggle-${k.id}`}
                        onClick={() => toggleStatus(k)}
                      >
                        {k.status === "disabled" ? (
                          <CircleCheck className="h-4 w-4" />
                        ) : (
                          <CircleX className="h-4 w-4" />
                        )}
                      </Button>
                      <Button
                        size="sm"
                        variant="ghost"
                        title="删除"
                        disabled={busy === `del-${k.id}`}
                        onClick={() => removeKey(k.id)}
                      >
                        <Trash2 className="h-4 w-4 text-destructive" />
                      </Button>
                    </div>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </CardContent>
      </Card>

      <Dialog open={importOpen} onOpenChange={setImportOpen}>
        <DialogContent className="max-w-lg">
          <DialogHeader>
            <DialogTitle>一键导入 U1S1 Key</DialogTitle>
            <DialogDescription>
              每行一把 Key，支持「Key 备注」写法；自动跳过空行、# 注释行和非 u1s1- 前缀行。
            </DialogDescription>
          </DialogHeader>
          <Textarea
            rows={10}
            placeholder={"u1s1-xxxxxxxxxxxxxxxx 备注：主号\nu1s1-yyyyyyyyyyyyyyyy\n# 这是注释"}
            value={importText}
            onChange={(e) => setImportText(e.target.value)}
          />
          <DialogFooter>
            <Button variant="outline" onClick={() => setImportOpen(false)}>
              取消
            </Button>
            <Button onClick={handleImport} disabled={!importText.trim() || busy === "import"}>
              {busy === "import" ? "导入中…" : "导入"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}

function HeadRight({ children }: { children: React.ReactNode }) {
  return <TableHead className="text-right">{children}</TableHead>
}

function RightCell({ children }: { children: React.ReactNode }) {
  return <TableCell className="text-right text-xs">{children}</TableCell>
}
