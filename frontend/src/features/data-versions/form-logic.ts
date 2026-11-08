/**
 * “生成数据版本”表单的纯逻辑（见 form-logic.test.ts）：数据集与标注来源的默认
 * 选择、以已有版本为模板时的预填、不在类别列表中的标签的处理方式、把表单
 * 转换成预览 / 创建的请求，以及预览中的警告。
 */
import type { Dataset, DatasetVersion } from "@/features/datasets/api"
import type {
  AugmentMethod,
  Augmentation,
  DataVersion,
  DataVersionPreview,
  DataVersionPreviewInput,
  GroupBy,
  SourceInput,
} from "@/features/data-versions/api"
import { validateVersionNote } from "@/features/reviews/review-format"
import { formatNumber } from "@/lib/format"
import {
  AUGMENT_COPIES_LIMIT,
  copiesLimit,
  normalizeMethods,
} from "@/features/data-versions/augmentations"
import {
  classSplitWarnings,
  findRatioPreset,
  parseCustomRatio,
  parseSeed,
  RATIO_PRESETS,
  type RatioResult,
} from "@/features/data-versions/format"

/** 增强图片的总数上限（后端返回 422）。 */
export const MAX_AUGMENTED_IMAGES = 100_000

/** UI 的默认值（与 docs 中 CreateInput 的说明一致）。 */
export const DEFAULT_SEED = 42

// ---- 数据集与标注来源 ----

/** 一个数据集的标注来源：基础标注与审图版本（新的在前）。 */
export type SourceSelection = { include_base: boolean; version_ids: number[] }

export const NO_SOURCE: SourceSelection = {
  include_base: false,
  version_ids: [],
}

export function isSelected(selection: SourceSelection) {
  return selection.include_base || selection.version_ids.length > 0
}

type DatasetForSource = Pick<
  Dataset,
  "id" | "directory_missing" | "image_count" | "annotated_count"
>

/** 不能作为来源的原因（文件夹不存在、没有图片），否则为 null。 */
export function sourceDisabledReason(
  dataset: Pick<Dataset, "directory_missing" | "image_count">
) {
  if (dataset.directory_missing) {
    return "数据集文件夹不存在，恢复文件夹并重新扫描后才能使用"
  }
  if (dataset.image_count === 0) {
    return "数据集中还没有图片"
  }
  return null
}

/** 已生成的审图版本，新的在前（列表已按此排序）。 */
export function readyReviewVersions(versions: readonly DatasetVersion[]) {
  return versions.filter((version) => version.status === "ready")
}

/**
 * 没有模板时的默认来源：数据集有标注（至少一个目标）时选基础标注，并选中
 * 全部已生成的审图版本（只审图、基础没有标注的数据集也能纳入需加样的图片）。
 */
export function defaultSelection(
  dataset: Pick<Dataset, "annotated_count">,
  versions: readonly Pick<DatasetVersion, "id">[]
): SourceSelection {
  return {
    include_base: dataset.annotated_count > 0,
    version_ids: versions.map((version) => version.id),
  }
}

/** 模板中这个数据集的来源（只保留现在还在的审图版本）；不在模板中时为 null。 */
export function templateSelection(
  template: Pick<DataVersion, "sources">,
  datasetId: number,
  versions: readonly Pick<DatasetVersion, "id">[]
): SourceSelection | null {
  const source = template.sources.find((s) => s.dataset_id === datasetId)
  if (!source) {
    return null
  }
  const available = new Set(versions.map((version) => version.id))
  return {
    include_base: source.include_base,
    version_ids: source.versions
      .map((version) => version.id)
      .filter((id) => available.has(id)),
  }
}

/**
 * 数据集当前的来源：用户改过的（`override`）> 模板 > 默认。不能使用的数据集
 * 总是不选。`versions` 是这个数据集已生成的审图版本。
 */
export function effectiveSelection({
  dataset,
  versions,
  template,
  override,
}: {
  dataset: DatasetForSource
  versions: readonly Pick<DatasetVersion, "id">[]
  template: Pick<DataVersion, "sources"> | null
  override: SourceSelection | undefined
}): SourceSelection {
  if (sourceDisabledReason(dataset)) {
    return NO_SOURCE
  }
  if (override) {
    const available = new Set(versions.map((version) => version.id))
    return {
      include_base: override.include_base,
      version_ids: override.version_ids.filter((id) => available.has(id)),
    }
  }
  if (template) {
    return templateSelection(template, dataset.id, versions) ?? NO_SOURCE
  }
  return defaultSelection(dataset, versions)
}

