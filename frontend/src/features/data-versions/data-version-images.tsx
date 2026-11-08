/**
 * 数据版本详情的“图片”标签页：按划分 / 类别 / 数据集 / 原图与增强图片筛选、
 * 搜索，缩略图网格（滚动到底部加载下一页），点开查看大图与标注框。筛选条件
 * 保存在地址栏中（见 images.ts）。
 */
import { useEffect, useMemo, useRef, useState } from "react"
import { useInfiniteQuery } from "@tanstack/react-query"
import { ImageOff, Images, SearchX, Sparkles, SquareDashed } from "lucide-react"

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
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { Skeleton } from "@/components/ui/skeleton"
import { Spinner } from "@/components/ui/spinner"
import { ImageSearch } from "@/features/datasets/dataset-images"
import { isBrowserNativeImage } from "@/features/datasets/files"
import { LoadError } from "@/features/datasets/load-error"
import {
  dataVersionImagesQuery,
  EMPTY_IMAGE_FILTERS,
  SPLITS,
  type DataVersion,
  type DataVersionImage,
  type DataVersionImageFilters,
  type Split,
} from "@/features/data-versions/api"
import { DataVersionImageViewer } from "@/features/data-versions/data-version-image-viewer"
import { classColor, SPLIT_LABELS } from "@/features/data-versions/format"
import {
  flattenImagePages,
  isFiltered,
  loadMoreImages,
} from "@/features/data-versions/images"
import { formatNumber } from "@/lib/format"
import { cn } from "@/lib/utils"

// Radix Select 的值不能为空字符串。
const ALL = "all"
const prefixed = (prefix: string, value: string | number) =>
  `${prefix}:${value}`
const unprefixed = (value: string) => value.slice(value.indexOf(":") + 1)

type DataVersionImagesProps = {
  version: DataVersion
  filters: DataVersionImageFilters
  onFiltersChange: (changes: Partial<DataVersionImageFilters>) => void
}

