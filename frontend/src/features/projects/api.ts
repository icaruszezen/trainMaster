/**
 * 项目模块的 API 类型、查询与写操作。
 *
 * 这里的类型就是与后端 `internal/httpapi/projects.go` 的契约（字段名、取值），
 * 修改时两边同步，并更新 docs/architecture.md 的“项目接口”。
 */
import { queryOptions } from "@tanstack/react-query"

import type { DataVersionStatus } from "@/features/data-versions/api"
import {
  activeImportRefetchInterval,
  datasetKeys,
  type Dataset,
  type DatasetImageRef,
} from "@/features/datasets/api"
import type { YoloVersion } from "@/features/models/api"
import { apiGet, apiSend, type ListResponse } from "@/lib/api"
import { queryClient } from "@/lib/query-client"

/** 项目卡片的封面：来自项目中某个数据集的一张图片。 */
export type ProjectCover = DatasetImageRef & { dataset_id: number }

/** 项目中版本号最大的模型（详见 features/models）。 */
export type ProjectLatestModel = {
  id: number
  version: number
  yolo_version: YoloVersion
  created_at: string
}

/**
 * 项目中版本号最大的数据版本（不在回收站中的，任何状态；详见
 * features/data-versions）。
 */
export type ProjectLatestDataVersion = {
  id: number
  version: number
  status: DataVersionStatus
  created_at: string
}

/**
 * 项目：一组数据集、由它们生成的数据版本与模型仓库中归属它的模型（以后还有
 * 训练任务）。图片、标注等统计数据是其数据集的合计。
 */
export type Project = {
  id: number
  name: string
  description: string
  dataset_count: number
  image_count: number
  /** 标注文件中至少有一个目标的图片数。 */
  annotated_count: number
  /** 有标注文件但没有目标的图片数（负样本）。 */
  empty_count: number
  /** 有同名 xml 但无法按 VOC 解析的图片数。 */
  invalid_count: number
  object_count: number
  /** 图片文件的总字节数。 */
  total_bytes: number
  /** 最多 4 张，轮流取自项目中的前几个数据集。 */
  covers: ProjectCover[]
  /** 模型仓库中归属该项目的模型数。 */
  model_count: number
  /** 没有模型时为 null。 */
  latest_model: ProjectLatestModel | null
  /** 项目的数据版本数（不含回收站中的，任何状态）。 */
  data_version_count: number
  /** 没有数据版本时为 null。 */
  latest_data_version: ProjectLatestDataVersion | null
  created_at: string
  /** 修改信息、增减数据集时更新。 */
  updated_at: string
}

/** 项目中所有数据集合计的标签分布。 */
export type ProjectLabel = {
  name: string
  object_count: number
  image_count: number
  /** 含有该标签目标的数据集数。 */
  dataset_count: number
}

/**
 * 项目类别列表中的一个类别。列表的顺序就是训练时 YOLO 的类别编号（第 1 个
 * 为 0）。`color` 为 `#rrggbb`，`""` 表示按名称自动配色。
 */
export type ProjectClass = { name: string; color: string }

/**
 * 类别列表接口的响应。`version` 标识列表的内容（不透明的字符串，空列表也有），
 * 整体替换时作为 `base_version` 带上，列表已被别人修改时返回 409 `conflict`。
 */
export type ProjectClassList = ListResponse<ProjectClass> & { version: string }

const BASE = "/api/v1/projects"

export const projectPath = (id: number) => `${BASE}/${id}`

function withSearch(path: string, q: string) {
  return q ? `${path}?${new URLSearchParams({ q })}` : path
}

/** 查询键。`detail(id)` 是该项目下所有查询（数据集、标签）的前缀。 */
export const projectKeys = {
  all: ["projects"] as const,
  lists: () => [...projectKeys.all, "list"] as const,
  list: (q: string) => [...projectKeys.lists(), { q }] as const,
  detail: (id: number) => [...projectKeys.all, "detail", id] as const,
  datasets: (id: number) => [...projectKeys.detail(id), "datasets"] as const,
  labels: (id: number) => [...projectKeys.detail(id), "labels"] as const,
  classes: (id: number) => [...projectKeys.detail(id), "classes"] as const,
}

/** 项目列表（`q` 按名称 / 描述筛选），按更新时间倒序。 */
export function projectsQuery(q = "") {
  return queryOptions({
    queryKey: projectKeys.list(q),
    queryFn: ({ signal }) =>
      apiGet<ListResponse<Project>>(withSearch(BASE, q), { signal }),
  })
}

export function projectQuery(id: number) {
  return queryOptions({
    queryKey: projectKeys.detail(id),
    queryFn: ({ signal }) => apiGet<Project>(projectPath(id), { signal }),
  })
}

/** 项目中的数据集；有导入正在进行时定期刷新。 */
export function projectDatasetsQuery(id: number) {
  return queryOptions({
    queryKey: projectKeys.datasets(id),
    queryFn: ({ signal }) =>
      apiGet<ListResponse<Dataset>>(`${projectPath(id)}/datasets`, {
        signal,
      }),
    refetchInterval: activeImportRefetchInterval,
  })
}

export function projectLabelsQuery(id: number) {
  return queryOptions({
    queryKey: projectKeys.labels(id),
    queryFn: ({ signal }) =>
      apiGet<ListResponse<ProjectLabel>>(`${projectPath(id)}/labels`, {
        signal,
      }),
  })
}

/** 项目的类别列表（带版本）。 */
export function projectClassesQuery(id: number) {
  return queryOptions({
    queryKey: projectKeys.classes(id),
    queryFn: ({ signal }) =>
      apiGet<ProjectClassList>(`${projectPath(id)}/classes`, { signal }),
  })
}

