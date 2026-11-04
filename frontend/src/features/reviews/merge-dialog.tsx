/**
 * 合并到数据集：把审图标注（默认只有需加样的图片）写进数据集的 VOC 标注文件。
 * 审图任务保留自己的标注与审核状态，之后还可以再次合并。合并是后台任务，
 * 对话框显示它的进度与结果，关闭后继续进行。
 */
import { useEffect, useId, useRef, useState, type FormEvent } from "react"
import { useMutation, useQuery } from "@tanstack/react-query"
import {
  CircleAlert,
  CircleCheck,
  GitMerge,
  Info,
  TriangleAlert,
} from "lucide-react"

import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { Field, FieldLabel } from "@/components/ui/field"
import { RadioGroup } from "@/components/ui/radio-group"
import { Spinner } from "@/components/ui/spinner"
import { TaskProgress } from "@/features/datasets/task-progress"
import {
  createReviewMerge,
  refreshMergedDatasets,
  refreshReviewMerges,
  reviewKeys,
  reviewMergeQuery,
  type CreateReviewMergeInput,
  type Review,
  type ReviewImageStatus,
  type ReviewMerge,
} from "@/features/reviews/api"
import {
  ChoiceCard,
  DatasetScopeSelect,
  StatusCheckboxes,
} from "@/features/reviews/review-fields"
import {
  inferredImageCount,
  mergeDatasetsText,
  mergeInput,
  statusesText,
} from "@/features/reviews/review-format"
import { ProblemList } from "@/features/reviews/review-parts"
import { scopedCounts } from "@/features/reviews/review-status"
import { ApiError } from "@/lib/api"
import { formatNumber, percentOf } from "@/lib/format"
import { queryClient } from "@/lib/query-client"
import { cn } from "@/lib/utils"

/** 可以合并的审核状态（审核过的结果；待审核的是没有确认的识别结果）。 */
const MERGE_STATUSES: readonly ReviewImageStatus[] = [
  "needs_sample",
  "approved",
]

const STATUS_HINTS: Partial<Record<ReviewImageStatus, string>> = {
  needs_sample: "需要参与后续训练的图片",
  approved: "识别正确的图片，合并后同样有标注",
}

export const onExistingLabels: Record<
  CreateReviewMergeInput["on_existing"],
  string
> = {
  skip: "跳过已有标注的图片",
  overwrite: "覆盖已有标注",
}

type MergeDialogProps = {
  review: Review
  open: boolean
  onOpenChange: (open: boolean) => void
  /** 只合并到这个数据集（数据集列表、数据集页面的入口）；null 为全部。 */
  datasetId?: number | null
}

export function MergeDialog({
  review,
  open,
  onOpenChange,
  datasetId = null,
}: MergeDialogProps) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[calc(100dvh-2rem)] overflow-y-auto sm:max-w-xl">
        {open && (
          <MergeContent
            review={review}
            initialDatasetId={datasetId}
            onDone={() => onOpenChange(false)}
          />
        )}
      </DialogContent>
    </Dialog>
  )
}

function MergeContent({
  review,
  initialDatasetId,
  onDone,
}: {
  review: Review
  initialDatasetId: number | null
  onDone: () => void
}) {
  // 已有合并在进行：直接显示它的进度（一个审图任务同时只能有一个合并）。打开
  // 时还不知道的（如开始合并时 409、列表刷新后才看到）出现后也切换过去；显示
  // 之后一直显示它，合并结束后 `active_merge` 变回 null 时仍显示结果。
  const [mergeId, setMergeId] = useState<number | null>(
    review.active_merge?.id ?? null
  )
  if (mergeId === null && review.active_merge) {
    setMergeId(review.active_merge.id)
  }
  if (mergeId !== null) {
    return <MergeProgress review={review} mergeId={mergeId} onDone={onDone} />
  }
  return (
    <MergeForm
      review={review}
      initialDatasetId={initialDatasetId}
      onStarted={setMergeId}
      onDone={onDone}
    />
  )
}

