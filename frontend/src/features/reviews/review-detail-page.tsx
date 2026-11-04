/**
 * 审图任务详情（`/models/reviews/:reviewId`）：识别进度或失败原因、各审核状态的
 * 图片数、每个数据集的情况、本地审图的步骤与上传、合并记录。排队、识别中或正在
 * 合并时自动刷新。
 */
import { useMemo, useState, type ReactNode } from "react"
import { useQuery } from "@tanstack/react-query"
import {
  ArrowLeft,
  Boxes,
  CircleCheck,
  CircleDashed,
  Download,
  FolderKanban,
  GitMerge,
  ImagePlus,
  Images,
  MonitorDown,
  PencilLine,
  RotateCcw,
  ScanEye,
  Server,
  SquareX,
  TriangleAlert,
  Upload,
} from "lucide-react"
import { Link, useNavigate, useParams } from "react-router"
import { toast } from "sonner"

import { PageHeader } from "@/components/page-header"
import { StatCard } from "@/components/stat-card"
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"
import {
  Empty,
  EmptyContent,
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from "@/components/ui/empty"
import { Skeleton } from "@/components/ui/skeleton"
import { Spinner } from "@/components/ui/spinner"
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import { LoadError } from "@/features/datasets/load-error"
import { TaskProgress } from "@/features/datasets/task-progress"
import { CopyButton } from "@/features/training/command-block"
import {
  REVIEW_TOOL_DOWNLOAD_URL,
  reviewQuery,
  reviewToolQuery,
  type Review,
  type ReviewDataset,
  type ReviewMerge,
} from "@/features/reviews/api"
import { MergeDialog, useFinishedMerges } from "@/features/reviews/merge-dialog"
import { DownloadPackageDialog } from "@/features/reviews/package-dialog"
import { parseId } from "@/features/reviews/params"
import {
  ReviewActionsMenu,
  useReviewActions,
} from "@/features/reviews/review-actions"
import {
  datasetNamesText,
  inferenceReport,
  inferenceSpeedText,
  inferredImageCount,
  mergeSummaryText,
  reviewEntryPath,
  reviewModelText,
  reviewModelTitle,
  reviewOptionsText,
  uploadSummaryText,
} from "@/features/reviews/review-format"
import { ReviewHistory } from "@/features/reviews/review-history"
import {
  ReviewStatusBar,
  ReviewStatusLegend,
  ReviewTaskStatusBadge,
} from "@/features/reviews/review-parts"
import { reviewStageLabels } from "@/features/reviews/review-status"
import { UploadResultsDialog } from "@/features/reviews/upload-results-dialog"
import { useMediaQuery } from "@/hooks/use-media-query"
import { usePageTitle } from "@/hooks/use-page-title"
import { ApiError } from "@/lib/api"
import { startDownload } from "@/lib/download"
import {
  formatBytes,
  formatDateTime,
  formatNumber,
  formatPercent,
  formatRelativeTime,
  percentOf,
} from "@/lib/format"
import { imgSizeText } from "@/features/models/yolo-versions"

export function ReviewDetailPage() {
  const { reviewId } = useParams()
  const id = parseId(reviewId)
  if (id === null) {
    return <ReviewNotFound />
  }
  // 切换审图任务时整体重置页面状态（对话框等）。
  return <ReviewDetail key={id} id={id} />
}

function ReviewNotFound() {
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
          <Link to="/models/reviews">
            <ArrowLeft />
            返回审图任务列表
          </Link>
        </Button>
      </EmptyContent>
    </Empty>
  )
}

/** 页面上的对话框：合并时可以只合并到某个数据集。 */
type DialogState =
  | { kind: "package" }
  | { kind: "upload" }
  | { kind: "merge"; datasetId: number | null }
  | null

