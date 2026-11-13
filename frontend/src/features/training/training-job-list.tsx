/**
 * 训练任务列表：桌面宽度显示表格，移动宽度显示卡片。
 * 训练任务主页（`/training/jobs`）与项目详情“训练任务”标签页共用。
 */
import type { MouseEvent } from "react"
import { Box, Rocket } from "lucide-react"
import { Link, useNavigate } from "react-router"

import { Badge } from "@/components/ui/badge"
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import { dataVersionDetailPath } from "@/features/data-versions/paths"
import type { TrainingJob } from "@/features/training/api"
import {
  formatMap,
  INIT_MODE_LABELS,
  trainingArchText,
  trainingJobTitle,
} from "@/features/training/format"
import { trainingJobDetailPath } from "@/features/training/paths"
import {
  TrainingJobActionsMenu,
  type TrainingJobActions,
} from "@/features/training/training-job-actions"
import {
  TrainingActivity,
  TrainingStatusBadge,
} from "@/features/training/training-status"
import { useMediaQuery } from "@/hooks/use-media-query"
import { formatDateTime, formatRelativeTime } from "@/lib/format"

type TrainingJobListProps = {
  jobs: TrainingJob[]
  actions: TrainingJobActions
  showProject?: boolean
}

export function TrainingJobList(props: TrainingJobListProps) {
  const wide = useMediaQuery("(min-width: 48rem)")
  return wide ? (
    <TrainingJobTable {...props} />
  ) : (
    <TrainingJobCards {...props} />
  )
}

function bestMap50Of(job: TrainingJob) {
  return job.metrics.best_map50 ?? job.metrics.map50
}

function bestMap5095Of(job: TrainingJob) {
  return job.metrics.best_map50_95 ?? job.metrics.map50_95
}

function TrainingJobTable({
  jobs,
  actions,
  showProject = true,
}: TrainingJobListProps) {
  const navigate = useNavigate()

  const onRowClick = (event: MouseEvent<HTMLTableRowElement>, id: number) => {
    const target = event.target as HTMLElement
    if (
      !event.currentTarget.contains(target) ||
      target.closest("a, button, [role=menuitem]")
    ) {
      return
    }
    navigate(trainingJobDetailPath(id))
  }

  return (
    <div className="overflow-hidden rounded-xl border bg-card shadow-sm">
      <Table>
        <TableHeader>
          <TableRow className="hover:bg-transparent">
            <TableHead className="pl-4">训练任务</TableHead>
            {showProject && (
              <TableHead className="hidden lg:table-cell">
                项目 / 数据版本
              </TableHead>
            )}
            {!showProject && (
              <TableHead className="hidden lg:table-cell">数据版本</TableHead>
            )}
            <TableHead>状态与进度</TableHead>
            <TableHead className="hidden text-right xl:table-cell">
              最佳 mAP50
            </TableHead>
            <TableHead className="hidden text-right xl:table-cell">
              产出模型
            </TableHead>
            <TableHead className="hidden xl:table-cell">创建时间</TableHead>
            <TableHead className="w-12 pr-4">
              <span className="sr-only">操作</span>
            </TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {jobs.map((job) => {
            const title = trainingJobTitle(job)
            return (
              <TableRow
                key={job.id}
                className="cursor-pointer"
                onClick={(event) => onRowClick(event, job.id)}
              >
                <TableCell className="w-full max-w-0 pl-4">
                  <div className="flex min-w-0 items-center gap-3">
                    <div className="flex size-9 shrink-0 items-center justify-center rounded-md border bg-muted text-muted-foreground">
                      <Rocket className="size-4" />
                    </div>
                    <div className="grid min-w-0 gap-1">
                      <Link
                        to={trainingJobDetailPath(job.id)}
                        className="truncate font-medium underline-offset-4 outline-none hover:underline focus-visible:underline"
                      >
                        {title}
                      </Link>
                      <div className="flex flex-wrap items-center gap-1.5 text-xs text-muted-foreground">
                        <span>{trainingArchText(job)}</span>
                        <span>·</span>
                        <span>{INIT_MODE_LABELS[job.init_mode]}</span>
                        {job.base_model && (
                          <>
                            <span>·</span>
                            <span>基于模型 v{job.base_model.version}</span>
                          </>
                        )}
                      </div>
                    </div>
                  </div>
                </TableCell>

                <TableCell className="hidden max-w-52 lg:table-cell">
                  <div className="grid min-w-0 gap-0.5 text-xs">
                    {showProject && (
                      <span
                        className="truncate font-medium text-foreground"
                        title={job.project_name}
                      >
                        {job.project_name}
                      </span>
                    )}
                    {job.data_version_live ? (
                      <Link
                        to={dataVersionDetailPath(job.data_version_id)}
                        className="w-fit truncate text-muted-foreground underline-offset-4 hover:text-foreground hover:underline"
                      >
                        数据版本 v{job.data_version.version}
                      </Link>
                    ) : (
                      <span className="truncate text-muted-foreground">
                        数据版本 v{job.data_version.version}（已移入回收站）
                      </span>
                    )}
                  </div>
                </TableCell>

                <TableCell>
                  <div className="grid w-44 gap-1.5 lg:w-52">
                    <TrainingStatusBadge job={job} className="w-fit" />
                    {job.status === "failed" && job.error ? (
                      <p
                        className="truncate text-xs text-destructive"
                        title={job.error}
                      >
                        {job.error}
                      </p>
                    ) : (
                      <TrainingActivity job={job} />
                    )}
                  </div>
                </TableCell>

                <TableCell className="hidden text-right tabular-nums xl:table-cell">
                  <div className="grid gap-0.5">
                    <span className="font-medium">
                      {formatMap(bestMap50Of(job))}
                    </span>
                    <span className="text-xs text-muted-foreground">
                      50-95: {formatMap(bestMap5095Of(job))}
                    </span>
                  </div>
                </TableCell>

                <TableCell className="hidden text-right xl:table-cell">
                  {job.best_model ? (
                    <Link
                      to={`/models/${job.best_model.id}`}
                      className="inline-flex items-center gap-1 rounded-md border bg-muted/50 px-2 py-0.5 text-xs font-medium hover:bg-accent"
                    >
                      <Box className="size-3" />
                      模型 v{job.best_model.version}
                    </Link>
                  ) : job.last_model ? (
                    <Link
                      to={`/models/${job.last_model.id}`}
                      className="inline-flex items-center gap-1 rounded-md border bg-muted/50 px-2 py-0.5 text-xs font-medium hover:bg-accent"
                    >
                      <Box className="size-3" />
                      检查点 v{job.last_model.version}
                    </Link>
                  ) : (
                    <span className="text-xs text-muted-foreground">—</span>
                  )}
                </TableCell>

                <TableCell
                  className="hidden whitespace-nowrap text-muted-foreground xl:table-cell"
                  title={formatDateTime(job.created_at)}
                >
                  {formatRelativeTime(job.created_at)}
                </TableCell>

                <TableCell className="pr-4 text-right">
                  <TrainingJobActionsMenu job={job} actions={actions} />
                </TableCell>
              </TableRow>
            )
          })}
        </TableBody>
      </Table>
    </div>
  )
}

