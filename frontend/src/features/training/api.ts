/**
 * 训练任务模块的 API 类型、查询与写操作。
 *
 * 与后端 `internal/training`（`types.go`、`params.go`）及 `internal/httpapi/training.go`
 * 保持一致。
 */
import { queryOptions } from "@tanstack/react-query"

import {
  modelKeys,
  type Model,
  type ModelTask,
  type YoloVersion,
} from "@/features/models/api"
import { projectKeys } from "@/features/projects/api"
import { apiGet, apiSend, isPermanentError, type ListResponse } from "@/lib/api"
import { queryClient } from "@/lib/query-client"

export type TrainingStatus =
  "queued" | "running" | "succeeded" | "failed" | "canceled"

export type TrainingStage =
  "" | "preparing" | "downloading" | "training" | "uploading"

export type InitMode = "scratch" | "finetune" | "resume"

export type WeightVariant = "best" | "last"

export type ParamGroupKey = "train" | "hyperparameters" | "augmentation" | "val"

export type ParamType =
  | "int"
  | "nullable_int"
  | "float"
  | "nullable_float"
  | "bool"
  | "nullable_bool"
  | "string"
  | "enum"
  | "imgsz"
  | "batch"
  | "cache"
  | "compile"
  | "quantize"
  | "freeze"
  | "int_list"

export type ModelScale = {
  value: string
  label: string
  description: string
}

export type ParamOption = {
  value: unknown
  label: string
}

export type ParamSpec = {
  key: string
  label: string
  group: ParamGroupKey
  type: ParamType
  default: unknown
  min?: number
  max?: number
  step?: number
  options?: ParamOption[]
  description: string
  task_scope?: string
}

export type ParamGroupMeta = {
  key: ParamGroupKey
  label: string
  description: string
}

export type ParamsSchemaResponse = {
  groups: ParamGroupMeta[]
  specs: ParamSpec[]
  defaults: Record<string, unknown>
  scales: Record<string, ModelScale[]>
}

export type TrainingDataVersionRef = {
  id: number
  version: number
  dir_name: string
  class_names: string[]
  image_count: number
  train_count: number
  val_count: number
  test_count: number
  augmented_count: number
}

export type TrainingBaseModelRef = {
  id: number
  project_id: number
  project_name: string
  version: number
  yolo_version: YoloVersion
  task: ModelTask
  scale: string
  file_name: string
  size_bytes: number
  sha256: string
  training_checkpoint: boolean
}

export type RegisteredModelRef = {
  id: number
  version: number
  yolo_version: YoloVersion
}

export type TrainingNodeRef = {
  id: number
  name: string
  hostname: string
}

export type SummaryMetrics = {
  train_box_loss?: number
  train_cls_loss?: number
  train_dfl_loss?: number
  val_box_loss?: number
  val_cls_loss?: number
  val_dfl_loss?: number
  precision?: number
  recall?: number
  map50?: number
  map50_95?: number
  fitness?: number
  best_fitness?: number
  best_epoch?: number
  best_map50?: number
  best_map50_95?: number
  lr?: number
  gpu_mem_gb?: number
}

export type EpochMetrics = {
  epoch: number
  total_epochs: number
  train_box_loss?: number
  train_cls_loss?: number
  train_dfl_loss?: number
  val_box_loss?: number
  val_cls_loss?: number
  val_dfl_loss?: number
  precision?: number
  recall?: number
  map50?: number
  map50_95?: number
  fitness?: number
  lr?: number
  gpu_mem_gb?: number
  duration_sec?: number
}

export type ArtifactKind = "weight" | "plot" | "csv" | "yaml" | "log"

export type TrainingArtifact = {
  name: string
  rel_path: string
  kind: ArtifactKind
  size_bytes: number
}

export type TrainingRunResult = {
  ultralytics_version?: string
  torch_version?: string
  cuda_available?: boolean
  device?: string
  duration_ms?: number
  node_name?: string
}

export type TrainingJob = {
  id: number
  project_id: number
  project_name: string
  data_version_id: number
  data_version_live: boolean
  data_version: TrainingDataVersionRef
  name: string
  description: string
  init_mode: InitMode
  yolo_version: YoloVersion
  model_scale: string
  task: ModelTask
  base_model_id: number | null
  base_model: TrainingBaseModelRef | null
  params: Record<string, unknown>
  auto_register: boolean
  status: TrainingStatus
  stage: TrainingStage
  queue_position: number | null
  workers_online: number
  node: TrainingNodeRef | null
  attempts: number
  current_epoch: number
  total_epochs: number
  batch_done: number
  batch_total: number
  eta_seconds: number | null
  metrics: SummaryMetrics
  epochs: EpochMetrics[]
  artifacts: TrainingArtifact[]
  best_model: RegisteredModelRef | null
  last_model: RegisteredModelRef | null
  error: string
  error_detail: string
  result: TrainingRunResult | null
  queued_at: string
  started_at: string | null
  finished_at: string | null
  created_at: string
  updated_at: string
}

export type LogSlice = {
  content: string
  offset: number
  next_offset: number
  total_bytes: number
  truncated: boolean
}

export type TrainingJobFilters = {
  projectId: number | null
  dataVersionId: number | null
  status: TrainingStatus | null
  yoloVersion: YoloVersion | null
  q: string
}

export const ALL_TRAINING_JOBS: TrainingJobFilters = {
  projectId: null,
  dataVersionId: null,
  status: null,
  yoloVersion: null,
  q: "",
}

