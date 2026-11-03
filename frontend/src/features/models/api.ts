/**
 * 模型仓库的 API 类型、查询与写操作。
 *
 * 这里的类型就是与后端 `internal/model`（`types.go`）、`internal/httpapi/models.go`
 * 的契约（字段名、取值），修改时两边同步，并更新 docs/architecture.md 的
 * “模型仓库接口”。约定：未知的数值为 `null`，未知的字符串为 `""`，数组与对象
 * 字段永远存在。
 */
import { queryOptions } from "@tanstack/react-query"

import { projectKeys, projectPath } from "@/features/projects/api"
import { apiGet, apiSend, apiUpload, type ListResponse } from "@/lib/api"
import { queryClient } from "@/lib/query-client"

/** 支持的 YOLO 版本（Ultralytics 训练的模型），说明见 yolo-versions.ts。 */
export type YoloVersion =
  "yolov8" | "yolov9" | "yolov10" | "yolo11" | "yolo12" | "yolo26"

/** 模型的来源：用户上传，或平台上的训练任务产出（训练任务上线后使用）。 */
export type ModelSource = "upload" | "training"

/**
 * 模型的任务，来自模型文件；`""` 表示无法识别。以后的 Ultralytics 可能有新的
 * 任务，界面对不认识的值原样显示。
 */
export type ModelTask =
  "detect" | "segment" | "classify" | "pose" | "obb" | "semantic" | "depth" | ""

/** 识别 YOLO 版本的依据：模型的 yaml 文件名，或网络结构中的模块。 */
export type VersionSource = "yaml_file" | "architecture" | ""

/** 上传时从 .pt 文件中解析出的元数据（服务端只读解析，不执行文件中的代码）。 */
export type ModelInfo = {
  task: ModelTask
  /** pickle 中模型对象的类名，如 `DetectionModel`。 */
  model_class: string
  /** 最后一层（检测头）的类名，如 `Detect`、`v10Detect`、`Segment`。 */
  head: string
  /** 模型的 yaml 文件名，如 `yolo11n.yaml`。 */
  yaml_file: string
  /** 规模：n / s / m / l / x 等，`""` 未知。 */
  scale: string
  /** 文件中识别出的版本；无法识别时为 null。 */
  detected_yolo_version: YoloVersion | null
  version_source: VersionSource
  /** 下标即类别编号。 */
  class_names: string[]
  /** 训练时的输入尺寸（train_args.imgsz）。 */
  image_size: number | null
  channels: number
  parameters: number | null
  /** 带端到端（one-to-one，NMS-free）检测头。 */
  end2end: boolean
  /** 保存时的 Ultralytics 版本。 */
  ultralytics_version: string
  /** 文件中记录的保存时间（ISO 字符串，原样）。 */
  trained_at: string
  epochs: number | null
  /** -1 表示训练完成后的最终权重；否则是保存时所在的轮次（从 0 开始）。 */
  epoch: number | null
  best_fitness: number | null
  /** train_metrics 中的数值，键原样，如 `metrics/mAP50(B)`。 */
  metrics: Record<string, number>
  /** 训练使用的数据集配置（train_args.data）。 */
  train_data: string
  /** 训练的起点（train_args.model），如 `yolo11n.pt`。 */
  base_model: string
  /** 含优化器状态（last.pt 这类可以继续训练的检查点）。 */
  training_checkpoint: boolean
}

export type ModelProjectRef = { id: number; name: string }

/** 模型仓库中的一个模型：绑定一个项目，在项目内按版本号迭代。 */
export type Model = {
  id: number
  project: ModelProjectRef
  /** 项目内的版本号 v1、v2…，删除后不会再分配。 */
  version: number
  description: string
  source: ModelSource
  /** `source` 为 `training` 时：产出它的训练任务。 */
  training_job_id: number | null
  /** 上传时选择（并经文件校验）的版本。 */
  yolo_version: YoloVersion
  task: ModelTask
  /** 上传时的原始文件名，如 `best.pt`。 */
  file_name: string
  size_bytes: number
  sha256: string
  info: ModelInfo
  /** 服务器上的模型文件已不存在。 */
  file_missing: boolean
  /** 已成功的 ONNX 转换数（缓存在服务器上，可直接下载）。 */
  export_count: number
  /** 下载 PT（同源，服务端带 Content-Disposition）。 */
  download_url: string
  created_at: string
  updated_at: string
}

