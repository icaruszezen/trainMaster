import type { MouseEvent } from "react"
import { Images } from "lucide-react"
import { Link, useNavigate } from "react-router"

import { Badge } from "@/components/ui/badge"
import { Progress } from "@/components/ui/progress"
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import type { Dataset } from "@/features/datasets/api"
import { DatasetActionsMenu } from "@/features/datasets/dataset-actions"
import {
  annotatedPercent,
  DatasetStatusBadges,
  originLabels,
} from "@/features/datasets/dataset-card"
import { DatasetThumbnail } from "@/features/datasets/dataset-thumbnail"
import {
  formatBytes,
  formatDateTime,
  formatNumber,
  formatPercent,
  formatRelativeTime,
} from "@/lib/format"

type DatasetTableProps = {
  datasets: Dataset[]
  onScan: (dataset: Dataset) => void
  onEdit: (dataset: Dataset) => void
  onDelete: (dataset: Dataset) => void
}

export function DatasetTable({
  datasets,
  onScan,
  onEdit,
  onDelete,
}: DatasetTableProps) {
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
    navigate(`/datasets/${id}`)
  }

  return (
    <div className="overflow-hidden rounded-xl border bg-card shadow-sm">
      <Table>
        <TableHeader>
          <TableRow className="hover:bg-transparent">
            <TableHead className="pl-4">名称</TableHead>
            <TableHead className="text-right">图片</TableHead>
            <TableHead className="hidden w-44 sm:table-cell">已标注</TableHead>
            <TableHead className="hidden text-right lg:table-cell">
              标签
            </TableHead>
            <TableHead className="hidden text-right md:table-cell">
              大小
            </TableHead>
            <TableHead className="hidden xl:table-cell">来源</TableHead>
            <TableHead className="hidden md:table-cell">更新时间</TableHead>
            <TableHead className="w-12 pr-4">
              <span className="sr-only">操作</span>
            </TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {datasets.map((dataset) => {
            const percent = annotatedPercent(dataset)
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
                <TableCell className="hidden xl:table-cell">
                  <Badge variant="outline" className="font-normal">
                    {originLabels[dataset.origin]}
                  </Badge>
                </TableCell>
                <TableCell
                  className="hidden text-muted-foreground md:table-cell"
                  title={formatDateTime(dataset.updated_at)}
                >
                  {formatRelativeTime(dataset.updated_at)}
                </TableCell>
                <TableCell className="pr-4 text-right">
                  <DatasetActionsMenu
                    dataset={dataset}
                    onScan={() => onScan(dataset)}
                    onEdit={() => onEdit(dataset)}
                    onDelete={() => onDelete(dataset)}
                  />
                </TableCell>
              </TableRow>
            )
          })}
        </TableBody>
      </Table>
    </div>
  )
}
