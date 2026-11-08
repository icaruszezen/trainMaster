/**
 * 数据版本的显示文字（纯函数，见 format.test.ts）。
 *
 * 模型也有 v1、v2…，所以界面上总是写“数据版本 v3”，不单独写“v3”。
 */
import { labelColor } from "@/features/datasets/label-colors"
import { formatBytes, formatNumber, percentOf } from "@/lib/format"

import {
  SPLITS,
  type Augmentation,
  type DataVersion,
  type DataVersionClass,
  type DataVersionCompare,
  type DataVersionLabelStats,
  type DataVersionPreview,
  type DataVersionProgress,
  type DataVersionSkipped,
  type DataVersionStage,
  type DataVersionStats,
  type DataVersionStatus,
  type GroupBy,
  type Split,
  type SplitConfig,
  type SplitCounts,
  type StoredKind,
} from "@/features/data-versions/api"
import { augmentMethodsText } from "@/features/data-versions/augmentations"

/** “数据版本 v3”。 */
export function dataVersionTitle(v: Pick<DataVersion, "version">) {
  return `数据版本 v${v.version}`
}

/** “外观检测 · 数据版本 v3”。 */
export function dataVersionFullTitle(
  v: Pick<DataVersion, "project_name" | "version">
) {
  return `${v.project_name} · ${dataVersionTitle(v)}`
}

export const DATA_VERSION_STATUS_LABELS: Record<DataVersionStatus, string> = {
  queued: "排队中",
  building: "生成中",
  ready: "已生成",
  failed: "生成失败",
}

export const SPLIT_LABELS: Record<Split, string> = {
  train: "训练",
  val: "验证",
  test: "测试",
}

/** 划分的全称，如“验证集”。 */
export const SPLIT_SET_LABELS: Record<Split, string> = {
  train: "训练集",
  val: "验证集",
  test: "测试集",
}

export const DATA_VERSION_STAGE_LABELS: Record<DataVersionStage, string> = {
  preparing: "读取图片与标注",
  linking: "链接与复制图片",
  augmenting: "生成增强图片",
  finishing: "写入文件列表",
}

export const GROUP_BY_LABELS: Record<GroupBy, string> = {
  stem: "按文件名分组",
  folder: "按文件夹分组",
}

export const GROUP_BY_DESCRIPTIONS: Record<GroupBy, string> = {
  stem: "同一文件夹中文件名相同的图片（去掉“副本”“-aug1”等后缀后相同）总在同一个划分中，避免同一张图的多个副本分到训练集和验证集。",
  folder:
    "同一个最底层文件夹中的全部图片总在同一个划分中，适合一个零件或一个批次一个文件夹（如 SN123_cam1、SN123_cam2）。",
}

export const STORED_LABELS: Record<StoredKind, string> = {
  link: "硬链接（与数据集共用文件）",
  copy: "复制",
  generated: "增强生成",
}

/** 生成进度，如“链接与复制图片 120 / 400”；总数未知时只有阶段。 */
export function progressText(progress: DataVersionProgress) {
  const stage = DATA_VERSION_STAGE_LABELS[progress.stage] ?? progress.stage
  return progress.total > 0
    ? `${stage} ${formatNumber(progress.done)} / ${formatNumber(progress.total)}`
    : stage
}

/** 进度条的百分比；总数未知时为 null（不确定进度）。 */
export function progressPercent(progress: DataVersionProgress | null) {
  return progress && progress.total > 0
    ? percentOf(progress.done, progress.total)
    : null
}

/** 排队的位置，如“下一个生成”“前面还有 2 个”。 */
export function queueText(position: number | null) {
  if (position === null || position <= 1) {
    return "下一个生成"
  }
  return `前面还有 ${formatNumber(position - 1)} 个`
}

/** 状态的详细说明：排队位置、生成进度或状态名。 */
export function statusDetailText(
  v: Pick<DataVersion, "status" | "progress" | "queue_position">
) {
  if (v.status === "queued") {
    return `排队中 · ${queueText(v.queue_position)}`
  }
  if (v.status === "building" && v.progress) {
    return progressText(v.progress)
  }
  return DATA_VERSION_STATUS_LABELS[v.status]
}

