import { FolderKanban, Tags } from "lucide-react"
import { Link } from "react-router"

import { Progress } from "@/components/ui/progress"
import { DatasetThumbnail } from "@/features/datasets/dataset-thumbnail"
import type { Project } from "@/features/projects/api"
import { ProjectActionsMenu } from "@/features/projects/project-dialogs"
import {
  formatBytes,
  formatNumber,
  formatPercent,
  formatRelativeTime,
  percentOf,
} from "@/lib/format"
import { cn } from "@/lib/utils"

/** 标注进度：有目标的图片占项目全部图片的比例。 */
export function projectAnnotatedPercent(project: Project) {
  return percentOf(project.annotated_count, project.image_count)
}

/** 卡片封面：≥ 4 张时 2×2 拼图，1–3 张时显示第一张，没有图片时显示占位。 */
function CoverMosaic({ project }: { project: Project }) {
  const { covers } = project
  const imageClass =
    "size-full transition-transform duration-500 ease-out group-hover/card:scale-[1.04] motion-reduce:transition-none"

  if (covers.length === 0) {
    return (
      <div className="flex size-full flex-col items-center justify-center gap-2 bg-gradient-to-br from-muted via-muted/60 to-background text-muted-foreground">
        <div className="flex size-11 items-center justify-center rounded-full border bg-background/80 shadow-xs">
          <FolderKanban className="size-5" />
        </div>
        <span className="text-xs">
          {project.dataset_count > 0 ? "数据集中暂无图片" : "暂无数据集"}
        </span>
      </div>
    )
  }
  if (covers.length < 4) {
    return (
      <DatasetThumbnail
        datasetId={covers[0].dataset_id}
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
            datasetId={cover.dataset_id}
            image={cover}
            className={imageClass}
          />
        </div>
      ))}
    </div>
  )
}

type ProjectCardProps = {
  project: Project
  onEdit: () => void
  onDelete: () => void
}

export function ProjectCard({ project, onEdit, onDelete }: ProjectCardProps) {
  const percent = projectAnnotatedPercent(project)

  return (
    <article className="group/card relative flex flex-col overflow-hidden rounded-xl border bg-card text-card-foreground shadow-sm transition-shadow hover:shadow-md has-[a:focus-visible]:ring-[3px] has-[a:focus-visible]:ring-ring/50">
      <div className="relative aspect-[16/9] overflow-hidden border-b bg-muted">
        <CoverMosaic project={project} />
      </div>
      <ProjectActionsMenu
        project={project}
        onEdit={onEdit}
        onDelete={onDelete}
        className="absolute top-2 right-2 z-[1] bg-background/80 shadow-xs backdrop-blur hover:bg-background"
      />

      <div className="flex flex-1 flex-col gap-4 p-4">
        <div className="grid gap-1">
          <h3 className="truncate leading-snug font-semibold">
            {/* 伸展的链接覆盖整张卡片；菜单按钮通过 z-[1] 浮在它上面（低于吸顶的页头）。 */}
            <Link
              to={`/projects/${project.id}`}
              className="outline-none after:absolute after:inset-0 after:content-['']"
            >
              {project.name}
            </Link>
          </h3>
          <p
            className={cn(
              "line-clamp-2 min-h-10 text-sm text-muted-foreground",
              !project.description && "text-muted-foreground/60"
            )}
          >
            {project.description || "暂无描述"}
          </p>
        </div>

        <dl className="grid grid-cols-3 gap-2 text-sm">
          <div className="grid gap-0.5">
            <dt className="text-xs text-muted-foreground">数据集</dt>
            <dd className="font-medium tabular-nums">
              {formatNumber(project.dataset_count)}
            </dd>
          </div>
          <div className="grid gap-0.5">
            <dt className="text-xs text-muted-foreground">图片</dt>
            <dd className="font-medium tabular-nums">
              {formatNumber(project.image_count)}
            </dd>
          </div>
          <div className="grid gap-0.5">
            <dt className="text-xs text-muted-foreground">大小</dt>
            <dd className="font-medium tabular-nums">
              {formatBytes(project.total_bytes)}
            </dd>
          </div>
        </dl>

        <div className="grid gap-1.5">
          <div className="flex items-center justify-between text-xs text-muted-foreground">
            <span>已标注</span>
            <span className="tabular-nums">
              {formatNumber(project.annotated_count)} 张 ·{" "}
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

        <div className="mt-auto flex items-center justify-between gap-2 border-t pt-3 text-xs text-muted-foreground">
          <span className="truncate">
            更新于 {formatRelativeTime(project.updated_at)}
          </span>
          <span className="inline-flex items-center gap-1 tabular-nums">
            <Tags className="size-3" />
            {formatNumber(project.object_count)} 个标注目标
          </span>
        </div>
      </div>
    </article>
  )
}
