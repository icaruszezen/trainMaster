/** 图片列表的纯函数：标注状态的筛选项与文案、分页结果的合并（便于测试）。 */
import type {
  AnnotationState,
  Dataset,
  DatasetImage,
  DatasetImageDetail,
  ImageStatusFilter,
} from "@/features/datasets/api"
import type { ListResponse } from "@/lib/api"

/** 网格角标等处使用的标注状态名称。 */
export const annotationStateLabels: Record<AnnotationState, string> = {
  none: "未标注",
  invalid: "标注无效",
  empty: "空标注",
  annotated: "已标注",
}

type DatasetCounts = Pick<
  Dataset,
  "image_count" | "annotated_count" | "empty_count" | "invalid_count"
>

/** 没有标注文件的图片数（不含 xml 无法解析的图片）。 */
export function unannotatedCount(dataset: DatasetCounts) {
  return Math.max(
    0,
    dataset.image_count -
      dataset.annotated_count -
      dataset.empty_count -
      dataset.invalid_count
  )
}

export type ImageStatusOption = {
  value: ImageStatusFilter
  label: string
  count: number
}

/**
 * 标注状态筛选项与数量。“标注无效”只在有无法解析的 xml 时出现（或者地址栏中
 * 已经选中它），平时不占位置。
 */
export function imageStatusOptions(
  dataset: DatasetCounts,
  current: ImageStatusFilter
): ImageStatusOption[] {
  const options: ImageStatusOption[] = [
    { value: "all", label: "全部", count: dataset.image_count },
    { value: "annotated", label: "已标注", count: dataset.annotated_count },
    { value: "empty", label: "空标注", count: dataset.empty_count },
    { value: "unannotated", label: "未标注", count: unannotatedCount(dataset) },
  ]
  if (dataset.invalid_count > 0 || current === "invalid") {
    options.push({
      value: "invalid",
      label: annotationStateLabels.invalid,
      count: dataset.invalid_count,
    })
  }
  return options
}

/**
 * 合并已加载的分页。导入或扫描进行中时索引在变化，按偏移量翻页可能拿到
 * 已显示过的图片：按 id 去重，保留先出现的。
 */
export function flattenImagePages(
  pages: readonly ListResponse<DatasetImage>[] | undefined
): DatasetImage[] {
  const seen = new Set<number>()
  const images: DatasetImage[] = []
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
  data?: { pages: readonly ListResponse<DatasetImage>[] }
  isFetchNextPageError: boolean
  error: Error | null
}

/**
 * 加载下一页，返回加载后（去重的）图片数。React Query 的 `fetchNextPage()`
 * 失败时也会正常返回，这里改为抛出错误，调用方才不会把失败当成已加载。
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

export type AnnotationPanel = {
  /** 标注文件相对于数据集文件夹的路径；没有配对的 xml 时为 null。 */
  path: string | null
  /** 无法读取标注的原因（中文），否则为 `""`。 */
  error: string
  /** 显示“无标注文件”：没有配对的 xml，也没有错误。 */
  noFile: boolean
  /** 显示“空标注（负样本）”：标注文件可以读取，但没有目标。 */
  empty: boolean
}

/**
 * 查看大图时“标注”一栏的内容。详情是此刻对磁盘上 xml 的解析，比列表（索引）
 * 新，以详情为准；详情还没加载到（或加载失败）时先用列表中的状态。
 */
export function annotationPanel(
  image: DatasetImage,
  detail: DatasetImageDetail | undefined
): AnnotationPanel {
  const current = detail ?? image
  const error = detail?.annotation_error ?? ""
  return {
    path: current.annotation_path,
    error,
    noFile: current.annotation_path === null && error === "",
    empty: current.annotation_state === "empty" && error === "",
  }
}
