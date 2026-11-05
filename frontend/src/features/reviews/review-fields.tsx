/**
 * 下载审图包与合并对话框共用的表单部件：按审核状态选择图片、选择数据集范围。
 */
import { useId } from "react"

import { Checkbox } from "@/components/ui/checkbox"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectSeparator,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import type {
  Review,
  ReviewCounts,
  ReviewImageStatus,
} from "@/features/reviews/api"
import { inferredImageCount } from "@/features/reviews/review-format"
import { ReviewStatusDot } from "@/features/reviews/review-status-badge"
import { reviewImageStatusLabels } from "@/features/reviews/review-status"
import { formatNumber } from "@/lib/format"
import { cn } from "@/lib/utils"

/**
 * 按审核状态选择图片（多选），每个状态显示识别完成的图片数与说明。
 * `statuses` 是可选的状态及其顺序。
 */
export function StatusCheckboxes({
  statuses,
  value,
  onChange,
  counts,
  hints,
  disabled,
  label,
}: {
  statuses: readonly ReviewImageStatus[]
  value: ReadonlySet<ReviewImageStatus>
  onChange: (value: Set<ReviewImageStatus>) => void
  counts: ReviewCounts
  hints: Partial<Record<ReviewImageStatus, string>>
  disabled: boolean
  /** 列表的可访问名称。 */
  label: string
}) {
  const baseId = useId()
  const toggle = (status: ReviewImageStatus, checked: boolean) => {
    const next = new Set(value)
    if (checked) {
      next.add(status)
    } else {
      next.delete(status)
    }
    onChange(next)
  }
  return (
    <div role="group" aria-label={label} className="grid gap-2 sm:grid-cols-3">
      {statuses.map((status) => {
        const id = `${baseId}-${status}`
        const checked = value.has(status)
        return (
          <label
            key={status}
            htmlFor={id}
            className={cn(
              "flex min-w-0 cursor-pointer items-start gap-3 rounded-lg border p-3 transition-colors hover:bg-muted/40 has-[:focus-visible]:ring-[3px] has-[:focus-visible]:ring-ring/50",
              checked && "border-primary/60 bg-muted/40",
              disabled && "pointer-events-none opacity-60"
            )}
          >
            <Checkbox
              id={id}
              checked={checked}
              onCheckedChange={(next) => toggle(status, next === true)}
              disabled={disabled}
              className="mt-0.5"
            />
            <span className="grid min-w-0 flex-1 gap-1">
              <span className="flex items-center gap-1.5 text-sm font-medium">
                <ReviewStatusDot status={status} />
                {reviewImageStatusLabels[status]}
                <span className="ml-auto font-normal text-muted-foreground tabular-nums">
                  {formatNumber(inferredImageCount(counts, [status]))}
                </span>
              </span>
              {hints[status] && (
                <span className="text-xs text-muted-foreground">
                  {hints[status]}
                </span>
              )}
            </span>
          </label>
        )
      })}
    </div>
  )
}

/** 选择数据集范围：全部数据集或其中一个（审图任务只有一个数据集时不需要）。 */
export function DatasetScopeSelect({
  id,
  review,
  value,
  onChange,
  disabled,
}: {
  id?: string
  review: Pick<Review, "datasets" | "counts">
  value: number | null
  onChange: (value: number | null) => void
  disabled: boolean
}) {
  return (
    <Select
      value={value === null ? "all" : String(value)}
      onValueChange={(next) => onChange(next === "all" ? null : Number(next))}
      disabled={disabled}
    >
      <SelectTrigger id={id} className="w-full sm:w-80">
        <SelectValue />
      </SelectTrigger>
      <SelectContent>
        <SelectItem value="all">
          全部数据集（{formatNumber(review.datasets.length)} 个）
        </SelectItem>
        <SelectSeparator />
        {review.datasets.map((dataset) => (
          <SelectItem key={dataset.id} value={String(dataset.id)}>
            <span className="max-w-64 truncate">{dataset.name}</span>
            <span className="text-muted-foreground tabular-nums">
              {formatNumber(dataset.counts.total)} 张
            </span>
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  )
}
