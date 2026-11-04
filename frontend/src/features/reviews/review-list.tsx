/**
 * 审图任务列表：桌面宽度为表格（窄一些时隐藏次要的列），手机宽度为卡片。
 */
import type { MouseEvent } from "react"
import { ScanEye, TriangleAlert } from "lucide-react"
import { Link, useNavigate } from "react-router"

import { Badge } from "@/components/ui/badge"
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import type { Review } from "@/features/reviews/api"
import { reviewDetailPath } from "@/features/reviews/params"
import {
  ReviewActionsMenu,
  type ReviewActions,
} from "@/features/reviews/review-actions"
import {
  datasetNamesText,
  reviewModelText,
  reviewModelTitle,
} from "@/features/reviews/review-format"
import {
  ReviewStatusBar,
  ReviewTaskStatusBadge,
  statusBarLabel,
  waitingForWorkers,
} from "@/features/reviews/review-parts"
import { useMediaQuery } from "@/hooks/use-media-query"
import {
  formatDateTime,
  formatNumber,
  formatPercent,
  formatRelativeTime,
  percentOf,
} from "@/lib/format"

type ReviewListProps = {
  reviews: Review[]
  actions: ReviewActions
}

export function ReviewList(props: ReviewListProps) {
  // 与 Tailwind 的 md: 一致：手机宽度显示卡片。
  const wide = useMediaQuery("(min-width: 48rem)")
  return wide ? <ReviewTable {...props} /> : <ReviewCards {...props} />
}

/** 模型的标题与版本，模型已被删除时带上标记。 */
function ModelCell({ review }: { review: Review }) {
  return (
    <span className="grid w-40 gap-0.5 xl:w-52">
      <span className="flex min-w-0 items-center gap-1.5">
        <span className="truncate" title={reviewModelTitle(review.model)}>
          {reviewModelTitle(review.model)}
        </span>
        {!review.model_exists && (
          <Badge variant="outline" className="shrink-0 px-1.5 py-0 font-normal">
            已删除
          </Badge>
        )}
      </span>
      <span className="truncate text-xs text-muted-foreground">
        {reviewModelText(review.model)}
      </span>
    </span>
  )
}

/** 审核进度：进度条与已审核的张数。 */
function ProgressCell({ review }: { review: Review }) {
  const { counts } = review
  const reviewed = counts.approved + counts.needs_sample
  return (
    <span className="grid w-full gap-1.5" title={statusBarLabel(counts)}>
      <ReviewStatusBar counts={counts} />
      <span className="text-xs text-muted-foreground tabular-nums">
        {formatNumber(reviewed)} / {formatNumber(counts.total)}（
        {formatPercent(percentOf(reviewed, counts.total))}）
        {counts.needs_sample > 0 &&
          ` · 需加样 ${formatNumber(counts.needs_sample)}`}
      </span>
    </span>
  )
}

function TaskStatusCell({ review }: { review: Review }) {
  return (
    // min-w-max：徽标带 overflow-hidden，不撑开列宽时会和下一列重叠。
    <span className="grid min-w-max justify-items-start gap-0.5">
      <ReviewTaskStatusBadge review={review} />
      {waitingForWorkers(review) && (
        <span className="text-xs text-amber-700 dark:text-amber-500">
          没有可以识别的训练节点
        </span>
      )}
      {review.active_merge && (
        <span className="text-xs text-muted-foreground tabular-nums">
          正在合并 {formatNumber(review.active_merge.done)} /{" "}
          {formatNumber(review.active_merge.total)}
        </span>
      )}
    </span>
  )
}

