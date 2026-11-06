/**
 * 模型审图的 API 类型、查询与写操作。
 *
 * 这里的类型就是与后端 `internal/review`（`types.go`）、`internal/httpapi/reviews.go`
 * 的契约（字段名、取值），修改时两边同步，并更新 docs/architecture.md 的
 * “审图接口”。约定：未知的数值为 `null`，未知的字符串为 `""`，数组与对象字段
 * 永远存在。
 *
 * 审图编辑器只通过 {@link ReviewSource}（接口前缀与查询键）读写一个审图任务的
 * 图片：平台上是 `/api/v1/reviews/<id>`（{@link reviewSource}），本地审图程序是
 * `/api/review`，两边的接口形状相同（见 docs/architecture.md 第 11 节“本地审图
 * 程序”）。
 */
import { keepPreviousData, queryOptions } from "@tanstack/react-query"

import {
  datasetKeys,
  datasetPath,
  type AnnotationObject,
} from "@/features/datasets/api"
import {
  apiGet,
  apiSend,
  apiUpload,
  isPermanentError,
  type ListResponse,
} from "@/lib/api"
import { queryClient } from "@/lib/query-client"

// ---- 类型 ----

/** 审图任务的状态：排队 → 识别中 → 可以审图；识别失败时可以重试（继续识别剩下的图片）。 */
export type ReviewStatus = "queued" | "running" | "ready" | "failed"

/** 识别中的阶段（训练节点上报）。 */
export type ReviewStage = "" | "downloading" | "inferring"

/** 图片的审核状态：待审核（默认）/ 已审核（识别结果正确）/ 需加样（参与后续的训练）。 */
export type ReviewImageStatus = "pending" | "approved" | "needs_sample"

/** 识别参数。`imgsz` 为 [高, 宽]。 */
export type ReviewOptions = {
  conf: number
  iou: number
  imgsz: [number, number]
  max_det: number
}

/** 创建审图任务时模型的快照（模型之后可能被删除）。 */
export type ReviewModelRef = {
  id: number
  project_id: number
  project_name: string
  version: number
  yolo_version: string
  task: string
  file_name: string
  class_names: string[]
}

/** 审图任务（或其中一个数据集）的图片数，读取时统计。 */
export type ReviewCounts = {
  total: number
  /** 已识别（包括识别失败的）。 */
  inferred: number
  inference_failed: number
  /** 待审核（包括还没有识别的）。 */
  pending: number
  approved: number
  needs_sample: number
  /** 审图标注与模型的识别结果不同。 */
  edited: number
  /** 合并过到数据集（进了某个审图版本）。 */
  merged: number
}

/** 审图任务中的一个数据集：在任务文件夹与审图包中的文件夹名，以及它的图片数。 */
export type ReviewDataset = {
  id: number
  /** 当前的名称（数据集已删除时为创建任务时的名称）。 */
  name: string
  folder: string
  counts: ReviewCounts
  /**
   * 下一次合并会给这个数据集生成的审图版本名，如“审图1.2”（预计：别的审图任务
   * 先合并完成时会用掉这个编号；数据集已删除或在本地审图程序中为空）。
   */
  next_version: string
}

export type ReviewNode = { id: number; name: string }

/** 上传审图结果或合并时没有处理的一张图片。 */
export type ReviewProblem<K extends string> = {
  path: string
  kind: K
  message: string
}

export type ReviewUploadSummary = {
  images: number
  applied: number
  status_changes: number
  annotation_changes: number
  unchanged: number
  conflicts: number
  invalid: number
  unknown: number
}

/** 从本地审图程序上传的审图结果。 */
export type ReviewUpload = {
  id: number
  review_id: number
  file_name: string
  overwrite: boolean
  summary: ReviewUploadSummary
  problems: ReviewProblem<"conflict" | "invalid" | "unknown">[]
  created_at: string
}

export type ReviewMergeStatus = "running" | "succeeded" | "failed"

export type ReviewMergeSummary = {
  merged: number
  /** 旧的合并（直接写进数据集）：数据集中已有标注而跳过的图片。 */
  skipped_existing: number
  /** 识别失败、也没有修改过，没有审图标注的图片。 */
  skipped_empty: number
  /** 合并进行时审核状态被改为其它状态的图片。 */
  skipped_changed: number
  /** 旧的合并：数据集的标注同时被其他人修改的图片。 */
  conflicts: number
  failed: number
}