/** 勾选 / 取消一个审图版本，结果保持新的在前（与 versions 的顺序一致）。 */
export function toggleVersion(
  selection: SourceSelection,
  versionId: number,
  checked: boolean,
  versions: readonly Pick<DatasetVersion, "id">[]
): SourceSelection {
  const ids = new Set(selection.version_ids)
  if (checked) {
    ids.add(versionId)
  } else {
    ids.delete(versionId)
  }
  return {
    include_base: selection.include_base,
    version_ids: versions.map((v) => v.id).filter((id) => ids.has(id)),
  }
}

/** 请求中的来源：选中的数据集，按项目中的顺序。 */
export function sourceInputs(
  entries: readonly { datasetId: number; selection: SourceSelection }[]
): SourceInput[] {
  return entries
    .filter((entry) => isSelected(entry.selection))
    .map((entry) => ({
      dataset_id: entry.datasetId,
      include_base: entry.selection.include_base,
      version_ids: entry.selection.version_ids,
    }))
}

const timeOf = (value: string | null | undefined) =>
  value ? Date.parse(value) : Number.NaN

/**
 * 相对模板是新的审图版本：模板中没有选它，且它是在模板创建之后生成的（之前
 * 就有、模板没有选的版本是有意不选的，不算新的）。
 */
export function isNewReviewVersion(
  version: Pick<DatasetVersion, "id" | "created_at" | "finished_at">,
  datasetId: number,
  template: Pick<DataVersion, "sources" | "created_at"> | null
) {
  if (!template) {
    return false
  }
  const source = template.sources.find((s) => s.dataset_id === datasetId)
  if (source?.versions.some((v) => v.id === version.id)) {
    return false
  }
  return (
    timeOf(version.finished_at ?? version.created_at) >
    timeOf(template.created_at)
  )
}

/** 数据集不在模板的来源中。 */
export function notInTemplate(
  datasetId: number,
  template: Pick<DataVersion, "sources"> | null
) {
  return (
    template !== null &&
    !template.sources.some((s) => s.dataset_id === datasetId)
  )
}

/** 模板中已不在项目中（或已不能使用）的数据集名称。 */
export function missingTemplateSources(
  template: Pick<DataVersion, "sources"> | null,
  datasets: readonly DatasetForSource[]
) {
  if (!template) {
    return []
  }
  const usable = new Set(
    datasets
      .filter((dataset) => sourceDisabledReason(dataset) === null)
      .map((dataset) => dataset.id)
  )
  return template.sources
    .filter((source) => !usable.has(source.dataset_id))
    .map((source) => source.dataset_name)
}

// ---- 不在类别列表中的标签 ----

/** 忽略这些框（保留图片），或跳过含有它的图片。 */
export type UnlistedChoice = "ignore" | "skip"

/**
 * 默认的处理方式：模板忽略的标签继续忽略；属于共用数据集的另一个项目的标签
 * 通常是那个项目的类别，忽略它们的框；其它的可能是真实缺陷，跳过图片。
 */
export function defaultUnlistedChoice(
  label: { name: string; other_project: boolean },
  templateIgnore: readonly string[] | null
): UnlistedChoice {
  if (templateIgnore?.includes(label.name)) {
    return "ignore"
  }
  return label.other_project ? "ignore" : "skip"
}

export type KnownUnlisted = Record<
  string,
  { name: string; other_project: boolean }
>

/** 合并预览中新出现的标签（标签在预览之间可能消失，选择要保留）。 */
export function mergeKnownUnlisted(
  known: KnownUnlisted,
  labels: readonly { name: string; other_project: boolean }[]
): KnownUnlisted {
  let changed = false
  const next: KnownUnlisted = { ...known }
  for (const label of labels) {
    const previous = known[label.name]
    if (!previous || previous.other_project !== label.other_project) {
      next[label.name] = {
        name: label.name,
        other_project: label.other_project,
      }
      changed = true
    }
  }
  return changed ? next : known
}

/**
 * 请求中的 ignore_labels：选择忽略的标签（含模板忽略的），去掉现在已在类别
 * 列表中的，排序。
 */
