/**
 * 回收站页面的纯函数：把四类内容合成一个列表、搜索、到期时间的文字与恢复时
 * 建议的新名称。与界面无关，有单元测试。
 */
import { dataVersionFullTitle } from "@/features/data-versions/format"
import type {
  RecycleBin,
  TrashedDataset,
  TrashedDataVersion,
  TrashedModel,
  TrashedVersion,
} from "@/features/recycle-bin/api"
import { characterCount } from "@/features/datasets/naming"

/**
 * 回收站的保留天数（后端 `internal/recycle` 的 `Retention`）。删除对话框用它说明；
 * 回收站页面用接口返回的 `retention_days`。
 */
export const RECYCLE_RETENTION_DAYS = 7

/** 回收站页面的路径（与 config/navigation 中的一致）。 */
export const RECYCLE_BIN_PATH = "/system/recycle-bin"

export type RecycleKind = "dataset" | "version" | "data_version" | "model"

/** 各类内容的名称，也是类型筛选中的顺序。 */
export const recycleKindLabels: Record<RecycleKind, string> = {
  dataset: "数据集",
  version: "审图版本",
  data_version: "数据版本",
  model: "模型",
}

/** 回收站中的一项，各类内容共用的字段与原始数据。 */
export type RecycleItem = {
  /** 列表中唯一，如 `dataset-12`。 */
  key: string
  title: string
  deletedAt: string
  purgeAt: string
} & (
  | { kind: "dataset"; dataset: TrashedDataset }
  | { kind: "version"; version: TrashedVersion }
  | { kind: "data_version"; dataVersion: TrashedDataVersion }
  | { kind: "model"; model: TrashedModel }
)

/** 版本的名称，如“审图1.3”。 */
export function versionTitle(version: TrashedVersion) {
  return version.name || `版本 #${version.id}`
}

/** 数据版本的新增占用（见 data-versions/format.ts）。 */
export { dataVersionAddedBytes } from "@/features/data-versions/format"

/** 模型的名称，如“A 线外观 v3”。 */
export function trashedModelTitle(model: TrashedModel) {
  return `${model.project.name} v${model.version}`
}

/** 各类内容合成一个列表，最近删除的在前。 */
export function recycleItems(bin: RecycleBin): RecycleItem[] {
  const items: RecycleItem[] = [
    ...bin.datasets.map((dataset): RecycleItem => ({
      kind: "dataset",
      key: `dataset-${dataset.id}`,
      title: dataset.name,
      deletedAt: dataset.deleted_at,
      purgeAt: dataset.purge_at,
      dataset,
    })),
    ...bin.versions.map((version): RecycleItem => ({
      kind: "version",
      key: `version-${version.id}`,
      title: versionTitle(version),
      deletedAt: version.deleted_at,
      purgeAt: version.purge_at,
      version,
    })),
    ...bin.data_versions.map((dataVersion): RecycleItem => ({
      kind: "data_version",
      key: `data_version-${dataVersion.id}`,
      // “外观检测 · 数据版本 v4”：只写“数据版本 v4”分不清是哪个项目的。
      title: dataVersionFullTitle(dataVersion),
      deletedAt: dataVersion.deleted_at,
      purgeAt: dataVersion.purge_at,
      dataVersion,
    })),
    ...bin.models.map((model): RecycleItem => ({
      kind: "model",
      key: `model-${model.id}`,
      title: trashedModelTitle(model),
      deletedAt: model.deleted_at,
      purgeAt: model.purge_at,
      model,
    })),
  ]
  return items.sort(
    (a, b) =>
      Date.parse(b.deletedAt) - Date.parse(a.deletedAt) ||
      a.key.localeCompare(b.key)
  )
}

/** 搜索时比较的文字：名称、所属的数据集或项目、说明、文件名。 */
function searchText(item: RecycleItem) {
  switch (item.kind) {
    case "dataset":
      return [item.title, item.dataset.description, item.dataset.dir_name]
    case "version":
      return [item.title, item.version.dataset_name, item.version.note]
    case "data_version":
      // 标题中已有项目名称；也可以按来源的数据集查找。
      return [
        item.title,
        item.dataVersion.note,
        ...item.dataVersion.sources.map((source) => source.dataset_name),
      ]
    case "model":
      return [item.title, item.model.description, item.model.file_name]
  }
}

/** 按类型与关键字（不区分大小写）筛选。 */
export function filterRecycleItems(
  items: RecycleItem[],
  { kind, q }: { kind: RecycleKind | null; q: string }
) {
  const needle = q.trim().toLowerCase()
  return items.filter(
    (item) =>
      (kind === null || item.kind === kind) &&
      (needle === "" ||
        searchText(item).some((text) => text.toLowerCase().includes(needle)))
  )
}

const HOUR = 3_600_000
const DAY = 24 * HOUR

/**
 * 到期的说明：`text` 如“6 天后永久删除”，不到一天时按小时；`urgent` 表示不到一天。
 * 已经到期（后台任务还没执行）时为“即将永久删除”。
 */
export function purgeCountdown(purgeAt: string, now: number) {
  const left = Date.parse(purgeAt) - now
  if (!Number.isFinite(left) || left < HOUR) {
    return { text: "即将永久删除", urgent: true }
  }
  if (left < DAY) {
    return { text: `${Math.floor(left / HOUR)} 小时后永久删除`, urgent: true }
  }
  return { text: `${Math.floor(left / DAY)} 天后永久删除`, urgent: false }
}

/** 剩余时间占保留期的比例（0–1），用于进度条。 */
export function remainingFraction(
  deletedAt: string,
  purgeAt: string,
  now: number
) {
  const start = Date.parse(deletedAt)
  const end = Date.parse(purgeAt)
  if (!Number.isFinite(start) || !Number.isFinite(end) || end <= start) {
    return 0
  }
  return Math.min(1, Math.max(0, (end - now) / (end - start)))
}

/**
 * 不能恢复的原因（按钮禁用时的提示），可以恢复时为 null：审图版本的数据集也在
 * 回收站中时需先恢复数据集。
 */
export function restoreBlockedReason(item: RecycleItem): string | null {
  if (item.kind === "version" && item.version.dataset_deleted) {
    return `数据集“${item.version.dataset_name}”也在回收站中，请先恢复数据集`
  }
  return null
}

/**
 * 名称被占用时建议的新名称：“原名（恢复）”，再被占用时“原名（恢复 2）”……
 * 超过长度上限时截短原名。`taken` 不区分大小写。
 */
export function suggestRestoreName(
  name: string,
  taken: Iterable<string>,
  maxLength = 64
) {
  const used = new Set(Array.from(taken, (value) => value.toLowerCase()))
  for (let n = 1; ; n++) {
    const suffix = n === 1 ? "（恢复）" : `（恢复 ${n}）`
    const room = maxLength - characterCount(suffix)
    const base = Array.from(name).slice(0, Math.max(0, room)).join("").trim()
    const candidate = `${base}${suffix}`
    if (!used.has(candidate.toLowerCase())) {
      return candidate
    }
  }
}
