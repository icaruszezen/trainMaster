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

import type { ReviewImageStatus } from "@/features/reviews/api"
import { apiGet, apiSend, type ListResponse } from "@/lib/api"

export type DatasetOrigin = "upload" | "directory"

/**
 * 数据集的状态（团队标记的工作进度，与图片的标注统计无关）：`""` 为未设置，
 * 其余按工作流的顺序为待标注、标注中、待审图、审图中、可训练（名称与配色见
 * `dataset-status.ts`）。
 */
export type DatasetStatus =
  | ""
  | "pending_annotation"
  | "annotating"
  | "pending_review"
  | "reviewing"
  | "trainable"

/** 图片的引用；`version` 随文件变化而变化，用于缩略图 / 原图的缓存失效。 */
export type DatasetImageRef = { id: number; version: string }

/** 数据集所属的项目（见 `features/projects`）。 */
export type DatasetProjectRef = { id: number; name: string }

export type Dataset = {
  id: number
  name: string
  description: string
  /** `datasets/` 下的文件夹名。 */
  dir_name: string
  /** 服务器（容器）内的绝对路径，如 `/data/datasets/划痕-A`。 */
  path: string
  origin: DatasetOrigin
  status: DatasetStatus
  /** 最近一次修改状态的时间；从未设置过时为 null。只改状态不更新 `updated_at`。 */
  status_updated_at: string | null
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
  /** 纳入了该数据集的项目，按名称排序。 */
  projects: DatasetProjectRef[]
  /** 审图版本数（合并审图生成，见 {@link DatasetVersion}；不含正在生成的）。 */
  version_count: number
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
  /**
   * 标注文件内容的版本（没有文件时为 `""`）。保存时作为 `base_version`
   * 提交，文件在此期间被他人修改时保存失败（409 `annotation_conflict`）。
   */
  annotation_version: string
  /**
   * 标注文件中平台无法显示的目标数（不是矩形框，如多边形；或缺少类别、坐标）。
   * 保存时这些目标原样保留。
   */
  annotation_unsupported: number
  /**
   * 不能在平台上保存这张图片标注的原因（如同名的另一张图片已经使用了这个
   * 标注文件），否则为 `""`。
   */
  annotation_blocked: string
}

/**
 * 按固定顺序（与图片列表相同）相邻的图片，在同样的筛选条件下计算。当前图片
 * 不必满足筛选条件（如刚标注完的图片不再是“未标注”），`matches` 表示它是否满足。
 */
export type ImageNeighbors = {
  prev: DatasetImage | null
  next: DatasetImage | null
  /** 排在当前图片之前、满足筛选条件的图片数（当前图片满足时即其下标）。 */
  position: number
  matches: boolean
  /** 满足筛选条件的图片总数。 */
  total: number
}

export type DatasetLabel = {
  name: string
  object_count: number
  image_count: number
}

export type ImportSource = "archive" | "folder" | "scan"
export type ImportStatus =
  "uploading" | "queued" | "running" | "succeeded" | "failed" | "canceled"
/**
 * `converting`：把 X-AnyLabeling / YOLO 标注转换为 VOC（位于解压之后、导入之前）；
 * `renaming`：检查与其它图片同名的图片并重命名（上传在转换之后、导入之前，
 * 添加已有目录在扫描之前）。
 */
export type ImportStage =
  "" | "extracting" | "converting" | "renaming" | "importing" | "scanning"
export type ProgressUnit = "" | "bytes" | "files" | "entries"
export type ConflictPolicy = "skip" | "overwrite"

/**
 * 导入 / 导出的标注格式。数据集在服务器上始终以 VOC（图片 + 同名 .xml）保存，
 * 其它格式只在导入时转换为 VOC、在下载时生成。
 */
export type AnnotationFormat = "voc" | "xanylabeling" | "yolo"

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
  | "annotations_converted"
  | "annotations_failed"
  | "objects_skipped"
  | "files_ignored"
  | "entries_unsafe"
  | "names_invalid"
  /** 与数据集中其它图片同名（不含扩展名、不区分大小写）、内容不同而重命名的图片。 */
  | "images_renamed"
  /** 与同名图片内容完全相同、保留原名的图片。 */
  | "images_identical"
  /** 添加已有目录时文件夹不允许重命名（如只读）而保留原名的图片。 */
  | "images_rename_failed"

