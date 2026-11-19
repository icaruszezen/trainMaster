/**
 * 模型评估模块的 API 类型、TanStack Query 查询与写操作。
 *
 * 与后端 `internal/evaluation/types.go` 及 `internal/httpapi/evaluations.go` 保持严格契约一致。
 */
import { keepPreviousData, queryOptions } from "@tanstack/react-query"

import type { AnnotationObject } from "@/features/datasets/api"
import { apiGet, apiSend, type ListResponse } from "@/lib/api"
import { queryClient } from "@/lib/query-client"

export type EvaluationStatus =
  | "queued"
  | "running"
  | "succeeded"
  | "failed"
  | "canceled"

export type EvaluationStage =
  | ""
  | "preparing"
  | "downloading"
  | "inferring"
  | "computing"
  | "uploading"

export type EvaluationSourceType = "data_version" | "datasets"

export type ImageMode = "annotated" | "background" | "all"

export type ImageOutcome =
  | "ok_ok"
  | "overkill"
  | "escape"
  | "ng_ng"
  | "failed"

export type BoxMatchKind = "tp" | "fp" | "misclassified" | "fn"

export type SampleErrorType =
  | "all"
  | "any_error"
  | "fn"
  | "fp"
  | "misclassified"
  | "overkill"
  | "escape"
  | "perfect"
  | "failed"

export type SampleSort =
  | "position"
  | "errors_desc"
  | "fn_desc"
  | "fp_desc"
  | "max_fp_conf_desc"

export type CompareMode = "default" | "unified"

export type CompareDiffType =
  | "changed"
  | "improved"
  | "regressed"
  | "fp_reduced"
  | "fp_added"
  | "outcome_changed"
  | "same"
  | "all"

export type EvalModelRef = {
  id: number
  project_id: number
  project_name: string
  version: number
  yolo_version: string
  task: string
  scale: string
  file_name: string
  size_bytes: number
  sha256: string
  image_size?: number | null
  class_names: string[]
}

export type EvalVersionRef = {
  id: number
  name: string
}

export type DatasetSourceInput = {
  dataset_id: number
  include_base?: boolean
  version_ids?: number[]
  image_mode?: ImageMode
  include_empty?: boolean
}

export type SourceInput = {
  type: EvaluationSourceType
  data_version_id?: number
  splits?: string[]
  datasets?: DatasetSourceInput[]
}

export type DataVersionSourceRef = {
  id: number
  project_id: number
  project_name: string
  version: number
  dir_name: string
  shared_local?: boolean | null
  splits: string[]
  image_count: number
  ok_count: number
  ng_count: number
  object_count: number
}

export type DatasetSourceRef = {
  dataset_id: number
  dataset_name: string
  dir_name: string
  include_base: boolean
  versions: EvalVersionRef[]
  image_mode: ImageMode
  include_empty: boolean
  image_count: number
  ok_count: number
  ng_count: number
  object_count: number
}

export type SourceRef = {
  type: EvaluationSourceType
  data_version?: DataVersionSourceRef | null
  datasets?: DatasetSourceRef[] | null
  total_images: number
  ok_images: number
  ng_images: number
  total_objects: number
}

export type EvaluationOptions = {
  conf: number
  iou: number
  nms_iou: number
  imgsz?: [number, number] | null
  batch: number
  max_det: number
  half: boolean
  device: string
  store_min_conf: number
}

export type EvaluationOptionsInput = {
  conf?: number
  iou?: number
  nms_iou?: number
  imgsz?: number[]
  batch?: number
  max_det?: number
  half?: boolean
  device?: string
}

export type EvalClassInfo = {
  name: string
  color: string
  in_model: boolean
  in_gt: boolean
  model_idx?: number | null
  gt_count: number
  gt_images: number
}

export type EvalNodeRef = {
  id: number
  name: string
  hostname: string
}

export type SpeedMetrics = {
  preprocess_ms: number
  inference_ms: number
  postprocess_ms: number
  total_ms: number
  fps: number
  device: string
  batch_size: number
  imgsz: [number, number]
}

export type OKNGMetrics = {
  total_images: number
  ok_images: number
  ng_images: number
  failed_images: number
  tn: number
  overkill: number
  escape: number
  tp: number
  overkill_rate: number
  escape_rate: number
  accuracy: number
  precision: number
  recall: number
  f1: number
}

