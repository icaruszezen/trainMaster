/**
 * 标注工作台的查询与保存。接口见 docs/architecture.md 的“标注接口”，类型在
 * `features/datasets/api.ts`。
 */
import { keepPreviousData, queryOptions } from "@tanstack/react-query"
import type { QueryClient } from "@tanstack/react-query"

import {
  datasetKeys,
  datasetPath,
  deleteAnnotation,
  imageFilterParams,
  saveAnnotation,
  type AnnotationObject,
  type DatasetImage,
  type DatasetImageDetail,
  type ImageFilters,
} from "@/features/datasets/api"
import { projectKeys } from "@/features/projects/api"
import { apiGet, type ListResponse } from "@/lib/api"

export const annotationKeys = {
  /**
   * 编辑中的图片详情。不放在 `datasetKeys.detail(id)` 下：数据集的查询整体
   * 失效（如保存后刷新计数）时不重新获取它，编辑中的框不会被后台刷新覆盖。
   */
  image: (datasetId: number, imageId: number) =>
    ["annotation", "image", datasetId, imageId] as const,
  page: (datasetId: number, filters: ImageFilters, offset: number) =>
    [...datasetKeys.detail(datasetId), "images-page", filters, offset] as const,
}

function imageDetailPath(datasetId: number, imageId: number) {
  return `${datasetPath(datasetId)}/images/${imageId}`
}

export function editorImageQuery(datasetId: number, imageId: number) {
  return queryOptions({
    queryKey: annotationKeys.image(datasetId, imageId),
    queryFn: ({ signal }) =>
      apiGet<DatasetImageDetail>(imageDetailPath(datasetId, imageId), {
        signal,
      }),
    // 30 秒内重新打开同一张图片不再请求（含预加载的前后图片）；之后打开时
    // 重新读取文件，看到他人的修改。编辑期间不在后台刷新。
    staleTime: 30_000,
    refetchOnWindowFocus: false,
    refetchOnReconnect: false,
  })
}

export const IMAGE_LIST_PAGE_SIZE = 50

function listPath(
  datasetId: number,
  filters: ImageFilters,
  offset: number,
  limit: number
) {
  const params = new URLSearchParams({
    offset: String(offset),
    limit: String(limit),
  })
  for (const [key, value] of Object.entries(imageFilterParams(filters))) {
    if (value !== "") {
      params.set(key, value)
    }
  }
  return `${datasetPath(datasetId)}/images?${params}`
}

/** 图片列表的一页（按固定顺序，`offset` 起的 {@link IMAGE_LIST_PAGE_SIZE} 张）。 */
export function imagesPageQuery(
  datasetId: number,
  filters: ImageFilters,
  offset: number
) {
  return queryOptions({
    queryKey: annotationKeys.page(datasetId, filters, offset),
    queryFn: ({ signal }) =>
      apiGet<ListResponse<DatasetImage>>(
        listPath(datasetId, filters, offset, IMAGE_LIST_PAGE_SIZE),
        { signal }
      ),
    placeholderData: keepPreviousData,
  })
}

/** 满足筛选条件的第 `index` 张图片（从 0 开始），没有时为 null。 */
export async function fetchImageAt(
  datasetId: number,
  filters: ImageFilters,
  index: number
) {
  const list = await apiGet<ListResponse<DatasetImage>>(
    listPath(datasetId, filters, index, 1)
  )
  return list.items[0] ?? null
}

/** 编辑会话的上下文：保存到哪张图片，以及没有框时如何保存。 */
export type SaveContext = {
  datasetId: number
  imageId: number
  /** 开始编辑时已有标注文件。 */
  hadFile: boolean
  /** 用户把这张图片标为了无目标（负样本）。 */
  negative: boolean
}

/**
 * 保存编辑器中的框。没有框时：图片原本有标注文件，或用户标为了无目标，则
 * 写入空标注（负样本）；否则删除本次编辑中创建的文件，图片恢复为“未标注”，
 * 画了又删掉的框不会把图片误标为负样本。返回保存后的详情；没有任何写入时为 null。
 * `init` 用于关闭页面时补发的 `keepalive` 请求（与平时的保存走同一套规则）。
 */
export async function persistAnnotation(
  objects: AnnotationObject[],
  baseVersion: string,
  force: boolean,
  context: SaveContext,
  init?: RequestInit
): Promise<DatasetImageDetail | null> {
  const { datasetId, imageId } = context
  if (objects.length === 0 && !context.hadFile && !context.negative) {
    if (baseVersion === "" && !force) {
      return null // 磁盘上没有文件，也不需要有
    }
    return deleteAnnotation(datasetId, imageId, baseVersion, force, init)
  }
  return saveAnnotation(
    datasetId,
    imageId,
    { objects, base_version: baseVersion, force },
    init
  )
}

/**
 * 这张图片最后保存的详情：保存成功后由 {@link applySavedDetail} 写入缓存
 * （重新读取时也会更新）；缓存中没有时为 `loaded`（编辑器载入时的详情）。
 */
export function savedDetail(
  queryClient: QueryClient,
  datasetId: number,
  loaded: DatasetImageDetail
) {
  return (
    queryClient.getQueryData<DatasetImageDetail>(
      annotationKeys.image(datasetId, loaded.id)
    ) ?? loaded
  )
}

function labelKey(detail: DatasetImageDetail | undefined) {
  if (!detail) {
    return ""
  }
  return JSON.stringify([
    detail.annotation_state,
    detail.annotation?.labels ?? [],
  ])
}

/**
 * 保存后更新缓存：编辑器与查看大图的图片详情直接替换；标注状态或标签变了
 * 时（数据集计数、图片列表的状态、标签分布随之变化）让数据集的查询失效。
 */
export function applySavedDetail(
  queryClient: QueryClient,
  datasetId: number,
  detail: DatasetImageDetail
) {
  const editorKey = annotationKeys.image(datasetId, detail.id)
  const previous = queryClient.getQueryData<DatasetImageDetail>(editorKey)
  queryClient.setQueryData(editorKey, detail)
  queryClient.setQueryData(datasetKeys.image(datasetId, detail.id), detail)
  if (labelKey(previous) !== labelKey(detail)) {
    void queryClient.invalidateQueries({
      queryKey: datasetKeys.detail(datasetId),
    })
    void queryClient.invalidateQueries({ queryKey: datasetKeys.lists() })
    void queryClient.invalidateQueries({ queryKey: projectKeys.all })
  }
}
