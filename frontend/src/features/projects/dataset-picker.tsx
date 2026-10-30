import { useId, useMemo, useState } from "react"
import { Images, Search, X } from "lucide-react"

import { Checkbox } from "@/components/ui/checkbox"
import {
  InputGroup,
  InputGroupAddon,
  InputGroupButton,
  InputGroupInput,
} from "@/components/ui/input-group"
import type { Dataset } from "@/features/datasets/api"
import { DatasetThumbnail } from "@/features/datasets/dataset-thumbnail"
import { formatNumber } from "@/lib/format"
import { cn } from "@/lib/utils"

/** 超过这个数量时显示搜索框。 */
const SEARCH_THRESHOLD = 6

const nameCollator = new Intl.Collator("zh-CN", { numeric: true })

type DatasetPickerProps = {
  datasets: Dataset[]
  selected: ReadonlySet<number>
  onSelectedChange: (selected: Set<number>) => void
  disabled?: boolean
  /** 列表的可访问名称，如“选择要添加的数据集”。 */
  label: string
  className?: string
}

/** 带搜索的数据集多选列表（按名称排序），显示缩略图、图片数与已属于的项目。 */
export function DatasetPicker({
  datasets,
  selected,
  onSelectedChange,
  disabled = false,
  label,
  className,
}: DatasetPickerProps) {
  const listId = useId()
  const [search, setSearch] = useState("")
  const q = search.trim().toLowerCase()

  const sorted = useMemo(
    () => [...datasets].sort((a, b) => nameCollator.compare(a.name, b.name)),
    [datasets]
  )
  const visible = q
    ? sorted.filter(
        (dataset) =>
          dataset.name.toLowerCase().includes(q) ||
          dataset.description.toLowerCase().includes(q)
      )
    : sorted
  const allVisibleSelected =
    visible.length > 0 && visible.every((dataset) => selected.has(dataset.id))
  // 只数列表中的：列表刷新后已不存在的选择不会提交。
  const selectedCount = datasets.filter((dataset) =>
    selected.has(dataset.id)
  ).length

  const toggle = (id: number, checked: boolean) => {
    const next = new Set(selected)
    if (checked) {
      next.add(id)
    } else {
      next.delete(id)
    }
    onSelectedChange(next)
  }

  const toggleVisible = () => {
    const next = new Set(selected)
    for (const dataset of visible) {
      if (allVisibleSelected) {
        next.delete(dataset.id)
      } else {
        next.add(dataset.id)
      }
    }
    onSelectedChange(next)
  }

  return (
    <div className={cn("grid gap-2", className)}>
      <div className="flex items-center gap-2">
        {datasets.length > SEARCH_THRESHOLD && (
          <InputGroup className="h-8 flex-1">
            <InputGroupAddon>
              <Search />
            </InputGroupAddon>
            <InputGroupInput
              value={search}
              onChange={(event) => setSearch(event.target.value)}
              placeholder="搜索数据集"
              aria-label="搜索数据集"
              aria-controls={listId}
              disabled={disabled}
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
        )}
        <span className="ml-auto text-xs whitespace-nowrap text-muted-foreground tabular-nums">
          已选 {formatNumber(selectedCount)} 个
        </span>
        {visible.length > 1 && (
          <button
            type="button"
            onClick={toggleVisible}
            disabled={disabled}
            className="text-xs whitespace-nowrap text-muted-foreground underline-offset-4 outline-none hover:text-foreground hover:underline focus-visible:underline disabled:pointer-events-none disabled:opacity-50"
          >
            {allVisibleSelected
              ? q
                ? "取消选择匹配项"
                : "全部取消"
              : q
                ? "选择匹配项"
                : "全选"}
          </button>
        )}
      </div>
      <ul
        id={listId}
        aria-label={label}
        className="grid max-h-64 gap-0.5 overflow-y-auto rounded-lg border p-1"
      >
        {visible.length === 0 ? (
          <li className="px-3 py-6 text-center text-sm text-muted-foreground">
            没有名称或描述包含“{search.trim()}”的数据集
          </li>
        ) : (
          visible.map((dataset) => (
            <DatasetOption
              key={dataset.id}
              dataset={dataset}
              checked={selected.has(dataset.id)}
              onCheckedChange={(checked) => toggle(dataset.id, checked)}
              disabled={disabled}
            />
          ))
        )}
      </ul>
    </div>
  )
}

function DatasetOption({
  dataset,
  checked,
  onCheckedChange,
  disabled,
}: {
  dataset: Dataset
  checked: boolean
  onCheckedChange: (checked: boolean) => void
  disabled: boolean
}) {
  const id = useId()
  const projects = dataset.projects.map((project) => project.name).join("、")

  return (
    <li>
      <label
        htmlFor={id}
        className={cn(
          "flex cursor-pointer items-center gap-3 rounded-md px-2 py-1.5 transition-colors hover:bg-muted/60 has-[:focus-visible]:bg-muted/60",
          checked && "bg-muted/40",
          disabled && "pointer-events-none opacity-60"
        )}
      >
        <Checkbox
          id={id}
          checked={checked}
          onCheckedChange={(value) => onCheckedChange(value === true)}
          disabled={disabled}
        />
        <div className="size-8 shrink-0 overflow-hidden rounded border bg-muted">
          {dataset.covers[0] ? (
            <DatasetThumbnail
              datasetId={dataset.id}
              image={dataset.covers[0]}
              className="size-full"
            />
          ) : (
            <div className="flex size-full items-center justify-center text-muted-foreground">
              <Images className="size-3.5" />
            </div>
          )}
        </div>
        <span className="grid min-w-0 flex-1">
          <span className="truncate text-sm font-medium">{dataset.name}</span>
          <span className="truncate text-xs text-muted-foreground">
            {formatNumber(dataset.image_count)} 张图片
            {projects && ` · 已在 ${projects}`}
          </span>
        </span>
      </label>
    </li>
  )
}
