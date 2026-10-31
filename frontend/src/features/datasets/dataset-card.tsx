import { FolderX, Images } from "lucide-react"
import { Link } from "react-router"

import { Badge } from "@/components/ui/badge"
import { Progress } from "@/components/ui/progress"
import { Spinner } from "@/components/ui/spinner"
import type { Dataset, DatasetImport } from "@/features/datasets/api"
import { DatasetActionsMenu } from "@/features/datasets/dataset-actions"
import { DatasetThumbnail } from "@/features/datasets/dataset-thumbnail"
import { jobPercent, stageLabel } from "@/features/datasets/import-status"
import { DatasetProjectBadges } from "@/features/projects/dataset-project-badges"
import {
  formatBytes,
  formatNumber,
  formatPercent,
  formatRelativeTime,
  percentOf,
} from "@/lib/format"
import { cn } from "@/lib/utils"

export const originLabels: Record<Dataset["origin"], string> = {
  upload: "上传导入",
  directory: "目录添加",
}

/** 标注进度：有目标的图片占全部图片的比例。 */
export function annotatedPercent(dataset: Dataset) {
  return percentOf(dataset.annotated_count, dataset.image_count)
}

/** 进行中的导入任务的简短状态，如“正在导入 45%”。 */
export function activeImportText(job: DatasetImport) {
  const percent = jobPercent(job)
  const stage = job.status === "uploading" ? "上传中" : stageLabel(job)
  return percent === null ? stage : `${stage} ${formatPercent(percent)}`
}

export function DatasetStatusBadges({
  dataset,
  className,
}: {
  dataset: Dataset
  className?: string
}) {
  if (!dataset.directory_missing && !dataset.active_import) {
    return null
  }
  return (
    <div className={cn("flex flex-wrap gap-1.5", className)}>
      {dataset.directory_missing && (
        <Badge variant="destructive">
          <FolderX />
          文件夹缺失
        </Badge>
      )}
      {dataset.active_import && (
        <Badge
          variant="outline"
          className="bg-background/90 tabular-nums backdrop-blur"
        >
          <Spinner className="size-3" aria-hidden />
          {activeImportText(dataset.active_import)}
        </Badge>
      )}
    </div>
  )
}

/** 卡片封面：≥ 4 张时 2×2 拼图，1–3 张时显示第一张，没有图片时显示占位。 */
function CoverMosaic({ dataset }: { dataset: Dataset }) {
  const { covers } = dataset
  const imageClass =
    "size-full transition-transform duration-500 ease-out group-hover/card:scale-[1.04] motion-reduce:transition-none"

  if (covers.length === 0) {
    return (
      <div className="flex size-full flex-col items-center justify-center gap-2 bg-gradient-to-br from-muted via-muted/60 to-background text-muted-foreground">
        <div className="flex size-11 items-center justify-center rounded-full border bg-background/80 shadow-xs">
          <Images className="size-5" />
        </div>
        <span className="text-xs">暂无图片</span>
      </div>
    )
  }
  if (covers.length < 4) {
    return (
      <DatasetThumbnail
        datasetId={dataset.id}
        image={covers[0]}
        className={imageClass}
      />
    )
  }
  return (
    <div className="grid size-full grid-cols-2 grid-rows-2 gap-0.5">
      {covers.slice(0, 4).map((cover) => (
        <div key={cover.id} className="overflow-hidden bg-muted">
          <DatasetThumbnail
            datasetId={dataset.id}
            image={cover}
            className={imageClass}
          />
        </div>
      ))}
    </div>
  )
}

type DatasetCardProps = {
  dataset: Dataset
  onScan: () => void
  onEdit: () => void
  onDelete: () => void
  onManageProjects: () => void
  onDownload: () => void
}

export function DatasetCard({
  dataset,
  onScan,
  onDownload,
  onEdit,
  onDelete,
  onManageProjects,
}: DatasetCardProps) {
  const percent = annotatedPercent(dataset)

  return (
    <article className="group/card relative flex flex-col overflow-hidden rounded-xl border bg-card text-card-foreground shadow-sm transition-shadow hover:shadow-md has-[a:focus-visible]:ring-[3px] has-[a:focus-visible]:ring-ring/50">
      <div className="relative aspect-[16/9] overflow-hidden border-b bg-muted">
        <CoverMosaic dataset={dataset} />
        <DatasetStatusBadges
          dataset={dataset}
          className="absolute top-2.5 left-2.5 max-w-[calc(100%-3.5rem)]"
        />
      </div>
      <DatasetActionsMenu
        dataset={dataset}
        onScan={onScan}
        onDownload={onDownload}
        onEdit={onEdit}
        onDelete={onDelete}
        onManageProjects={onManageProjects}
        className="absolute top-2 right-2 z-[1] bg-background/80 shadow-xs backdrop-blur hover:bg-background"
      />

      <div className="flex flex-1 flex-col gap-4 p-4">
        <div className="grid gap-1">
          <h3 className="truncate leading-snug font-semibold">
            {/* 伸展的链接覆盖整张卡片；菜单按钮通过 z-[1] 浮在它上面（低于吸顶的页头）。 */}
            <Link
              to={`/datasets/${dataset.id}`}
              className="outline-none after:absolute after:inset-0 after:content-['']"
            >
              {dataset.name}
            </Link>
          </h3>
          <p
            className={cn(
              "line-clamp-2 min-h-10 text-sm text-muted-foreground",
              !dataset.description && "text-muted-foreground/60"
            )}
          >
            {dataset.description || "暂无描述"}
          </p>
        </div>

        <dl className="grid grid-cols-3 gap-2 text-sm">
          <div className="grid gap-0.5">
            <dt className="text-xs text-muted-foreground">图片</dt>
            <dd className="font-medium tabular-nums">
              {formatNumber(dataset.image_count)}
            </dd>
          </div>
          <div className="grid gap-0.5">
            <dt className="text-xs text-muted-foreground">标签</dt>
            <dd className="font-medium tabular-nums">
              {formatNumber(dataset.label_count)}
            </dd>
          </div>
          <div className="grid gap-0.5">
            <dt className="text-xs text-muted-foreground">大小</dt>
            <dd className="font-medium tabular-nums">
              {formatBytes(dataset.total_bytes)}
            </dd>
          </div>
        </dl>

        <div className="grid gap-1.5">
          <div className="flex items-center justify-between text-xs text-muted-foreground">
            <span>已标注</span>
            <span className="tabular-nums">
              {formatNumber(dataset.annotated_count)} 张 ·{" "}
              <span className="font-medium text-foreground">
                {formatPercent(percent)}
              </span>
            </span>
          </div>
          <Progress
            value={percent}
            className="h-1"
            aria-label={`已标注 ${formatPercent(percent)}`}
          />
        </div>

        <DatasetProjectBadges projects={dataset.projects} />

        <div className="mt-auto flex items-center justify-between gap-2 border-t pt-3 text-xs text-muted-foreground">
          <span className="truncate">
            更新于 {formatRelativeTime(dataset.updated_at)}
          </span>
          <Badge variant="outline" className="font-normal">
            {originLabels[dataset.origin]}
          </Badge>
        </div>
      </div>
    </article>
  )
}
