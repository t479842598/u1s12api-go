import { useCallback, useEffect, useRef, useState } from "react"
import { api } from "@/lib/api-client"
import type { LogEntry } from "@/types"
import { Badge } from "@/components/ui/badge"
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"

export default function LogsPage() {
  const [items, setItems] = useState<LogEntry[]>([])
  const [autoScroll, setAutoScroll] = useState(true)
  const boxRef = useRef<HTMLDivElement>(null)
  const lastIdRef = useRef(0)

  const poll = useCallback(async () => {
    try {
      const d = await api.logs({ since_id: lastIdRef.current, limit: 500 })
      if (d.items.length > 0) {
        lastIdRef.current = d.items[d.items.length - 1].id
        setItems((prev) => [...prev, ...d.items].slice(-2000))
      }
    } catch {
      // 静默重试
    }
  }, [])

  useEffect(() => {
    lastIdRef.current = 0
    setItems([])
    poll()
    const t = setInterval(poll, 3000)
    return () => clearInterval(t)
  }, [poll])

  useEffect(() => {
    if (autoScroll && boxRef.current) {
      boxRef.current.scrollTop = boxRef.current.scrollHeight
    }
  }, [items, autoScroll])

  const levelVariant = (lv: string) =>
    lv === "ERROR" ? "destructive" : lv === "WARN" ? "outline" : "secondary"

  return (
    <div className="flex flex-col gap-6">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">运行日志</h1>
          <p className="text-sm text-muted-foreground">内存环形缓冲最近 2000 条，每 3 秒增量拉取</p>
        </div>
        <label className="flex items-center gap-2 text-sm text-muted-foreground">
          <input
            type="checkbox"
            checked={autoScroll}
            onChange={(e) => setAutoScroll(e.target.checked)}
          />
          自动滚动
        </label>
      </div>

      <Card>
        <CardHeader>
          <CardTitle className="text-base">实时输出</CardTitle>
          <CardDescription>级别：INFO / WARN / ERROR / DEBUG</CardDescription>
        </CardHeader>
        <CardContent>
          <div
            ref={boxRef}
            className="h-[65vh] overflow-y-auto rounded-md bg-zinc-950 p-4 font-mono text-xs leading-relaxed text-zinc-200"
          >
            {items.length === 0 && (
              <p className="text-zinc-500">等待日志输出…</p>
            )}
            {items.map((e) => (
              <div key={e.id} className="whitespace-pre-wrap break-all">
                <span className="text-zinc-500">{e.ts}</span>{" "}
                <Badge variant={levelVariant(e.level)} className="mr-1 align-middle text-[10px]">
                  {e.level}
                </Badge>{" "}
                <span className="text-sky-400">{e.name}</span>: {e.msg}
              </div>
            ))}
          </div>
        </CardContent>
      </Card>
    </div>
  )
}