/** 合并生成（或正在生成）的一个审图版本。 */
export type ReviewMergeVersion = {
  id: number
  dataset_id: number
  /** 如“审图1.0”；正在生成时为空。 */
  name: string
  status: "building" | "ready"
  image_count: number
}

/**
 * 把审图标注合并到数据集（后台任务）：给每个数据集生成一个审图版本，不修改
 * 数据集的标注文件。
 */
export type ReviewMerge = {
  id: number
  review_id: number
  dataset_ids: number[]
  statuses: ReviewImageStatus[]
  /** 旧的合并（直接写进数据集的标注文件）对已有标注的处理；生成版本的合并为 null。 */
  on_existing: "skip" | "overwrite" | null
  /** 生成的版本的备注（旧的合并为空）。 */
  note: string
  /** 生成的版本，按数据集 ID；删除的版本不再列出。 */
  versions: ReviewMergeVersion[]
  status: ReviewMergeStatus
  total: number
  done: number
  summary: ReviewMergeSummary
  problems: ReviewProblem<"conflict" | "failed" | "skipped">[]
  error: string
  created_at: string
  finished_at: string | null
}

export type Review = {
  id: number
  name: string
  description: string
  model: ReviewModelRef
  /** 模型被删除后为 false。 */
  model_exists: boolean
  /** 模型当前所属的项目（已删除时为 null）。 */
  project: { id: number; name: string } | null
  options: ReviewOptions
  status: ReviewStatus
  stage: ReviewStage
  attempts: number
  node: ReviewNode | null
  error: string
  error_detail: string
  /** 识别完成时训练节点的报告（设备、版本、耗时等），否则为 null。 */
  result: Record<string, unknown> | null
  /** 排队中：在排队的审图任务中的位置（从 1 开始）。 */
  queue_position: number | null
  /** 最近能执行审图识别的训练节点数（0 时界面提示没有节点可以识别）。 */
  workers_online: number
  counts: ReviewCounts
  /** 按选择的顺序。 */
  datasets: ReviewDataset[]
  last_upload: ReviewUpload | null
  active_merge: ReviewMerge | null
  last_merge: ReviewMerge | null
  created_at: string
  updated_at: string
  started_at: string | null
  finished_at: string | null
}

/**
 * 审图编辑器用到的审图任务信息（`GET {base}`）。平台返回完整的 {@link Review}；
 * 本地审图程序返回其中这些字段（状态总是 `ready`，没有可选的字段）。
 */
export type ReviewSummary = Pick<
  Review,
  | "id"
  | "name"
  | "description"
  | "model"
  | "options"
  | "status"
  | "counts"
  | "datasets"
> &
  Partial<Pick<Review, "project" | "model_exists" | "queue_position">>

export type ReviewLabelCount = { name: string; count: number }

/** 审图任务中的一张图片。 */
export type ReviewImage = {
  id: number
  review_id: number
  dataset_id: number
  /** 数据集中的图片。 */
  image_id: number
  /** 在审图任务中的顺序（从 0 开始）：数据集按选择的顺序，其中按图片顺序。 */
  position: number
  /** 创建任务时在数据集中的相对路径。 */
  path: string
  file_name: string
  /** 图片在审图包中的路径。 */
  package_path: string
  /** 审图标注（VOC）在审图任务文件夹与审图包中的路径。 */
  annotation_path: string
  width: number | null
  height: number | null
  /** 创建任务时数据集图片的版本（缩略图的缓存版本）。 */
  image_version: string
  inferred: boolean
  /** 模型没能识别这张图片的原因（`""` 为没有问题）。 */
  inference_error: string
  object_count: number
  /** 审图标注中的类别，按数量降序、名称升序。 */
  labels: ReviewLabelCount[]
  prediction_count: number
  /** 审图标注与模型的识别结果不同。 */
  edited: boolean
  status: ReviewImageStatus
  /** 每次保存标注或修改审核状态加 1。 */
  revision: number
  /** 最后一次标为已审核 / 需加样的时间，待审核时为 null。 */
  reviewed_at: string | null
  merged_at: string | null
  /** 审图标注文件的版本，`""` 为没有文件（还没有识别、识别失败且没有修改）。 */
  annotation_version: string
}

