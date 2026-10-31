/** 标注工作台地址栏参数：`?image=` 当前图片，`status` / `label` / `q` 图片筛选条件。 */
import type { ImageFilters, ImageStatusFilter } from "@/features/datasets/api"

const STATUS_VALUES: readonly ImageStatusFilter[] = [
  "all",
  "annotated",
  "empty",
  "unannotated",
  "invalid",
]

export const NO_FILTERS: ImageFilters = { status: "all", label: "", q: "" }

/** 正整数 id（如 `?image=12`），其它取值为 null。 */
export function parseId(value: string | null | undefined): number | null {
  if (!value || !/^[1-9]\d{0,15}$/.test(value)) {
    return null
  }
  const id = Number(value)
  return Number.isSafeInteger(id) ? id : null
}

export function parseFilters(params: URLSearchParams): ImageFilters {
  const status = params.get("status") as ImageStatusFilter | null
  return {
    status: status && STATUS_VALUES.includes(status) ? status : "all",
    label: params.get("label") ?? "",
    q: params.get("q") ?? "",
  }
}

/** 修改参数（默认值不写进地址栏），返回新的参数。 */
export function updateParams(
  params: URLSearchParams,
  changes: Partial<ImageFilters> & { image?: number }
) {
  const next = new URLSearchParams(params)
  for (const [key, value] of Object.entries(changes)) {
    const text = String(value)
    if (text === "" || (key === "status" && text === "all")) {
      next.delete(key)
    } else {
      next.set(key, text)
    }
  }
  return next
}

export function hasFilters(filters: ImageFilters) {
  return filters.status !== "all" || filters.label !== "" || filters.q !== ""
}

/** 工作台中某个数据集（可选某张图片）的地址。 */
export function workspacePath(datasetId: number, imageId?: number) {
  const base = `/annotation/workspace/${datasetId}`
  return imageId === undefined ? base : `${base}?image=${imageId}`
}
