import { useCallback, useEffect, useState } from "react"
import { api } from "@/lib/api-client"
import type { RequestsData } from "@/types"
import { Button } from "@/components/ui/button"
import { Badge } from "@/components/ui/badge"
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"
import { Input } from "@/components/ui/input"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
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
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { ChevronLeft, ChevronRight, Trash2 } from "lucide-react"

const PAGE_SIZE = 50

export default function RequestsPage() {
  const [data, setData] = useState<RequestsData | null>(null)
  const [page, setPage] = useState(0)
  const [modelFilter, setModelFilter] = useState("")
  const [statusFilter, setStatusFilter] = useState("all")
  const [detail, setDetail] = useState<RequestsData["items"][number] | null>(null)

  const load = useCallback(async () => {
    setData(
      await api.requests({
        limit: PAGE_SIZE,
        offset: page * PAGE_SIZE,
        model: modelFilter || undefined,
        status: statusFilter === "all" ? undefined : statusFilter,
      }),
    )
  }, [page, modelFilter, statusFilter])

  useEffect(() => {
    load()
    const t = setInterval(load, 15000)
    return () => clearInterval(t)
  }, [load])

  if (!data) return <PageLoading />

  const totalPages = Math.max(1, Math.ceil(data.total / PAGE_SIZE))

  return (
    <div className="flex flex-col gap-6">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">请求记录</h1>
          <p className="text-sm text-muted-foreground">
            共 {data.total} 条（按北京时间聚合统计）
          </p>
        </div>
        <Button
          variant="outline"
          onClick={async () => {
            await api.clearRequests()
            setPage(0)
            load()
          }}
        >
          <Trash2 className="mr-2 h-4 w-4" />
          清空记录
        </Button>
      </div>

      <Card>
        <CardHeader className="flex flex-row flex-wrap items-center gap-3">
          <CardTitle className="text-base">转发日志</CardTitle>
          <CardDescription>点击行查看详情</CardDescription>
          <div className="ml-auto flex gap-2">
            <Input
              placeholder="按模型过滤"
              className="w-48"
              value={modelFilter}
              onChange={(e) => {
                setModelFilter(e.target.value)
                setPage(0)
              }}
            />
            <Select
              value={statusFilter}
              onValueChange={(v) => {
                setStatusFilter(v ?? "all")
                setPage(0)
              }}
            >
              <SelectTrigger className="w-28">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="all">全部状态</SelectItem>
                <SelectItem value="success">成功</SelectItem>
                <SelectItem value="error">失败</SelectItem>
              </SelectContent>
            </Select>
          </div>
        </CardHeader>
        <CardContent>
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>时间</TableHead>
                <TableHead>模型</TableHead>
                <TableHead>本地 Key</TableHead>
                <TableHead>状态</TableHead>
                <TableHead className="text-right">入/出 Tokens</TableHead>
                <TableHead className="text-right">耗时</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {data.items.length === 0 && (
                <TableRow>
                  <TableCell colSpan={6} className="py-10 text-center text-muted-foreground">
                    暂无请求记录
                  </TableCell>
                </TableRow>
              )}
              {data.items.map((r) => (
                <TableRow
                  key={r.id}
                  className="cursor-pointer"
                  onClick={() => setDetail(r)}
                >
                  <TableCell className="whitespace-nowrap text-xs">
                    {new Date(r.ts * 1000).toLocaleString("zh-CN")}
                  </TableCell>
                  <TableCell>
                    <code className="text-xs">{r.model}</code>
                  </TableCell>
                  <TableCell className="text-xs">{r.api_key_name}</TableCell>
                  <TableCell>
                    <Badge variant={r.status === "success" ? "secondary" : "destructive"}>
                      {r.status === "success" ? `${r.http_status}` : r.http_status ? `${r.http_status}` : "ERR"}
                    </Badge>
                  </TableCell>
                  <TableCell className="text-right text-xs">
                    {r.input_tokens} / {r.output_tokens}
                  </TableCell>
                  <TableCell className="text-right text-xs">{r.duration_ms}ms</TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>

          <div className="mt-4 flex items-center justify-between text-sm text-muted-foreground">
            <span>
              第 {page + 1} / {totalPages} 页
            </span>
            <div className="flex gap-2">
              <Button
                size="sm"
                variant="outline"
                disabled={page === 0}
                onClick={() => setPage((p) => p - 1)}
              >
                <ChevronLeft className="h-4 w-4" />
              </Button>
              <Button
                size="sm"
                variant="outline"
                disabled={page + 1 >= totalPages}
                onClick={() => setPage((p) => p + 1)}
              >
                <ChevronRight className="h-4 w-4" />
              </Button>
            </div>
          </div>
        </CardContent>
      </Card>

      <Dialog open={!!detail} onOpenChange={(open) => !open && setDetail(null)}>
        <DialogContent className="max-w-lg">
          <DialogHeader>
            <DialogTitle>请求详情 #{detail?.id}</DialogTitle>
            <DialogDescription>
              {detail &&
                new Date(detail.ts * 1000).toLocaleString("zh-CN")}
            </DialogDescription>
          </DialogHeader>
          {detail && (
            <div className="flex flex-col gap-2 text-sm">
              <Row label="模型">
                <code>{detail.model}</code>
              </Row>
              <Row label="本地 Key">{detail.api_key_name}</Row>
              <Row label="上游 Key ID">#{detail.upstream_key_id}</Row>
              <Row label="模式">{detail.stream ? "流式" : "非流式"}</Row>
              <Row label="HTTP 状态">{detail.http_status}</Row>
              <Row label="Tokens">
                入 {detail.input_tokens} · 出 {detail.output_tokens} · 共{" "}
                {detail.total_tokens}
              </Row>
              <Row label="成本估算">${detail.cost_usd.toFixed(6)}</Row>
              <Row label="客户端 IP">{detail.client_ip || "—"}</Row>
              <Row label="耗时">{detail.duration_ms} ms</Row>
              {detail.error && (
                <div className="rounded bg-destructive/10 p-3 text-xs break-all text-destructive">
                  {detail.error}
                </div>
              )}
            </div>
          )}
        </DialogContent>
      </Dialog>
    </div>
  )
}

function Row({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="flex items-start justify-between gap-4">
      <span className="shrink-0 text-muted-foreground">{label}</span>
      <span className="break-all text-right">{children}</span>
    </div>
  )
}
