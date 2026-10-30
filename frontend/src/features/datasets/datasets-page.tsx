import { useMemo, useState } from "react"
import { keepPreviousData, useQuery } from "@tanstack/react-query"
import {
  Database,
  FolderOpen,
  HardDrive,
  Images,
  LayoutGrid,
  List,
  Plus,
  Search,
  SearchX,
  SquareDashed,
  Tags,
  Upload,
  X,
} from "lucide-react"

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
import { datasetsQuery, type Dataset } from "@/features/datasets/api"
import {
  CreateDatasetDialog,
  type CreateTab,
} from "@/features/datasets/create-dataset-dialog"
import {
  DeleteDatasetDialog,
  EditDatasetDialog,
  useScanDataset,
} from "@/features/datasets/dataset-actions"
import { DatasetCard } from "@/features/datasets/dataset-card"
import { DatasetTable } from "@/features/datasets/dataset-table"
import { DatasetsRootHint } from "@/features/datasets/directory-picker"
import { LoadError } from "@/features/datasets/load-error"
import { useDatasetsRoot } from "@/features/datasets/use-datasets-root"
import { useSystemInfo } from "@/features/system/api"
import { useDebouncedValue } from "@/hooks/use-debounced-value"
import {
  formatBytes,
  formatNumber,
  formatPercent,
  percentOf,
} from "@/lib/format"
import { readPreference, writePreference } from "@/lib/storage"

type SortKey = "updated" | "name" | "images"
type ViewMode = "grid" | "list"

const sortOptions: { value: SortKey; label: string }[] = [
  { value: "updated", label: "最近更新" },
  { value: "name", label: "名称" },
  { value: "images", label: "图片数量" },
]

const nameCollator = new Intl.Collator("zh-CN", { numeric: true })

function sortDatasets(datasets: Dataset[], sort: SortKey) {
  const sorted = [...datasets]
  switch (sort) {
    case "name":
      return sorted.sort((a, b) => nameCollator.compare(a.name, b.name))
    case "images":
      return sorted.sort((a, b) => b.image_count - a.image_count)
    default:
      // 服务端已按更新时间倒序返回。
      return sorted
  }
}

const VIEW_STORAGE_KEY = "trainmaster-datasets-view"

function readViewMode(): ViewMode {
  return readPreference(VIEW_STORAGE_KEY) === "list" ? "list" : "grid"
}

export function DatasetsPage() {
  const [search, setSearch] = useState("")
  const q = useDebouncedValue(search.trim(), 300)
  const [sort, setSort] = useState<SortKey>("updated")
  const [view, setView] = useState<ViewMode>(readViewMode)

  const [createOpen, setCreateOpen] = useState(false)
  const [createTab, setCreateTab] = useState<CreateTab>("archive")
  // 每次打开对话框换一个 key，表单从头开始。
  const [createKey, setCreateKey] = useState(0)
  const [editing, setEditing] = useState<Dataset | null>(null)
  const [deleting, setDeleting] = useState<Dataset | null>(null)
  const scan = useScanDataset()

  // 不带搜索条件的完整列表用于统计；搜索结果单独查询。
  const all = useQuery(datasetsQuery())
  const filtered = useQuery({
    ...datasetsQuery(q),
    placeholderData: keepPreviousData,
  })

  const items = useMemo(
    () => sortDatasets(filtered.data?.items ?? [], sort),
    [filtered.data, sort]
  )

  const openCreate = (tab: CreateTab) => {
    setCreateTab(tab)
    setCreateKey((key) => key + 1)
    setCreateOpen(true)
  }

  const changeView = (value: string) => {
    if (value === "grid" || value === "list") {
      setView(value)
      writePreference(VIEW_STORAGE_KEY, value)
    }
  }

  const hasDatasets = (all.data?.total ?? 0) > 0

  return (
    <>
      <PageHeader
        title="数据集"
        description="上传图片压缩包或文件夹，或把服务器上已有的文件夹添加为数据集；同目录下的 VOC 标注会被自动识别。"
        actions={
          <>
            <Button variant="outline" onClick={() => openCreate("directory")}>
              <FolderOpen />
              添加已有目录
            </Button>
            <Button onClick={() => openCreate("archive")}>
              <Plus />
              新建数据集
            </Button>
          </>
        }
      />

      {all.isPending ? (
        <DatasetsSkeleton />
      ) : all.data === undefined ? (
        // 只在没有数据时显示错误：后台刷新失败时保留已加载的列表。
        <LoadError
          title="无法获取数据集列表"
          error={all.error}
          retrying={all.isFetching}
          onRetry={() => all.refetch()}
        />
      ) : !hasDatasets ? (
        <DatasetsEmptyState
          onUpload={() => openCreate("archive")}
          onAddDirectory={() => openCreate("directory")}
        />
      ) : (
        <>
          <DatasetsSummary datasets={all.data.items} />

          <div className="flex flex-wrap items-center gap-2">
            <InputGroup className="w-full sm:w-72">
              <InputGroupAddon>
                <Search />
              </InputGroupAddon>
              <InputGroupInput
                value={search}
                onChange={(event) => setSearch(event.target.value)}
                placeholder="搜索名称或描述"
                aria-label="搜索数据集"
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
                <EmptyTitle className="text-base">没有匹配的数据集</EmptyTitle>
                <EmptyDescription>
                  没有名称或描述包含“{q}”的数据集。
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
              {items.map((dataset) => (
                <DatasetCard
                  key={dataset.id}
                  dataset={dataset}
                  onScan={() => scan.mutate(dataset)}
                  onEdit={() => setEditing(dataset)}
                  onDelete={() => setDeleting(dataset)}
                />
              ))}
            </div>
          ) : (
            <DatasetTable
              datasets={items}
              onScan={(dataset) => scan.mutate(dataset)}
              onEdit={setEditing}
              onDelete={setDeleting}
            />
          )}
        </>
      )}

      <CreateDatasetDialog
        key={createKey}
        open={createOpen}
        onOpenChange={setCreateOpen}
        initialTab={createTab}
      />
      <EditDatasetDialog
        dataset={editing}
        onOpenChange={(open) => !open && setEditing(null)}
      />
      <DeleteDatasetDialog
        dataset={deleting}
        onOpenChange={(open) => !open && setDeleting(null)}
      />
    </>
  )
}