/** 模型识别出的框（不会被修改）。 */
export type ReviewPrediction = {
  name: string
  confidence: number
  xmin: number
  ymin: number
  xmax: number
  ymax: number
}

export type ReviewImageDetail = ReviewImage & {
  /** 此刻审图标注文件中的框（没有文件时为 []）。 */
  objects: AnnotationObject[]
  predictions: ReviewPrediction[]
  dataset_name: string
  /** 审图标注文件无法使用的原因（不应该发生），这时只能查看。 */
  annotation_error: string
}

/** 当前图片在符合筛选条件的图片中的位置与前后的图片（当前图片本身不一定符合）。 */
export type ReviewNeighbors = {
  prev: ReviewImage | null
  next: ReviewImage | null
  /** 排在它前面的符合条件的图片数。 */
  position: number
  matches: boolean
  total: number
}

/** 候选类别：模型所属项目的类别列表，为空时是模型的类别（`color` 为 `""`）。 */
export type ReviewClass = { name: string; color: string }

export type ReviewClassList = ListResponse<ReviewClass> & {
  /** 项目类别列表的版本；用的是模型的类别时为 `""`。 */
  version: string
}

/** 审图标注中的一个类别与它的目标数、图片数。 */
export type ReviewLabel = {
  name: string
  object_count: number
  image_count: number
}

/** 下载审图包：`url` 在 `expires_at` 前有效（1 小时）。 */
export type ReviewPackage = {
  url: string
  file_name: string
  expires_at: string
  image_count: number
}

/** 数据集的审核记录中的一个审图任务：`dataset` 是这个数据集的部分。 */
export type DatasetReview = Review & {
  dataset: ReviewDataset
  /** 包括这个数据集的合并，新的在前，最多 5 个。 */
  dataset_merges: ReviewMerge[]
}

/** 平台是否附带本地审图程序（Windows）。 */
export type ReviewTool = {
  available: boolean
  file_name: string
  size_bytes: number
  version: string
}

export type DeleteReviewResult = { files_removed: boolean; message: string }

// ---- 图片筛选条件 ----

/** 审核状态的筛选：`reviewed` 为已审核与需加样。 */
export type ReviewStatusFilter = "all" | ReviewImageStatus | "reviewed"

/** 图片列表与相邻图片的筛选条件（查询参数见 {@link reviewImageFilterParams}）。 */
export type ReviewImageFilters = {
  status: ReviewStatusFilter
  /** 只看其中一个数据集；null 为全部。 */
  datasetId: number | null
  /** 审图标注中有这个类别（名称完全相同）。 */
  label: string
  /** 路径包含（不区分大小写）。 */
  q: string
  edited: "all" | "edited" | "unedited"
  boxes: "all" | "with" | "without"
  inference: "all" | "failed"
}

export const ALL_REVIEW_IMAGES: ReviewImageFilters = {
  status: "all",
  datasetId: null,
  label: "",
  q: "",
  edited: "all",
  boxes: "all",
  inference: "all",
}

/** 筛选条件 → 查询参数，省略默认值（与地址栏中的参数名相同）。 */
export function reviewImageFilterParams(
  filters: ReviewImageFilters
): Record<string, string> {
  const params: Record<string, string> = {}
  if (filters.status !== "all") params.status = filters.status
  if (filters.datasetId !== null) params.dataset_id = String(filters.datasetId)
  if (filters.label !== "") params.label = filters.label
  if (filters.q !== "") params.q = filters.q
  if (filters.edited !== "all") params.edited = filters.edited
  if (filters.boxes !== "all") params.boxes = filters.boxes
  if (filters.inference !== "all") params.inference = filters.inference
  return params
}

// ---- 地址与查询键 ----

const BASE = "/api/v1/reviews"

export const reviewPath = (id: number) => `${BASE}/${id}`

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

