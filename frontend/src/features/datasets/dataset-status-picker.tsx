/**
 * 数据集状态的徽标与修改：卡片、列表与详情页上的状态按钮（点开选择状态），
 * 操作菜单中的“设置状态”子菜单。修改后提示结果，并可以撤销。
 */
import { useMutation } from "@tanstack/react-query"
import { ChevronDown, CircleDot } from "lucide-react"
import { toast } from "sonner"

import { Badge } from "@/components/ui/badge"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuLabel,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuSeparator,
  DropdownMenuSub,
  DropdownMenuSubContent,
  DropdownMenuSubTrigger,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { Spinner } from "@/components/ui/spinner"
import {
  datasetKeys,
  updateDataset,
  type Dataset,
  type DatasetStatus,
} from "@/features/datasets/api"
import {
  DATASET_STATUSES,
  datasetStatusHints,
  datasetStatusLabels,
  datasetStatusStyles,
} from "@/features/datasets/dataset-status"
import { projectKeys } from "@/features/projects/api"
import type { ListResponse } from "@/lib/api"
import { queryClient } from "@/lib/query-client"
import { cn } from "@/lib/utils"

export function DatasetStatusDot({
  status,
  className,
}: {
  status: DatasetStatus
  className?: string
}) {
  return (
    <span
      aria-hidden
      className={cn(
        "inline-block size-2 shrink-0 rounded-full",
        datasetStatusStyles[status].dot,
        className
      )}
    />
  )
}

/** 只读的状态徽标；未设置时不显示（`showUnset` 时显示“未设置”）。 */
export function DatasetStatusBadge({
  status,
  showUnset = false,
  className,
}: {
  status: DatasetStatus
  showUnset?: boolean
  className?: string
}) {
  if (!status && !showUnset) {
    return null
  }
  return (
    <Badge
      variant="outline"
      className={cn(datasetStatusStyles[status].badge, className)}
      title={datasetStatusHints[status]}
    >
      <DatasetStatusDot status={status} />
      {datasetStatusLabels[status]}
    </Badge>
  )
}

/** 用修改后的数据集更新缓存：详情与数据集列表就地替换，其余重新读取。 */
function applyUpdatedDataset(updated: Dataset) {
  queryClient.setQueryData(datasetKeys.detail(updated.id), updated)
  queryClient.setQueriesData<ListResponse<Dataset>>(
    { queryKey: datasetKeys.lists() },
    (old) =>
      old && {
        ...old,
        items: old.items.map((item) =>
          item.id === updated.id ? updated : item
        ),
      }
  )
  void queryClient.invalidateQueries({ queryKey: datasetKeys.lists() })
  // 项目中的数据集列表、生成数据版本时的数据集都显示状态。
  void queryClient.invalidateQueries({ queryKey: projectKeys.all })
}

/** 修改数据集的状态并更新缓存，返回修改后的数据集。 */
export async function setDatasetStatus(
  dataset: Pick<Dataset, "id">,
  status: DatasetStatus
) {
  const updated = await updateDataset(dataset.id, { status })
  applyUpdatedDataset(updated)
  return updated
}

function statusChangedText(name: string, status: DatasetStatus) {
  return status
    ? `已将“${name}”标记为“${datasetStatusLabels[status]}”`
    : `已清除“${name}”的状态`
}

/**
 * 修改数据集的状态。成功后提示，并可以撤销（改回原来的状态）；修改与撤销都在
 * 组件卸载后（如按状态筛选时数据集离开了列表）照常完成。
 */
export function useSetDatasetStatus() {
  return useMutation({
    mutationFn: ({
      dataset,
      status,
    }: {
      dataset: Dataset
      status: DatasetStatus
    }) => setDatasetStatus(dataset, status),
    onSuccess: (updated, { dataset }) => {
      const previous = dataset.status
      toast.success(statusChangedText(updated.name, updated.status), {
        action: {
          label: "撤销",
          onClick: () => {
            setDatasetStatus(updated, previous).then(
              () => toast.success(statusChangedText(updated.name, previous)),
              (error: Error) =>
                toast.error("撤销失败", { description: error.message })
            )
          },
        },
      })
    },
    onError: (error, { dataset }) => {
      toast.error(`无法修改“${dataset.name}”的状态`, {
        description: error.message,
      })
    },
  })
}

