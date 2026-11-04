/**
 * 模型审图（`/models/reviews`）：所有审图任务，可按项目与关键字筛选。项目与模型的
 * 筛选保存在地址栏中（`?project=12`、`?model=7`），模型页面可以直接链接过来。
 */
import { useMemo, useState } from "react"
import { keepPreviousData, useQuery } from "@tanstack/react-query"
import {
  CircleCheck,
  CircleDashed,
  FolderKanban,
  ImagePlus,
  Plus,
  ScanEye,
  Search,
  SearchX,
  Upload,
  X,
} from "lucide-react"
import { Link, useSearchParams } from "react-router"

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
import { modelsQuery } from "@/features/models/api"
import { modelTitle } from "@/features/models/model-info"
import { projectsQuery, type Project } from "@/features/projects/api"
import {
  ALL_REVIEWS,
  reviewsQuery,
  type Review,
  type ReviewListFilters,
} from "@/features/reviews/api"
import { CreateReviewDialog } from "@/features/reviews/create-review-dialog"
import { parseId } from "@/features/reviews/params"
import { useReviewActions } from "@/features/reviews/review-actions"
import { ReviewList } from "@/features/reviews/review-list"
import { useDebouncedValue } from "@/hooks/use-debounced-value"
import { formatNumber, formatPercent, percentOf } from "@/lib/format"

const nameCollator = new Intl.Collator("zh-CN", { numeric: true })

/** 筛选条件（项目与模型在地址栏中）。 */
function useReviewFilters() {
  const [params, setParams] = useSearchParams()
  const projectId = parseId(params.get("project"))
  const modelId = parseId(params.get("model"))

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
    modelId,
    setProjectId: (id: number | null) =>
      setParam("project", id === null ? null : String(id)),
    setModelId: (id: number | null) =>
      setParam("model", id === null ? null : String(id)),
  }
}

export function ReviewsPage() {
  const [search, setSearch] = useState("")
  const q = useDebouncedValue(search.trim(), 300)
  const [createOpen, setCreateOpen] = useState(false)
  // 每次打开新建对话框换一个 key，表单从头开始。
  const [createKey, setCreateKey] = useState(0)

  const filters = useReviewFilters()
  const projects = useQuery(projectsQuery())
  const { actions, dialogs } = useReviewActions()

  // 不带筛选条件的完整列表用于统计；筛选结果单独查询。
  const all = useQuery(reviewsQuery())
  const currentFilters: ReviewListFilters = {
    ...ALL_REVIEWS,
    q,
    projectId: filters.projectId,
    modelId: filters.modelId,
  }
  const isFiltered =
    q !== "" || filters.projectId !== null || filters.modelId !== null
  const filtered = useQuery({
    ...reviewsQuery(currentFilters),
    placeholderData: keepPreviousData,
    enabled: isFiltered,
  })
  const list = isFiltered ? filtered : all
  const items = list.data?.items ?? []

  const openCreate = () => {
    setCreateKey((key) => key + 1)
    setCreateOpen(true)
  }
  const clearFilters = () => {
    setSearch("")
    filters.setProjectId(null)
    filters.setModelId(null)
  }

  const hasReviews = (all.data?.total ?? 0) > 0
  // 按模型筛选时显示模型的名称：从任务中的模型快照中找。
  const filterModel =
    filters.modelId !== null
      ? all.data?.items.find((review) => review.model.id === filters.modelId)
          ?.model
      : undefined

  return (
    <>
      <PageHeader
        title="模型审图"
        description="用模型识别一批数据集，逐张确认识别结果：标为已审核（识别正确）或需加样（参与后续训练）。可以在网页上审图，也可以下载审图包用本地程序离线审图。"
        actions={
          <Button onClick={openCreate}>
            <Plus />
            新建审图任务
          </Button>
        }
      />

      {all.isPending ? (
        <ReviewsSkeleton />
      ) : all.data === undefined ? (
        // 只在没有数据时显示错误：后台刷新失败时保留已加载的列表。
        <LoadError
          title="无法获取审图任务"
          error={all.error}
          retrying={all.isFetching}
          onRetry={() => all.refetch()}
        />
      ) : !hasReviews ? (
        <ReviewsEmptyState onCreate={openCreate} />
      ) : (
        <>
          <ReviewsSummary reviews={all.data.items} />

          <div className="flex flex-wrap items-center gap-2">
            <InputGroup className="w-full sm:w-72">
              <InputGroupAddon>
                <Search />
              </InputGroupAddon>
              <InputGroupInput
                value={search}
                onChange={(event) => setSearch(event.target.value)}
                placeholder="搜索名称、说明或项目"
                aria-label="搜索审图任务"
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
            {filters.modelId !== null && (
              <Badge variant="secondary" className="h-9 gap-1 pr-1 font-normal">
                模型：
                {filterModel
                  ? `${filterModel.project_name} v${filterModel.version}`
                  : `#${filters.modelId}`}
                <Button
                  variant="ghost"
                  size="icon-xs"
                  onClick={() => filters.setModelId(null)}
                  aria-label="不按模型筛选"
                >
                  <X />
                </Button>
              </Badge>
            )}
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
                <EmptyTitle className="text-base">
                  没有匹配的审图任务
                </EmptyTitle>
                <EmptyDescription>
                  {noMatchText(
                    q,
                    projects.data?.items.find(
                      (project) => project.id === filters.projectId
                    ),
                    filterModel
                      ? modelTitle({
                          project: { name: filterModel.project_name },
                          version: filterModel.version,
                        })
                      : null
                  )}
                </EmptyDescription>
              </EmptyHeader>
              <EmptyContent>
                <div className="flex flex-wrap justify-center gap-2">
                  <Button variant="outline" size="sm" onClick={clearFilters}>
                    清除筛选
                  </Button>
                  <Button size="sm" onClick={openCreate}>
                    <Plus />
                    新建审图任务
                  </Button>
                </div>
              </EmptyContent>
            </Empty>
          ) : list.isPending ? (
            <Skeleton className="h-64 rounded-xl" />
          ) : (
            <ReviewList reviews={items} actions={actions} />
          )}
        </>
      )}

      {dialogs}
      <CreateReviewDialog
        key={createKey}
        open={createOpen}
        onOpenChange={setCreateOpen}
        modelId={filters.modelId}
      />
    </>
  )
}