export type DatasetImport = {
  id: number
  dataset_id: number
  source: ImportSource
  status: ImportStatus
  on_conflict: ConflictPolicy
  /** 上传内容中的标注格式；扫描任务为 `voc`。 */
  annotation_format: AnnotationFormat
  /** YOLO 的类别顺序（下标即类别编号）；其它格式为 `[]`。 */
  classes: string[]
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
  /**
   * 这个任务会重命名同名图片：上传压缩包 / 文件夹，以及添加已有目录后的
   * 第一次扫描（之后的重新扫描不会）。为 true 时结果摘要中有
   * `images_renamed` 与 `images_identical`，明细见 `importRenamesQuery`。
   */
  rename_images: boolean
}

/**
 * 导入时重命名的图片（`renamed`），或因与同名图片内容完全相同而保留原名的图片
 * （`identical`）。路径都相对于数据集文件夹。
 */
export type ImageRename = {
  kind: "renamed" | "identical"
  /** 上传内容 / 文件夹中原来的路径。 */
  original_path: string
  /** 现在在数据集中的路径；`identical` 时与 `original_path` 相同。 */
  path: string
  /** 占用这个名称的图片（按图片顺序第一张）。 */
  same_name_as: string
  /** 一并改名后的 VOC 标注文件；没有标注或 `identical` 时为 null。 */
  annotation_path: string | null
}

