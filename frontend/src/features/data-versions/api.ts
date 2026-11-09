/**
 * 数据版本的 API 类型、查询与写操作。
 *
 * 这里的类型就是与后端 `internal/dataversion`（`types.go`）、
 * `internal/httpapi/dataversions.go` 的契约（字段名、取值），修改时两边同步，
 * 并更新 docs/architecture.md 的“数据版本接口”。约定：未知的数值为 `null`，
 * 未知的字符串为 `""`，数组与对象字段永远存在。
 *
 * “版本”在数据集页面上指审图版本（features/datasets/versions.ts）；这里的
 * 数据版本是项目的冻结训练集，界面上总是写作“数据版本 v3”。
 */
import {
  infiniteQueryOptions,
  keepPreviousData,
  queryOptions,
} from "@tanstack/react-query"

import type { AnnotationObject } from "@/features/datasets/api"
import { projectKeys } from "@/features/projects/api"
import { apiGet, apiSend, type ListResponse } from "@/lib/api"
import { queryClient } from "@/lib/query-client"

/** queued → building → ready | failed；生成失败的版本可以重新生成。 */
export type DataVersionStatus = "queued" | "building" | "ready" | "failed"

/** 生成的阶段。 */
export type DataVersionStage =
  "preparing" | "linking" | "augmenting" | "finishing"

export type Split = "train" | "val" | "test"

export const SPLITS: readonly Split[] = ["train", "val", "test"]

/** 图片的划分来自比例（固定随机种子），还是来自它所在的 train / val / test 文件夹。 */
export type SplitBy = "ratio" | "folder"

/** 防止数据泄漏的分组：同一文件夹中文件名相同（去掉副本、增强后缀）的图片，或同一个最底层文件夹中的全部图片，总在同一个划分中。 */
export type GroupBy = "stem" | "folder"

/** 增强方法，说明见 augmentations.ts。 */
export type AugmentMethod =
  "hflip" | "vflip" | "rotate90" | "brightness_contrast" | "blur" | "noise"

/** 图片文件在版本文件夹中的存储方式：硬链接到数据集文件、复制、增强生成。 */
export type StoredKind = "link" | "copy" | "generated"

export type DataVersionFormat = "yolo" | "voc" | "coco"

export type DataVersionClass = { name: string; color: string }

/**
 * 参与训练的图片：`annotated` 仅有标注的图片（有目标的图片，加上
 * `include_empty` 时的空标注图片；没有标注的图片不纳入）；`background` 仅无标注
 * 的背景图（没有目标的图片，含没有标注文件的，作为负样本；有目标的图片不纳入）；
 * `all` 整个数据集（没有目标的图片作为负样本）。版本有一个默认的设置，每个
 * 数据集可以单独修改（名称与说明见 format.ts）。
 */
export type TrainingScope = "annotated" | "background" | "all"

export type SplitConfig = {
  /** 百分比：train ≥ 1，val ≥ 1，test ≥ 0，三者之和为 100。 */
  train: number
  val: number
  test: number
  /** 0–2147483647。 */
  seed: number
  /**
   * 数据集同时有 train 与 val 文件夹时（如 images/train、images/val），
   * 其中的图片沿用文件夹的划分。
   */
  use_folders: boolean
  group_by: GroupBy
}

export type Augmentation = {
  methods: AugmentMethod[]
  /** 每张训练集图片生成的增强图片数，1–maxCopies(methods)（最多 5）。 */
  copies: number
  /** 只增强有目标的图片（负样本不增强）。 */
  only_with_objects: boolean
}

export type DataVersionConfig = {
  split: SplitConfig
  /** 数据集默认的参与训练的图片（各数据集使用的见 {@link DataVersionSource}）。 */
  scope: TrainingScope
  /** 纳入空标注图片（负样本）；只影响 `annotated` 的数据集，其它两种总是纳入。 */
  include_empty: boolean
  /**
   * 不在类别列表中、去掉其目标（保留图片）的标签。含有其它不在类别列表中的
   * 标签的图片不纳入。
   */
  ignore_labels: string[]
  augmentation: Augmentation | null
}

