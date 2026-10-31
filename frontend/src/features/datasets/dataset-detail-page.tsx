import { useState } from "react"
import { useQuery } from "@tanstack/react-query"
import {
  ArrowLeft,
  Check,
  Clock,
  Copy,
  DatabaseZap,
  Download,
  FolderKanban,
  FolderX,
  ImagePlus,
  Images,
  RefreshCw,
  SquareDashed,
  Tags,
} from "lucide-react"
import { Link, useNavigate, useParams, useSearchParams } from "react-router"
import { toast } from "sonner"

import { PageHeader } from "@/components/page-header"
import { StatCard } from "@/components/stat-card"
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
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
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@/components/ui/tooltip"
import {
  datasetQuery,
  type Dataset,
  type ImageFilters,
  type ImageSort,
  type ImageStatusFilter,
} from "@/features/datasets/api"
import {
  DatasetActionsMenu,
  DeleteDatasetDialog,
  EditDatasetDialog,
  useScanDataset,
} from "@/features/datasets/dataset-actions"
import { originLabels } from "@/features/datasets/dataset-card"
import {
  DatasetImages,
  DEFAULT_IMAGE_FILTERS,
} from "@/features/datasets/dataset-images"
import { DatasetImports } from "@/features/datasets/dataset-imports"
import { DatasetLabels } from "@/features/datasets/dataset-labels"
import {
  canExportDataset,
  ExportDialog,
} from "@/features/datasets/export-dialog"
import { ImportDialog } from "@/features/datasets/import-dialog"
import { ImportProgressCard } from "@/features/datasets/import-progress-card"
import { LoadError } from "@/features/datasets/load-error"
import { DatasetProjectBadges } from "@/features/projects/dataset-project-badges"
import { DatasetProjectsDialog } from "@/features/projects/dataset-projects-dialog"
import { usePageTitle } from "@/hooks/use-page-title"
import { ApiError } from "@/lib/api"
import { copyText } from "@/lib/clipboard"
import {
  formatBytes,
  formatDateTime,
  formatNumber,
  formatPercent,
  formatRelativeTime,
  percentOf,
} from "@/lib/format"

/** 路径参数必须是正整数（与后端一致），否则视为不存在。 */
function parseDatasetId(value: string | undefined) {
  if (!value || !/^[1-9]\d{0,15}$/.test(value)) {
    return null
  }
  const id = Number(value)
  return Number.isSafeInteger(id) ? id : null
}

export function DatasetDetailPage() {
  const { datasetId } = useParams()
  const id = parseDatasetId(datasetId)

  if (id === null) {
    return <DatasetNotFound />
  }
  // 切换数据集时整体重置页面状态（对话框、查看器等）。
  return <DatasetDetail key={id} id={id} />
}

function DatasetNotFound() {
  usePageTitle("数据集不存在")

  return (
    <Empty className="min-h-[60svh]">
      <EmptyHeader>
        <EmptyMedia variant="icon">
          <DatabaseZap />
        </EmptyMedia>
        <EmptyTitle>数据集不存在</EmptyTitle>
        <EmptyDescription>
          该数据集可能已被删除，或者链接有误。
        </EmptyDescription>
      </EmptyHeader>
      <EmptyContent>
        <Button asChild>
          <Link to="/datasets">
            <ArrowLeft />
            返回数据集列表
          </Link>
        </Button>
      </EmptyContent>
    </Empty>
  )
}

type DetailTab = "images" | "labels" | "imports"

const STATUS_VALUES: ImageStatusFilter[] = [
  "all",
  "annotated",
  "empty",
  "unannotated",
  "invalid",
]
const SORT_VALUES: ImageSort[] = ["path", "-mod_time", "-objects"]

/** 标签页与图片筛选条件保存在地址栏中，刷新、分享链接、前进后退都能保留。 */
function useDetailSearchParams() {
  const [params, setParams] = useSearchParams()

  const tabParam = params.get("tab")
  const tab: DetailTab =
    tabParam === "labels" || tabParam === "imports" ? tabParam : "images"
  const status = params.get("status") as ImageStatusFilter | null
  const sort = params.get("sort") as ImageSort | null
  const filters: ImageFilters = {
    status: status && STATUS_VALUES.includes(status) ? status : "all",
    label: params.get("label") ?? "",
    q: params.get("q") ?? "",
    sort: sort && SORT_VALUES.includes(sort) ? sort : "path",
  }

  const update = (changes: Record<string, string>) => {
    setParams(
      (previous) => {
        const next = new URLSearchParams(previous)
        for (const [key, value] of Object.entries(changes)) {
          // 默认值不写进地址栏，保持链接简洁。
          const isDefault =
            value === "" ||
            (key === "tab" && value === "images") ||
            (key in DEFAULT_IMAGE_FILTERS &&
              DEFAULT_IMAGE_FILTERS[key as keyof ImageFilters] === value)
          if (isDefault) {
            next.delete(key)
          } else {
            next.set(key, value)
          }
        }
        return next
      },
      { replace: true }
    )
  }

  return {
    tab,
    filters,
    setTab: (value: DetailTab) => update({ tab: value }),
    setFilters: (changes: Partial<ImageFilters>) => update(changes),
    showLabel: (label: string) =>
      update({ tab: "images", label, status: "all", q: "" }),
  }
}

