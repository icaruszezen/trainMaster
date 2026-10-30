/**
 * 数据集模块的 API 类型、查询与写操作。
 *
 * 这里的类型就是与后端 `internal/httpapi/datasets.go` 的契约（字段名、取值），
 * 修改时两边同步，并更新 docs/architecture.md 的“数据集接口”。
 */
import {
  infiniteQueryOptions,
  keepPreviousData,
  queryOptions,
} from "@tanstack/react-query"

import { apiGet, apiSend, type ListResponse } from "@/lib/api"

export type DatasetOrigin = "upload" | "directory"

/** 图片的引用；`version` 随文件变化而变化，用于缩略图 / 原图的缓存失效。 */
export type DatasetImageRef = { id: number; version: string }

export type Dataset = {
  id: number
  name: string
  description: string
  /** `datasets/` 下的文件夹名。 */
  dir_name: string
  /** 服务器（容器）内的绝对路径，如 `/data/datasets/划痕-A`。 */
  path: string
  origin: DatasetOrigin
  /** 文件夹已不在磁盘上。 */
  directory_missing: boolean
  image_count: number
  /** 标注文件中至少有一个目标的图片数。 */
  annotated_count: number
  /** 有标注文件但没有目标的图片数（负样本）。 */
  empty_count: number
  /**
   * 有同名 xml 但无法按 VOC 解析的图片数。其余
   * `image_count - annotated_count - empty_count - invalid_count` 张没有标注文件。
   */
  invalid_count: number
  object_count: number
  label_count: number
  /** 图片文件的总字节数。 */
  total_bytes: number
  /** 最多 4 张，用于卡片封面。 */
  covers: DatasetImageRef[]
  /** 最近一个上传中 / 排队中 / 执行中的导入任务。 */
  active_import: DatasetImport | null
  scanned_at: string | null
  created_at: string
  updated_at: string
}

export type ImageAnnotation = {
  /** 标注文件相对于数据集文件夹的路径。 */
  path: string
  object_count: number
  /** 按数量降序、名称升序。 */
  labels: { name: string; count: number }[]
}

/**
 * 图片的标注状态：没有同名 xml / xml 无法解析 / 空标注（负样本）/ 至少有一个目标。
 */
export type AnnotationState = "none" | "invalid" | "empty" | "annotated"

export type DatasetImage = {
  id: number
  dataset_id: number
  /** 相对于数据集文件夹的路径，`/` 分隔。 */
  path: string
  file_name: string
  size_bytes: number
  width: number | null
  height: number | null
  mod_time: string
  version: string
  annotation_state: AnnotationState
  /** 配对的 xml 相对于数据集文件夹的路径；`none` 时为 null。 */
  annotation_path: string | null
  /** `none` 与 `invalid` 时为 null。 */
  annotation: ImageAnnotation | null
}

export type AnnotationObject = {
  name: string
  xmin: number
  ymin: number
  xmax: number
  ymax: number
  difficult: boolean
  truncated: boolean
}

/**
 * 单张图片的详情。`annotation`、`annotation_state`、`objects` 来自此刻对磁盘上
 * xml 的解析（索引可能更旧），`annotation_path` 为索引中的配对。
 */
export type DatasetImageDetail = DatasetImage & {
  /** 没有标注或无法解析时为 `[]`。 */
  objects: AnnotationObject[]
  /**
   * 无法读取标注时的中文说明（xml 无法解析、已不存在等），否则为 `""`。
   */
  annotation_error: string
}

export type DatasetLabel = {
  name: string
  object_count: number
  image_count: number
}

export type ImportSource = "archive" | "folder" | "scan"
export type ImportStatus =
  "uploading" | "queued" | "running" | "succeeded" | "failed" | "canceled"
export type ImportStage = "" | "extracting" | "importing" | "scanning"
export type ProgressUnit = "" | "bytes" | "files" | "entries"
export type ConflictPolicy = "skip" | "overwrite"

export type ImportSummaryKey =
  | "images_added"
  | "images_replaced"
  | "images_skipped"
  | "images_updated"
  | "images_removed"
  | "images_unchanged"
  | "images_total"
  | "annotations_matched"
  | "annotations_invalid"
  | "annotations_orphaned"
  | "files_ignored"
  | "entries_unsafe"
  | "names_invalid"

