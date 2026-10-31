import { useQuery } from "@tanstack/react-query"
import {
  FileArchive,
  Folder,
  History,
  ScanSearch,
  type LucideIcon,
} from "lucide-react"

import { Badge } from "@/components/ui/badge"
import { Card } from "@/components/ui/card"
import {
  Empty,
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
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@/components/ui/tooltip"
import {
  importsQuery,
  type Dataset,
  type DatasetImport,
  type ImportSource,
} from "@/features/datasets/api"
import {
  importFormatLabel,
  jobDurationSeconds,
  sourceLabels,
  statusLabels,
  summaryText,
} from "@/features/datasets/import-status"
import { LoadError } from "@/features/datasets/load-error"
import { formatDateTime, formatDuration, formatNumber } from "@/lib/format"

const sourceIcons: Record<ImportSource, LucideIcon> = {
  archive: FileArchive,
  folder: Folder,
  scan: ScanSearch,
}

/** YOLO 导入时使用的类别顺序，悬停在类型上时显示。 */
function classesTitle(job: DatasetImport) {
  if (job.annotation_format !== "yolo" || !job.classes?.length) {
    return undefined
  }
  return `类别顺序：${job.classes.map((name, index) => `${index} ${name}`).join("，")}`
}

function StatusBadge({ job }: { job: DatasetImport }) {
  const label = statusLabels[job.status]
  switch (job.status) {
    case "succeeded":
      return (
        <Badge variant="outline">
          <span className="size-1.5 rounded-full bg-emerald-500" />
          {label}
        </Badge>
      )
    case "failed":
      return (
        <Tooltip>
          <TooltipTrigger asChild>
            <Badge variant="destructive" tabIndex={0} className="cursor-help">
              {label}
            </Badge>
          </TooltipTrigger>
          <TooltipContent className="max-w-xs">
            {job.error || "导入失败"}
          </TooltipContent>
        </Tooltip>
      )
    case "canceled":
      return <Badge variant="secondary">{label}</Badge>
    default:
      return (
        <Badge variant="outline">
          <Spinner className="size-3" aria-hidden />
          {label}
        </Badge>
      )
  }
}

export function DatasetImports({ dataset }: { dataset: Dataset }) {
  const { data, error, isPending, isFetching, refetch } = useQuery(
    importsQuery(dataset.id)
  )

  if (isPending) {
    return <Skeleton className="h-64 w-full rounded-xl" />
  }
  if (error && data === undefined) {
    return (
      <LoadError
        title="无法获取导入记录"
        error={error}
        retrying={isFetching}
        onRetry={() => refetch()}
      />
    )
  }
  if (data.items.length === 0) {
    return (
      <Empty className="border">
        <EmptyHeader>
          <EmptyMedia variant="icon">
            <History />
          </EmptyMedia>
          <EmptyTitle className="text-base">还没有导入记录</EmptyTitle>
          <EmptyDescription>
            上传压缩包、文件夹或重新扫描后，这里会记录每次的来源与结果。
          </EmptyDescription>
        </EmptyHeader>
      </Empty>
    )
  }

  return (
    <div className="grid gap-2">
      <Card className="gap-0 overflow-hidden py-0">
        <Table>
          <TableHeader>
            <TableRow className="hover:bg-transparent">
              <TableHead className="pl-4">类型</TableHead>
              <TableHead className="hidden md:table-cell">来源</TableHead>
              <TableHead>状态</TableHead>
              <TableHead>结果</TableHead>
              <TableHead className="hidden lg:table-cell">开始时间</TableHead>
              <TableHead className="hidden pr-4 text-right sm:table-cell">
                耗时
              </TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {data.items.map((job) => {
              const Icon = sourceIcons[job.source]
              const duration = jobDurationSeconds(job)
              const summary = summaryText(job)
              const formatLabel = importFormatLabel(job)
              return (
                <TableRow key={job.id}>
                  <TableCell className="pl-4">
                    <span
                      className="inline-flex items-center gap-2"
                      title={classesTitle(job)}
                    >
                      <Icon className="size-4 text-muted-foreground" />
                      <span>
                        {sourceLabels[job.source]}
                        {formatLabel && (
                          <span className="text-muted-foreground">
                            {" · "}
                            {formatLabel}
                          </span>
                        )}
                      </span>
                    </span>
                  </TableCell>
                  <TableCell
                    className="hidden max-w-56 truncate md:table-cell"
                    title={job.file_name}
                  >
                    {job.file_name || (
                      <span className="text-muted-foreground">—</span>
                    )}
                  </TableCell>
                  <TableCell>
                    <StatusBadge job={job} />
                  </TableCell>
                  <TableCell className="max-w-[16rem] whitespace-normal text-muted-foreground sm:max-w-md">
                    <span className="line-clamp-2" title={summary}>
                      {summary || "—"}
                    </span>
                  </TableCell>
                  <TableCell className="hidden text-muted-foreground tabular-nums lg:table-cell">
                    {formatDateTime(job.started_at ?? job.created_at)}
                  </TableCell>
                  <TableCell className="hidden pr-4 text-right text-muted-foreground tabular-nums sm:table-cell">
                    {duration === null
                      ? "—"
                      : formatDuration(Math.round(duration))}
                  </TableCell>
                </TableRow>
              )
            })}
          </TableBody>
        </Table>
      </Card>
      {data.total > data.items.length && (
        <p className="text-xs text-muted-foreground">
          共 {formatNumber(data.total)} 条记录，显示最近 {data.items.length}{" "}
          条。
        </p>
      )}
    </div>
  )
}