function MergeForm({
  review,
  initialDatasetId,
  onStarted,
  onDone,
}: {
  review: Review
  initialDatasetId: number | null
  onStarted: (mergeId: number) => void
  onDone: () => void
}) {
  const datasetFieldId = useId()
  const onExistingId = useId()
  const [statuses, setStatuses] = useState<Set<ReviewImageStatus>>(
    () => new Set(["needs_sample"])
  )
  const [datasetId, setDatasetId] = useState<number | null>(
    review.datasets.some((dataset) => dataset.id === initialDatasetId)
      ? initialDatasetId
      : null
  )
  const [onExisting, setOnExisting] =
    useState<CreateReviewMergeInput["on_existing"]>("skip")

  const counts = scopedCounts(review, datasetId)
  const imageCount = inferredImageCount(counts, statuses)
  const scopeDataset =
    datasetId === null
      ? review.datasets.length === 1
        ? review.datasets[0]
        : undefined
      : review.datasets.find((dataset) => dataset.id === datasetId)
  const scopeName = scopeDataset
    ? `数据集“${scopeDataset.name}”`
    : datasetId === null
      ? `这 ${formatNumber(review.datasets.length)} 个数据集`
      : "数据集"

  const mutation = useMutation({
    mutationFn: () =>
      createReviewMerge(review.id, mergeInput(statuses, datasetId, onExisting)),
    onSuccess: (merge) => {
      queryClient.setQueryData(reviewKeys.merge(review.id, merge.id), merge)
      // 任务上的 active_merge（详情、列表、数据集的审核记录）与合并记录：关闭
      // 对话框后页面照样显示进度，合并结束时刷新数据集。
      refreshReviewMerges(review.id)
      onStarted(merge.id)
    },
    onError: (error) => {
      if (error instanceof ApiError && error.status === 409) {
        // 多半是已有合并在进行：显示最新的状态（刷新后对话框显示它的进度）。
        refreshReviewMerges(review.id)
      }
    },
  })

  const submit = (event: FormEvent) => {
    event.preventDefault()
    if (statuses.size > 0 && imageCount > 0) {
      mutation.mutate()
    }
  }
  const pending = mutation.isPending
  const change = () => mutation.reset()

  return (
    <form
      onSubmit={submit}
      className="grid min-w-0 grid-cols-[minmax(0,1fr)] gap-6"
      noValidate
    >
      <DialogHeader>
        <DialogTitle>合并到数据集</DialogTitle>
        <DialogDescription>
          把审图标注写进数据集，用于之后的训练。
        </DialogDescription>
      </DialogHeader>

      <Field>
        <FieldLabel asChild>
          <span>合并哪些图片</span>
        </FieldLabel>
        <StatusCheckboxes
          statuses={MERGE_STATUSES}
          value={statuses}
          onChange={(next) => {
            setStatuses(next)
            change()
          }}
          counts={counts}
          hints={STATUS_HINTS}
          disabled={pending}
          label="合并哪些审核状态的图片"
        />
      </Field>

      {review.datasets.length > 1 && (
        <Field>
          <FieldLabel htmlFor={datasetFieldId}>数据集</FieldLabel>
          <DatasetScopeSelect
            id={datasetFieldId}
            review={review}
            value={datasetId}
            onChange={(next) => {
              setDatasetId(next)
              change()
            }}
            disabled={pending}
          />
        </Field>
      )}

      <div className="grid gap-3">
        <FieldLabel id={onExistingId}>数据集中的图片已有标注时</FieldLabel>
        <RadioGroup
          value={onExisting}
          onValueChange={(value) => {
            setOnExisting(value as CreateReviewMergeInput["on_existing"])
            change()
          }}
          aria-labelledby={onExistingId}
          className="grid gap-2 sm:grid-cols-2"
          disabled={pending}
        >
          <ChoiceCard
            id={`${onExistingId}-skip`}
            value="skip"
            selected={onExisting === "skip"}
            title={onExistingLabels.skip}
            description="只给还没有标注文件的图片写入审图标注，已有的标注保持不变"
          />
          <ChoiceCard
            id={`${onExistingId}-overwrite`}
            value="overwrite"
            selected={onExisting === "overwrite"}
            title={onExistingLabels.overwrite}
            description="用审图标注替换数据集中原有的标注文件"
          />
        </RadioGroup>
      </div>

      <Alert>
        <Info />
        <AlertDescription>
          只改动{scopeName}中这些图片的 VOC
          标注文件，图片本身不变。审图任务保留自己的标注与审核状态，之后还可以再次合并。
        </AlertDescription>
      </Alert>

      <p className="text-sm text-muted-foreground tabular-nums">
        {statuses.size === 0
          ? "请至少选择一种审核状态。"
          : imageCount === 0
            ? `没有${statusesText(statuses)}的图片。`
            : `将合并 ${formatNumber(imageCount)} 张${statusesText(statuses)}的图片。`}
      </p>

      {mutation.error && (
        <p role="alert" className="text-sm text-destructive">
          {mutation.error.message}
        </p>
      )}
      <DialogFooter>
        <Button
          type="button"
          variant="outline"
          onClick={onDone}
          disabled={pending}
        >
          取消
        </Button>
        <Button
          type="submit"
          disabled={pending || statuses.size === 0 || imageCount === 0}
        >
          {pending ? <Spinner aria-hidden /> : <GitMerge />}
          开始合并
        </Button>
      </DialogFooter>
    </form>
  )
}

