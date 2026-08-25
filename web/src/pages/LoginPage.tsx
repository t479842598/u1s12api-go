import { useState, type FormEvent } from "react"
import { Navigate, useNavigate } from "react-router-dom"
import { useAuth } from "@/hooks/use-auth"
import { LogoMark } from "@/components/shared/LogoMark"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"

export default function LoginPage() {
  const { isAuthenticated, isLoading, login } = useAuth()
  const navigate = useNavigate()
  const [password, setPassword] = useState("")
  const [error, setError] = useState<string | null>(null)
  const [submitting, setSubmitting] = useState(false)

  if (!isLoading && isAuthenticated) {
    return <Navigate to="/admin/dashboard" replace />
  }

  const handleSubmit = async (e: FormEvent) => {
    e.preventDefault()
    if (!password.trim() || submitting) return
    setSubmitting(true)
    setError(null)
    const msg = await login(password.trim())
    setSubmitting(false)
    if (msg) {
      setError(msg)
    } else {
      navigate("/admin/dashboard", { replace: true })
    }
  }

  return (
    <div className="flex min-h-screen items-center justify-center bg-background p-6">
      <Card className="w-full max-w-sm">
        <CardHeader className="items-center text-center">
          <LogoMark className="mx-auto mb-2 h-14 w-14" />
          <CardTitle className="text-xl">U1S12API 管理后台</CardTitle>
          <CardDescription>
            输入管理口令登录（首次启动打印在服务日志 / 写入 .env）
          </CardDescription>
        </CardHeader>
        <CardContent>
          <form onSubmit={handleSubmit} className="flex flex-col gap-3">
            <Input
              type="password"
              placeholder="管理口令"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              autoFocus
            />
            {error && <p className="text-sm text-destructive">{error}</p>}
            <Button type="submit" disabled={submitting || !password.trim()}>
              {submitting ? "登录中…" : "登录"}
            </Button>
          </form>
        </CardContent>
      </Card>
    </div>
  )
}
