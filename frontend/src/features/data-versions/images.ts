/**
 * 数据版本详情“图片”标签页的纯逻辑（见 images.test.ts）：地址栏中的筛选
 * 条件、分页结果的合并与大图地址。
 */
import {
  dataVersionPath,
  EMPTY_IMAGE_FILTERS,
  SPLITS,
  type DataVersionImage,
  type DataVersionImageFilters,
  type Split,
} from "@/features/data-versions/api"
import { parsePositiveId } from "@/features/data-versions/paths"
import type { ListResponse } from "@/lib/api"

/** 地址栏参数名（tab 之外）。 */
export const IMAGE_FILTER_PARAMS = [
  "split",
  "label",
  "dataset",
  "augmented",
  "q",
] as const

/** 从地址栏读筛选条件；无效的值当作未筛选。 */
export function parseImageFilters(
  params: Pick<URLSearchParams, "get">
): DataVersionImageFilters {
  const split = params.get("split")
  const augmented = params.get("augmented")
  return {
    split: SPLITS.includes(split as Split) ? (split as Split) : "",
    label: params.get("label") ?? "",
    dataset_id: parsePositiveId(params.get("dataset")),
    augmented:
      augmented === "true" ? true : augmented === "false" ? false : null,
    q: params.get("q") ?? "",
  }
}

/** 写回地址栏的参数；默认值为 `""`（删除该参数）。 */
export function imageFilterParams(
  changes: Partial<DataVersionImageFilters>
): Record<string, string> {
  const params: Record<string, string> = {}
  if ("split" in changes) {
    params.split = changes.split ?? ""
  }
  if ("label" in changes) {
    params.label = changes.label ?? ""
  }
  if ("dataset_id" in changes) {
    params.dataset =
      changes.dataset_id === null || changes.dataset_id === undefined
        ? ""
        : String(changes.dataset_id)
  }
  if ("augmented" in changes) {
    params.augmented =
      changes.augmented === null || changes.augmented === undefined
        ? ""
        : String(changes.augmented)
  }
  if ("q" in changes) {
    params.q = changes.q ?? ""
  }
  return params
}

export function isFiltered(filters: DataVersionImageFilters) {
  return (
    filters.split !== EMPTY_IMAGE_FILTERS.split ||
    filters.label !== EMPTY_IMAGE_FILTERS.label ||
    filters.dataset_id !== EMPTY_IMAGE_FILTERS.dataset_id ||
    filters.augmented !== EMPTY_IMAGE_FILTERS.augmented ||
    filters.q !== EMPTY_IMAGE_FILTERS.q
  )
}

/** 合并已加载的各页（按 id 去重，保持顺序）。 */
export function flattenImagePages(
  pages: readonly ListResponse<DataVersionImage>[] | undefined
): DataVersionImage[] {
  const seen = new Set<number>()
  const images: DataVersionImage[] = []
  for (const page of pages ?? []) {
    for (const image of page.items) {
      if (!seen.has(image.id)) {
        seen.add(image.id)
        images.push(image)
      }
    }
  }
  return images
}

/** `fetchNextPage()` 结果中用到的部分。 */
type NextPageResult = {
  data?: { pages: readonly ListResponse<DataVersionImage>[] }
  isFetchNextPageError: boolean
  error: Error | null
}

/**
 * 加载下一页，返回加载后的图片数。`fetchNextPage()` 失败时也会正常返回，
 * 这里改为抛出错误，查看器才不会把失败当成已加载。
 */
export async function loadMoreImages(
  fetchNextPage: () => Promise<NextPageResult>
): Promise<number> {
  const result = await fetchNextPage()
  if (result.isFetchNextPageError) {
    throw result.error ?? new Error("加载失败")
  }
  return flattenImagePages(result.data?.pages).length
}

/** 查看大图时用的大号缩略图（服务端按文件中的像素方向生成，不按 EXIF 旋转）。 */
export function largeThumbnailUrl(versionId: number, imageId: number) {
  return `${dataVersionPath(versionId)}/images/${imageId}/thumbnail?size=lg`
}