export type DataVersionSource = {
  dataset_id: number
  dataset_name: string
  dir_name: string
  include_base: boolean
  /** 所选的审图版本，新的在前。 */
  versions: { id: number; name: string }[]
  /** 这个数据集使用的参与训练的图片（单独设置的或默认的）。 */
  scope: TrainingScope
  /** 从这个数据集纳入的原图数（生成完成前为 0）。 */
  image_count: number
  /** 数据集已不在使用中（在回收站中或已永久删除）。 */
  dataset_deleted: boolean
}

export type DataVersionProgress = {
  stage: DataVersionStage
  done: number
  /** 0 表示总数还不知道。 */
  total: number
}

export type SplitCounts = { train: number; val: number; test: number }

export type ObjectImages = { objects: number; images: number }

export type DataVersionLabelStats = {
  name: string
  color: string
  train: ObjectImages
  val: ObjectImages
  test: ObjectImages
}

export type DataVersionSkipped = {
  /** 没有标注。 */
  unannotated: number
  /** 标注文件无法读取。 */
  invalid: number
  /** 含有不在类别列表中、也没有忽略的标签。 */
  unlisted: number
  /** 空标注图片（include_empty 为 false 时）。 */
  empty: number
  /** 有目标的图片（参与训练的图片为“仅无标注的背景图”的数据集）。 */
  with_objects: number
  /** 文件在上次扫描后被删除（生成时才知道）。 */
  missing: number
  /** 文件在上次扫描后被修改（生成时才知道，重新扫描数据集即可）。 */
  changed: number
  /** 无法读取图片尺寸。 */
  unknown_size: number
  /** 训练工具不读取的格式（GIF、AVIF）。 */
  unsupported_format: number
  /** 无法增强的训练集图片（按图片计，生成时才知道）。 */
  augment_skipped: number
}

/** 与项目中更早的一个数据版本对比原图（按数据集与图片路径对应）。 */
export type DataVersionCompare = {
  id: number
  version: number
  /** 这里有、那里没有。 */
  added: number
  /** 那里有、这里没有。 */
  removed: number
  /** 两边都有、划分不同。 */
  split_changed: number
  /** 两边都有、标注不同（预览中为 0）。 */
  annotation_changed: number
}

export type DataVersionStats = {
  /** 类别列表的顺序。生成完成前为空数组。 */
  labels: DataVersionLabelStats[]
  /** 没有目标的图片。 */
  negatives: SplitCounts
  /** 划分来自所在文件夹的原图数。 */
  by_folder: number
  skipped: DataVersionSkipped
  /** 去掉的忽略标签的目标数。 */
  dropped_objects: number
  /** 裁剪到图片范围后没有面积、被去掉的框。 */
  degenerate_objects: number
  storage: {
    linked: number
    copied: number
    generated: number
    /** 重置了 EXIF / TIFF 方向标记而复制的原图。 */
    orientation_fixed: number
    copied_bytes: number
    generated_bytes: number
  }
  /** 与项目中上一个已生成的数据版本对比；没有时为 null。 */
  compare: DataVersionCompare | null
}

export type DataVersion = {
  id: number
  project_id: number
  /** 项目当前的名称。 */
  project_name: string
  /** 项目内的版本号 1、2…，不会再分配给别的版本。界面上写作“数据版本 v3”（format.ts）。 */
  version: number
  note: string
  status: DataVersionStatus
  /** 生成失败的原因。 */
  error: string
  /** 正在生成时的进度，其它状态为 null。 */
  progress: DataVersionProgress | null
  /** 排队时的位置，1 表示下一个生成；其它状态为 null。 */
  queue_position: number | null
  /** 创建时项目的类别列表，顺序即 YOLO 的类别编号。 */
  classes: DataVersionClass[]
  /** 冻结的类别列表的版本，与项目类别列表的 version 相同时说明项目的类别没有变。 */
  class_version: string
  config: DataVersionConfig
  sources: DataVersionSource[]
  /** 计数在生成完成后才有（之前为 0）；image_count、object_count 与 train_count 包含增强图片。 */
  image_count: number
  object_count: number
  train_count: number
  val_count: number
  test_count: number
  augmented_count: number
  /** 全部图片文件的大小；硬链接的原图与数据集共用磁盘空间（新增占用见 stats.storage）。 */
  total_bytes: number
  /** 总是完整的对象，生成完成前为 0 与空数组。 */
  stats: DataVersionStats
  created_at: string
  updated_at: string
  started_at: string | null
  finished_at: string | null
}