/** 审图任务列表的筛选条件；`null` / `""` 表示不限。 */
export type ReviewListFilters = {
  /** 名称、说明或项目名包含（不区分大小写）。 */
  q: string
  projectId: number | null
  datasetId: number | null
  modelId: number | null
  status: ReviewStatus | null
}

export const ALL_REVIEWS: ReviewListFilters = {
  q: "",
  projectId: null,
  datasetId: null,
  modelId: null,
  status: null,
}

/**
 * 查询键。`detail(id)` 既是审图任务本身的查询，也是它下面所有查询（图片、
 * 标签、上传与合并记录）的前缀，见 {@link reviewSource}。
 */
export const reviewKeys = {
  all: ["reviews"] as const,
  lists: () => [...reviewKeys.all, "list"] as const,
  list: (filters: ReviewListFilters) =>
    [...reviewKeys.lists(), filters] as const,
  details: () => [...reviewKeys.all, "detail"] as const,
  detail: (id: number) => [...reviewKeys.details(), id] as const,
  uploads: (id: number) => [...reviewKeys.detail(id), "uploads"] as const,
  merges: (id: number) => [...reviewKeys.detail(id), "merges"] as const,
  merge: (id: number, mergeId: number) =>
    [...reviewKeys.detail(id), "merge", mergeId] as const,
  /** 包括某个数据集的审图任务（数据集页面的“审核记录”）。 */
  datasets: () => [...reviewKeys.all, "dataset"] as const,
  dataset: (datasetId: number) =>
    [...reviewKeys.datasets(), datasetId] as const,
  tool: () => [...reviewKeys.all, "tool"] as const,
}

/**
 * 审图编辑器读写的一个审图任务：`base` 是接口前缀（其下有 `/images`、
 * `/labels`、`/classes`，`GET base` 返回 {@link ReviewSummary}），`key` 是查询键
 * 的前缀（`GET base` 本身的查询键就是 `key`）。编辑器只依赖它和普通的属性，平台
 * 与本地审图程序因此共用同一个编辑器。
 */
export type ReviewSource = {
  key: readonly unknown[]
  base: string
  /** 修改（标注、审核状态）时带上的请求头：本地审图程序用它确认修改的是打开的审图包。 */
  headers?: Record<string, string>
}

/** 平台上的审图任务。查询键与 {@link reviewQuery} 相同，两者共用缓存。 */
export function reviewSource(id: number): ReviewSource {
  return { key: reviewKeys.detail(id), base: reviewPath(id) }
}

/** 一个 {@link ReviewSource} 下的查询键。 */
export const reviewSourceKeys = {
  summary: (source: ReviewSource) => source.key,
  images: (source: ReviewSource) => [...source.key, "images"] as const,
  page: (source: ReviewSource, filters: ReviewImageFilters, offset: number) =>
    [...source.key, "images", filters, offset] as const,
  neighbors: (source: ReviewSource) => [...source.key, "neighbors"] as const,
  neighbor: (
    source: ReviewSource,
    imageId: number,
    filters: ReviewImageFilters
  ) => [...source.key, "neighbors", imageId, filters] as const,
  labels: (source: ReviewSource) => [...source.key, "labels"] as const,
  classes: (source: ReviewSource) => [...source.key, "classes"] as const,
  /** 所有图片详情的前缀（见 `image`）。 */
  imageDetails: (source: ReviewSource) =>
    ["review-image", ...source.key] as const,
  /**
   * 编辑中的图片详情。不放在 `key` 下：任务的查询整体失效（如合并后刷新）时
   * 不重新获取它，编辑中的框不会被后台刷新覆盖。
   */
  image: (source: ReviewSource, imageId: number) =>
    [...reviewSourceKeys.imageDetails(source), imageId] as const,
}

type ImageRef = Pick<ReviewImage, "id" | "image_version">

/** 原图地址（数据集中的图片）。 */
export function reviewImageFileUrl(source: ReviewSource, image: ImageRef) {
  return withQuery(`${source.base}/images/${image.id}/file`, {
    v: image.image_version,
  })
}

/** 缩略图地址（JPEG）：`sm` 适合列表（320px），`lg` 适合查看大图（1600px）。 */
export function reviewThumbnailUrl(
  source: ReviewSource,
  image: ImageRef,
  size: "sm" | "lg"
) {
  return withQuery(`${source.base}/images/${image.id}/thumbnail`, {
    size,
    v: image.image_version,
  })
}