function ReviewDetail({ id }: { id: number }) {
  const navigate = useNavigate()
  const query = useQuery(reviewQuery(id))
  const { actions, dialogs } = useReviewActions({
    onDeleted: () => navigate("/models/reviews", { replace: true }),
  })
  const [dialog, setDialog] = useState<DialogState>(null)

  usePageTitle(query.data?.name)

  // 合并在对话框之外结束时（如关闭了对话框）刷新数据集，并提示结果。
  const watched = useMemo(() => (query.data ? [query.data] : []), [query.data])
  useFinishedMerges(watched, (_, merge) => {
    if (dialog?.kind === "merge" || !merge) {
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

  if (query.isPending) {
    return <DetailSkeleton />
  }
  if (query.error instanceof ApiError && query.error.status === 404) {
    return <ReviewNotFound />
  }
  // 后台刷新失败时继续显示已加载的数据，不卸载打开着的对话框。
  if (query.data === undefined) {
    return (
      <LoadError
        title="无法获取审图任务"
        error={query.error}
        retrying={query.isFetching}
        onRetry={() => query.refetch()}
      />
    )
  }

  const review = query.data
  const canReview = review.counts.inferred > 0
  const openMerge = (datasetId: number | null = null) =>
    setDialog({ kind: "merge", datasetId })

  return (
    <>
      {/* grid-cols-1 = minmax(0, 1fr)：长名称不撑宽页面。 */}
      <div className="grid grid-cols-1 gap-3">
        <PageHeader
          title={review.name}
          description={review.description || undefined}
          actions={
            <>
              <Button
                variant="outline"
                onClick={() => setDialog({ kind: "package" })}
                disabled={!canReview}
                aria-label="下载审图包"
                title="下载审图包，用本地审图程序离线审图"
              >
                <Download />
                <span className="hidden sm:inline">下载审图包</span>
              </Button>
              <Button
                variant="outline"
                onClick={() => setDialog({ kind: "upload" })}
                aria-label="上传审图结果"
                title="上传本地审图程序导出的审图结果"
              >
                <Upload />
                <span className="hidden sm:inline">上传审图结果</span>
              </Button>
              <Button
                variant="outline"
                onClick={() => openMerge()}
                disabled={!canReview && review.active_merge === null}
                aria-label={review.active_merge ? "正在合并" : "合并到数据集"}
                title={
                  review.active_merge
                    ? "正在合并到数据集，查看进度"
                    : "把审图标注合并到数据集的标注中"
                }
              >
                {review.active_merge ? <Spinner aria-hidden /> : <GitMerge />}
                <span className="hidden sm:inline">
                  {review.active_merge ? "正在合并" : "合并到数据集"}
                </span>
              </Button>
              {canReview ? (
                <Button asChild>
                  <Link to={reviewEntryPath(review)}>
                    <ScanEye />
                    网页审图
                  </Link>
                </Button>
              ) : (
                <Button disabled title="识别完成的图片可以开始审核">
                  <ScanEye />
                  网页审图
                </Button>
              )}
              <ReviewActionsMenu
                review={review}
                actions={actions}
                showLinks={false}
                className="size-9"
              />
            </>
          }
        />
        <ReviewMeta review={review} />
      </div>

      <InferenceStatus
        review={review}
        onRetry={() => actions.retry(review)}
        retrying={actions.retrying === review.id}
      />
      {review.active_merge && (
        <ActiveMerge
          merge={review.active_merge}
          onShow={() => openMerge(null)}
        />
      )}

      <ReviewCountsGrid review={review} />
      <DatasetsCard review={review} onMerge={openMerge} />

      <div className="grid gap-6 lg:grid-cols-2">
        <LocalReviewCard
          review={review}
          onPackage={() => setDialog({ kind: "package" })}
          onUpload={() => setDialog({ kind: "upload" })}
        />
        <InferenceInfoCard review={review} />
      </div>

      <ReviewHistory review={review} />

      {dialogs}
      <DownloadPackageDialog
        review={review}
        open={dialog?.kind === "package"}
        onOpenChange={(open) => !open && setDialog(null)}
      />
      <UploadResultsDialog
        review={review}
        open={dialog?.kind === "upload"}
        onOpenChange={(open) => !open && setDialog(null)}
      />
      <MergeDialog
        review={review}
        open={dialog?.kind === "merge"}
        datasetId={dialog?.kind === "merge" ? dialog.datasetId : null}
        onOpenChange={(open) => !open && setDialog(null)}
      />
    </>
  )
}

function ReviewMeta({ review }: { review: Review }) {
  const model = (
    <>
      <Boxes className="size-3.5 shrink-0" />
      <span className="max-w-60 truncate">
        {reviewModelTitle(review.model)} · {reviewModelText(review.model)}
      </span>
    </>
  )
  return (
    <div className="flex flex-wrap items-center gap-x-4 gap-y-2 text-xs text-muted-foreground">
      <ReviewTaskStatusBadge review={review} />
      {review.model_exists ? (
        <Link
          to={`/models/${review.model.id}`}
          className="inline-flex min-w-0 items-center gap-1 underline-offset-4 hover:text-foreground hover:underline"
        >
          {model}
        </Link>
      ) : (
        <span
          className="inline-flex min-w-0 items-center gap-1"
          title="模型已被删除，审图任务保留了创建时的模型信息"
        >
          {model}
          <span>（已删除）</span>
        </span>
      )}
      {review.project && (
        <Link
          to={`/projects/${review.project.id}`}
          className="inline-flex min-w-0 items-center gap-1 underline-offset-4 hover:text-foreground hover:underline"
        >
          <FolderKanban className="size-3.5 shrink-0" />
          <span className="max-w-60 truncate">{review.project.name}</span>
        </Link>
      )}
      <span
        className="inline-flex min-w-0 items-center gap-1"
        title={review.datasets.map((dataset) => dataset.name).join("、")}
      >
        <Images className="size-3.5 shrink-0" />
        <span className="max-w-60 truncate">
          {datasetNamesText(review.datasets)} ·{" "}
          {formatNumber(review.counts.total)} 张
        </span>
      </span>
      <span title={formatDateTime(review.created_at)}>
        创建于 {formatRelativeTime(review.created_at)}
      </span>
    </div>
  )
}

/** 排队、识别中的进度，或识别失败的原因与重新识别。 */
function InferenceStatus({
  review,
  onRetry,
  retrying,
}: {
  review: Review
  onRetry: () => void
  retrying: boolean
}) {
  const [showDetail, setShowDetail] = useState(false)
  const { counts } = review
  const left = counts.total - counts.inferred

  if (review.status === "failed") {
    return (
      <Alert variant="destructive">
        <TriangleAlert />
        <AlertTitle>识别失败</AlertTitle>
        <AlertDescription>
          <p className="break-words whitespace-pre-line">
            {review.error || "训练节点没有说明原因"}
          </p>
          <p>
            {counts.inferred > 0
              ? `已识别的 ${formatNumber(counts.inferred)} 张图片可以正常审核；`
              : ""}
            {review.model_exists
              ? `重新识别时只识别剩下的 ${formatNumber(left)} 张。`
              : "模型已被删除，无法重新识别。"}
          </p>
          <div className="mt-2 flex flex-wrap items-center gap-2">
            {review.model_exists && (
              <Button
                size="sm"
                variant="outline"
                onClick={onRetry}
                disabled={retrying}
                className="text-foreground"
              >
                {retrying ? <Spinner aria-hidden /> : <RotateCcw />}
                重新识别
              </Button>
            )}
            {review.error_detail && (
              <Button
                size="sm"
                variant="ghost"
                onClick={() => setShowDetail((value) => !value)}
                aria-expanded={showDetail}
              >
                {showDetail ? "收起错误详情" : "查看错误详情"}
              </Button>
            )}
            {review.error_detail && showDetail && (
              <CopyButton text={review.error_detail} label="复制错误详情" />
            )}
          </div>
          {review.error_detail && showDetail && (
            <pre className="mt-2 max-h-60 w-full overflow-auto rounded-md border bg-muted/50 p-3 font-mono text-xs leading-relaxed break-all whitespace-pre-wrap text-foreground">
              {review.error_detail}
            </pre>
          )}
        </AlertDescription>
      </Alert>
    )
  }

  if (review.status !== "queued" && review.status !== "running") {
    return null
  }

  const queued = review.status === "queued"
  return (
    <Card className="min-w-0 gap-4">
      <CardHeader>
        <CardTitle className="flex items-center gap-2">
          {queued ? (
            <CircleDashed className="size-4 text-muted-foreground" />
          ) : (
            <Spinner />
          )}
          {queued ? "排队等待识别" : "模型正在识别图片"}
        </CardTitle>
        <CardDescription>
          {queued
            ? review.queue_position !== null && review.queue_position > 1
              ? `前面还有 ${formatNumber(review.queue_position - 1)} 个审图任务，训练节点按顺序识别。`
              : "等待训练节点领取。"
            : `${reviewStageLabels[review.stage] || "正在识别"}${review.node ? `（训练节点 ${review.node.name}）` : ""}。识别完成的图片可以立即开始审核。`}
        </CardDescription>
      </CardHeader>
      <CardContent className="grid gap-3">
        <div className="grid gap-1.5">
          <TaskProgress
            value={
              queued && counts.inferred === 0
                ? null
                : percentOf(counts.inferred, counts.total)
            }
            label="识别进度"
          />
          <p className="flex flex-wrap justify-between gap-x-3 text-xs text-muted-foreground tabular-nums">
            <span>
              已识别 {formatNumber(counts.inferred)} /{" "}
              {formatNumber(counts.total)}
              {counts.inference_failed > 0 &&
                `（${formatNumber(counts.inference_failed)} 张无法识别）`}
            </span>
            {review.attempts > 1 && (
              <span>第 {formatNumber(review.attempts)} 次尝试</span>
            )}
          </p>
        </div>
        {queued && review.workers_online === 0 && (
          <Alert className="border-amber-500/40 [&>svg]:text-amber-600 dark:[&>svg]:text-amber-500">
            <TriangleAlert />
            <AlertTitle>没有可以识别的训练节点</AlertTitle>
            <AlertDescription>
              <p>
                审图识别由训练节点执行，节点需要装有 PyTorch 与 Ultralytics（有
                GPU 时更快）。节点上线后会自动开始识别。
              </p>
              <Link
                to="/training/compute"
                className="text-foreground underline underline-offset-4"
              >
                查看计算资源
              </Link>
            </AlertDescription>
          </Alert>
        )}
      </CardContent>
    </Card>
  )
}

function ActiveMerge({
  merge,
  onShow,
}: {
  merge: ReviewMerge
  onShow: () => void
}) {
  return (
    <Alert>
      <GitMerge />
      <AlertTitle>正在合并到数据集</AlertTitle>
      <AlertDescription>
        <div className="grid w-full gap-1.5">
          <TaskProgress
            value={merge.total > 0 ? percentOf(merge.done, merge.total) : null}
            label="合并进度"
          />
          <div className="flex flex-wrap items-center justify-between gap-2">
            <span className="tabular-nums">
              已处理 {formatNumber(merge.done)} / {formatNumber(merge.total)} 张
            </span>
            <Button
              size="xs"
              variant="ghost"
              onClick={onShow}
              className="text-foreground"
            >
              查看进度
            </Button>
          </div>
        </div>
      </AlertDescription>
    </Alert>
  )
}

function ReviewCountsGrid({ review }: { review: Review }) {
  const { counts } = review
  const reviewed = counts.approved + counts.needs_sample
  return (
    <div className="grid grid-cols-2 gap-4 md:grid-cols-3 xl:grid-cols-6">
      <StatCard
        title="待审核"
        value={formatNumber(counts.pending)}
        icon={CircleDashed}
      >
        <ReviewStatusBar counts={counts} />
        <span className="tabular-nums">
          已审核 {formatPercent(percentOf(reviewed, counts.total))}
        </span>
      </StatCard>
      <StatCard
        title="已审核"
        value={formatNumber(counts.approved)}
        icon={CircleCheck}
      >
        <span>识别正确，不需要再学习</span>
      </StatCard>
      <StatCard
        title="需加样"
        value={formatNumber(counts.needs_sample)}
        icon={ImagePlus}
      >
        <span>参与后续的训练</span>
      </StatCard>
      <StatCard
        title="已修改"
        value={formatNumber(counts.edited)}
        icon={PencilLine}
      >
        <span>框与模型的识别结果不同</span>
      </StatCard>
      <StatCard
        title="识别失败"
        value={formatNumber(counts.inference_failed)}
        icon={SquareX}
      >
        <span className="tabular-nums">
          已识别 {formatNumber(counts.inferred)} / {formatNumber(counts.total)}
        </span>
      </StatCard>
      <StatCard
        title="已合并"
        value={formatNumber(counts.merged)}
        icon={GitMerge}
      >
        <span className="truncate">
          {review.last_merge
            ? `${formatRelativeTime(review.last_merge.finished_at ?? review.last_merge.created_at)}合并`
            : "还没有合并到数据集"}
        </span>
      </StatCard>
    </div>
  )
}

/** 每个数据集的图片数与入口（只审这个数据集、只合并到这个数据集）。 */
function DatasetsCard({
  review,
  onMerge,
}: {
  review: Review
  onMerge: (datasetId: number) => void
}) {
  const wide = useMediaQuery("(min-width: 48rem)")
  return (
    <Card className="min-w-0">
      <CardHeader>
        <CardTitle className="flex items-center gap-2">
          数据集
          <span className="text-sm font-normal text-muted-foreground tabular-nums">
            {formatNumber(review.datasets.length)}
          </span>
        </CardTitle>
        <CardDescription>
          图片按数据集的顺序排列；可以只审核或只合并其中一个数据集。
        </CardDescription>
      </CardHeader>
      <CardContent>
        {review.datasets.length === 0 ? (
          <p className="rounded-lg border border-dashed px-4 py-6 text-center text-sm text-muted-foreground">
            任务中的数据集都已被删除。
          </p>
        ) : wide ? (
          <div className="overflow-hidden rounded-lg border">
            <Table>
              <TableHeader>
                <TableRow className="hover:bg-transparent">
                  <TableHead className="pl-4">数据集</TableHead>
                  <TableHead className="text-right">图片</TableHead>
                  <TableHead className="text-right">待审核</TableHead>
                  <TableHead className="text-right">已审核</TableHead>
                  <TableHead className="text-right">需加样</TableHead>
                  <TableHead className="hidden text-right lg:table-cell">
                    已修改
                  </TableHead>
                  <TableHead className="hidden text-right lg:table-cell">
                    已合并
                  </TableHead>
                  <TableHead className="pr-4">
                    <span className="sr-only">操作</span>
                  </TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {review.datasets.map((dataset) => (
                  <TableRow key={dataset.id}>
                    <TableCell className="w-full max-w-0 pl-4">
                      <div className="grid min-w-0 gap-1.5">
                        <Link
                          to={`/datasets/${dataset.id}?tab=reviews`}
                          className="truncate font-medium underline-offset-4 outline-none hover:underline focus-visible:underline"
                          title={dataset.name}
                        >
                          {dataset.name}
                        </Link>
                        <ReviewStatusBar
                          counts={dataset.counts}
                          className="max-w-48"
                        />
                      </div>
                    </TableCell>
                    <CountCell value={dataset.counts.total} />
                    <CountCell value={dataset.counts.pending} />
                    <CountCell value={dataset.counts.approved} />
                    <CountCell value={dataset.counts.needs_sample} />
                    <CountCell
                      value={dataset.counts.edited}
                      className="hidden lg:table-cell"
                    />
                    <CountCell
                      value={dataset.counts.merged}
                      className="hidden lg:table-cell"
                    />
                    <TableCell className="pr-4">
                      <DatasetActions
                        review={review}
                        dataset={dataset}
                        onMerge={onMerge}
                      />
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </div>
        ) : (
          <ul className="divide-y rounded-lg border">
            {review.datasets.map((dataset) => (
              <li key={dataset.id} className="grid gap-2 p-3">
                <Link
                  to={`/datasets/${dataset.id}?tab=reviews`}
                  className="truncate text-sm font-medium underline-offset-4 hover:underline"
                >
                  {dataset.name}
                </Link>
                <ReviewStatusBar counts={dataset.counts} />
                <ReviewStatusLegend counts={dataset.counts} />
                <p className="text-xs text-muted-foreground tabular-nums">
                  共 {formatNumber(dataset.counts.total)} 张 · 已修改{" "}
                  {formatNumber(dataset.counts.edited)} · 已合并{" "}
                  {formatNumber(dataset.counts.merged)}
                </p>
                <DatasetActions
                  review={review}
                  dataset={dataset}
                  onMerge={onMerge}
                  className="justify-start"
                />
              </li>
            ))}
          </ul>
        )}
      </CardContent>
    </Card>
  )
}

function CountCell({
  value,
  className,
}: {
  value: number
  className?: string
}) {
  return (
    <TableCell
      className={`text-right whitespace-nowrap tabular-nums ${className ?? ""}`}
    >
      {formatNumber(value)}
    </TableCell>
  )
}

function DatasetActions({
  review,
  dataset,
  onMerge,
  className,
}: {
  review: Review
  dataset: ReviewDataset
  onMerge: (datasetId: number) => void
  className?: string
}) {
  const canReview = dataset.counts.inferred > 0
  const mergeable =
    review.active_merge === null &&
    inferredImageCount(dataset.counts, ["approved", "needs_sample"]) > 0
  return (
    <div
      className={`flex shrink-0 items-center justify-end gap-1 ${className ?? ""}`}
    >
      {canReview ? (
        <Button variant="outline" size="sm" asChild>
          <Link to={reviewEntryPath(review, dataset.id)}>
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
        onClick={() => onMerge(dataset.id)}
        disabled={!mergeable}
        title={
          mergeable
            ? `合并到“${dataset.name}”`
            : review.active_merge
              ? "正在合并，请等合并完成"
              : "这个数据集还没有已审核或需加样的图片"
        }
      >
        <GitMerge />
        合并
      </Button>
    </div>
  )
}

/** 本地审图的四个步骤：下载程序、下载审图包、在程序中审图、上传结果。 */
function LocalReviewCard({
  review,
  onPackage,
  onUpload,
}: {
  review: Review
  onPackage: () => void
  onUpload: () => void
}) {
  const tool = useQuery(reviewToolQuery())
  const canDownload = review.counts.inferred > 0

  const downloadTool = () => {
    const info = tool.data
    if (!info?.available) {
      return
    }
    startDownload(REVIEW_TOOL_DOWNLOAD_URL, info.file_name)
    toast.success("已开始下载本地审图程序", {
      description: `${info.file_name}（${formatBytes(info.size_bytes)}）`,
    })
  }

  return (
    <Card className="min-w-0">
      <CardHeader>
        <CardTitle>本地审图</CardTitle>
        <CardDescription>
          网络不方便或图片很多时，可以下载到 Windows 电脑上离线审图。
        </CardDescription>
      </CardHeader>
      <CardContent>
        <ol className="grid gap-4">
          <Step
            index={1}
            title="下载本地审图程序（Windows）"
            description={
              tool.isPending
                ? "正在检查…"
                : tool.data?.available
                  ? `${tool.data.file_name} · ${formatBytes(tool.data.size_bytes)}${tool.data.version ? ` · ${tool.data.version}` : ""}，下载一次即可，不需要安装。`
                  : tool.data
                    ? "平台没有附带本地审图程序，请联系管理员。"
                    : "无法获取本地审图程序的信息。"
            }
          >
            <Button
              variant="outline"
              size="sm"
              onClick={downloadTool}
              disabled={!tool.data?.available}
            >
              <MonitorDown />
              下载程序
            </Button>
          </Step>
          <Step
            index={2}
            title="下载审图包"
            description="图片、审图标注与 review.json 的压缩包，可以只下载待审核的图片。"
          >
            <Button
              variant="outline"
              size="sm"
              onClick={onPackage}
              disabled={!canDownload}
            >
              <Download />
              下载审图包
            </Button>
          </Step>
          <Step
            index={3}
            title="在程序中审图并导出结果"
            description="运行程序后打开下载的审图包（也可以把 zip 拖到程序上），快捷键与网页审图相同；审完后点击“导出审图结果”。"
          />
          <Step
            index={4}
            title="上传审图结果"
            description={
              review.last_upload
                ? `上次上传：${formatRelativeTime(review.last_upload.created_at)}，${uploadSummaryText(review.last_upload.summary)}。`
                : "上传导出的“…-审图结果.zip”，平台读取其中的审核状态与修改过的标注。"
            }
          >
            <Button variant="outline" size="sm" onClick={onUpload}>
              <Upload />
              上传审图结果
            </Button>
          </Step>
        </ol>
      </CardContent>
    </Card>
  )
}

function Step({
  index,
  title,
  description,
  children,
}: {
  index: number
  title: string
  description: string
  children?: ReactNode
}) {
  return (
    <li className="grid grid-cols-[1.75rem_minmax(0,1fr)] gap-3">
      <span className="flex size-7 items-center justify-center rounded-full border bg-muted text-xs font-medium tabular-nums">
        {index}
      </span>
      <div className="grid min-w-0 gap-2">
        <div className="grid gap-0.5">
          <p className="text-sm font-medium">{title}</p>
          <p className="text-xs break-words text-muted-foreground">
            {description}
          </p>
        </div>
        {children && <div className="flex flex-wrap gap-2">{children}</div>}
      </div>
    </li>
  )
}

function InferenceInfoCard({ review }: { review: Review }) {
  const report = inferenceReport(review.result)
  const speed = inferenceSpeedText(report)
  const { options } = review
  const nodeName = report?.nodeName ?? review.node?.name ?? null
  return (
    <Card className="min-w-0">
      <CardHeader>
        <CardTitle>识别信息</CardTitle>
        <CardDescription>{reviewOptionsText(options)}</CardDescription>
      </CardHeader>
      <CardContent>
        <dl className="grid gap-y-2 text-sm">
          <Fact label="模型">
            {review.model_exists ? (
              <Link
                to={`/models/${review.model.id}`}
                className="underline-offset-4 hover:underline"
              >
                {reviewModelTitle(review.model)}
              </Link>
            ) : (
              `${reviewModelTitle(review.model)}（已删除）`
            )}
            <span className="text-muted-foreground">
              {" "}
              · {reviewModelText(review.model)} ·{" "}
              {formatNumber(review.model.class_names.length)} 类
            </span>
          </Fact>
          <Fact label="置信度阈值">
            <span className="tabular-nums">{options.conf}</span>
          </Fact>
          <Fact label="IoU 阈值">
            <span className="tabular-nums">{options.iou}</span>
          </Fact>
          <Fact label="输入尺寸">
            <span className="tabular-nums">{imgSizeText(options.imgsz)}</span>
          </Fact>
          <Fact label="最多检测框数">
            <span className="tabular-nums">
              {formatNumber(options.max_det)}
            </span>
          </Fact>
          <Fact label="训练节点">
            {nodeName ? (
              <span className="inline-flex items-center gap-1">
                <Server className="size-3.5 text-muted-foreground" />
                {nodeName}
                {report?.device && (
                  <span className="text-muted-foreground">
                    （{report.device}）
                  </span>
                )}
              </span>
            ) : (
              <span className="text-muted-foreground">—</span>
            )}
          </Fact>
          {speed && <Fact label="识别用时">{speed}</Fact>}
          {(report?.ultralyticsVersion || report?.torchVersion) && (
            <Fact label="版本">
              <span className="font-mono text-xs">
                {[
                  report.ultralyticsVersion &&
                    `ultralytics ${report.ultralyticsVersion}`,
                  report.torchVersion && `torch ${report.torchVersion}`,
                ]
                  .filter(Boolean)
                  .join(" · ")}
              </span>
            </Fact>
          )}
          <Fact label="开始识别">
            {review.started_at ? (
              formatDateTime(review.started_at)
            ) : (
              <span className="text-muted-foreground">—</span>
            )}
          </Fact>
          <Fact label="完成">
            {review.status === "ready" && review.finished_at ? (
              formatDateTime(review.finished_at)
            ) : (
              <span className="text-muted-foreground">—</span>
            )}
          </Fact>
        </dl>
      </CardContent>
    </Card>
  )
}

function Fact({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="grid min-w-0 grid-cols-[6.5rem_1fr] items-baseline gap-3 sm:grid-cols-[7.5rem_1fr]">
      <dt className="text-muted-foreground">{label}</dt>
      <dd className="min-w-0 break-words">{children}</dd>
    </div>
  )
}

function DetailSkeleton() {
  return (
    <div className="grid gap-6" aria-busy="true" aria-label="正在加载">
      <div className="grid gap-3">
        <Skeleton className="h-8 w-64" />
        <Skeleton className="h-5 w-96 max-w-full" />
      </div>
      <div className="grid grid-cols-2 gap-4 md:grid-cols-3 xl:grid-cols-6">
        {[0, 1, 2, 3, 4, 5].map((index) => (
          <Skeleton key={index} className="h-[118px] rounded-xl" />
        ))}
      </div>
      <Skeleton className="h-48 rounded-xl" />
      <div className="grid gap-6 lg:grid-cols-2">
        <Skeleton className="h-72 rounded-xl" />
        <Skeleton className="h-72 rounded-xl" />
      </div>
    </div>
  )
}