export function DataVersionImages({
  version,
  filters,
  onFiltersChange,
}: DataVersionImagesProps) {
  const [viewerIndex, setViewerIndex] = useState<number | null>(null)
  const query = useInfiniteQuery(dataVersionImagesQuery(version.id, filters))
  const images = useMemo(
    () => flattenImagePages(query.data?.pages),
    [query.data]
  )
  const total = query.data?.pages[0]?.total ?? 0
  if (viewerIndex !== null && viewerIndex >= images.length) {
    setViewerIndex(null)
  }
  const filtered = isFiltered(filters)
  const colors = useMemo(
    () => new Map(version.classes.map((cls) => [cls.name, classColor(cls)])),
    [version.classes]
  )
  const splitCounts: Record<Split, number> = {
    train: version.train_count,
    val: version.val_count,
    test: version.test_count,
  }
  const splits = SPLITS.filter(
    (split) => splitCounts[split] > 0 || filters.split === split
  )
  const labelMissing =
    filters.label !== "" &&
    !version.classes.some((cls) => cls.name === filters.label)

  return (
    <div className="grid gap-4">
      <div className="flex flex-wrap items-center gap-2">
        <ImageSearch
          value={filters.q}
          onChange={(q) => onFiltersChange({ q })}
        />
        <Select
          value={filters.split ? prefixed("split", filters.split) : ALL}
          onValueChange={(value) =>
            onFiltersChange({
              split: value === ALL ? "" : (unprefixed(value) as Split),
            })
          }
        >
          <SelectTrigger
            className="w-[calc(50%-0.25rem)] sm:w-36"
            aria-label="按划分筛选"
          >
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value={ALL}>全部划分</SelectItem>
            {splits.map((split) => (
              <SelectItem key={split} value={prefixed("split", split)}>
                {SPLIT_LABELS[split]}
                <span className="text-muted-foreground tabular-nums">
                  {formatNumber(splitCounts[split])}
                </span>
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <Select
          value={filters.label ? prefixed("label", filters.label) : ALL}
          onValueChange={(value) =>
            onFiltersChange({ label: value === ALL ? "" : unprefixed(value) })
          }
          disabled={version.classes.length === 0}
        >
          <SelectTrigger
            className="w-[calc(50%-0.25rem)] sm:w-40"
            aria-label="按类别筛选"
          >
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value={ALL}>全部类别</SelectItem>
            {labelMissing && (
              <SelectItem value={prefixed("label", filters.label)}>
                {filters.label}
              </SelectItem>
            )}
            {version.classes.map((cls, index) => (
              <SelectItem key={cls.name} value={prefixed("label", cls.name)}>
                <span
                  className="size-2 rounded-full"
                  style={{ backgroundColor: classColor(cls) }}
                  aria-hidden
                />
                <span className="truncate">{cls.name}</span>
                <span className="font-mono text-muted-foreground tabular-nums">
                  {index}
                </span>
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        {(version.sources.length > 1 || filters.dataset_id !== null) && (
          <Select
            value={
              filters.dataset_id === null
                ? ALL
                : prefixed("dataset", filters.dataset_id)
            }
            onValueChange={(value) =>
              onFiltersChange({
                dataset_id: value === ALL ? null : Number(unprefixed(value)),
              })
            }
          >
            <SelectTrigger
              className="w-[calc(50%-0.25rem)] sm:w-44"
              aria-label="按数据集筛选"
            >
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value={ALL}>全部数据集</SelectItem>
              {filters.dataset_id !== null &&
                !version.sources.some(
                  (s) => s.dataset_id === filters.dataset_id
                ) && (
                  <SelectItem value={prefixed("dataset", filters.dataset_id)}>
                    数据集 #{filters.dataset_id}
                  </SelectItem>
                )}
              {version.sources.map((source) => (
                <SelectItem
                  key={source.dataset_id}
                  value={prefixed("dataset", source.dataset_id)}
                >
                  <span className="truncate">{source.dataset_name}</span>
                  <span className="text-muted-foreground tabular-nums">
                    {formatNumber(source.image_count)}
                  </span>
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        )}
        {(version.augmented_count > 0 || filters.augmented !== null) && (
          <Select
            value={
              filters.augmented === null
                ? ALL
                : filters.augmented
                  ? "augmented"
                  : "original"
            }
            onValueChange={(value) =>
              onFiltersChange({
                augmented: value === ALL ? null : value === "augmented",
              })
            }
          >
            <SelectTrigger
              className="w-[calc(50%-0.25rem)] sm:w-36"
              aria-label="原图或增强图片"
            >
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value={ALL}>原图与增强</SelectItem>
              <SelectItem value="original">只看原图</SelectItem>
              <SelectItem value="augmented">
                只看增强图片
                <span className="text-muted-foreground tabular-nums">
                  {formatNumber(version.augmented_count)}
                </span>
              </SelectItem>
            </SelectContent>
          </Select>
        )}
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
        <Empty className="border">
          <EmptyHeader>
            <EmptyMedia variant="icon">
              {filtered ? <SearchX /> : <Images />}
            </EmptyMedia>
            <EmptyTitle className="text-base">
              {filtered ? "没有符合条件的图片" : "这个数据版本中没有图片"}
            </EmptyTitle>
            {filtered && (
              <EmptyDescription>换个筛选条件试试。</EmptyDescription>
            )}
          </EmptyHeader>
          {filtered && (
            <EmptyContent>
              <Button
                variant="outline"
                size="sm"
                onClick={() => onFiltersChange(EMPTY_IMAGE_FILTERS)}
              >
                清除筛选
              </Button>
            </EmptyContent>
          )}
        </Empty>
      ) : (
        <>
          <ul
            className={cn(
              "grid grid-cols-2 gap-3 sm:grid-cols-3 md:grid-cols-4 lg:grid-cols-5 xl:grid-cols-6",
              query.isPlaceholderData && "opacity-60 transition-opacity"
            )}
          >
            {images.map((image, index) => (
              <li key={image.id} className="min-w-0">
                <ImageTile
                  image={image}
                  colors={colors}
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

      <DataVersionImageViewer
        version={version}
        colors={colors}
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

/** 缩略图；服务端无法生成缩略图时回退到原图（浏览器能显示的格式），仍然失败显示占位图标。 */
function DataVersionThumbnail({
  image,
  className,
}: {
  image: DataVersionImage
  className?: string
}) {
  const [stage, setStage] = useState<"thumbnail" | "original" | "failed">(
    "thumbnail"
  )
  if (stage === "failed") {
    return (
      <div
        className={cn(
          "flex items-center justify-center bg-muted text-muted-foreground/60",
          className
        )}
      >
        <ImageOff className="size-6" aria-hidden />
      </div>
    )
  }
  return (
    <img
      src={stage === "original" ? image.file_url : image.thumbnail_url}
      alt=""
      loading="lazy"
      decoding="async"
      draggable={false}
      style={{ imageOrientation: "none" }}
      onError={() =>
        setStage(
          stage === "thumbnail" && isBrowserNativeImage(image.file_name)
            ? "original"
            : "failed"
        )
      }
      className={cn("object-cover", className)}
    />
  )
}

function ImageTile({
  image,
  colors,
  onOpen,
}: {
  image: DataVersionImage
  colors: ReadonlyMap<string, string>
  onOpen: () => void
}) {
  const augmented = image.augmented_from !== null
  return (
    <button
      type="button"
      onClick={onOpen}
      className="group relative block aspect-square w-full overflow-hidden rounded-lg border bg-muted text-left transition-shadow outline-none hover:shadow-md focus-visible:ring-[3px] focus-visible:ring-ring/50"
      aria-label={`查看 ${image.path}`}
      title={image.path}
    >
      <DataVersionThumbnail
        key={image.id}
        image={image}
        className="size-full transition-transform duration-300 ease-out group-hover:scale-[1.04] motion-reduce:transition-none"
      />
      <span className="absolute inset-x-1.5 top-1.5 flex items-start justify-between gap-1">
        <span className="flex min-w-0 flex-wrap gap-1">
          <span className="rounded-md bg-background/90 px-1.5 py-0.5 text-[11px] font-medium shadow-xs backdrop-blur">
            {SPLIT_LABELS[image.split]}
          </span>
          {augmented && (
            <span className="inline-flex items-center gap-0.5 rounded-md bg-primary px-1.5 py-0.5 text-[11px] font-medium text-primary-foreground shadow-xs">
              <Sparkles className="size-3" aria-hidden />
              增强
            </span>
          )}
        </span>
        {image.object_count > 0 ? (
          <span className="inline-flex shrink-0 items-center gap-1 rounded-md bg-background/90 px-1.5 py-0.5 text-[11px] font-medium tabular-nums shadow-xs backdrop-blur">
            <SquareDashed className="size-3" aria-hidden />
            {formatNumber(image.object_count)}
          </span>
        ) : (
          <span className="shrink-0 rounded-md bg-background/85 px-1.5 py-0.5 text-[11px] font-medium shadow-xs backdrop-blur">
            负样本
          </span>
        )}
      </span>
      <span className="absolute inset-x-0 bottom-0 flex items-end gap-2 bg-gradient-to-t from-black/70 via-black/35 to-transparent px-2 pt-8 pb-1.5">
        <span className="min-w-0 flex-1 truncate text-[11px] text-white">
          {image.file_name}
        </span>
        {image.labels.length > 0 && (
          <span className="flex shrink-0 -space-x-0.5 pb-0.5" aria-hidden>
            {image.labels.slice(0, 4).map((label) => (
              <span
                key={label.name}
                className="size-2 rounded-full ring-1 ring-black/40"
                style={{
                  backgroundColor:
                    colors.get(label.name) ??
                    classColor({ name: label.name, color: "" }),
                }}
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
            <p className="text-sm break-words text-destructive">
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
