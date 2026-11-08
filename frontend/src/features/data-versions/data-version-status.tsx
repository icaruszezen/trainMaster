/** 数据版本的状态标记与进度（列表、详情与项目页共用）。 */
import { CircleAlert, CircleCheck, Clock } from "lucide-react"

import { Badge } from "@/components/ui/badge"
import { Spinner } from "@/components/ui/spinner"
import { TaskProgress } from "@/features/datasets/task-progress"
import type { DataVersion } from "@/features/data-versions/api"
import {
  DATA_VERSION_STATUS_LABELS,
  progressPercent,
  progressText,
  queueText,
} from "@/features/data-versions/format"
import { formatPercent } from "@/lib/format"
import { cn } from "@/lib/utils"

type StatusFields = Pick<DataVersion, "status" | "progress" | "queue_position">

export function DataVersionStatusBadge({
  version,
  className,
}: {
  version: StatusFields
  className?: string
}) {
  const label = DATA_VERSION_STATUS_LABELS[version.status]
  switch (version.status) {
    case "queued":
      return (
        <Badge variant="outline" className={cn("font-normal", className)}>
          <Clock />
          {label}
        </Badge>
      )
    case "building": {
      const percent = progressPercent(version.progress)
      return (
        <Badge
          variant="secondary"
          className={cn("font-normal tabular-nums", className)}
        >
          <Spinner className="size-3" aria-hidden />
          {label}
          {percent !== null && ` ${formatPercent(percent)}`}
        </Badge>
      )
    }
    case "ready":
      return (
        <Badge
          variant="outline"
          className={cn(
            "border-emerald-500/40 font-normal text-emerald-700 dark:text-emerald-500",
            className
          )}
        >
          <CircleCheck />
          {label}
        </Badge>
      )
    case "failed":
      return (
        <Badge variant="destructive" className={cn("font-normal", className)}>
          <CircleAlert />
          {label}
        </Badge>
      )
  }
}

/** 排队的位置或生成进度（进度条与阶段）；其它状态不显示。 */
export function DataVersionActivity({
  version,
  className,
}: {
  version: StatusFields
  className?: string
}) {
  if (version.status === "queued") {
    return (
      <p
        className={cn("text-xs text-muted-foreground tabular-nums", className)}
      >
        {queueText(version.queue_position)}
      </p>
    )
  }
  if (version.status !== "building") {
    return null
  }
  return (
    <div className={cn("grid grid-cols-[minmax(0,1fr)] gap-1", className)}>
      <TaskProgress
        value={progressPercent(version.progress)}
        label="生成进度"
      />
      <p className="truncate text-xs text-muted-foreground tabular-nums">
        {version.progress ? progressText(version.progress) : "正在开始…"}
      </p>
    </div>
  )
}
