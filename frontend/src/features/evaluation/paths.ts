/**
 * 模型评估模块的路由地址与参数解析。
 */

export const EVALUATIONS_PATH = "/evaluation"

export function evaluationDetailPath(
  id: number,
  options?: { tab?: string; conf?: number; iou?: number }
) {
  const params = new URLSearchParams()
  if (options?.tab && options.tab !== "overview") {
    params.set("tab", options.tab)
  }
  if (options?.conf !== undefined) {
    params.set("conf", options.conf.toFixed(2))
  }
  if (options?.iou !== undefined) {
    params.set("iou", options.iou.toFixed(2))
  }
  const qs = params.toString()
  return qs ? `${EVALUATIONS_PATH}/${id}?${qs}` : `${EVALUATIONS_PATH}/${id}`
}

export function evaluationsListPath(options?: {
  projectId?: number
  modelId?: number
  dataVersionId?: number
}) {
  const params = new URLSearchParams()
  if (options?.projectId) {
    params.set("project", String(options.projectId))
  }
  if (options?.modelId) {
    params.set("model", String(options.modelId))
  }
  if (options?.dataVersionId) {
    params.set("data_version", String(options.dataVersionId))
  }
  const qs = params.toString()
  return qs ? `${EVALUATIONS_PATH}?${qs}` : EVALUATIONS_PATH
}

export function newEvaluationPath(options?: {
  projectId?: number
  modelId?: number
  dataVersionId?: number
  datasetId?: number
}) {
  const params = new URLSearchParams()
  if (options?.projectId) {
    params.set("project_id", String(options.projectId))
  }
  if (options?.modelId) {
    params.set("model_id", String(options.modelId))
  }
  if (options?.dataVersionId) {
    params.set("data_version_id", String(options.dataVersionId))
  }
  if (options?.datasetId) {
    params.set("dataset_id", String(options.datasetId))
  }
  const qs = params.toString()
  return qs ? `${EVALUATIONS_PATH}/new?${qs}` : `${EVALUATIONS_PATH}/new`
}

export function evaluationComparePath(
  ids: readonly number[],
  options?: { mode?: "default" | "unified"; conf?: number; iou?: number }
) {
  const params = new URLSearchParams()
  if (ids.length > 0) {
    params.set("ids", ids.join(","))
  }
  if (options?.mode === "unified") {
    params.set("mode", "unified")
    if (options.conf !== undefined) {
      params.set("conf", options.conf.toFixed(2))
    }
    if (options.iou !== undefined) {
      params.set("iou", options.iou.toFixed(2))
    }
  }
  const qs = params.toString()
  return qs
    ? `${EVALUATIONS_PATH}/compare?${qs}`
    : `${EVALUATIONS_PATH}/compare`
}

/** 路径参数必须是正整数（与后端一致），否则返回 null。 */
export function parsePositiveId(value: string | undefined | null): number | null {
  if (!value || !/^[1-9]\d{0,15}$/.test(value)) {
    return null
  }
  const id = Number(value)
  return Number.isSafeInteger(id) ? id : null
}

/** 解析逗号分隔的正整数 ID 列表（去重、保序、最多 6 个）。 */
export function parseCompareIds(value: string | null): number[] {
  if (!value) {
    return []
  }
  const out: number[] = []
  const seen = new Set<number>()
  for (const part of value.split(",")) {
    const id = parsePositiveId(part.trim())
    if (id !== null && !seen.has(id)) {
      seen.add(id)
      out.push(id)
      if (out.length >= 6) {
        break
      }
    }
  }
  return out
}
