import { useCallback, useEffect, useState } from "react"
import { toast } from "sonner"
import { api } from "@/lib/api-client"
import type { LocalKeyItem, LocalKeysData } from "@/types"
import { Button } from "@/components/ui/button"
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"
import { Badge } from "@/components/ui/badge"
import { Input } from "@/components/ui/input"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import { PageLoading } from "@/components/shared/PageLoading"
import { CopyButton } from "@/components/shared/CopyButton"
import { Plus, Trash2 } from "lucide-react"

function fmtTime(unix: number): string {
  if (!unix) return "—"
  return new Date(unix * 1000).toLocaleString("zh-CN")
}

export default function LocalKeysPage() {
  const [data, setData] = useState<LocalKeysData | null>(null)
  const [name, setName] = useState("")
  const [note, setNote] = useState("")
  const [creating, setCreating] = useState(false)
  const [createdKey, setCreatedKey] = useState<LocalKeyItem | null>(null)

  const load = useCallback(async () => {
    setData(await api.localKeys())
  }, [])

  useEffect(() => {
    load()
  }, [load])

  if (!data) return <PageLoading />

  const handleCreate = async () => {
    setCreating(true)
    try {
      const created = await api.createLocalKey(name.trim(), note.trim())
      // 创建响应携带完整 key，仅此一次展示
      setCreatedKey({ ...created })
      setName("")
      setNote("")
      load()
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "创建失败")
    } finally {
      setCreating(false)
    }
  }

  const toggle = async (k: LocalKeyItem) => {
    await api.updateLocalKey(k.name, { enabled: !k.enabled })
    load()
  }

  const remove = async (k: LocalKeyItem) => {
    await api.deleteLocalKey(k.name)
    toast.success(`已删除 ${k.name}`)
    load()
  }

  return (
    <div className="flex flex-col gap-6">
      <div>
        <h1 className="text-2xl font-semibold tracking-tight">本地 API Key</h1>
        <p className="text-sm text-muted-foreground">
          分发给客户端使用的 sk- Key（OpenAI 兼容端点鉴权），可随时启停
        </p>
      </div>

      <Card>
        <CardHeader>
          <CardTitle className="text-base">新建 Key</CardTitle>
        </CardHeader>
        <CardContent className="flex flex-wrap gap-2">
          <Input
            placeholder="名称（如 my-cli）"
            className="w-48"
            value={name}
            onChange={(e) => setName(e.target.value)}
          />
          <Input
            placeholder="备注（可选）"
            className="w-64"
            value={note}
            onChange={(e) => setNote(e.target.value)}
          />
          <Button onClick={handleCreate} disabled={creating}>
            <Plus className="mr-2 h-4 w-4" />
            {creating ? "生成中…" : "生成"}
          </Button>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle className="text-base">Key 列表</CardTitle>
          <CardDescription>完整密钥只在创建时展示一次，请妥善保存</CardDescription>
        </CardHeader>
        <CardContent>
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>名称</TableHead>
                <TableHead>Key</TableHead>
                <TableHead>状态</TableHead>
                <TableHead className="text-right">请求数</TableHead>
                <TableHead>最后使用</TableHead>
                <TableHead className="text-right">操作</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {data.keys.length === 0 && (
                <TableRow>
                  <TableCell colSpan={6} className="py-10 text-center text-muted-foreground">
                    还没有本地 Key，先在上面生成一个
                  </TableCell>
                </TableRow>
              )}
              {data.keys.map((k) => (
                <TableRow key={k.id}>
                  <TableCell>
                    <div className="flex flex-col">
                      <span className="font-medium">{k.name}</span>
                      {k.note && (
                        <span className="text-xs text-muted-foreground">{k.note}</span>
                      )}
                    </div>
                  </TableCell>
                  <TableCell>
                    <div className="flex items-center gap-1">
                      <code className="rounded bg-muted px-2 py-0.5 text-xs">
                        {k.key_masked}
                      </code>
                    </div>
                  </TableCell>
                  <TableCell>
                    <Badge variant={k.enabled ? "secondary" : "destructive"}>
                      {k.enabled ? "启用" : "停用"}
                    </Badge>
                  </TableCell>
                  <TableCell className="text-right text-xs">{k.total_requests}</TableCell>
                  <TableCell className="whitespace-nowrap text-xs text-muted-foreground">
                    {fmtTime(k.last_used_at)}
                  </TableCell>
                  <TableCell className="text-right">
                    <div className="flex justify-end gap-1">
                      <Button size="sm" variant="ghost" onClick={() => toggle(k)}>
                        {k.enabled ? "停用" : "启用"}
                      </Button>
                      <Button size="sm" variant="ghost" onClick={() => remove(k)}>
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

      {/* 完整 key 一次性展示 */}
      <Dialog open={!!createdKey} onOpenChange={(open) => !open && setCreatedKey(null)}>
        <DialogContent className="max-w-lg">
          <DialogHeader>
            <DialogTitle>Key 已生成：{createdKey?.name}</DialogTitle>
            <DialogDescription>请立即复制保存，关闭后无法再次查看完整内容。</DialogDescription>
          </DialogHeader>
          <div className="flex items-center gap-2">
            <code className="flex-1 break-all rounded bg-muted px-3 py-2 text-xs">
              {createdKey?.key}
            </code>
            <CopyButton text={createdKey?.key ?? ""} />
          </div>
          <DialogFooter>
            <Button onClick={() => setCreatedKey(null)}>我已保存</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}
