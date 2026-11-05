/**
 * 合并到数据集：把审图标注（默认只有需加样的图片）保存为数据集的一个新的审图
 * 版本（审图1.0、1.1…，附用户填写的备注），不修改数据集的标注文件（基础版本）。
 * 下载数据集时可以选择版本。审图任务保留自己的标注与审核状态，之后还可以再次
 * 合并（生成下一个版本）。合并是后台任务，对话框显示它的进度与结果，关闭后继续
 * 进行。
 */
import { useEffect, useId, useRef, useState, type FormEvent } from "react"
import { useMutation, useQuery } from "@tanstack/react-query"
import {
  CircleAlert,
  CircleCheck,
  GitBranch,
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
import { Field, FieldError, FieldLabel } from "@/components/ui/field"
import { Spinner } from "@/components/ui/spinner"
import { Textarea } from "@/components/ui/textarea"
import { DescriptionCounter } from "@/features/datasets/dataset-actions"
import { TaskProgress } from "@/features/datasets/task-progress"
import {
  createReviewMerge,
  refreshMergedDatasets,
  refreshReviewMerges,
  reviewKeys,
  reviewMergeQuery,
  type Review,
  type ReviewImageStatus,
  type ReviewMerge,
} from "@/features/reviews/api"
import {
  DatasetScopeSelect,
  StatusCheckboxes,
} from "@/features/reviews/review-fields"
import {
  inferredImageCount,
  mergeDatasetsText,
  mergeInput,
  mergeVersionsText,
  onExistingLabels,
  statusesText,
  validateVersionNote,
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
  approved: "识别正确的图片，版本中同样有标注",
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
  const noteId = useId()
  const [statuses, setStatuses] = useState<Set<ReviewImageStatus>>(
    () => new Set(["needs_sample"])
  )
  const [datasetId, setDatasetId] = useState<number | null>(
    review.datasets.some((dataset) => dataset.id === initialDatasetId)
      ? initialDatasetId
      : null
  )
  const [note, setNote] = useState("")
  // 备注为空的提示在第一次提交后才显示。
  const [submitted, setSubmitted] = useState(false)

  const counts = scopedCounts(review, datasetId)
  const imageCount = inferredImageCount(counts, statuses)
  // 有要合并的图片的数据集各生成一个版本。
  const targets = review.datasets.filter(
    (dataset) =>
      (datasetId === null || dataset.id === datasetId) &&
      inferredImageCount(scopedCounts(review, dataset.id), statuses) > 0
  )
  const noteError = validateVersionNote(note)
  const shownNoteError =
    noteError && (submitted || note.trim() !== "") ? noteError : null

  const mutation = useMutation({
    mutationFn: () =>
      createReviewMerge(review.id, mergeInput(statuses, datasetId, note)),
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
    setSubmitted(true)
    if (statuses.size > 0 && imageCount > 0 && noteError === null) {
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
          把审图标注保存为数据集的一个新的审图版本，下载数据集时可以选择版本。
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

      <Field data-invalid={shownNoteError ? true : undefined}>
        <FieldLabel htmlFor={noteId}>版本备注</FieldLabel>
        <Textarea
          id={noteId}
          value={note}
          onChange={(event) => {
            setNote(event.target.value)
            change()
          }}
          placeholder="这个版本的内容，如“第二批产线图片复核，补充划痕样本”"
          aria-invalid={shownNoteError ? true : undefined}
          aria-required
          disabled={pending}
          className="min-h-20"
        />
        {shownNoteError ? (
          <FieldError>{shownNoteError}</FieldError>
        ) : (
          <DescriptionCounter value={note} />
        )}
      </Field>

      {targets.length > 0 && <PlannedVersions datasets={targets} />}

      <Alert>
        <Info />
        <AlertDescription>
          数据集的标注文件（基础版本）不会被修改，标注工作台、标签分布仍是基础版本。审图任务保留自己的标注与审核状态，之后还可以再次合并，生成下一个版本。
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

/** 合并将生成的版本：每个数据集的下一个版本名（预计）。 */
function PlannedVersions({
  datasets,
}: {
  datasets: readonly Review["datasets"][number][]
}) {
  return (
    <div className="grid gap-2 rounded-lg border bg-muted/30 p-3">
      <p className="text-sm font-medium">将生成审图版本</p>
      <ul className="grid gap-1 text-sm">
        {datasets.map((dataset) => (
          <li key={dataset.id} className="flex min-w-0 items-center gap-2">
            <GitBranch className="size-4 shrink-0 text-muted-foreground" />
            <span className="min-w-0 truncate" title={dataset.name}>
              {dataset.name}
            </span>
            {dataset.next_version && (
              <span className="shrink-0 font-medium tabular-nums">
                {dataset.next_version}
              </span>
            )}
          </li>
        ))}
      </ul>
      <p className="text-xs text-muted-foreground">
        版本号在合并完成时确定；其他审图任务先合并完成时会顺延。
      </p>
    </div>
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
            ? `${statusesText(merge.statuses)}的图片 → ${mergeDatasetsText(merge, review.datasets)}${merge.on_existing ? `，${onExistingLabels[merge.on_existing]}` : ""}。`
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
        <MergeStatus merge={merge} datasets={review.datasets} />
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

/**
 * 合并的进度（进行中）或结果（生成的版本、各项计数、没有合并的图片）。
 * `datasetId`：只说明这个数据集的版本（数据集页面的审核记录）。
 */
export function MergeStatus({
  merge,
  datasets,
  datasetId = null,
}: {
  merge: ReviewMerge
  datasets: readonly { id: number; name: string }[]
  datasetId?: number | null
}) {
  const { summary } = merge
  const problemTotal = summary.conflicts + summary.failed
  const legacy = merge.on_existing !== null
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
  const versions = mergeVersionsText(merge, datasets, datasetId)
  return (
    <div className="grid min-w-0 gap-4">
      {merge.status === "failed" ? (
        <Alert variant="destructive">
          <CircleAlert />
          <AlertTitle>合并中断</AlertTitle>
          <AlertDescription>
            <p className="break-words">
              {merge.error ||
                (legacy
                  ? "合并没有完成，已合并的图片保留，可以再次合并。"
                  : "合并没有完成，没有生成新的版本，可以重新合并。")}
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
          <div className="grid min-w-0 gap-1">
            <p className="text-sm font-medium">
              {legacy
                ? `已合并 ${formatNumber(summary.merged)} 张图片的标注`
                : versions
                  ? `已生成 ${versions}，共 ${formatNumber(summary.merged)} 张图片`
                  : summary.merged > 0
                    ? `已合并 ${formatNumber(summary.merged)} 张图片（版本已删除）`
                    : "没有可以合并的图片，没有生成版本"}
              {problemTotal > 0 && `，${formatNumber(problemTotal)} 张没有合并`}
            </p>
            {!legacy && merge.note && (
              <p className="text-xs break-words whitespace-pre-line text-muted-foreground">
                备注：{merge.note}
              </p>
            )}
            {!legacy && versions && (
              <p className="text-xs text-muted-foreground">
                下载数据集时可以选择这个版本：只导出其中需加样的图片，或覆盖基础版本中对应图片的标注后一起导出。
              </p>
            )}
          </div>
        </div>
      )}
      <MergeSummaryGrid merge={merge} />
      <ProblemList
        problems={merge.problems}
        total={problemTotal + summary.skipped_changed}
      />
    </div>
  )
}

/** 合并结果的各项计数。 */
export function MergeSummaryGrid({ merge }: { merge: ReviewMerge }) {
  const { summary } = merge
  const legacy = merge.on_existing !== null
  const items = [
    { label: "已合并", value: summary.merged },
    ...(legacy
      ? [{ label: "已有标注，跳过", value: summary.skipped_existing }]
      : []),
    { label: "没有审图标注", value: summary.skipped_empty },
    ...(legacy
      ? [{ label: "冲突", value: summary.conflicts, warn: true }]
      : [{ label: "状态已改变", value: summary.skipped_changed }]),
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