export type DatasetDirectory = {
  /** `datasets/` 下的文件夹名。 */
  name: string
  path: string
  registered: boolean
  dataset_id: number | null
  /**
   * 使用它的数据集在回收站中（删除时保留了文件夹）：不能再次添加，只能在
   * 回收站中恢复那个数据集；它永久删除后文件夹可以重新添加。
   */
  in_recycle_bin: boolean
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

/** 数据集列表的项目筛选：某个项目的 id、`"none"`（未纳入任何项目）或不限（null）。 */
export type DatasetProjectFilter = number | "none" | null

/** 解析地址栏 `?project=` 的取值：项目 id 或 `none`，其它值视为不筛选。 */
export function parseProjectFilter(value: string | null): DatasetProjectFilter {
  if (value === "none") {
    return "none"
  }
  if (!value || !/^[1-9]\d{0,15}$/.test(value)) {
    return null
  }
  const id = Number(value)
  return Number.isSafeInteger(id) ? id : null
}

export type ImageFilters = {
  status: ImageStatusFilter
  /** 精确匹配的标签名，`""` 表示不限。 */
  label: string
  /** 路径包含（不区分大小写）。 */
  q: string
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
  list: (q: string, project: DatasetProjectFilter) =>
    [...datasetKeys.lists(), { q, project }] as const,
  directories: () => [...datasetKeys.all, "directories"] as const,
  details: () => [...datasetKeys.all, "detail"] as const,
  detail: (id: number) => [...datasetKeys.details(), id] as const,
  images: (id: number, filters: ImageFilters) =>
    [...datasetKeys.detail(id), "images", filters] as const,
  image: (id: number, imageId: number) =>
    [...datasetKeys.detail(id), "image", imageId] as const,
  neighbors: (id: number, imageId: number, filters: ImageFilters) =>
    [...datasetKeys.image(id, imageId), "neighbors", filters] as const,
  labels: (id: number) => [...datasetKeys.detail(id), "labels"] as const,
  /** 数据集所属项目的类别（标注时的候选），见 projects/api 的 datasetClassesQuery。 */
  classes: (id: number) => [...datasetKeys.detail(id), "classes"] as const,
  imports: (id: number) => [...datasetKeys.detail(id), "imports"] as const,
  versions: (id: number) => [...datasetKeys.detail(id), "versions"] as const,
  exportPreview: (id: number, selection: VersionSelection) =>
    [...datasetKeys.detail(id), "export-preview", selection] as const,
  lastYoloImport: (id: number) =>
    [...datasetKeys.imports(id), "last-yolo"] as const,
  import: (id: number, importId: number) =>
    [...datasetKeys.detail(id), "import", importId] as const,
  importRenames: (id: number, importId: number) =>
    [...datasetKeys.import(id, importId), "renames"] as const,
}

const ACTIVE_STATUSES: ReadonlySet<ImportStatus> = new Set([
  "uploading",
  "queued",
  "running",
])

export function isActiveImport(job: Pick<DatasetImport, "status">) {
  return ACTIVE_STATUSES.has(job.status)
}

/** 列表中有导入正在进行时每 3 秒刷新一次，以更新卡片上的进度。 */
export function activeImportRefetchInterval(query: {
  state: { data?: ListResponse<Dataset> }
}) {
  return query.state.data?.items.some((dataset) => dataset.active_import)
    ? 3_000
    : false
}

/** 数据集列表，可按名称 / 描述（`q`）与所属项目筛选。 */
export function datasetsQuery(q = "", project: DatasetProjectFilter = null) {
  return queryOptions({
    queryKey: datasetKeys.list(q, project),
    queryFn: ({ signal }) =>
      apiGet<ListResponse<Dataset>>(
        withQuery(BASE, { q, project_id: project ?? "" }),
        { signal }
      ),
    refetchInterval: activeImportRefetchInterval,
  })
}

export function datasetQuery(id: number) {
  return queryOptions({
    queryKey: datasetKeys.detail(id),
    queryFn: ({ signal }) => apiGet<Dataset>(datasetPath(id), { signal }),
  })
}

/**
 * 对话框中显示的数据集：以打开时传入的数据集为初始数据的 `datasetQuery`。
 *
 * 列表页打开对话框时传入的是当时列表项的副本，之后列表刷新了它也不会变；
 * 改为读取数据集详情的查询后，refreshMembership 等刷新会反映到打开着的对话框
 * 中（详情页上就是页面本身的查询）。传入的数据可能是一段时间前读取的（例如
 * 所属项目已被别人修改），视为已过期：打开时在后台重新读取一次。
 */
export function liveDatasetQuery(dataset: Dataset) {
  return queryOptions({
    ...datasetQuery(dataset.id),
    initialData: dataset,
    initialDataUpdatedAt: 0,
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
          ...imageFilterParams(filters),
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

/** 图片筛选条件 → 查询参数（`status` 为 `all` 时省略）。 */
export function imageFilterParams(filters: ImageFilters) {
  return {
    status: filters.status === "all" ? "" : filters.status,
    label: filters.label,
    q: filters.q,
  }
}

export function imageNeighborsQuery(
  id: number,
  imageId: number,
  filters: ImageFilters
) {
  return queryOptions({
    queryKey: datasetKeys.neighbors(id, imageId, filters),
    queryFn: ({ signal }) =>
      apiGet<ImageNeighbors>(
        withQuery(
          `${datasetPath(id)}/images/${imageId}/neighbors`,
          imageFilterParams(filters)
        ),
        { signal }
      ),
    placeholderData: keepPreviousData,
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

/**
 * 最近一次成功的 YOLO 导入（不属于任何项目的数据集下载为 YOLO 时，默认沿用
 * 它的类别顺序）；没有时为 null。
 */
export function lastYoloImportQuery(id: number) {
  return queryOptions({
    queryKey: datasetKeys.lastYoloImport(id),
    queryFn: async ({ signal }) => {
      const list = await apiGet<ListResponse<DatasetImport>>(
        withQuery(`${datasetPath(id)}/imports`, {
          annotation_format: "yolo",
          status: "succeeded",
          limit: 1,
        }),
        { signal }
      )
      return list.items[0] ?? null
    },
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

export const IMPORT_RENAMES_PAGE_SIZE = 100

/** 导入任务的重命名明细（按图片顺序），分页加载。任务结束后不再变化。 */
export function importRenamesQuery(datasetId: number, importId: number) {
  return infiniteQueryOptions({
    queryKey: datasetKeys.importRenames(datasetId, importId),
    queryFn: ({ signal, pageParam }) =>
      apiGet<ListResponse<ImageRename>>(
        withQuery(`${importPath(datasetId, importId)}/renames`, {
          offset: pageParam,
          limit: IMPORT_RENAMES_PAGE_SIZE,
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
    staleTime: Infinity,
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
  input: { name?: string; description?: string; status?: DatasetStatus }
) {
  return apiSend<Dataset>("PATCH", datasetPath(id), input)
}

/**
 * 把数据集移入回收站（7 天内可以恢复，见 features/recycle-bin）。`deleteFiles`：
 * 到期永久删除时一并删除磁盘上的文件夹；否则文件夹留在原处。
 */
export function deleteDataset(id: number, deleteFiles: boolean) {
  return apiSend<void>(
    "DELETE",
    deleteFiles ? `${datasetPath(id)}?delete_files=true` : datasetPath(id)
  )
}

/** 设置数据集纳入的项目（整体替换），返回更新后的数据集。 */
export function setDatasetProjects(id: number, projectIds: number[]) {
  return apiSend<Dataset>("PUT", `${datasetPath(id)}/projects`, {
    project_ids: projectIds,
  })
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
  /** 省略时为 `voc`。 */
  annotation_format?: AnnotationFormat
  /** `annotation_format` 为 `yolo` 时必填。 */
  classes?: string[]
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

export type CreateExportInput = {
  format: AnnotationFormat
  /**
   * `format` 为 `yolo` 时与 `classes` 二选一：类别编号按这个项目的类别列表的
   * 顺序（第 1 个类别为 0）。项目必须包含这个数据集，且类别列表不为空。
   */
  project_id?: number
  /** `format` 为 `yolo` 且数据集不属于任何项目时：手动填写的类别顺序。 */
  classes?: string[]
} & Partial<VersionSelection>

// ---- 审图版本 ----

/** 审图版本来自的模型（合并时的快照）。 */
export type DatasetVersionModel = {
  id: number
  project_id: number
  project_name: string
  version: number
  yolo_version: string
  task: string
  file_name: string
}

/**
 * 数据集的审图版本：合并审图任务时生成的审图标注快照（按图片在数据集文件夹中的
 * 路径），不修改数据集的标注文件（基础版本）。每个数据集依次为审图1.0、1.1…，
 * 编号不重复使用。
 */
export type DatasetVersion = {
  id: number
  dataset_id: number
  kind: "review"
  /** 如“审图1.3”；正在生成时为空。 */
  name: string
  major: number | null
  minor: number | null
  note: string
  /** `building`：合并还在进行，完成后编号并变为 `ready`，失败时删除。 */
  status: "building" | "ready"
  image_count: number
  needs_sample_count: number
  approved_count: number
  object_count: number
  /** 来自的审图任务（当前名称）；任务删除后为 null，见 `source`。 */
  review: { id: number; name: string } | null
  merge_id: number | null
  /** 合并时的快照：审图任务删除后也保留。 */
  source: {
    review_id: number
    review_name: string
    merge_id: number
    /** 合并的审核状态。 */
    statuses: ReviewImageStatus[]
    model: DatasetVersionModel
  }
  created_at: string
  updated_at: string
  finished_at: string | null
}

/**
 * 下载时组合的版本：基础版本（数据集自己的标注）与审图版本。每张图片用所选
 * 版本中最新的、包含这张图片的版本的标注，没有时用基础版本；不选基础版本时只
 * 导出（最新的那个版本中）标为需加样的图片。
 */
export type VersionSelection = {
  include_base: boolean
  version_ids: number[]
}

/** 一次下载的内容（按索引统计，与数据集的计数一样）。 */
export type ExportPreview = {
  image_count: number
  /** 其中用审图版本标注的图片数。 */
  from_versions: number
  annotated_count: number
  empty_count: number
  invalid_count: number
  unannotated_count: number
  object_count: number
  /** 按目标数降序、名称升序。 */
  labels: DatasetLabel[]
  /** 所选版本中被选中的图片数（每个路径一张；不选基础版本时只算需加样的）。 */
  version_image_count: number
  /** 其中已不在数据集中的图片数（不导出）。 */
  missing_count: number
  /** 其中合并后文件被替换的图片数（用基础版本的标注）。 */
  changed_count: number
}

export function datasetVersionsQuery(id: number) {
  return queryOptions({
    queryKey: datasetKeys.versions(id),
    queryFn: ({ signal }) =>
      apiGet<ListResponse<DatasetVersion>>(`${datasetPath(id)}/versions`, {
        signal,
      }),
    // 有正在生成的版本时轮询，直到合并完成。
    refetchInterval: (query) =>
      query.state.data?.items.some((v) => v.status === "building")
        ? 2_000
        : false,
  })
}

export function exportPreviewQuery(id: number, selection: VersionSelection) {
  return queryOptions({
    queryKey: datasetKeys.exportPreview(id, selection),
    queryFn: ({ signal }) =>
      apiGet<ExportPreview>(
        withQuery(`${datasetPath(id)}/exports/preview`, {
          include_base: String(selection.include_base),
          version_ids: selection.version_ids.join(","),
        }),
        { signal }
      ),
    placeholderData: keepPreviousData,
  })
}

export function updateDatasetVersion(
  datasetId: number,
  versionId: number,
  input: { note: string }
) {
  return apiSend<DatasetVersion>(
    "PATCH",
    `${datasetPath(datasetId)}/versions/${versionId}`,
    input
  )
}

/** 把审图版本移入回收站（7 天内可以恢复，见 features/recycle-bin）。 */
export function deleteDatasetVersion(datasetId: number, versionId: number) {
  return apiSend<void>(
    "DELETE",
    `${datasetPath(datasetId)}/versions/${versionId}`
  )
}

/**
 * 下载链接：`url` 同源、1 小时内有效，压缩包在下载时流式生成（没有
 * Content-Length），用浏览器原生下载，不要读进内存。
 */
export type DatasetExport = {
  url: string
  file_name: string
  expires_at: string
}

export function createExport(datasetId: number, input: CreateExportInput) {
  return apiSend<DatasetExport>(
    "POST",
    `${datasetPath(datasetId)}/exports`,
    input
  )
}

export type SaveAnnotationInput = {
  objects: AnnotationObject[]
  /** 编辑开始时文件的版本（{@link DatasetImageDetail.annotation_version}）。 */
  base_version: string
  /** 不检查版本，直接覆盖（冲突时用户选择“覆盖”）。 */
  force?: boolean
  /**
   * 文件无法在保留其它内容的情况下修改（`annotation_unpatchable`）时，按平台的
   * 格式重新生成整个文件（用户选择“仍然保存”）。不带 force 时仍然检查版本。
   */
  regenerate?: boolean
}

/**
 * 把图片的标注写入 VOC 文件（没有目标时写入空标注，即负样本），返回保存后
 * 的图片详情（含新的 `annotation_version`）。
 */
export function saveAnnotation(
  datasetId: number,
  imageId: number,
  input: SaveAnnotationInput,
  init?: RequestInit
) {
  return apiSend<DatasetImageDetail>(
    "PUT",
    `${datasetPath(datasetId)}/images/${imageId}/annotation`,
    input,
    init
  )
}

/**
 * 删除图片的标注文件（图片变为“未标注”），返回删除后的图片详情。`init` 用于
 * 关闭页面时的 `keepalive` 请求。
 */
export function deleteAnnotation(
  datasetId: number,
  imageId: number,
  baseVersion: string,
  force = false,
  init?: RequestInit
) {
  // base_version 为空（没有文件）也要带上：它表示“我看到的是没有文件”。
  const search = new URLSearchParams({ base_version: baseVersion })
  if (force) {
    search.set("force", "true")
  }
  return apiSend<DatasetImageDetail>(
    "DELETE",
    `${datasetPath(datasetId)}/images/${imageId}/annotation?${search}`,
    undefined,
    init
  )
}
