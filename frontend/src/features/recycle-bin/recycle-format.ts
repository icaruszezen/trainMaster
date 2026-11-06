/**
 * 回收站页面的纯函数：把三类内容合成一个列表、搜索、到期时间的文字与恢复时
 * 建议的新名称。与界面无关，有单元测试。
 */
import type {
  RecycleBin,
  TrashedDataset,
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

export type RecycleKind = "dataset" | "version" | "model"

export const recycleKindLabels: Record<RecycleKind, string> = {
  dataset: "数据集",
  version: "审图版本",
  model: "模型",
}

/** 回收站中的一项，三类内容共用的字段与原始数据。 */
export type RecycleItem = {
  /** 列表中唯一，如 `dataset-12`。 */
  key: string
  title: string
  deletedAt: string
  purgeAt: string
} & (
  | { kind: "dataset"; dataset: TrashedDataset }
  | { kind: "version"; version: TrashedVersion }
  | { kind: "model"; model: TrashedModel }
)

/** 版本的名称，如“审图1.3”。 */
export function versionTitle(version: TrashedVersion) {
  return version.name || `版本 #${version.id}`
}

/** 模型的名称，如“A 线外观 v3”。 */
export function trashedModelTitle(model: TrashedModel) {
  return `${model.project.name} v${model.version}`
}

/** 三类内容合成一个列表，最近删除的在前。 */
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
