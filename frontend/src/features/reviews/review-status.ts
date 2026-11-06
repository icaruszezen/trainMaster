/**
 * 审核状态与审图任务状态的名称、配色，以及与界面无关的计算：各状态的图片数、
 * 保存或标记后计数与列表缓存的更新、审图标注与模型识别结果的比较。
 */
import type { AnnotationObject } from "@/features/datasets/api"
import type {
  ReviewCounts,
  ReviewImage,
  ReviewImageDetail,
  ReviewImageStatus,
  ReviewPrediction,
  ReviewStage,
  ReviewStatus,
  ReviewStatusFilter,
  ReviewSummary,
} from "@/features/reviews/api"
import type { ListResponse } from "@/lib/api"

/** 审核状态，按界面上的顺序。 */
export const REVIEW_IMAGE_STATUSES: readonly ReviewImageStatus[] = [
  "pending",
  "approved",
  "needs_sample",
]

export const reviewImageStatusLabels: Record<ReviewImageStatus, string> = {
  pending: "待审核",
  approved: "已审核",
  needs_sample: "需加样",
}

/** 审核状态的含义（按钮与提示中的说明）。 */
export const reviewImageStatusHints: Record<ReviewImageStatus, string> = {
  pending: "还没有审核",
  approved: "识别结果正确，模型不需要再学习这张图片",
  needs_sample: "这张图片需要参与后续的训练",
}

/** 标记审核状态的快捷键（见 annotation/shortcuts 的 review 模式）。 */
export const reviewImageStatusKeys: Record<ReviewImageStatus, string> = {
  pending: "U",
  approved: "S",
  needs_sample: "X",
}

/**
 * 审核状态的配色（浅色与深色主题都清楚）：待审核为中性的灰色，已审核为绿色，
 * 需加样为琥珀色。
 * - `dot`：圆点 / 色条的底色；
 * - `badge`：徽标（浅底、深色文字、描边）；
 * - `text`：文字颜色；
 * - `active`：选中的状态按钮（实心）；
 * - `idle`：未选中的状态按钮（描边，悬停时浅底）。
 */
export const reviewImageStatusStyles: Record<
  ReviewImageStatus,
  { dot: string; badge: string; text: string; active: string; idle: string }
> = {
  pending: {
    dot: "bg-muted-foreground/45",
    badge: "border-border bg-muted text-muted-foreground",
    text: "text-muted-foreground",
    active:
      "border-transparent bg-secondary text-secondary-foreground ring-2 ring-muted-foreground/30",
    idle: "hover:bg-muted",
  },
  approved: {
    dot: "bg-emerald-500",
    badge:
      "border-emerald-600/30 bg-emerald-500/12 text-emerald-700 dark:border-emerald-400/30 dark:text-emerald-400",
    text: "text-emerald-700 dark:text-emerald-400",
    active:
      "border-transparent bg-emerald-600 text-white hover:bg-emerald-600/90 dark:bg-emerald-600 dark:hover:bg-emerald-600/90",
    idle: "border-emerald-600/40 text-emerald-700 hover:bg-emerald-500/10 dark:border-emerald-400/40 dark:text-emerald-400",
  },
  needs_sample: {
    dot: "bg-amber-500",
    badge:
      "border-amber-600/30 bg-amber-500/15 text-amber-800 dark:border-amber-400/30 dark:text-amber-400",
    text: "text-amber-700 dark:text-amber-400",
    active:
      "border-transparent bg-amber-500 text-amber-950 hover:bg-amber-500/90 dark:bg-amber-500 dark:hover:bg-amber-500/90",
    idle: "border-amber-600/40 text-amber-800 hover:bg-amber-500/10 dark:border-amber-400/40 dark:text-amber-400",
  },
}

export const reviewStatusLabels: Record<ReviewStatus, string> = {
  queued: "排队中",
  running: "识别中",
  ready: "识别完成",
  failed: "识别失败",
}

export const reviewStageLabels: Record<ReviewStage, string> = {
  "": "",
  downloading: "正在下载模型",
  inferring: "正在识别",
}

type InferenceState = Pick<
  ReviewSummary,
  "status" | "model_exists" | "queue_position"
>

/**
 * 排队但模型已删除、还在回收站中：平台把它移出了队列（没有 `queue_position`），
 * 训练节点不会领取，恢复模型后才开始识别，模型被永久删除后任务失败。模型已被
 * 永久删除的排队任务仍在队列中（训练节点领取时失败），不算。
 */
export function waitingForModel(review: InferenceState) {
  return (
    review.status === "queued" &&
    review.model_exists === false &&
    review.queue_position === null
  )
}

/** 还在排队或识别中（之后还会有图片识别完成）；等待恢复模型的不算。 */
export function isInferring(review: InferenceState) {
  return (
    (review.status === "queued" || review.status === "running") &&
    !waitingForModel(review)
  )
}

/** 一个数据集（`datasetId`）或整个任务（null）的图片数。数据集不在任务中时为整个任务的。 */
export function scopedCounts(
  review: Pick<ReviewSummary, "counts" | "datasets">,
  datasetId: number | null
): ReviewCounts {
  if (datasetId === null) {
    return review.counts
  }
  return (
    review.datasets.find((dataset) => dataset.id === datasetId)?.counts ??
    review.counts
  )
}

export type StatusFilterOption = {
  value: ReviewStatusFilter
  label: string
  count: number
}

/** 审核状态筛选的选项（全部 / 待审核 / 已审核 / 需加样）与各自的图片数。 */
export function statusFilterOptions(
  counts: ReviewCounts
): StatusFilterOption[] {
  return [
    { value: "all", label: "全部", count: counts.total },
    ...REVIEW_IMAGE_STATUSES.map((status) => ({
      value: status,
      label: reviewImageStatusLabels[status],
      count: counts[status],
    })),
  ]
}