function DatasetDetail({ id }: { id: number }) {
  const navigate = useNavigate()
  const query = useQuery(datasetQuery(id))
  const search = useDetailSearchParams()
  const scan = useScanDataset()

  const [importOpen, setImportOpen] = useState(false)
  const [importKey, setImportKey] = useState(0)
  const [editing, setEditing] = useState(false)
  const [deleting, setDeleting] = useState(false)
  const [assigning, setAssigning] = useState(false)
  const [exporting, setExporting] = useState(false)

  usePageTitle(query.data?.name)

  if (query.isPending) {
    return <DetailSkeleton />
  }
  if (query.error instanceof ApiError && query.error.status === 404) {
    return <DatasetNotFound />
  }
  // 后台刷新失败时继续显示已加载的数据，不卸载打开着的对话框、查看器。
  if (query.data === undefined) {
    return (
      <LoadError
        title="无法获取数据集"
        error={query.error}
        retrying={query.isFetching}
        onRetry={() => query.refetch()}
      />
    )
  }

  const dataset = query.data
  const busy = dataset.active_import !== null || scan.isPending
  const openImport = () => {
    setImportKey((key) => key + 1)
    setImportOpen(true)
  }

  return (
    <>
      {/* grid-cols-1 = minmax(0, 1fr): a long folder path must not widen the page. */}
      <div className="grid grid-cols-1 gap-3">
        <PageHeader
          title={dataset.name}
          description={dataset.description || undefined}
          actions={
            <>
              <Button
                variant="outline"
                onClick={() => scan.mutate(dataset)}
                disabled={busy}
              >
                <RefreshCw className={scan.isPending ? "animate-spin" : ""} />
                重新扫描
              </Button>
              <Button
                variant="outline"
                onClick={() => setExporting(true)}
                disabled={!canExportDataset(dataset)}
                aria-label="下载"
              >
                <Download />
                {/* 手机宽度下只显示图标，让操作按钮保持一行。 */}
                <span className="hidden sm:inline">下载</span>
              </Button>
              <Button onClick={openImport} disabled={dataset.directory_missing}>
                <ImagePlus />
                导入数据
              </Button>
              <DatasetActionsMenu
                dataset={dataset}
                onEdit={() => setEditing(true)}
                onDelete={() => setDeleting(true)}
                onManageProjects={() => setAssigning(true)}
                className="size-9"
              />
            </>
          }
        />
        <DatasetMeta
          dataset={dataset}
          onManageProjects={() => setAssigning(true)}
        />
      </div>

      {dataset.directory_missing && (
        <Alert variant="destructive">
          <FolderX />
          <AlertTitle>数据集文件夹不存在</AlertTitle>
          <AlertDescription>
            <p>
              磁盘上找不到{" "}
              <code className="font-mono break-all">{dataset.path}</code>
              ，它可能已被移动、重命名或删除。恢复文件夹后点击“重新扫描”；如果不再需要，可以删除该数据集。
            </p>
          </AlertDescription>
        </Alert>
      )}

      <ImportProgressCard dataset={dataset} />

      <DatasetStats dataset={dataset} />

      <Tabs
        value={search.tab}
        onValueChange={(value) => search.setTab(value as DetailTab)}
        className="gap-4"
      >
        <TabsList className="w-full sm:w-fit">
          <TabsTrigger value="images" className="px-3">
            图片
            <span className="text-xs text-muted-foreground tabular-nums">
              {formatNumber(dataset.image_count)}
            </span>
          </TabsTrigger>
          <TabsTrigger value="labels" className="px-3">
            标签分布
            <span className="text-xs text-muted-foreground tabular-nums">
              {formatNumber(dataset.label_count)}
            </span>
          </TabsTrigger>
          <TabsTrigger value="imports" className="px-3">
            导入记录
          </TabsTrigger>
        </TabsList>
        <TabsContent value="images">
          <DatasetImages
            dataset={dataset}
            filters={search.filters}
            onFiltersChange={search.setFilters}
            onImport={openImport}
          />
        </TabsContent>
        <TabsContent value="labels">
          <DatasetLabels dataset={dataset} onSelectLabel={search.showLabel} />
        </TabsContent>
        <TabsContent value="imports">
          <DatasetImports dataset={dataset} />
        </TabsContent>
      </Tabs>

      <ImportDialog
        key={importKey}
        dataset={dataset}
        open={importOpen}
        onOpenChange={setImportOpen}
      />
      <EditDatasetDialog
        dataset={editing ? dataset : null}
        onOpenChange={setEditing}
      />
      <DeleteDatasetDialog
        dataset={deleting ? dataset : null}
        onOpenChange={setDeleting}
        onDeleted={() => navigate("/datasets", { replace: true })}
      />
      <DatasetProjectsDialog
        dataset={assigning ? dataset : null}
        onOpenChange={setAssigning}
      />
      <ExportDialog
        dataset={exporting ? dataset : null}
        onOpenChange={setExporting}
      />
    </>
  )
}

