import { useMemo, useState } from "react"
import { keepPreviousData, useQuery } from "@tanstack/react-query"
import {
  ArrowRight,
  Database,
  FolderKanban,
  FolderOpen,
  Images,
  LayoutGrid,
  List,
  Plus,
  Search,
  SearchX,
  X,
} from "lucide-react"
import { Link, useNavigate } from "react-router"

import { PageHeader } from "@/components/page-header"
import { StatCard } from "@/components/stat-card"
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
  InputGroup,
  InputGroupAddon,
  InputGroupButton,
  InputGroupInput,
} from "@/components/ui/input-group"
import { Progress } from "@/components/ui/progress"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { Skeleton } from "@/components/ui/skeleton"
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group"
import { datasetsQuery } from "@/features/datasets/api"
import { LoadError } from "@/features/datasets/load-error"
import { projectsQuery, type Project } from "@/features/projects/api"
import { ProjectCard } from "@/features/projects/project-card"
import {
  CreateProjectDialog,
  DeleteProjectDialog,
  EditProjectDialog,
} from "@/features/projects/project-dialogs"
import { ProjectTable } from "@/features/projects/project-table"
import { useDebouncedValue } from "@/hooks/use-debounced-value"
import { formatNumber, formatPercent, percentOf } from "@/lib/format"
import { readPreference, writePreference } from "@/lib/storage"

type SortKey = "updated" | "name" | "datasets" | "images"
type ViewMode = "grid" | "list"

const sortOptions: { value: SortKey; label: string }[] = [
  { value: "updated", label: "最近更新" },
  { value: "name", label: "名称" },
  { value: "datasets", label: "数据集数量" },
  { value: "images", label: "图片数量" },
]

const nameCollator = new Intl.Collator("zh-CN", { numeric: true })

function sortProjects(projects: Project[], sort: SortKey) {
  const sorted = [...projects]
  switch (sort) {
    case "name":
      return sorted.sort((a, b) => nameCollator.compare(a.name, b.name))
    case "datasets":
      return sorted.sort((a, b) => b.dataset_count - a.dataset_count)
    case "images":
      return sorted.sort((a, b) => b.image_count - a.image_count)
    default:
      // 服务端已按更新时间倒序返回。
      return sorted
  }
}

const VIEW_STORAGE_KEY = "trainmaster-projects-view"

function readViewMode(): ViewMode {
  return readPreference(VIEW_STORAGE_KEY) === "list" ? "list" : "grid"
}

