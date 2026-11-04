import { RotateCcw } from "lucide-react"

import { Button } from "@/components/ui/button"
import { Kbd } from "@/components/ui/kbd"
import { Label } from "@/components/ui/label"
import { Spinner } from "@/components/ui/spinner"
import { Switch } from "@/components/ui/switch"
import type { Size } from "@/features/annotation/geometry"
import {
  InfoRow,
  NoticeBox,
  PanelSection,
  type Notice,
} from "@/features/annotation/side-panel"
import type {
  ReviewImageDetail,
  ReviewImageStatus,
  ReviewPrediction,
} from "@/features/reviews/api"
import {
  ReviewImageStatusBadge,
  reviewImageStatusIcons,
} from "@/features/reviews/review-status-badge"
import {
  formatConfidence,
  reviewImageStatusHints,
  reviewImageStatusKeys,
  reviewImageStatusLabels,
  reviewImageStatusStyles,
} from "@/features/reviews/review-status"
import { formatDateTime, formatNumber } from "@/lib/format"
import { cn } from "@/lib/utils"

/** 状态按钮的顺序：常用的已审核、需加样在前，待审核（撤销标记）在后。 */
const MARK_ORDER: readonly ReviewImageStatus[] = [
  "approved",
  "needs_sample",
  "pending",
]

type MarkButtonsProps = {
  status: ReviewImageStatus
  /** 正在标记的状态（按钮显示进度）。 */
  marking: ReviewImageStatus | null
  /** 不能标记（还没有识别等），说明原因；null 为可以标记。 */
  disabledReason: string | null
  onMark: (status: ReviewImageStatus) => void
  /** 紧凑的一行（窄屏时画布下方的状态栏）。 */
  compact?: boolean
}

/** 已审核（S）/ 需加样（X）/ 待审核（U）三个按钮，当前状态的按钮为实心。 */
export function ReviewMarkButtons({
  status,
  marking,
  disabledReason,
  onMark,
  compact = false,
}: MarkButtonsProps) {
  return (
    <div
      className={cn("grid gap-1.5", compact ? "grid-cols-3" : "grid-cols-2")}
      role="group"
      aria-label="标记审核状态"
    >
      {MARK_ORDER.map((value) => {
        const Icon = reviewImageStatusIcons[value]
        const active = status === value
        const styles = reviewImageStatusStyles[value]
        return (
          <Button
            key={value}
            variant="outline"
            size={compact ? "sm" : "default"}
            aria-pressed={active}
            disabled={disabledReason !== null || marking !== null}
            title={disabledReason ?? reviewImageStatusHints[value]}
            onClick={() => onMark(value)}
            className={cn(
              "justify-center gap-1.5 font-medium",
              !compact && value === "pending" && "col-span-2",
              !compact && value !== "pending" && "h-11",
              active ? styles.active : styles.idle
            )}
          >
            {marking === value ? <Spinner /> : <Icon aria-hidden />}
            {reviewImageStatusLabels[value]}
            <Kbd
              className={cn(
                "ml-0.5",
                active && "bg-black/10 text-current dark:bg-white/15"
              )}
            >
              {reviewImageStatusKeys[value]}
            </Kbd>
          </Button>
        )
      })}
    </div>
  )
}

type ReviewStatusCardProps = Omit<MarkButtonsProps, "status" | "compact"> & {
  detail: Pick<ReviewImageDetail, "status" | "reviewed_at" | "edited">
  autoAdvance: boolean
  onAutoAdvanceChange: (value: boolean) => void
}

/**
 * 当前图片的审核状态：状态徽标与标记时间，标记按钮，“标记后自动跳到下一张”。
 */
export function ReviewStatusCard({
  detail,
  marking,
  disabledReason,
  onMark,
  autoAdvance,
  onAutoAdvanceChange,
}: ReviewStatusCardProps) {
  return (
    <PanelSection
      title="审核状态"
      action={<ReviewImageStatusBadge status={detail.status} />}
    >
      <div className="grid gap-3 rounded-lg border bg-card p-3">
        <ReviewMarkButtons
          status={detail.status}
          marking={marking}
          disabledReason={disabledReason}
          onMark={onMark}
        />
        <p className="text-xs text-muted-foreground">
          {disabledReason ??
            (detail.status === "pending"
              ? "已审核：识别结果正确，模型不需要再学习这张图片；需加样：这张图片需要参与后续的训练。"
              : `标记于 ${formatDateTime(detail.reviewed_at)}${detail.edited ? "，审图标注已修改" : ""}。`)}
        </p>
        <div className="flex items-center justify-between gap-3 border-t pt-3">
          <Label
            htmlFor="review-auto-advance"
            className="text-xs font-normal text-muted-foreground"
          >
            标为已审核 / 需加样后自动跳到下一张
          </Label>
          <Switch
            id="review-auto-advance"
            size="sm"
            checked={autoAdvance}
            onCheckedChange={onAutoAdvanceChange}
          />
        </div>
      </div>
    </PanelSection>
  )
}