function TrainingJobCards({
  jobs,
  actions,
  showProject = true,
}: TrainingJobListProps) {
  return (
    <ul className="grid grid-cols-[minmax(0,1fr)] gap-3">
      {jobs.map((job) => {
        const title = trainingJobTitle(job)
        const bestMap = bestMap50Of(job)
        return (
          <li key={job.id}>
            <article className="relative grid grid-cols-[minmax(0,1fr)] gap-3 rounded-xl border bg-card p-4 text-card-foreground shadow-sm has-[a:focus-visible]:ring-[3px] has-[a:focus-visible]:ring-ring/50">
              <div className="flex items-start gap-3">
                <div className="flex size-9 shrink-0 items-center justify-center rounded-lg border bg-muted text-muted-foreground">
                  <Rocket className="size-4" />
                </div>
                <div className="grid min-w-0 flex-1 grid-cols-[minmax(0,1fr)] gap-0.5">
                  <h3 className="truncate leading-snug font-medium">
                    <Link
                      to={trainingJobDetailPath(job.id)}
                      className="outline-none after:absolute after:inset-0 after:content-['']"
                    >
                      {title}
                    </Link>
                  </h3>
                  <p className="truncate text-xs text-muted-foreground">
                    {showProject ? `${job.project_name} · ` : ""}
                    数据版本 v{job.data_version.version} ·{" "}
                    {trainingArchText(job)}
                  </p>
                </div>
                <TrainingJobActionsMenu
                  job={job}
                  actions={actions}
                  className="relative z-[1] -mt-1 -mr-2"
                />
              </div>

              <div className="flex flex-wrap items-center gap-1.5">
                <TrainingStatusBadge job={job} />
                <Badge variant="outline" className="font-normal">
                  {INIT_MODE_LABELS[job.init_mode]}
                </Badge>
                {job.best_model && (
                  <Badge variant="secondary" className="font-normal">
                    模型 v{job.best_model.version}
                  </Badge>
                )}
              </div>

              {job.status === "failed" && job.error && (
                <p className="line-clamp-2 text-xs break-words text-destructive">
                  {job.error}
                </p>
              )}

              <TrainingActivity job={job} />

              <div className="flex items-center justify-between gap-2 border-t pt-3 text-xs text-muted-foreground">
                <span className="truncate tabular-nums">
                  {bestMap !== undefined
                    ? `最佳 mAP50 ${formatMap(bestMap)}`
                    : `${job.total_epochs} 轮训练`}
                </span>
                <span
                  className="shrink-0"
                  title={formatDateTime(job.created_at)}
                >
                  {formatRelativeTime(job.created_at)}
                </span>
              </div>
            </article>
          </li>
        )
      })}
    </ul>
  )
}
