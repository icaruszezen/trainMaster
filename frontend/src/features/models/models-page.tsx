/**
 * 模型仓库（`/models`）：所有项目的模型，可按项目、YOLO 版本与关键字筛选。
 * 筛选保存在地址栏中（`?project=12&yolo=yolo11`），项目页面可以直接链接过来。
 */
import { useMemo, useState } from "react"
import { keepPreviousData, useQuery } from "@tanstack/react-query"
import {
  Boxes,
  FileDown,
  FolderKanban,
  HardDrive,
  Layers,
  Plus,
  Search,
  SearchX,
  Upload,
  X,
} from "lucide-react"
import { Link, useSearchParams } from "react-router"

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
import {
  Select,
  SelectContent,
  SelectItem,
  SelectSeparator,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { Skeleton } from "@/components/ui/skeleton"
import { LoadError } from "@/features/datasets/load-error"
import {
  ALL_MODELS,
  modelsQuery,
  type Model,
  type ModelFilters,
  type YoloVersion,
} from "@/features/models/api"
import { useModelActions } from "@/features/models/model-actions"
import { ModelList } from "@/features/models/model-list"
import { UploadModelDialog } from "@/features/models/upload-model-dialog"
import {
  findYoloVersion,
  yoloVersionLabel,
  yoloVersions,
} from "@/features/models/yolo-versions"
import { projectsQuery, type Project } from "@/features/projects/api"
import { useDebouncedValue } from "@/hooks/use-debounced-value"
import { formatBytes, formatNumber } from "@/lib/format"

const nameCollator = new Intl.Collator("zh-CN", { numeric: true })

/** 地址栏中的项目编号；不是正整数时视为不筛选。 */
function parseProjectParam(raw: string | null) {
  if (raw === null || !/^[1-9]\d{0,15}$/.test(raw)) {
    return null
  }
  return Number(raw)
}

function parseYoloParam(raw: string | null): YoloVersion | null {
  return raw !== null && findYoloVersion(raw) ? (raw as YoloVersion) : null
}

/**
 * 筛选条件（项目与 YOLO 版本在地址栏中）。筛选的项目已被删除时视为不筛选，
 * 免得列表一直是空的却看不出原因。
 */
function useModelFilters(projects: Project[] | undefined) {
  const [params, setParams] = useSearchParams()
  const parsedProject = parseProjectParam(params.get("project"))
  const project =
    parsedProject !== null
      ? projects?.find((candidate) => candidate.id === parsedProject)
      : undefined
  const projectId =
    parsedProject !== null && projects && !project ? null : parsedProject
  const yoloVersion = parseYoloParam(params.get("yolo"))

  const setParam = (key: string, value: string | null) => {
    setParams(
      (previous) => {
        const updated = new URLSearchParams(previous)
        if (value === null) {
          updated.delete(key)
        } else {
          updated.set(key, value)
        }
        return updated
      },
      { replace: true }
    )
  }

  return {
    projectId,
    project,
    yoloVersion,
    setProjectId: (id: number | null) =>
      setParam("project", id === null ? null : String(id)),
    setYoloVersion: (version: YoloVersion | null) => setParam("yolo", version),
  }
}

export function ModelsPage() {
  const [search, setSearch] = useState("")
  const q = useDebouncedValue(search.trim(), 300)
  const [uploadOpen, setUploadOpen] = useState(false)
  // 每次打开上传对话框换一个 key，表单从头开始。
  const [uploadKey, setUploadKey] = useState(0)

  const projects = useQuery(projectsQuery())
  const filters = useModelFilters(projects.data?.items)
  const { actions, dialogs } = useModelActions()

  // 不带筛选条件的完整列表用于统计；筛选结果单独查询。
  const all = useQuery(modelsQuery())
  const currentFilters: ModelFilters = {
    ...ALL_MODELS,
    projectId: filters.projectId,
    yoloVersion: filters.yoloVersion,
    q,
  }
  const isFiltered =
    q !== "" || filters.projectId !== null || filters.yoloVersion !== null
  const filtered = useQuery({
    ...modelsQuery(currentFilters),
    placeholderData: keepPreviousData,
    enabled: isFiltered,
  })
  const list = isFiltered ? filtered : all
  const items = list.data?.items ?? []

  const openUpload = () => {
    setUploadKey((key) => key + 1)
    setUploadOpen(true)
  }

  const clearFilters = () => {
    setSearch("")
    filters.setProjectId(null)
    filters.setYoloVersion(null)
  }

  const hasModels = (all.data?.total ?? 0) > 0

  return (
    <>
      <PageHeader
        title="模型仓库"
        description="按项目保存与迭代 YOLO 模型：上传训练好的 .pt 文件，每个项目内按版本号 v1、v2… 递增；下载时可以选择 PT，或由训练节点自动转换为 ONNX。"
        actions={
          <Button onClick={openUpload}>
            <Upload />
            上传模型
          </Button>
        }
      />

      {all.isPending ? (
        <ModelsSkeleton />
      ) : all.data === undefined ? (
        // 只在没有数据时显示错误：后台刷新失败时保留已加载的列表。
        <LoadError
          title="无法获取模型列表"
          error={all.error}
          retrying={all.isFetching}
          onRetry={() => all.refetch()}
        />
      ) : !hasModels ? (
        <ModelsEmptyState
          projects={projects.data?.items}
          onUpload={openUpload}
        />
      ) : (
        <>
          <ModelsSummary
            models={all.data.items}
            total={all.data.total}
            projects={projects.data?.items}
          />

          <div className="flex flex-wrap items-center gap-2">
            <InputGroup className="w-full sm:w-72">
              <InputGroupAddon>
                <Search />
              </InputGroupAddon>
              <InputGroupInput
                value={search}
                onChange={(event) => setSearch(event.target.value)}
                placeholder="搜索项目、说明或文件名"
                aria-label="搜索模型"
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
            <ProjectFilterSelect
              projects={projects.data?.items}
              loading={projects.isPending}
              value={filters.projectId}
              onChange={filters.setProjectId}
            />
            <YoloFilterSelect
              value={filters.yoloVersion}
              onChange={filters.setYoloVersion}
            />
            <p className="text-sm text-muted-foreground tabular-nums">
              {isFiltered
                ? `找到 ${formatNumber(items.length)} 个`
                : `共 ${formatNumber(items.length)} 个`}
            </p>
          </div>

          {list.error && list.data === undefined ? (
            <LoadError
              title="筛选失败"
              error={list.error}
              retrying={list.isFetching}
              onRetry={() => list.refetch()}
            />
          ) : items.length === 0 && !list.isPending ? (
            <Empty className="border">
              <EmptyHeader>
                <EmptyMedia variant="icon">
                  <SearchX />
                </EmptyMedia>
                <EmptyTitle className="text-base">没有匹配的模型</EmptyTitle>
                <EmptyDescription>
                  {noMatchText(q, filters.project, filters.yoloVersion)}
                </EmptyDescription>
              </EmptyHeader>
              <EmptyContent>
                <div className="flex flex-wrap justify-center gap-2">
                  <Button variant="outline" size="sm" onClick={clearFilters}>
                    清除筛选
                  </Button>
                  {filters.project && q === "" && (
                    <Button size="sm" onClick={openUpload}>
                      <Upload />
                      上传到该项目
                    </Button>
                  )}
                </div>
              </EmptyContent>
            </Empty>
          ) : list.isPending ? (
            <Skeleton className="h-64 rounded-xl" />
          ) : (
            <ModelList models={items} actions={actions} />
          )}
        </>
      )}

      {dialogs}
      <UploadModelDialog
        key={uploadKey}
        open={uploadOpen}
        onOpenChange={setUploadOpen}
        projectId={filters.projectId}
      />
    </>
  )
}

function noMatchText(
  q: string,
  project: Project | undefined,
  yoloVersion: YoloVersion | null
) {
  const conditions = [
    project ? `属于项目“${project.name}”` : null,
    yoloVersion ? `版本为 ${yoloVersionLabel(yoloVersion)}` : null,
    q ? `项目名、说明或文件名包含“${q}”` : null,
  ].filter((part): part is string => part !== null)
  return conditions.length > 0
    ? `没有${conditions.join("、")}的模型。`
    : "没有符合条件的模型。"
}

/** 按项目筛选：全部项目 / 某个项目（有模型的项目排在前面）。 */
function ProjectFilterSelect({
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
      [...(projects ?? [])].sort(
        (a, b) =>
          Number(b.model_count > 0) - Number(a.model_count > 0) ||
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
      <SelectTrigger className="w-40" aria-label="按项目筛选">
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

function YoloFilterSelect({
  value,
  onChange,
}: {
  value: YoloVersion | null
  onChange: (value: YoloVersion | null) => void
}) {
  return (
    <Select
      value={value ?? "all"}
      onValueChange={(next) =>
        onChange(next === "all" ? null : (next as YoloVersion))
      }
    >
      <SelectTrigger className="w-36" aria-label="按 YOLO 版本筛选">
        <Layers className="text-muted-foreground" />
        <SelectValue />
      </SelectTrigger>
      <SelectContent>
        <SelectItem value="all">全部版本</SelectItem>
        <SelectSeparator />
        {yoloVersions.map((version) => (
          <SelectItem key={version.value} value={version.value}>
            {version.label}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  )
}

function ModelsSummary({
  models,
  total,
  projects,
}: {
  models: Model[]
  total: number
  projects: Project[] | undefined
}) {
  const totals = models.reduce(
    (sum, model) => ({
      uploads: sum.uploads + (model.source === "upload" ? 1 : 0),
      exports: sum.exports + model.export_count,
      bytes: sum.bytes + model.size_bytes,
      missing: sum.missing + (model.file_missing ? 1 : 0),
    }),
    { uploads: 0, exports: 0, bytes: 0, missing: 0 }
  )
  const projectIds = new Set(models.map((model) => model.project.id))
  const trained = models.length - totals.uploads

  return (
    <div className="grid grid-cols-2 gap-4 lg:grid-cols-4">
      <StatCard title="模型" value={formatNumber(total)} icon={Boxes}>
        <span className="tabular-nums">
          {trained > 0
            ? `上传 ${formatNumber(totals.uploads)} · 平台训练 ${formatNumber(trained)}`
            : "均为用户上传"}
        </span>
      </StatCard>
      <StatCard
        title="有模型的项目"
        value={formatNumber(projectIds.size)}
        icon={FolderKanban}
      >
        <span className="tabular-nums">
          {projects
            ? `共 ${formatNumber(projects.length)} 个项目`
            : "正在加载…"}
        </span>
      </StatCard>
      <StatCard
        title="ONNX 转换结果"
        value={formatNumber(totals.exports)}
        icon={FileDown}
      >
        <span>
          {totals.exports > 0
            ? "已缓存，再次下载无需转换"
            : "下载 ONNX 时自动转换"}
        </span>
      </StatCard>
      <StatCard
        title="模型文件"
        value={formatBytes(totals.bytes)}
        icon={HardDrive}
      >
        <span className={totals.missing > 0 ? "text-destructive" : undefined}>
          {totals.missing > 0
            ? `${formatNumber(totals.missing)} 个模型的文件缺失`
            : "PT 文件，不含 ONNX 转换结果"}
        </span>
      </StatCard>
    </div>
  )
}

function ModelsEmptyState({
  projects,
  onUpload,
}: {
  projects: Project[] | undefined
  onUpload: () => void
}) {
  const noProjects = projects !== undefined && projects.length === 0
  return (
    <Empty className="border bg-card">
      <EmptyHeader className="max-w-lg">
        <EmptyMedia variant="icon">
          <Boxes />
        </EmptyMedia>
        <EmptyTitle>还没有模型</EmptyTitle>
        <EmptyDescription>
          {noProjects
            ? "模型需要绑定到项目：先新建一个项目，再把训练好的 YOLO 模型（.pt 文件）上传到该项目。"
            : "上传训练好的 YOLO 模型（.pt 文件，支持 YOLOv8 – YOLO26），选择所属项目与 YOLO 版本；同一项目多次上传按 v1、v2… 递增版本号。"}
        </EmptyDescription>
      </EmptyHeader>
      <EmptyContent>
        <div className="flex flex-wrap justify-center gap-2">
          {noProjects ? (
            <Button asChild>
              <Link to="/projects">
                <Plus />
                新建项目
              </Link>
            </Button>
          ) : (
            <Button onClick={onUpload}>
              <Upload />
              上传模型
            </Button>
          )}
        </div>
        <p className="text-xs text-muted-foreground">
          之后在平台上训练的模型也会保存在这里。
        </p>
      </EmptyContent>
    </Empty>
  )
}

function ModelsSkeleton() {
  return (
    <div className="grid gap-6" aria-busy="true" aria-label="正在加载">
      <div className="grid grid-cols-2 gap-4 lg:grid-cols-4">
        {[0, 1, 2, 3].map((index) => (
          <Skeleton key={index} className="h-[118px] rounded-xl" />
        ))}
      </div>
      <Skeleton className="h-9 w-full max-w-md" />
      <Skeleton className="h-64 rounded-xl" />
    </div>
  )
}