type PredictionListProps = {
  predictions: readonly ReviewPrediction[]
  /** 审图标注中已经没有的识别结果（下标）。 */
  unmatched: ReadonlySet<number>
  colorFor: (name: string) => string
  inferred: boolean
  inferenceError: string
  canReset: boolean
  onReset: () => void
  onHighlight: (index: number | null) => void
}

/**
 * 模型的识别结果（不会被修改）：类别、置信度与尺寸。审图标注中已经没有的（被
 * 删除、移动或改了类别）标为“已修改”，在画布上用虚线显示。
 */
export function PredictionList({
  predictions,
  unmatched,
  colorFor,
  inferred,
  inferenceError,
  canReset,
  onReset,
  onHighlight,
}: PredictionListProps) {
  return (
    <PanelSection
      title={`模型识别结果（${formatNumber(predictions.length)}）`}
      action={
        <Button
          variant="ghost"
          size="xs"
          onClick={onReset}
          disabled={!canReset}
          title="审图标注换成模型识别出的框（可以撤销）"
        >
          <RotateCcw />
          恢复
        </Button>
      }
    >
      {!inferred ? (
        <p className="rounded-md border border-dashed p-3 text-sm text-muted-foreground">
          还没有识别。
        </p>
      ) : inferenceError ? (
        <p className="rounded-md border border-destructive/30 bg-destructive/5 p-3 text-xs text-destructive">
          模型没能识别这张图片：{inferenceError}
        </p>
      ) : predictions.length === 0 ? (
        <p className="rounded-md border border-dashed p-3 text-sm text-muted-foreground">
          模型没有识别出目标。
        </p>
      ) : (
        <ol
          className="grid max-h-56 gap-0.5 overflow-y-auto"
          onMouseLeave={() => onHighlight(null)}
        >
          {predictions.map((prediction, index) => {
            const changed = unmatched.has(index)
            return (
              <li
                key={index}
                onMouseEnter={() => onHighlight(changed ? index : null)}
                className="flex items-center gap-2 rounded-md px-2 py-1 text-sm hover:bg-muted"
              >
                <span className="w-5 text-right text-xs text-muted-foreground tabular-nums">
                  {index + 1}
                </span>
                <span
                  className={cn(
                    "size-2.5 shrink-0 rounded-sm",
                    changed && "border border-dashed bg-transparent!"
                  )}
                  style={{
                    backgroundColor: colorFor(prediction.name),
                    borderColor: colorFor(prediction.name),
                  }}
                  aria-hidden
                />
                <span
                  className={cn(
                    "min-w-0 flex-1 truncate",
                    changed && "text-muted-foreground"
                  )}
                >
                  {prediction.name}
                </span>
                {changed && (
                  <span
                    className="rounded-sm bg-primary/10 px-1 text-[10px] text-primary"
                    title="审图标注中已经没有这个框（被删除、移动或改了类别），画布上用虚线显示"
                  >
                    已修改
                  </span>
                )}
                <span className="text-xs text-muted-foreground tabular-nums">
                  {formatConfidence(prediction.confidence)}
                </span>
              </li>
            )
          })}
        </ol>
      )}
    </PanelSection>
  )
}

/** 审图图片的信息：数据集、路径、尺寸、识别与审图标注文件。 */
export function ReviewImageInfo({
  detail,
  size,
  notices,
}: {
  detail: ReviewImageDetail
  size: Size | null
  notices: Notice[]
}) {
  return (
    <PanelSection title="图片">
      <dl className="grid gap-1.5 text-xs">
        <InfoRow label="数据集">
          <span className="break-all">{detail.dataset_name}</span>
        </InfoRow>
        <InfoRow label="路径">
          <span className="font-mono break-all">{detail.path}</span>
        </InfoRow>
        <InfoRow label="尺寸">
          {size ? (
            <span className="tabular-nums">
              {formatNumber(size.width)} × {formatNumber(size.height)}
            </span>
          ) : (
            "未知"
          )}
        </InfoRow>
        <InfoRow label="识别">
          {!detail.inferred ? (
            "还没有识别"
          ) : detail.inference_error ? (
            <span className="text-destructive">识别失败</span>
          ) : (
            `${formatNumber(detail.prediction_count)} 个框`
          )}
        </InfoRow>
        <InfoRow label="审图标注">
          {detail.annotation_version === "" ? (
            "还没有"
          ) : (
            <span className="font-mono break-all">
              {detail.annotation_path}
            </span>
          )}
        </InfoRow>
        {detail.merged_at && (
          <InfoRow label="已合并">
            <span className="tabular-nums">
              {formatDateTime(detail.merged_at)}
            </span>
          </InfoRow>
        )}
      </dl>
      {notices.map((notice, index) => (
        <NoticeBox key={index} notice={notice} />
      ))}
    </PanelSection>
  )
}
