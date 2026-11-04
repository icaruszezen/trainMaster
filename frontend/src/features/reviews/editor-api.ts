/**
 * 审图编辑器的保存、标记与缓存更新（只通过 {@link ReviewSource}，平台与本地审图
 * 程序共用）。做法同标注工作台（`features/annotation/api.ts`）：编辑中的图片详情
 * 单独缓存，写入前取消这张图片还没有返回的读取，冲突时让它失效；写入后更新
 * 详情、图片列表与计数，再让依赖它们的查询重新读取。
 */
import type { QueryClient } from "@tanstack/react-query"

import {
  isConflictError,
  type SaveRequest,
} from "@/features/annotation/autosave"
import type { AnnotationObject } from "@/features/datasets/api"
import {
  reviewSourceKeys,
  saveReviewAnnotation,
  setReviewImageStatus,
  type ReviewImage,
  type ReviewImageDetail,
  type ReviewImageStatus,
  type ReviewSource,
  type ReviewSummary,
} from "@/features/reviews/api"
import {
  adjustReviewCounts,
  replaceReviewImage,
  toReviewImage,
} from "@/features/reviews/review-status"
import type { ListResponse } from "@/lib/api"

/** 编辑会话的上下文：保存到哪个审图任务的哪张图片。 */
export type ReviewSaveContext = { source: ReviewSource; imageId: number }

/**
 * 写入审图标注。审图标注总是整个文件重新生成：没有框时写入没有目标的文件，
 * 图片识别过就有文件，没有“未标注”与“空标注”之分。`init` 用于关闭页面时补发的
 * `keepalive` 请求。
 */
export function persistReviewAnnotation(
  objects: AnnotationObject[],
  baseVersion: string,
  force: boolean,
  context: ReviewSaveContext,
  init?: RequestInit
) {
  return saveReviewAnnotation(
    context.source,
    context.imageId,
    { objects, base_version: baseVersion, force },
    init
  )
}

/**
 * 写入之前调用：取消这张图片还没有返回的读取（如冲突后的重新读取）。它们读到
 * 的是写入之前的文件，晚于写入返回时会替换缓存中保存的结果。
 */
export async function cancelReviewImageReads(
  queryClient: QueryClient,
  source: ReviewSource,
  imageId: number
) {
  await queryClient.cancelQueries({
    queryKey: reviewSourceKeys.image(source, imageId),
  })
}

/**
 * 保存时文件已被他人修改（`annotation_conflict`）：让缓存的详情失效（正在查看时
 * 在后台重新读取，编辑器中的框不变），放弃修改或之后再打开时显示文件的当前内容。
 */
export function markReviewImageOutdated(
  queryClient: QueryClient,
  source: ReviewSource,
  imageId: number
) {
  void queryClient.invalidateQueries({
    queryKey: reviewSourceKeys.image(source, imageId),
  })
}

/**
 * 这张图片最后保存的详情：保存或标记后由 {@link applyReviewImage} 写入缓存；
 * 缓存中没有时为 `loaded`（编辑器载入时的详情）。
 */
export function savedReviewDetail(
  queryClient: QueryClient,
  source: ReviewSource,
  loaded: ReviewImageDetail
) {
  return (
    queryClient.getQueryData<ReviewImageDetail>(
      reviewSourceKeys.image(source, loaded.id)
    ) ?? loaded
  )
}

const labelsKey = (image: Pick<ReviewImage, "labels"> | undefined) =>
  JSON.stringify(image?.labels ?? null)

/**
 * 保存或标记后更新缓存：编辑器的图片详情直接替换；图片列表中这张图片换成新的
 * 状态；审核状态或“已修改”变了时立即调整任务（及其数据集）的图片数。随后让
 * 任务信息、图片列表与相邻图片（筛选结果可能变了）重新读取；审图标注的类别变了
 * 时类别统计也重新读取，它也是候选类别。
 */
export function applyReviewImage(
  queryClient: QueryClient,
  source: ReviewSource,
  detail: ReviewImageDetail
) {
  const imageKey = reviewSourceKeys.image(source, detail.id)
  const previous = queryClient.getQueryData<ReviewImageDetail>(imageKey)
  queryClient.setQueryData(imageKey, detail)
  const image = toReviewImage(detail)
  queryClient.setQueriesData<ListResponse<ReviewImage>>(
    { queryKey: reviewSourceKeys.images(source) },
    (page) => (page ? replaceReviewImage(page, image) : page)
  )
  if (previous) {
    queryClient.setQueryData<ReviewSummary>(
      reviewSourceKeys.summary(source),
      (review) =>
        review ? adjustReviewCounts(review, previous, image) : review
    )
  }
  void queryClient.invalidateQueries({
    queryKey: reviewSourceKeys.summary(source),
    exact: true,
  })
  void queryClient.invalidateQueries({
    queryKey: reviewSourceKeys.images(source),
  })
  void queryClient.invalidateQueries({
    queryKey: reviewSourceKeys.neighbors(source),
  })
  if (!previous || labelsKey(previous) !== labelsKey(image)) {
    void queryClient.invalidateQueries({
      queryKey: reviewSourceKeys.labels(source),
    })
  }
}

/**
 * 编辑器的保存函数（`AnnotationSaver` 的 `save`）：写入后更新缓存，返回文件的
 * 新版本。写入前取消这张图片之前开始的读取，冲突时让缓存的详情失效。
 */
export async function saveReviewEditorAnnotation(
  queryClient: QueryClient,
  { objects, baseVersion, force, context }: SaveRequest<ReviewSaveContext>
) {
  const { source, imageId } = context
  await cancelReviewImageReads(queryClient, source, imageId)
  let saved: ReviewImageDetail
  try {
    saved = await persistReviewAnnotation(objects, baseVersion, force, context)
  } catch (error) {
    if (isConflictError(error)) {
      markReviewImageOutdated(queryClient, source, imageId)
    }
    throw error
  }
  applyReviewImage(queryClient, source, saved)
  return saved.annotation_version
}

/**
 * 修改图片的审核状态并更新缓存，返回修改后的详情。调用前先保存这张图片的修改
 * （编辑器中先 flush）：返回的详情中的框是文件此刻的内容。
 */
export async function markReviewImage(
  queryClient: QueryClient,
  source: ReviewSource,
  imageId: number,
  status: ReviewImageStatus
) {
  await cancelReviewImageReads(queryClient, source, imageId)
  const detail = await setReviewImageStatus(source, imageId, status)
  applyReviewImage(queryClient, source, detail)
  return detail
}