export type SummaryMetrics = {
  conf: number
  iou: number
  map50: number
  map75: number
  map50_95: number
  precision: number
  recall: number
  f1: number
  best_f1: number
  best_f1_conf: number
  total_images: number
  evaluated_images: number
  failed_images: number
  total_gt: number
  total_pred: number
  tp: number
  fp: number
  fn: number
  misclassified: number
  ok_ng: OKNGMetrics
  speed?: SpeedMetrics | null
  evaluated_class_count: number
  unmatched_model_classes: number
  unmatched_gt_classes: number
}

export type ClassMetrics = {
  name: string
  color: string
  in_model: boolean
  in_gt: boolean
  gt_count: number
  pred_count: number
  tp: number
  fp: number
  fn: number
  precision: number
  recall: number
  f1: number
  ap50?: number | null
  ap75?: number | null
  ap50_95?: number | null
}

export type CurveSeries = {
  name: string
  color?: string
  x: number[]
  y: number[]
}

export type CurvesData = {
  pr: CurveSeries[]
  f1: CurveSeries[]
  precision: CurveSeries[]
  recall: CurveSeries[]
  class_ap50?: Record<string, number>
  class_ap75?: Record<string, number>
  class_ap50_95?: Record<string, number>
  map50: number
  map75: number
  map50_95: number
  best_f1: number
  best_f1_conf: number
  speed?: SpeedMetrics | null
}

export type ConfusionMatrix = {
  labels: string[]
  matrix: number[][]
}

export type Evaluation = {
  id: number
  project_id: number
  project_name: string
  model_id: number | null
  model_exists: boolean
  model: EvalModelRef
  name: string
  description: string
  source_type: EvaluationSourceType
  source_fingerprint: string
  data_version_id: number | null
  data_version_live: boolean
  source: SourceRef
  options: EvaluationOptions
  classes: EvalClassInfo[]
  status: EvaluationStatus
  stage: EvaluationStage
  queue_position: number | null
  workers_online: number
  node: EvalNodeRef | null
  attempts: number
  progress_done: number
  progress_total: number
  eta_seconds: number | null
  summary: SummaryMetrics
  error: string
  error_detail: string
  result: Record<string, unknown> | null
  queued_at: string
  started_at: string | null
  finished_at: string | null
  created_at: string
  updated_at: string
}

export type AnalysisResult = {
  evaluation_id: number
  conf: number
  iou: number
  store_min_conf: number
  summary: SummaryMetrics
  classes: ClassMetrics[]
  confusion: ConfusionMatrix
  ok_ng: OKNGMetrics
  curves: CurvesData
}

export type PredBox = {
  name: string
  class_idx?: number
  xmin: number
  ymin: number
  xmax: number
  ymax: number
  conf: number
}

export type MatchedPredBox = PredBox & {
  match: "tp" | "fp" | "misclassified"
  matched_gt_idx?: number | null
  matched_gt_name?: string
  iou?: number
}

export type MatchedGTBox = AnnotationObject & {
  index: number
  matched: boolean
  match: "tp" | "misclassified" | "fn"
  matched_pred_idx?: number | null
  matched_pred_name?: string
  matched_pred_conf?: number
  iou?: number
}

export type SampleItem = {
  id: number
  position: number
  sample_key: string
  split: string
  dataset_id?: number | null
  dataset_name: string
  dataset_image_id?: number | null
  data_version_image_id?: number | null
  path: string
  file_name: string
  width: number
  height: number
  gt_count: number
  pred_count: number
  tp_count: number
  fp_count: number
  fn_count: number
  misclassified_count: number
  image_outcome: ImageOutcome
  max_fp_conf: number
  inference_error: string
  thumbnail_url: string
  file_url: string
  annotate_url?: string
}

export type SampleDetail = SampleItem & {
  gt_objects: MatchedGTBox[]
  predictions: MatchedPredBox[]
  source_missing: boolean
}

export type LabelCount = {
  name: string
  object_count: number
  image_count: number
}

export type ModelAlignmentPreview = {
  model: EvalModelRef
  classes: EvalClassInfo[]
  matched_count: number
  model_only_count: number
  gt_only_count: number
}

export type PreviewResult = {
  source: SourceRef
  gt_labels: LabelCount[]
  models: ModelAlignmentPreview[]
  warnings: string[]
}

