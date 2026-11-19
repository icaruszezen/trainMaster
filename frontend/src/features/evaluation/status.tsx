/**
 * 模型评估状态徽章与图片级 OK/NG 判定结果徽章。
 */
import {
  Ban,
  CheckCircle2,
  Clock,
  Loader2,
  XCircle,
} from "lucide-react"

import { Badge } from "@/components/ui/badge"
import type {
  Evaluation,
  ImageOutcome,
} from "@/features/evaluation/api"
import {
  EVALUATION_STAGE_LABELS,
  EVALUATION_STATUS_LABELS,
  IMAGE_OUTCOME_LABELS,
} from "@/features/evaluation/format"
import { cn } from "@/lib/utils"

export function EvaluationStatusBadge({
  evaluation,
  showStage = false,
  className,
}: {
  evaluation: Pick<Evaluation, "status" | "stage">
  showStage?: boolean
  className?: string
}) {
  const { status, stage } = evaluation
  const label =
    status === "running" && showStage && stage
      ? EVALUATION_STAGE_LABELS[stage] || EVALUATION_STATUS_LABELS[status]
      : EVALUATION_STATUS_LABELS[status] || status

  switch (status) {
    case "succeeded":
      return (
        <Badge
          variant="outline"
          className={cn(
            "gap-1 border-emerald-500/40 bg-emerald-500/10 text-emerald-700 dark:text-emerald-400",
            className
          )}
        >
          <CheckCircle2 className="size-3" />
          {label}
        </Badge>
      )
    case "running":
      return (
        <Badge
          variant="outline"
          className={cn(
            "gap-1 border-blue-500/40 bg-blue-500/10 text-blue-700 dark:text-blue-400",
            className
          )}
        >
          <Loader2 className="size-3 animate-spin" />
          {label}
        </Badge>
      )
    case "queued":
      return (
        <Badge
          variant="outline"
          className={cn(
            "gap-1 border-amber-500/40 bg-amber-500/10 text-amber-700 dark:text-amber-400",
            className
          )}
        >
          <Clock className="size-3" />
          {label}
        </Badge>
      )
    case "failed":
      return (
        <Badge
          variant="outline"
          className={cn(
            "gap-1 border-destructive/40 bg-destructive/10 text-destructive",
            className
          )}
        >
          <XCircle className="size-3" />
          {label}
        </Badge>
      )
    case "canceled":
      return (
        <Badge
          variant="secondary"
          className={cn("gap-1 text-muted-foreground", className)}
        >
          <Ban className="size-3" />
          {label}
        </Badge>
      )
  }
}

export function OutcomeBadge({
  outcome,
  className,
}: {
  outcome: ImageOutcome
  className?: string
}) {
  const label = IMAGE_OUTCOME_LABELS[outcome] || outcome
  switch (outcome) {
    case "ok_ok":
      return (
        <Badge
          variant="outline"
          className={cn(
            "border-emerald-500/40 bg-emerald-500/10 text-emerald-700 dark:text-emerald-400",
            className
          )}
        >
          {label}
        </Badge>
      )
    case "ng_ng":
      return (
        <Badge
          variant="outline"
          className={cn(
            "border-blue-500/40 bg-blue-500/10 text-blue-700 dark:text-blue-400",
            className
          )}
        >
          {label}
        </Badge>
      )
    case "overkill":
      return (
        <Badge
          variant="outline"
          className={cn(
            "border-amber-500/50 bg-amber-500/15 text-amber-800 dark:text-amber-400",
            className
          )}
        >
          {label}
        </Badge>
      )
    case "escape":
      return (
        <Badge
          variant="outline"
          className={cn(
            "border-destructive/50 bg-destructive/15 text-destructive",
            className
          )}
        >
          {label}
        </Badge>
      )
    case "failed":
      return (
        <Badge variant="destructive" className={className}>
          {label}
        </Badge>
      )
  }
}