/** “训练 800 · 验证 200 · 测试 0”。 */
export function splitSummary(counts: SplitCounts) {
  return SPLITS.map(
    (split) => `${SPLIT_LABELS[split]} ${formatNumber(counts[split])}`
  ).join(" · ")
}

/** 版本的划分计数（含增强图片，增强图片都在训练集中）。 */
export function versionSplitCounts(
  v: Pick<DataVersion, "train_count" | "val_count" | "test_count">
): SplitCounts {
  return { train: v.train_count, val: v.val_count, test: v.test_count }
}

/** 划分比例，如“训练 80% · 验证 20% · 测试 0%”。 */
export function splitRatioText(split: Pick<SplitConfig, Split>) {
  return SPLITS.map((s) => `${SPLIT_LABELS[s]} ${split[s]}%`).join(" · ")
}

// ---- 划分比例 ----

export type RatioPreset = {
  id: string
  train: number
  val: number
  test: number
}

export const RATIO_PRESETS: readonly RatioPreset[] = [
  { id: "80/20/0", train: 80, val: 20, test: 0 },
  { id: "70/20/10", train: 70, val: 20, test: 10 },
  { id: "90/10/0", train: 90, val: 10, test: 0 },
]

/** 与比例相同的预设；没有时为 null（自定义）。 */
export function findRatioPreset(
  ratio: Pick<SplitConfig, Split>
): RatioPreset | null {
  return (
    RATIO_PRESETS.find(
      (preset) =>
        preset.train === ratio.train &&
        preset.val === ratio.val &&
        preset.test === ratio.test
    ) ?? null
  )
}

/** 预设的文字，如“训练 70% · 验证 20% · 测试 10%”。 */
export function ratioPresetLabel(preset: RatioPreset) {
  return `${preset.train} / ${preset.val} / ${preset.test}`
}

export type RatioResult =
  | { ok: true; train: number; val: number; test: number }
  | { ok: false; error: string }

function parsePercent(raw: string) {
  const text = raw.trim()
  return /^\d{1,3}$/.test(text) ? Number(text) : null
}

/**
 * 自定义比例：填写训练与验证的百分比（整数），测试 = 100 − 训练 − 验证。
 * 训练与验证至少 1%，测试不能为负。
 */
export function parseCustomRatio(
  trainRaw: string,
  valRaw: string
): RatioResult {
  const train = parsePercent(trainRaw)
  const val = parsePercent(valRaw)
  if (train === null || val === null) {
    return { ok: false, error: "请填写 0–100 的整数百分比" }
  }
  if (train < 1) {
    return { ok: false, error: "训练集至少 1%" }
  }
  if (val < 1) {
    return { ok: false, error: "验证集至少 1%" }
  }
  const test = 100 - train - val
  if (test < 0) {
    return { ok: false, error: "训练与验证合计不能超过 100%" }
  }
  return { ok: true, train, val, test }
}

// ---- 随机种子 ----

export const MAX_SEED = 2_147_483_647

/** 随机种子：0–2147483647 的整数。 */
export function parseSeed(raw: string): number | null {
  const text = raw.trim()
  if (!/^\d{1,10}$/.test(text)) {
    return null
  }
  const seed = Number(text)
  return seed <= MAX_SEED ? seed : null
}

/** “换一种划分”：一个新的随机种子（与当前的不同）。 */
export function randomSeed(current: number | null, random = Math.random) {
  for (;;) {
    const seed = Math.floor(random() * (MAX_SEED + 1))
    if (seed !== current) {
      return seed
    }
  }
}

// ---- 跳过的图片 ----

export const SKIPPED_LABELS: Record<keyof DataVersionSkipped, string> = {
  unannotated: "没有标注",
  invalid: "标注文件无法读取",
  unlisted: "含有不在类别列表中的标签",
  empty: "空标注（未纳入负样本）",
  missing: "文件已不存在",
  changed: "文件在上次扫描后被修改",
  unknown_size: "无法读取图片尺寸",
  unsupported_format: "训练工具不支持的格式（GIF、AVIF）",
  augment_skipped: "增强图片少于所选份数的训练集图片",
}