// ---- 编辑器的查询（按 ReviewSource） ----

/** 审图任务的信息（名称、模型、各状态的图片数等）。 */
export function reviewSummaryQuery(source: ReviewSource) {
  return queryOptions({
    queryKey: reviewSourceKeys.summary(source),
    queryFn: ({ signal }) => apiGet<ReviewSummary>(source.base, { signal }),
  })
}

export const REVIEW_IMAGE_PAGE_SIZE = 50

function imagesPath(
  source: ReviewSource,
  filters: ReviewImageFilters,
  offset: number,
  limit: number
) {
  return withQuery(`${source.base}/images`, {
    offset,
    limit,
    ...reviewImageFilterParams(filters),
  })
}

/** 图片列表的一页（按审图任务中的顺序，`offset` 起的 {@link REVIEW_IMAGE_PAGE_SIZE} 张）。 */
export function reviewImagesPageQuery(
  source: ReviewSource,
  filters: ReviewImageFilters,
  offset: number
) {
  return queryOptions({
    queryKey: reviewSourceKeys.page(source, filters, offset),
    queryFn: ({ signal }) =>
      apiGet<ListResponse<ReviewImage>>(
        imagesPath(source, filters, offset, REVIEW_IMAGE_PAGE_SIZE),
        { signal }
      ),
    placeholderData: keepPreviousData,
  })
}

/** 符合筛选条件的第 `index` 张图片（从 0 开始），没有时为 null。 */
export async function fetchReviewImageAt(
  source: ReviewSource,
  filters: ReviewImageFilters,
  index: number
) {
  const list = await apiGet<ListResponse<ReviewImage>>(
    imagesPath(source, filters, index, 1)
  )
  return list.items[0] ?? null
}

/**
 * 编辑器中的图片详情（审图标注的框、模型识别的框）。与标注工作台一样：30 秒内
 * 重新打开同一张图片不再请求（含预加载的前后图片）；之后打开时重新读取，读取
 * 完成后才载入（见 annotation/api 的 openableDetail）；编辑期间不在后台刷新。
 */
export function reviewImageQuery(source: ReviewSource, imageId: number) {
  return queryOptions({
    queryKey: reviewSourceKeys.image(source, imageId),
    queryFn: ({ signal }) =>
      apiGet<ReviewImageDetail>(`${source.base}/images/${imageId}`, {
        signal,
      }),
    staleTime: 30_000,
    refetchOnWindowFocus: false,
    refetchOnReconnect: false,
  })
}

/** 当前图片在符合筛选条件的图片中的位置与前后的图片。 */
export function reviewNeighborsQuery(
  source: ReviewSource,
  imageId: number,
  filters: ReviewImageFilters
) {
  return queryOptions({
    queryKey: reviewSourceKeys.neighbor(source, imageId, filters),
    queryFn: ({ signal }) =>
      apiGet<ReviewNeighbors>(
        withQuery(
          `${source.base}/images/${imageId}/neighbors`,
          reviewImageFilterParams(filters)
        ),
        { signal }
      ),
    placeholderData: keepPreviousData,
  })
}

/** 审图标注中的类别（按目标数降序）：候选类别与按类别筛选。 */
export function reviewLabelsQuery(source: ReviewSource) {
  return queryOptions({
    queryKey: reviewSourceKeys.labels(source),
    queryFn: ({ signal }) =>
      apiGet<ListResponse<ReviewLabel>>(`${source.base}/labels`, { signal }),
  })
}

/** 候选类别（模型所属项目的类别列表，没有时是模型的类别）。 */
export function reviewClassesQuery(source: ReviewSource) {
  return queryOptions({
    queryKey: reviewSourceKeys.classes(source),
    queryFn: ({ signal }) =>
      apiGet<ReviewClassList>(`${source.base}/classes`, { signal }),
  })
}

export type SaveReviewAnnotationInput = {
  objects: AnnotationObject[]
  /** 开始编辑时审图标注文件的版本（没有文件时为 `""`）。 */
  base_version: string
  /** 不检查版本，直接覆盖（冲突时用户选择“用我的版本覆盖”）。 */
  force?: boolean
}