export type SourceInput = {
  dataset_id: number
  include_base: boolean
  version_ids: number[]
  /** 单独设置的参与训练的图片；`""` 使用默认的（`scope`）。 */
  scope: TrainingScope | ""
}

export type CreateDataVersionInput = {
  project_id: number
  note: string
  /** 表单显示的类别列表的版本（预览返回的 class_version）；项目的类别在这之后变了，创建返回 409。 */
  class_version: string
  /** 1–1000 个项目中的数据集，每个一次（版本中的顺序是项目中数据集的顺序）。 */
  sources: SourceInput[]
  split: SplitConfig
  /** 数据集默认的参与训练的图片。 */
  scope: TrainingScope
  include_empty: boolean
  ignore_labels: string[]
  augmentation: Augmentation | null
}

/** 预览的输入：创建的输入去掉备注与类别列表的版本。 */
export type DataVersionPreviewInput = Omit<
  CreateDataVersionInput,
  "note" | "class_version"
>

export type PreviewSplit = {
  images: number
  objects: number
  negatives: number
  by_folder: number
}

export type DataVersionPreview = {
  /** 项目当前的类别列表与它的版本（创建时带上 class_version）。 */
  classes: DataVersionClass[]
  class_version: string
  /** 将纳入的原图数。 */
  image_count: number
  object_count: number
  /** 将生成的增强图片数（估计）。 */
  augmented_count: number
  sources: {
    dataset_id: number
    dataset_name: string
    /** 这个数据集使用的参与训练的图片。 */
    scope: TrainingScope
    /** 所选标注来源中的图片数（过滤前）。 */
    selected_count: number
    /** 其中标注来自审图版本的图片数。 */
    from_versions: number
    /** 其中纳入的图片数。 */
    image_count: number
    /** 纳入的图片中没有目标的图片数（负样本）。 */
    negatives: number
    /** 纳入的图片中按所在文件夹划分的图片数。 */
    by_folder: number
  }[]
  /** missing、changed 与 augment_skipped 在生成时才知道，预览中为 0。 */
  skipped: DataVersionSkipped
  dropped_objects: number
  degenerate_objects: number
  splits: Record<Split, PreviewSplit>
  /** 类别列表的顺序：各划分中的目标数与图片数。 */
  labels: DataVersionLabelStats[]
  /**
   * 所选图片中不在类别列表中的标签，按目标数降序。other_project：这个标签在
   * 共用其中某个数据集的另一个项目的类别列表中（通常应忽略这些框）。
   */
  unlisted_labels: {
    name: string
    objects: number
    images: number
    other_project: boolean
  }[]
  /** 与项目中最新的已生成数据版本对比；没有时为 null。 */
  compare: DataVersionCompare | null
}

export type DataVersionImage = {
  id: number
  position: number
  split: Split
  split_by: SplitBy
  /** 在版本文件夹中的路径：images/<数据集文件夹>/…（增强图片在原图旁边，名为 <原名>-aug<k>）。 */
  path: string
  file_name: string
  width: number
  height: number
  size_bytes: number
  object_count: number
  /** 按目标数降序。 */
  labels: { name: string; count: number }[]
  dataset_id: number
  dataset_name: string
  /** 在数据集文件夹中的路径。 */
  source_path: string
  /** 标注来自的审图版本；null 表示基础版本。 */
  annotation_source: { id: number; name: string } | null
  /** 增强图片的原图；原图为 null。 */
  augmented_from: number | null
  /** 增强图片应用的方法，按顺序；原图为空数组。 */
  augment: AugmentMethod[]
  stored: StoredKind
  thumbnail_url: string
  file_url: string
}

