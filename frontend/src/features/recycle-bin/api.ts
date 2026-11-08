/**
 * 回收站的 API 类型、查询与写操作。
 *
 * 这里的类型就是与后端 `internal/httpapi/recycle.go` 的契约，修改时两边同步，并
 * 更新 docs/architecture.md 的“回收站接口”与第 12 节。删除数据集、审图版本、
 * 已生成的数据版本与模型（各自的 DELETE 接口）都把它们移入回收站，保留
 * `retention_days` 天后自动永久删除；回收站只能恢复，不能提前永久删除。
 */
import { queryOptions } from "@tanstack/react-query"

import {
  refreshDataVersions,
  type DataVersion,
} from "@/features/data-versions/api"
import {
  datasetKeys,
  type Dataset,
  type DatasetProjectRef,
  type DatasetVersion,
} from "@/features/datasets/api"
import { refreshModels, type Model } from "@/features/models/api"
import { refreshMembership } from "@/features/projects/api"
import { refreshReviews } from "@/features/reviews/api"
import { apiGet, apiSend } from "@/lib/api"
import { queryClient } from "@/lib/query-client"

const BASE = "/api/v1/recycle-bin"

/** 回收站中的一项都有的时间。 */
type Trashed = {
  deleted_at: string
  /** 到期永久删除的时间（后台任务稍后执行，实际可能晚一点）。 */
  purge_at: string
}

export type TrashedDataset = Dataset &
  Trashed & {
    /** 删除时选了同时删除文件夹：永久删除时一并删除。 */
    delete_files: boolean
    /** 恢复后回到的项目（仍在使用的）。 */
    restore_projects: DatasetProjectRef[]
    /** 恢复后回到的审图任务数。 */
    review_count: number
  }

export type TrashedVersion = DatasetVersion &
  Trashed & {
    dataset_name: string
    /** 数据集也在回收站中：先恢复数据集，才能恢复版本。 */
    dataset_deleted: boolean
  }

export type TrashedModel = Model &
  Trashed & {
    /** 所属项目随它在回收站中，恢复模型时一并恢复。 */
    project_deleted: boolean
  }

export type TrashedDataVersion = DataVersion &
  Trashed & {
    /** 所属项目随它在回收站中，恢复数据版本时一并恢复。 */
    project_deleted: boolean
  }

export type RecycleBin = {
  retention_days: number
  datasets: TrashedDataset[]
  versions: TrashedVersion[]
  /** 数据版本（只有已生成的会移入回收站），最近删除的在前。 */
  data_versions: TrashedDataVersion[]
  models: TrashedModel[]
}

export const recycleBinKeys = {
  all: ["recycle-bin"] as const,
}

export function recycleBinQuery() {
  return queryOptions({
    queryKey: recycleBinKeys.all,
    queryFn: ({ signal }) => apiGet<RecycleBin>(BASE, { signal }),
    // 到期的内容由后台任务删除，页面开着时也会消失。
    refetchInterval: 60_000,
  })
}

/**
 * 恢复数据集。名称（或文件夹名）已被占用时 409 `name_taken`，带 `name` 用新名称
 * 恢复。
 */
export function restoreDataset(id: number, name?: string) {
  return apiSend<Dataset>(
    "POST",
    `${BASE}/datasets/${id}/restore`,
    name === undefined ? undefined : { name }
  )
}

/** 恢复审图版本；它的数据集也在回收站中时 409。 */
export function restoreVersion(id: number) {
  return apiSend<DatasetVersion>("POST", `${BASE}/versions/${id}/restore`)
}

/**
 * 恢复模型（项目随它在回收站中时一并恢复）。项目的名称已被占用时 409
 * `name_taken`，带 `projectName` 用新名称恢复项目。
 */
export function restoreModel(id: number, projectName?: string) {
  return apiSend<Model>(
    "POST",
    `${BASE}/models/${id}/restore`,
    projectName === undefined ? undefined : { project_name: projectName }
  )
}

/**
 * 恢复数据版本（项目随它在回收站中时一并恢复）。项目的名称已被占用时 409
 * `name_taken`，带 `projectName` 用新名称恢复项目。定义在数据版本的 API 中
 * （数据版本页面的撤销也用它）。
 */
export { restoreDataVersion } from "@/features/data-versions/api"

/** 删除或恢复之后刷新回收站。 */
export function refreshRecycleBin() {
  void queryClient.invalidateQueries({ queryKey: recycleBinKeys.all })
}

/**
 * 数据集移入或移出回收站之后：数据集与目录列表、项目（数据集所属的项目）、
 * 审图任务（数据集的图片随之离开或回到任务）与回收站。
 */
export function refreshAfterDatasetMove() {
  void queryClient.invalidateQueries({ queryKey: datasetKeys.directories() })
  refreshMembership()
  refreshReviews()
  refreshRecycleBin()
}

/** 审图版本移入或移出回收站之后。 */
export function refreshAfterVersionMove(datasetId: number) {
  void queryClient.invalidateQueries({
    queryKey: datasetKeys.versions(datasetId),
  })
  // 数据集的版本数。
  void queryClient.invalidateQueries({ queryKey: datasetKeys.lists() })
  void queryClient.invalidateQueries({
    queryKey: datasetKeys.detail(datasetId),
    exact: true,
  })
  refreshReviews()
  refreshRecycleBin()
}

/**
 * 模型移入或移出回收站之后：模型与项目（模型数、最新版本；随它恢复的项目）、
 * 审图任务（模型是否可用）与回收站。恢复的项目也带回它的数据集。
 */
export function refreshAfterModelMove() {
  refreshModels()
  refreshMembership()
  refreshReviews()
  refreshRecycleBin()
}

/**
 * 数据版本移入或移出回收站之后：数据版本与项目（数据版本数、最新版本；随它
 * 恢复的项目，也带回它的数据集）与回收站。
 */
export function refreshAfterDataVersionMove() {
  void refreshDataVersions()
  refreshMembership()
  refreshRecycleBin()
}
