/**
 * 项目模块的 API 类型、查询与写操作。
 *
 * 这里的类型就是与后端 `internal/httpapi/projects.go` 的契约（字段名、取值），
 * 修改时两边同步，并更新 docs/architecture.md 的“项目接口”。
 */
import { queryOptions } from "@tanstack/react-query"

import {
  activeImportRefetchInterval,
  datasetKeys,
  type Dataset,
  type DatasetImageRef,
} from "@/features/datasets/api"
import { apiGet, apiSend, type ListResponse } from "@/lib/api"
import { queryClient } from "@/lib/query-client"

/** 项目卡片的封面：来自项目中某个数据集的一张图片。 */
export type ProjectCover = DatasetImageRef & { dataset_id: number }

/** 项目：一组数据集（以后还有训练任务与模型）。统计数据是其数据集的合计。 */
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

/** 删除项目；其中的数据集保留，只是不再属于该项目。 */
export function deleteProject(id: number) {
  return apiSend("DELETE", projectPath(id))
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
 * 与筛选结果），以及各数据集的详情（只是详情本身，不含图片等子查询；只有正在
 * 显示的会立即重新请求）。
 */
export function refreshMembership() {
  void queryClient.invalidateQueries({ queryKey: projectKeys.all })
  void queryClient.invalidateQueries({ queryKey: datasetKeys.lists() })
  void queryClient.invalidateQueries({
    queryKey: datasetKeys.details(),
    predicate: (query) =>
      query.queryKey.length === datasetKeys.details().length + 1,
  })
}
