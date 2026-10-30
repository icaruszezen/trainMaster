import { useEffect, useMemo, useRef, useState } from "react"
import { useInfiniteQuery, useQuery } from "@tanstack/react-query"
import {
  ImagePlus,
  Images,
  Search,
  SearchX,
  SquareDashed,
  X,
} from "lucide-react"

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
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { Skeleton } from "@/components/ui/skeleton"
import { Spinner } from "@/components/ui/spinner"
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group"
import {
  imagesQuery,
  labelsQuery,
  type Dataset,
  type DatasetImage,
  type ImageFilters,
  type ImageSort,
  type ImageStatusFilter,
} from "@/features/datasets/api"
import { DatasetThumbnail } from "@/features/datasets/dataset-thumbnail"
import { ImageViewerDialog } from "@/features/datasets/image-viewer-dialog"
import {
  annotationStateLabels,
  flattenImagePages,
  imageStatusOptions,
  loadMoreImages,
} from "@/features/datasets/images"
import { labelColor } from "@/features/datasets/label-colors"
import { LoadError } from "@/features/datasets/load-error"
import { useActiveUploadTask } from "@/features/datasets/upload-manager"
import { formatNumber } from "@/lib/format"
import { cn } from "@/lib/utils"

export const DEFAULT_IMAGE_FILTERS: ImageFilters = {
  status: "all",
  label: "",
  q: "",
  sort: "path",
}

const sortOptions: { value: ImageSort; label: string }[] = [
  { value: "path", label: "按路径" },
  { value: "-mod_time", label: "最近修改" },
  { value: "-objects", label: "目标最多" },
]

// Radix Select 的值不能为空字符串；标签名加前缀，避免与“全部”冲突。
const ALL_LABELS = "all"
const labelValue = (name: string) => `label:${name}`

type DatasetImagesProps = {
  dataset: Dataset
  filters: ImageFilters
  onFiltersChange: (changes: Partial<ImageFilters>) => void
  onImport: () => void
}