/**
 * 保存审图标注（整个 VOC 文件按框重新生成，只写在审图任务中，不改数据集），
 * 返回保存后的详情。文件在 `base_version` 之后被修改过时 409
 * `annotation_conflict`；图片还没有识别时 409 `conflict`。`init` 用于关闭页面时
 * 的 `keepalive` 请求。
 */
export function saveReviewAnnotation(
  source: ReviewSource,
  imageId: number,
  input: SaveReviewAnnotationInput,
  init?: RequestInit
) {
  return apiSend<ReviewImageDetail>(
    "PUT",
    `${source.base}/images/${imageId}/annotation`,
    input,
    withSourceHeaders(source, init)
  )
}

/** 修改图片的审核状态，返回修改后的详情；图片还没有识别时 409。 */
export function setReviewImageStatus(
  source: ReviewSource,
  imageId: number,
  status: ReviewImageStatus
) {
  return apiSend<ReviewImageDetail>(
    "PUT",
    `${source.base}/images/${imageId}/status`,
    { status },
    withSourceHeaders(source)
  )
}

function withSourceHeaders(
  source: ReviewSource,
  init?: RequestInit
): RequestInit | undefined {
  if (!source.headers) {
    return init
  }
  const headers = new Headers(init?.headers)
  for (const [name, value] of Object.entries(source.headers)) {
    headers.set(name, value)
  }
  return { ...init, headers }
}

// ---- 平台：审图任务 ----

/** 排队、识别中或正在合并：页面需要轮询。 */
export function isBusyReview(
  review: Pick<Review, "status" | "active_merge">
): boolean {
  return (
    review.status === "queued" ||
    review.status === "running" ||
    review.active_merge !== null
  )
}

/** 审图任务列表，新的在前；有任务在识别或合并时每 3 秒刷新。 */
export function reviewsQuery(filters: ReviewListFilters = ALL_REVIEWS) {
  return queryOptions({
    queryKey: reviewKeys.list(filters),
    queryFn: ({ signal }) =>
      apiGet<ListResponse<Review>>(
        withQuery(BASE, {
          q: filters.q,
          project_id: filters.projectId ?? "",
          dataset_id: filters.datasetId ?? "",
          model_id: filters.modelId ?? "",
          status: filters.status ?? "",
        }),
        { signal }
      ),
    refetchInterval: (query) =>
      query.state.data?.items.some(isBusyReview) ? 3_000 : false,
  })
}

/**
 * 一个审图任务；排队、识别中或正在合并时每 2 秒刷新（识别进度、图片数）。
 * 任务被删除（404）等再请求也不会变的错误后停止。与编辑器的
 * {@link reviewSummaryQuery}（{@link reviewSource}）共用缓存。
 */
export function reviewQuery(id: number) {
  return queryOptions({
    queryKey: reviewKeys.detail(id),
    queryFn: ({ signal }) => apiGet<Review>(reviewPath(id), { signal }),
    refetchInterval: (query) => {
      const review = query.state.data
      return !isPermanentError(query.state.error) &&
        review &&
        isBusyReview(review)
        ? 2_000
        : false
    },
  })
}

/** 上传审图结果的记录，新的在前（最多 50 个）。 */
export function reviewUploadsQuery(id: number) {
  return queryOptions({
    queryKey: reviewKeys.uploads(id),
    queryFn: ({ signal }) =>
      apiGet<ListResponse<ReviewUpload>>(`${reviewPath(id)}/uploads`, {
        signal,
      }),
  })
}

/** 合并记录，新的在前（最多 50 个）；有合并在进行时每 2 秒刷新。 */
export function reviewMergesQuery(id: number) {
  return queryOptions({
    queryKey: reviewKeys.merges(id),
    queryFn: ({ signal }) =>
      apiGet<ListResponse<ReviewMerge>>(`${reviewPath(id)}/merges`, {
        signal,
      }),
    refetchInterval: (query) =>
      !isPermanentError(query.state.error) &&
      query.state.data?.items.some((merge) => merge.status === "running")
        ? 2_000
        : false,
  })
}

