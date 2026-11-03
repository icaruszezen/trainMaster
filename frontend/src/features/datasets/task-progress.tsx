import type { ReactNode } from "react"

import { Progress } from "@/components/ui/progress"
import { etaSeconds } from "@/features/datasets/upload-plan"
import { formatBytes, formatDuration, percentOf } from "@/lib/format"
import { cn } from "@/lib/utils"

/**
 * 任务进度条：`value` 为 0–100；为 null 时（总量未知，如排队等待处理）
 * 显示往复滑动的不确定进度。
 */
export function TaskProgress({
  value,
  className,
  label,
}: {
  value: number | null
  className?: string
  /** 读屏文案，如“上传进度”。 */
  label: string
}) {
  if (value === null) {
    return (
      <div
        role="progressbar"
        aria-label={label}
        aria-busy="true"
        className={cn(
          "relative h-1.5 w-full overflow-hidden rounded-full bg-primary/20",
          className
        )}
      >
        <div className="absolute inset-y-0 left-0 w-2/5 animate-progress-indeterminate rounded-full bg-primary motion-reduce:animate-none" />
      </div>
    )
  }
  return (
    <Progress
      value={value}
      aria-label={`${label} ${Math.round(value)}%`}
      className={cn("h-1.5", className)}
    />
  )
}

/**
 * 上传中的进度：进度条，下面是“已上传 / 总大小 · 速度 · 剩余时间”。
 * `children` 插在大小之后（如文件夹上传的文件数，以“· ”开头）。
 */
export function TransferProgress({
  uploadedBytes,
  totalBytes,
  speed,
  children,
}: {
  uploadedBytes: number
  totalBytes: number
  /** 字节/秒，0 表示未知。 */
  speed: number
  children?: ReactNode
}) {
  const remaining = etaSeconds(totalBytes - uploadedBytes, speed)
  return (
    <>
      <TaskProgress
        value={percentOf(uploadedBytes, totalBytes)}
        label="上传进度"
      />
      <p className="flex flex-wrap gap-x-1 text-xs text-muted-foreground tabular-nums">
        <span>
          {formatBytes(uploadedBytes)} / {formatBytes(totalBytes)}
        </span>
        {children}
        {speed > 0 && <span>· {formatBytes(speed)}/s</span>}
        {remaining !== null && (
          <span>· 剩余 {formatDuration(Math.ceil(remaining))}</span>
        )}
      </p>
    </>
  )
}