export type DataVersionImageDetail = DataVersionImage & {
  /** 负样本为空数组。 */
  objects: AnnotationObject[]
}

export type DataVersionImageFilters = {
  split: Split | ""
  label: string
  dataset_id: number | null
  /** null 表示全部，true 只看增强图片，false 只看原图。 */
  augmented: boolean | null
  /** 路径或数据集中路径包含的文字（不区分大小写）。 */
  q: string
}

export const EMPTY_IMAGE_FILTERS: DataVersionImageFilters = {
  split: "",
  label: "",
  dataset_id: null,
  augmented: null,
  q: "",
}

/**
 * 下载链接：`url` 同源、1 小时内有效，压缩包在下载时流式生成（没有
 * Content-Length），用浏览器原生下载，不要读进内存。
 */
export type DataVersionExport = {
  url: string
  file_name: string
  expires_at: string
}

const BASE = "/api/v1/data-versions"

export const dataVersionPath = (id: number) => `${BASE}/${id}`

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

export const dataVersionKeys = {
  all: ["data-versions"] as const,
  lists: () => [...dataVersionKeys.all, "list"] as const,
  list: (projectId: number | null) =>
    [...dataVersionKeys.lists(), projectId] as const,
  previews: () => [...dataVersionKeys.all, "preview"] as const,
  preview: (input: DataVersionPreviewInput) =>
    [...dataVersionKeys.previews(), input] as const,
  details: () => [...dataVersionKeys.all, "detail"] as const,
  detail: (id: number) => [...dataVersionKeys.details(), id] as const,
  images: (id: number, filters: DataVersionImageFilters) =>
    [...dataVersionKeys.detail(id), "images", filters] as const,
  image: (id: number, imageId: number) =>
    [...dataVersionKeys.detail(id), "image", imageId] as const,
}

/** 排队或正在生成：列表与详情在这时轮询。 */
export function isActiveDataVersion(v: Pick<DataVersion, "status">) {
  return v.status === "queued" || v.status === "building"
}

/** 全部（或一个项目的）数据版本，新的在前；有排队或正在生成的版本时每 2 秒刷新。 */
export function dataVersionsQuery(projectId: number | null = null) {
  return queryOptions({
    queryKey: dataVersionKeys.list(projectId),
    queryFn: ({ signal }) =>
      apiGet<ListResponse<DataVersion>>(
        withQuery(BASE, { project_id: projectId ?? "" }),
        { signal }
      ),
    refetchInterval: (query) =>
      query.state.data?.items.some(isActiveDataVersion) ? 2_000 : false,
  })
}

export function dataVersionQuery(id: number) {
  return queryOptions({
    queryKey: dataVersionKeys.detail(id),
    queryFn: ({ signal }) =>
      apiGet<DataVersion>(dataVersionPath(id), { signal }),
    refetchInterval: (query) =>
      query.state.data && isActiveDataVersion(query.state.data) ? 1_500 : false,
  })
}

/**
 * 生成前的预览（按数据集的索引统计，不读文件）。调用方应先防抖（约 300 毫秒），
 * 输入无效时不要发请求（enabled: false）。
 */
export function dataVersionPreviewQuery(input: DataVersionPreviewInput) {
  return queryOptions({
    queryKey: dataVersionKeys.preview(input),
    queryFn: ({ signal }) =>
      apiSend<DataVersionPreview>("POST", `${BASE}/preview`, input, {
        signal,
      }),
    placeholderData: keepPreviousData,
    staleTime: 0,
  })
}

export const DATA_VERSION_IMAGES_PAGE_SIZE = 60

