import type { MouseEvent } from "react"
import { FolderKanban } from "lucide-react"
import { Link, useNavigate } from "react-router"

import { Progress } from "@/components/ui/progress"
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import { DatasetThumbnail } from "@/features/datasets/dataset-thumbnail"
import type { Project } from "@/features/projects/api"
import { projectAnnotatedPercent } from "@/features/projects/project-card"
import { ProjectActionsMenu } from "@/features/projects/project-dialogs"
import {
  formatBytes,
  formatDateTime,
  formatNumber,
  formatPercent,
  formatRelativeTime,
} from "@/lib/format"

type ProjectTableProps = {
  projects: Project[]
  onEdit: (project: Project) => void
  onDelete: (project: Project) => void
}

export function ProjectTable({
  projects,
  onEdit,
  onDelete,
}: ProjectTableProps) {
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
    navigate(`/projects/${id}`)
  }

  return (
    <div className="overflow-hidden rounded-xl border bg-card shadow-sm">
      <Table>
        <TableHeader>
          <TableRow className="hover:bg-transparent">
            <TableHead className="pl-4">名称</TableHead>
            <TableHead className="text-right">数据集</TableHead>
            <TableHead className="hidden text-right sm:table-cell">
              图片
            </TableHead>
            <TableHead className="hidden w-44 md:table-cell">已标注</TableHead>
            <TableHead className="hidden text-right lg:table-cell">
              大小
            </TableHead>
            <TableHead className="hidden md:table-cell">更新时间</TableHead>
            <TableHead className="w-12 pr-4">
              <span className="sr-only">操作</span>
            </TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {projects.map((project) => {
            const percent = projectAnnotatedPercent(project)
            const cover = project.covers[0]
            return (
              <TableRow
                key={project.id}
                className="cursor-pointer"
                onClick={(event) => onRowClick(event, project.id)}
              >
                <TableCell className="max-w-0 pl-4 sm:max-w-none">
                  <div className="flex min-w-0 items-center gap-3">
                    <div className="size-10 shrink-0 overflow-hidden rounded-md border bg-muted">
                      {cover ? (
                        <DatasetThumbnail
                          datasetId={cover.dataset_id}
                          image={cover}
                          className="size-full"
                        />
                      ) : (
                        <div className="flex size-full items-center justify-center text-muted-foreground">
                          <FolderKanban className="size-4" />
                        </div>
                      )}
                    </div>
                    <div className="grid min-w-0 gap-1">
                      <Link
                        to={`/projects/${project.id}`}
                        className="truncate font-medium underline-offset-4 outline-none hover:underline focus-visible:underline"
                      >
                        {project.name}
                      </Link>
                      <span className="max-w-xs truncate text-xs text-muted-foreground">
                        {project.description || "暂无描述"}
                      </span>
                    </div>
                  </div>
                </TableCell>
                <TableCell className="text-right tabular-nums">
                  {formatNumber(project.dataset_count)}
                </TableCell>
                <TableCell className="hidden text-right tabular-nums sm:table-cell">
                  {formatNumber(project.image_count)}
                </TableCell>
                <TableCell className="hidden md:table-cell">
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
                  {formatBytes(project.total_bytes)}
                </TableCell>
                <TableCell
                  className="hidden text-muted-foreground md:table-cell"
                  title={formatDateTime(project.updated_at)}
                >
                  {formatRelativeTime(project.updated_at)}
                </TableCell>
                <TableCell className="pr-4 text-right">
                  <ProjectActionsMenu
                    project={project}
                    onEdit={() => onEdit(project)}
                    onDelete={() => onDelete(project)}
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