export type PreviewInput = {
  model_ids: number[]
  source: SourceInput
}

export type CreateEvaluationInput = {
  model_ids: number[]
  name?: string
  description?: string
  source: SourceInput
  options?: EvaluationOptionsInput
}

export type UpdateEvaluationInput = {
  name?: string
  description?: string
  default_conf?: number
  default_iou?: number
}

export type EvaluationListFilters = {
  projectId?: number | null
  modelId?: number | null
  dataVersionId?: number | null
  sourceType?: EvaluationSourceType | ""
  status?: EvaluationStatus | ""
  q?: string
}

export type SampleFilters = {
  conf?: number
  iou?: number
  errorType?: SampleErrorType
  className?: string
  gtClass?: string
  predClass?: string
  q?: string
  sort?: SampleSort
  offset?: number
  limit?: number
}

export type CompareItem = {
  evaluation: Evaluation
  conf: number
  iou: number
  summary: SummaryMetrics
  classes: ClassMetrics[]
  ok_ng: OKNGMetrics
  pr_all?: CurveSeries | null
  f1_all?: CurveSeries | null
  confusion: ConfusionMatrix
}

export type CompareResult = {
  mode: CompareMode
  conf?: number | null
  iou?: number | null
  same_source: boolean
  source_fingerprint?: string
  class_names: string[]
  items: CompareItem[]
}

export type CompareDiffItem = {
  sample_key: string
  path: string
  file_name: string
  width: number
  height: number
  gt_count: number
  diff_category: Exclude<CompareDiffType, "changed" | "all">
  base: SampleDetail
  target: SampleDetail
  delta_fn: number
  delta_fp: number
  delta_tp: number
}

export type CompareDiffResult = {
  base_id: number
  target_id: number
  counts: Record<string, number>
  items: CompareDiffItem[]
  total: number
}

export type CompareDiffParams = {
  baseId: number
  targetId: number
  mode?: CompareMode
  conf?: number
  iou?: number
  diffType?: CompareDiffType
  q?: string
  offset?: number
  limit?: number
}

const BASE = "/api/v1/evaluations"

export function isActiveEvaluation(ev: Pick<Evaluation, "status">): boolean {
  return ev.status === "queued" || ev.status === "running"
}

export const evaluationKeys = {
  all: ["evaluations"] as const,
  lists: () => [...evaluationKeys.all, "list"] as const,
  list: (filters: EvaluationListFilters) =>
    [...evaluationKeys.lists(), filters] as const,
  details: () => [...evaluationKeys.all, "detail"] as const,
  detail: (id: number) => [...evaluationKeys.details(), id] as const,
  analysis: (id: number, conf?: number, iou?: number) =>
    [...evaluationKeys.detail(id), "analysis", { conf, iou }] as const,
  samples: (id: number, filters: SampleFilters) =>
    [...evaluationKeys.detail(id), "samples", filters] as const,
  sampleDetail: (id: number, sampleId: number, conf?: number, iou?: number) =>
    [...evaluationKeys.detail(id), "sample", sampleId, { conf, iou }] as const,
  compare: (
    ids: readonly number[],
    mode: CompareMode,
    conf?: number,
    iou?: number
  ) => [...evaluationKeys.all, "compare", { ids, mode, conf, iou }] as const,
  compareDiff: (params: CompareDiffParams) =>
    [...evaluationKeys.all, "compare-diff", params] as const,
}

export function evaluationsQuery(filters: EvaluationListFilters = {}) {
  const params = new URLSearchParams()
  if (filters.projectId) {
    params.set("project_id", String(filters.projectId))
  }
  if (filters.modelId) {
    params.set("model_id", String(filters.modelId))
  }
  if (filters.dataVersionId) {
    params.set("data_version_id", String(filters.dataVersionId))
  }
  if (filters.sourceType) {
    params.set("source_type", filters.sourceType)
  }
  if (filters.status) {
    params.set("status", filters.status)
  }
  if (filters.q?.trim()) {
    params.set("q", filters.q.trim())
  }
  const qs = params.toString()
  return queryOptions({
    queryKey: evaluationKeys.list(filters),
    queryFn: ({ signal }) =>
      apiGet<ListResponse<Evaluation>>(qs ? `${BASE}?${qs}` : BASE, { signal }),
    refetchInterval: (query) =>
      query.state.data?.items.some(isActiveEvaluation) ? 2_500 : false,
  })
}