export function dataVersionImagesQuery(
  id: number,
  filters: DataVersionImageFilters
) {
  return infiniteQueryOptions({
    queryKey: dataVersionKeys.images(id, filters),
    queryFn: ({ signal, pageParam }) =>
      apiGet<ListResponse<DataVersionImage>>(
        withQuery(`${dataVersionPath(id)}/images`, {
          offset: pageParam,
          limit: DATA_VERSION_IMAGES_PAGE_SIZE,
          split: filters.split,
          label: filters.label,
          dataset_id: filters.dataset_id ?? "",
          augmented:
            filters.augmented === null ? "" : String(filters.augmented),
          q: filters.q,
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
    placeholderData: keepPreviousData,
    // 版本生成后不再变化。
    staleTime: Infinity,
  })
}

export function dataVersionImageQuery(id: number, imageId: number) {
  return queryOptions({
    queryKey: dataVersionKeys.image(id, imageId),
    queryFn: ({ signal }) =>
      apiGet<DataVersionImageDetail>(
        `${dataVersionPath(id)}/images/${imageId}`,
        { signal }
      ),
    staleTime: Infinity,
  })
}

export function createDataVersion(input: CreateDataVersionInput) {
  return apiSend<DataVersion>("POST", BASE, input)
}

/** 修改备注（任何状态都可以，回收站中的除外）。 */
export function updateDataVersion(id: number, input: { note: string }) {
  return apiSend<DataVersion>("PATCH", dataVersionPath(id), input)
}

/**
 * 删除数据版本：已生成的移入回收站（7 天内可以恢复，见 features/recycle-bin），
 * 生成失败、排队中与正在生成的直接删除（正在生成的会停止）。
 */
export function deleteDataVersion(id: number) {
  return apiSend<void>("DELETE", dataVersionPath(id))
}

/**
 * 从回收站恢复数据版本（删除后提示中的“撤销”）。项目随它在回收站中时一并
 * 恢复；项目的名称已被占用时 409 `name_taken`，带 `projectName` 用新名称恢复。
 */
export function restoreDataVersion(id: number, projectName?: string) {
  return apiSend<DataVersion>(
    "POST",
    `/api/v1/recycle-bin/data-versions/${id}/restore`,
    projectName === undefined ? undefined : { project_name: projectName }
  )
}

/** 重新生成失败的版本（同样的类别、设置与数据集，按数据集现在的内容）。 */
export function retryDataVersion(id: number) {
  return apiSend<DataVersion>("POST", `${dataVersionPath(id)}/retry`)
}

export function createDataVersionExport(
  id: number,
  input: { format: DataVersionFormat; include_augmented: boolean }
) {
  return apiSend<DataVersionExport>(
    "POST",
    `${dataVersionPath(id)}/exports`,
    input
  )
}

/** 刷新数据版本列表与详情，以及显示数据版本数的项目。 */
export function refreshDataVersions() {
  return Promise.all([
    queryClient.invalidateQueries({
      queryKey: dataVersionKeys.all,
      // 图片列表与单张图片在版本生成后不再变化：不重新加载已经翻过的页
      // （生成结束时 build-watch 刷新那个版本的全部查询）。
      predicate: (query) => !isDataVersionImageKey(query.queryKey),
    }),
    queryClient.invalidateQueries({ queryKey: projectKeys.all }),
  ])
}

/** 一个版本的图片列表与单张图片（重新生成之后）。 */
export function refreshDataVersionImages(id: number) {
  return queryClient.invalidateQueries({
    queryKey: dataVersionKeys.detail(id),
    predicate: (query) => isDataVersionImageKey(query.queryKey),
  })
}

/** dataVersionKeys.images 与 dataVersionKeys.image 的查询键。 */
function isDataVersionImageKey(key: readonly unknown[]) {
  return key[1] === "detail" && (key[3] === "images" || key[3] === "image")
}

/**
 * 数据版本移入或移出回收站之后：数据版本、项目与回收站。回收站的查询键与
 * features/recycle-bin/api 的 recycleBinKeys.all 相同（这里不导入它，避免循环
 * 依赖）。
 */
export function refreshAfterDataVersionMove() {
  return Promise.all([
    refreshDataVersions(),
    queryClient.invalidateQueries({ queryKey: ["recycle-bin"] }),
  ])
}
