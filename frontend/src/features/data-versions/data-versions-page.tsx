/**
 * 数据版本（`/datasets/versions`）：所有项目的数据版本，可按项目筛选（地址栏
 * `?project=12`，项目页面可以直接链接过来）。
 */
import { useMemo } from "react"
import { useQuery } from "@tanstack/react-query"
import { FolderKanban, Layers, Plus, SearchX } from "lucide-react"
import { Link, useSearchParams } from "react-router"

import { PageHeader } from "@/components/page-header"
import { Button } from "@/components/ui/button"
import {
  Empty,
  EmptyContent,
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from "@/components/ui/empty"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectSeparator,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { Skeleton } from "@/components/ui/skeleton"
import { Spinner } from "@/components/ui/spinner"
import { LoadError } from "@/features/datasets/load-error"
import { dataVersionsQuery } from "@/features/data-versions/api"
import { useDataVersionActions } from "@/features/data-versions/data-version-actions"
import { DataVersionList } from "@/features/data-versions/data-version-list"
import {
  newDataVersionPath,
  parsePositiveId,
} from "@/features/data-versions/paths"
import { useBuildWatchNavigate } from "@/features/data-versions/use-build-watch-navigate"
import { projectsQuery, type Project } from "@/features/projects/api"
import { formatNumber } from "@/lib/format"

const nameCollator = new Intl.Collator("zh-CN", { numeric: true })

/** 页面说明：数据版本与审图版本的区别。 */
export const DATA_VERSIONS_DESCRIPTION =
  "数据版本是项目的冻结训练集：从项目的数据集（基础标注与审图版本）中选取图片，按比例划分训练 / 验证 / 测试集，生成后不再变化，可以直接训练或下载为 YOLO / VOC / COCO。审图版本则是单个数据集上合并审图得到的一层标注。"

/**
 * 地址栏中的项目筛选。筛选的项目已被删除时视为不筛选，免得列表一直是空的却
 * 看不出原因。
 */
function useProjectFilter(projects: Project[] | undefined) {
  const [params, setParams] = useSearchParams()
  const parsed = parsePositiveId(params.get("project"))
  const project =
    parsed !== null ? projects?.find((p) => p.id === parsed) : undefined
  const projectId = parsed !== null && projects && !project ? null : parsed

  const setProjectId = (id: number | null) =>
    setParams(
      (previous) => {
        const next = new URLSearchParams(previous)
        if (id === null) {
          next.delete("project")
        } else {
          next.set("project", String(id))
        }
        return next
      },
      { replace: true }
    )

  return { projectId, project, setProjectId }
}

export function DataVersionsPage() {
  const projects = useQuery(projectsQuery())
  const filter = useProjectFilter(projects.data?.items)
  const list = useQuery(dataVersionsQuery(filter.projectId))
  const { actions, dialogs } = useDataVersionActions()
  useBuildWatchNavigate()

  const items = list.data?.items ?? []
  const createPath = newDataVersionPath({ projectId: filter.projectId })
  const noProjects =
    projects.data !== undefined && projects.data.items.length === 0

  return (
    <>
      <PageHeader
        title="数据版本"
        description={DATA_VERSIONS_DESCRIPTION}
        actions={
          noProjects ? undefined : (
            <Button asChild>
              <Link to={createPath}>
                <Plus />
                生成数据版本
              </Link>
            </Button>
          )
        }
      />

      <div className="flex flex-wrap items-center gap-2">
        <ProjectFilterSelect
          projects={projects.data?.items}
          loading={projects.isPending}
          value={filter.projectId}
          onChange={filter.setProjectId}
        />
        <p className="flex items-center gap-2 text-sm text-muted-foreground tabular-nums">
          {list.isFetching && list.data && (
            <Spinner className="size-3.5" aria-hidden />
          )}
          {list.data &&
            (filter.projectId !== null
              ? `项目中共 ${formatNumber(items.length)} 个`
              : `共 ${formatNumber(items.length)} 个`)}
        </p>
      </div>

      {list.isPending ? (
        <ListSkeleton />
      ) : list.data === undefined ? (
        // 只在没有数据时显示错误：后台刷新失败时保留已加载的列表。
        <LoadError
          title="无法获取数据版本"
          error={list.error}
          retrying={list.isFetching}
          onRetry={() => list.refetch()}
        />
      ) : items.length === 0 ? (
        filter.projectId !== null ? (
          <Empty className="border">
            <EmptyHeader>
              <EmptyMedia variant="icon">
                <SearchX />
              </EmptyMedia>
              <EmptyTitle className="text-base">
                项目“{filter.project?.name ?? `#${filter.projectId}`}
                ”还没有数据版本
              </EmptyTitle>
              <EmptyDescription>
                从项目的数据集生成第一个数据版本：选择标注来源与划分比例，生成后可以直接训练或下载。
              </EmptyDescription>
            </EmptyHeader>
            <EmptyContent>
              <div className="flex flex-wrap justify-center gap-2">
                <Button
                  variant="outline"
                  size="sm"
                  onClick={() => filter.setProjectId(null)}
                >
                  查看全部项目
                </Button>
                <Button size="sm" asChild>
                  <Link to={createPath}>
                    <Plus />
                    生成数据版本
                  </Link>
                </Button>
              </div>
            </EmptyContent>
          </Empty>
        ) : (
          <DataVersionsEmptyState noProjects={noProjects} />
        )
      ) : (
        <DataVersionList versions={items} actions={actions} />
      )}

      {dialogs}
    </>
  )
}

function DataVersionsEmptyState({ noProjects }: { noProjects: boolean }) {
  return (
    <Empty className="border bg-card">
      <EmptyHeader className="max-w-lg">
        <EmptyMedia variant="icon">
          <Layers />
        </EmptyMedia>
        <EmptyTitle>还没有数据版本</EmptyTitle>
        <EmptyDescription>
          {noProjects
            ? "数据版本属于项目：先新建一个项目，把数据集加入项目并设置类别列表，再生成数据版本。"
            : "选择一个项目，从它的数据集生成冻结的训练数据：类别编号按项目的类别列表，图片按固定的随机种子划分训练 / 验证 / 测试集，可选离线增强。"}
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
            <Link to={newDataVersionPath()}>
              <Plus />
              生成数据版本
            </Link>
          </Button>
        )}
        <p className="text-xs text-muted-foreground">
          之后的训练任务将选择数据版本来训练，保证每个模型都能追溯到它的训练数据。
        </p>
      </EmptyContent>
    </Empty>
  )
}

