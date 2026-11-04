/**
 * 数据集详情的“审核记录”标签页：包括这个数据集的审图任务，显示这个数据集的
 * 审核进度与合并情况。每个审图任务的标注单独保存，可以选择合并到数据集。
 */
import { useMemo, useState } from "react"
import { useQuery } from "@tanstack/react-query"
import { GitMerge, Plus, ScanEye } from "lucide-react"
import { Link } from "react-router"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import { Card } from "@/components/ui/card"
import {
  Empty,
  EmptyContent,
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from "@/components/ui/empty"
import { Skeleton } from "@/components/ui/skeleton"
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import type { Dataset } from "@/features/datasets/api"
import { LoadError } from "@/features/datasets/load-error"
import {
  datasetReviewsQuery,
  type DatasetReview,
  type ReviewMerge,
} from "@/features/reviews/api"
import { CreateReviewDialog } from "@/features/reviews/create-review-dialog"
import {
  MergeDialog,
  useFinishedMerges,
  useMergesEnded,
} from "@/features/reviews/merge-dialog"
import { reviewDetailPath } from "@/features/reviews/params"
import {
  inferredImageCount,
  mergeSummaryText,
  reviewEntryPath,
  reviewModelText,
  reviewModelTitle,
  statusesText,
} from "@/features/reviews/review-format"
import {
  ReviewStatusBar,
  ReviewStatusLegend,
  ReviewTaskStatusBadge,
} from "@/features/reviews/review-parts"
import { useMediaQuery } from "@/hooks/use-media-query"
import { formatDateTime, formatNumber, formatRelativeTime } from "@/lib/format"
import { cn } from "@/lib/utils"

const NO_REVIEWS: DatasetReview[] = []

/**
 * 标签页上的审图任务数（加载完成后显示）。它在标签上，总是挂载：这个数据集的
 * 审图任务中有合并结束时在这里刷新数据集（标注状态、计数），“审核记录”标签页
 * 没有显示时也是。
 */
export function DatasetReviewsCount({ datasetId }: { datasetId: number }) {
  const { data } = useQuery(datasetReviewsQuery(datasetId))
  useFinishedMerges(data?.items ?? NO_REVIEWS)
  if (!data || data.total === 0) {
    return null
  }
  return (
    <span className="text-xs text-muted-foreground tabular-nums">
      {formatNumber(data.total)}
    </span>
  )
}

export function DatasetReviews({ dataset }: { dataset: Dataset }) {
  const query = useQuery(datasetReviewsQuery(dataset.id))
  const wide = useMediaQuery("(min-width: 48rem)")
  const [createOpen, setCreateOpen] = useState(false)
  // 每次打开新建对话框换一个 key，表单从头开始。
  const [createKey, setCreateKey] = useState(0)
  const [merging, setMerging] = useState<DatasetReview | null>(null)

  const items = useMemo(() => query.data?.items ?? [], [query.data])
  // 合并在对话框之外结束时提示结果（数据集由 DatasetReviewsCount 刷新）。
  useMergesEnded(items, ({ reviewId, merge }) => {
    if (merging?.id === reviewId || !merge) {
      return
    }
    if (merge.status === "succeeded") {
      toast.success("已合并到数据集", {
        description: mergeSummaryText(merge.summary),
      })
    } else {
      toast.error("合并没有完成", { description: merge.error || undefined })
    }
  })

  const openCreate = () => {
    setCreateKey((key) => key + 1)
    setCreateOpen(true)
  }
  // 合并对话框显示最新的审图任务（列表刷新后的状态）。
  const mergingReview = merging
    ? (items.find((item) => item.id === merging.id) ?? merging)
    : null

  let content
  if (query.isPending) {
    content = <Skeleton className="h-48 w-full rounded-xl" />
  } else if (query.data === undefined) {
    content = (
      <LoadError
        title="无法获取审核记录"
        error={query.error}
        retrying={query.isFetching}
        onRetry={() => query.refetch()}
      />
    )
  } else if (items.length === 0) {
    content = (
      <Empty className="border">
        <EmptyHeader>
          <EmptyMedia variant="icon">
            <ScanEye />
          </EmptyMedia>
          <EmptyTitle className="text-base">还没有审核记录</EmptyTitle>
          <EmptyDescription>
            用模型识别这个数据集的图片，逐张确认识别结果；审图标注单独保存，确认后可以选择合并到这个数据集。
          </EmptyDescription>
        </EmptyHeader>
        <EmptyContent>
          <Button
            size="sm"
            onClick={openCreate}
            disabled={dataset.image_count === 0}
          >
            <Plus />
            新建审图任务
          </Button>
        </EmptyContent>
      </Empty>
    )
  } else {
    content = wide ? (
      <ReviewTable items={items} onMerge={setMerging} />
    ) : (
      <ReviewCards items={items} onMerge={setMerging} />
    )
  }

  return (
    <div className="grid gap-3">
      {items.length > 0 && (
        <div className="flex flex-wrap items-center justify-between gap-2">
          <p className="text-sm text-muted-foreground">
            每个审图任务的标注单独保存，只有合并后才会写进这个数据集。
          </p>
          <Button
            size="sm"
            variant="outline"
            onClick={openCreate}
            disabled={dataset.image_count === 0}
          >
            <Plus />
            新建审图任务
          </Button>
        </div>
      )}
      {content}

      <CreateReviewDialog
        key={createKey}
        open={createOpen}
        onOpenChange={setCreateOpen}
        datasetIds={[dataset.id]}
      />
      {mergingReview && (
        <MergeDialog
          review={mergingReview}
          open
          datasetId={dataset.id}
          onOpenChange={(open) => !open && setMerging(null)}
        />
      )}
    </div>
  )
}

type ListProps = {
  items: DatasetReview[]
  onMerge: (review: DatasetReview) => void
}

/** 这次合并还包括别的数据集时的说明，如“（共 2 个数据集）”。 */
function otherDatasetsNote(merge: ReviewMerge, review: DatasetReview) {
  const count =
    merge.dataset_ids.length > 0
      ? merge.dataset_ids.length
      : review.datasets.length
  return count > 1 ? `（共 ${formatNumber(count)} 个数据集）` : ""
}

/**
 * 这个数据集的合并情况：已合并的张数（这个数据集的，合并可能包括别的数据集）、
 * 最近一次合并的时间；进行中或中断时显示合并的状态。没有合并过时为 null。
 */
function latestMerge(review: DatasetReview) {
  const merge = review.dataset_merges[0]
  if (!merge) {
    return null
  }
  const merged = review.dataset.counts.merged
  return {
    text:
      merge.status === "running"
        ? `正在合并 ${formatNumber(merge.done)} / ${formatNumber(merge.total)}`
        : merge.status === "failed"
          ? "合并中断"
          : `已合并 ${formatNumber(merged)} 张`,
    time: merge.finished_at ?? merge.created_at,
    title: review.dataset_merges
      .map(
        (item) =>
          `${formatDateTime(item.finished_at ?? item.created_at)}：${statusesText(item.statuses)}${otherDatasetsNote(item, review)}，${item.status === "running" ? "进行中" : item.status === "failed" ? "中断" : mergeSummaryText(item.summary)}`
      )
      .join("\n"),
  }
}

function MergeInfo({ review }: { review: DatasetReview }) {
  const latest = latestMerge(review)
  if (!latest) {
    return <span className="text-muted-foreground">还没有合并</span>
  }
  return (
    <span className="grid min-w-0 gap-0.5" title={latest.title}>
      <span className="truncate tabular-nums">{latest.text}</span>
      <span className="text-xs text-muted-foreground">
        {formatRelativeTime(latest.time)}
        {/* 列表最多带 5 次合并。 */}
        {review.dataset_merges.length > 1 &&
          (review.dataset_merges.length < 5
            ? ` · 共 ${formatNumber(review.dataset_merges.length)} 次`
            : " · 至少 5 次")}
      </span>
    </span>
  )
}

/** 审图、合并与查看任务；表格中（compact）不换行，也不显示“查看任务”（名称就是链接）。 */
function RowActions({
  review,
  onMerge,
  compact = false,
}: {
  review: DatasetReview
  onMerge: (review: DatasetReview) => void
  compact?: boolean
}) {
  const counts = review.dataset.counts
  const canReview = counts.inferred > 0
  const mergeable =
    review.active_merge === null &&
    inferredImageCount(counts, ["approved", "needs_sample"]) > 0
  return (
    <div
      className={cn(
        "flex shrink-0 items-center justify-end gap-1",
        compact ? "min-w-max" : "flex-wrap"
      )}
    >
      {canReview ? (
        <Button variant="outline" size="sm" asChild>
          <Link to={reviewEntryPath(review, review.dataset.id)}>
            <ScanEye />
            审图
          </Link>
        </Button>
      ) : (
        <Button variant="outline" size="sm" disabled>
          <ScanEye />
          审图
        </Button>
      )}
      <Button
        variant="ghost"
        size="sm"
        onClick={() => onMerge(review)}
        disabled={!mergeable && review.active_merge === null}
        title={
          review.active_merge
            ? "正在合并，查看进度"
            : mergeable
              ? "把审图标注合并到这个数据集"
              : "还没有已审核或需加样的图片"
        }
      >
        <GitMerge />
        {review.active_merge ? "合并中" : compact ? "合并" : "合并到数据集"}
      </Button>
      {!compact && (
        <Button variant="ghost" size="sm" asChild>
          <Link to={reviewDetailPath(review.id)}>查看任务</Link>
        </Button>
      )}
    </div>
  )
}

function ReviewTable({ items, onMerge }: ListProps) {
  return (
    <Card className="gap-0 overflow-hidden py-0">
      <Table>
        <TableHeader>
          <TableRow className="hover:bg-transparent">
            <TableHead className="pl-4">审图任务</TableHead>
            <TableHead>识别</TableHead>
            <TableHead>这个数据集的审核进度</TableHead>
            <TableHead className="hidden xl:table-cell">合并</TableHead>
            <TableHead className="pr-4">
              <span className="sr-only">操作</span>
            </TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {items.map((review) => (
            <TableRow key={review.id}>
              <TableCell className="w-full max-w-0 pl-4">
                <div className="grid min-w-0 gap-0.5">
                  <Link
                    to={reviewDetailPath(review.id)}
                    className="truncate font-medium underline-offset-4 outline-none hover:underline focus-visible:underline"
                    title={review.name}
                  >
                    {review.name}
                  </Link>
                  <span
                    className="truncate text-xs text-muted-foreground"
                    title={formatDateTime(review.created_at)}
                  >
                    {reviewModelTitle(review.model)} ·{" "}
                    {reviewModelText(review.model)} · 创建于{" "}
                    {formatRelativeTime(review.created_at)}
                  </span>
                </div>
              </TableCell>
              <TableCell className="whitespace-nowrap">
                <ReviewTaskStatusBadge
                  review={{ ...review, counts: review.dataset.counts }}
                />
              </TableCell>
              <TableCell>
                <div className="grid w-48 gap-1.5 xl:w-56">
                  <ReviewStatusBar counts={review.dataset.counts} />
                  <ReviewStatusLegend counts={review.dataset.counts} />
                </div>
              </TableCell>
              <TableCell className="hidden xl:table-cell">
                <div className="w-32">
                  <MergeInfo review={review} />
                </div>
              </TableCell>
              <TableCell className="pr-4">
                <RowActions review={review} onMerge={onMerge} compact />
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </Card>
  )
}

function ReviewCards({ items, onMerge }: ListProps) {
  return (
    <ul className="grid grid-cols-[minmax(0,1fr)] gap-3">
      {items.map((review) => (
        <li key={review.id}>
          <article className="grid grid-cols-[minmax(0,1fr)] gap-3 rounded-xl border bg-card p-4 text-card-foreground shadow-sm">
            <div className="grid min-w-0 gap-0.5">
              <Link
                to={reviewDetailPath(review.id)}
                className="truncate font-medium underline-offset-4 hover:underline"
              >
                {review.name}
              </Link>
              <p className="truncate text-xs text-muted-foreground">
                {reviewModelTitle(review.model)} ·{" "}
                {reviewModelText(review.model)} ·{" "}
                {formatRelativeTime(review.created_at)}
              </p>
            </div>
            <div>
              <ReviewTaskStatusBadge
                review={{ ...review, counts: review.dataset.counts }}
              />
            </div>
            <div className="grid gap-1.5">
              <ReviewStatusBar counts={review.dataset.counts} />
              <ReviewStatusLegend counts={review.dataset.counts} />
            </div>
            <div className="border-t pt-3 text-sm">
              <MergeInfo review={review} />
            </div>
            <RowActions review={review} onMerge={onMerge} />
          </article>
        </li>
      ))}
    </ul>
  )
}