export function ignoreLabels({
  known,
  choices,
  templateIgnore,
  classNames,
}: {
  known: KnownUnlisted
  choices: Readonly<Record<string, UnlistedChoice>>
  templateIgnore: readonly string[] | null
  classNames: ReadonlySet<string>
}) {
  const names = new Set([...Object.keys(known), ...(templateIgnore ?? [])])
  const result: string[] = []
  for (const name of names) {
    const label = known[name] ?? { name, other_project: false }
    const choice = choices[name] ?? defaultUnlistedChoice(label, templateIgnore)
    if (choice === "ignore" && !classNames.has(name)) {
      result.push(name)
    }
  }
  return result.sort()
}

// ---- 划分与增强 ----

/** 划分比例的表单：预设的 id 或 custom（训练与验证可编辑，测试由此得出）。 */
export type RatioDraft = { preset: string; train: string; val: string }

export const CUSTOM_RATIO = "custom"

export function ratioDraftOf(split: {
  train: number
  val: number
  test: number
}): RatioDraft {
  const preset = findRatioPreset(split)
  return {
    preset: preset?.id ?? CUSTOM_RATIO,
    train: String(split.train),
    val: String(split.val),
  }
}

export function parseRatio(draft: RatioDraft): RatioResult {
  const preset = RATIO_PRESETS.find((p) => p.id === draft.preset)
  if (preset) {
    return { ok: true, train: preset.train, val: preset.val, test: preset.test }
  }
  return parseCustomRatio(draft.train, draft.val)
}

export type AugmentDraft = {
  enabled: boolean
  methods: AugmentMethod[]
  copies: number
  only_with_objects: boolean
}

export const DEFAULT_AUGMENT: AugmentDraft = {
  enabled: false,
  methods: ["hflip"],
  copies: 1,
  only_with_objects: true,
}

/** 实际的份数：不超过所选方法能生成的份数。 */
export function effectiveCopies(
  draft: Pick<AugmentDraft, "methods" | "copies">
) {
  const limit = copiesLimit(draft.methods)
  return Math.max(1, Math.min(draft.copies, limit || 1))
}

export function parseAugmentation(
  draft: AugmentDraft
): { ok: true; value: Augmentation | null } | { ok: false; error: string } {
  if (!draft.enabled) {
    return { ok: true, value: null }
  }
  const methods = normalizeMethods(draft.methods)
  if (methods.length === 0) {
    return { ok: false, error: "请至少选择一种增强方式，或关闭增强" }
  }
  return {
    ok: true,
    value: {
      methods,
      copies: effectiveCopies({ methods, copies: draft.copies }),
      only_with_objects: draft.only_with_objects,
    },
  }
}

// ---- 整个表单 ----

export type FormDraft = {
  /** 用户改过的数据集来源（按数据集 ID）；没有改过的用模板或默认值。 */
  overrides: Record<number, SourceSelection>
  ratio: RatioDraft
  seed: string
  use_folders: boolean
  group_by: GroupBy
  include_empty: boolean
  /** 用户选择的不在类别列表中的标签的处理方式；没有选择的用默认值。 */
  choices: Record<string, UnlistedChoice>
  augment: AugmentDraft
  note: string
}

/** 表单的初始值：有模板时沿用它的划分、过滤与增强设置（备注总是新写）。 */
export function initialDraft(template: DataVersion | null): FormDraft {
  if (!template) {
    return {
      overrides: {},
      ratio: ratioDraftOf(RATIO_PRESETS[0]),
      seed: String(DEFAULT_SEED),
      use_folders: true,
      group_by: "stem",
      include_empty: true,
      choices: {},
      augment: DEFAULT_AUGMENT,
      note: "",
    }
  }
  const { split, augmentation } = template.config
  return {
    overrides: {},
    ratio: ratioDraftOf(split),
    seed: String(split.seed),
    use_folders: split.use_folders,
    group_by: split.group_by,
    include_empty: template.config.include_empty,
    choices: {},
    augment: augmentation
      ? {
          enabled: true,
          methods: normalizeMethods(augmentation.methods),
          copies: Math.min(augmentation.copies, AUGMENT_COPIES_LIMIT),
          only_with_objects: augmentation.only_with_objects,
        }
      : DEFAULT_AUGMENT,
    note: "",
  }
}

export type FormErrors = {
  sources?: string
  ratio?: string
  seed?: string
  augment?: string
}

/**
 * 预览（与创建）的请求；表单无效时 `input` 为 null，`errors` 说明原因（备注
 * 另外检查，见 noteError）。
 */
