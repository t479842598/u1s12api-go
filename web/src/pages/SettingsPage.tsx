import { useCallback, useEffect, useState } from "react"
import { toast } from "sonner"
import { api } from "@/lib/api-client"
import type { SettingsData } from "@/types"
import { Button } from "@/components/ui/button"
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

export default function SettingsPage() {
  const [data, setData] = useState<SettingsData | null>(null)
  const [upstream, setUpstream] = useState("")
  const [proxy, setProxy] = useState("")
  const [version, setVersion] = useState("")
  const [profile, setProfile] = useState("auto")
  const [barkKey, setBarkKey] = useState("")
  const [checkHours, setCheckHours] = useState("24")
  const [newPassword, setNewPassword] = useState("")
  const [saving, setSaving] = useState(false)
  const [testingProxy, setTestingProxy] = useState(false)

  const load = useCallback(async () => {
    const s = await api.settings()
    setData(s)
    setUpstream(s.upstream_base_url)
    setProxy(s.egress_proxy)
    setVersion(s.u1s1_version)
    setProfile(s.fingerprint_profile === "auto" ? "auto" : s.current_profile)
    setBarkKey(s.bark_key ?? "")
    setCheckHours(String(s.site_feed_check_hours ?? 24))
  }, [])

  useEffect(() => {
    load()
  }, [load])

  if (!data) return null

  const save = async (patch: Parameters<typeof api.saveSettings>[0], msg: string) => {
    setSaving(true)
    try {
      await api.saveSettings(patch)
      toast.success(msg)
      await load()
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "保存失败")
    } finally {
      setSaving(false)
    }
  }

  return (
    <div className="flex flex-col gap-6">
      <div>
        <h1 className="text-2xl font-semibold tracking-tight">设置</h1>
        <p className="text-sm text-muted-foreground">
          修改后写回项目根目录 .env 并即时生效（无需重启）
        </p>
      </div>

      {/* 上游 */}
      <Card>
        <CardHeader>
          <CardTitle className="text-base">上游网关</CardTitle>
          <CardDescription>U1S1 官方 API 地址与出口代理</CardDescription>
        </CardHeader>
        <CardContent className="flex flex-col gap-4">
          <div className="flex flex-wrap items-center gap-2">
            <span className="w-28 text-sm text-muted-foreground">上游地址</span>
            <Input
              className="w-96"
              value={upstream}
              onChange={(e) => setUpstream(e.target.value)}
              placeholder="https://api.u1s1.io/v1"
            />
            <Button
              variant="secondary"
              disabled={saving}
              onClick={() =>
                save({ upstream_base_url: upstream }, "上游地址已更新")
              }
            >
              保存
            </Button>
          </div>

          <div className="flex flex-wrap items-center gap-2">
            <span className="w-28 text-sm text-muted-foreground">出口代理</span>
            <Input
              className="w-96"
              value={proxy}
              onChange={(e) => setProxy(e.target.value)}
              placeholder="留空直连；支持 http(s):// 与 socks5://，如 socks5://127.0.0.1:7897"
            />
            <Button
              variant="secondary"
              disabled={testingProxy}
              onClick={async () => {
                setTestingProxy(true)
                try {
                  const r = await api.proxyTest(proxy.trim())
                  if (r.ok) {
                    toast.success(`连通正常（HTTP ${r.status}）`)
                  } else {
                    toast.error(`不通：${r.error}`)
                  }
                } catch {
                  toast.error("测试请求失败")
                } finally {
                  setTestingProxy(false)
                }
              }}
            >
              {testingProxy ? "测试中…" : "测试"}
            </Button>
            <Button
              disabled={saving}
              onClick={() => save({ egress_proxy: proxy.trim() }, "出口代理已更新")}
            >
              保存
            </Button>
          </div>

          <div className="flex flex-wrap items-center gap-2">
            <span className="w-28 text-sm text-muted-foreground">客户端版本</span>
            <Input
              className="w-40"
              value={version}
              onChange={(e) => setVersion(e.target.value)}
              placeholder="1.2.5"
            />
            <Button
              variant="secondary"
              disabled={saving}
              onClick={() => save({ u1s1_version: version.trim() }, "版本号已更新")}
            >
              保存
            </Button>
            <span className="text-xs text-muted-foreground">
              即 x-u1s1-version 头的值，跟随官方 CLI 新版本时在此更新
            </span>
          </div>
        </CardContent>
      </Card>

      {/* 指纹档案 */}
      <Card>
        <CardHeader>
          <CardTitle className="text-base">请求头指纹</CardTitle>
          <CardDescription>
            切换后新请求立即生效；档案持久化，重启不变。当前生效：
            <code className="ml-1 rounded bg-muted px-1.5 py-0.5 text-xs">
              {data.current_profile}
            </code>
          </CardDescription>
        </CardHeader>
        <CardContent className="flex flex-col gap-3">
          <div className="flex flex-wrap items-center gap-2">
            <Select value={profile} onValueChange={(v) => setProfile(v ?? "auto")}>
              <SelectTrigger className="w-72">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="auto">auto（保持当前档案）</SelectItem>
                {data.profiles.map((p) => (
                  <SelectItem key={p.id} value={p.id}>
                    {p.label} — {p.user_agent}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            <Button
              disabled={saving || profile === data.current_profile}
              onClick={() =>
                save(
                  { fingerprint_profile: profile },
                  profile === "auto" ? "保持自动" : `已切换到 ${profile}`,
                )
              }
            >
              应用
            </Button>
          </div>
          <div className="rounded-md bg-muted p-3 font-mono text-xs leading-relaxed text-muted-foreground">
            {data.profiles
              .filter((p) => p.id === data.current_profile)
              .map((p) => (
                <pre key={p.id} className="whitespace-pre-wrap">{`User-Agent: ${p.user_agent}
X-Stainless-Runtime-Version: ${p.runtime}`}</pre>
              ))}
          </div>
        </CardContent>
      </Card>

      {/* 官网动态推送 */}
      <Card>
        <CardHeader>
          <CardTitle className="text-base">官网动态推送</CardTitle>
          <CardDescription>
            定时抓取 u1s1.io 公告与更新记录，新内容经 Bark 推送到手机；CLI 新版本提醒同步 U1S1_VERSION
          </CardDescription>
        </CardHeader>
        <CardContent className="flex flex-col gap-4">
          <div className="flex flex-wrap items-center gap-2">
            <span className="w-28 text-sm text-muted-foreground">Bark Key</span>
            <Input
              className="w-96"
              value={barkKey}
              onChange={(e) => setBarkKey(e.target.value)}
              placeholder="https://api.day.app/&lt;key&gt; 中的 key；留空则只入库不推送"
            />
            <Button
              variant="secondary"
              disabled={saving}
              onClick={() => save({ bark_key: barkKey.trim() }, "Bark Key 已更新")}
            >
              保存
            </Button>
          </div>
          <div className="flex flex-wrap items-center gap-2">
            <span className="w-28 text-sm text-muted-foreground">检查间隔</span>
            <Input
              className="w-24"
              value={checkHours}
              onChange={(e) => setCheckHours(e.target.value)}
              placeholder="24"
            />
            <span className="text-xs text-muted-foreground">小时</span>
            <Button
              variant="secondary"
              disabled={saving}
              onClick={() => {
                const h = Number.parseInt(checkHours, 10)
                if (Number.isNaN(h) || h <= 0) {
                  toast.error("检查间隔必须是正整数（小时）")
                  return
                }
                save({ site_feed_check_hours: h }, "检查间隔已更新（下轮生效）")
              }}
            >
              保存
            </Button>
            <span className="text-xs text-muted-foreground">
              0 或留空恢复默认 24；间隔调整在下一轮检查时生效
            </span>
          </div>
        </CardContent>
      </Card>

      {/* 安全 */}
      <Card>
        <CardHeader>
          <CardTitle className="text-base">安全</CardTitle>
          <CardDescription>
            管理口令{data.has_password ? "已设置" : "未设置"}；服务监听{" "}
            {data.host}:{data.port}
          </CardDescription>
        </CardHeader>
        <CardContent className="flex flex-wrap items-center gap-2">
          <Input
            type="password"
            placeholder="新的管理口令"
            className="w-64"
            value={newPassword}
            onChange={(e) => setNewPassword(e.target.value)}
          />
          <Button
            disabled={!newPassword.trim() || saving}
            onClick={async () => {
              await save({ admin_password: newPassword.trim() }, "口令已更新，下次登录生效")
              setNewPassword("")
            }}
          >
            修改口令
          </Button>
        </CardContent>
      </Card>
    </div>
  )
}
