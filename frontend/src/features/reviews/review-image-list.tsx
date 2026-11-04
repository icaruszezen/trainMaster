import { useCallback, useRef, useState } from "react"
import { useQuery } from "@tanstack/react-query"
import { ChevronLeft, ChevronRight, ImageOff } from "lucide-react"

import { Button } from "@/components/ui/button"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { Skeleton } from "@/components/ui/skeleton"
import { Spinner } from "@/components/ui/spinner"
import { Toggle } from "@/components/ui/toggle"
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group"
import { focusCanvas, preventTypeahead } from "@/features/annotation/shortcuts"
import { ImageSearch } from "@/features/datasets/dataset-images"
import { isBrowserNativeImage } from "@/features/datasets/files"
import {
  ALL_REVIEW_IMAGES,
  REVIEW_IMAGE_PAGE_SIZE,
  reviewImageFileUrl,
  reviewImagesPageQuery,
  reviewLabelsQuery,
  reviewThumbnailUrl,
  type ReviewImage,
  type ReviewImageFilters,
  type ReviewImageStatus,
  type ReviewSource,
  type ReviewStatusFilter,
  type ReviewSummary,
} from "@/features/reviews/api"
import { hasReviewFilters } from "@/features/reviews/params"
import { ReviewStatusDot } from "@/features/reviews/review-status-badge"
import {
  REVIEW_IMAGE_STATUSES,
  reviewImageStatusLabels,
  reviewImageStatusStyles,
  scopedCounts,
  statusFilterOptions,
} from "@/features/reviews/review-status"
import { formatNumber } from "@/lib/format"
import { cn } from "@/lib/utils"

const ALL = "all"
const datasetValue = (id: number) => `dataset:${id}`
const labelValue = (name: string) => `label:${name}`

type ReviewImageListProps = {
  source: ReviewSource
  review: ReviewSummary
  filters: ReviewImageFilters
  onFiltersChange: (changes: Partial<ReviewImageFilters>) => void
  currentId: number
  /** 当前图片在筛选结果中的位置；不满足筛选条件时为 null。 */
  position: number | null
  onOpen: (image: ReviewImage) => void
}

/**
 * 审图的图片列表（按审图任务中的顺序分页）：筛选（审核状态，各状态的图片数来自
 * 任务的统计；数据集、类别、已修改、无框、识别失败，文件名），每张图片的缩略图、
 * 审核状态的色条、框数与“已修改”。单击打开；默认显示当前图片所在的一页，切换
 * 图片时跟着翻页。
 */