export function buildPreviewInput({
  projectId,
  draft,
  sources,
  ignore,
}: {
  projectId: number
  draft: FormDraft
  sources: SourceInput[]
  ignore: string[]
}): { input: DataVersionPreviewInput | null; errors: FormErrors } {
  const errors: FormErrors = {}
  if (sources.length === 0) {
    errors.sources = "请至少选择一个数据集的标注来源"
  }
  const ratio = parseRatio(draft.ratio)
  if (!ratio.ok) {
    errors.ratio = ratio.error
  }
  const seed = parseSeed(draft.seed)
  if (seed === null) {
    errors.seed = "随机种子是 0–2147483647 的整数"
  }
  const augmentation = parseAugmentation(draft.augment)
  if (!augmentation.ok) {
    errors.augment = augmentation.error
  }
  if (!ratio.ok || seed === null || !augmentation.ok || sources.length === 0) {
    return { input: null, errors }
  }
  return {
    input: {
      project_id: projectId,
      sources,
      split: {
        train: ratio.train,
        val: ratio.val,
        test: ratio.test,
        seed,
        use_folders: draft.use_folders,
        group_by: draft.group_by,
      },
      include_empty: draft.include_empty,
      ignore_labels: ignore,
      augmentation: augmentation.value,
    },
    errors,
  }
}

export function noteError(note: string) {
  return validateVersionNote(note)
}

// ---- 预览中的警告 ----

export type PreviewWarning = {
  key: string
  text: string
  /** error：服务端会拒绝创建；warning：可以创建，但最好先处理。 */
  tone: "error" | "warning"
}

/** 预览的问题，严重的在前。 */
export function previewWarnings(
  preview: DataVersionPreview,
  testPercent: number
): PreviewWarning[] {
  const warnings: PreviewWarning[] = []
  const error = (key: string, text: string) =>
    warnings.push({ key, text, tone: "error" })
  const warn = (key: string, text: string) =>
    warnings.push({ key, text, tone: "warning" })

  if (preview.image_count === 0) {
    error("empty", "没有可以放进数据版本的图片，请检查所选的标注来源与过滤设置")
  } else {
    if (preview.splits.train.images === 0) {
      error("train", "划分后训练集没有图片，请调整划分比例")
    }
    if (preview.splits.val.images === 0) {
      error("val", "划分后验证集没有图片，请调整划分比例或换一种划分")
    }
    if (testPercent > 0 && preview.splits.test.images === 0) {
      warn("test", "划分后测试集没有图片")
    }
  }
  if (preview.augmented_count > MAX_AUGMENTED_IMAGES) {
    error(
      "augment",
      `增强图片约 ${formatNumber(preview.augmented_count)} 张，超过上限 ${formatNumber(MAX_AUGMENTED_IMAGES)} 张，请减少份数或方法`
    )
  }
  if (preview.skipped.unlisted > 0) {
    warn(
      "unlisted",
      `${formatNumber(preview.skipped.unlisted)} 张图片含有不在类别列表中的标签，不会纳入`
    )
  }
  if (preview.image_count > 0) {
    for (const item of classSplitWarnings(preview.labels, testPercent)) {
      warn(`class:${item.split}:${item.name}`, item.text)
    }
  }
  return warnings
}

/** 服务端会拒绝创建的问题（第一个），没有时为 null。 */
export function blockingWarning(warnings: readonly PreviewWarning[]) {
  return warnings.find((warning) => warning.tone === "error") ?? null
}

/** 创建失败的 409 是类别列表在预览之后被修改了。 */
export function isClassListConflict(message: string) {
  return message.includes("类别列表已经修改") || message.includes("还没有类别")
}

/** 422 的错误属于哪个字段（按消息内容），用于显示在字段旁边。 */
export function fieldOfServerError(
  message: string
): "note" | "sources" | "ratio" | "seed" | "augment" | "labels" | null {
  if (message.includes("备注")) {
    return "note"
  }
  if (message.includes("增强") || message.includes("副本")) {
    return "augment"
  }
  if (message.includes("随机种子") || message.includes("seed")) {
    return "seed"
  }
  if (message.includes("比例") || message.includes("划分")) {
    return "ratio"
  }
  if (message.includes("忽略的标签")) {
    return "labels"
  }
  if (message.includes("数据集") || message.includes("审图版本")) {
    return "sources"
  }
  return null
}