export function DatasetImages({
  dataset,
  filters,
  onFiltersChange,
  onImport,
}: DatasetImagesProps) {
  const [viewerIndex, setViewerIndex] = useState<number | null>(null)
  const query = useInfiniteQuery(imagesQuery(dataset.id, filters))
  const labels = useQuery(labelsQuery(dataset.id))

  const images = useMemo(
    () => flattenImagePages(query.data?.pages),
    [query.data]
  )
  const total = query.data?.pages[0]?.total ?? 0
  // 图片变少（如重新扫描后）时，查看器的位置可能已超出范围：关闭查看器，
  // 避免之后加载更多时它自己弹出来。
  if (viewerIndex !== null && viewerIndex >= images.length) {
    setViewerIndex(null)
  }
  const filtered =
    filters.status !== "all" || filters.label !== "" || filters.q !== ""
  const uploadTask = useActiveUploadTask(dataset.id)
  const importing = dataset.active_import !== null || uploadTask !== undefined

  const statusOptions = imageStatusOptions(dataset, filters.status)

  const labelItems = labels.data?.items ?? []
  const labelMissing =
    filters.label !== "" && !labelItems.some((l) => l.name === filters.label)

  return (
    <div className="grid gap-4">
      {/* 数据集还没有图片时只显示下面的引导，不显示用不上的筛选栏。 */}
      {(dataset.image_count > 0 || filtered) && (
        <div className="flex flex-wrap items-center gap-2">
          <ImageSearch
            value={filters.q}
            onChange={(q) => onFiltersChange({ q })}
          />
          <ToggleGroup
            type="single"
            variant="outline"
            value={filters.status}
            onValueChange={(value) =>
              value && onFiltersChange({ status: value as ImageStatusFilter })
            }
            className="hidden md:flex"
            aria-label="标注状态"
          >
            {statusOptions.map((option) => (
              <ToggleGroupItem
                key={option.value}
                value={option.value}
                className="gap-1.5"
              >
                {option.label}
                <span
                  className={cn(
                    "text-xs text-muted-foreground tabular-nums",
                    option.value === "invalid" &&
                      option.count > 0 &&
                      "text-destructive"
                  )}
                >
                  {formatNumber(option.count)}
                </span>
              </ToggleGroupItem>
            ))}
          </ToggleGroup>
          <Select
            value={filters.status}
            onValueChange={(value) =>
              onFiltersChange({ status: value as ImageStatusFilter })
            }
          >
            <SelectTrigger
              className="w-[calc(50%-0.25rem)] md:hidden"
              aria-label="标注状态"
            >
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {statusOptions.map((option) => (
                <SelectItem key={option.value} value={option.value}>
                  {option.label}（{formatNumber(option.count)}）
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          <Select
            value={filters.label ? labelValue(filters.label) : ALL_LABELS}
            onValueChange={(value) =>
              onFiltersChange({
                label: value === ALL_LABELS ? "" : value.slice("label:".length),
              })
            }
            disabled={labelItems.length === 0 && !filters.label}
          >
            <SelectTrigger
              className="w-[calc(50%-0.25rem)] sm:w-40"
              aria-label="按标签筛选"
            >
              <SelectValue placeholder="全部标签" />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value={ALL_LABELS}>全部标签</SelectItem>
              {labelMissing && (
                <SelectItem value={labelValue(filters.label)}>
                  {filters.label}
                </SelectItem>
              )}
              {labelItems.map((label) => (
                <SelectItem key={label.name} value={labelValue(label.name)}>
                  <span
                    className="size-2 rounded-full"
                    style={{ backgroundColor: labelColor(label.name) }}
                    aria-hidden
                  />
                  {label.name}
                  <span className="text-muted-foreground tabular-nums">
                    {formatNumber(label.image_count)}
                  </span>
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          <Select
            value={filters.sort}
            onValueChange={(value) =>
              onFiltersChange({ sort: value as ImageSort })
            }
          >
            <SelectTrigger
              className="w-[calc(50%-0.25rem)] sm:w-32"
              aria-label="排序方式"
            >
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
          <p className="ml-auto flex items-center gap-2 text-sm text-muted-foreground tabular-nums">
            {query.isFetching && !query.isFetchingNextPage && (
              <Spinner className="size-3.5" aria-hidden />
            )}
            {query.data &&
              (filtered
                ? `筛选出 ${formatNumber(total)} 张`
                : `共 ${formatNumber(total)} 张`)}
          </p>
        </div>
      )}

      {query.isPending ? (
        <ImageGridSkeleton />
      ) : query.error && images.length === 0 ? (
        <LoadError
          title="无法获取图片列表"
          error={query.error}
          retrying={query.isFetching}
          onRetry={() => query.refetch()}
        />
      ) : images.length === 0 ? (
        filtered ? (
          <Empty className="border">
            <EmptyHeader>
              <EmptyMedia variant="icon">
                <SearchX />
              </EmptyMedia>
              <EmptyTitle className="text-base">没有符合条件的图片</EmptyTitle>
              <EmptyDescription>换个筛选条件试试。</EmptyDescription>
            </EmptyHeader>
            <EmptyContent>
              <Button
                variant="outline"
                size="sm"
                onClick={() =>
                  onFiltersChange({ status: "all", label: "", q: "" })
                }
              >
                清除筛选
              </Button>
            </EmptyContent>
          </Empty>
        ) : (
          <Empty className="border">
            <EmptyHeader>
              <EmptyMedia variant="icon">
                {importing ? <Spinner /> : <Images />}
              </EmptyMedia>
              <EmptyTitle className="text-base">
                {importing ? "正在导入图片" : "还没有图片"}
              </EmptyTitle>
              <EmptyDescription>
                {importing
                  ? "导入完成后，图片会显示在这里。"
                  : "上传压缩包或文件夹导入图片；如果文件是直接拷贝到数据集文件夹中的，点击“重新扫描”即可纳入。"}
              </EmptyDescription>
            </EmptyHeader>
            {!importing && !dataset.directory_missing && (
              <EmptyContent>
                <Button size="sm" onClick={onImport}>
                  <ImagePlus />
                  导入数据
                </Button>
              </EmptyContent>
            )}
          </Empty>
        )
      ) : (
        <>
          <ul
            className={cn(
              "grid grid-cols-2 gap-3 sm:grid-cols-3 md:grid-cols-4 lg:grid-cols-5 xl:grid-cols-6",
              query.isPlaceholderData && "opacity-60 transition-opacity"
            )}
          >
            {images.map((image, index) => (
              <li key={image.id}>
                <ImageTile
                  datasetId={dataset.id}
                  image={image}
                  onOpen={() => setViewerIndex(index)}
                />
              </li>
            ))}
          </ul>
          <LoadMore
            hasMore={query.hasNextPage}
            loading={query.isFetchingNextPage}
            error={query.isFetchNextPageError ? query.error : null}
            onLoadMore={() => void query.fetchNextPage()}
            loaded={images.length}
            total={total}
          />
        </>
      )}

      <ImageViewerDialog
        datasetId={dataset.id}
        images={images}
        index={viewerIndex}
        onIndexChange={setViewerIndex}
        total={total}
        hasMore={query.hasNextPage}
        onLoadMore={() => loadMoreImages(() => query.fetchNextPage())}
      />
    </div>
  )
}

/** 搜索框：输入停止 300 ms 后才更新筛选条件（以及地址栏）。 */
function ImageSearch({
  value,
  onChange,
}: {
  value: string
  onChange: (value: string) => void
}) {
  const [text, setText] = useState(value)
  const [lastValue, setLastValue] = useState(value)
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined)
  // 定时器触发时调用最新的 onChange：它基于当前的地址栏参数，用按键时的旧闭包
  // 会把这 300 ms 内改过的其它筛选条件改回去。
  const latest = useRef(onChange)
  useEffect(() => {
    latest.current = onChange
  })

  // 外部清除筛选（或浏览器前进 / 后退）时同步输入框。
  if (value !== lastValue) {
    setLastValue(value)
    setText(value)
  }

  useEffect(() => () => clearTimeout(timer.current), [])

  const update = (next: string, immediate = false) => {
    setText(next)
    clearTimeout(timer.current)
    if (immediate) {
      onChange(next.trim())
    } else {
      timer.current = setTimeout(() => latest.current(next.trim()), 300)
    }
  }

  return (
    <InputGroup className="w-full md:w-56">
      <InputGroupAddon>
        <Search />
      </InputGroupAddon>
      <InputGroupInput
        value={text}
        onChange={(event) => update(event.target.value)}
        placeholder="搜索文件名或路径"
        aria-label="搜索图片"
      />
      {text && (
        <InputGroupAddon align="inline-end">
          <InputGroupButton
            size="icon-xs"
            onClick={() => update("", true)}
            aria-label="清除搜索"
          >
            <X />
          </InputGroupButton>
        </InputGroupAddon>
      )}
    </InputGroup>
  )
}

function ImageTile({
  datasetId,
  image,
  onOpen,
}: {
  datasetId: number
  image: DatasetImage
  onOpen: () => void
}) {
  const { annotation } = image
  const labels = annotation?.labels ?? []
  const state = image.annotation_state

  return (
    <button
      type="button"
      onClick={onOpen}
      className="group relative block aspect-square w-full overflow-hidden rounded-lg border bg-muted text-left transition-shadow outline-none hover:shadow-md focus-visible:ring-[3px] focus-visible:ring-ring/50"
      aria-label={`查看 ${image.path}`}
      title={image.path}
    >
      <DatasetThumbnail
        datasetId={datasetId}
        image={image}
        fileName={image.file_name}
        className="size-full transition-transform duration-300 ease-out group-hover:scale-[1.04] motion-reduce:transition-none"
      />
      <span className="absolute top-1.5 right-1.5">
        {state === "annotated" && annotation ? (
          <span className="inline-flex items-center gap-1 rounded-md bg-background/90 px-1.5 py-0.5 text-[11px] font-medium tabular-nums shadow-xs backdrop-blur">
            <SquareDashed className="size-3" aria-hidden />
            {formatNumber(annotation.object_count)}
          </span>
        ) : state === "invalid" ? (
          <span className="rounded-md bg-destructive px-1.5 py-0.5 text-[11px] font-medium text-white shadow-xs backdrop-blur dark:bg-destructive/60">
            {annotationStateLabels.invalid}
          </span>
        ) : state === "empty" ? (
          <span className="rounded-md bg-background/85 px-1.5 py-0.5 text-[11px] font-medium shadow-xs backdrop-blur">
            {annotationStateLabels.empty}
          </span>
        ) : (
          <span className="rounded-md bg-background/75 px-1.5 py-0.5 text-[11px] text-muted-foreground shadow-xs backdrop-blur">
            {annotationStateLabels.none}
          </span>
        )}
      </span>
      <span className="absolute inset-x-0 bottom-0 flex items-end gap-2 bg-gradient-to-t from-black/70 via-black/35 to-transparent px-2 pt-8 pb-1.5">
        <span className="min-w-0 flex-1 truncate text-[11px] text-white">
          {image.file_name}
        </span>
        {labels.length > 0 && (
          <span className="flex shrink-0 -space-x-0.5 pb-0.5" aria-hidden>
            {labels.slice(0, 4).map((label) => (
              <span
                key={label.name}
                className="size-2 rounded-full ring-1 ring-black/40"
                style={{ backgroundColor: labelColor(label.name) }}
              />
            ))}
          </span>
        )}
      </span>
    </button>
  )
}

/** 滚动到底部自动加载下一页；同时保留按钮作为后备（以及键盘操作）。 */
function LoadMore({
  hasMore,
  loading,
  error,
  onLoadMore,
  loaded,
  total,
}: {
  hasMore: boolean
  loading: boolean
  error: Error | null
  onLoadMore: () => void
  loaded: number
  total: number
}) {
  const sentinel = useRef<HTMLDivElement>(null)
  const latest = useRef(onLoadMore)
  useEffect(() => {
    latest.current = onLoadMore
  })

  useEffect(() => {
    const element = sentinel.current
    if (!element || !hasMore || loading || error) {
      return
    }
    const observer = new IntersectionObserver(
      ([entry]) => {
        if (entry.isIntersecting) {
          latest.current()
        }
      },
      { rootMargin: "600px 0px" }
    )
    observer.observe(element)
    return () => observer.disconnect()
  }, [hasMore, loading, error])

  return (
    <div ref={sentinel} className="flex flex-col items-center gap-2 py-2">
      {hasMore ? (
        <>
          {error && (
            <p className="text-sm text-destructive">
              加载失败：{error.message}
            </p>
          )}
          <Button
            variant="outline"
            size="sm"
            onClick={onLoadMore}
            disabled={loading}
          >
            {loading && <Spinner aria-hidden />}
            {loading ? "正在加载…" : error ? "重试" : "加载更多"}
          </Button>
        </>
      ) : (
        loaded > 0 && (
          <p className="text-xs text-muted-foreground tabular-nums">
            已显示全部 {formatNumber(total)} 张
          </p>
        )
      )}
    </div>
  )
}

function ImageGridSkeleton() {
  return (
    <div
      className="grid grid-cols-2 gap-3 sm:grid-cols-3 md:grid-cols-4 lg:grid-cols-5 xl:grid-cols-6"
      aria-busy="true"
      aria-label="正在加载"
    >
      {Array.from({ length: 12 }, (_, index) => (
        <Skeleton key={index} className="aspect-square rounded-lg" />
      ))}
    </div>
  )
}
