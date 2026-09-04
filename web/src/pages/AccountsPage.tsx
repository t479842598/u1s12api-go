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
import { CheckCircle2, Clock3, ExternalLink, Plus, RefreshCw, Unplug, Copy, KeyRound } from "lucide-react"

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
  const [countdown, setCountdown] = useState(0)
  const [authState, setAuthState] = useState<"idle" | "confirming" | "done">("idle")
  const [confirmMsg, setConfirmMsg] = useState("")
  const confirmAbortRef = useRef(false)
  const countdownTimer = useRef<ReturnType<typeof setInterval> | null>(null)

  // 一键登录（无需预填账号密码）
  const [ocOpen, setOcOpen] = useState(false)
  const [ocVerifyUrl, setOcVerifyUrl] = useState("")
  const [ocCountdown, setOcCountdown] = useState(0)
  const [ocSessionId, setOcSessionId] = useState("")
  const [ocState, setOcState] = useState<"idle" | "confirming" | "done">("idle")
  const [ocMsg, setOcMsg] = useState("")
  const ocAbortRef = useRef(false)

  // 设置密码（打卡需要保存账号密码才能自动登录）
  const [pwdOpen, setPwdOpen] = useState(false)
  const [pwdAcc, setPwdAcc] = useState<AccountItem | null>(null)
  const [pwdValue, setPwdValue] = useState("")

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
      setCountdown(r.expires_in)
      setAuthState("idle")
      setConfirmMsg("")
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

  const copyLink = (url?: string) => {
    navigator.clipboard.writeText(url ?? verifyUrl).then(() => {
      toast.success("授权链接已复制")
    }).catch(() => {
      toast.error("复制失败，请手动复制")
    })
  }

  const confirmAuth = async () => {
    if (!authAcc) return
    confirmAbortRef.current = false
    setAuthState("confirming")
    setConfirmMsg("正在确认授权，请稍候…")
    try {
      const maxTries = 180
      for (let i = 0; i < maxTries; i++) {
        if (confirmAbortRef.current) return
        const r = await api.deviceConfirm(authAcc.id)
        if (r.status === "authorized") {
          if (confirmAbortRef.current) return
          toast.success("设备授权成功")
          setAuthState("done")
          setConfirmMsg("授权成功，设备凭证已保存")
          if (countdownTimer.current) clearInterval(countdownTimer.current)
          setTimeout(() => { setAuthOpen(false); load() }, 1500)
          return
        }
        setConfirmMsg(`等待浏览器批准第 ${i + 1} 次…（请确认已在浏览器完成批准）`)
        await new Promise((resolve) => setTimeout(resolve, 5000))
        if (confirmAbortRef.current) return
      }
      if (!confirmAbortRef.current) {
        toast.error("授权超时，请重新发起授权")
        setAuthState("idle")
        setConfirmMsg("")
      }
    } catch (err) {
      if (!confirmAbortRef.current) {
        toast.error(err instanceof Error ? err.message : "授权确认失败")
        setAuthState("idle")
        setConfirmMsg("")
      }
    }
  }

  const startOneClick = async () => {
    setBusy("oc-start")
    try {
      const r = await api.oneClickStart()
      setOcVerifyUrl(r.verify_url)
      setOcSessionId(r.session_id)
      setOcCountdown(r.expires_in)
      setOcState("idle")
      setOcMsg("")
      setOcOpen(true)
      if (countdownTimer.current) clearInterval(countdownTimer.current)
      countdownTimer.current = setInterval(() => {
        setOcCountdown((c) => (c <= 1 ? 0 : c - 1))
      }, 1000)
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "发起一键登录失败")
    } finally {
      setBusy(null)
    }
  }

  const confirmOneClick = async () => {
    ocAbortRef.current = false
    setOcState("confirming")
    setOcMsg("正在确认授权，请稍候…")
    try {
      const maxTries = 180
      for (let i = 0; i < maxTries; i++) {
        if (ocAbortRef.current) return
        const r = await api.oneClickConfirm(ocSessionId)
        if (r.status === "authorized") {
          if (ocAbortRef.current) return
          toast.success("一键登录成功，账号已添加并自动导入 Key 池")
          setOcState("done")
          setOcMsg("登录成功，账号已添加")
          if (countdownTimer.current) clearInterval(countdownTimer.current)
          setTimeout(() => { setOcOpen(false); load() }, 1500)
          return
        }
        setOcMsg(`等待 U1S1 登录批准第 ${i + 1} 次…（请在浏览器完成登录并批准）`)
        await new Promise((resolve) => setTimeout(resolve, 5000))
        if (ocAbortRef.current) return
      }
      if (!ocAbortRef.current) {
        toast.error("授权超时，请重新发起一键登录")
        setOcState("idle")
        setOcMsg("")
      }
    } catch (err) {
      if (!ocAbortRef.current) {
        toast.error(err instanceof Error ? err.message : "一键登录确认失败")
        setOcState("idle")
        setOcMsg("")
      }
    }
  }

  const openPwd = (a: AccountItem) => {
    setPwdAcc(a)
    setPwdValue("")
    setPwdOpen(true)
  }

  const savePwd = async () => {
    if (!pwdAcc || !pwdValue.trim()) return
    setBusy(`pwd-${pwdAcc.id}`)
    try {
      await api.updateAccount(pwdAcc.id, { password: pwdValue })
      toast.success(`${pwdAcc.email_masked} 密码已保存`)
      setPwdOpen(false)
      setPwdAcc(null)
      setPwdValue("")
      load()
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "保存密码失败")
    } finally {
      setBusy(null)
    }
  }

  const checkinOne = async (a: AccountItem) => {
    if (!a.has_password) {
      toast.error(`账号 ${a.email_masked} 未保存密码，请先点击「设置密码」保存后再打卡`)
      openPwd(a)
      return
    }
    setBusy(`checkin-${a.id}`)
    try {
      const r = await api.checkinOne(a.id)
      if (r.ok) {
        toast.success(`${a.email_masked} 打卡完成`)
      }
      load()
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "打卡失败")
    } finally {
      setBusy(null)
    }
  }

  // 刷新单账号额度快照（/v1/me 拉取加量包）。
  const refreshQuota = async (a: AccountItem) => {
    setBusy(`quota-${a.id}`)
    try {
      await api.accountQuotaRefresh(a.id)
      toast.success(`${a.email_masked} 额度已刷新`)
      load()
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "刷新额度失败")
    } finally {
      setBusy(null)
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
          <h1 className="text-2xl font-semibold tracking-tight">授权账号</h1>
          <p className="text-sm text-muted-foreground">
            共 {data.accounts.length} 个账号 · 已授权 {authCount} 个；每日北京时间 0 点后自动网页打卡（capcat 人机验证 + 登录 + claim）领取 200 万 Token，也可点「打卡」手动触发
          </p>
        </div>
        <div className="flex gap-2">
          <Button variant="outline" size="sm" onClick={checkAll} disabled={busy === "check-all" || authCount === 0}>
            <RefreshCw className={`mr-1 h-4 w-4 ${busy === "check-all" ? "animate-spin" : ""}`} />
            全部签到
          </Button>
          <Button size="sm" variant="outline" onClick={startOneClick} disabled={busy === "oc-start"}>
            <ExternalLink className="mr-1 h-4 w-4" />
            一键登录
          </Button>
          <Button size="sm" onClick={() => setAddOpen(true)}>
            <Plus className="mr-1 h-4 w-4" />
            添加账号
          </Button>
        </div>
      </div>

      <Card>
        <CardHeader>
          <CardTitle className="text-base">账号列表</CardTitle>
          <CardDescription>录入账号并保存官网密码后，服务端自动完成设备授权与每日网页打卡（capcat 人机验证纯 API 求解，无需真浏览器）；账号列表展示各账号剩余额度（固定/每日赠送/邀请/签到）</CardDescription>
        </CardHeader>
        <CardContent>
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>账号</TableHead>
                <TableHead>授权状态</TableHead>
                <TableHead>剩余额度</TableHead>
                <TableHead>最近签到</TableHead>
                <TableHead className="text-right">操作</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {data.accounts.length === 0 && (
                <TableRow>
                  <TableCell colSpan={5} className="py-10 text-center text-muted-foreground">
                    还没有账号。点击「添加账号」录入邮箱+密码，再对账号发起设备授权。
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
                      {a.has_password ? (
                        <span className="text-xs text-emerald-600">✓ 已存密码</span>
                      ) : (
                        <span className="text-xs text-amber-600">未存密码 · 打卡前需设置</span>
                      )}
                    </div>
                  </TableCell>
                  <TableCell>
                    {a.authorized ? (
                      <Badge className="gap-1 bg-emerald-600 hover:bg-emerald-600"><CheckCircle2 className="h-3 w-3" /> 已授权</Badge>
                    ) : (
                      <Badge variant="outline" className="gap-1"><Unplug className="h-3 w-3" /> 未授权</Badge>
                    )}
                    {/* 网关拒绝原因：401 需重新授权 / 403 不受信任已停用（官方语义：重登也没用） */}
                    {a.device_status_reason && (
                      <div className="mt-1 max-w-[220px] text-[11px] leading-tight text-red-600">
                        {a.device_status_reason}
                      </div>
                    )}
                    {!a.enabled && a.authorized && (
                      <div className="mt-1 text-[11px] text-red-600">已停用 · 需人工到 u1s1.io 处理</div>
                    )}
                  </TableCell>
                  <TableCell>
                    {a.quota && a.quota.total > 0 ? (
                      <div className="flex flex-col gap-0.5">
                        <span className="font-medium">{fmtTokens(a.quota.total)}</span>
                        <span className="flex flex-col gap-0.5 text-[11px] text-muted-foreground">
                          {a.quota.items.map((it) => (
                            <span key={it.key}>{it.label} {fmtTokens(it.remaining)}</span>
                          ))}
                        </span>
                      </div>
                    ) : (
                      <span className="text-muted-foreground">{a.login_checkin_remaining > 0 ? fmtTokens(a.login_checkin_remaining) : "—"}</span>
                    )}
                  </TableCell>
                  <TableCell className="whitespace-nowrap text-xs">
                    {a.web_checkin_status ? (
                      <div className="flex flex-col gap-0.5">
                        <span className={a.web_checkin_status.startsWith("失败") ? "text-red-600" : "text-emerald-700"}>
                          {a.web_checkin_status}
                        </span>
                        <span className="text-muted-foreground">{fmtTime(a.last_web_checkin_at || a.last_checkin_at)}</span>
                      </div>
                    ) : (
                      <span className="text-muted-foreground">{fmtTime(a.last_checkin_at)}</span>
                    )}
                  </TableCell>
                  <TableCell className="text-right">
                    <div className="flex justify-end gap-2 flex-wrap">
                      {a.authorized ? (
                        <>
                          <Button size="sm" variant="ghost" disabled={busy === `checkin-${a.id}`}
                            onClick={() => checkinOne(a)}>
                            <RefreshCw className={`mr-1 h-3 w-3 ${busy === `checkin-${a.id}` ? "animate-spin" : ""}`} />
                            打卡
                          </Button>
                          <Button size="sm" variant="ghost" disabled={busy === `quota-${a.id}`}
                            onClick={() => refreshQuota(a)}>
                            <RefreshCw className={`mr-1 h-3 w-3 ${busy === `quota-${a.id}` ? "animate-spin" : ""}`} />
                            刷额度
                          </Button>
                        </>
                      ) : (
                        <Button size="sm" variant="outline" disabled={busy === `auth-${a.id}`}
                          onClick={() => startAuth(a)}>
                          <ExternalLink className="mr-1 h-3 w-3" />
                          授权
                        </Button>
                      )}
                      <Button size="sm" variant="outline" disabled={busy === `pwd-${a.id}`}
                        onClick={() => openPwd(a)}>
                        <KeyRound className="mr-1 h-3 w-3" />
                        设置密码
                      </Button>
                      <Button size="sm" variant="ghost" disabled={busy === `toggle-${a.id}`}
                        onClick={() => toggleAccount(a)}>
                        {a.enabled ? "停用" : "启用"}
                      </Button>
                      <Button size="sm" variant="ghost" disabled={busy === `del-${a.id}`}
                        onClick={() => removeAccount(a.id)} className="text-destructive">
                        删除
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
            <DialogTitle>添加账号</DialogTitle>
            <DialogDescription>邮箱+密码用于服务端自动登录打卡（每日领取 200 万 Token 加量包）与设备授权；密码仅存本机数据库。</DialogDescription>
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
      <Dialog open={authOpen} onOpenChange={(o) => {
        if (!o) {
          confirmAbortRef.current = true  // 关闭弹窗时立即停止轮询
          if (countdownTimer.current) clearInterval(countdownTimer.current)
          setAuthState("idle")
          setConfirmMsg("")
        }
        setAuthOpen(o)
      }}>
        <DialogContent className="max-w-lg">
          <DialogHeader>
            <DialogTitle>设备授权 {authAcc?.email_masked}</DialogTitle>
            <DialogDescription>
              ① 复制下面链接到浏览器打开，用该账号登录并批准设备；
              ② 批准后回来点「我已授权」领取设备凭证。
            </DialogDescription>
          </DialogHeader>
          <div className="flex flex-col gap-3">
            <div className="flex items-center gap-2">
              <a href={verifyUrl} target="_blank" rel="noreferrer"
                className="flex-1 break-all rounded-md border bg-muted/50 px-3 py-2 text-xs text-blue-600 hover:underline">
                {verifyUrl}
              </a>
              <Button size="sm" variant="outline" onClick={() => copyLink()} title="复制链接">
                <Copy className="h-4 w-4" />
              </Button>
            </div>
            <div className="flex items-center gap-2 text-sm">
              <Badge variant={countdown > 0 ? "outline" : "destructive"} className="gap-1">
                <Clock3 className="h-3 w-3" />
                剩余 {Math.floor(countdown / 60)}:{String(countdown % 60).padStart(2, "0")}
              </Badge>
              {countdown === 0 && <span className="text-xs text-destructive">已过期，可关闭窗口重新授权</span>}
            </div>
            {authState === "confirming" && confirmMsg && (
              <p className="text-xs text-muted-foreground">{confirmMsg}</p>
            )}
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setAuthOpen(false)}>
              {authState === "done" ? "关闭" : "取消"}
            </Button>
            {authState !== "done" && (
              <Button onClick={confirmAuth} disabled={authState === "confirming" || countdown === 0}>
                {authState === "confirming" ? "确认中…" : "我已授权"}
              </Button>
            )}
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* 设置账号密码（打卡需要） */}
      <Dialog open={pwdOpen} onOpenChange={setPwdOpen}>
        <DialogContent className="max-w-sm">
          <DialogHeader>
            <DialogTitle>设置账号密码 {pwdAcc?.email_masked}</DialogTitle>
            <DialogDescription>
              保存该账号的 U1S1 官网密码，用于自动登录打卡领取每日 200 万 Token 加量包。密码仅存本机数据库（与其它账号一致），不会外传。
            </DialogDescription>
          </DialogHeader>
          <div className="flex flex-col gap-3">
            <Input type="password" placeholder="U1S1 官网密码" value={pwdValue}
              onChange={(e) => setPwdValue(e.target.value)}
              autoFocus />
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setPwdOpen(false)}>取消</Button>
            <Button onClick={savePwd} disabled={!pwdValue.trim() || busy === `pwd-${pwdAcc?.id}`}>
              {busy === `pwd-${pwdAcc?.id}` ? "保存中…" : "保存密码"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* 一键登录（无需预填账号密码） */}
      <Dialog open={ocOpen} onOpenChange={(o) => {
        if (!o) {
          ocAbortRef.current = true
          if (countdownTimer.current) clearInterval(countdownTimer.current)
          setOcState("idle")
          setOcMsg("")
        }
        setOcOpen(o)
      }}>
        <DialogContent className="max-w-lg">
          <DialogHeader>
            <DialogTitle>一键登录 U1S1</DialogTitle>
            <DialogDescription>
              无需在后台填写账号密码：① 打开链接用 U1S1 账号登录并批准设备；② 回来后点「我已登录」，系统自动建账号并领取设备凭证与 API Key，加入 Key 池。
            </DialogDescription>
          </DialogHeader>
          <div className="flex flex-col gap-3">
            <div className="flex items-center gap-2">
              <a href={ocVerifyUrl} target="_blank" rel="noreferrer"
                className="flex-1 break-all rounded-md border bg-muted/50 px-3 py-2 text-xs text-blue-600 hover:underline">
                {ocVerifyUrl}
              </a>
              <Button size="sm" variant="outline" onClick={() => copyLink(ocVerifyUrl)} title="复制链接">
                <Copy className="h-4 w-4" />
              </Button>
            </div>
            <div className="flex items-center gap-2 text-sm">
              <Badge variant={ocCountdown > 0 ? "outline" : "destructive"} className="gap-1">
                <Clock3 className="h-3 w-3" />
                剩余 {Math.floor(ocCountdown / 60)}:{String(ocCountdown % 60).padStart(2, "0")}
              </Badge>
              {ocCountdown === 0 && <span className="text-xs text-destructive">已过期，可关闭窗口重新登录</span>}
            </div>
            {ocState === "confirming" && ocMsg && (
              <p className="text-xs text-muted-foreground">{ocMsg}</p>
            )}
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setOcOpen(false)}>
              {ocState === "done" ? "关闭" : "取消"}
            </Button>
            {ocState !== "done" && (
              <Button onClick={confirmOneClick} disabled={ocState === "confirming" || ocCountdown === 0}>
                {ocState === "confirming" ? "确认中…" : "我已登录"}
              </Button>
            )}
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}