/**
 * 标注数据集时优先提示的类别：数据集所属各项目的类别列表，按项目名称依次
 * 合并，同名类别只保留第一个（颜色以它为准）。数据集不属于任何项目时为空。
 * 只读：类别列表在项目页面中修改。
 */
export function datasetClassesQuery(datasetId: number) {
  return queryOptions({
    queryKey: datasetKeys.classes(datasetId),
    queryFn: ({ signal }) =>
      apiGet<ListResponse<ProjectClass>>(
        `/api/v1/datasets/${datasetId}/classes`,
        { signal }
      ),
  })
}

/**
 * 设置项目的类别列表（整体替换），返回保存后的列表。`baseVersion` 是编辑
 * 开始时列表的 `version`：列表在此之后被修改过时返回 409 `conflict`，不做任何
 * 改动。
 */
export function setProjectClasses(
  id: number,
  classes: ProjectClass[],
  baseVersion: string
) {
  return apiSend<ProjectClassList>("PUT", `${projectPath(id)}/classes`, {
    classes,
    base_version: baseVersion,
  })
}

/**
 * 在类别列表末尾加入一个类别（已有同名类别时列表不变），返回加入后的列表。
 * 只追加，不会覆盖别人同时做的修改。
 */
export function addProjectClass(id: number, name: string, color = "") {
  return apiSend<ProjectClassList>("POST", `${projectPath(id)}/classes`, {
    name,
    color,
  })
}

/**
 * 项目的类别列表保存后：写入缓存，并刷新各数据集的标注候选类别（数据集可能
 * 属于这个项目）与项目信息（更新时间）。
 */
export function cacheProjectClasses(id: number, list: ProjectClassList) {
  queryClient.setQueryData(projectKeys.classes(id), list)
  invalidateDatasetClasses()
  void queryClient.invalidateQueries({
    queryKey: projectKeys.detail(id),
    exact: true,
  })
  void queryClient.invalidateQueries({ queryKey: projectKeys.lists() })
}

/** 所有数据集的标注候选类别（项目的类别列表或数据集所属的项目变化后）。 */
function invalidateDatasetClasses() {
  void queryClient.invalidateQueries({
    queryKey: datasetKeys.details(),
    predicate: (query) =>
      query.queryKey.length === datasetKeys.classes(0).length &&
      query.queryKey[query.queryKey.length - 1] === "classes",
  })
}

export type CreateProjectInput = {
  name: string
  description?: string
  /** 创建时直接纳入的数据集。 */
  dataset_ids?: number[]
}

export function createProject(input: CreateProjectInput) {
  return apiSend<Project>("POST", BASE, input)
}

export function updateProject(
  id: number,
  input: { name?: string; description?: string }
) {
  return apiSend<Project>("PATCH", projectPath(id), input)
}

/**
 * 删除项目；其中的数据集保留，只是不再属于该项目。项目中有模型时必须同时删除
 * 模型（`deleteModels`，模型移入回收站），有数据版本时必须同时删除数据版本
 * （`deleteDataVersions`：已生成的移入回收站，其余直接删除），否则 409
 * `conflict`。
 */
export function deleteProject(
  id: number,
  {
    deleteModels = false,
    deleteDataVersions = false,
    deleteTrainingJobs = false,
  } = {}
) {
  const params = new URLSearchParams()
  if (deleteModels) {
    params.set("delete_models", "true")
  }
  if (deleteDataVersions) {
    params.set("delete_data_versions", "true")
  }
  if (deleteTrainingJobs) {
    params.set("delete_training_jobs", "true")
  }
  const query = params.toString()
  return apiSend<void>(
    "DELETE",
    query ? `${projectPath(id)}?${query}` : projectPath(id)
  )
}

/** 把数据集纳入项目（已在项目中的忽略），返回更新后的项目。 */
export function addProjectDatasets(id: number, datasetIds: number[]) {
  return apiSend<Project>("POST", `${projectPath(id)}/datasets`, {
    dataset_ids: datasetIds,
  })
}

/** 把数据集移出项目（数据集本身不受影响）。 */
export function removeProjectDataset(id: number, datasetId: number) {
  return apiSend("DELETE", `${projectPath(id)}/datasets/${datasetId}`)
}

/** 新建的项目立即写入缓存（详情与不带搜索的列表），列表随后在后台刷新。 */
export function cacheNewProject(project: Project) {
  queryClient.setQueryData(projectKeys.detail(project.id), project)
  queryClient.setQueryData<ListResponse<Project>>(
    projectKeys.list(""),
    (list) =>
      list && !list.items.some((item) => item.id === project.id)
        ? { items: [project, ...list.items], total: list.total + 1 }
        : list
  )
  void queryClient.invalidateQueries({ queryKey: projectKeys.lists() })
}

/**
 * 项目与数据集的关系、或项目名称变化后刷新：所有项目查询、数据集列表（项目标记
 * 与筛选结果），各数据集的详情（只是详情本身，不含图片等子查询；只有正在
 * 显示的会立即重新请求）与标注候选类别。
 */
export function refreshMembership() {
  void queryClient.invalidateQueries({ queryKey: projectKeys.all })
  void queryClient.invalidateQueries({ queryKey: datasetKeys.lists() })
  void queryClient.invalidateQueries({
    queryKey: datasetKeys.details(),
    predicate: (query) =>
      query.queryKey.length === datasetKeys.details().length + 1,
  })
  // 数据集所属的项目变了，标注时的候选类别（来自这些项目）也随之变化。
  invalidateDatasetClasses()
}