function ReviewTable({ reviews, actions }: ReviewListProps) {
  const navigate = useNavigate()

  // 点击行的空白处进入详情；链接、按钮与（经 portal 渲染的）菜单各自处理。
  const onRowClick = (event: MouseEvent<HTMLTableRowElement>, id: number) => {
    const target = event.target as HTMLElement
    if (
      !event.currentTarget.contains(target) ||
      target.closest("a, button, [role=menuitem]")
    ) {
      return
    }
    navigate(reviewDetailPath(id))
  }

  return (
    <div className="overflow-hidden rounded-xl border bg-card shadow-sm">
      <Table>
        <TableHeader>
          <TableRow className="hover:bg-transparent">
            <TableHead className="pl-4">名称</TableHead>
            <TableHead className="hidden lg:table-cell">模型</TableHead>
            <TableHead>识别</TableHead>
            <TableHead>审核进度</TableHead>
            <TableHead className="hidden xl:table-cell">创建时间</TableHead>
            <TableHead className="w-12 pr-4">
              <span className="sr-only">操作</span>
            </TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {reviews.map((review) => (
            <TableRow
              key={review.id}
              className="cursor-pointer"
              onClick={(event) => onRowClick(event, review.id)}
            >
              {/* 名称列占满剩余宽度，长名称截断而不是挤压其它列。 */}
              <TableCell className="w-full max-w-0 pl-4">
                <div className="flex min-w-0 items-center gap-3">
                  <div className="flex size-9 shrink-0 items-center justify-center rounded-md border bg-muted text-muted-foreground">
                    <ScanEye className="size-4" />
                  </div>
                  <div className="grid min-w-0 gap-1">
                    <Link
                      to={reviewDetailPath(review.id)}
                      className="truncate font-medium underline-offset-4 outline-none hover:underline focus-visible:underline"
                      title={review.name}
                    >
                      {review.name}
                    </Link>
                    <span
                      className="truncate text-xs text-muted-foreground"
                      title={review.datasets.map((d) => d.name).join("、")}
                    >
                      <span className="lg:hidden">
                        {reviewModelTitle(review.model)} ·{" "}
                      </span>
                      {datasetNamesText(review.datasets)} ·{" "}
                      {formatNumber(review.counts.total)} 张
                    </span>
                  </div>
                </div>
              </TableCell>
              <TableCell className="hidden lg:table-cell">
                <ModelCell review={review} />
              </TableCell>
              <TableCell className="whitespace-nowrap">
                <TaskStatusCell review={review} />
              </TableCell>
              <TableCell>
                <div className="w-36 lg:w-44">
                  <ProgressCell review={review} />
                </div>
              </TableCell>
              <TableCell
                className="hidden whitespace-nowrap text-muted-foreground xl:table-cell"
                title={formatDateTime(review.created_at)}
              >
                {formatRelativeTime(review.created_at)}
              </TableCell>
              <TableCell className="pr-4 text-right">
                <ReviewActionsMenu review={review} actions={actions} />
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </div>
  )
}

function ReviewCards({ reviews, actions }: ReviewListProps) {
  return (
    <ul className="grid grid-cols-[minmax(0,1fr)] gap-3">
      {reviews.map((review) => (
        <li key={review.id}>
          <article className="relative grid grid-cols-[minmax(0,1fr)] gap-3 rounded-xl border bg-card p-4 text-card-foreground shadow-sm has-[a:focus-visible]:ring-[3px] has-[a:focus-visible]:ring-ring/50">
            <div className="flex items-start gap-3">
              <div className="flex size-9 shrink-0 items-center justify-center rounded-lg border bg-muted text-muted-foreground">
                <ScanEye className="size-4" />
              </div>
              <div className="grid min-w-0 flex-1 gap-0.5">
                <h3 className="truncate leading-snug font-medium">
                  {/* 伸展的链接覆盖整张卡片；菜单按钮通过 z-[1] 浮在它上面。 */}
                  <Link
                    to={reviewDetailPath(review.id)}
                    className="outline-none after:absolute after:inset-0 after:content-['']"
                  >
                    {review.name}
                  </Link>
                </h3>
                <p className="truncate text-xs text-muted-foreground">
                  {reviewModelTitle(review.model)} ·{" "}
                  {reviewModelText(review.model)}
                  {!review.model_exists && "（模型已删除）"}
                </p>
              </div>
              <ReviewActionsMenu
                review={review}
                actions={actions}
                className="relative z-[1] -mt-1 -mr-2"
              />
            </div>
            <div className="flex flex-wrap items-center gap-1.5">
              <ReviewTaskStatusBadge review={review} />
              {waitingForWorkers(review) && (
                <Badge
                  variant="outline"
                  className="border-amber-500/40 font-normal text-amber-700 dark:text-amber-500"
                >
                  <TriangleAlert />
                  没有可以识别的训练节点
                </Badge>
              )}
              {review.active_merge && (
                <Badge variant="outline" className="font-normal tabular-nums">
                  正在合并 {formatNumber(review.active_merge.done)} /{" "}
                  {formatNumber(review.active_merge.total)}
                </Badge>
              )}
            </div>
            <ProgressCell review={review} />
            <div className="flex items-center justify-between gap-2 border-t pt-3 text-xs text-muted-foreground">
              <span className="truncate tabular-nums">
                {datasetNamesText(review.datasets)} ·{" "}
                {formatNumber(review.counts.total)} 张
              </span>
              <span
                className="shrink-0"
                title={formatDateTime(review.created_at)}
              >
                {formatRelativeTime(review.created_at)}
              </span>
            </div>
          </article>
        </li>
      ))}
    </ul>
  )
}