export type DatasetImport = {
  id: number
  dataset_id: number
  source: ImportSource
  status: ImportStatus
  on_conflict: ConflictPolicy
  /** 压缩包文件名 / 上传的文件夹名。 */
  file_name: string
  /** 声明的上传总字节数。 */
  file_size: number
  /** 声明的文件数（文件夹上传）。 */
  file_count: number
  received_bytes: number
  received_files: number
  stage: ImportStage
  progress: { done: number; total: number; unit: ProgressUnit }
  summary: Partial<Record<ImportSummaryKey, number>>
  /** 失败原因（中文），否则为 `""`。 */
  error: string
  created_at: string
  updated_at: string
  started_at: string | null
  finished_at: string | null
}

export type DatasetDirectory = {
  /** `datasets/` 下的文件夹名。 */
  name: string
  path: string
  registered: boolean
  dataset_id: number | null
  image_count: number
  /** `.xml` 文件数。 */
  annotation_count: number
  total_bytes: number
  /** 文件太多时只统计了一部分，数量为估算值。 */
  approximate: boolean
  modified_at: string
}

/** `unannotated` 只含没有 xml 的图片；xml 无法解析的是 `invalid`。 */
export type ImageStatusFilter =
  "all" | "annotated" | "empty" | "unannotated" | "invalid"
export type ImageSort = "path" | "-mod_time" | "-objects"

export type ImageFilters = {
  status: ImageStatusFilter
  /** 精确匹配的标签名，`""` 表示不限。 */
  label: string
  /** 路径包含（不区分大小写）。 */
  q: string
  sort: ImageSort
}

const BASE = "/api/v1/datasets"

export const datasetPath = (id: number) => `${BASE}/${id}`
export const importPath = (datasetId: number, importId: number) =>
  `${datasetPath(datasetId)}/imports/${importId}`

function withQuery(path: string, params: Record<string, string | number>) {
  const search = new URLSearchParams()
  for (const [key, value] of Object.entries(params)) {
    if (value !== "") {
      search.set(key, String(value))
    }
  }
  const query = search.toString()
  return query ? `${path}?${query}` : path
}

/** 缩略图地址（JPEG）：`sm` 适合网格（320px），`lg` 适合查看大图（1600px）。 */
export function thumbnailUrl(
  datasetId: number,
  image: DatasetImageRef,
  size: "sm" | "lg"
) {
  return withQuery(`${datasetPath(datasetId)}/images/${image.id}/thumbnail`, {
    size,
    v: image.version,
  })
}

/** 原图地址。 */
export function fileUrl(datasetId: number, image: DatasetImageRef) {
  return withQuery(`${datasetPath(datasetId)}/images/${image.id}/file`, {
    v: image.version,
  })
}

/**
 * 查询键。`detail(id)` 是该数据集下所有查询（图片、标签、导入记录）的前缀，
 * 导入完成后整体失效即可。
 */
export const datasetKeys = {
  all: ["datasets"] as const,
  lists: () => [...datasetKeys.all, "list"] as const,
  list: (q: string) => [...datasetKeys.lists(), { q }] as const,
  directories: () => [...datasetKeys.all, "directories"] as const,
  detail: (id: number) => [...datasetKeys.all, "detail", id] as const,
  images: (id: number, filters: ImageFilters) =>
    [...datasetKeys.detail(id), "images", filters] as const,
  image: (id: number, imageId: number) =>
    [...datasetKeys.detail(id), "image", imageId] as const,
  labels: (id: number) => [...datasetKeys.detail(id), "labels"] as const,
  imports: (id: number) => [...datasetKeys.detail(id), "imports"] as const,
  import: (id: number, importId: number) =>
    [...datasetKeys.detail(id), "import", importId] as const,
}

const ACTIVE_STATUSES: ReadonlySet<ImportStatus> = new Set([
  "uploading",
  "queued",
  "running",
])

export function isActiveImport(job: Pick<DatasetImport, "status">) {
  return ACTIVE_STATUSES.has(job.status)
}

/** 数据集列表；有导入正在进行时每 3 秒刷新一次，以更新卡片上的进度。 */
export function datasetsQuery(q = "") {
  return queryOptions({
    queryKey: datasetKeys.list(q),
    queryFn: ({ signal }) =>
      apiGet<ListResponse<Dataset>>(withQuery(BASE, { q }), { signal }),
    refetchInterval: (query) =>
      query.state.data?.items.some((dataset) => dataset.active_import)
        ? 3_000
        : false,
  })
}

export function datasetQuery(id: number) {
  return queryOptions({
    queryKey: datasetKeys.detail(id),
    queryFn: ({ signal }) => apiGet<Dataset>(datasetPath(id), { signal }),
  })
}

export function directoriesQuery() {
  return queryOptions({
    queryKey: datasetKeys.directories(),
    queryFn: ({ signal }) =>
      apiGet<ListResponse<DatasetDirectory>>(`${BASE}/directories`, {
        signal,
      }),
    staleTime: 0,
  })
}

