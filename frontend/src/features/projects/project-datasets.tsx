import type { MouseEvent } from "react"
import { useMutation, useQuery } from "@tanstack/react-query"
import { Database, FolderMinus, Images, Plus } from "lucide-react"
import { Link, useNavigate } from "react-router"
import { toast } from "sonner"

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
import type { Dataset } from "@/features/datasets/api"
import {
  annotatedPercent,
  DatasetStatusBadges,
} from "@/features/datasets/dataset-card"
import { DatasetThumbnail } from "@/features/datasets/dataset-thumbnail"
import { LoadError } from "@/features/datasets/load-error"
import {
  addProjectDatasets,
  projectDatasetsQuery,
  refreshMembership,
  removeProjectDataset,
  type Project,
} from "@/features/projects/api"
import {
  formatBytes,
  formatDateTime,
  formatNumber,
  formatPercent,
  formatRelativeTime,
} from "@/lib/format"

type ProjectDatasetsProps = {
  project: Project
  onAddDatasets: () => void
}

/** 项目中的数据集：每个数据集的图片、标注进度与大小，可以移出项目。 */
export function ProjectDatasets({
  project,
  onAddDatasets,
}: ProjectDatasetsProps) {
  const navigate = useNavigate()
  const { data, error, isPending, isFetching, refetch } = useQuery(
    projectDatasetsQuery(project.id)
  )

  const remove = useMutation({
    mutationFn: (dataset: Dataset) =>
      removeProjectDataset(project.id, dataset.id),
    onSuccess: (_, dataset) => {
      refreshMembership()
      toast(`已将“${dataset.name}”移出项目`, {
        description: "数据集本身不受影响。",
        action: {
          label: "撤销",
          onClick: () => {
            addProjectDatasets(project.id, [dataset.id]).then(
              () => refreshMembership(),
              (undoError: Error) =>
                toast.error("撤销失败", { description: undoError.message })
            )
          },
        },
      })
    },
    onError: (removeError, dataset) => {
      // 数据集或项目可能刚被删除：刷新后列表是最新的。
      refreshMembership()
      toast.error(`无法将“${dataset.name}”移出项目`, {
        description: removeError.message,
      })
    },
  })

  if (isPending) {
    return (
      <div className="overflow-hidden rounded-xl border">
        {[0, 1, 2].map((row) => (
          <div key={row} className="flex items-center gap-3 border-b p-4">
            <Skeleton className="size-10 rounded-md" />
            <div className="grid flex-1 gap-2">
              <Skeleton className="h-4 w-40" />
              <Skeleton className="h-3 w-64 max-w-full" />
            </div>
          </div>
        ))}
      </div>
    )
  }
  if (error && data === undefined) {
    return (
      <LoadError
        title="无法获取项目中的数据集"
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
            <Database />
          </EmptyMedia>
          <EmptyTitle className="text-base">项目中还没有数据集</EmptyTitle>
          <EmptyDescription>
            把已有的数据集添加到项目中，这里会汇总每个数据集的图片数量、标注进度与大小。
          </EmptyDescription>
        </EmptyHeader>
        <EmptyContent>
          <Button size="sm" onClick={onAddDatasets}>
            <Plus />
            添加数据集
          </Button>
        </EmptyContent>
      </Empty>
    )
  }

  // 点击行的空白处进入数据集；链接与按钮各自处理。
  const onRowClick = (event: MouseEvent<HTMLTableRowElement>, id: number) => {
    const target = event.target as HTMLElement
    if (!event.currentTarget.contains(target) || target.closest("a, button")) {
      return
    }
    navigate(`/datasets/${id}`)
  }

  return (
    <div className="overflow-hidden rounded-xl border bg-card shadow-sm">
      <Table>
        <TableHeader>
          <TableRow className="hover:bg-transparent">
            <TableHead className="pl-4">数据集</TableHead>
            <TableHead className="text-right">图片</TableHead>
            <TableHead className="hidden w-44 sm:table-cell">已标注</TableHead>
            <TableHead className="hidden text-right lg:table-cell">
              标签
            </TableHead>
            <TableHead className="hidden text-right md:table-cell">
              大小
            </TableHead>
            <TableHead className="hidden xl:table-cell">更新时间</TableHead>
            <TableHead className="w-12 pr-4">
              <span className="sr-only">操作</span>
            </TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {data.items.map((dataset) => {
            const percent = annotatedPercent(dataset)
            const removing =
              remove.isPending && remove.variables.id === dataset.id
            return (
              <TableRow
                key={dataset.id}
                className="cursor-pointer"
                onClick={(event) => onRowClick(event, dataset.id)}
              >
                <TableCell className="max-w-0 pl-4 sm:max-w-none">
                  <div className="flex min-w-0 items-center gap-3">
                    <div className="size-10 shrink-0 overflow-hidden rounded-md border bg-muted">
                      {dataset.covers[0] ? (
                        <DatasetThumbnail
                          datasetId={dataset.id}
                          image={dataset.covers[0]}
                          className="size-full"
                        />
                      ) : (
                        <div className="flex size-full items-center justify-center text-muted-foreground">
                          <Images className="size-4" />
                        </div>
                      )}
                    </div>
                    <div className="grid min-w-0 gap-1">
                      <Link
                        to={`/datasets/${dataset.id}`}
                        className="truncate font-medium underline-offset-4 outline-none hover:underline focus-visible:underline"
                      >
                        {dataset.name}
                      </Link>
                      {dataset.directory_missing || dataset.active_import ? (
                        <DatasetStatusBadges dataset={dataset} />
                      ) : (
                        <span className="max-w-xs truncate text-xs text-muted-foreground">
                          {dataset.description || dataset.dir_name}
                        </span>
                      )}
                    </div>
                  </div>
                </TableCell>
                <TableCell className="text-right tabular-nums">
                  {formatNumber(dataset.image_count)}
                </TableCell>
                <TableCell className="hidden sm:table-cell">
                  <div className="flex items-center gap-2">
                    <Progress
                      value={percent}
                      className="h-1 w-20"
                      aria-label={`已标注 ${formatPercent(percent)}`}
                    />
                    <span className="text-xs text-muted-foreground tabular-nums">
                      {formatPercent(percent)}
                    </span>
                  </div>
                </TableCell>
                <TableCell className="hidden text-right tabular-nums lg:table-cell">
                  {formatNumber(dataset.label_count)}
                </TableCell>
                <TableCell className="hidden text-right tabular-nums md:table-cell">
                  {formatBytes(dataset.total_bytes)}
                </TableCell>
                <TableCell
                  className="hidden text-muted-foreground xl:table-cell"
                  title={formatDateTime(dataset.updated_at)}
                >
                  {formatRelativeTime(dataset.updated_at)}
                </TableCell>
                <TableCell className="pr-4 text-right">
                  <Tooltip>
                    <TooltipTrigger asChild>
                      <Button
                        variant="ghost"
                        size="icon-sm"
                        onClick={() => remove.mutate(dataset)}
                        disabled={removing}
                        aria-label={`移出项目：${dataset.name}`}
                      >
                        {removing ? <Spinner aria-hidden /> : <FolderMinus />}
                      </Button>
                    </TooltipTrigger>
                    <TooltipContent>移出项目</TooltipContent>
                  </Tooltip>
                </TableCell>
              </TableRow>
            )
          })}
        </TableBody>
      </Table>
    </div>
  )
}
