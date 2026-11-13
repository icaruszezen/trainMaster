/** 训练任务的文案、进度与指标格式化（纯函数）。 */
import { taskLabel, yoloVersionLabel } from "@/features/models/yolo-versions"
import type {
  InitMode,
  TrainingJob,
  TrainingStage,
  TrainingStatus,
} from "@/features/training/api"
import { formatDuration, formatNumber, percentOf } from "@/lib/format"

export const TRAINING_STATUS_LABELS: Record<TrainingStatus, string> = {
  queued: "排队中",
  running: "训练中",
  succeeded: "已完成",
  failed: "失败",
  canceled: "已取消",
}

export const TRAINING_STAGE_LABELS: Record<
  Exclude<TrainingStage, "">,
  string
> = {
  preparing: "准备环境",
  downloading: "准备数据与权重",
  training: "模型训练中",
  uploading: "回传训练产物",
}

export const INIT_MODE_LABELS: Record<InitMode, string> = {
  scratch: "从头训练",
  finetune: "预训练微调",
  resume: "断点续训",
}

export const INIT_MODE_DESCRIPTIONS: Record<InitMode, string> = {
  scratch:
    "选择 YOLO 版本与网络规模，从网络结构配置（.yaml）随机初始化权重开始完整训练，无需外部预训练模型。",
  finetune:
    "从模型仓库中选择已上传或已产出的 .pt 权重作为起点进行迁移微调，收敛更快、小样本效果更好。",
  resume:
    "从含有优化器状态的训练检查点（如 last.pt）恢复中断的训练，继续跑完剩余轮次。",
}

export const TASK_SCOPE_LABELS: Record<string, string> = {
  detect: "检测专用",
  segment: "分割专用",
  pose: "姿态专用",
  obb: "旋转框专用",
  classify: "分类专用",
  depth: "深度专用",
}

export function trainingJobTitle(
  job: Pick<TrainingJob, "id" | "name" | "yolo_version" | "model_scale">
) {
  if (job.name.trim()) {
    return job.name.trim()
  }
  return `训练 #${job.id} · ${yoloVersionLabel(job.yolo_version)}${job.model_scale}`
}

export function trainingArchText(
  job: Pick<TrainingJob, "yolo_version" | "model_scale" | "task">
) {
  const arch = `${yoloVersionLabel(job.yolo_version)}${job.model_scale}`
  return job.task && job.task !== "detect"
    ? `${arch} · ${taskLabel(job.task)}`
    : arch
}

/** 计算训练整体百分比（0–100）；阶段未进入训练循环时返回 null（显示不确定进度条）。 */
export function trainingProgressPercent(
  job: Pick<
    TrainingJob,
    | "status"
    | "stage"
    | "current_epoch"
    | "total_epochs"
    | "batch_done"
    | "batch_total"
  >
): number | null {
  if (job.status === "succeeded") {
    return 100
  }
  if (job.status !== "running") {
    return null
  }
  if (job.stage === "uploading") {
    return 99
  }
  if (job.stage !== "training" || job.total_epochs <= 0) {
    return null
  }
  if (job.current_epoch <= 0) {
    return 0
  }
  const batchFrac =
    job.batch_total > 0
      ? Math.min(1, Math.max(0, job.batch_done / job.batch_total))
      : 0
  const doneEpochs = Math.max(0, job.current_epoch - 1) + batchFrac
  return percentOf(doneEpochs, job.total_epochs)
}

export function trainingQueueText(
  queuePosition: number | null,
  workersOnline: number
) {
  if (workersOnline === 0) {
    return queuePosition !== null
      ? `排队第 ${formatNumber(queuePosition)} 位 · 暂无在线训练节点`
      : "排队中 · 暂无在线训练节点"
  }
  return queuePosition !== null
    ? `排队第 ${formatNumber(queuePosition)} 位`
    : "等待训练节点领取…"
}

export function trainingProgressText(
  job: Pick<
    TrainingJob,
    | "stage"
    | "current_epoch"
    | "total_epochs"
    | "batch_done"
    | "batch_total"
    | "eta_seconds"
  >
) {
  const stageText = job.stage ? TRAINING_STAGE_LABELS[job.stage] : "正在启动…"
  if (job.stage !== "training" || job.total_epochs <= 0) {
    return stageText
  }
  const epochPart = `轮次 ${formatNumber(job.current_epoch)} / ${formatNumber(job.total_epochs)}`
  const batchPart =
    job.batch_total > 0
      ? ` · 批次 ${formatNumber(job.batch_done)}/${formatNumber(job.batch_total)}`
      : ""
  const etaPart =
    job.eta_seconds !== null && job.eta_seconds > 0
      ? ` · 剩余约 ${formatDuration(job.eta_seconds)}`
      : ""
  return `${epochPart}${batchPart}${etaPart}`
}

export function formatMap(value: number | null | undefined): string {
  if (value === undefined || value === null || !Number.isFinite(value)) {
    return "—"
  }
  return `${(value * 100).toFixed(1)}%`
}

export function formatLoss(value: number | null | undefined): string {
  if (value === undefined || value === null || !Number.isFinite(value)) {
    return "—"
  }
  return value.toFixed(4)
}

export function formatLr(value: number | null | undefined): string {
  if (value === undefined || value === null || !Number.isFinite(value)) {
    return "—"
  }
  if (value === 0) {
    return "0"
  }
  return value < 0.001
    ? value.toExponential(2)
    : Number(value.toFixed(6)).toString()
}

export function jobDurationSeconds(
  job: Pick<TrainingJob, "started_at" | "finished_at" | "status">,
  nowMs = Date.now()
): number | null {
  if (!job.started_at) {
    return null
  }
  const start = new Date(job.started_at).getTime()
  if (Number.isNaN(start)) {
    return null
  }
  const end = job.finished_at
    ? new Date(job.finished_at).getTime()
    : job.status === "running"
      ? nowMs
      : NaN
  if (Number.isNaN(end) || end < start) {
    return null
  }
  return Math.floor((end - start) / 1000)
}
