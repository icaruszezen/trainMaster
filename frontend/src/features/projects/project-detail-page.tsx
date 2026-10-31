import { useEffect, useState } from "react"
import { useQuery } from "@tanstack/react-query"
import {
  ArrowLeft,
  Boxes,
  Database,
  FolderSearch,
  FolderX,
  Images,
  Plus,
  SquareDashed,
} from "lucide-react"
import { Link, useNavigate, useParams, useSearchParams } from "react-router"

import { PageHeader } from "@/components/page-header"
import { StatCard } from "@/components/stat-card"
import { Badge } from "@/components/ui/badge"
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
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { watchImport } from "@/features/datasets/import-watch"
import { LoadError } from "@/features/datasets/load-error"
import {
  projectDatasetsQuery,
  projectLabelsQuery,
  projectQuery,
  type Project,
} from "@/features/projects/api"
import { AddDatasetsDialog } from "@/features/projects/add-datasets-dialog"
import {
  DeleteProjectDialog,
  EditProjectDialog,
  ProjectActionsMenu,
} from "@/features/projects/project-dialogs"
import { ProjectDatasets } from "@/features/projects/project-datasets"
import { ProjectLabels } from "@/features/projects/project-labels"
import { ProjectModels } from "@/features/projects/project-models"
import { usePageTitle } from "@/hooks/use-page-title"
import { ApiError } from "@/lib/api"
import {
  formatBytes,
  formatDateTime,
  formatNumber,
  formatPercent,
  formatRelativeTime,
  percentOf,
} from "@/lib/format"

/** 路径参数必须是正整数（与后端一致），否则视为不存在。 */
function parseProjectId(value: string | undefined) {
  if (!value || !/^[1-9]\d{0,15}$/.test(value)) {
    return null
  }
  const id = Number(value)
  return Number.isSafeInteger(id) ? id : null
}

export function ProjectDetailPage() {
  const { projectId } = useParams()
  const id = parseProjectId(projectId)

  if (id === null) {
    return <ProjectNotFound />
  }
  // 切换项目时整体重置页面状态（对话框等）。
  return <ProjectDetail key={id} id={id} />
}

function ProjectNotFound() {
  usePageTitle("项目不存在")

  return (
    <Empty className="min-h-[60svh]">
      <EmptyHeader>
        <EmptyMedia variant="icon">
          <FolderX />
        </EmptyMedia>
        <EmptyTitle>项目不存在</EmptyTitle>
        <EmptyDescription>该项目可能已被删除，或者链接有误。</EmptyDescription>
      </EmptyHeader>
      <EmptyContent>
        <Button asChild>
          <Link to="/projects">
            <ArrowLeft />
            返回项目列表
          </Link>
        </Button>
      </EmptyContent>
    </Empty>
  )
}

type DetailTab = "datasets" | "labels" | "models"

/** 当前标签页保存在地址栏中，刷新、分享链接、前进后退都能保留。 */
function useDetailTab() {
  const [params, setParams] = useSearchParams()
  const value = params.get("tab")
  const tab: DetailTab =
    value === "labels" || value === "models" ? value : "datasets"

  const setTab = (next: DetailTab) => {
    setParams(
      (previous) => {
        const updated = new URLSearchParams(previous)
        if (next === "datasets") {
          updated.delete("tab")
        } else {
          updated.set("tab", next)
        }
        return updated
      },
      { replace: true }
    )
  }
  return [tab, setTab] as const
}

function ProjectDetail({ id }: { id: number }) {
  const navigate = useNavigate()
  const query = useQuery(projectQuery(id))
  // 标签数量显示在标签页上，所以随页面一起加载。
  const labels = useQuery(projectLabelsQuery(id))
  const [tab, setTab] = useDetailTab()

  const [addOpen, setAddOpen] = useState(false)
  const [editing, setEditing] = useState(false)
  const [deleting, setDeleting] = useState(false)

  usePageTitle(query.data?.name)

  if (query.isPending) {
    return <DetailSkeleton />
  }
  if (query.error instanceof ApiError && query.error.status === 404) {
    return <ProjectNotFound />
  }
  // 后台刷新失败时继续显示已加载的数据，不卸载打开着的对话框。
  if (query.data === undefined) {
    return (
      <LoadError
        title="无法获取项目"
        error={query.error}
        retrying={query.isFetching}
        onRetry={() => query.refetch()}
      />
    )
  }

  const project = query.data

  return (
    <>
      <div className="grid grid-cols-1 gap-3">
        <PageHeader
          title={project.name}
          description={project.description || undefined}
          actions={
            <>
              <Button onClick={() => setAddOpen(true)}>
                <Plus />
                添加数据集
              </Button>
              <ProjectActionsMenu
                project={project}
                onEdit={() => setEditing(true)}
                onDelete={() => setDeleting(true)}
                className="size-9"
              />
            </>
          }
        />
        <ProjectMeta project={project} />
      </div>

      <ProjectStats project={project} />

      <Tabs
        value={tab}
        onValueChange={(value) => setTab(value as DetailTab)}
        className="gap-4"
      >
        <TabsList className="w-full sm:w-fit">
          <TabsTrigger value="datasets" className="px-3">
            数据集
            <span className="text-xs text-muted-foreground tabular-nums">
              {formatNumber(project.dataset_count)}
            </span>
          </TabsTrigger>
          <TabsTrigger value="labels" className="px-3">
            标签分布
            {labels.data && (
              <span className="text-xs text-muted-foreground tabular-nums">
                {formatNumber(labels.data.total)}
              </span>
            )}
          </TabsTrigger>
          <TabsTrigger value="models" className="px-3">
            模型
          </TabsTrigger>
        </TabsList>
        <TabsContent value="datasets">
          <ProjectDatasets
            project={project}
            onAddDatasets={() => setAddOpen(true)}
          />
        </TabsContent>
        <TabsContent value="labels">
          <ProjectLabels query={labels} />
        </TabsContent>
        <TabsContent value="models">
          <ProjectModels />
        </TabsContent>
      </Tabs>

      <AddDatasetsDialog
        project={project}
        open={addOpen}
        onOpenChange={setAddOpen}
      />
      <EditProjectDialog
        project={editing ? project : null}
        onOpenChange={setEditing}
      />
      <DeleteProjectDialog
        project={deleting ? project : null}
        onOpenChange={setDeleting}
        onDeleted={() => navigate("/projects", { replace: true })}
      />
    </>
  )
}

