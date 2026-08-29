import { useCallback, useEffect, useState } from "react"
import { api } from "@/lib/api-client"
import type { SitefeedData } from "@/types"

/** 侧边栏/页面共享的官网动态摘要（版本对比、新条目红点）。 */
let cache: SitefeedData | null = null
const listeners = new Set<(d: SitefeedData | null) => void>()

export function useSitefeedSummary() {
  const [data, setData] = useState<SitefeedData | null>(cache)

  const reload = useCallback(async () => {
    const d = await api.sitefeed()
    cache = d
    listeners.forEach((l) => l(d))
    return d
  }, [])

  useEffect(() => {
    listeners.add(setData)
    if (!cache) {
      reload().catch(() => {
        /* 侧边栏摘要拉取失败静默（页面内会再拉并报错） */
      })
    }
    return () => {
      listeners.delete(setData)
    }
  }, [reload])

  return { summary: data, reload }
}
