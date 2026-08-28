import { useCallback, useEffect, useRef, useState } from "react"
import { toast } from "sonner"
import { api, ApiClientError } from "@/lib/api-client"
import type { AccountItem, AccountsData } from "@/types"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
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
  CheckCircle2,
  Clock3,
  ExternalLink,
  Plus,
  RefreshCw,
  Trash2,
  Unplug,
} from "lucide-react"

function fmtTime(unix: number): string {
  if (!unix) return "—"
  return new Date(unix * 1000).toLocaleString("zh-CN")
}

function fmtTokens(v: number): string {
  if (v < 0) return "—"
  if (v >= 1e8) return `${Math.round(v / 1e8 * 10) / 10} 亿`
  if (v >= 1e4) return `${Math.round(v / 1e4).toLocaleString("en-US")} 万`
  return v.toLocaleString("en-US")
}

export default function AccountsPage() {
  const [data, setData] = useState<AccountsData | null>(null)
  const [error, setError] = useState<string | null>(null)

  const [addOpen, setAddOpen] = useState(false)
  const [addEmail, setAddEmail] = useState("")
  const [addPwd, setAddPwd] = useState("")
  const [addNote, setAddNote] = useState("")
  const [busy, setBusy] = useState<string | null>(null)

  // 设备授权弹窗状态
  const [authOpen, setAuthOpen] = useState(false)
  const [authAcc, setAuthAcc] = useState<AccountItem | null>(null)
  const [verifyUrl, setVerifyUrl] = useState("")
  const [expiresIn, setExpiresIn] = useState(0)
  const [countdown, setCountdown] = useState(0)
  const [authState, setAuthState] = useState<"idle" | "confirming">("idle")
  const countdownTimer = useRef<ReturnType<typeof setInterval> | null>(null)

  const load = useCallback(async () => {
    try {
      setData(await api.accounts())
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

  useEffect(() => {
    return () => {
      if (countdownTimer.current) clearInterval(countdownTimer.current)
    }
  }, [])

  if (!data) return error ? <p className="text-destructive">{error}</p> : <PageLoading />

  const handleAdd = async () => {
    if (!addEmail.trim()) return
    setBusy("add")
    try {
      await api.addAccount(addEmail.trim(), addPwd, addNote.trim())
      toast.success("账号已添加")
      setAddOpen(false)
      setAddEmail(""); setAddPwd(""); setAddNote("")
      load()
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "添加失败")
    } finally {
      setBusy(null)
    }
  }

  const toggleAccount = async (a: AccountItem) => {
    setBusy(`toggle-${a.id}`)
    try {
      await api.updateAccount(a.id, { enabled: !a.enabled })
      load()
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "操作失败")
    } finally {
      setBusy(null)
    }
  }

  const removeAccount = async (id: number) => {
    setBusy(`del-${id}`)
    try {
      await api.deleteAccount(id)
      toast.success("已删除")
      load()
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "删除失败")
    } finally {
      setBusy(null)
    }
  }

  const startAuth = async (a: AccountItem) => {
    setBusy(`auth-${a.id}`)
    try {
      const r = await api.deviceStart(a.id)
      setAuthAcc(a)
      setVerifyUrl(r.verify_url)
      setExpiresIn(r.expires_in)
      setCountdown(r.expires_in)
      setAuthState("idle")
      setAuthOpen(true)
      if (countdownTimer.current) clearInterval(countdownTimer.current)
      countdownTimer.current = setInterval(() => {
        setCountdown((c) => (c <= 1 ? 0 : c - 1))
      }, 1000)
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "发起授权失败")
    } finally {
      setBusy(null)
    }
  }

  const confirmAuth = async () => {
    if (!authAcc) return
    setAuthState("confirming")
    try {
      await api.deviceConfirm(authAcc.id)
      toast.success("设备授权成功")
      if (countdownTimer.current) clearInterval(countdownTimer.current)
      setAuthOpen(false)
      load()
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "授权确认失败")
      setAuthState("idle")
    }
  }

  const checkAll = async () => {
    setBusy("check-all")
    toast.info("开始对全部已授权账号签到…")
    try {
      const r = await api.checkAllCheckin()
      toast.success(`签到完成：成功 ${r.ok}/${r.total}`)
      load()
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "签到失败")
    } finally {
      setBusy(null)
    }
  }

  const authCount = data.accounts.filter((a) => a.authorized).length

  return (
    <div className="flex flex-col gap-6">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">官网账号</h1>
          <p className="text-sm text-muted-foreground">
            共 {data.accounts.length} 个 · 已授权 {authCount} 个；授权后设备凭证用于消耗「仅限
            u1s1 客户端使用」的加量包并每日自动签到
          </p>
        </div>
        <div className="flex gap-2">
          <Button variant="outline" onClick={checkAll} disabled={busy === "check-all" || authCount === 0}>
            <RefreshCw className={`mr-2 h-4 w-4 ${busy === "check-all" ? "animate-spin" : ""}`} />
            全部签到
          </Button>
          <Button onClick={() => setAddOpen(true)}>
            <Plus className="mr-2 h-4 w-4" />
            添加账号
          </Button>
        </div>
      </div>

      <Card>
        <CardHeader>
          <CardTitle className="text-base">账号列表</CardTitle>
          <CardDescription>邮箱密码仅用于初始授权（浏览器登录一次）；设备凭证授权后自动签到</CardDescription>
        </CardHeader>
        <CardContent>
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>账号</TableHead>
                <TableHead>授权状态</TableHead>
                <TableHead>设备</TableHead>
                <TableHead>签到剩余</TableHead>
                <TableHead>最近签到</TableHead>
                <TableHead className="text-right">操作</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {data.accounts.length === 0 && (
                <TableRow>
                  <TableCell colSpan={6} className="py-10 text-center text-muted-foreground">
                    还没有官网账号。点击「添加账号」录入邮箱+密码，再对账号发起设备授权。
                  </TableCell>
                </TableRow>
              )}
              {data.accounts.map((a) => (
                <TableRow key={a.id}>
                  <TableCell>
                    <div className="flex flex-col">
                      <span className="text-sm">{a.email_masked}</span>
                      {a.note && <span className="text-xs text-muted-foreground">{a.note}</span>}
                      {!a.enabled && <span className="text-xs text-muted-foreground">（已停用）</span>}
                    </div>
                  </TableCell>
                  <TableCell>
                    {a.authorized ? (
                      <Badge className="gap-1 bg-emerald-600 hover:bg-emerald-600"><CheckCircle2 className="h-3 w-3" /> 已授权</Badge>
                    ) : (
                      <Badge variant="outline" className="gap-1"><Unplug className="h-3 w-3" /> 未授权</Badge>
                    )}
                  </TableCell>
                  <TableCell className="text-xs">
                    {a.device_name ? <code className="text-xs">{a.device_token_masked || a.device_name}</code> : <span className="text-muted-foreground">—</span>}
                  </TableCell>
                  <TableCell className="text-right text-xs">{fmtTokens(a.login_checkin_remaining)}</TableCell>
                  <TableCell className="whitespace-nowrap text-xs text-muted-foreground">{fmtTime(a.last_checkin_at)}</TableCell>
                  <TableCell className="text-right">
                    <div className="flex justify-end gap-1">
                      <Button size="sm" variant="ghost" title={a.authorized ? "重新授权" : "设备授权"}
                        disabled={busy === `auth-${a.id}`} onClick={() => startAuth(a)}>
                        <ExternalLink className="h-4 w-4" />
                      </Button>
                      <Button size="sm" variant="ghost" title={a.enabled ? "停用" : "启用"}
                        disabled={busy === `toggle-${a.id}`} onClick={() => toggleAccount(a)}>
                        {a.enabled ? <Clock3 className="h-4 w-4" /> : <CheckCircle2 className="h-4 w-4" />}
                      </Button>
                      <Button size="sm" variant="ghost" title="删除"
                        disabled={busy === `del-${a.id}`} onClick={() => removeAccount(a.id)}>
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

      {/* 添加账号 */}
      <Dialog open={addOpen} onOpenChange={setAddOpen}>
        <DialogContent className="max-w-md">
          <DialogHeader>
            <DialogTitle>添加官网账号</DialogTitle>
            <DialogDescription>邮箱+密码仅用于设备授权时浏览器登录一次，之后靠设备凭证自动签到。</DialogDescription>
          </DialogHeader>
          <div className="flex flex-col gap-3">
            <Input placeholder="邮箱" value={addEmail} onChange={(e) => setAddEmail(e.target.value)} />
            <Input type="password" placeholder="密码" value={addPwd} onChange={(e) => setAddPwd(e.target.value)} />
            <Input placeholder="备注（可选）" value={addNote} onChange={(e) => setAddNote(e.target.value)} />
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setAddOpen(false)}>取消</Button>
            <Button onClick={handleAdd} disabled={!addEmail.trim() || busy === "add"}>
              {busy === "add" ? "添加中…" : "添加"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* 设备授权 */}
      <Dialog open={authOpen} onOpenChange={(o) => { setAuthOpen(o); if (!o && countdownTimer.current) clearInterval(countdownTimer.current) }}>
        <DialogContent className="max-w-lg">
          <DialogHeader>
            <DialogTitle>设备授权 {authAcc?.email_masked}</DialogTitle>
            <DialogDescription>
              ① 在浏览器打开下面的授权链接，用该账号登录并批准设备；
              ② 回来点「我已授权」领取设备凭证。链接 {expiresIn} 秒内有效。
            </DialogDescription>
          </DialogHeader>
          <div className="flex flex-col gap-3">
            <a href={verifyUrl} target="_blank" rel="noreferrer"
              className="break-all rounded-md border bg-muted/50 px-3 py-2 text-xs text-blue-600 hover:underline">
              {verifyUrl}
            </a>
            <div className="flex items-center gap-2 text-sm">
              <Badge variant={countdown > 0 ? "outline" : "destructive"} className="gap-1">
                <Clock3 className="h-3 w-3" />
                剩余 {Math.floor(countdown / 60)}:{String(countdown % 60).padStart(2, "0")}
              </Badge>
              {countdown === 0 && <span className="text-xs text-destructive">已过期，可点「重新授权」</span>}
            </div>
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setAuthOpen(false)}>关闭</Button>
            <Button onClick={confirmAuth} disabled={authState === "confirming"}>
              {authState === "confirming" ? "确认中…" : "我已授权"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}
