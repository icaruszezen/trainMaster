import { useQuery } from "@tanstack/react-query"
import { ChevronRight, Tags } from "lucide-react"

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
import { labelsQuery, type Dataset } from "@/features/datasets/api"
import { labelColor } from "@/features/datasets/label-colors"
import { LoadError } from "@/features/datasets/load-error"
import { formatNumber, formatPercent, percentOf } from "@/lib/format"

type DatasetLabelsProps = {
  dataset: Dataset
  /** 点击标签：切换到图片页并按该标签筛选。 */
  onSelectLabel: (name: string) => void
}

export function DatasetLabels({ dataset, onSelectLabel }: DatasetLabelsProps) {
  const { data, error, isPending, isFetching, refetch } = useQuery(
    labelsQuery(dataset.id)
  )

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
            导入带标注（VOC、X-AnyLabeling 或
            YOLO）的图片后，这里会按标签统计目标数量与图片数量。
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
          共 {formatNumber(data.total)} 种标签、{formatNumber(totalObjects)}{" "}
          个标注目标。点击标签查看对应的图片。
        </CardDescription>
      </CardHeader>
      <CardContent className="px-3 sm:px-4">
        <ul className="grid gap-1">
          {data.items.map((label) => {
            const share = percentOf(label.object_count, totalObjects)
            const color = labelColor(label.name)
            return (
              <li key={label.name}>
                <button
                  type="button"
                  onClick={() => onSelectLabel(label.name)}
                  className="group grid w-full gap-2 rounded-lg px-3 py-2.5 text-left transition-colors outline-none hover:bg-muted/60 focus-visible:ring-[3px] focus-visible:ring-ring/50"
                  aria-label={`查看标签 ${label.name} 的图片`}
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
                      {formatNumber(label.object_count)} 个目标 ·{" "}
                      {formatNumber(label.image_count)} 张图片
                    </span>
                    <span className="w-14 text-right text-sm font-medium tabular-nums">
                      {formatPercent(share, 1)}
                    </span>
                    <ChevronRight className="size-4 text-muted-foreground opacity-0 transition-opacity group-hover:opacity-100 group-focus-visible:opacity-100" />
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
                    {formatNumber(label.object_count)} 个目标 ·{" "}
                    {formatNumber(label.image_count)} 张图片
                  </span>
                </button>
              </li>
            )
          })}
        </ul>
      </CardContent>
    </Card>
  )
}
