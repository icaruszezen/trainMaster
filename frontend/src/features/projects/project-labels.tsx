import type { UseQueryResult } from "@tanstack/react-query"
import { Tags } from "lucide-react"

import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"
import {
  Empty,
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from "@/components/ui/empty"
import { Skeleton } from "@/components/ui/skeleton"
import { labelColor } from "@/features/datasets/label-colors"
import { LoadError } from "@/features/datasets/load-error"
import type { ProjectLabel } from "@/features/projects/api"
import type { ListResponse } from "@/lib/api"
import { formatNumber, formatPercent, percentOf } from "@/lib/format"

type ProjectLabelsProps = {
  /** 详情页已经发起的标签查询（标签页上的数量也来自它）。 */
  query: UseQueryResult<ListResponse<ProjectLabel>>
}

/** 项目中所有数据集合计的标签分布：每个标签的目标数、图片数与所在数据集数。 */
export function ProjectLabels({ query }: ProjectLabelsProps) {
  const { data, error, isPending, isFetching, refetch } = query

  if (isPending) {
    return (
      <Card>
        <CardContent className="grid gap-5">
          {[0, 1, 2, 3].map((row) => (
            <div key={row} className="grid gap-2">
              <div className="flex justify-between">
                <Skeleton className="h-4 w-28" />
                <Skeleton className="h-4 w-36" />
              </div>
              <Skeleton className="h-2 w-full" />
            </div>
          ))}
        </CardContent>
      </Card>
    )
  }
  if (error && data === undefined) {
    return (
      <LoadError
        title="无法获取标签分布"
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
            <Tags />
          </EmptyMedia>
          <EmptyTitle className="text-base">还没有标签</EmptyTitle>
          <EmptyDescription>
            项目中的数据集包含 VOC
            标注后，这里会合计所有数据集，按标签统计目标数量、图片数量与所在的数据集。
          </EmptyDescription>
        </EmptyHeader>
      </Empty>
    )
  }

  const totalObjects = data.items.reduce(
    (sum, label) => sum + label.object_count,
    0
  )

  return (
    <Card>
      <CardHeader>
        <CardTitle>标签分布</CardTitle>
        <CardDescription>
          项目中所有数据集合计 {formatNumber(data.total)} 种标签、
          {formatNumber(totalObjects)} 个标注目标。
        </CardDescription>
      </CardHeader>
      <CardContent className="px-3 sm:px-4">
        <ul className="grid gap-1">
          {data.items.map((label) => {
            const share = percentOf(label.object_count, totalObjects)
            const color = labelColor(label.name)
            const detail = `${formatNumber(label.object_count)} 个目标 · ${formatNumber(label.image_count)} 张图片 · ${formatNumber(label.dataset_count)} 个数据集`
            return (
              <li
                key={label.name}
                className="grid gap-2 rounded-lg px-3 py-2.5"
              >
                <div className="flex items-center gap-3">
                  <span
                    className="size-2.5 shrink-0 rounded-full"
                    style={{ backgroundColor: color }}
                    aria-hidden
                  />
                  <span className="min-w-0 flex-1 truncate text-sm font-medium">
                    {label.name}
                  </span>
                  <span className="hidden text-xs text-muted-foreground tabular-nums sm:inline">
                    {detail}
                  </span>
                  <span className="w-14 text-right text-sm font-medium tabular-nums">
                    {formatPercent(share, 1)}
                  </span>
                </div>
                <div className="h-2 overflow-hidden rounded-full bg-muted">
                  <div
                    className="h-full rounded-full transition-[width] duration-500"
                    style={{
                      width: `${Math.max(share, 0.5)}%`,
                      backgroundColor: color,
                    }}
                  />
                </div>
                <span className="text-xs text-muted-foreground tabular-nums sm:hidden">
                  {detail}
                </span>
              </li>
            )
          })}
        </ul>
      </CardContent>
    </Card>
  )
}