export function projectEvaluationsQuery(projectId: number) {
  return evaluationsQuery({ projectId })
}

export function modelEvaluationsQuery(modelId: number) {
  return evaluationsQuery({ modelId })
}

export function evaluationQuery(id: number) {
  return queryOptions({
    queryKey: evaluationKeys.detail(id),
    queryFn: ({ signal }) => apiGet<Evaluation>(`${BASE}/${id}`, { signal }),
    refetchInterval: (query) =>
      query.state.data && isActiveEvaluation(query.state.data) ? 2_000 : false,
  })
}

export function evaluationAnalysisQuery(
  id: number,
  conf?: number,
  iou?: number
) {
  const params = new URLSearchParams()
  if (conf !== undefined) {
    params.set("conf", conf.toFixed(4))
  }
  if (iou !== undefined) {
    params.set("iou", iou.toFixed(4))
  }
  const qs = params.toString()
  return queryOptions({
    queryKey: evaluationKeys.analysis(id, conf, iou),
    queryFn: ({ signal }) =>
      apiGet<AnalysisResult>(
        qs ? `${BASE}/${id}/analysis?${qs}` : `${BASE}/${id}/analysis`,
        { signal }
      ),
    placeholderData: keepPreviousData,
  })
}

export function buildSampleQueryParams(filters: SampleFilters): URLSearchParams {
  const params = new URLSearchParams()
  if (filters.conf !== undefined) {
    params.set("conf", filters.conf.toFixed(4))
  }
  if (filters.iou !== undefined) {
    params.set("iou", filters.iou.toFixed(4))
  }
  if (filters.errorType && filters.errorType !== "all") {
    params.set("error_type", filters.errorType)
  }
  if (filters.className) {
    params.set("class", filters.className)
  }
  if (filters.gtClass) {
    params.set("gt_class", filters.gtClass)
  }
  if (filters.predClass) {
    params.set("pred_class", filters.predClass)
  }
  if (filters.q?.trim()) {
    params.set("q", filters.q.trim())
  }
  if (filters.sort && filters.sort !== "position") {
    params.set("sort", filters.sort)
  }
  if (filters.offset && filters.offset > 0) {
    params.set("offset", String(filters.offset))
  }
  if (filters.limit && filters.limit > 0) {
    params.set("limit", String(filters.limit))
  }
  return params
}

export function evaluationSamplesQuery(id: number, filters: SampleFilters) {
  const qs = buildSampleQueryParams(filters).toString()
  return queryOptions({
    queryKey: evaluationKeys.samples(id, filters),
    queryFn: ({ signal }) =>
      apiGet<ListResponse<SampleItem>>(
        qs ? `${BASE}/${id}/images?${qs}` : `${BASE}/${id}/images`,
        { signal }
      ),
    placeholderData: keepPreviousData,
  })
}

export function evaluationSampleDetailQuery(
  id: number,
  sampleId: number,
  conf?: number,
  iou?: number
) {
  const params = new URLSearchParams()
  if (conf !== undefined) {
    params.set("conf", conf.toFixed(4))
  }
  if (iou !== undefined) {
    params.set("iou", iou.toFixed(4))
  }
  const qs = params.toString()
  return queryOptions({
    queryKey: evaluationKeys.sampleDetail(id, sampleId, conf, iou),
    queryFn: ({ signal }) =>
      apiGet<SampleDetail>(
        qs
          ? `${BASE}/${id}/images/${sampleId}?${qs}`
          : `${BASE}/${id}/images/${sampleId}`,
        { signal }
      ),
    placeholderData: keepPreviousData,
  })
}

export function evaluationCompareQuery(
  ids: readonly number[],
  mode: CompareMode,
  conf?: number,
  iou?: number
) {
  const params = new URLSearchParams()
  params.set("ids", ids.join(","))
  params.set("mode", mode)
  if (mode === "unified") {
    if (conf !== undefined) {
      params.set("conf", conf.toFixed(4))
    }
    if (iou !== undefined) {
      params.set("iou", iou.toFixed(4))
    }
  }
  return queryOptions({
    queryKey: evaluationKeys.compare(ids, mode, conf, iou),
    queryFn: ({ signal }) =>
      apiGet<CompareResult>(`${BASE}/compare?${params.toString()}`, { signal }),
    placeholderData: keepPreviousData,
  })
}

