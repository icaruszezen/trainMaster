/**
 * 数据集状态的名称、配色与列表筛选（与界面无关的部分）。状态的取值见
 * `api.ts` 的 {@link DatasetStatus}。
 */
import type { Dataset, DatasetStatus } from "@/features/datasets/api"

/** 可以标记的状态，按工作流的顺序（不含未设置）。 */
export const DATASET_STATUSES: readonly Exclude<DatasetStatus, "">[] = [
  "pending_annotation",
  "annotating",
  "pending_review",
  "reviewing",
  "trainable",
]

export const datasetStatusLabels: Record<DatasetStatus, string> = {
  "": "未设置",
  pending_annotation: "待标注",
  annotating: "标注中",
  pending_review: "待审图",
  reviewing: "审图中",
  trainable: "可训练",
}

/** 状态的含义（菜单与提示中的说明）。 */
export const datasetStatusHints: Record<DatasetStatus, string> = {
  "": "还没有标记状态",
  pending_annotation: "图片已就绪，等待标注",
  annotating: "正在标注",
  pending_review: "标注完成，等待审图",
  reviewing: "正在审图",
  trainable: "审图通过，可以用于训练",
}

/**
 * 状态的配色（浅色与深色主题都清楚）：待标注为灰蓝色，标注中为蓝色，待审图为
 * 琥珀色，审图中为紫色，可训练为绿色；未设置为虚线描边的中性色。
 * - `dot`：圆点的底色；
 * - `badge`：徽标（浅底、深色文字、描边）。
 */
export const datasetStatusStyles: Record<
  DatasetStatus,
  { dot: string; badge: string }
> = {
  "": {
    dot: "border border-dashed border-muted-foreground/60",
    badge:
      "border-dashed border-muted-foreground/40 bg-transparent text-muted-foreground",
  },
  pending_annotation: {
    dot: "bg-slate-500 dark:bg-slate-400",
    badge:
      "border-slate-500/30 bg-slate-500/12 text-slate-700 dark:border-slate-400/30 dark:bg-slate-400/15 dark:text-slate-300",
  },
  annotating: {
    dot: "bg-blue-500",
    badge:
      "border-blue-600/30 bg-blue-500/12 text-blue-700 dark:border-blue-400/30 dark:text-blue-400",
  },
  pending_review: {
    dot: "bg-amber-500",
    badge:
      "border-amber-600/30 bg-amber-500/15 text-amber-800 dark:border-amber-400/30 dark:text-amber-400",
  },
  reviewing: {
    dot: "bg-violet-500",
    badge:
      "border-violet-600/30 bg-violet-500/12 text-violet-700 dark:border-violet-400/30 dark:text-violet-400",
  },
  trainable: {
    dot: "bg-emerald-500",
    badge:
      "border-emerald-600/30 bg-emerald-500/12 text-emerald-700 dark:border-emerald-400/30 dark:text-emerald-400",
  },
}

/** 数据集列表的状态筛选：某个状态、`""`（未设置）或不限（null）。 */
export type DatasetStatusFilter = DatasetStatus | null

/**
 * 解析地址栏 `?status=` 的取值：状态名或 `none`（未设置），其它值视为不筛选。
 * 与后端 `GET /datasets?status=` 的取值相同。
 */
export function parseStatusFilter(value: string | null): DatasetStatusFilter {
  if (value === "none") {
    return ""
  }
  return DATASET_STATUSES.find((status) => status === value) ?? null
}

/** {@link parseStatusFilter} 的反向：写入地址栏的取值，不筛选时为 null。 */
export function statusFilterParam(filter: DatasetStatusFilter) {
  return filter === null ? null : filter || "none"
}

export function filterByStatus<T extends Pick<Dataset, "status">>(
  datasets: T[],
  filter: DatasetStatusFilter
) {
  return filter === null
    ? datasets
    : datasets.filter((dataset) => dataset.status === filter)
}

/** 各状态（含未设置）的数据集数。 */
export function countByStatus(datasets: Pick<Dataset, "status">[]) {
  const counts = Object.fromEntries(
    Object.keys(datasetStatusLabels).map((status) => [status, 0])
  ) as Record<DatasetStatus, number>
  for (const dataset of datasets) {
    counts[dataset.status] = (counts[dataset.status] ?? 0) + 1
  }
  return counts
}
