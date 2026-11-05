/**
 * 审图任务的记录：上传的审图结果与合并到数据集，新的在前。每条记录可以展开
 * 查看各项计数与没有处理的图片。
 */
import { useState, type ReactNode } from "react"
import { useQuery } from "@tanstack/react-query"
import { ChevronDown, FileArchive, GitMerge } from "lucide-react"

import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"
import { Skeleton } from "@/components/ui/skeleton"
import { Spinner } from "@/components/ui/spinner"
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { LoadError } from "@/features/datasets/load-error"
import {
  reviewMergesQuery,
  reviewUploadsQuery,
  type Review,
  type ReviewMerge,
  type ReviewUpload,
} from "@/features/reviews/api"
import { MergeStatus } from "@/features/reviews/merge-dialog"
import {
  mergeDatasetsText,
  mergeOutcomeText,
  mergeSummaryText,
  statusesText,
  uploadSummaryText,
} from "@/features/reviews/review-format"
import { ProblemList } from "@/features/reviews/review-parts"
import { UploadSummaryGrid } from "@/features/reviews/upload-results-dialog"
import { formatDateTime, formatNumber, formatRelativeTime } from "@/lib/format"
import { cn } from "@/lib/utils"

type HistoryTab = "uploads" | "merges"

export function ReviewHistory({ review }: { review: Review }) {
  const uploads = useQuery(reviewUploadsQuery(review.id))
  const merges = useQuery(reviewMergesQuery(review.id))
  const [tab, setTab] = useState<HistoryTab | null>(null)
  // 默认显示有记录的那个（都有时显示上传记录）。
  const current: HistoryTab =
    tab ??
    ((uploads.data?.total ?? 0) === 0 && (merges.data?.total ?? 0) > 0
      ? "merges"
      : "uploads")

  return (
    <Card className="min-w-0">
      <CardHeader>
        <CardTitle>记录</CardTitle>
        <CardDescription>
          从本地审图程序上传的审图结果，以及合并到数据集的记录。
        </CardDescription>
      </CardHeader>
      <CardContent>
        <Tabs
          value={current}
          onValueChange={(value) => setTab(value as HistoryTab)}
          className="gap-3"
        >
          <TabsList>
            <TabsTrigger value="uploads" className="px-3">
              上传记录
              {uploads.data && (
                <span className="text-xs text-muted-foreground tabular-nums">
                  {formatNumber(uploads.data.total)}
                </span>
              )}
            </TabsTrigger>
            <TabsTrigger value="merges" className="px-3">
              合并记录
              {merges.data && (
                <span className="text-xs text-muted-foreground tabular-nums">
                  {formatNumber(merges.data.total)}
                </span>
              )}
            </TabsTrigger>
          </TabsList>
          <TabsContent value="uploads">
            {uploads.isPending ? (
              <HistorySkeleton />
            ) : uploads.data === undefined ? (
              <LoadError
                title="无法获取上传记录"
                error={uploads.error}
                retrying={uploads.isFetching}
                onRetry={() => uploads.refetch()}
              />
            ) : uploads.data.items.length === 0 ? (
              <EmptyHistory>
                还没有上传过审图结果。在本地审图程序中导出审图结果后，点击“上传审图结果”。
              </EmptyHistory>
            ) : (
              <HistoryList
                total={uploads.data.total}
                shown={uploads.data.items.length}
              >
                {uploads.data.items.map((upload) => (
                  <UploadEntry key={upload.id} upload={upload} />
                ))}
              </HistoryList>
            )}
          </TabsContent>
          <TabsContent value="merges">
            {merges.isPending ? (
              <HistorySkeleton />
            ) : merges.data === undefined ? (
              <LoadError
                title="无法获取合并记录"
                error={merges.error}
                retrying={merges.isFetching}
                onRetry={() => merges.refetch()}
              />
            ) : merges.data.items.length === 0 ? (
              <EmptyHistory>
                还没有合并过。审核完成后，点击“合并到数据集”把需加样（以及已审核）图片的标注写进数据集。
              </EmptyHistory>
            ) : (
              <HistoryList
                total={merges.data.total}
                shown={merges.data.items.length}
              >
                {merges.data.items.map((merge) => (
                  <MergeEntry key={merge.id} merge={merge} review={review} />
                ))}
              </HistoryList>
            )}
          </TabsContent>
        </Tabs>
      </CardContent>
    </Card>
  )
}

function HistorySkeleton() {
  return (
    <div className="grid gap-2">
      <Skeleton className="h-14 w-full" />
      <Skeleton className="h-14 w-full" />
    </div>
  )
}

function EmptyHistory({ children }: { children: ReactNode }) {
  return (
    <p className="rounded-lg border border-dashed px-4 py-6 text-center text-sm text-muted-foreground">
      {children}
    </p>
  )
}