export function evaluationCompareDiffQuery(p: CompareDiffParams) {
  const params = new URLSearchParams()
  params.set("base_id", String(p.baseId))
  params.set("target_id", String(p.targetId))
  if (p.mode) {
    params.set("mode", p.mode)
  }
  if (p.mode === "unified") {
    if (p.conf !== undefined) {
      params.set("conf", p.conf.toFixed(4))
    }
    if (p.iou !== undefined) {
      params.set("iou", p.iou.toFixed(4))
    }
  }
  if (p.diffType) {
    params.set("diff_type", p.diffType)
  }
  if (p.q?.trim()) {
    params.set("q", p.q.trim())
  }
  if (p.offset && p.offset > 0) {
    params.set("offset", String(p.offset))
  }
  if (p.limit && p.limit > 0) {
    params.set("limit", String(p.limit))
  }
  return queryOptions({
    queryKey: evaluationKeys.compareDiff(p),
    queryFn: ({ signal }) =>
      apiGet<CompareDiffResult>(`${BASE}/compare/diff?${params.toString()}`, {
        signal,
      }),
    placeholderData: keepPreviousData,
  })
}

export function previewEvaluation(input: PreviewInput, signal?: AbortSignal) {
  return apiSend<PreviewResult>("POST", `${BASE}/preview`, input, { signal })
}

export function createEvaluations(input: CreateEvaluationInput) {
  return apiSend<ListResponse<Evaluation>>("POST", BASE, input)
}

export function updateEvaluation(id: number, input: UpdateEvaluationInput) {
  return apiSend<Evaluation>("PATCH", `${BASE}/${id}`, input)
}

export function cancelEvaluation(id: number) {
  return apiSend<Evaluation>("POST", `${BASE}/${id}/cancel`)
}

export function retryEvaluation(id: number) {
  return apiSend<Evaluation>("POST", `${BASE}/${id}/retry`)
}

export function deleteEvaluation(id: number) {
  return apiSend<void>("DELETE", `${BASE}/${id}`)
}

export function refreshEvaluations(id?: number) {
  void queryClient.invalidateQueries({ queryKey: evaluationKeys.lists() })
  if (id !== undefined) {
    void queryClient.invalidateQueries({ queryKey: evaluationKeys.detail(id) })
  }
}

export type EvaluationExportFormat =
  | "html"
  | "json"
  | "classes_csv"
  | "samples_csv"

export function evaluationExportUrl(
  id: number,
  format: EvaluationExportFormat,
  options?: {
    conf?: number
    iou?: number
    inline?: boolean
    sampleFilters?: SampleFilters
  }
) {
  const params =
    format === "samples_csv" && options?.sampleFilters
      ? buildSampleQueryParams({
          ...options.sampleFilters,
          offset: undefined,
          limit: undefined,
        })
      : new URLSearchParams()
  params.set("format", format)
  if (options?.conf !== undefined) {
    params.set("conf", options.conf.toFixed(4))
  }
  if (options?.iou !== undefined) {
    params.set("iou", options.iou.toFixed(4))
  }
  if (options?.inline) {
    params.set("inline", "true")
  }
  return `${BASE}/${id}/export?${params.toString()}`
}

export function evaluationCompareExportUrl(
  ids: readonly number[],
  format: "html" | "csv",
  options?: {
    mode?: CompareMode
    conf?: number
    iou?: number
    inline?: boolean
  }
) {
  const params = new URLSearchParams()
  params.set("ids", ids.join(","))
  params.set("format", format)
  if (options?.mode) {
    params.set("mode", options.mode)
  }
  if (options?.mode === "unified") {
    if (options.conf !== undefined) {
      params.set("conf", options.conf.toFixed(4))
    }
    if (options.iou !== undefined) {
      params.set("iou", options.iou.toFixed(4))
    }
  }
  if (options?.inline) {
    params.set("inline", "true")
  }
  return `${BASE}/compare/export?${params.toString()}`
}

export function sampleThumbnailUrl(
  sample: Pick<SampleItem, "thumbnail_url">,
  size: "sm" | "lg" = "sm"
): string {
  if (!sample.thumbnail_url) {
    return ""
  }
  const sep = sample.thumbnail_url.includes("?") ? "&" : "?"
  return `${sample.thumbnail_url}${sep}size=${size}`
}
