import { useState } from "react"
import { CloudOff, RefreshCw } from "lucide-react"

import { Button } from "@/components/ui/button"
import { formatClockTime } from "@/features/training/compute-format"
import { cn } from "@/lib/utils"

/**
 * 定时刷新的数据的状态：后台刷新失败时保留上次的数据，并在这里低调地提示
 * （悬停查看原因）；右侧是手动刷新按钮，只在手动刷新时转动，
 * 避免每次定时刷新都闪一下。
 */
export function RefreshStatus({
  error,
  hasData,
  updatedAt,
  onRefresh,
  label,
  className,
}: {
  error: Error | null
  hasData: boolean
  /** 上次成功获取数据的时间（毫秒）。 */
  updatedAt: number
  onRefresh: () => Promise<unknown>
  /** 刷新按钮的读屏文字，如“刷新节点列表”。 */
  label: string
  className?: string
}) {
  const [refreshing, setRefreshing] = useState(false)
  const stale = error !== null && hasData

  const refresh = () => {
    setRefreshing(true)
    void onRefresh().finally(() => setRefreshing(false))
  }

  return (
    <div className={cn("flex min-w-0 items-center gap-1", className)}>
      {stale && (
        <span
          role="status"
          className="inline-flex min-w-0 items-center gap-1.5 text-xs text-amber-700 dark:text-amber-500"
          title={error.message}
        >
          <CloudOff className="size-3.5 shrink-0" aria-hidden />
          <span className="truncate">
            刷新失败
            {updatedAt > 0 && ` · 数据更新于 ${formatClockTime(updatedAt)}`}
          </span>
          <span className="sr-only">：{error.message}</span>
        </span>
      )}
      <Button
        variant="ghost"
        size="icon-sm"
        onClick={refresh}
        disabled={refreshing}
        aria-label={label}
        title={label}
      >
        <RefreshCw className={cn(refreshing && "animate-spin")} />
      </Button>
    </div>
  )
}
