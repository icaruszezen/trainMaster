/** 模型信息（从 .pt 中解析出的元数据）与转换状态的显示文字（纯函数，便于测试）。 */
import type {
  ExportStage,
  ModelExport,
  ModelInfo,
  ModelSource,
} from "@/features/models/api"
import { ApiError } from "@/lib/api"

export const sourceLabels: Record<ModelSource, string> = {
  upload: "用户上传",
  training: "平台训练",
}

/** 模型的标题：`<项目名> v<版本>`。 */
export function modelTitle(model: {
  project: { name: string }
  version: number
}) {
  return `${model.project.name} v${model.version}`
}

/** 参数量的简写：`2.59M`、`25.3K`、`1.07B`；未知时为“—”。 */
export function formatParameters(count: number | null) {
  if (count === null || !Number.isFinite(count) || count < 0) {
    return "—"
  }
  const units: [number, string][] = [
    [1e9, "B"],
    [1e6, "M"],
    [1e3, "K"],
  ]
  for (const [size, unit] of units) {
    if (count >= size) {
      const value = count / size
      // 保留 3 位有效数字：2.59M、25.9M、259M。
      return `${Number(value.toPrecision(3))}${unit}`
    }
  }
  return String(count)
}

/** 训练轮数：完成的权重、训练中途的检查点（第几轮），或只知道计划的轮数。 */
export function epochText(info: Pick<ModelInfo, "epoch" | "epochs">) {
  const { epoch, epochs } = info
  if (epoch === -1) {
    return epochs !== null ? `${epochs} 轮（已完成）` : "已完成"
  }
  if (epoch !== null && epoch >= 0) {
    // epoch 从 0 开始，保存时已训练完第 epoch + 1 轮。
    return epochs !== null
      ? `第 ${epoch + 1} / ${epochs} 轮（训练中途保存）`
      : `第 ${epoch + 1} 轮（训练中途保存）`
  }
  return epochs !== null ? `${epochs} 轮` : "—"
}

/** 指标名的后缀：Ultralytics 用括号区分框、掩码与关键点的指标。 */
const metricTargets: Record<string, string> = {
  B: "框",
  M: "掩码",
  P: "关键点",
}

const metricNames: Record<string, string> = {
  precision: "Precision",
  recall: "Recall",
  mAP50: "mAP50",
  "mAP50-95": "mAP50-95",
  accuracy_top1: "Top-1 准确率",
  accuracy_top5: "Top-5 准确率",
  fitness: "综合得分",
}

export type MetricEntry = { key: string; label: string; value: number }

