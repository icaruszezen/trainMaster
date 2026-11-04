/** 本地审图程序页面上的文字：打开进度、路径、修改的统计。 */
import type { ReviewCounts } from "@/features/reviews/api"
import type {
  BrowseEntry,
  ExportResult,
  Opening,
  OpenFailure,
  PackageInfo,
  Session,
} from "@/reviewer/api"
import { formatBytes, formatNumber, percentOf } from "@/lib/format"

export const openingStageLabels: Record<Opening["stage"], string> = {
  checking: "正在检查审图包",
  extracting: "正在解压审图包",
  loading: "正在读取图片与标注",
}

/** 打开进度：百分比（总量未知时为 null）与说明。 */
export function openingProgress(opening: Opening): {
  percent: number | null
  detail: string
} {
  if (opening.total <= 0) {
    return { percent: null, detail: "" }
  }
  const percent = percentOf(opening.done, opening.total)
  const detail =
    opening.stage === "extracting"
      ? `${formatBytes(opening.done)} / ${formatBytes(opening.total)}`
      : `${formatNumber(opening.done)} / ${formatNumber(opening.total)} 张`
  return { percent, detail }
}

/** 路径的最后一段（文件或文件夹名），Windows 与 Linux 的分隔符都认。 */
export function baseName(path: string): string {
  const trimmed = path.replace(/[\\/]+$/, "")
  const index = Math.max(trimmed.lastIndexOf("\\"), trimmed.lastIndexOf("/"))
  const name = index >= 0 ? trimmed.slice(index + 1) : trimmed
  return name || path
}

/** 路径所在的文件夹（没有上一级时为空）。 */
export function dirName(path: string): string {
  const trimmed = path.replace(/[\\/]+$/, "")
  const index = Math.max(trimmed.lastIndexOf("\\"), trimmed.lastIndexOf("/"))
  if (index < 0) {
    return ""
  }
  // C:\a → C:\；/a → /
  const parent = trimmed.slice(0, index)
  if (parent === "" || /^[A-Za-z]:$/.test(parent)) {
    return trimmed.slice(0, index + 1)
  }
  return parent
}

/**
 * 输入框中粘贴的路径：去掉首尾的空白和一对包住整个路径的双引号（Windows 资源
 * 管理器的“复制为路径”总是带着它们；Windows 的路径中不能有双引号）。
 */
export function typedPath(text: string): string {
  const trimmed = text.trim()
  const unquoted =
    trimmed.length >= 2 && trimmed.startsWith('"') && trimmed.endsWith('"')
      ? trimmed.slice(1, -1)
      : trimmed
  return unquoted.trim()
}

/** 能打开的项目：审图包文件夹与 zip 文件。 */
export function isOpenable(entry: Pick<BrowseEntry, "kind">) {
  return entry.kind === "package" || entry.kind === "zip"
}

/** 已审核（已审核 + 需加样）的图片数。 */
export function reviewedCount(counts: ReviewCounts) {
  return counts.approved + counts.needs_sample
}

/** 审核进度，如“已审核 12 / 40 张（需加样 3 张）”。 */
export function progressSummary(counts: ReviewCounts) {
  const reviewed = reviewedCount(counts)
  const base = `已审核 ${formatNumber(reviewed)} / ${formatNumber(counts.total)} 张`
  return counts.needs_sample > 0
    ? `${base}（需加样 ${formatNumber(counts.needs_sample)} 张）`
    : base
}

/** 导出的审图结果中有哪些修改。 */
export function exportSummary(result: ExportResult) {
  if (result.status_changes === 0 && result.annotation_changes === 0) {
    return "没有任何修改：上传后平台上的审图任务不会变化。"
  }
  const parts: string[] = []
  if (result.status_changes > 0) {
    parts.push(`${formatNumber(result.status_changes)} 张图片改了审核状态`)
  }
  if (result.annotation_changes > 0) {
    parts.push(`${formatNumber(result.annotation_changes)} 张图片改了标注`)
  }
  return `${parts.join("，")}；${formatNumber(result.unchanged)} 张没有修改。`
}

/**
 * 打开审图包之后：`following` 是等待打开完成的路径。打开完成（没有在打开、
 * 没有这个路径的错误、有打开的审图包）时进入审图；`failure` 是这次打开的错误。
 */
export function openOutcome(
  session: Pick<Session, "opening" | "open_error" | "package">,
  following: string | null
): { done: boolean; failure: OpenFailure | null } {
  if (following === null || session.opening) {
    return { done: false, failure: null }
  }
  if (session.open_error && session.open_error.path === following) {
    return { done: false, failure: session.open_error }
  }
  return { done: session.package !== null, failure: null }
}

/** 打开的是哪个审图包（审图页面据此判断审着的审图包是否被换掉了）。 */
export function packageKey(pkg: Pick<PackageInfo, "package_id" | "path">) {
  return `${pkg.package_id}\n${pkg.path}`
}

/** 窗口 / 标签页的标题。 */
export function pageTitle(pkg: Pick<PackageInfo, "review"> | null) {
  return pkg
    ? `${pkg.review.name} - trainMaster 本地审图`
    : "trainMaster 本地审图"
}