type MergeWatched = Pick<Review, "id" | "active_merge" | "last_merge">

/**
 * 一次结束的合并：`running` 是之前看到的进行中的合并，`merge` 见
 * {@link endedMerges}。
 */
export type EndedMerge = {
  reviewId: number
  running: ReviewMerge
  merge: ReviewMerge | null
}

/**
 * 比较前后两次看到的审图任务：`active` 是现在进行中的合并（按任务），`ended`
 * 是之前进行中、现在没有了的合并；`merge` 是结束的合并（任务的 `last_merge`，
 * 与之前进行中的是同一个时，否则为 null）。
 */
export function endedMerges(
  previous: ReadonlyMap<number, ReviewMerge>,
  reviews: readonly MergeWatched[]
): { active: Map<number, ReviewMerge>; ended: EndedMerge[] } {
  const active = new Map<number, ReviewMerge>()
  const ended: EndedMerge[] = []
  for (const review of reviews) {
    if (review.active_merge) {
      active.set(review.id, review.active_merge)
      continue
    }
    const running = previous.get(review.id)
    if (running) {
      ended.push({
        reviewId: review.id,
        running,
        merge: review.last_merge?.id === running.id ? review.last_merge : null,
      })
    }
  }
  return { active, ended }
}

/** 页面上的审图任务中有合并结束（`active_merge` 从有到无）时调用 `onEnded`。 */
export function useMergesEnded(
  reviews: readonly MergeWatched[],
  onEnded: (ended: EndedMerge) => void
) {
  const active = useRef(new Map<number, ReviewMerge>())
  const callback = useRef(onEnded)
  useEffect(() => {
    callback.current = onEnded
  })
  useEffect(() => {
    const result = endedMerges(active.current, reviews)
    active.current = result.active
    for (const ended of result.ended) {
      callback.current(ended)
    }
  }, [reviews])
}

/**
 * 页面上的审图任务中有合并结束（`active_merge` 从有到无）时，刷新被合并的
 * 数据集与审图任务；`onFinished` 收到结束的合并（任务的 `last_merge`，与刚才
 * 进行中的是同一个时）。合并对话框自己也会刷新，两边重复刷新没有关系。
 */
export function useFinishedMerges(
  reviews: readonly MergeWatched[],
  onFinished?: (reviewId: number, merge: ReviewMerge | null) => void
) {
  useMergesEnded(reviews, ({ reviewId, running, merge }) => {
    refreshMergedDatasets(reviewId, running.dataset_ids)
    onFinished?.(reviewId, merge)
  })
}