/** 单个合并；进行中时每秒轮询，结束或被删除（404）后停止。 */
export function reviewMergeQuery(id: number, mergeId: number) {
  return queryOptions({
    queryKey: reviewKeys.merge(id, mergeId),
    queryFn: ({ signal }) =>
      apiGet<ReviewMerge>(`${reviewPath(id)}/merges/${mergeId}`, { signal }),
    refetchInterval: (query) => {
      if (isPermanentError(query.state.error)) {
        return false
      }
      const merge = query.state.data
      return !merge || merge.status === "running" ? 1_000 : false
    },
    staleTime: 0,
  })
}

/**
 * 包括这个数据集的审图任务（数据集页面的“审核记录”），新的在前；有任务在识别
 * 或合并时每 3 秒刷新。数据集不存在时 404。
 */
export function datasetReviewsQuery(datasetId: number) {
  return queryOptions({
    queryKey: reviewKeys.dataset(datasetId),
    queryFn: ({ signal }) =>
      apiGet<ListResponse<DatasetReview>>(`${datasetPath(datasetId)}/reviews`, {
        signal,
      }),
    refetchInterval: (query) =>
      !isPermanentError(query.state.error) &&
      query.state.data?.items.some(isBusyReview)
        ? 3_000
        : false,
  })
}

/** 平台是否附带本地审图程序（随平台一起构建，部署后不会变）。 */
export function reviewToolQuery() {
  return queryOptions({
    queryKey: reviewKeys.tool(),
    queryFn: ({ signal }) => apiGet<ReviewTool>(`${BASE}/tool`, { signal }),
    staleTime: 5 * 60_000,
  })
}

/** 下载本地审图程序（`trainmaster-reviewer.exe`）。 */
export const REVIEW_TOOL_DOWNLOAD_URL = `${BASE}/tool/download`

export type CreateReviewInput = {
  /** 省略或为空时为“<项目名> v<版本> 审图”。最多 64 个字符。 */
  name?: string
  description?: string
  model_id: number
  /** 1–50 个，按选择的顺序。 */
  dataset_ids: number[]
  /** 省略的选项用默认值：conf 0.25、iou 0.7、imgsz 训练尺寸、max_det 300。 */
  options?: {
    conf?: number
    iou?: number
    /** 单个整数表示正方形，或 [高, 宽]。 */
    imgsz?: number | [number, number]
    max_det?: number
  }
}

/**
 * 创建审图任务（排队等训练节点识别）。模型不存在、没有检测框（分类模型），
 * 数据集不存在或没有图片时 422；模型文件缺失时 409。
 */
export function createReview(input: CreateReviewInput) {
  return apiSend<Review>("POST", BASE, input)
}

export function updateReview(
  id: number,
  input: { name?: string; description?: string }
) {
  return apiSend<Review>("PATCH", reviewPath(id), input)
}

/**
 * 删除审图任务与它的文件夹（审图标注），数据集不受影响。正在合并时 409；
 * 文件夹没能完全删除时返回 {@link DeleteReviewResult}。
 */
export function deleteReview(id: number) {
  return apiSend<DeleteReviewResult | undefined>("DELETE", reviewPath(id))
}

/** 重新识别失败的审图任务（继续识别还没有识别的图片）；不是失败状态时 409。 */
export function retryReview(id: number) {
  return apiSend<Review>("POST", `${reviewPath(id)}/retry`)
}

export type CreateReviewPackageInput = {
  /** 只包含这些审核状态的图片；省略为全部。 */
  statuses?: ReviewImageStatus[]
  /** 只包含这些数据集的图片；省略为全部。 */
  dataset_ids?: number[]
}

/**
 * 准备下载审图包（图片、审图标注与 review.json 的 zip），返回下载地址（1 小时
 * 内有效，用 `@/lib/download` 的 startDownload 下载）。还没有识别完成的图片、
 * 没有符合条件的图片时 409。
 */
export function createReviewPackage(
  id: number,
  input: CreateReviewPackageInput = {}
) {
  return apiSend<ReviewPackage>("POST", `${reviewPath(id)}/packages`, input)
}