export const IMAGES_PAGE_SIZE = 60

export function imagesQuery(id: number, filters: ImageFilters) {
  return infiniteQueryOptions({
    queryKey: datasetKeys.images(id, filters),
    queryFn: ({ signal, pageParam }) =>
      apiGet<ListResponse<DatasetImage>>(
        withQuery(`${datasetPath(id)}/images`, {
          offset: pageParam,
          limit: IMAGES_PAGE_SIZE,
          status: filters.status === "all" ? "" : filters.status,
          label: filters.label,
          q: filters.q,
          sort: filters.sort === "path" ? "" : filters.sort,
        }),
        { signal }
      ),
    initialPageParam: 0,
    getNextPageParam: (lastPage, pages) => {
      const loaded = pages.reduce((sum, page) => sum + page.items.length, 0)
      return lastPage.items.length > 0 && loaded < lastPage.total
        ? loaded
        : undefined
    },
    // 切换筛选条件时保留上一次的结果，避免网格闪成骨架屏。
    placeholderData: keepPreviousData,
  })
}

export function imageDetailQuery(id: number, imageId: number) {
  return queryOptions({
    queryKey: datasetKeys.image(id, imageId),
    queryFn: ({ signal }) =>
      apiGet<DatasetImageDetail>(`${datasetPath(id)}/images/${imageId}`, {
        signal,
      }),
  })
}

export function labelsQuery(id: number) {
  return queryOptions({
    queryKey: datasetKeys.labels(id),
    queryFn: ({ signal }) =>
      apiGet<ListResponse<DatasetLabel>>(`${datasetPath(id)}/labels`, {
        signal,
      }),
  })
}

export const IMPORTS_PAGE_SIZE = 20

export function importsQuery(id: number) {
  return queryOptions({
    queryKey: datasetKeys.imports(id),
    queryFn: ({ signal }) =>
      apiGet<ListResponse<DatasetImport>>(
        withQuery(`${datasetPath(id)}/imports`, { limit: IMPORTS_PAGE_SIZE }),
        { signal }
      ),
    // 有任务进行中时定期刷新，让状态列跟上进度。
    refetchInterval: (query) =>
      query.state.data?.items.some(isActiveImport) ? 3_000 : false,
  })
}

/** 单个导入任务；进行中时每 1.5 秒轮询，结束后停止。 */
export function importQuery(datasetId: number, importId: number) {
  return queryOptions({
    queryKey: datasetKeys.import(datasetId, importId),
    queryFn: ({ signal }) =>
      apiGet<DatasetImport>(importPath(datasetId, importId), { signal }),
    refetchInterval: (query) => {
      const job = query.state.data
      return !job || isActiveImport(job) ? 1_500 : false
    },
    staleTime: 0,
  })
}

export type CreateDatasetInput = {
  name: string
  description?: string
  /** 把 `datasets/` 下已有的这个文件夹添加为数据集。 */
  directory?: string
}

export function createDataset(input: CreateDatasetInput) {
  return apiSend<Dataset>("POST", BASE, input)
}

export function updateDataset(
  id: number,
  input: { name?: string; description?: string }
) {
  return apiSend<Dataset>("PATCH", datasetPath(id), input)
}

/**
 * `?delete_files=true` 时记录已删除、但文件夹没能完全删除的结果（200）；
 * 全部删除时为 204，`deleteDataset` 返回 undefined。
 */
export type DeleteDatasetResult = { files_removed: boolean; message: string }

export function deleteDataset(id: number, deleteFiles: boolean) {
  return apiSend<DeleteDatasetResult | undefined>(
    "DELETE",
    deleteFiles ? `${datasetPath(id)}?delete_files=true` : datasetPath(id)
  )
}

export function scanDataset(id: number) {
  return apiSend<DatasetImport>("POST", `${datasetPath(id)}/scan`)
}

export type CreateImportInput = {
  source: "archive" | "folder"
  file_name: string
  file_size: number
  file_count?: number
  on_conflict?: ConflictPolicy
}

export function createImport(datasetId: number, input: CreateImportInput) {
  return apiSend<DatasetImport>(
    "POST",
    `${datasetPath(datasetId)}/imports`,
    input
  )
}

export function commitImport(datasetId: number, importId: number) {
  return apiSend<DatasetImport>(
    "POST",
    `${importPath(datasetId, importId)}/commit`
  )
}

export function cancelImport(datasetId: number, importId: number) {
  return apiSend<DatasetImport>(
    "POST",
    `${importPath(datasetId, importId)}/cancel`
  )
}