function DatasetsSummary({ datasets }: { datasets: Dataset[] }) {
  const { data: systemInfo } = useSystemInfo()
  const totals = datasets.reduce(
    (sum, dataset) => ({
      images: sum.images + dataset.image_count,
      annotated: sum.annotated + dataset.annotated_count,
      objects: sum.objects + dataset.object_count,
      bytes: sum.bytes + dataset.total_bytes,
      uploads: sum.uploads + (dataset.origin === "upload" ? 1 : 0),
    }),
    { images: 0, annotated: 0, objects: 0, bytes: 0, uploads: 0 }
  )
  const percent = percentOf(totals.annotated, totals.images)
  const freeBytes =
    systemInfo && systemInfo.storage.total_bytes > 0
      ? systemInfo.storage.free_bytes
      : null

  return (
    <div className="grid grid-cols-2 gap-4 lg:grid-cols-4">
      <StatCard
        title="数据集"
        value={formatNumber(datasets.length)}
        icon={Database}
      >
        <span>
          上传 {formatNumber(totals.uploads)} · 已有目录{" "}
          {formatNumber(datasets.length - totals.uploads)}
        </span>
      </StatCard>
      <StatCard
        title="图片总数"
        value={formatNumber(totals.images)}
        icon={Images}
      >
        <span className="flex items-center gap-1">
          <Tags className="size-3" />
          标注目标 {formatNumber(totals.objects)} 个
        </span>
      </StatCard>
      <StatCard
        title="标注进度"
        value={formatPercent(percent)}
        icon={SquareDashed}
      >
        <Progress
          value={percent}
          className="h-1.5"
          aria-label={`已标注 ${formatPercent(percent)}`}
        />
        <span className="tabular-nums">
          已标注 {formatNumber(totals.annotated)} 张
        </span>
      </StatCard>
      <StatCard
        title="占用空间"
        value={formatBytes(totals.bytes)}
        icon={HardDrive}
      >
        <span>
          {freeBytes !== null
            ? `磁盘剩余 ${formatBytes(freeBytes)}`
            : "图片文件的总大小"}
        </span>
      </StatCard>
    </div>
  )
}

function DatasetsEmptyState({
  onUpload,
  onAddDirectory,
}: {
  onUpload: () => void
  onAddDirectory: () => void
}) {
  const root = useDatasetsRoot()

  return (
    <Empty className="border bg-card">
      <EmptyHeader className="max-w-lg">
        <EmptyMedia variant="icon">
          <Database />
        </EmptyMedia>
        <EmptyTitle>还没有数据集</EmptyTitle>
        <EmptyDescription>
          数据集就是服务器上的一个图片文件夹，图片旁边同名的 VOC
          标注（.xml）会被自动识别。可以通过下面两种方式创建：
        </EmptyDescription>
      </EmptyHeader>
      <EmptyContent className="max-w-2xl">
        <div className="grid w-full gap-3 text-left text-wrap sm:grid-cols-2">
          <div className="flex flex-col gap-3 rounded-xl border bg-background p-4">
            <div className="flex items-center gap-2 font-medium">
              <Upload className="size-4 text-muted-foreground" />
              上传图片
            </div>
            <p className="flex-1 text-sm text-muted-foreground">
              上传 zip、tar、7z、rar
              等压缩包，或直接选择整个文件夹。平台会在数据目录下按数据集名称新建文件夹。
            </p>
            <Button onClick={onUpload} className="self-start">
              <Plus />
              新建数据集
            </Button>
          </div>
          <div className="flex flex-col gap-3 rounded-xl border bg-background p-4">
            <div className="flex items-center gap-2 font-medium">
              <FolderOpen className="size-4 text-muted-foreground" />
              使用服务器上的文件夹
            </div>
            <div className="flex-1">
              <p className="text-sm text-muted-foreground">
                文件较多时，可以先把文件夹直接拷贝到服务器的数据目录，再添加为数据集，文件不会被移动或复制。
              </p>
              <DatasetsRootHint root={root} className="mt-2" />
            </div>
            <Button
              variant="outline"
              onClick={onAddDirectory}
              className="self-start"
            >
              <FolderOpen />
              添加已有目录
            </Button>
          </div>
        </div>
      </EmptyContent>
    </Empty>
  )
}

function DatasetsSkeleton() {
  return (
    <div className="grid gap-6" aria-busy="true" aria-label="正在加载">
      <div className="grid grid-cols-2 gap-4 lg:grid-cols-4">
        {[0, 1, 2, 3].map((index) => (
          <Skeleton key={index} className="h-[118px] rounded-xl" />
        ))}
      </div>
      <Skeleton className="h-9 w-full max-w-md" />
      <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3 2xl:grid-cols-4">
        {[0, 1, 2, 3, 4, 5].map((index) => (
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
