import { Progress } from "@/components/ui/progress"
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
