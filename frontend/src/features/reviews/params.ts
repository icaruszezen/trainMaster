/**
 * 审图编辑器地址栏参数：`?image=` 当前图片（审图任务中的图片 ID），其余是图片
 * 的筛选条件，参数名与图片列表接口相同：`status`、`dataset_id`、`label`、`q`、
 * `edited`、`boxes`、`inference`（默认值不写进地址栏）。
 */
import { parseId } from "@/features/annotation/workspace-params"
import {
  ALL_REVIEW_IMAGES,
  reviewImageFilterParams,
  type ReviewImageFilters,
  type ReviewStatusFilter,
} from "@/features/reviews/api"

export { parseId }

const STATUS_VALUES: readonly ReviewStatusFilter[] = [
  "all",
  "pending",
  "approved",
  "needs_sample",
  "reviewed",
]
const EDITED_VALUES: readonly ReviewImageFilters["edited"][] = [
  "all",
  "edited",
  "unedited",
]
const BOXES_VALUES: readonly ReviewImageFilters["boxes"][] = [
  "all",
  "with",
  "without",
]
const INFERENCE_VALUES: readonly ReviewImageFilters["inference"][] = [
  "all",
  "failed",
]

function oneOf<T extends string>(
  value: string | null,
  values: readonly T[]
): T {
  return value !== null && (values as readonly string[]).includes(value)
    ? (value as T)
    : values[0]
}

/** 地址栏中的筛选条件；无效的取值按默认值（不筛选）处理。 */
export function parseReviewFilters(
  params: URLSearchParams
): ReviewImageFilters {
  return {
    status: oneOf(params.get("status"), STATUS_VALUES),
    datasetId: parseId(params.get("dataset_id")),
    label: params.get("label") ?? "",
    q: params.get("q") ?? "",
    edited: oneOf(params.get("edited"), EDITED_VALUES),
    boxes: oneOf(params.get("boxes"), BOXES_VALUES),
    inference: oneOf(params.get("inference"), INFERENCE_VALUES),
  }
}

/** 地址栏中筛选条件的参数名。 */
const FILTER_PARAMS = [
  "status",
  "dataset_id",
  "label",
  "q",
  "edited",
  "boxes",
  "inference",
] as const

/**
 * 修改参数，返回新的参数：`changes` 中的筛选条件与原来的合并后整体写回（默认值
 * 不写进地址栏），`image` 换成当前图片；其它参数保持不变。
 */
export function updateReviewParams(
  params: URLSearchParams,
  changes: Partial<ReviewImageFilters> & { image?: number }
) {
  const { image, ...filterChanges } = changes
  const next = new URLSearchParams(params)
  if (image !== undefined) {
    next.set("image", String(image))
  }
  if (Object.keys(filterChanges).length > 0) {
    const filters = { ...parseReviewFilters(params), ...filterChanges }
    for (const name of FILTER_PARAMS) {
      next.delete(name)
    }
    for (const [name, value] of Object.entries(
      reviewImageFilterParams(filters)
    )) {
      next.set(name, value)
    }
  }
  return next
}

export function hasReviewFilters(filters: ReviewImageFilters) {
  return Object.keys(reviewImageFilterParams(filters)).length > 0
}

/** 两组筛选条件是否相同（按写进地址栏的参数比较）。 */
export function sameReviewFilters(
  a: ReviewImageFilters,
  b: ReviewImageFilters
) {
  const text = (filters: ReviewImageFilters) =>
    new URLSearchParams(reviewImageFilterParams(filters)).toString()
  return text(a) === text(b)
}

/** 审图任务的详情页。 */
export function reviewDetailPath(reviewId: number) {
  return `/datasets/reviews/${reviewId}`
}

/**
 * 审图编辑器的地址：可选某张图片与筛选条件，如数据集页面的“审图”只看这个数据集
 * （`{ datasetId }`）。不指定图片时编辑器打开第一张待审核的（见 {@link pickStartImage}）。
 */
export function reviewWorkspacePath(
  reviewId: number,
  { image, ...filters }: Partial<ReviewImageFilters> & { image?: number } = {}
) {
  const params = new URLSearchParams()
  if (image !== undefined) {
    params.set("image", String(image))
  }
  for (const [name, value] of Object.entries(
    reviewImageFilterParams({ ...ALL_REVIEW_IMAGES, ...filters })
  )) {
    params.set(name, value)
  }
  const search = params.toString()
  return `${reviewDetailPath(reviewId)}/workspace${search ? `?${search}` : ""}`
}

/** 地址栏中没有指定图片时打开哪张（{@link pickStartImage}）。 */
export type StartImage = {
  /** 图片 ID；审图任务中没有图片时为 null。 */
  id: number | null
  /** 没有找到待审核 / 符合筛选条件的图片、改为打开第一张时的提示。 */
  notice: string | null
}

/**
 * 地址栏中没有指定图片时打开哪张，筛选条件不变：
 * 1. 符合筛选条件的第一张待审核的图片（审核状态筛选“全部”或“待审核”时）。
 *    “网页审图”与数据集的“审图”进来时列表显示全部图片，从第一张待审核的开始。
 * 2. 没有待审核的：第一张符合筛选条件的图片（如复查已审核的图片）。
 * 3. 没有符合筛选条件的图片：审图任务的第一张。
 *
 * `first(filters)` 返回符合 `filters` 的第一张图片（没有时为 null）。
 */
export async function pickStartImage(
  filters: ReviewImageFilters,
  first: (filters: ReviewImageFilters) => Promise<{ id: number } | null>
): Promise<StartImage> {
  const wantsPending = filters.status === "all" || filters.status === "pending"
  if (wantsPending) {
    const pending = await first({ ...filters, status: "pending" })
    if (pending) {
      return { id: pending.id, notice: null }
    }
  }
  if (filters.status !== "pending") {
    const match = await first(filters)
    if (match) {
      return {
        id: match.id,
        notice: !wantsPending
          ? null
          : hasReviewFilters(filters)
            ? "筛选出的图片中没有待审核的，已打开第一张"
            : "没有待审核的图片了，已打开第一张",
      }
    }
  }
  if (!hasReviewFilters(filters)) {
    return { id: null, notice: null } // 审图任务中没有图片
  }
  const any = await first(ALL_REVIEW_IMAGES)
  if (!any) {
    return { id: null, notice: null }
  }
  return {
    id: any.id,
    notice:
      filters.status === "pending"
        ? "没有待审核的图片了，已打开第一张"
        : "没有符合筛选条件的图片，已打开第一张",
  }
}
