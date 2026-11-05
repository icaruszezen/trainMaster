/**
 * 审图任务页面共用的小部件：识别状态的徽标、审核进度条（各审核状态的占比）、
 * 上传与合并没有处理的图片列表。
 */
import { useState } from "react"
import { CircleCheck, Clock, TriangleAlert } from "lucide-react"

import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Spinner } from "@/components/ui/spinner"
import type {
  Review,
  ReviewCounts,
  ReviewProblem,
} from "@/features/reviews/api"
import { ReviewStatusDot } from "@/features/reviews/review-status-badge"
import {
  REVIEW_IMAGE_STATUSES,
  reviewImageStatusLabels,
  reviewImageStatusStyles,
  reviewStatusLabels,
} from "@/features/reviews/review-status"
import { formatNumber, formatPercent, percentOf } from "@/lib/format"
import { cn } from "@/lib/utils"

type TaskStatusReview = Pick<
  Review,
  "status" | "queue_position" | "counts" | "error" | "workers_online"
>

/**
 * 审图任务的识别状态：排队中（第几个）、识别中（已识别 / 总数）、识别完成、
 * 识别失败（悬停显示原因）。`detail` 时排队与识别中带上位置与进度。
 */
export function ReviewTaskStatusBadge({
  review,
  detail = true,
  className,
}: {
  review: TaskStatusReview
  detail?: boolean
  className?: string
}) {
  const label = reviewStatusLabels[review.status]
  switch (review.status) {
    case "ready":
      return (
        <Badge variant="outline" className={cn("font-normal", className)}>
          <CircleCheck className="text-emerald-600 dark:text-emerald-500" />
          {label}
        </Badge>
      )
    case "failed":
      return (
        <Badge
          variant="destructive"
          className={cn("max-w-56 font-normal", className)}
          title={review.error || undefined}
        >
          <TriangleAlert />
          {label}
        </Badge>
      )
    case "running":
      return (
        <Badge
          variant="secondary"
          className={cn("font-normal tabular-nums", className)}
        >
          <Spinner />
          {detail
            ? `${label} ${formatNumber(review.counts.inferred)} / ${formatNumber(review.counts.total)}`
            : label}
        </Badge>
      )
    default:
      return (
        <Badge
          variant="secondary"
          className={cn("font-normal tabular-nums", className)}
        >
          <Clock />
          {detail && review.queue_position !== null
            ? `${label}（第 ${formatNumber(review.queue_position)} 个）`
            : label}
        </Badge>
      )
  }
}

/** 排队且没有可以识别的训练节点（不会开始识别）。 */
export function waitingForWorkers(review: TaskStatusReview) {
  return review.status === "queued" && review.workers_online === 0
}

/** 审核进度的文字说明，如“待审核 6，已审核 3，需加样 1”。 */
export function statusBarLabel(counts: ReviewCounts) {
  return REVIEW_IMAGE_STATUSES.map(
    (status) =>
      `${reviewImageStatusLabels[status]} ${formatNumber(counts[status])}`
  ).join("，")
}

/**
 * 审核进度条：已审核（绿）、需加样（琥珀）、待审核（灰）按图片数的占比，
 * 已审核与需加样从左侧开始累积。
 */
export function ReviewStatusBar({
  counts,
  className,
}: {
  counts: ReviewCounts
  className?: string
}) {
  const segments = (["approved", "needs_sample"] as const).map((status) => ({
    status,
    percent: percentOf(counts[status], counts.total),
  }))
  return (
    <div
      role="img"
      aria-label={statusBarLabel(counts)}
      title={statusBarLabel(counts)}
      className={cn(
        "flex h-1.5 w-full overflow-hidden rounded-full",
        reviewImageStatusStyles.pending.dot,
        className
      )}
    >
      {segments.map(
        ({ status, percent }) =>
          percent > 0 && (
            <div
              key={status}
              className={cn("h-full", reviewImageStatusStyles[status].dot)}
              style={{ width: `${percent}%` }}
            />
          )
      )}
    </div>
  )
}

/** 进度条下的说明：已审核的占比与各状态的图片数。 */
export function ReviewStatusLegend({
  counts,
  className,
}: {
  counts: ReviewCounts
  className?: string
}) {
  const reviewed = counts.approved + counts.needs_sample
  return (
    <p
      className={cn(
        "flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-muted-foreground tabular-nums",
        className
      )}
    >
      <span className="font-medium text-foreground">
        已审核 {formatPercent(percentOf(reviewed, counts.total))}
      </span>
      {REVIEW_IMAGE_STATUSES.map((status) => (
        <span key={status} className="inline-flex items-center gap-1">
          <ReviewStatusDot status={status} />
          {reviewImageStatusLabels[status]} {formatNumber(counts[status])}
        </span>
      ))}
    </p>
  )
}

const problemKindLabels: Record<string, string> = {
  conflict: "冲突",
  invalid: "无效",
  unknown: "不在任务中",
  failed: "失败",
  skipped: "状态已改变",
}

/** 默认显示的问题条数，更多时可以展开。 */
const COLLAPSED_PROBLEMS = 5

/**
 * 上传或合并时没有处理的图片（服务端最多记录 100 条）。`total` 为实际的
 * 问题数，大于记录的条数时说明只列出了一部分。
 */
export function ProblemList<K extends string>({
  problems,
  total,
  className,
}: {
  problems: ReviewProblem<K>[]
  total: number
  className?: string
}) {
  const [expanded, setExpanded] = useState(false)
  if (problems.length === 0) {
    return null
  }
  const shown = expanded ? problems : problems.slice(0, COLLAPSED_PROBLEMS)
  return (
    <div className={cn("grid min-w-0 gap-1.5", className)}>
      <ul className="grid max-h-64 min-w-0 gap-px overflow-y-auto rounded-md border bg-border text-xs">
        {shown.map((problem, index) => (
          <li
            key={`${problem.path}-${index}`}
            className="grid min-w-0 gap-0.5 bg-card px-3 py-2"
          >
            <span className="flex min-w-0 items-center gap-2">
              <Badge
                variant={problem.kind === "conflict" ? "outline" : "secondary"}
                className={cn(
                  "shrink-0 px-1.5 py-0 font-normal",
                  problem.kind === "conflict" &&
                    "border-amber-600/40 text-amber-800 dark:border-amber-400/40 dark:text-amber-400"
                )}
              >
                {problemKindLabels[problem.kind] ?? problem.kind}
              </Badge>
              <span
                className="min-w-0 truncate font-mono"
                title={problem.path || undefined}
              >
                {problem.path || "—"}
              </span>
            </span>
            <span className="break-words text-muted-foreground">
              {problem.message}
            </span>
          </li>
        ))}
      </ul>
      <div className="flex flex-wrap items-center justify-between gap-2 text-xs text-muted-foreground">
        {total > problems.length ? (
          <span className="tabular-nums">
            共 {formatNumber(total)} 张，记录了前{" "}
            {formatNumber(problems.length)} 张。
          </span>
        ) : (
          <span />
        )}
        {problems.length > COLLAPSED_PROBLEMS && (
          <Button
            type="button"
            variant="ghost"
            size="xs"
            onClick={() => setExpanded((value) => !value)}
            aria-expanded={expanded}
          >
            {expanded ? "收起" : `显示全部 ${formatNumber(problems.length)} 条`}
          </Button>
        )}
      </div>
    </div>
  )
}
