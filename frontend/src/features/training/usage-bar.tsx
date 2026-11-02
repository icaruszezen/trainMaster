import type { ReactNode } from "react"

import { Progress } from "@/components/ui/progress"
import { formatPercent } from "@/lib/format"
import { cn } from "@/lib/utils"

/**
 * 带标题与数值的用量条：CPU / 内存 / 磁盘 / 显存 / 利用率。
 * `percent` 为 null 表示未知：进度条留空，读屏软件读到“未知”。
 * `warning` 时进度条换成醒目的颜色，`detail` 中应同时有文字说明（不只靠颜色）。
 */
export function UsageBar({
  label,
  ariaLabel,
  value,
  percent,
  detail,
  warning = false,
  className,
  children,
}: {
  label: string
  /** 读屏用的名称，如 “GPU 0 显存”；默认与 label 相同。 */
  ariaLabel?: string
  /** 右上角的数值文字，如 “87%” 或 “19 GB / 24 GB”。 */
  value: ReactNode
  percent: number | null
  detail?: ReactNode
  warning?: boolean
  className?: string
  /** 进度条下方、说明文字上方的附加内容（如迷你折线图）。 */
  children?: ReactNode
}) {
  const known = percent !== null && Number.isFinite(percent)
  const clamped = known ? Math.min(100, Math.max(0, percent)) : null

  return (
    <div className={cn("grid min-w-0 content-start gap-1.5", className)}>
      <div className="flex items-baseline justify-between gap-3 text-xs">
        <span className="shrink-0 text-muted-foreground">{label}</span>
        <span className="min-w-0 truncate font-medium tabular-nums">
          {value}
        </span>
      </div>
      <Progress
        value={clamped}
        aria-label={ariaLabel ?? label}
        aria-valuetext={clamped === null ? "未知" : formatPercent(clamped)}
        className={cn(
          "h-1.5",
          warning &&
            "bg-amber-500/20 *:data-[slot=progress-indicator]:bg-amber-500"
        )}
      />
      {children}
      {detail && (
        <div className="min-w-0 text-xs text-muted-foreground">{detail}</div>
      )}
    </div>
  )
}
