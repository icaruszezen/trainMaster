/**
 * 导入记录中的“同名图片”明细：这次导入（或添加已有目录后的第一次扫描）中，
 * 与数据集中其它图片同名（不含扩展名、不区分大小写）的图片。内容不同的连同
 * 标注文件一起改名为 `名称_2`、`名称_3`…，内容完全相同的保留原名。
 */
import { useInfiniteQuery } from "@tanstack/react-query"
import { ArrowRight } from "lucide-react"

import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { Skeleton } from "@/components/ui/skeleton"
import { Spinner } from "@/components/ui/spinner"
import {
  importRenamesQuery,
  type DatasetImport,
  type ImageRename,
} from "@/features/datasets/api"
import { LoadError } from "@/features/datasets/load-error"
import { formatNumber } from "@/lib/format"
import { cn } from "@/lib/utils"

/** 路径的最后一段（文件名）。 */
function baseName(path: string) {
  return path.slice(path.lastIndexOf("/") + 1)
}

/** 一条明细的说明文字，如“与 cam1/0001.jpg 同名，标注一并改为 0001_2.xml”。 */
export function renameDetail(item: ImageRename): string {
  if (item.kind === "identical") {
    return `与 ${item.same_name_as} 同名且内容完全相同，保留原名`
  }
  if (item.annotation_path) {
    return `与 ${item.same_name_as} 同名，标注文件一并改为 ${item.annotation_path}`
  }
  return `与 ${item.same_name_as} 同名`
}

function RenameItem({ item }: { item: ImageRename }) {
  const renamed = item.kind === "renamed"
  return (
    <li className="grid min-w-0 gap-0.5 bg-card px-3 py-2">
      <span className="flex min-w-0 items-center gap-2">
        <Badge
          variant={renamed ? "outline" : "secondary"}
          className={cn(
            "shrink-0 px-1.5 py-0 font-normal",
            renamed &&
              "border-sky-600/40 text-sky-800 dark:border-sky-400/40 dark:text-sky-300"
          )}
        >
          {renamed ? "重命名" : "内容相同"}
        </Badge>
        <span
          className="flex min-w-0 items-center gap-1 font-mono"
          title={renamed ? `${item.original_path} → ${item.path}` : item.path}
        >
          <span className="min-w-0 truncate">{item.original_path}</span>
          {renamed && (
            <>
              <ArrowRight
                className="size-3 shrink-0 text-muted-foreground"
                aria-label="改名为"
              />
              <span className="shrink-0 font-medium">
                {baseName(item.path)}
              </span>
            </>
          )}
        </span>
      </span>
      <span className="break-all text-muted-foreground">
        {renameDetail(item)}
      </span>
    </li>
  )
}

function RenameList({ job }: { job: DatasetImport }) {
  const {
    data,
    error,
    isPending,
    isFetching,
    refetch,
    hasNextPage,
    fetchNextPage,
    isFetchingNextPage,
  } = useInfiniteQuery(importRenamesQuery(job.dataset_id, job.id))

  if (isPending) {
    return <Skeleton className="h-40 w-full rounded-md" />
  }
  if (error && data === undefined) {
    return (
      <LoadError
        title="无法获取同名图片明细"
        error={error}
        retrying={isFetching}
        onRetry={() => refetch()}
      />
    )
  }
  const items = data.pages.flatMap((page) => page.items)
  const total = data.pages[0]?.total ?? 0
  if (items.length === 0) {
    return (
      <p className="rounded-md border px-3 py-6 text-center text-sm text-muted-foreground">
        没有记录明细
      </p>
    )
  }
  return (
    <div className="grid min-w-0 gap-1.5">
      <ul className="grid max-h-[min(24rem,50dvh)] min-w-0 gap-px overflow-y-auto rounded-md border bg-border text-xs">
        {items.map((item) => (
          <RenameItem key={`${item.kind}:${item.original_path}`} item={item} />
        ))}
      </ul>
      <div className="flex flex-wrap items-center justify-between gap-2 text-xs text-muted-foreground">
        <span className="tabular-nums">
          {total > items.length
            ? `共 ${formatNumber(total)} 张，已显示 ${formatNumber(items.length)} 张`
            : `共 ${formatNumber(total)} 张`}
        </span>
        {hasNextPage && (
          <Button
            type="button"
            variant="ghost"
            size="xs"
            disabled={isFetchingNextPage}
            onClick={() => fetchNextPage()}
          >
            {isFetchingNextPage && <Spinner aria-hidden />}
            加载更多
          </Button>
        )}
      </div>
    </div>
  )
}

/** 重命名与内容相同的计数。 */
function RenameCounts({ job }: { job: DatasetImport }) {
  const items = [
    { label: "重命名", value: job.summary.images_renamed ?? 0 },
    { label: "内容相同", value: job.summary.images_identical ?? 0 },
  ]
  const failed = job.summary.images_rename_failed ?? 0
  if (failed > 0) {
    items.push({ label: "无法重命名", value: failed })
  }
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
          <dd className="text-base font-semibold tabular-nums">
            {formatNumber(item.value)}
          </dd>
        </div>
      ))}
    </dl>
  )
}

export function ImportRenamesDialog({
  job,
  open,
  onOpenChange,
}: {
  job: DatasetImport
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  const inPlace = job.source === "scan"
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[calc(100dvh-2rem)] overflow-y-auto sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>同名图片</DialogTitle>
          <DialogDescription>
            {inPlace ? "添加目录时" : "这次导入中"}
            ，名称（不含扩展名，不区分大小写）与数据集中其它图片相同的图片。内容不同的
            {inPlace ? "已在文件夹中" : ""}
            连同标注文件一起改名，在原名后加上
            _2、_3…；内容完全相同的是同一张图片，保留原名。
          </DialogDescription>
        </DialogHeader>
        <RenameCounts job={job} />
        {(job.summary.images_rename_failed ?? 0) > 0 && (
          <p className="text-sm text-muted-foreground">
            无法重命名的图片所在的文件夹不允许修改（如只读），保留了原名，详见服务日志。
          </p>
        )}
        <RenameList job={job} />
        <DialogFooter>
          <Button type="button" onClick={() => onOpenChange(false)}>
            完成
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