/** 按项目筛选：全部项目 / 某个项目。 */
export function ProjectFilterSelect({
  projects,
  loading,
  value,
  onChange,
}: {
  /** 还在加载或加载失败时为 undefined。 */
  projects: Project[] | undefined
  loading: boolean
  value: number | null
  onChange: (value: number | null) => void
}) {
  const sorted = useMemo(
    () =>
      [...(projects ?? [])].sort((a, b) =>
        nameCollator.compare(a.name, b.name)
      ),
    [projects]
  )
  // 从链接进入（?project=ID）时项目列表可能还没加载：先占一个选项，
  // 否则选择框是空的，看不出列表已经筛选过。
  const pendingValue =
    value !== null && !sorted.some((p) => p.id === value) ? value : null

  return (
    <Select
      value={value === null ? "all" : String(value)}
      onValueChange={(next) => onChange(next === "all" ? null : Number(next))}
    >
      <SelectTrigger className="w-full sm:w-56" aria-label="按项目筛选">
        <FolderKanban className="text-muted-foreground" />
        <SelectValue />
      </SelectTrigger>
      <SelectContent>
        <SelectItem value="all">全部项目</SelectItem>
        {(sorted.length > 0 || pendingValue !== null) && <SelectSeparator />}
        {pendingValue !== null && (
          <SelectItem value={String(pendingValue)}>
            {loading ? "加载中…" : `项目 #${pendingValue}`}
          </SelectItem>
        )}
        {sorted.map((project) => (
          <SelectItem key={project.id} value={String(project.id)}>
            <span className="max-w-56 truncate">{project.name}</span>
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  )
}

function ListSkeleton() {
  return (
    <div className="grid gap-3" aria-busy="true" aria-label="正在加载">
      {[0, 1, 2].map((row) => (
        <Skeleton key={row} className="h-20 rounded-xl" />
      ))}
    </div>
  )
}
