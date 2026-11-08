/**
 * 删除项目对话框的文字（纯函数，见 delete-project.test.ts）：模型与数据版本只
 * 属于一个项目，删除项目时需要一起删除。模型移入回收站；数据版本中已生成的移入
 * 回收站，其余（生成失败、排队中、正在生成）直接删除。
 */
import { formatNumber } from "@/lib/format"

/** 项目中要一起删除的内容。 */
export type ProjectDependants = {
  models: number
  dataVersions: number
  /** 其中已生成的数据版本；还不知道（列表没有加载）时为 null。 */
  readyDataVersions: number | null
}

/** 删除按钮的文字。 */
export function deleteProjectLabel({
  models,
  dataVersions,
}: ProjectDependants) {
  if (models > 0 && dataVersions > 0) {
    return "删除项目、模型与数据版本"
  }
  if (models > 0) {
    return "删除项目与模型"
  }
  return dataVersions > 0 ? "删除项目与数据版本" : "删除项目"
}

/** 对话框说明的后半句；没有要一起删除的内容时为 null。 */
export function dependantsNotice({ models, dataVersions }: ProjectDependants) {
  const parts = [
    models > 0 ? `${formatNumber(models)} 个模型` : null,
    dataVersions > 0 ? `${formatNumber(dataVersions)} 个数据版本` : null,
  ].filter((part) => part !== null)
  if (parts.length === 0) {
    return null
  }
  const subject = parts.length > 1 ? "它们" : models > 0 ? "模型" : "数据版本"
  return `项目中有 ${parts.join("、")}；${subject}只属于这个项目，需要一起删除。`
}

/** “同时删除数据版本”复选框的标题与说明。 */
export function dataVersionsCheckboxText(
  { dataVersions, readyDataVersions: ready }: ProjectDependants,
  retentionDays: number
) {
  const label = `同时删除 ${formatNumber(dataVersions)} 个数据版本`
  const binned = `保留 ${retentionDays} 天，期间可以恢复（项目随之恢复）`
  if (ready === dataVersions) {
    return {
      label: `${label}（移入回收站）`,
      detail: `数据版本移入回收站，${binned}；到期后永久删除。`,
    }
  }
  if (ready === 0) {
    return {
      label: `${label}（直接删除）`,
      detail:
        "这些数据版本还没有生成完成（生成失败、排队中或正在生成），直接删除，正在生成的会停止。",
    }
  }
  return {
    label: `${label}（已生成的移入回收站，其余直接删除）`,
    detail:
      ready === null
        ? `已生成的移入回收站，${binned}；生成失败、排队中与正在生成的直接删除，正在生成的会停止。`
        : `已生成的 ${formatNumber(ready)} 个移入回收站，${binned}；其余 ${formatNumber(dataVersions - ready)} 个（生成失败、排队中或正在生成）直接删除，正在生成的会停止。`,
  }
}

/** 删除成功后提示的说明：移入回收站的与直接删除的；都没有时为 undefined。 */
export function deletedProjectDescription(
  { models, dataVersions, readyDataVersions }: ProjectDependants,
  retentionDays: number
) {
  // 不知道哪些已生成时，按“已生成的移入回收站”笼统地说明。
  if (readyDataVersions === null && dataVersions > 0) {
    const modelText =
      models > 0 ? `${formatNumber(models)} 个模型已移入回收站；` : ""
    return `${modelText}${formatNumber(dataVersions)} 个数据版本已删除，其中已生成的移入回收站。${retentionDays} 天内可以在“系统管理 · 回收站”中恢复，项目随之恢复。`
  }
  const ready = readyDataVersions ?? 0
  const binned = [
    models > 0 ? `${formatNumber(models)} 个模型` : null,
    ready > 0 ? `${formatNumber(ready)} 个数据版本` : null,
  ].filter((part) => part !== null)
  const other = dataVersions - ready
  const sentences = [
    binned.length > 0
      ? `${binned.join("与 ")}已移入回收站，${retentionDays} 天内可以在“系统管理 · 回收站”中恢复，项目随之恢复。`
      : null,
    other > 0
      ? `${ready > 0 ? "其余 " : ""}${formatNumber(other)} 个数据版本（生成失败或未生成完成）已直接删除。`
      : null,
  ].filter((sentence) => sentence !== null)
  return sentences.length > 0 ? sentences.join("") : undefined
}
