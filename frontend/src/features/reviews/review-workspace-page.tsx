import { useMemo } from "react"
import { useQuery } from "@tanstack/react-query"
import { ArrowLeft, ScanEye } from "lucide-react"
import { Link, useParams } from "react-router"

import { Button } from "@/components/ui/button"
import {
  Empty,
  EmptyContent,
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from "@/components/ui/empty"
import { Progress } from "@/components/ui/progress"
import { Spinner } from "@/components/ui/spinner"
import { LoadError } from "@/features/datasets/load-error"
import { reviewQuery, reviewSource, type Review } from "@/features/reviews/api"
import { parseId, reviewDetailPath } from "@/features/reviews/params"
import {
  ReviewEditor,
  ReviewEditorSkeleton,
} from "@/features/reviews/review-editor"
import {
  isInferring,
  reviewStageLabels,
  reviewStatusLabels,
} from "@/features/reviews/review-status"
import { usePageTitle } from "@/hooks/use-page-title"
import { ApiError } from "@/lib/api"
import { formatNumber, percentOf } from "@/lib/format"

/** `/datasets/reviews/:reviewId/workspace?image=`：网页审图。 */
export function ReviewWorkspacePage() {
  const { reviewId } = useParams()
  const id = parseId(reviewId)
  if (id === null) {
    return <Missing />
  }
  return <WorkspaceForReview key={id} id={id} />
}

function WorkspaceForReview({ id }: { id: number }) {
  // 识别中时每 2 秒刷新：识别完成的图片数、各状态的图片数随之更新。
  const query = useQuery(reviewQuery(id))
  const source = useMemo(() => reviewSource(id), [id])
  usePageTitle(query.data ? `审图：${query.data.name}` : null)

  if (query.isPending) {
    return <ReviewEditorSkeleton />
  }
  if (query.error instanceof ApiError && query.error.status === 404) {
    return <Missing />
  }
  if (query.data === undefined) {
    return (
      <div className="p-4 md:p-6">
        <LoadError
          title="无法获取审图任务"
          error={query.error}
          retrying={query.isFetching}
          onRetry={() => query.refetch()}
        />
      </div>
    )
  }
  const review = query.data
  if (review.counts.inferred === 0 && review.counts.total > 0) {
    return <NotInferred review={review} />
  }
  return (
    <ReviewEditor
      source={source}
      review={review}
      backTo={reviewDetailPath(review.id)}
      backLabel={`返回审图任务“${review.name}”`}
    />
  )
}

/** 还没有识别完成任何图片：显示识别进度，识别出第一批后自动进入审图。 */
function NotInferred({ review }: { review: Review }) {
  const inferring = isInferring(review)
  const percent = percentOf(review.counts.inferred, review.counts.total)
  return (
    <Empty className="min-h-[60svh]">
      <EmptyHeader>
        <EmptyMedia variant="icon">
          {inferring ? <Spinner /> : <ScanEye />}
        </EmptyMedia>
        <EmptyTitle>
          {inferring ? "模型正在识别图片" : reviewStatusLabels[review.status]}
        </EmptyTitle>
        <EmptyDescription>
          {review.status === "queued" && !review.model_exists
            ? "模型已删除：模型还在回收站中时，恢复后才会开始识别。"
            : review.status === "queued"
              ? review.queue_position
                ? `排队中（第 ${formatNumber(review.queue_position)} 个），等待训练节点开始识别。`
                : "排队中，等待训练节点开始识别。"
              : review.status === "running"
                ? `${reviewStageLabels[review.stage] || "正在识别"}，识别完成的图片可以立即开始审核。`
                : review.status === "failed"
                  ? `识别失败：${review.error || "未知原因"}。请在审图任务页面重新识别。`
                  : "还没有识别完成的图片。"}
        </EmptyDescription>
      </EmptyHeader>
      <EmptyContent className="w-full max-w-sm">
        {inferring && (
          <div className="grid w-full gap-1.5">
            <Progress value={percent} aria-label="识别进度" />
            <p className="text-xs text-muted-foreground tabular-nums">
              已识别 {formatNumber(review.counts.inferred)} /{" "}
              {formatNumber(review.counts.total)}
            </p>
          </div>
        )}
        <Button variant="outline" asChild>
          <Link to={reviewDetailPath(review.id)}>
            <ArrowLeft />
            返回审图任务
          </Link>
        </Button>
      </EmptyContent>
    </Empty>
  )
}

function Missing() {
  usePageTitle("审图任务不存在")
  return (
    <Empty className="min-h-[60svh]">
      <EmptyHeader>
        <EmptyMedia variant="icon">
          <ScanEye />
        </EmptyMedia>
        <EmptyTitle>审图任务不存在</EmptyTitle>
        <EmptyDescription>
          该审图任务可能已被删除，或者链接有误。
        </EmptyDescription>
      </EmptyHeader>
      <EmptyContent>
        <Button asChild>
          <Link to="/datasets/reviews">
            <ArrowLeft />
            审图任务列表
          </Link>
        </Button>
      </EmptyContent>
    </Empty>
  )
}