function noMatchText(
  q: string,
  project: Project | undefined,
  model: string | null
) {
  const conditions = [
    project ? `属于项目“${project.name}”` : null,
    model ? `使用模型“${model}”` : null,
    q ? `名称、说明或项目名包含“${q}”` : null,
  ].filter((part): part is string => part !== null)
  return conditions.length > 0
    ? `没有${conditions.join("、")}的审图任务。`
    : "没有符合条件的审图任务。"
}

/** 按项目（模型所属的项目）筛选。 */
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
      [...(projects ?? [])].sort((a, b) =>
        nameCollator.compare(a.name, b.name)
      ),
    [projects]
  )
  // 从链接进入（?project=ID）时项目列表可能还没加载：先占一个选项。
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

function ReviewsSummary({ reviews }: { reviews: Review[] }) {
  const totals = reviews.reduce(
    (sum, review) => ({
      images: sum.images + review.counts.total,
      pending: sum.pending + review.counts.pending,
      approved: sum.approved + review.counts.approved,
      needsSample: sum.needsSample + review.counts.needs_sample,
      merged: sum.merged + review.counts.merged,
      inferring:
        sum.inferring +
        (review.status === "queued" || review.status === "running" ? 1 : 0),
      failed: sum.failed + (review.status === "failed" ? 1 : 0),
    }),
    {
      images: 0,
      pending: 0,
      approved: 0,
      needsSample: 0,
      merged: 0,
      inferring: 0,
      failed: 0,
    }
  )
  const reviewed = totals.approved + totals.needsSample

  return (
    <div className="grid grid-cols-2 gap-4 lg:grid-cols-4">
      <StatCard
        title="审图任务"
        value={formatNumber(reviews.length)}
        icon={ScanEye}
      >
        <span
          className={
            totals.failed > 0 ? "text-destructive tabular-nums" : "tabular-nums"
          }
        >
          {totals.failed > 0
            ? `${formatNumber(totals.failed)} 个识别失败`
            : totals.inferring > 0
              ? `${formatNumber(totals.inferring)} 个正在识别或排队`
              : `共 ${formatNumber(totals.images)} 张图片`}
        </span>
      </StatCard>
      <StatCard
        title="待审核"
        value={formatNumber(totals.pending)}
        icon={CircleDashed}
      >
        <span className="tabular-nums">
          已审核 {formatPercent(percentOf(reviewed, totals.images))}
        </span>
      </StatCard>
      <StatCard
        title="已审核"
        value={formatNumber(totals.approved)}
        icon={CircleCheck}
      >
        <span>识别正确，不需要再学习</span>
      </StatCard>
      <StatCard
        title="需加样"
        value={formatNumber(totals.needsSample)}
        icon={ImagePlus}
      >
        <span className="tabular-nums">
          {totals.merged > 0
            ? `${formatNumber(totals.merged)} 张已合并到数据集`
            : "参与后续的训练"}
        </span>
      </StatCard>
    </div>
  )
}

function ReviewsEmptyState({ onCreate }: { onCreate: () => void }) {
  const models = useQuery(modelsQuery())
  const noModels = models.data !== undefined && models.data.items.length === 0
  return (
    <Empty className="border bg-card">
      <EmptyHeader className="max-w-lg">
        <EmptyMedia variant="icon">
          <ScanEye />
        </EmptyMedia>
        <EmptyTitle>还没有审图任务</EmptyTitle>
        <EmptyDescription>
          {noModels
            ? "审图需要一个模型：先在模型仓库上传训练好的检测模型，再选择要审核的数据集。"
            : "选择一个模型与要审核的数据集，训练节点会自动识别所有图片；之后逐张确认识别结果，需要补充训练的图片标为“需加样”。"}
        </EmptyDescription>
      </EmptyHeader>
      <EmptyContent>
        {noModels ? (
          <Button asChild>
            <Link to="/models">
              <Upload />
              前往模型仓库
            </Link>
          </Button>
        ) : (
          <Button onClick={onCreate}>
            <Plus />
            新建审图任务
          </Button>
        )}
        <p className="text-xs text-muted-foreground">
          识别结果只保存在审图任务中，确认后可以选择合并到数据集的标注。
        </p>
      </EmptyContent>
    </Empty>
  )
}

function ReviewsSkeleton() {
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