function ProjectMeta({ project }: { project: Project }) {
  return (
    <div className="flex flex-wrap items-center gap-x-4 gap-y-2 text-xs text-muted-foreground">
      <Badge variant="outline" className="font-normal tabular-nums">
        {formatNumber(project.dataset_count)} 个数据集
      </Badge>
      <span title={formatDateTime(project.created_at)}>
        创建于 {formatRelativeTime(project.created_at)}
      </span>
      <span title={formatDateTime(project.updated_at)}>
        更新于 {formatRelativeTime(project.updated_at)}
      </span>
      {project.dataset_count > 0 && (
        <Link
          to={`/datasets?project=${project.id}`}
          className="inline-flex items-center gap-1 underline-offset-4 hover:text-foreground hover:underline"
        >
          <FolderSearch className="size-3.5" />
          在数据集页面查看
        </Link>
      )}
    </div>
  )
}

function ProjectStats({ project }: { project: Project }) {
  const percent = percentOf(project.annotated_count, project.image_count)
  const datasets = useQuery(projectDatasetsQuery(project.id))
  // 观察数据集上进行中的导入 / 扫描（包括别人发起的）：结束后会刷新项目的
  // 合计统计与标签分布，与数据集详情页的进度卡片一样。
  useEffect(() => {
    for (const dataset of datasets.data?.items ?? []) {
      if (dataset.active_import) {
        watchImport(dataset.active_import, dataset.name)
      }
    }
  }, [datasets.data])
  const missing =
    datasets.data?.items.filter((dataset) => dataset.directory_missing)
      .length ?? 0

  return (
    <div className="grid grid-cols-2 gap-4 lg:grid-cols-4">
      <StatCard
        title="数据集"
        value={formatNumber(project.dataset_count)}
        icon={Database}
      >
        <span className="tabular-nums">
          {missing > 0
            ? `${formatNumber(missing)} 个文件夹缺失`
            : `共 ${formatBytes(project.total_bytes)}`}
        </span>
      </StatCard>
      <StatCard
        title="图片"
        value={formatNumber(project.image_count)}
        icon={Images}
      >
        <span className="tabular-nums">
          标注目标 {formatNumber(project.object_count)} 个
        </span>
      </StatCard>
      <StatCard
        title="已标注"
        value={formatNumber(project.annotated_count)}
        icon={SquareDashed}
      >
        <Progress
          value={percent}
          className="h-1.5"
          aria-label={`标注率 ${formatPercent(percent)}`}
        />
        <span className="flex flex-wrap justify-between gap-x-2 tabular-nums">
          <span>标注率 {formatPercent(percent)}</span>
          <span>空标注 {formatNumber(project.empty_count)}</span>
        </span>
      </StatCard>
      <StatCard title="模型" value="—" icon={Boxes}>
        <span>模型仓库上线后显示</span>
      </StatCard>
    </div>
  )
}

function DetailSkeleton() {
  return (
    <div className="grid gap-6" aria-busy="true" aria-label="正在加载">
      <div className="grid gap-3">
        <Skeleton className="h-8 w-64" />
        <Skeleton className="h-4 w-96 max-w-full" />
        <Skeleton className="h-5 w-80 max-w-full" />
      </div>
      <div className="grid grid-cols-2 gap-4 lg:grid-cols-4">
        {[0, 1, 2, 3].map((index) => (
          <Skeleton key={index} className="h-[118px] rounded-xl" />
        ))}
      </div>
      <Skeleton className="h-9 w-72" />
      <Skeleton className="h-64 w-full rounded-xl" />
    </div>
  )
}
