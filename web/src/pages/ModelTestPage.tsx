import { useCallback, useEffect, useState } from "react"
import { api } from "@/lib/api-client"
import type { ModelsData, ChatTestResult } from "@/types"
import { Button } from "@/components/ui/button"
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"
import { Textarea } from "@/components/ui/textarea"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"

export default function ModelTestPage() {
  const [modelsData, setModelsData] = useState<ModelsData | null>(null)
  const [model, setModel] = useState("")
  const [prompt, setPrompt] = useState("用一句话介绍你自己")
  const [result, setResult] = useState<ChatTestResult | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [sending, setSending] = useState(false)

  const loadModels = useCallback(async () => {
    try {
      const d = await api.models()
      setModelsData(d)
      if (d.models.length > 0) {
        setModel((cur) => cur || d.models[0].id)
      }
    } catch {
      // 模型列表拉不到也允许手输
    }
  }, [])

  useEffect(() => {
    loadModels()
  }, [loadModels])

  const send = async () => {
    if (!model.trim() || !prompt.trim()) return
    setSending(true)
    setError(null)
    setResult(null)
    try {
      setResult(await api.chatTest(model.trim(), prompt))
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setSending(false)
    }
  }

  return (
    <div className="flex flex-col gap-6">
      <div>
        <h1 className="text-2xl font-semibold tracking-tight">模型测试</h1>
        <p className="text-sm text-muted-foreground">
          从 Key 池取一把 Key 走完整转发链路（非流式），验证上游连通性与指纹配置
        </p>
      </div>

      <Card>
        <CardHeader>
          <CardTitle className="text-base">发起测试</CardTitle>
          <CardDescription>
            {modelsData
              ? modelsData.cached
                ? "模型列表来自缓存（5 分钟刷新）"
                : "模型列表实时拉取自上游"
              : "模型列表加载中，也可直接输入模型 ID"}
          </CardDescription>
        </CardHeader>
        <CardContent className="flex flex-col gap-3">
          <div className="flex flex-wrap gap-2">
            {modelsData && modelsData.models.length > 0 ? (
              <Select value={model} onValueChange={(v) => setModel(v ?? "")}>
                <SelectTrigger className="w-72">
                  <SelectValue placeholder="选择模型" />
                </SelectTrigger>
                <SelectContent>
                  {modelsData.models.map((m) => (
                    <SelectItem key={m.id} value={m.id}>
                      {m.name}（{m.id}）
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            ) : (
              <input
                className="h-9 w-72 rounded-md border bg-transparent px-3 text-sm"
                value={model}
                onChange={(e) => setModel(e.target.value)}
                placeholder="deepseek-v4-flash"
              />
            )}
            <Button onClick={send} disabled={sending || !model || !prompt}>
              {sending ? "请求中…" : "发送"}
            </Button>
          </div>
          <Textarea rows={4} value={prompt} onChange={(e) => setPrompt(e.target.value)} />
        </CardContent>
      </Card>

      {(result || error) && (
        <Card>
          <CardHeader>
            <CardTitle className="text-base">结果</CardTitle>
          </CardHeader>
          <CardContent>
            {error ? (
              <p className="whitespace-pre-wrap break-all rounded-md bg-destructive/10 p-4 text-sm text-destructive">
                {error}
              </p>
            ) : result ? (
              <div className="flex flex-col gap-3">
                <div className="whitespace-pre-wrap rounded-md bg-muted p-4 text-sm">
                  {result.content || "(空响应)"}
                </div>
                <div className="text-xs text-muted-foreground">
                  模型 {result.model} · 输入 {result.input_tokens} tok · 输出{" "}
                  {result.output_tokens} tok · 耗时 {result.duration_ms} ms
                </div>
              </div>
            ) : null}
          </CardContent>
        </Card>
      )}
    </div>
  )
}