/** 状态的单选列表（状态按钮的菜单与“设置状态”子菜单共用）。 */
function StatusRadioItems({
  dataset,
  onSelect,
}: {
  dataset: Dataset
  onSelect: (status: DatasetStatus) => void
}) {
  return (
    <DropdownMenuRadioGroup
      value={dataset.status || "none"}
      onValueChange={(value) => {
        const status = value === "none" ? "" : (value as DatasetStatus)
        if (status !== dataset.status) {
          onSelect(status)
        }
      }}
    >
      {DATASET_STATUSES.map((status) => (
        <DropdownMenuRadioItem key={status} value={status}>
          <DatasetStatusDot status={status} />
          <span className="grid">
            <span>{datasetStatusLabels[status]}</span>
            <span className="text-xs text-muted-foreground">
              {datasetStatusHints[status]}
            </span>
          </span>
        </DropdownMenuRadioItem>
      ))}
      <DropdownMenuSeparator />
      <DropdownMenuRadioItem value="none" className="text-muted-foreground">
        <DatasetStatusDot status="" />
        未设置
      </DropdownMenuRadioItem>
    </DropdownMenuRadioGroup>
  )
}

/**
 * 状态按钮：显示当前状态（未设置时为虚线的“设置状态”），点开选择。卡片中
 * 伸展的链接覆盖了整张卡片，调用方用 `className` 把按钮浮在它上面。
 */
export function DatasetStatusPicker({
  dataset,
  align = "start",
  className,
}: {
  dataset: Dataset
  align?: "start" | "end"
  className?: string
}) {
  const mutation = useSetDatasetStatus()
  const { status } = dataset
  const pending = mutation.isPending

  return (
    <DropdownMenu modal={false}>
      <DropdownMenuTrigger asChild disabled={pending}>
        <button
          type="button"
          className={cn(
            "group/status inline-flex h-6 shrink-0 items-center gap-1 rounded-full border px-2 text-xs font-medium whitespace-nowrap transition-[color,box-shadow,background-color] outline-none hover:brightness-95 focus-visible:ring-[3px] focus-visible:ring-ring/50 disabled:opacity-70 data-[state=open]:ring-[3px] data-[state=open]:ring-ring/30 dark:hover:brightness-110",
            datasetStatusStyles[status].badge,
            !status && "hover:bg-muted hover:text-foreground",
            className
          )}
          aria-label={`数据集状态：${datasetStatusLabels[status]}，点击修改`}
        >
          {pending ? (
            <Spinner className="size-3" aria-hidden />
          ) : status ? (
            <DatasetStatusDot status={status} />
          ) : (
            <CircleDot className="size-3" aria-hidden />
          )}
          {status ? datasetStatusLabels[status] : "设置状态"}
          <ChevronDown
            className="size-3 opacity-60 transition-transform group-data-[state=open]/status:rotate-180"
            aria-hidden
          />
        </button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align={align} className="w-52">
        <DropdownMenuLabel className="text-xs font-normal text-muted-foreground">
          数据集状态
        </DropdownMenuLabel>
        <StatusRadioItems
          dataset={dataset}
          onSelect={(next) => mutation.mutate({ dataset, status: next })}
        />
      </DropdownMenuContent>
    </DropdownMenu>
  )
}

/** 操作菜单中的“设置状态”子菜单。 */
export function DatasetStatusSubmenu({ dataset }: { dataset: Dataset }) {
  const mutation = useSetDatasetStatus()
  return (
    <DropdownMenuSub>
      <DropdownMenuSubTrigger disabled={mutation.isPending}>
        <CircleDot />
        设置状态
      </DropdownMenuSubTrigger>
      <DropdownMenuSubContent className="w-52">
        <StatusRadioItems
          dataset={dataset}
          onSelect={(next) => mutation.mutate({ dataset, status: next })}
        />
      </DropdownMenuSubContent>
    </DropdownMenuSub>
  )
}