/**
 * 上传本地审图程序导出的审图结果（zip，也接受重新压缩的审图包）。`overwrite`
 * 时平台上在下载审图包之后被修改过的图片也用上传的结果覆盖，否则记为冲突。
 * 不是这个任务的审图结果时 422。
 */
export function uploadReviewResults(
  id: number,
  file: Blob,
  {
    fileName,
    overwrite = false,
    signal,
    onProgress,
  }: {
    fileName: string
    overwrite?: boolean
    signal?: AbortSignal
    onProgress?: (loaded: number, total: number) => void
  }
) {
  return apiUpload<ReviewUpload>(
    "POST",
    withQuery(`${reviewPath(id)}/results`, {
      overwrite: String(overwrite),
      file_name: fileName,
    }),
    file,
    { signal, onProgress, contentType: "application/zip" }
  )
}

export type CreateReviewMergeInput = {
  /** 合并到这些数据集；省略为任务中的全部数据集。 */
  dataset_ids?: number[]
  /** 合并这些审核状态的图片（至少一个，通常是需加样）。 */
  statuses: ReviewImageStatus[]
  /** 生成的审图版本的备注（必填，最多 500 个字符）。 */
  note: string
}

/**
 * 开始把审图标注合并到数据集（后台任务，用 {@link reviewMergeQuery} 查看进度）：
 * 每个数据集生成一个新的审图版本。已有合并在进行、还没有识别完成的图片时 409。
 */
export function createReviewMerge(id: number, input: CreateReviewMergeInput) {
  return apiSend<ReviewMerge>("POST", `${reviewPath(id)}/merges`, input)
}

// ---- 刷新 ----

/** 审图任务增删、改名后刷新所有审图任务的查询（列表、详情、数据集的审核记录）。 */
export function refreshReviews() {
  void queryClient.invalidateQueries({ queryKey: reviewKeys.all })
}

/**
 * 上传审图结果后刷新这个审图任务的所有查询（各状态的图片数、图片列表、标签、
 * 上传记录）与列表、数据集的审核记录。图片详情只标记为过期（不在后台重新获取），
 * 下次打开图片时读取最新的框。
 */
export function refreshReviewContent(reviewId: number) {
  void queryClient.invalidateQueries({ queryKey: reviewKeys.detail(reviewId) })
  void queryClient.invalidateQueries({
    queryKey: reviewSourceKeys.imageDetails(reviewSource(reviewId)),
    refetchType: "none",
  })
  void queryClient.invalidateQueries({ queryKey: reviewKeys.lists() })
  void queryClient.invalidateQueries({ queryKey: reviewKeys.datasets() })
}

/**
 * 开始合并后（或开始时 409：多半已有合并在进行）刷新任务上的 `active_merge`：
 * 详情、合并记录、列表与数据集的审核记录。它们看到进行中的合并后开始轮询，
 * 合并结束时刷新被合并的数据集（见 merge-dialog 的 useFinishedMerges），关闭了
 * 合并对话框也是。
 */
export function refreshReviewMerges(reviewId: number) {
  void queryClient.invalidateQueries({
    queryKey: reviewKeys.detail(reviewId),
    exact: true,
  })
  void queryClient.invalidateQueries({ queryKey: reviewKeys.merges(reviewId) })
  void queryClient.invalidateQueries({ queryKey: reviewKeys.lists() })
  void queryClient.invalidateQueries({ queryKey: reviewKeys.datasets() })
}

/**
 * 合并结束后刷新被合并的数据集：它们多了审图版本（版本列表、版本数、下载时
 * 可选的版本都在数据集的详情查询下），以及审图任务本身（已合并的图片数、下一
 * 个版本名）。数据集的标注文件没有变。
 */
export function refreshMergedDatasets(reviewId: number, datasetIds: number[]) {
  for (const id of datasetIds) {
    void queryClient.invalidateQueries({ queryKey: datasetKeys.detail(id) })
  }
  void queryClient.invalidateQueries({ queryKey: datasetKeys.lists() })
  void queryClient.invalidateQueries({ queryKey: reviewKeys.detail(reviewId) })
  void queryClient.invalidateQueries({ queryKey: reviewKeys.lists() })
  void queryClient.invalidateQueries({ queryKey: reviewKeys.datasets() })
}
