/** 训练任务状态徽章与实时训练进度条（列表页、项目页、详情页共用）。 */
import { Ban, CircleAlert, CircleCheck, Clock } from "lucide-react"

import { Badge } from "@/components/ui/badge"
import { Spinner } from "@/components/ui/spinner"
import { TaskProgress } from "@/features/datasets/task-progress"
import type { TrainingJob } from "@/features/training/api"
import {
  TRAINING_STATUS_LABELS,
  trainingProgressPercent,
  trainingProgressText,
  trainingQueueText,
} from "@/features/training/format"
import { formatPercent } from "@/lib/format"
import { cn } from "@/lib/utils"

type StatusFields = Pick<
  TrainingJob,
  | "status"
  | "stage"
  | "queue_position"
  | "workers_online"
  | "current_epoch"
  | "total_epochs"
  | "batch_done"
  | "batch_total"
  | "eta_seconds"
>

export function TrainingStatusBadge({
  job,
  className,
}: {
  job: StatusFields
  className?: string
}) {
  const label = TRAINING_STATUS_LABELS[job.status]
  switch (job.status) {
    case "queued":
      return (
        <Badge variant="outline" className={cn("font-normal", className)}>
          <Clock />
          {label}
        </Badge>
      )
    case "running": {
      const percent = trainingProgressPercent(job)
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
    case "succeeded":
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
    case "canceled":
      return (
        <Badge
          variant="outline"
          className={cn("font-normal text-muted-foreground", className)}
        >
          <Ban />
          {label}
        </Badge>
      )
  }
}

export function TrainingActivity({
  job,
  className,
}: {
  job: StatusFields
  className?: string
}) {
  if (job.status === "queued") {
    return (
      <p
        className={cn("text-xs text-muted-foreground tabular-nums", className)}
      >
        {trainingQueueText(job.queue_position, job.workers_online)}
      </p>
    )
  }
  if (job.status !== "running") {
    return null
  }
  return (
    <div className={cn("grid grid-cols-[minmax(0,1fr)] gap-1", className)}>
      <TaskProgress value={trainingProgressPercent(job)} label="训练进度" />
      <p className="truncate text-xs text-muted-foreground tabular-nums">
        {trainingProgressText(job)}
      </p>
    </div>
  )
}