export type CreateTrainingJobInput = {
  project_id: number
  data_version_id: number
  name?: string
  description?: string
  init_mode: InitMode
  yolo_version?: YoloVersion
  model_scale?: string
  base_model_id?: number | null
  params: Record<string, unknown>
  auto_register: boolean
}

export type UpdateTrainingJobInput = {
  name?: string
  description?: string
}

export type RegisterTrainedModelInput = {
  variant: WeightVariant
  description?: string
}

const BASE = "/api/v1/training"
const JOBS_BASE = `${BASE}/jobs`

export const trainingJobApiPath = (id: number) => `${JOBS_BASE}/${id}`

export function trainingArtifactUrl(
  jobId: number,
  relPath: string,
  download = false
) {
  const encoded = relPath
    .split("/")
    .map((segment) => encodeURIComponent(segment))
    .join("/")
  const base = `${trainingJobApiPath(jobId)}/artifacts/${encoded}`
  return download ? `${base}?download=1` : base
}

export function isActiveTrainingJob(
  job: Pick<TrainingJob, "status"> | null | undefined
) {
  return job?.status === "queued" || job?.status === "running"
}

function withQuery(
  path: string,
  params: Record<string, string | number | null | undefined>
) {
  const search = new URLSearchParams()
  for (const [key, value] of Object.entries(params)) {
    if (value !== undefined && value !== null && value !== "") {
      search.set(key, String(value))
    }
  }
  const query = search.toString()
  return query ? `${path}?${query}` : path
}

export const trainingKeys = {
  all: ["training"] as const,
  schema: () => [...trainingKeys.all, "params-schema"] as const,
  lists: () => [...trainingKeys.all, "list"] as const,
  list: (filters: TrainingJobFilters) =>
    [...trainingKeys.lists(), filters] as const,
  project: (projectId: number) =>
    [...trainingKeys.lists(), { projectId }] as const,
  details: () => [...trainingKeys.all, "detail"] as const,
  detail: (id: number) => [...trainingKeys.details(), id] as const,
  logs: (id: number) => [...trainingKeys.detail(id), "logs"] as const,
}

export function trainingParamsSchemaQuery() {
  return queryOptions({
    queryKey: trainingKeys.schema(),
    queryFn: ({ signal }) =>
      apiGet<ParamsSchemaResponse>(`${BASE}/params-schema`, { signal }),
    staleTime: Infinity,
  })
}

export function trainingJobsQuery(
  filters: TrainingJobFilters = ALL_TRAINING_JOBS
) {
  return queryOptions({
    queryKey: trainingKeys.list(filters),
    queryFn: ({ signal }) =>
      apiGet<ListResponse<TrainingJob>>(
        withQuery(JOBS_BASE, {
          project_id: filters.projectId,
          data_version_id: filters.dataVersionId,
          status: filters.status,
          yolo_version: filters.yoloVersion,
          q: filters.q.trim(),
        }),
        { signal }
      ),
    refetchInterval: (query) =>
      !isPermanentError(query.state.error) &&
      query.state.data?.items.some(isActiveTrainingJob)
        ? 3_000
        : false,
  })
}

export function projectTrainingJobsQuery(projectId: number) {
  return trainingJobsQuery({
    ...ALL_TRAINING_JOBS,
    projectId,
  })
}

export function trainingJobQuery(id: number) {
  return queryOptions({
    queryKey: trainingKeys.detail(id),
    queryFn: ({ signal }) =>
      apiGet<TrainingJob>(trainingJobApiPath(id), { signal }),
    refetchInterval: (query) =>
      !isPermanentError(query.state.error) &&
      isActiveTrainingJob(query.state.data)
        ? 2_000
        : false,
  })
}

export function fetchTrainingJobLogs(
  id: number,
  offset = -1,
  limit = 131_072,
  signal?: AbortSignal
) {
  return apiGet<LogSlice>(
    withQuery(`${trainingJobApiPath(id)}/logs`, { offset, limit }),
    { signal }
  )
}

export function createTrainingJob(input: CreateTrainingJobInput) {
  return apiSend<TrainingJob>("POST", JOBS_BASE, input)
}

export function updateTrainingJob(id: number, input: UpdateTrainingJobInput) {
  return apiSend<TrainingJob>("PATCH", trainingJobApiPath(id), input)
}

export function cancelTrainingJob(id: number) {
  return apiSend<TrainingJob>("POST", `${trainingJobApiPath(id)}/cancel`)
}

export function retryTrainingJob(id: number) {
  return apiSend<TrainingJob>("POST", `${trainingJobApiPath(id)}/retry`)
}

export function deleteTrainingJob(id: number) {
  return apiSend<void>("DELETE", trainingJobApiPath(id))
}

export function registerTrainedModel(
  id: number,
  input: RegisterTrainedModelInput
) {
  return apiSend<Model>(
    "POST",
    `${trainingJobApiPath(id)}/register-model`,
    input
  )
}

export function cacheTrainingJob(job: TrainingJob) {
  queryClient.setQueryData(trainingKeys.detail(job.id), job)
  void queryClient.invalidateQueries({ queryKey: trainingKeys.lists() })
}

export function refreshAfterModelRegistered(jobId: number, projectId: number) {
  void queryClient.invalidateQueries({ queryKey: trainingKeys.detail(jobId) })
  void queryClient.invalidateQueries({ queryKey: trainingKeys.lists() })
  void queryClient.invalidateQueries({ queryKey: modelKeys.all })
  void queryClient.invalidateQueries({
    queryKey: projectKeys.detail(projectId),
  })
  void queryClient.invalidateQueries({ queryKey: projectKeys.lists() })
}