function HistoryList({
  total,
  shown,
  children,
}: {
  total: number
  shown: number
  children: ReactNode
}) {
  return (
    <div className="grid gap-2">
      <ul className="divide-y rounded-lg border">{children}</ul>
      {total > shown && (
        <p className="text-xs text-muted-foreground">
          共 {formatNumber(total)} 条记录，显示最近 {formatNumber(shown)} 条。
        </p>
      )}
    </div>
  )
}

/** 一条记录：图标、标题、说明与可展开的详情。 */
function Entry({
  icon,
  title,
  badges,
  summary,
  time,
  children,
}: {
  icon: ReactNode
  title: ReactNode
  badges?: ReactNode
  summary: ReactNode
  time: string
  children: ReactNode
}) {
  const [open, setOpen] = useState(false)
  return (
    <li className="grid min-w-0 gap-3 p-3">
      <div className="flex min-w-0 items-start gap-3">
        <div className="flex size-8 shrink-0 items-center justify-center rounded-md border bg-muted text-muted-foreground">
          {icon}
        </div>
        <div className="grid min-w-0 flex-1 gap-0.5">
          <div className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1">
            <span className="min-w-0 truncate text-sm font-medium">
              {title}
            </span>
            {badges}
          </div>
          <p className="text-xs break-words text-muted-foreground">{summary}</p>
          <p
            className="text-xs text-muted-foreground"
            title={formatDateTime(time, { withSeconds: true })}
          >
            {formatRelativeTime(time)}
          </p>
        </div>
        <Button
          variant="ghost"
          size="icon-sm"
          onClick={() => setOpen((value) => !value)}
          aria-expanded={open}
          aria-label={open ? "收起详情" : "查看详情"}
          className="shrink-0"
        >
          <ChevronDown
            className={cn("transition-transform", open && "rotate-180")}
          />
        </Button>
      </div>
      {open && <div className="min-w-0 sm:pl-11">{children}</div>}
    </li>
  )
}

function UploadEntry({ upload }: { upload: ReviewUpload }) {
  const { summary } = upload
  const problems = summary.conflicts + summary.invalid + summary.unknown
  return (
    <Entry
      icon={<FileArchive className="size-4" />}
      title={<span title={upload.file_name}>{upload.file_name}</span>}
      badges={
        <>
          {upload.overwrite && (
            <Badge variant="outline" className="font-normal">
              覆盖平台上的修改
            </Badge>
          )}
          {problems > 0 && (
            <Badge
              variant="outline"
              className="border-amber-500/40 font-normal text-amber-700 dark:text-amber-500"
            >
              {formatNumber(problems)} 张没有应用
            </Badge>
          )}
        </>
      }
      summary={`共 ${formatNumber(summary.images)} 张：${uploadSummaryText(summary)}`}
      time={upload.created_at}
    >
      <div className="grid min-w-0 gap-3">
        <UploadSummaryGrid upload={upload} />
        <ProblemList problems={upload.problems} total={problems} />
      </div>
    </Entry>
  )
}

function MergeEntry({ merge, review }: { merge: ReviewMerge; review: Review }) {
  const note = merge.note ? ` · 备注：${shortNote(merge.note)}` : ""
  return (
    <Entry
      icon={<GitMerge className="size-4" />}
      title={`${statusesText(merge.statuses)} → ${mergeDatasetsText(merge, review.datasets)}`}
      badges={<MergeStatusBadge merge={merge} />}
      summary={
        merge.status === "running"
          ? `已处理 ${formatNumber(merge.done)} / ${formatNumber(merge.total)} 张${note}`
          : merge.status === "failed" && merge.on_existing === null
            ? `已处理 ${formatNumber(merge.done)} / ${formatNumber(merge.total)} 张后中断 · 没有生成版本${note}`
            : `${mergeSummaryText(merge.summary)} · ${mergeOutcomeText(merge, review.datasets)}${note}`
      }
      time={merge.finished_at ?? merge.created_at}
    >
      <MergeStatus merge={merge} datasets={review.datasets} />
    </Entry>
  )
}

/** 摘要中的备注：一行，最多 60 个字符（完整的备注在展开后的结果中）。 */
function shortNote(note: string) {
  const chars = [...note.replace(/\s+/g, " ")]
  return chars.length > 60 ? `${chars.slice(0, 60).join("")}…` : chars.join("")
}

export function MergeStatusBadge({ merge }: { merge: ReviewMerge }) {
  switch (merge.status) {
    case "running":
      return (
        <Badge variant="secondary" className="font-normal tabular-nums">
          <Spinner />
          合并中
        </Badge>
      )
    case "failed":
      return (
        <Badge
          variant="destructive"
          className="font-normal"
          title={merge.error || undefined}
        >
          中断
        </Badge>
      )
    default:
      return (
        <Badge variant="outline" className="font-normal">
          <span className="size-1.5 rounded-full bg-emerald-500" />
          完成
        </Badge>
      )
  }
}