export function ProjectsPage() {
  const navigate = useNavigate()
  const [search, setSearch] = useState("")
  const q = useDebouncedValue(search.trim(), 300)
  const [sort, setSort] = useState<SortKey>("updated")
  const [view, setView] = useState<ViewMode>(readViewMode)

  const [createOpen, setCreateOpen] = useState(false)
  const [editing, setEditing] = useState<Project | null>(null)
  const [deleting, setDeleting] = useState<Project | null>(null)

  // 不带搜索条件的完整列表用于统计；搜索结果单独查询。
  const all = useQuery(projectsQuery())
  const filtered = useQuery({
    ...projectsQuery(q),
    placeholderData: keepPreviousData,
  })

  const items = useMemo(
    () => sortProjects(filtered.data?.items ?? [], sort),
    [filtered.data, sort]
  )

  const changeView = (value: string) => {
    if (value === "grid" || value === "list") {
      setView(value)
      writePreference(VIEW_STORAGE_KEY, value)
    }
  }

  const hasProjects = (all.data?.total ?? 0) > 0

  return (
    <>
      <PageHeader
        title="项目"
        description="按检测任务把数据集组织成项目：一个数据集可以纳入多个项目，项目页面汇总其中的数据、标注与模型情况。"
        actions={
          <Button onClick={() => setCreateOpen(true)}>
            <Plus />
            新建项目
          </Button>
        }
      />

      {all.isPending ? (
        <ProjectsSkeleton />
      ) : all.data === undefined ? (
        // 只在没有数据时显示错误：后台刷新失败时保留已加载的列表。
        <LoadError
          title="无法获取项目列表"
          error={all.error}
          retrying={all.isFetching}
          onRetry={() => all.refetch()}
        />
      ) : !hasProjects ? (
        <ProjectsEmptyState onCreate={() => setCreateOpen(true)} />
      ) : (
        <>
          <ProjectsSummary projects={all.data.items} />

          <div className="flex flex-wrap items-center gap-2">
            <InputGroup className="w-full sm:w-72">
              <InputGroupAddon>
                <Search />
              </InputGroupAddon>
              <InputGroupInput
                value={search}
                onChange={(event) => setSearch(event.target.value)}
                placeholder="搜索名称或描述"
                aria-label="搜索项目"
              />
              {search && (
                <InputGroupAddon align="inline-end">
                  <InputGroupButton
                    size="icon-xs"
                    onClick={() => setSearch("")}
                    aria-label="清除搜索"
                  >
                    <X />
                  </InputGroupButton>
                </InputGroupAddon>
              )}
            </InputGroup>
            <Select
              value={sort}
              onValueChange={(value) => setSort(value as SortKey)}
            >
              <SelectTrigger className="w-32" aria-label="排序方式">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {sortOptions.map((option) => (
                  <SelectItem key={option.value} value={option.value}>
                    {option.label}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            <p className="text-sm text-muted-foreground tabular-nums">
              {q ? `找到 ${items.length} 个` : `共 ${items.length} 个`}
            </p>
            <ToggleGroup
              type="single"
              variant="outline"
              value={view}
              onValueChange={changeView}
              className="ml-auto"
              aria-label="显示方式"
            >
              <ToggleGroupItem value="grid" aria-label="卡片视图">
                <LayoutGrid />
              </ToggleGroupItem>
              <ToggleGroupItem value="list" aria-label="列表视图">
                <List />
              </ToggleGroupItem>
            </ToggleGroup>
          </div>

          {filtered.error && filtered.data === undefined ? (
            <LoadError
              title="搜索失败"
              error={filtered.error}
              retrying={filtered.isFetching}
              onRetry={() => filtered.refetch()}
            />
          ) : items.length === 0 && !filtered.isPending ? (
            <Empty className="border">
              <EmptyHeader>
                <EmptyMedia variant="icon">
                  <SearchX />
                </EmptyMedia>
                <EmptyTitle className="text-base">没有匹配的项目</EmptyTitle>
                <EmptyDescription>
                  没有名称或描述包含“{q}”的项目。
                </EmptyDescription>
              </EmptyHeader>
              <EmptyContent>
                <Button
                  variant="outline"
                  size="sm"
                  onClick={() => setSearch("")}
                >
                  清除搜索
                </Button>
              </EmptyContent>
            </Empty>
          ) : view === "grid" ? (
            <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3 2xl:grid-cols-4">
              {items.map((project) => (
                <ProjectCard
                  key={project.id}
                  project={project}
                  onEdit={() => setEditing(project)}
                  onDelete={() => setDeleting(project)}
                />
              ))}
            </div>
          ) : (
            <ProjectTable
              projects={items}
              onEdit={setEditing}
              onDelete={setDeleting}
            />
          )}
        </>
      )}

      <CreateProjectDialog
        open={createOpen}
        onOpenChange={setCreateOpen}
        onCreated={(project) => navigate(`/projects/${project.id}`)}
      />
      <EditProjectDialog
        project={editing}
        onOpenChange={(open) => !open && setEditing(null)}
      />
      <DeleteProjectDialog
        project={deleting}
        onOpenChange={(open) => !open && setDeleting(null)}
      />
    </>
  )
}

/**
 * 顶部统计。数据集可以属于多个项目，所以“项目内图片”按数据集去重统计，
 * 而不是把各项目的数字相加。
 */
function ProjectsSummary({ projects }: { projects: Project[] }) {
  const datasets = useQuery(datasetsQuery())
  const withDatasets = projects.filter((p) => p.dataset_count > 0).length

  const stats = useMemo(() => {
    const items = datasets.data?.items
    if (!items) {
      return null
    }
    const assigned = items.filter((dataset) => dataset.projects.length > 0)
    return {
      total: items.length,
      assigned: assigned.length,
      images: assigned.reduce((sum, d) => sum + d.image_count, 0),
      annotated: assigned.reduce((sum, d) => sum + d.annotated_count, 0),
    }
  }, [datasets.data])

  const loading = <Skeleton className="h-8 w-16" />
  const assignedPercent = stats ? percentOf(stats.assigned, stats.total) : 0
  const unassigned = stats ? stats.total - stats.assigned : 0

  return (
    <div className="grid grid-cols-2 gap-4 lg:grid-cols-4">
      <StatCard
        title="项目"
        value={formatNumber(projects.length)}
        icon={FolderKanban}
      >
        <span>
          {withDatasets === projects.length
            ? "每个项目都已纳入数据集"
            : `${formatNumber(projects.length - withDatasets)} 个项目还没有数据集`}
        </span>
      </StatCard>
      <StatCard
        title="已纳入项目的数据集"
        value={stats ? formatNumber(stats.assigned) : loading}
        icon={Database}
      >
        <Progress
          value={assignedPercent}
          className="h-1.5"
          aria-label={`已纳入 ${formatPercent(assignedPercent)}`}
        />
        <span className="tabular-nums">
          {stats ? `共 ${formatNumber(stats.total)} 个数据集` : "正在加载…"}
        </span>
      </StatCard>
      <StatCard
        title="项目内图片"
        value={stats ? formatNumber(stats.images) : loading}
        icon={Images}
      >
        <span className="tabular-nums">
          {stats
            ? stats.images > 0
              ? `已标注 ${formatPercent(percentOf(stats.annotated, stats.images))}`
              : "暂无图片"
            : "正在加载…"}
        </span>
      </StatCard>
      <StatCard
        title="未纳入项目的数据集"
        value={stats ? formatNumber(unassigned) : loading}
        icon={FolderOpen}
      >
        {stats && unassigned > 0 ? (
          <Link
            to="/datasets?project=none"
            className="inline-flex items-center gap-1 self-start underline-offset-4 hover:text-foreground hover:underline"
          >
            查看并纳入项目
            <ArrowRight className="size-3" />
          </Link>
        ) : (
          <span>{stats ? "所有数据集都已纳入项目" : "正在加载…"}</span>
        )}
      </StatCard>
    </div>
  )
}

function ProjectsEmptyState({ onCreate }: { onCreate: () => void }) {
  return (
    <Empty className="border bg-card">
      <EmptyHeader className="max-w-lg">
        <EmptyMedia variant="icon">
          <FolderKanban />
        </EmptyMedia>
        <EmptyTitle>还没有项目</EmptyTitle>
        <EmptyDescription>
          项目用于按检测任务（如某条产线的外观缺陷检测）组织数据集，并汇总其中的图片、标注进度与标签分布；模型功能上线后，项目也会关联训练出的模型。
        </EmptyDescription>
      </EmptyHeader>
      <EmptyContent>
        <div className="flex flex-wrap justify-center gap-2">
          <Button onClick={onCreate}>
            <Plus />
            新建项目
          </Button>
          <Button variant="outline" asChild>
            <Link to="/datasets">
              <Database />
              查看数据集
            </Link>
          </Button>
        </div>
      </EmptyContent>
    </Empty>
  )
}

function ProjectsSkeleton() {
  return (
    <div className="grid gap-6" aria-busy="true" aria-label="正在加载">
      <div className="grid grid-cols-2 gap-4 lg:grid-cols-4">
        {[0, 1, 2, 3].map((index) => (
          <Skeleton key={index} className="h-[118px] rounded-xl" />
        ))}
      </div>
      <Skeleton className="h-9 w-full max-w-md" />
      <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3 2xl:grid-cols-4">
        {[0, 1, 2].map((index) => (
          <div key={index} className="overflow-hidden rounded-xl border">
            <Skeleton className="aspect-[16/9] rounded-none" />
            <div className="grid gap-3 p-4">
              <Skeleton className="h-5 w-2/3" />
              <Skeleton className="h-4 w-full" />
              <Skeleton className="h-4 w-1/2" />
            </div>
          </div>
        ))}
      </div>
    </div>
  )
}
