/**
 * 项目详情页“训练任务”标签页：展示该项目下的所有训练任务及发起训练入口。
 */
import { useQuery } from "@tanstack/react-query"
import { ArrowRight, Plus, Rocket } from "lucide-react"
import { Link } from "react-router"

import { Button } from "@/components/ui/button"
import {
  Empty,
  EmptyContent,
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from "@/components/ui/empty"
import { Skeleton } from "@/components/ui/skeleton"
import { LoadError } from "@/features/datasets/load-error"
import {
  isActiveTrainingJob,
  projectTrainingJobsQuery,
  type TrainingJob,
} from "@/features/training/api"
import { newTrainingJobPath, trainingJobsPath } from "@/features/training/paths"
import { useTrainingJobActions } from "@/features/training/training-job-actions"
import { TrainingJobList } from "@/features/training/training-job-list"
import { formatNumber } from "@/lib/format"

export function projectTrainingJobsSummary(jobs: readonly TrainingJob[]) {
  if (jobs.length === 0) {
    return "还没有训练任务"
  }
  const activeCount = jobs.filter(isActiveTrainingJob).length
  const succeededCount = jobs.filter((j) => j.status === "succeeded").length
  const parts = [`共 ${formatNumber(jobs.length)} 个`]
  if (activeCount > 0) {
    parts.push(`${formatNumber(activeCount)} 个进行中`)
  }
  if (succeededCount > 0) {
    parts.push(`${formatNumber(succeededCount)} 个已完成`)
  }
  return parts.join(" · ")
}

export function ProjectTrainingJobs({
  projectId,
  projectName,
}: {
  projectId: number
  projectName: string
}) {
  const { data, error, isPending, isFetching, refetch } = useQuery(
    projectTrainingJobsQuery(projectId)
  )
  const { actions, dialogs } = useTrainingJobActions()
  const createPath = newTrainingJobPath({ projectId })
  const jobs = data?.items ?? []

  let content
  if (isPending) {
    content = (
      <div
        className="overflow-hidden rounded-xl border"
        aria-busy="true"
        aria-label="正在加载"
      >
        {[0, 1, 2].map((row) => (
          <div key={row} className="flex items-center gap-3 border-b p-4">
            <Skeleton className="size-9 rounded-md" />
            <div className="grid flex-1 gap-2">
              <Skeleton className="h-4 w-32" />
              <Skeleton className="h-3 w-64 max-w-full" />
            </div>
          </div>
        ))}
      </div>
    )
  } else if (error && data === undefined) {
    content = (
      <LoadError
        title="无法获取项目的训练任务"
        error={error}
        retrying={isFetching}
        onRetry={() => refetch()}
      />
    )
  } else if (jobs.length === 0) {
    content = (
      <Empty className="border">
        <EmptyHeader className="max-w-lg">
          <EmptyMedia variant="icon">
            <Rocket />
          </EmptyMedia>
          <EmptyTitle className="text-base">项目中还没有训练任务</EmptyTitle>
          <EmptyDescription>
            选择“{projectName}”已生成的数据版本，配置 Ultralytics YOLO
            全量训练参数（从头训练、预训练微调或断点续训），训练产出的权重可自动注册到项目的模型仓库。
          </EmptyDescription>
        </EmptyHeader>
        <EmptyContent>
          <Button size="sm" asChild>
            <Link to={createPath}>
              <Plus />
              新建训练任务
            </Link>
          </Button>
        </EmptyContent>
      </Empty>
    )
  } else {
    content = (
      <div className="grid gap-3">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <p className="text-sm text-muted-foreground tabular-nums">
            {projectTrainingJobsSummary(jobs)}
          </p>
          <div className="flex flex-wrap items-center gap-2">
            <Button variant="ghost" size="sm" asChild>
              <Link to={trainingJobsPath(projectId)}>
                在训练任务中查看
                <ArrowRight />
              </Link>
            </Button>
            <Button size="sm" asChild>
              <Link to={createPath}>
                <Plus />
                新建训练任务
              </Link>
            </Button>
          </div>
        </div>
        <TrainingJobList jobs={jobs} actions={actions} showProject={false} />
      </div>
    )
  }

  return (
    <>
      {content}
      {dialogs}
    </>
  )
}