/**
 * 上传任务：uploading（分块上传中；提交时版本不一致也保持该状态）→
 * completed / failed（文件不是可用的模型）/ canceled。
 */
export type ModelUploadStatus =
  "uploading" | "completed" | "failed" | "canceled"

export type ModelUpload = {
  id: number
  project_id: number
  yolo_version: YoloVersion
  description: string
  file_name: string
  file_size: number
  received_bytes: number
  status: ModelUploadStatus
  /** 提交被拒绝时文件中识别出的版本（前端据此提示改选）。 */
  detected_yolo_version: YoloVersion | null
  /** 失败原因（中文），否则为 `""`。 */
  error: string
  /** completed：创建的模型（模型被删除后为 null）。 */
  model_id: number | null
  created_at: string
  updated_at: string
}

/**
 * ONNX 的后处理方式：`raw` 原始输出（需要自行 NMS）、`nms` 导出时嵌入 NMS、
 * `end2end` 端到端（NMS-free）检测头。说明见 yolo-versions.ts。
 */
export type Postprocess = "raw" | "nms" | "end2end"

/** 规范化后的 ONNX 转换选项（服务端补齐默认值后返回）。 */
export type ExportOptions = {
  /** [高, 宽]，每个 32–4096 且是 32 的倍数。 */
  imgsz: [number, number]
  /** 动态输入（batch、高、宽）。 */
  dynamic: boolean
  postprocess: Postprocess
  /** null：由 Ultralytics 自动选择；否则 11–21。 */
  opset: number | null
}

export type ExportFormat = "onnx"

/** queued → running → succeeded / failed（失败或文件丢失后再次请求时重新排队）。 */
export type ExportStatus = "queued" | "running" | "succeeded" | "failed"

/** running 时训练节点上报的阶段。 */
export type ExportStage = "" | "downloading" | "converting" | "uploading"

/** ONNX 图的输入 / 输出；动态维度为字符串（如 `"batch"`），未知为 null。 */
export type TensorInfo = {
  name: string
  shape: (number | string | null)[]
  type: string
}

/** 训练节点上报的转换信息（服务端清洗过；未知的值为 null 或 `""`）。 */
export type ExportResult = {
  ultralytics_version: string
  torch_version: string
  onnx_version: string
  onnxruntime_version: string
  opset: number | null
  end2end: boolean | null
  nms: boolean | null
  dynamic: boolean | null
  inputs: TensorInfo[]
  outputs: TensorInfo[]
  /** 用 onnxruntime 试运行过一次。 */
  verified: boolean | null
  duration_ms: number | null
  node_name: string
}

/** 模型的一次格式转换（同时是转换结果的缓存）。 */
export type ModelExport = {
  id: number
  model_id: number
  format: ExportFormat
  options: ExportOptions
  status: ExportStatus
  stage: ExportStage
  /** 被训练节点领取的次数（节点失联或暂时性错误会重新排队，最多 3 次）。 */
  attempts: number
  /** 领取（或完成）它的训练节点。 */
  node: { id: number; name: string } | null
  /** queued：在队列中的位置（从 1 开始）；否则为 null。 */
  queue_position: number | null
  /** 最近 60 秒内来领取过转换任务的训练节点数；为 0 时排队的任务不会被执行。 */
  workers_online: number
  /** 下载时的文件名（不是服务器上的路径）。 */
  file_name: string
  size_bytes: number | null
  sha256: string
  /** succeeded：节点上报的转换信息。 */
  result: ExportResult | null
  /** failed：中文说明。 */
  error: string
  /** failed：训练节点上的错误输出（截断）。 */
  error_detail: string
  /** succeeded：下载地址。 */
  download_url: string | null
  created_at: string
  updated_at: string
  started_at: string | null
  finished_at: string | null
}

/** 删除时记录已删除、但文件夹没能完全删除的结果（200）；全部删除时为 204。 */
export type DeleteModelResult = { files_removed: boolean; message: string }