/** 合并的进度与结果（每秒刷新，结束后刷新被合并的数据集）。 */
function MergeProgress({
  review,
  mergeId,
  onDone,
}: {
  review: Review
  mergeId: number
  onDone: () => void
}) {
  const query = useQuery(reviewMergeQuery(review.id, mergeId))
  const merge = query.data

  // 合并结束时刷新数据集（标注状态、计数、标签分布）与审图任务。
  const finished = merge !== undefined && merge.status !== "running"
  const refreshed = useRef(false)
  useEffect(() => {
    if (finished && merge && !refreshed.current) {
      refreshed.current = true
      refreshMergedDatasets(review.id, merge.dataset_ids)
    }
  }, [finished, merge, review.id])

  return (
    <div className="grid min-w-0 grid-cols-[minmax(0,1fr)] gap-6">
      <DialogHeader>
        <DialogTitle>
          {merge?.status === "running" || !merge
            ? "正在合并到数据集"
            : merge.status === "succeeded"
              ? "合并完成"
              : "合并没有完成"}
        </DialogTitle>
        <DialogDescription>
          {merge
            ? `${statusesText(merge.statuses)}的图片 → ${mergeDatasetsText(merge, review.datasets)}，${onExistingLabels[merge.on_existing]}。`
            : "正在读取合并的进度…"}
        </DialogDescription>
      </DialogHeader>

      {merge === undefined ? (
        query.error ? (
          <p role="alert" className="text-sm text-destructive">
            {query.error.message}
          </p>
        ) : (
          <TaskProgress value={null} label="合并进度" />
        )
      ) : (
        <MergeStatus merge={merge} />
      )}

      <DialogFooter>
        {merge?.status === "running" && (
          <p className="mr-auto self-center text-xs text-muted-foreground">
            关闭后合并在后台继续。
          </p>
        )}
        <Button
          variant={merge?.status === "running" ? "outline" : "default"}
          onClick={onDone}
        >
          {merge?.status === "running" ? "关闭" : "完成"}
        </Button>
      </DialogFooter>
    </div>
  )
}

/** 合并的进度（进行中）或结果（各项计数、没有合并的图片）。 */
export function MergeStatus({ merge }: { merge: ReviewMerge }) {
  const { summary } = merge
  const problemTotal = summary.conflicts + summary.failed
  if (merge.status === "running") {
    return (
      <div className="grid gap-1.5">
        <TaskProgress
          value={merge.total > 0 ? percentOf(merge.done, merge.total) : null}
          label="合并进度"
        />
        <p className="flex items-center gap-1.5 text-xs text-muted-foreground tabular-nums">
          <Spinner className="size-3" aria-hidden />
          已处理 {formatNumber(merge.done)} / {formatNumber(merge.total)} 张
        </p>
      </div>
    )
  }
  return (
    <div className="grid min-w-0 gap-4">
      {merge.status === "failed" ? (
        <Alert variant="destructive">
          <CircleAlert />
          <AlertTitle>合并中断</AlertTitle>
          <AlertDescription>
            <p className="break-words">
              {merge.error || "合并没有完成，已合并的图片保留，可以再次合并。"}
            </p>
          </AlertDescription>
        </Alert>
      ) : (
        <div className="flex items-start gap-3 rounded-lg border bg-muted/30 p-3">
          {problemTotal > 0 ? (
            <TriangleAlert className="mt-0.5 size-5 shrink-0 text-amber-600 dark:text-amber-500" />
          ) : (
            <CircleCheck className="mt-0.5 size-5 shrink-0 text-emerald-600 dark:text-emerald-500" />
          )}
          <p className="text-sm font-medium">
            已合并 {formatNumber(summary.merged)} 张图片的标注
            {problemTotal > 0 && `，${formatNumber(problemTotal)} 张没有合并`}
          </p>
        </div>
      )}
      <MergeSummaryGrid merge={merge} />
      <ProblemList problems={merge.problems} total={problemTotal} />
    </div>
  )
}

/** 合并结果的各项计数。 */
export function MergeSummaryGrid({ merge }: { merge: ReviewMerge }) {
  const { summary } = merge
  const items = [
    { label: "已合并", value: summary.merged },
    { label: "已有标注，跳过", value: summary.skipped_existing },
    { label: "没有审图标注", value: summary.skipped_empty },
    { label: "冲突", value: summary.conflicts, warn: true },
    { label: "失败", value: summary.failed, warn: true },
  ]
  return (
    <dl className="flex flex-wrap gap-2">
      {items.map((item) => (
        <div
          key={item.label}
          className="grid min-w-20 flex-auto gap-0.5 rounded-md border px-3 py-2"
        >
          <dt className="text-xs whitespace-nowrap text-muted-foreground">
            {item.label}
          </dt>
          <dd
            className={cn(
              "text-base font-semibold tabular-nums",
              item.warn &&
                item.value > 0 &&
                "text-amber-700 dark:text-amber-500"
            )}
          >
            {formatNumber(item.value)}
          </dd>
        </div>
      ))}
    </dl>
  )
}