type CountedImage = Pick<ReviewImage, "dataset_id" | "status" | "edited">

function shift(
  counts: ReviewCounts,
  before: CountedImage,
  after: CountedImage
): ReviewCounts {
  const next = { ...counts }
  if (before.status !== after.status) {
    next[before.status] = Math.max(0, next[before.status] - 1)
    next[after.status] += 1
  }
  if (before.edited !== after.edited) {
    next.edited = Math.max(0, next.edited + (after.edited ? 1 : -1))
  }
  return next
}

/**
 * 一张图片的审核状态或“已修改”变了之后的图片数（整个任务与它所在的数据集），
 * 用于保存、标记后立即更新界面上的计数（随后再从服务端读取）。没有变化时返回
 * 原对象。
 */
export function adjustReviewCounts<
  T extends Pick<ReviewSummary, "counts" | "datasets">,
>(review: T, before: CountedImage, after: CountedImage): T {
  if (before.status === after.status && before.edited === after.edited) {
    return review
  }
  return {
    ...review,
    counts: shift(review.counts, before, after),
    datasets: review.datasets.map((dataset) =>
      dataset.id === after.dataset_id
        ? { ...dataset, counts: shift(dataset.counts, before, after) }
        : dataset
    ),
  }
}

/** 图片详情中列表项的部分（去掉框等只在详情中的字段）。 */
export function toReviewImage(detail: ReviewImageDetail): ReviewImage {
  // eslint-disable-next-line @typescript-eslint/no-unused-vars -- 去掉这些字段
  const { objects, predictions, dataset_name, annotation_error, ...image } =
    detail
  return image
}

/** 列表的一页中换上这张图片的最新状态；不在这一页时返回原对象。 */
export function replaceReviewImage(
  page: ListResponse<ReviewImage>,
  image: ReviewImage
): ListResponse<ReviewImage> {
  const index = page.items.findIndex((item) => item.id === image.id)
  if (index === -1) {
    return page
  }
  const items = [...page.items]
  items[index] = image
  return { ...page, items }
}

type BoxKey = Pick<AnnotationObject, "name" | "xmin" | "ymin" | "xmax" | "ymax">

const boxKey = (box: BoxKey) =>
  JSON.stringify([box.name, box.xmin, box.ymin, box.xmax, box.ymax])

/**
 * 审图标注与模型的识别结果是否相同：按（类别，坐标）作为多重集合比较，不管
 * 顺序、难例 / 截断与置信度（与后端 `reviewfile.SameBoxes` 一致）。不同时图片
 * 算作“已修改”。
 */
export function sameBoxes(
  objects: readonly BoxKey[],
  predictions: readonly BoxKey[]
) {
  if (objects.length !== predictions.length) {
    return false
  }
  const counts = new Map<string, number>()
  for (const prediction of predictions) {
    const key = boxKey(prediction)
    counts.set(key, (counts.get(key) ?? 0) + 1)
  }
  for (const object of objects) {
    const key = boxKey(object)
    const left = counts.get(key) ?? 0
    if (left === 0) {
      return false
    }
    counts.set(key, left - 1)
  }
  return true
}

/**
 * 与模型识别出的框完全相同（类别与坐标）的框的置信度，按框的 uid。每个识别
 * 结果只对应一个框；修改过的、新画的框没有置信度。
 */
export function matchConfidences(
  objects: readonly (BoxKey & { uid: string })[],
  predictions: readonly ReviewPrediction[]
): Map<string, number> {
  const available = new Map<string, number[]>()
  for (const prediction of predictions) {
    const key = boxKey(prediction)
    const list = available.get(key)
    if (list) {
      list.push(prediction.confidence)
    } else {
      available.set(key, [prediction.confidence])
    }
  }
  for (const list of available.values()) {
    list.sort((a, b) => b - a)
  }
  const matched = new Map<string, number>()
  for (const object of objects) {
    const confidence = available.get(boxKey(object))?.shift()
    if (confidence !== undefined) {
      matched.set(object.uid, confidence)
    }
  }
  return matched
}

/**
 * 审图标注中已经没有的模型识别结果（被删除、移动或改了类别的框），按下标：
 * 与 {@link matchConfidences} 相同的一一对应，置信度高的先对应。画布上用虚线
 * 显示它们，对照模型原来识别出的位置。
 */
export function unmatchedPredictions(
  objects: readonly BoxKey[],
  predictions: readonly ReviewPrediction[]
): number[] {
  const remaining = new Map<string, number>()
  for (const object of objects) {
    const key = boxKey(object)
    remaining.set(key, (remaining.get(key) ?? 0) + 1)
  }
  const order = predictions
    .map((prediction, index) => ({ prediction, index }))
    .sort((a, b) => b.prediction.confidence - a.prediction.confidence)
  const unmatched: number[] = []
  for (const { prediction, index } of order) {
    const key = boxKey(prediction)
    const left = remaining.get(key) ?? 0
    if (left > 0) {
      remaining.set(key, left - 1)
    } else {
      unmatched.push(index)
    }
  }
  return unmatched.sort((a, b) => a - b)
}

/** 模型识别结果换成审图标注的框（“恢复模型识别结果”）：不是难例，没有截断。 */
export function predictionsToObjects(
  predictions: readonly ReviewPrediction[]
): AnnotationObject[] {
  return predictions.map(({ name, xmin, ymin, xmax, ymax }) => ({
    name,
    xmin,
    ymin,
    xmax,
    ymax,
    difficult: false,
    truncated: false,
  }))
}

/** 置信度的显示：两位小数，如 `0.87`。 */
export function formatConfidence(confidence: number) {
  return confidence.toFixed(2)
}