/** 指标键的显示名称，如 `metrics/mAP50(B)` → `mAP50（框）`；不认识的去掉前缀原样显示。 */
export function metricLabel(key: string) {
  const bare = key.replace(/^metrics\//, "")
  const match = /^(.+)\(([A-Z])\)$/.exec(bare)
  const name = match ? match[1] : bare
  const target = match ? metricTargets[match[2]] : undefined
  const label = metricNames[name] ?? name
  if (match && !target) {
    return `${label}（${match[2]}）`
  }
  return target ? `${label}（${target}）` : label
}

/**
 * 主要指标：检测类为框的 mAP50、mAP50-95、Precision、Recall（没有框的指标时
 * 退而用掩码 / 关键点的），分类为 Top-1 / Top-5 准确率。没有的项不返回。
 */
export function summaryMetrics(metrics: Record<string, number>): MetricEntry[] {
  const pick = (key: string, label: string) =>
    key in metrics ? [{ key, label, value: metrics[key] }] : []

  const top = [
    ...pick("metrics/accuracy_top1", "Top-1"),
    ...pick("metrics/accuracy_top5", "Top-5"),
  ]
  if (top.length > 0) {
    return top
  }
  for (const suffix of ["B", "M", "P"]) {
    const entries = [
      ...pick(`metrics/mAP50(${suffix})`, "mAP50"),
      ...pick(`metrics/mAP50-95(${suffix})`, "mAP50-95"),
      ...pick(`metrics/precision(${suffix})`, "P"),
      ...pick(`metrics/recall(${suffix})`, "R"),
    ]
    if (entries.length > 0) {
      return entries
    }
  }
  return []
}

const metricOrder = [
  "mAP50",
  "mAP50-95",
  "precision",
  "recall",
  "accuracy_top1",
  "accuracy_top5",
]

/** 全部指标，按目标（框、掩码、关键点）与常用顺序排列。 */
export function allMetrics(metrics: Record<string, number>): MetricEntry[] {
  const rank = (key: string) => {
    const bare = key.replace(/^metrics\//, "")
    const match = /^(.+)\(([A-Z])\)$/.exec(bare)
    const name = match ? match[1] : bare
    const target = match ? "BMP".indexOf(match[2]) : -1
    const order = metricOrder.indexOf(name)
    return [
      target === -1 ? 3 : target,
      order === -1 ? metricOrder.length : order,
    ]
  }
  return Object.entries(metrics)
    .map(([key, value]) => ({ key, label: metricLabel(key), value }))
    .sort((a, b) => {
      const [ta, oa] = rank(a.key)
      const [tb, ob] = rank(b.key)
      return ta - tb || oa - ob || a.key.localeCompare(b.key)
    })
}

/** 指标值：0–1 的比例保留 3 位小数，其它（如损失）保留 4 位有效数字。 */
export function formatMetric(value: number) {
  if (!Number.isFinite(value)) {
    return "—"
  }
  if (value >= 0 && value <= 1) {
    return value.toFixed(3)
  }
  return String(Number(value.toPrecision(4)))
}

/** 文件中记录的训练时间是 ISO 字符串；无法解析时原样返回，未知时为 null。 */
export function trainedAt(info: Pick<ModelInfo, "trained_at">) {
  if (!info.trained_at) {
    return null
  }
  const time = new Date(info.trained_at)
  return Number.isNaN(time.getTime()) ? info.trained_at : time
}

const stageLabels: Record<Exclude<ExportStage, "">, string> = {
  downloading: "正在下载模型",
  converting: "正在转换",
  uploading: "正在上传结果",
}

/** 转换的当前状态，如“排队中（第 2 个）”“正在转换”“已完成”。 */
export function exportStatusText(
  job: Pick<ModelExport, "status" | "stage" | "queue_position">
) {
  switch (job.status) {
    case "queued":
      return job.queue_position !== null && job.queue_position > 0
        ? `排队中（第 ${job.queue_position} 个）`
        : "排队中"
    case "running":
      return job.stage ? stageLabels[job.stage] : "正在转换"
    case "succeeded":
      return "已完成"
    case "failed":
      return "失败"
  }
}

/**
 * 转换进度对话框显示的内容。转换或模型被删除后查询 404，但缓存中仍是删除前的记录
 * （可能还是“转换中”），所以先看错误：
 * - `deleted`：转换任务被删除；`model_deleted`：模型（或所在的项目）被删除，
 *   `modelGone` 为 null 表示还在确认模型是否存在，这时显示 `loading`；
 * - `loading` / `unavailable`：还没有拿到转换记录（`unavailable` 为出错）；
 * - 其余为转换记录的状态，查询暂时出错（如网络中断）时沿用上一次的记录。
 */
export type ExportProgressView =
  | { kind: "deleted" }
  | { kind: "model_deleted" }
  | { kind: "loading" }
  | { kind: "unavailable"; error: Error }
  | { kind: "active"; job: ModelExport }
  | { kind: "succeeded"; job: ModelExport }
  | { kind: "failed"; job: ModelExport }

export function exportProgressView({
  job,
  error,
  modelGone,
}: {
  job: ModelExport | undefined
  error: Error | null
  /** 转换 404 后模型是否也已不存在；确认中为 null。 */
  modelGone: boolean | null
}): ExportProgressView {
  if (isNotFound(error)) {
    if (modelGone === null) {
      return { kind: "loading" }
    }
    return modelGone ? { kind: "model_deleted" } : { kind: "deleted" }
  }
  if (job === undefined) {
    return error ? { kind: "unavailable", error } : { kind: "loading" }
  }
  switch (job.status) {
    case "succeeded":
      return { kind: "succeeded", job }
    case "failed":
      return { kind: "failed", job }
    default:
      return { kind: "active", job }
  }
}

/** 接口返回 404（资源不存在或已被删除）。 */
export function isNotFound(error: unknown) {
  return error instanceof ApiError && error.status === 404
}

/** 转换的步骤，用于进度显示；排队算第 0 步。 */
export const exportSteps: { stage: Exclude<ExportStage, "">; label: string }[] =
  [
    { stage: "downloading", label: "下载模型" },
    { stage: "converting", label: "转换" },
    { stage: "uploading", label: "上传结果" },
  ]

/** 当前进行到第几步（0 排队，1–3 对应 exportSteps，4 已完成）。 */
export function exportStep(job: Pick<ModelExport, "status" | "stage">) {
  switch (job.status) {
    case "queued":
      return 0
    case "succeeded":
      return exportSteps.length + 1
    case "failed":
      return -1
    case "running": {
      const index = exportSteps.findIndex((step) => step.stage === job.stage)
      return index === -1 ? 1 : index + 1
    }
  }
}

/** 张量形状，如 `[1, 3, 640, 640]` → `1 × 3 × 640 × 640`；未知的维度为“?”。 */
export function shapeText(shape: readonly (number | string | null)[]) {
  if (shape.length === 0) {
    return "标量"
  }
  return shape.map((dim) => (dim === null ? "?" : String(dim))).join(" × ")
}
