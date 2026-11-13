/**
 * 训练任务主页（`/training/jobs`）：展示平台全部训练任务，支持按项目、状态、
 * YOLO 版本与关键字筛选，并提示离线训练节点状态。
 */
import { useState } from "react"
import { useQuery } from "@tanstack/react-query"
import {
  AlertTriangle,
  Cpu,
  Plus,
  Rocket,
  Search,
  SearchX,
  X,
} from "lucide-react"
import { Link, useSearchParams } from "react-router"

import { PageHeader } from "@/components/page-header"
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import {
  Empty,
  EmptyContent,
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from "@/components/ui/empty"
import { Input } from "@/components/ui/input"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { Skeleton } from "@/components/ui/skeleton"
import { Spinner } from "@/components/ui/spinner"
import { ProjectFilterSelect } from "@/features/data-versions/data-versions-page"
import { LoadError } from "@/features/datasets/load-error"
import type { YoloVersion } from "@/features/models/api"
import { yoloVersions } from "@/features/models/yolo-versions"
import { projectsQuery, type Project } from "@/features/projects/api"
import {
  isActiveTrainingJob,
  trainingJobsQuery,
  type TrainingStatus,
} from "@/features/training/api"
import { TRAINING_STATUS_LABELS } from "@/features/training/format"
import { newTrainingJobPath, parsePositiveId } from "@/features/training/paths"
import { useTrainingJobActions } from "@/features/training/training-job-actions"
import { TrainingJobList } from "@/features/training/training-job-list"
import { formatNumber } from "@/lib/format"

const STATUS_OPTIONS: readonly TrainingStatus[] = [
  "running",
  "queued",
  "succeeded",
  "failed",
  "canceled",
]

export const TRAINING_JOBS_DESCRIPTION =
  "基于项目冻结的数据版本发起 Ultralytics YOLO 模型训练：支持从头训练、预训练权重微调与检查点断点续训，开放全部官方训练参数可编辑，训练完成后可将最佳权重一键或自动注册到模型仓库。"

function useTrainingUrlFilters(projects: Project[] | undefined) {
  const [params, setParams] = useSearchParams()
  const parsedProject = parsePositiveId(params.get("project"))
  const project =
    parsedProject !== null
      ? projects?.find((p) => p.id === parsedProject)
      : undefined
  const projectId =
    parsedProject !== null && projects && !project ? null : parsedProject

  const rawStatus = params.get("status")
  const status = STATUS_OPTIONS.includes(rawStatus as TrainingStatus)
    ? (rawStatus as TrainingStatus)
    : null

  const rawYolo = params.get("yolo")
  const yoloVersion = yoloVersions.some((v) => v.value === rawYolo)
    ? (rawYolo as YoloVersion)
    : null

  const updateParam = (key: string, value: string | null) => {
    setParams(
      (prev) => {
        const next = new URLSearchParams(prev)
        if (value === null || value === "") {
          next.delete(key)
        } else {
          next.set(key, value)
        }
        return next
      },
      { replace: true }
    )
  }

  return {
    projectId,
    project,
    status,
    yoloVersion,
    setProjectId: (id: number | null) =>
      updateParam("project", id === null ? null : String(id)),
    setStatus: (next: TrainingStatus | null) => updateParam("status", next),
    setYoloVersion: (next: YoloVersion | null) => updateParam("yolo", next),
    resetAll: () => setParams(new URLSearchParams(), { replace: true }),
  }
}

export function TrainingJobsPage() {
  const projects = useQuery(projectsQuery())
  const urlFilters = useTrainingUrlFilters(projects.data?.items)
  const [keyword, setKeyword] = useState("")

  const list = useQuery(
    trainingJobsQuery({
      projectId: urlFilters.projectId,
      dataVersionId: null,
      status: urlFilters.status,
      yoloVersion: urlFilters.yoloVersion,
      q: keyword,
    })
  )
  const { actions, dialogs } = useTrainingJobActions()

  const items = list.data?.items ?? []
  const noProjects =
    projects.data !== undefined && projects.data.items.length === 0
  const hasFilters =
    urlFilters.projectId !== null ||
    urlFilters.status !== null ||
    urlFilters.yoloVersion !== null ||
    keyword.trim() !== ""

  const hasOfflineQueuedJobs = items.some(
    (job) => isActiveTrainingJob(job) && job.workers_online === 0
  )

  const createPath = newTrainingJobPath({ projectId: urlFilters.projectId })

  return (
    <>
      <PageHeader
        title="训练任务"
        description={TRAINING_JOBS_DESCRIPTION}
        actions={
          noProjects ? undefined : (
            <Button asChild>
              <Link to={createPath}>
                <Plus />
                新建训练任务
              </Link>
            </Button>
          )
        }
      />

      {hasOfflineQueuedJobs && (
        <Alert>
          <AlertTriangle className="size-4" />
          <AlertTitle>当前没有在线的训练节点</AlertTitle>
          <AlertDescription className="flex flex-wrap items-center justify-between gap-2">
            <span>
              排队中的训练任务会安全保存在队列中，一旦有训练节点连接上线将自动开始训练。
            </span>
            <Button variant="outline" size="xs" asChild>
              <Link to="/training/compute">前往计算资源查看节点</Link>
            </Button>
          </AlertDescription>
        </Alert>
      )}

      <div className="flex flex-wrap items-center gap-2">
        <ProjectFilterSelect
          projects={projects.data?.items}
          loading={projects.isPending}
          value={urlFilters.projectId}
          onChange={urlFilters.setProjectId}
        />

        <Select
          value={urlFilters.status ?? "all"}
          onValueChange={(v) =>
            urlFilters.setStatus(v === "all" ? null : (v as TrainingStatus))
          }
        >
          <SelectTrigger className="w-full sm:w-40" aria-label="按状态筛选">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="all">全部状态</SelectItem>
            {STATUS_OPTIONS.map((st) => (
              <SelectItem key={st} value={st}>
                {TRAINING_STATUS_LABELS[st]}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>

        <Select
          value={urlFilters.yoloVersion ?? "all"}
          onValueChange={(v) =>
            urlFilters.setYoloVersion(v === "all" ? null : (v as YoloVersion))
          }
        >
          <SelectTrigger
            className="w-full sm:w-40"
            aria-label="按 YOLO 版本筛选"
          >
            <Cpu className="text-muted-foreground" />
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="all">全部版本</SelectItem>
            {yoloVersions.map((v) => (
              <SelectItem key={v.value} value={v.value}>
                {v.label}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>

        <div className="relative min-w-48 flex-1 sm:max-w-xs">
          <Search className="pointer-events-none absolute top-1/2 left-3 size-4 -translate-y-1/2 text-muted-foreground" />
          <Input
            value={keyword}
            onChange={(event) => setKeyword(event.target.value)}
            placeholder="搜索任务名称或备注…"
            className="pr-8 pl-9"
            aria-label="搜索训练任务"
          />
          {keyword && (
            <Button
              type="button"
              variant="ghost"
              size="icon-xs"
              className="absolute top-1/2 right-1.5 -translate-y-1/2 text-muted-foreground"
              onClick={() => setKeyword("")}
              aria-label="清空搜索"
            >
              <X />
            </Button>
          )}
        </div>

        <p className="flex items-center gap-2 text-sm text-muted-foreground tabular-nums">
          {list.isFetching && list.data && (
            <Spinner className="size-3.5" aria-hidden />
          )}
          {list.data && `共 ${formatNumber(items.length)} 个`}
        </p>
      </div>

      {list.isPending ? (
        <div className="grid gap-3" aria-busy="true" aria-label="正在加载">
          {[0, 1, 2].map((row) => (
            <Skeleton key={row} className="h-20 rounded-xl" />
          ))}
        </div>
      ) : list.data === undefined ? (
        <LoadError
          title="无法获取训练任务列表"
          error={list.error}
          retrying={list.isFetching}
          onRetry={() => list.refetch()}
        />
      ) : items.length === 0 ? (
        hasFilters ? (
          <Empty className="border">
            <EmptyHeader>
              <EmptyMedia variant="icon">
                <SearchX />
              </EmptyMedia>
              <EmptyTitle className="text-base">
                没有符合筛选条件的训练任务
              </EmptyTitle>
              <EmptyDescription>
                尝试清除筛选条件，或者在当前项目下直接新建一个训练任务。
              </EmptyDescription>
            </EmptyHeader>
            <EmptyContent>
              <div className="flex flex-wrap justify-center gap-2">
                <Button
                  variant="outline"
                  size="sm"
                  onClick={() => {
                    urlFilters.resetAll()
                    setKeyword("")
                  }}
                >
                  清除全部筛选
                </Button>
                {!noProjects && (
                  <Button size="sm" asChild>
                    <Link to={createPath}>
                      <Plus />
                      新建训练任务
                    </Link>
                  </Button>
                )}
              </div>
            </EmptyContent>
          </Empty>
        ) : (
          <Empty className="border bg-card">
            <EmptyHeader className="max-w-lg">
              <EmptyMedia variant="icon">
                <Rocket />
              </EmptyMedia>
              <EmptyTitle>还没有训练任务</EmptyTitle>
              <EmptyDescription>
                {noProjects
                  ? "训练任务绑定在项目之下：先创建项目、准备数据集并生成数据版本，即可发起模型训练。"
                  : "选择项目中已生成的数据版本，配置 YOLO 版本与全量训练超参数，在线训练节点会自动拉取数据执行训练并实时回传曲线与日志。"}
              </EmptyDescription>
            </EmptyHeader>
            <EmptyContent>
              {noProjects ? (
                <Button asChild>
                  <Link to="/projects">
                    <Plus />
                    新建项目
                  </Link>
                </Button>
              ) : (
                <Button asChild>
                  <Link to={newTrainingJobPath()}>
                    <Plus />
                    新建训练任务
                  </Link>
                </Button>
              )}
            </EmptyContent>
          </Empty>
        )
      ) : (
        <TrainingJobList jobs={items} actions={actions} />
      )}

      {dialogs}
    </>
  )
}
