/**
 * 模型评估模块的文案、格式化函数与业务判定逻辑（纯函数，含单元测试）。
 */
import type {
  CompareDiffType,
  Evaluation,
  EvaluationStage,
  EvaluationStatus,
  ImageMode,
  ImageOutcome,
  SampleErrorType,
  SampleSort,
  SourceRef,
} from "@/features/evaluation/api"
import type { Model } from "@/features/models/api"
import { yoloVersionLabel } from "@/features/models/yolo-versions"
import { formatDuration, formatNumber } from "@/lib/format"

export const EVALUATION_STATUS_LABELS: Record<EvaluationStatus, string> = {
  queued: "排队中",
  running: "评估中",
  succeeded: "已完成",
  failed: "失败",
  canceled: "已取消",
}

export const EVALUATION_STAGE_LABELS: Record<EvaluationStage, string> = {
  "": "等待调度",
  preparing: "准备环境",
  downloading: "拉取模型与图片",
  inferring: "批量推理中",
  computing: "计算指标与曲线",
  uploading: "上传评估结果",
}

export const IMAGE_OUTCOME_LABELS: Record<ImageOutcome, string> = {
  ok_ok: "OK 正确",
  overkill: "过杀 (OK→NG)",
  escape: "漏杀 (NG→OK)",
  ng_ng: "NG 检出",
  failed: "推理失败",
}

export const IMAGE_MODE_LABELS: Record<ImageMode, string> = {
  annotated: "仅有标注的图片",
  background: "仅无目标背景图（OK 样本）",
  all: "全部图片（未标注视为 OK）",
}

export const SAMPLE_ERROR_TYPE_LABELS: Record<SampleErrorType, string> = {
  any_error: "全部错误",
  fn: "漏检 (FN)",
  fp: "误检 (FP)",
  misclassified: "误分类",
  overkill: "过杀 (OK→NG)",
  escape: "漏杀 (NG→OK)",
  perfect: "完美预测",
  failed: "推理失败",
  all: "全部样本",
}

export const SAMPLE_SORT_LABELS: Record<SampleSort, string> = {
  errors_desc: "按总错误数降序",
  fn_desc: "按漏检数 (FN) 降序",
  fp_desc: "按误检数 (FP) 降序",
  max_fp_conf_desc: "按最高误检置信度降序",
  position: "按样本原始顺序",
}

export const COMPARE_DIFF_LABELS: Record<CompareDiffType, string> = {
  changed: "全部差异",
  improved: "漏检减少",
  regressed: "新增漏检",
  fp_reduced: "误检减少",
  fp_added: "新增误检",
  outcome_changed: "OK↔NG 翻转",
  same: "完全一致",
  all: "全部样本",
}

export const SPLIT_LABELS: Record<string, string> = {
  val: "验证集 (val)",
  test: "测试集 (test)",
  train: "训练集 (train)",
}

/** 支持目标检测框评估的模型任务集合（分类/语义分割/深度估计不支持）。 */
const SUPPORTED_BOX_TASKS = new Set(["", "detect", "segment", "pose", "obb"])

/** 检查一个模型是否支持发起模型评估；支持时返回 null，否则返回中文原因。 */
export function evaluationModelBlocker(model: Model): string | null {
  if (model.file_missing) {
    return "模型文件缺失，无法评估"
  }
  if (!SUPPORTED_BOX_TASKS.has(model.task)) {
    return `当前评估仅支持输出检测框的模型（该模型任务为 ${model.task}）`
  }
  return null
}

/** 格式化 0–1 比例为百分比，如 0.9125 → `91.3%`。 */
export function formatMetricPercent(
  value: number | null | undefined,
  digits = 1
): string {
  if (value === null || value === undefined || !Number.isFinite(value)) {
    return "—"
  }
  return `${(value * 100).toFixed(digits)}%`
}

/** 格式化差值（百分比点），如 +0.024 → `+2.4%`，-0.015 → `-1.5%`。 */
export function formatMetricDelta(
  delta: number | null | undefined,
  digits = 1
): string {
  if (delta === null || delta === undefined || !Number.isFinite(delta)) {
    return "—"
  }
  const pct = delta * 100
  if (Math.abs(pct) < 0.05) {
    return "0.0%"
  }
  return `${pct > 0 ? "+" : ""}${pct.toFixed(digits)}%`
}

/** 格式化阈值（0.00–1.00）。 */
export function formatThreshold(value: number): string {
  if (!Number.isFinite(value)) {
    return "—"
  }
  return value.toFixed(2)
}