function DatasetMeta({
  dataset,
  onManageProjects,
}: {
  dataset: Dataset
  onManageProjects: () => void
}) {
  const [copied, setCopied] = useState(false)

  const copyPath = async () => {
    if (await copyText(dataset.path)) {
      setCopied(true)
      setTimeout(() => setCopied(false), 1500)
    } else {
      toast.error("复制失败，请手动选择路径复制")
    }
  }

  return (
    <div className="flex flex-wrap items-center gap-x-4 gap-y-2 text-xs text-muted-foreground">
      <Badge variant="outline" className="font-normal">
        {originLabels[dataset.origin]}
      </Badge>
      <span className="inline-flex max-w-full min-w-0 items-center gap-1">
        <code className="truncate rounded bg-muted px-1.5 py-0.5 font-mono text-foreground">
          {dataset.path}
        </code>
        <Tooltip>
          <TooltipTrigger asChild>
            <Button
              variant="ghost"
              size="icon-xs"
              onClick={() => void copyPath()}
              aria-label="复制文件夹路径"
            >
              {copied ? <Check /> : <Copy />}
            </Button>
          </TooltipTrigger>
          <TooltipContent>{copied ? "已复制" : "复制路径"}</TooltipContent>
        </Tooltip>
      </span>
      <span className="inline-flex min-w-0 items-center gap-1.5">
        <DatasetProjectBadges projects={dataset.projects} max={3} />
        <Button
          variant="ghost"
          size="xs"
          onClick={onManageProjects}
          className="text-muted-foreground"
        >
          <FolderKanban />
          {dataset.projects.length > 0 ? "管理项目" : "纳入项目"}
        </Button>
      </span>
      <span title={formatDateTime(dataset.created_at)}>
        创建于 {formatRelativeTime(dataset.created_at)}
      </span>
      <span title={formatDateTime(dataset.updated_at)}>
        更新于 {formatRelativeTime(dataset.updated_at)}
      </span>
    </div>
  )
}

function DatasetStats({ dataset }: { dataset: Dataset }) {
  const percent = percentOf(dataset.annotated_count, dataset.image_count)

  return (
    <div className="grid grid-cols-2 gap-4 lg:grid-cols-4">
      <StatCard
        title="图片"
        value={formatNumber(dataset.image_count)}
        icon={Images}
      >
        <span className="tabular-nums">
          共 {formatBytes(dataset.total_bytes)}
        </span>
      </StatCard>
      <StatCard
        title="已标注"
        value={formatNumber(dataset.annotated_count)}
        icon={SquareDashed}
      >
        <Progress
          value={percent}
          className="h-1.5"
          aria-label={`标注率 ${formatPercent(percent)}`}
        />
        <span className="flex flex-wrap justify-between gap-x-2 tabular-nums">
          <span>标注率 {formatPercent(percent)}</span>
          <span>空标注 {formatNumber(dataset.empty_count)}</span>
        </span>
      </StatCard>
      <StatCard
        title="标注目标"
        value={formatNumber(dataset.object_count)}
        icon={Tags}
      >
        <span className="tabular-nums">
          {formatNumber(dataset.label_count)} 种标签
        </span>
      </StatCard>
      <StatCard
        title="最近扫描"
        value={
          dataset.scanned_at
            ? formatRelativeTime(dataset.scanned_at)
            : "尚未扫描"
        }
        icon={Clock}
      >
        <span className="tabular-nums">
          {dataset.scanned_at
            ? formatDateTime(dataset.scanned_at)
            : "导入或扫描完成后更新"}
        </span>
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
      <div className="grid grid-cols-2 gap-3 sm:grid-cols-3 md:grid-cols-4 lg:grid-cols-5 xl:grid-cols-6">
        {Array.from({ length: 12 }, (_, index) => (
          <Skeleton key={index} className="aspect-square rounded-lg" />
        ))}
      </div>
    </div>
  )
}
