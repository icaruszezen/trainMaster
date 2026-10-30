/** 导入 / 扫描任务的展示文案（纯函数，便于测试）。 */
import type {
  DatasetImport,
  ImportSource,
  ImportStatus,
  ImportSummaryKey,
} from "@/features/datasets/api"
import { formatBytes, formatNumber, percentOf } from "@/lib/format"

export const sourceLabels: Record<ImportSource, string> = {
  archive: "压缩包",
  folder: "文件夹",
  scan: "扫描",
}

export const statusLabels: Record<ImportStatus, string> = {
  uploading: "上传中",
  queued: "排队中",
  running: "处理中",
  succeeded: "已完成",
  failed: "失败",
  canceled: "已取消",
}

type JobProgress = Pick<
  DatasetImport,
  | "status"
  | "stage"
  | "progress"
  | "source"
  | "file_size"
  | "received_bytes"
  | "file_count"
  | "received_files"
>

/** 当前阶段的简短描述，如“正在解压”。 */
export function stageLabel(job: Pick<DatasetImport, "status" | "stage">) {
  switch (job.status) {
    case "uploading":
      return "等待上传"
    case "queued":
      return "等待处理"
    case "running":
      switch (job.stage) {
        case "extracting":
          return "正在解压"
        case "importing":
          return "正在导入"
        case "scanning":
          return "正在扫描"
        default:
          return "正在处理"
      }
    default:
      return statusLabels[job.status]
  }
}

/** 进度百分比（0–100）；总量未知时返回 null（显示为不确定进度）。 */
export function jobPercent(job: JobProgress): number | null {
  if (job.status === "uploading") {
    if (job.source === "folder" && job.file_count > 0) {
      return percentOf(job.received_files, job.file_count)
    }
    return job.file_size > 0
      ? percentOf(job.received_bytes, job.file_size)
      : null
  }
  if (job.status === "running" && job.progress.total > 0) {
    return percentOf(job.progress.done, job.progress.total)
  }
  return null
}

/** 带单位的进度明细，如“1,234 / 5,000 个文件”“1.2 GB / 3.4 GB”。 */
export function progressDetail(job: JobProgress): string {
  if (job.status === "uploading") {
    if (job.source === "folder" && job.file_count > 0) {
      return `${formatNumber(job.received_files)} / ${formatNumber(job.file_count)} 个文件`
    }
    return `${formatBytes(job.received_bytes)} / ${formatBytes(job.file_size)}`
  }

  const { done, total, unit } = job.progress
  if (job.status !== "running" || total <= 0) {
    return ""
  }
  switch (unit) {
    case "bytes":
      return `${formatBytes(done)} / ${formatBytes(total)}`
    case "files":
      return `${formatNumber(done)} / ${formatNumber(total)} 个文件`
    case "entries":
      return `${formatNumber(done)} / ${formatNumber(total)} 个条目`
    default:
      return `${formatNumber(done)} / ${formatNumber(total)}`
  }
}

type SummaryPart = [key: ImportSummaryKey, format: (n: number) => string]

const namesInvalidPart: SummaryPart = [
  "names_invalid",
  (n) =>
    `${formatNumber(n)} 个文件名不是 UTF-8 编码，已跳过（请在服务器上重命名）`,
]

const importParts: SummaryPart[] = [
  ["images_added", (n) => `新增 ${formatNumber(n)} 张图片`],
  ["annotations_matched", (n) => `匹配 ${formatNumber(n)} 个标注`],
  ["images_replaced", (n) => `覆盖 ${formatNumber(n)} 张同名图片`],
  ["images_skipped", (n) => `跳过 ${formatNumber(n)} 张同名图片`],
  ["annotations_invalid", (n) => `${formatNumber(n)} 个标注无法解析`],
  ["annotations_orphaned", (n) => `${formatNumber(n)} 个标注没有对应图片`],
  ["files_ignored", (n) => `忽略 ${formatNumber(n)} 个其他文件`],
  ["entries_unsafe", (n) => `${formatNumber(n)} 个不安全路径已跳过`],
  namesInvalidPart,
]

const scanParts: SummaryPart[] = [
  ["images_added", (n) => `新增 ${formatNumber(n)} 张`],
  ["images_updated", (n) => `更新 ${formatNumber(n)} 张`],
  ["images_removed", (n) => `移除 ${formatNumber(n)} 张`],
  ["annotations_matched", (n) => `匹配 ${formatNumber(n)} 个标注`],
  ["annotations_invalid", (n) => `${formatNumber(n)} 个标注无法解析`],
  ["annotations_orphaned", (n) => `${formatNumber(n)} 个标注没有对应图片`],
  ["files_ignored", (n) => `忽略 ${formatNumber(n)} 个其他文件`],
  namesInvalidPart,
]

/**
 * 已完成任务的结果摘要，例如“新增 120 张图片，匹配 118 个标注，跳过 2 张同名图片”。
 * 失败时返回错误原因；其它状态返回空字符串。
 */
export function summaryText(
  job: Pick<DatasetImport, "source" | "status" | "summary" | "error">
): string {
  if (job.status === "failed") {
    return job.error || "导入失败"
  }
  if (job.status !== "succeeded") {
    return job.status === "canceled" ? "已取消" : ""
  }

  const { summary } = job
  const parts: string[] = []
  if (job.source === "scan") {
    parts.push(`共 ${formatNumber(summary.images_total ?? 0)} 张图片`)
  }
  for (const [key, format] of job.source === "scan" ? scanParts : importParts) {
    const value = summary[key] ?? 0
    // 导入时“新增”即使为 0 也写出来，让结果一目了然。
    if (value > 0 || (key === "images_added" && job.source !== "scan")) {
      parts.push(format(value))
    }
  }
  if (job.source === "scan" && parts.length === 1) {
    parts.push("没有变化")
  }
  return parts.join("，")
}

/** 任务耗时（秒）；未开始或未结束时以 `now` 计算到当前。 */
export function jobDurationSeconds(
  job: Pick<DatasetImport, "started_at" | "finished_at" | "created_at">,
  now: number = Date.now()
): number | null {
  const start = Date.parse(job.started_at ?? job.created_at)
  const end = job.finished_at ? Date.parse(job.finished_at) : now
  if (Number.isNaN(start) || Number.isNaN(end)) {
    return null
  }
  return Math.max(0, (end - start) / 1000)
}