/** 模型列表的筛选条件；`null` / `""` 表示不限。 */
export type ModelFilters = {
  projectId: number | null
  yoloVersion: YoloVersion | null
  source: ModelSource | null
  /** 说明 / 原始文件名 / 项目名包含（不区分大小写）。 */
  q: string
}

export const ALL_MODELS: ModelFilters = {
  projectId: null,
  yoloVersion: null,
  source: null,
  q: "",
}

const BASE = "/api/v1/models"

export const modelPath = (id: number) => `${BASE}/${id}`
export const modelUploadPath = (id: number) => `${BASE}/uploads/${id}`
export const modelExportPath = (modelId: number, exportId: number) =>
  `${modelPath(modelId)}/exports/${exportId}`

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

/**
 * 查询键。`detail(id)` 是该模型下所有查询（转换记录）的前缀；项目中的模型也在
 * `all` 之下，模型增删后整体失效即可。
 */
export const modelKeys = {
  all: ["models"] as const,
  lists: () => [...modelKeys.all, "list"] as const,
  list: (filters: ModelFilters) => [...modelKeys.lists(), filters] as const,
  project: (projectId: number) =>
    [...modelKeys.all, "project", projectId] as const,
  details: () => [...modelKeys.all, "detail"] as const,
  detail: (id: number) => [...modelKeys.details(), id] as const,
  exports: (id: number) => [...modelKeys.detail(id), "exports"] as const,
  export: (id: number, exportId: number) =>
    [...modelKeys.detail(id), "export", exportId] as const,
}

/** 模型列表，新的在前，可按项目、YOLO 版本、来源与关键字筛选。 */
export function modelsQuery(filters: ModelFilters = ALL_MODELS) {
  return queryOptions({
    queryKey: modelKeys.list(filters),
    queryFn: ({ signal }) =>
      apiGet<ListResponse<Model>>(
        withQuery(BASE, {
          project_id: filters.projectId ?? "",
          yolo_version: filters.yoloVersion ?? "",
          source: filters.source ?? "",
          q: filters.q,
        }),
        { signal }
      ),
  })
}

/** 项目中的模型，版本号大的在前；项目不存在时 404。 */
export function projectModelsQuery(projectId: number) {
  return queryOptions({
    queryKey: modelKeys.project(projectId),
    queryFn: ({ signal }) =>
      apiGet<ListResponse<Model>>(`${projectPath(projectId)}/models`, {
        signal,
      }),
  })
}

export function modelQuery(id: number) {
  return queryOptions({
    queryKey: modelKeys.detail(id),
    queryFn: ({ signal }) => apiGet<Model>(modelPath(id), { signal }),
  })
}

const ACTIVE_EXPORTS: ReadonlySet<ExportStatus> = new Set(["queued", "running"])

export function isActiveExport(job: Pick<ModelExport, "status">) {
  return ACTIVE_EXPORTS.has(job.status)
}

/** 模型的转换记录（新的在前）；有转换在进行时每 2 秒刷新。 */
export function modelExportsQuery(modelId: number) {
  return queryOptions({
    queryKey: modelKeys.exports(modelId),
    queryFn: ({ signal }) =>
      apiGet<ListResponse<ModelExport>>(`${modelPath(modelId)}/exports`, {
        signal,
      }),
    refetchInterval: (query) =>
      query.state.data?.items.some(isActiveExport) ? 2_000 : false,
  })
}

/** 单个转换；排队或转换中时每秒轮询，结束后停止。 */
export function modelExportQuery(modelId: number, exportId: number) {
  return queryOptions({
    queryKey: modelKeys.export(modelId, exportId),
    queryFn: ({ signal }) =>
      apiGet<ModelExport>(modelExportPath(modelId, exportId), { signal }),
    refetchInterval: (query) => {
      const job = query.state.data
      return !job || isActiveExport(job) ? 1_000 : false
    },
    staleTime: 0,
  })
}

/** 修改说明（规则同项目描述：最多 500 个字符）。 */
export function updateModel(id: number, input: { description: string }) {
  return apiSend<Model>("PATCH", modelPath(id), input)
}