/** 各原因的说明（悬停或详情中显示）。 */
export const SKIPPED_HINTS: Partial<Record<keyof DataVersionSkipped, string>> =
  {
    unannotated: "没有标注文件，也没有审图标注的图片不会纳入。",
    unlisted:
      "图片中有不在项目类别列表中、也没有选择忽略的标签：这些框可能是真实缺陷，纳入后会被当作背景。",
    changed: "重新扫描数据集后再生成即可纳入。",
    augment_skipped:
      "这些图片本身已纳入，只是少生成或没有生成增强图片：无法解码、太大，或者目标太小而不再有不同的增强方式。",
  }

const SKIPPED_ORDER: (keyof DataVersionSkipped)[] = [
  "unannotated",
  "invalid",
  "unlisted",
  "empty",
  "missing",
  "changed",
  "unknown_size",
  "unsupported_format",
  "augment_skipped",
]

export type SkippedEntry = {
  key: keyof DataVersionSkipped
  label: string
  count: number
}

/** 数量不为 0 的跳过原因，按固定顺序。 */
export function skippedEntries(skipped: DataVersionSkipped): SkippedEntry[] {
  return SKIPPED_ORDER.filter((key) => (skipped[key] ?? 0) > 0).map((key) => ({
    key,
    label: SKIPPED_LABELS[key],
    count: skipped[key],
  }))
}

/** 没有纳入的图片数（不含 augment_skipped：那些图片已纳入，只是没有增强）。 */
export function skippedTotal(skipped: DataVersionSkipped) {
  return SKIPPED_ORDER.filter((key) => key !== "augment_skipped").reduce(
    (sum, key) => sum + (skipped[key] ?? 0),
    0
  )
}

/** “没有标注 3 张 · 标注文件无法读取 1 张”；没有时为 “”。 */
export function skippedSummary(skipped: DataVersionSkipped) {
  return skippedEntries(skipped)
    .map((entry) => `${entry.label} ${formatNumber(entry.count)} 张`)
    .join(" · ")
}

// ---- 存储与对比 ----

/** 新增占用的字节数：复制与增强生成的文件（硬链接的原图与数据集共用）。 */
export function addedBytes(storage: DataVersionStats["storage"]) {
  return storage.copied_bytes + storage.generated_bytes
}

/** 版本的新增占用（与 addedBytes 相同，参数是整个版本）。 */
export function dataVersionAddedBytes(v: Pick<DataVersion, "stats">) {
  return addedBytes(v.stats.storage)
}

/** “新增占用 1.2 GB（其余文件与数据集共用）”；没有硬链接的文件时不带括号。 */
export function storageText(storage: DataVersionStats["storage"]) {
  const added = `新增占用 ${formatBytes(addedBytes(storage))}`
  return storage.linked > 0 ? `${added}（其余文件与数据集共用）` : added
}

/** 存储方式的计数，如“硬链接 1,000 张 · 复制 3 张 · 增强生成 600 张”。 */
export function storageCountsText(storage: DataVersionStats["storage"]) {
  const parts = [
    storage.linked > 0 ? `硬链接 ${formatNumber(storage.linked)} 张` : null,
    storage.copied > 0 ? `复制 ${formatNumber(storage.copied)} 张` : null,
    storage.generated > 0
      ? `增强生成 ${formatNumber(storage.generated)} 张`
      : null,
  ].filter((part) => part !== null)
  return parts.length > 0 ? parts.join(" · ") : "没有图片文件"
}

/**
 * “相比 数据版本 v3：新增 120 张，移除 4 张，划分变化 0 张，标注变化 7 张”。
 * 预览按索引对比，不知道标注是否变化，`annotations` 为 false 时不写这一项。
 */