export function ReviewImageList({
  source,
  review,
  filters,
  onFiltersChange,
  currentId,
  position,
  onOpen,
}: ReviewImageListProps) {
  const followPage =
    position === null ? 0 : Math.floor(position / REVIEW_IMAGE_PAGE_SIZE)
  const filtersKey = JSON.stringify(filters)
  // 手动翻页后停在那一页，直到当前图片换到了别的一页或筛选条件变了。
  const [manual, setManual] = useState<{
    follow: number
    filters: string
    page: number
  } | null>(null)
  const page =
    manual && manual.follow === followPage && manual.filters === filtersKey
      ? manual.page
      : followPage
  const query = useQuery(
    reviewImagesPageQuery(source, filters, page * REVIEW_IMAGE_PAGE_SIZE)
  )
  const labels = useQuery(reviewLabelsQuery(source))
  const total = query.data?.total ?? 0
  const pages = Math.max(1, Math.ceil(total / REVIEW_IMAGE_PAGE_SIZE))
  // 这一页超出了结果的范围（如标记后图片不再符合筛选条件）：退到最后一页。占位
  // 数据是上一个查询（可能是别的筛选条件）的结果，不据此判断。
  const lastPage = query.data && !query.isPlaceholderData ? pages - 1 : Infinity
  if (page > lastPage) {
    setManual({ follow: followPage, filters: filtersKey, page: lastPage })
  }
  const labelItems = labels.data?.items ?? []
  const labelMissing =
    filters.label !== "" && !labelItems.some((l) => l.name === filters.label)
  const datasets = review.datasets
  const datasetMissing =
    filters.datasetId !== null &&
    !datasets.some((dataset) => dataset.id === filters.datasetId)
  const statusOptions = statusFilterOptions(
    scopedCounts(review, filters.datasetId)
  )
  const filtered = hasReviewFilters(filters)

  // 下拉框关闭后把焦点还给画布（在弹出的面板中时除外），快捷键随即可用；焦点在
  // 下拉框按钮上时，字母键也不会按首字母改掉筛选条件。
  const datasetTrigger = useRef<HTMLButtonElement>(null)
  const labelTrigger = useRef<HTMLButtonElement>(null)
  const returnFocus = (event: Event, trigger: HTMLButtonElement | null) => {
    if (focusCanvas(trigger)) {
      event.preventDefault()
    }
  }

  const goPage = (next: number) =>
    setManual({ follow: followPage, filters: filtersKey, page: next })
  // 当前图片换了（或翻到了它所在的一页）时滚动到它。
  const scrollToCurrent = useCallback(
    (element: HTMLButtonElement | null) =>
      element?.scrollIntoView({ block: "nearest" }),
    // eslint-disable-next-line react-hooks/exhaustive-deps -- 依赖用于触发滚动
    [currentId, page]
  )

  return (
    <div className="flex min-h-0 flex-1 flex-col gap-2">
      <div className="grid gap-2 px-3 pt-3">
        <ToggleGroup
          type="single"
          variant="outline"
          size="sm"
          value={filters.status}
          onValueChange={(value) =>
            value && onFiltersChange({ status: value as ReviewStatusFilter })
          }
          aria-label="审核状态"
          className="grid w-full grid-cols-4"
        >
          {statusOptions.map((option) => (
            <ToggleGroupItem
              key={option.value}
              value={option.value}
              aria-label={`${option.label}（${formatNumber(option.count)} 张）`}
              className="h-auto flex-col gap-0 px-1 py-1 leading-tight"
            >
              <span className="flex items-center gap-1 text-xs">
                {option.value !== "all" && (
                  <ReviewStatusDot status={option.value as ReviewImageStatus} />
                )}
                {option.label}
              </span>
              <span className="text-[11px] font-normal text-muted-foreground tabular-nums">
                {formatNumber(option.count)}
              </span>
            </ToggleGroupItem>
          ))}
        </ToggleGroup>
        <ImageSearch
          value={filters.q}
          onChange={(q) => onFiltersChange({ q })}
          className="md:w-full"
        />
        {/* 数据集名称通常较长：两个下拉框上下排列。 */}
        <div className="grid gap-2">
          {(datasets.length > 1 || datasetMissing) && (
            <Select
              value={
                filters.datasetId === null
                  ? ALL
                  : datasetValue(filters.datasetId)
              }
              onValueChange={(value) =>
                onFiltersChange({
                  datasetId:
                    value === ALL
                      ? null
                      : Number(value.slice("dataset:".length)),
                })
              }
            >
              <SelectTrigger
                ref={datasetTrigger}
                size="sm"
                className="w-full min-w-0"
                aria-label="按数据集筛选"
                onKeyDown={preventTypeahead}
              >
                <SelectValue placeholder="全部数据集" />
              </SelectTrigger>
              <SelectContent
                onCloseAutoFocus={(event) =>
                  returnFocus(event, datasetTrigger.current)
                }
              >
                <SelectItem value={ALL}>全部数据集</SelectItem>
                {datasetMissing && filters.datasetId !== null && (
                  <SelectItem value={datasetValue(filters.datasetId)}>
                    数据集 #{filters.datasetId}
                  </SelectItem>
                )}
                {datasets.map((dataset) => (
                  <SelectItem key={dataset.id} value={datasetValue(dataset.id)}>
                    <span className="truncate">{dataset.name}</span>
                    <span className="text-muted-foreground tabular-nums">
                      {formatNumber(dataset.counts.total)}
                    </span>
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          )}
          <Select
            value={filters.label ? labelValue(filters.label) : ALL}
            onValueChange={(value) =>
              onFiltersChange({
                label: value === ALL ? "" : value.slice("label:".length),
              })
            }
            disabled={labelItems.length === 0 && !filters.label}
          >
            <SelectTrigger
              ref={labelTrigger}
              size="sm"
              className="w-full min-w-0"
              aria-label="按类别筛选"
              onKeyDown={preventTypeahead}
            >
              <SelectValue placeholder="全部类别" />
            </SelectTrigger>
            <SelectContent
              onCloseAutoFocus={(event) =>
                returnFocus(event, labelTrigger.current)
              }
            >
              <SelectItem value={ALL}>全部类别</SelectItem>
              {labelMissing && (
                <SelectItem value={labelValue(filters.label)}>
                  {filters.label}
                </SelectItem>
              )}
              {labelItems.map((label) => (
                <SelectItem key={label.name} value={labelValue(label.name)}>
                  <span className="truncate">{label.name}</span>
                  <span className="text-muted-foreground tabular-nums">
                    {formatNumber(label.image_count)}
                  </span>
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
        <div className="flex flex-wrap gap-1.5" aria-label="其它筛选条件">
          <FilterToggle
            pressed={filters.edited === "edited"}
            onPressedChange={(on) =>
              onFiltersChange({ edited: on ? "edited" : "all" })
            }
            title="审图标注与模型的识别结果不同的图片"
          >
            已修改
          </FilterToggle>
          <FilterToggle
            pressed={filters.boxes === "without"}
            onPressedChange={(on) =>
              onFiltersChange({ boxes: on ? "without" : "all" })
            }
            title="审图标注中没有框的图片"
          >
            无框
          </FilterToggle>
          <FilterToggle
            pressed={filters.inference === "failed"}
            onPressedChange={(on) =>
              onFiltersChange({ inference: on ? "failed" : "all" })
            }
            title="模型没能识别的图片（如图片无法读取）"
          >
            识别失败
          </FilterToggle>
        </div>
        <div className="flex items-center justify-between text-xs text-muted-foreground tabular-nums">
          <span className="flex items-center gap-1.5">
            {query.isFetching && <Spinner className="size-3" />}
            {filtered
              ? `筛选出 ${formatNumber(total)} 张`
              : `共 ${formatNumber(total)} 张`}
          </span>
          {filtered && (
            <button
              type="button"
              className="hover:text-foreground hover:underline"
              onClick={() => onFiltersChange(ALL_REVIEW_IMAGES)}
            >
              清除筛选
            </button>
          )}
        </div>
      </div>

      <ol className="min-h-0 flex-1 overflow-y-auto px-1.5" aria-label="图片">
        {query.isPending &&
          Array.from({ length: 8 }, (_, index) => (
            <li key={index} className="flex items-center gap-2 px-1.5 py-1">
              <Skeleton className="size-10 rounded-md" />
              <Skeleton className="h-4 flex-1" />
            </li>
          ))}
        {query.error && !query.data && (
          <li className="px-1.5 py-2 text-xs text-destructive">
            无法获取图片列表：{query.error.message}
          </li>
        )}
        {query.data?.items.length === 0 && (
          <li className="px-1.5 py-6 text-center text-sm text-muted-foreground">
            没有符合条件的图片
          </li>
        )}
        {query.data?.items.map((image) => (
          <li key={image.id}>
            <ImageRow
              source={source}
              image={image}
              current={image.id === currentId}
              rowRef={image.id === currentId ? scrollToCurrent : undefined}
              onOpen={() => onOpen(image)}
            />
          </li>
        ))}
      </ol>

      {pages > 1 && (
        <div className="flex items-center justify-between border-t px-2 py-1.5 text-xs text-muted-foreground tabular-nums">
          <Button
            variant="ghost"
            size="icon-sm"
            onClick={() => goPage(page - 1)}
            disabled={page === 0}
            aria-label="上一页"
          >
            <ChevronLeft />
          </Button>
          第 {formatNumber(page + 1)} / {formatNumber(pages)} 页
          <Button
            variant="ghost"
            size="icon-sm"
            onClick={() => goPage(page + 1)}
            disabled={page >= pages - 1}
            aria-label="下一页"
          >
            <ChevronRight />
          </Button>
        </div>
      )}
      <Legend />
    </div>
  )
}

function FilterToggle({
  pressed,
  onPressedChange,
  title,
  children,
}: {
  pressed: boolean
  onPressedChange: (pressed: boolean) => void
  title: string
  children: string
}) {
  return (
    <Toggle
      variant="outline"
      size="sm"
      pressed={pressed}
      onPressedChange={onPressedChange}
      title={title}
      className="h-7 rounded-full px-2.5 text-xs font-normal data-[state=on]:border-primary/50 data-[state=on]:bg-primary/10 data-[state=on]:text-foreground"
    >
      {children}
    </Toggle>
  )
}

function ImageRow({
  source,
  image,
  current,
  rowRef,
  onOpen,
}: {
  source: ReviewSource
  image: ReviewImage
  current: boolean
  rowRef?: (element: HTMLButtonElement | null) => void
  onOpen: () => void
}) {
  const status = reviewImageStatusLabels[image.status]
  const meta = !image.inferred
    ? "还没有识别"
    : image.inference_error
      ? "识别失败"
      : image.object_count > 0
        ? `${formatNumber(image.object_count)} 个框`
        : "无框"
  return (
    <button
      type="button"
      onClick={onOpen}
      aria-current={current ? "true" : undefined}
      aria-label={`${image.file_name}，${status}，${meta}${image.edited ? "，已修改" : ""}`}
      title={image.path}
      ref={rowRef}
      className={cn(
        "relative flex w-full items-center gap-2.5 rounded-md py-1.5 pr-2 pl-3 text-left text-sm outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50",
        current ? "bg-accent text-accent-foreground" : "hover:bg-muted"
      )}
    >
      <span
        className={cn(
          "absolute inset-y-1.5 left-1 w-1 rounded-full",
          reviewImageStatusStyles[image.status].dot
        )}
        aria-hidden
      />
      <ReviewThumbnail
        source={source}
        image={image}
        className="size-10 shrink-0 rounded-md"
      />
      <span className="grid min-w-0 flex-1 gap-0.5">
        <span className={cn("truncate leading-5", current && "font-medium")}>
          {image.file_name}
        </span>
        <span className="flex items-center gap-1.5 text-[11px] leading-4 text-muted-foreground tabular-nums">
          <span
            className={cn(
              image.inference_error && "text-destructive",
              !image.inferred && "italic"
            )}
          >
            {meta}
          </span>
          {image.edited && (
            <span className="rounded-sm bg-primary/10 px-1 text-primary">
              已修改
            </span>
          )}
        </span>
      </span>
      {image.status !== "pending" && (
        <span
          className={cn(
            "shrink-0 text-[11px] font-medium",
            reviewImageStatusStyles[image.status].text
          )}
          aria-hidden
        >
          {status}
        </span>
      )}
    </button>
  )
}

/**
 * 审图图片的缩略图。服务端无法生成缩略图时，浏览器能直接显示的格式回退到原图，
 * 仍然失败则显示占位图标。
 */
export function ReviewThumbnail(props: {
  source: ReviewSource
  image: ReviewImage
  className?: string
}) {
  // 换图（或图片版本变化）时重置回退状态。
  return (
    <Thumbnail
      key={`${props.image.id}:${props.image.image_version}`}
      {...props}
    />
  )
}

function Thumbnail({
  source,
  image,
  className,
}: {
  source: ReviewSource
  image: ReviewImage
  className?: string
}) {
  const [stage, setStage] = useState<"thumbnail" | "original" | "failed">(
    "thumbnail"
  )
  if (stage === "failed") {
    return (
      <span
        className={cn(
          "flex items-center justify-center bg-muted text-muted-foreground/60",
          className
        )}
      >
        <ImageOff className="size-4" aria-hidden />
      </span>
    )
  }
  return (
    <img
      src={
        stage === "thumbnail"
          ? reviewThumbnailUrl(source, image, "sm")
          : reviewImageFileUrl(source, image)
      }
      alt=""
      loading="lazy"
      decoding="async"
      draggable={false}
      onError={() =>
        setStage(
          stage === "thumbnail" && isBrowserNativeImage(image.path)
            ? "original"
            : "failed"
        )
      }
      className={cn("bg-muted object-cover", className)}
    />
  )
}

function Legend() {
  return (
    <div className="flex flex-wrap gap-x-3 gap-y-1 border-t px-3 py-2 text-[11px] text-muted-foreground">
      {REVIEW_IMAGE_STATUSES.map((status) => (
        <span key={status} className="flex items-center gap-1">
          <ReviewStatusDot status={status} />
          {reviewImageStatusLabels[status]}
        </span>
      ))}
      <span className="flex items-center gap-1">
        <span className="rounded-sm bg-primary/10 px-1 text-primary">
          已修改
        </span>
        与模型结果不同
      </span>
    </div>
  )
}
