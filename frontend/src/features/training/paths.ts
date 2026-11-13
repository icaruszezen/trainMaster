/** 训练任务页面的地址（与 config/navigation、router 中的一致）。 */
import type { InitMode } from "@/features/training/api"

export const TRAINING_JOBS_PATH = "/training/jobs"

export function trainingJobsPath(projectId: number | null = null) {
  return projectId === null
    ? TRAINING_JOBS_PATH
    : `${TRAINING_JOBS_PATH}?project=${projectId}`
}

export function trainingJobDetailPath(id: number) {
  return `${TRAINING_JOBS_PATH}/${id}`
}

export type NewTrainingJobLinkOptions = {
  projectId?: number | null
  dataVersionId?: number | null
  baseModelId?: number | null
  initMode?: InitMode | null
  /** 复用已有训练任务的配置与全量 YOLO 参数。 */
  from?: number | null
}

export function newTrainingJobPath({
  projectId = null,
  dataVersionId = null,
  baseModelId = null,
  initMode = null,
  from = null,
}: NewTrainingJobLinkOptions = {}) {
  const search = new URLSearchParams()
  if (projectId !== null) {
    search.set("project", String(projectId))
  }
  if (dataVersionId !== null) {
    search.set("data_version", String(dataVersionId))
  }
  if (baseModelId !== null) {
    search.set("base_model", String(baseModelId))
  }
  if (initMode !== null) {
    search.set("init_mode", initMode)
  }
  if (from !== null) {
    search.set("from", String(from))
  }
  const query = search.toString()
  return `${TRAINING_JOBS_PATH}/new${query ? `?${query}` : ""}`
}

export function projectTrainingJobsPath(projectId: number) {
  return `/projects/${projectId}?tab=training-jobs`
}

export function parsePositiveId(value: string | null | undefined) {
  if (!value || !/^[1-9]\d{0,15}$/.test(value)) {
    return null
  }
  const id = Number(value)
  return Number.isSafeInteger(id) ? id : null
}