export function compareText(
  compare: DataVersionCompare,
  { annotations = true }: { annotations?: boolean } = {}
) {
  const parts = [
    `新增 ${formatNumber(compare.added)} 张`,
    `移除 ${formatNumber(compare.removed)} 张`,
    `划分变化 ${formatNumber(compare.split_changed)} 张`,
  ]
  if (annotations) {
    parts.push(`标注变化 ${formatNumber(compare.annotation_changed)} 张`)
  }
  return `相比 ${dataVersionTitle(compare)}：${parts.join("，")}`
}

// ---- 类别 ----

/** 类别的颜色：类别列表中设置的颜色，没有时按名称自动配色。 */
export function classColor(cls: Pick<DataVersionClass, "name" | "color">) {
  return cls.color || labelColor(cls.name)
}

/** 类别在所有划分中的目标数。 */
export function labelObjects(label: DataVersionLabelStats) {
  return SPLITS.reduce((sum, split) => sum + label[split].objects, 0)
}

export type ClassSplitWarning = {
  name: string
  split: Split
  text: string
}

/**
 * 划分后缺少某个类别的划分：类别有目标，但验证集（或训练集；测试比例大于 0
 * 时还有测试集）中没有。没有目标的类别不在这里（见 emptyClasses）。
 */
export function classSplitWarnings(
  labels: readonly DataVersionLabelStats[],
  testPercent: number
): ClassSplitWarning[] {
  const warnings: ClassSplitWarning[] = []
  for (const label of labels) {
    if (labelObjects(label) === 0) {
      continue
    }
    const splits: Split[] =
      testPercent > 0 ? ["val", "train", "test"] : ["val", "train"]
    for (const split of splits) {
      if (label[split].objects === 0) {
        warnings.push({
          name: label.name,
          split,
          text: `“${label.name}”在${SPLIT_SET_LABELS[split]}中没有目标`,
        })
      }
    }
  }
  return warnings
}

/** 类别列表中没有任何目标的类别。 */
export function emptyClasses(labels: readonly DataVersionLabelStats[]) {
  return labels
    .filter((label) => labelObjects(label) === 0)
    .map((label) => label.name)
}

// ---- 数据集与标注来源 ----

/** 预览中一个数据集的计数，如“选中 130 张 · 纳入 120 张（20 张用审图标注）”。 */
export function previewSourceText(
  source: Pick<
    DataVersionPreview["sources"][number],
    "selected_count" | "image_count" | "from_versions" | "by_folder"
  >
) {
  const parts = [
    `选中 ${formatNumber(source.selected_count)} 张`,
    `纳入 ${formatNumber(source.image_count)} 张` +
      (source.from_versions > 0
        ? `（${formatNumber(source.from_versions)} 张用审图标注）`
        : ""),
  ]
  if (source.by_folder > 0) {
    parts.push(`${formatNumber(source.by_folder)} 张按文件夹划分`)
  }
  return parts.join(" · ")
}

/** 一个数据集选择的标注来源的含义。 */
export function selectionHint(selection: {
  include_base: boolean
  version_ids: readonly number[]
}) {
  if (selection.version_ids.length === 0) {
    return selection.include_base ? "使用数据集自己的标注。" : ""
  }
  return selection.include_base
    ? "所选审图版本中有的图片用审图标注（多个版本都有时以新的为准），其他图片用数据集自己的标注。"
    : "只纳入所选审图版本中标为需加样的图片（用审图标注）。"
}

// ---- 设置 ----

/** “水平翻转、亮度 / 对比度 · 每张训练集图片 2 份 · 只增强有目标的图片”。 */
export function augmentationText(augmentation: Augmentation | null) {
  if (!augmentation || augmentation.methods.length === 0) {
    return "不增强"
  }
  return [
    augmentMethodsText(augmentation.methods),
    `每张训练集图片 ${formatNumber(augmentation.copies)} 份`,
    augmentation.only_with_objects ? "只增强有目标的图片" : "负样本也增强",
  ].join(" · ")
}

/** 版本备注的第一行（列表中显示）。 */
export function noteFirstLine(note: string) {
  return note.split(/\r?\n/, 1)[0] ?? ""
}
