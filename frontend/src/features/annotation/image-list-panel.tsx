import { useCallback, useRef, useState } from "react"
import { useQuery } from "@tanstack/react-query"
import { ChevronLeft, ChevronRight } from "lucide-react"

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
import {
  IMAGE_LIST_PAGE_SIZE,
  imagesPageQuery,
} from "@/features/annotation/api"
import { focusCanvas, preventTypeahead } from "@/features/annotation/shortcuts"
import { hasFilters } from "@/features/annotation/workspace-params"
import {
  labelsQuery,
  type AnnotationState,
  type Dataset,
  type DatasetImage,
  type ImageFilters,
  type ImageStatusFilter,
} from "@/features/datasets/api"
import { ImageSearch } from "@/features/datasets/dataset-images"
import {
  annotationStateLabels,
  imageStatusOptions,
} from "@/features/datasets/images"
import { formatNumber } from "@/lib/format"
import { cn } from "@/lib/utils"

const ALL_LABELS = "all"
const labelValue = (name: string) => `label:${name}`

const STATE_DOT: Record<AnnotationState, string> = {
  none: "border border-muted-foreground/50 bg-transparent",
  empty: "bg-sky-500",
  annotated: "bg-emerald-500",
  invalid: "bg-destructive",
}

type ImageListPanelProps = {
  dataset: Dataset
  filters: ImageFilters
  onFiltersChange: (changes: Partial<ImageFilters>) => void
  currentId: number
  /** 当前图片在筛选结果中的位置；不满足筛选条件时为 null。 */
  position: number | null
  onOpen: (image: DatasetImage) => void
}

/**
 * 图片列表（按固定顺序分页）：标注状态、文件名，单击打开。默认显示当前图片
 * 所在的一页，切换图片时跟着翻页。
 */
export function ImageListPanel({
  dataset,
  filters,
  onFiltersChange,
  currentId,
  position,
  onOpen,
}: ImageListPanelProps) {
  const followPage =
    position === null ? 0 : Math.floor(position / IMAGE_LIST_PAGE_SIZE)
  const filtersKey = JSON.stringify([filters.status, filters.label, filters.q])
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
    imagesPageQuery(dataset.id, filters, page * IMAGE_LIST_PAGE_SIZE)
  )
  const labels = useQuery(labelsQuery(dataset.id))
  const total = query.data?.total ?? 0
  const pages = Math.max(1, Math.ceil(total / IMAGE_LIST_PAGE_SIZE))
  // 这一页超出了结果的范围（如图片被删除后）：退到最后一页。占位数据是上一个
  // 查询（可能是别的筛选条件）的结果，不据此判断。
  const lastPage = query.data && !query.isPlaceholderData ? pages - 1 : Infinity
  if (page > lastPage) {
    setManual({ follow: followPage, filters: filtersKey, page: lastPage })
  }
  const statusOptions = imageStatusOptions(dataset, filters.status)
  const labelItems = labels.data?.items ?? []
  const labelMissing =
    filters.label !== "" && !labelItems.some((l) => l.name === filters.label)

  // 筛选下拉框关闭后把焦点还给画布（在弹出的面板中时除外），快捷键随即可用；
  // 焦点在下拉框按钮上时，字母键也不会按首字母改掉筛选条件。
  const statusTrigger = useRef<HTMLButtonElement>(null)
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
        <ImageSearch
          value={filters.q}
          onChange={(q) => onFiltersChange({ q })}
          className="md:w-full"
        />
        <div className="grid grid-cols-2 gap-2">
          <Select
            value={filters.status}
            onValueChange={(value) =>
              onFiltersChange({ status: value as ImageStatusFilter })
            }
          >
            <SelectTrigger
              ref={statusTrigger}
              size="sm"
              className="w-full"
              aria-label="标注状态"
              onKeyDown={preventTypeahead}
            >
              <SelectValue />
            </SelectTrigger>
            <SelectContent
              onCloseAutoFocus={(event) =>
                returnFocus(event, statusTrigger.current)
              }
            >
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
              ref={labelTrigger}
              size="sm"
              className="w-full"
              aria-label="按标签筛选"
              onKeyDown={preventTypeahead}
            >
              <SelectValue placeholder="全部标签" />
            </SelectTrigger>
            <SelectContent
              onCloseAutoFocus={(event) =>
                returnFocus(event, labelTrigger.current)
              }
            >
              <SelectItem value={ALL_LABELS}>全部标签</SelectItem>
              {labelMissing && (
                <SelectItem value={labelValue(filters.label)}>
                  {filters.label}
                </SelectItem>
              )}
              {labelItems.map((label) => (
                <SelectItem key={label.name} value={labelValue(label.name)}>
                  {label.name}
                  <span className="text-muted-foreground tabular-nums">
                    {formatNumber(label.image_count)}
                  </span>
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
        <div className="flex items-center justify-between text-xs text-muted-foreground tabular-nums">
          <span className="flex items-center gap-1.5">
            {query.isFetching && <Spinner className="size-3" />}
            {hasFilters(filters)
              ? `筛选出 ${formatNumber(total)} 张`
              : `共 ${formatNumber(total)} 张`}
          </span>
          {hasFilters(filters) && (
            <button
              type="button"
              className="hover:text-foreground hover:underline"
              onClick={() =>
                onFiltersChange({ status: "all", label: "", q: "" })
              }
            >
              清除筛选
            </button>
          )}
        </div>
      </div>

      <ol className="min-h-0 flex-1 overflow-y-auto px-1.5" aria-label="图片">
        {query.isPending &&
          Array.from({ length: 8 }, (_, index) => (
            <li key={index} className="px-1.5 py-1">
              <Skeleton className="h-6 w-full" />
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
        {query.data?.items.map((image, index) => {
          const current = image.id === currentId
          const count = image.annotation?.object_count ?? 0
          return (
            <li key={image.id}>
              <button
                type="button"
                onClick={() => onOpen(image)}
                aria-current={current ? "true" : undefined}
                title={image.path}
                ref={current ? scrollToCurrent : undefined}
                className={cn(
                  "flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-left text-sm outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50",
                  current
                    ? "bg-accent font-medium text-accent-foreground"
                    : "hover:bg-muted"
                )}
              >
                <span className="w-9 shrink-0 text-right text-[11px] text-muted-foreground tabular-nums">
                  {formatNumber(page * IMAGE_LIST_PAGE_SIZE + index + 1)}
                </span>
                <span
                  className={cn(
                    "size-2 shrink-0 rounded-full",
                    STATE_DOT[image.annotation_state]
                  )}
                  title={annotationStateLabels[image.annotation_state]}
                  aria-label={annotationStateLabels[image.annotation_state]}
                />
                <span className="min-w-0 flex-1 truncate">
                  {image.file_name}
                </span>
                {count > 0 && (
                  <span className="text-[11px] text-muted-foreground tabular-nums">
                    {formatNumber(count)}
                  </span>
                )}
              </button>
            </li>
          )
        })}
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

function Legend() {
  const items: AnnotationState[] = ["annotated", "empty", "none", "invalid"]
  return (
    <div className="flex flex-wrap gap-x-3 gap-y-1 border-t px-3 py-2 text-[11px] text-muted-foreground">
      {items.map((state) => (
        <span key={state} className="flex items-center gap-1">
          <span className={cn("size-2 rounded-full", STATE_DOT[state])} />
          {annotationStateLabels[state]}
        </span>
      ))}
    </div>
  )
}
