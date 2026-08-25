import { LogoMark } from "@/components/shared/LogoMark"
import { LoadingSpinner } from "@/components/shared/LoadingSpinner"

export function PageLoading() {
  return (
    <div className="flex h-screen flex-col items-center justify-center gap-6 bg-background">
      <div className="flex flex-col items-center gap-3">
        <LogoMark className="h-12 w-12 animate-pulse" />
        <h1 className="text-lg font-semibold tracking-tight text-foreground">
          U1S12API
        </h1>
      </div>
      <div className="flex items-center gap-2.5 text-sm text-muted-foreground">
        <LoadingSpinner size={18} />
        正在加载管理后台…
      </div>
    </div>
  )
}
