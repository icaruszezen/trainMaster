/**
 * 审图版本的显示与下载时的版本选择（与后端 internal/dataset/versions.go 的
 * 规则一致：每张图片用所选版本中最新的、包含它的版本的标注，没有时用基础
 * 版本；不选基础版本时只导出最新的那个版本中标为需加样的图片）。
 */
import type {
  DatasetVersion,
  ExportPreview,
  VersionSelection,
} from "@/features/datasets/api"
import { reviewModelTitle } from "@/features/reviews/review-format"
import { formatNumber } from "@/lib/format"

/**
 * 下载的方式：只有基础版本（与没有版本时一样）、审图版本覆盖基础版本、只有
 * 审图版本（需加样的图片），或什么都没选。
 */
export type SelectionMode = "base" | "overlay" | "versions" | "none"

export function selectionMode(selection: VersionSelection): SelectionMode {
  if (selection.version_ids.length === 0) {
    return selection.include_base ? "base" : "none"
  }
  return selection.include_base ? "overlay" : "versions"
}

/** 只有基础版本的选择（下载请求中省略版本，与以前一样）。 */
export const BASE_ONLY: VersionSelection = {
  include_base: true,
  version_ids: [],
}

/** 规范化的选择：版本 ID 去重、升序（用作查询键）。 */
export function versionSelection(
  includeBase: boolean,
  versionIds: Iterable<number>
): VersionSelection {
  return {
    include_base: includeBase,
    version_ids: [...new Set(versionIds)].sort((a, b) => a - b),
  }
}

/** 下载请求中的版本字段；只有基础版本时省略。 */
export function selectionInput(
  selection: VersionSelection
): Partial<VersionSelection> {
  return selectionMode(selection) === "base" ? {} : selection
}

/** 各种下载方式的说明。 */
export function selectionModeText(mode: SelectionMode) {
  switch (mode) {
    case "base":
      return "只导出基础版本：数据集的全部图片与它们自己的标注，不含审图版本的标注。"
    case "overlay":
      return "导出数据集的全部图片：所选审图版本中有的图片用版本中的标注（多个版本中都有时以新版本为准），其他图片用基础版本的标注。"
    case "versions":
      return "只导出所选审图版本中标为需加样的图片与它们的审图标注（多个版本中都有的图片以新版本为准），不含基础版本的标注。"
    case "none":
      return "请至少选择一个版本。"
  }
}

/** 版本的名称：正在生成的版本还没有编号。 */
export function versionName(version: Pick<DatasetVersion, "name" | "status">) {
  return version.name || "正在生成的版本"
}

/** 版本中的图片，如“12 张图片（需加样 10、已审核 2），35 个目标”。 */
export function versionCountsText(
  version: Pick<
    DatasetVersion,
    "image_count" | "needs_sample_count" | "approved_count" | "object_count"
  >
) {
  const pending =
    version.image_count - version.needs_sample_count - version.approved_count
  const parts = [
    version.needs_sample_count > 0
      ? `需加样 ${formatNumber(version.needs_sample_count)}`
      : null,
    version.approved_count > 0
      ? `已审核 ${formatNumber(version.approved_count)}`
      : null,
    pending > 0 ? `待审核 ${formatNumber(pending)}` : null,
  ].filter((part) => part !== null)
  return `${formatNumber(version.image_count)} 张图片${parts.length > 0 ? `（${parts.join("、")}）` : ""}，${formatNumber(version.object_count)} 个目标`
}

/** 版本来自哪里，如“审图任务“第二批复核” · 外观检测 v3”。 */
export function versionSourceText(
  version: Pick<DatasetVersion, "review" | "source">
) {
  const name = version.review?.name ?? version.source.review_name
  const deleted = version.review === null ? "（已删除）" : ""
  const model = version.source.model.project_name
    ? ` · ${reviewModelTitle(version.source.model)}`
    : ""
  return `审图任务“${name}”${deleted}${model}`
}

/** 可以下载的版本（已生成），新的在前（列表已按此排序）。 */
export function readyVersions(versions: readonly DatasetVersion[]) {
  return versions.filter((version) => version.status === "ready")
}

/** 不是只有基础版本时，下载的内容，如“将导出 1,234 张图片，其中 56 张用审图版本的标注”。 */
export function previewText(
  mode: SelectionMode,
  preview: Pick<ExportPreview, "image_count" | "from_versions" | "object_count">
) {
  const objects = `${formatNumber(preview.object_count)} 个目标`
  if (mode === "versions") {
    return `将导出 ${formatNumber(preview.image_count)} 张需加样的图片，共 ${objects}。`
  }
  return `将导出 ${formatNumber(preview.image_count)} 张图片，其中 ${formatNumber(preview.from_versions)} 张用审图版本的标注，其余用基础版本的标注，共 ${objects}。`
}

/** 所选版本中不会按版本导出的图片：已不在数据集中，或合并后文件被替换。 */
export function previewNotes(
  mode: SelectionMode,
  preview: Pick<ExportPreview, "missing_count" | "changed_count">
) {
  const notes: string[] = []
  if (preview.missing_count > 0) {
    notes.push(
      `所选版本中有 ${formatNumber(preview.missing_count)} 张图片已不在数据集中，不会导出。`
    )
  }
  if (preview.changed_count > 0) {
    notes.push(
      `${formatNumber(preview.changed_count)} 张图片在合并后被替换了文件，审图标注已不适用，${mode === "versions" ? "不会导出" : "使用基础版本的标注"}。`
    )
  }
  return notes
}