/** 格式化毫秒延迟。 */
export function formatMs(value: number | null | undefined): string {
  if (value === null || value === undefined || !Number.isFinite(value) || value <= 0) {
    return "—"
  }
  return value >= 100 ? `${value.toFixed(0)} ms` : `${value.toFixed(1)} ms`
}

/** 格式化 FPS 吞吐。 */
export function formatFps(value: number | null | undefined): string {
  if (value === null || value === undefined || !Number.isFinite(value) || value <= 0) {
    return "—"
  }
  return `${value.toFixed(1)} FPS`
}

/** 评估任务的展示标题：自定义名称优先，否则“模型 vN · 评估 #ID”。 */
export function evaluationTitle(ev: Pick<Evaluation, "id" | "name" | "model">): string {
  if (ev.name?.trim()) {
    return ev.name.trim()
  }
  const prefix = ev.model.project_name
    ? `${ev.model.project_name} v${ev.model.version}`
    : `模型 v${ev.model.version}`
  return `${prefix} · 评估 #${ev.id}`
}

/** 模型标签文本，如 `v3 · YOLO11m`。 */
export function evaluationModelLabel(ev: Pick<Evaluation, "model">): string {
  const ver = yoloVersionLabel(ev.model.yolo_version)
  const scale = ev.model.scale ? ev.model.scale : ""
  return `v${ev.model.version} · ${ver}${scale}`
}

/** 评估数据源的简短摘要。 */
export function evaluationSourceSummary(source: SourceRef): string {
  if (source.type === "data_version" && source.data_version) {
    const dv = source.data_version
    const splits = dv.splits?.length ? dv.splits.join("/") : "val"
    return `数据版本 v${dv.version} (${splits})`
  }
  const list = source.datasets ?? []
  if (list.length === 0) {
    return "数据集"
  }
  if (list.length === 1) {
    return `数据集：${list[0].dataset_name}`
  }
  return `${list[0].dataset_name} 等 ${list.length} 个数据集`
}

/** 评估进度百分比（0–100）。 */
export function evaluationProgressPercent(
  ev: Pick<Evaluation, "status" | "progress_done" | "progress_total">
): number {
  if (ev.status === "succeeded") {
    return 100
  }
  if (ev.progress_total <= 0) {
    return 0
  }
  return Math.max(
    0,
    Math.min(100, Math.round((ev.progress_done / ev.progress_total) * 100))
  )
}

/** 评估实时进度文案。 */
export function evaluationProgressText(ev: Evaluation): string {
  if (ev.status === "queued") {
    if (ev.workers_online === 0) {
      return "排队中（当前无在线训练节点）"
    }
    if (ev.queue_position && ev.queue_position > 0) {
      return `排队中（第 ${ev.queue_position} 位）`
    }
    return "排队中，等待训练节点领取"
  }
  const stage = EVALUATION_STAGE_LABELS[ev.stage] || "评估中"
  const parts: string[] = [stage]
  if (ev.progress_total > 0) {
    parts.push(
      `${formatNumber(ev.progress_done)} / ${formatNumber(ev.progress_total)} 张`
    )
  }
  if (ev.eta_seconds !== null && ev.eta_seconds > 0) {
    parts.push(`预计剩余 ${formatDuration(ev.eta_seconds)}`)
  }
  return parts.join(" · ")
}

/** 评估耗时（秒）。 */
export function evaluationDurationSeconds(
  ev: Pick<Evaluation, "started_at" | "finished_at">
): number | null {
  if (!ev.started_at) {
    return null
  }
  const start = Date.parse(ev.started_at)
  const end = ev.finished_at ? Date.parse(ev.finished_at) : Date.now()
  if (Number.isNaN(start) || Number.isNaN(end) || end < start) {
    return null
  }
  return Math.round((end - start) / 1000)
}

/** 按类别或对比序位生成稳定易区分的调色板。 */
export const SERIES_PALETTE = [
  "#2563eb", // blue
  "#10b981", // emerald
  "#f59e0b", // amber
  "#8b5cf6", // violet
  "#ef4444", // red
  "#06b6d4", // cyan
  "#ec4899", // pink
  "#84cc16", // lime
  "#f97316", // orange
  "#14b8a6", // teal
] as const

export function colorForIndex(index: number, preferred?: string): string {
  if (preferred && /^#[0-9a-fA-F]{3,8}$/.test(preferred)) {
    return preferred
  }
  return SERIES_PALETTE[Math.abs(index) % SERIES_PALETTE.length]
}