/** 删除模型记录、转换结果与模型文件夹。 */
export function deleteModel(id: number) {
  return apiSend<DeleteModelResult | undefined>("DELETE", modelPath(id))
}

export type CreateModelUploadInput = {
  project_id: number
  yolo_version: YoloVersion
  /** 以 `.pt` 结尾（不区分大小写），最多 255 个字符。 */
  file_name: string
  /** 1 字节–8 GiB。 */
  file_size: number
  description?: string
}

/** 创建上传任务；服务端预留磁盘空间不足时 507。 */
export function createModelUpload(input: CreateModelUploadInput) {
  return apiSend<ModelUpload>("POST", `${BASE}/uploads`, input)
}

export function getModelUpload(id: number, init?: RequestInit) {
  return apiGet<ModelUpload>(modelUploadPath(id), init)
}

/**
 * 上传一个分块（原始字节，最多 64 MiB）。`offset` 必须等于服务端已接收的字节数，
 * 否则 409 `upload_offset_mismatch`；失败的分块不留数据，可以从断点续传。
 */
export function uploadModelChunk(
  id: number,
  offset: number,
  chunk: Blob,
  options: {
    signal?: AbortSignal
    onProgress?: (loaded: number, total: number) => void
  } = {}
) {
  return apiUpload<ModelUpload>(
    "PUT",
    `${modelUploadPath(id)}?offset=${offset}`,
    chunk,
    { ...options, contentType: "application/octet-stream" }
  )
}

/**
 * 提交上传：服务端校验文件、识别模型并分配版本号，返回创建的模型（重复提交
 * 已完成的上传返回同一个模型）。文件不是可用的 YOLO 模型时 422 `invalid_model`；
 * 文件中的版本与所选不同时 422 `yolo_version_mismatch`（上传保持 uploading，
 * 可以带新的 `yolo_version` 再次提交）。
 */
export function commitModelUpload(
  id: number,
  input: { yolo_version?: YoloVersion; description?: string } = {},
  init?: RequestInit
) {
  return apiSend<Model>("POST", `${modelUploadPath(id)}/commit`, input, init)
}

/** 取消上传并删除临时文件；已结束的上传原样返回。 */
export function cancelModelUpload(id: number) {
  return apiSend<ModelUpload>("POST", `${modelUploadPath(id)}/cancel`)
}

export type RequestExportInput = {
  format: ExportFormat
  /** 省略时为训练尺寸（没有时 640）；单个整数表示正方形。 */
  imgsz?: number | [number, number]
  dynamic?: boolean
  /** 省略时按模型的默认值（见 yolo-versions.ts 的 allowedPostprocess）。 */
  postprocess?: Postprocess
  opset?: number | null
}

/**
 * 请求转换。同样的选项已成功且文件还在：200，直接下载；正在排队 / 转换：202
 * 该记录；失败过：重新排队，202；新选项：新记录，202。选项不合法 422，模型文件
 * 缺失 409。
 */
export function requestModelExport(modelId: number, input: RequestExportInput) {
  return apiSend<ModelExport>("POST", `${modelPath(modelId)}/exports`, input)
}

/** 删除转换（排队 / 转换中的也可以删除，训练节点随后放弃）。 */
export function deleteModelExport(modelId: number, exportId: number) {
  return apiSend("DELETE", modelExportPath(modelId, exportId))
}

/**
 * 模型增删后刷新：模型查询，以及项目（模型数与最新版本显示在项目卡片、
 * 列表与详情上）。
 */
export function refreshModels() {
  void queryClient.invalidateQueries({ queryKey: modelKeys.all })
  void queryClient.invalidateQueries({ queryKey: projectKeys.all })
}

/** 转换完成或被删除后刷新：该模型的转换记录、详情与列表中的转换数。 */
export function refreshModelExports(modelId: number) {
  void queryClient.invalidateQueries({ queryKey: modelKeys.detail(modelId) })
  void queryClient.invalidateQueries({ queryKey: modelKeys.lists() })
  void queryClient.invalidateQueries({
    queryKey: [...modelKeys.all, "project"],
  })
}
