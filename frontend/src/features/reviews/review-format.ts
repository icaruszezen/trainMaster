/**
 * 审图任务页面（列表、详情与各个对话框）用到的纯函数：模型与数据集的简短
 * 说明、默认名称与输入校验（与后端 internal/review 的规则一致，服务端的校验
 * 仍是最终结果）、图片数的估计，以及上传与合并结果的摘要。
 */
import type { Dataset } from "@/features/datasets/api"
import { characterCount } from "@/features/datasets/naming"
import type { Model } from "@/features/models/api"
import {
  DEFAULT_IMG_SIZE,
  imgSizeText,
  roundImgSize,
  taskLabel,
  validateImgSize,
  yoloVersionLabel,
} from "@/features/models/yolo-versions"
import { validateProjectName } from "@/features/projects/naming"
import type {
  CreateReviewInput,
  CreateReviewMergeInput,
  CreateReviewPackageInput,
  Review,
  ReviewCounts,
  ReviewImageStatus,
  ReviewMerge,
  ReviewMergeSummary,
  ReviewModelRef,
  ReviewOptions,
  ReviewUploadSummary,
} from "@/features/reviews/api"
import { reviewWorkspacePath } from "@/features/reviews/params"
import {
  REVIEW_IMAGE_STATUSES,
  reviewImageStatusLabels,
} from "@/features/reviews/review-status"
import { formatDuration, formatNumber } from "@/lib/format"

// ---- 规则（与后端 internal/review 一致） ----

export const REVIEW_NAME_MAX_LENGTH = 64
export const REVIEW_DESCRIPTION_MAX_LENGTH = 500
/** 一个审图任务最多的数据集数与图片数。 */
export const REVIEW_MAX_DATASETS = 50
export const REVIEW_MAX_IMAGES = 200_000
/** 上传的审图结果最大 4 GiB。 */
export const REVIEW_RESULTS_MAX_BYTES = 4 * 1024 ** 3

/** 识别参数的范围与默认值。 */
export const CONF_RANGE = { min: 0.01, max: 1, fallback: 0.25 } as const
export const IOU_RANGE = { min: 0.1, max: 0.95, fallback: 0.7 } as const
export const MAX_DET_RANGE = { min: 1, max: 1000, fallback: 300 } as const

/** 带检测框的任务（与后端 model.HasBoxes 一致）：只有它们能用于审图。 */
const BOX_TASKS: readonly string[] = ["detect", "segment", "pose", "obb"]

const nameCollator = new Intl.Collator("zh-CN", { numeric: true })

// ---- 模型 ----

/** 模型快照的标题：`<项目名> v<版本>`。 */
export function reviewModelTitle(
  model: Pick<ReviewModelRef, "project_name" | "version">
) {
  return `${model.project_name} v${model.version}`
}

/** 模型的版本与任务，如 `YOLO11 · 检测`。 */
export function reviewModelText(
  model: Pick<ReviewModelRef, "yolo_version" | "task">
) {
  return `${yoloVersionLabel(model.yolo_version)} · ${taskLabel(model.task)}`
}

/** 模型不能用于审图的原因（文件缺失、没有检测框）；可以使用时为 null。 */
export function reviewModelBlocker(
  model: Pick<Model, "task" | "file_missing">
): string | null {
  if (model.file_missing) {
    return "模型文件缺失"
  }
  if (!BOX_TASKS.includes(model.task)) {
    return model.task === ""
      ? "无法识别任务，不能用于审图"
      : `${taskLabel(model.task)}模型没有检测框`
  }
  return null
}

export type ModelGroup = {
  project: { id: number; name: string }
  /** 版本号大的（新的）在前。 */
  models: Model[]
}

/** 按项目分组的模型（项目按名称排序），选择模型时使用。 */
export function groupModelsByProject(models: readonly Model[]): ModelGroup[] {
  const groups = new Map<number, ModelGroup>()
  for (const model of models) {
    let group = groups.get(model.project.id)
    if (!group) {
      group = { project: model.project, models: [] }
      groups.set(model.project.id, group)
    }
    group.models.push(model)
  }
  const sorted = [...groups.values()].sort(
    (a, b) =>
      nameCollator.compare(a.project.name, b.project.name) ||
      a.project.id - b.project.id
  )
  for (const group of sorted) {
    group.models.sort((a, b) => b.version - a.version)
  }
  return sorted
}

// ---- 名称与说明 ----

/**
 * 不填名称时的名称：`<项目名> v<版本> 审图`，项目名太长时截短（与后端
 * defaultName 一致），用作名称输入框的占位文字。
 */
export function defaultReviewName(projectName: string, version: number) {
  const suffix = ` v${version} 审图`
  const room = REVIEW_NAME_MAX_LENGTH - characterCount(suffix)
  const base = Array.from(projectName.trim()).slice(0, room).join("").trim()
  return base + suffix
}

/**
 * 校验名称（先去掉首尾空白），规则与项目名称相同。新建时可以不填（`optional`），
 * 由服务端生成默认名称。
 */
export function validateReviewName(
  raw: string,
  { optional = false }: { optional?: boolean } = {}
): string | null {
  if (optional && raw.trim() === "") {
    return null
  }
  return validateProjectName(raw)
}

const DESCRIPTION_CONTROL_CHARS =
  // eslint-disable-next-line no-control-regex -- 换行与制表符以外的控制字符正是要拒绝的内容
  /[\u0000-\u0008\u000b\u000c\u000e-\u001f\u007f]/

/** 校验说明（先去掉首尾空白）：最多 500 个字符，可以换行。 */
export function validateReviewDescription(raw: string): string | null {
  const description = raw.trim()
  if (DESCRIPTION_CONTROL_CHARS.test(description)) {
    return "说明包含无效字符"
  }
  if (characterCount(description) > REVIEW_DESCRIPTION_MAX_LENGTH) {
    return `说明不能超过 ${REVIEW_DESCRIPTION_MAX_LENGTH} 个字符`
  }
  return null
}

// ---- 识别参数 ----

/** 识别参数的输入框（字符串，校验后才转换）。 */
export type ReviewOptionsDraft = {
  conf: string
  iou: string
  height: string
  width: string
  maxDet: string
}

/** 模型的默认输入尺寸：训练尺寸向上取 32 的倍数，未记录时 640（与后端一致）。 */
export function defaultReviewImgSize(model: {
  info: { image_size: number | null }
}) {
  const trained = model.info.image_size
  return trained !== null && trained > 0
    ? roundImgSize(trained)
    : DEFAULT_IMG_SIZE
}

/** 默认的识别参数；没有选择模型时输入尺寸为 640。 */
export function defaultOptionsDraft(
  model: { info: { image_size: number | null } } | null
): ReviewOptionsDraft {
  const size = String(model ? defaultReviewImgSize(model) : DEFAULT_IMG_SIZE)
  return {
    conf: String(CONF_RANGE.fallback),
    iou: String(IOU_RANGE.fallback),
    height: size,
    width: size,
    maxDet: String(MAX_DET_RANGE.fallback),
  }
}

const DECIMAL = /^(\d+(\.\d*)?|\.\d+)$/

function validateDecimal(
  raw: string,
  what: string,
  range: { min: number; max: number }
): string | null {
  const text = raw.trim()
  if (text === "") {
    return `请填写${what}`
  }
  if (!DECIMAL.test(text)) {
    return "请填写数字，如 0.25"
  }
  const value = Number(text)
  if (value < range.min || value > range.max) {
    return `${what}应在 ${range.min}–${range.max} 之间`
  }
  return null
}

/** 校验置信度阈值（0.01–1）：低于它的框不显示。 */
export function validateConf(raw: string) {
  return validateDecimal(raw, "置信度阈值", CONF_RANGE)
}

/** 校验 NMS 的 IoU 阈值（0.1–0.95）。 */
export function validateIoU(raw: string) {
  return validateDecimal(raw, "IoU 阈值", IOU_RANGE)
}

/** 校验每张图片最多的检测框数（1–1000 的整数）。 */
export function validateMaxDet(raw: string): string | null {
  const text = raw.trim()
  if (text === "") {
    return "请填写最多检测框数"
  }
  if (!/^\d+$/.test(text)) {
    return "请填写整数"
  }
  const value = Number(text)
  if (value < MAX_DET_RANGE.min || value > MAX_DET_RANGE.max) {
    return `最多检测框数应在 ${MAX_DET_RANGE.min}–${MAX_DET_RANGE.max} 之间`
  }
  return null
}

export type ReviewOptionsErrors = {
  conf: string | null
  iou: string | null
  imgsz: string | null
  maxDet: string | null
}

/** 各个识别参数的错误（合法时为 null）。 */
export function optionsDraftErrors(
  draft: ReviewOptionsDraft
): ReviewOptionsErrors {
  return {
    conf: validateConf(draft.conf),
    iou: validateIoU(draft.iou),
    imgsz: validateImgSize(draft.height) ?? validateImgSize(draft.width),
    maxDet: validateMaxDet(draft.maxDet),
  }
}

/** 识别参数 → 请求中的 `options`；有不合法的参数时为 null。 */
export function optionsFromDraft(
  draft: ReviewOptionsDraft
): NonNullable<CreateReviewInput["options"]> | null {
  const errors = optionsDraftErrors(draft)
  if (Object.values(errors).some((error) => error !== null)) {
    return null
  }
  return {
    conf: Number(draft.conf.trim()),
    iou: Number(draft.iou.trim()),
    imgsz: [Number(draft.height.trim()), Number(draft.width.trim())],
    max_det: Number(draft.maxDet.trim()),
  }
}

/** 与默认值不同的识别参数个数（“高级选项”旁显示）。 */
export function changedOptionCount(
  draft: ReviewOptionsDraft,
  defaults: ReviewOptionsDraft
) {
  const same = (a: string, b: string) =>
    a.trim() === b.trim() || Number(a.trim()) === Number(b.trim())
  return [
    !same(draft.conf, defaults.conf),
    !same(draft.iou, defaults.iou),
    !same(draft.height, defaults.height) || !same(draft.width, defaults.width),
    !same(draft.maxDet, defaults.maxDet),
  ].filter(Boolean).length
}

/** 识别参数的简短描述，如 `置信度 ≥ 0.25 · IoU 0.7 · 640 · 最多 300 个框`。 */
export function reviewOptionsText(options: ReviewOptions) {
  return [
    `置信度 ≥ ${options.conf}`,
    `IoU ${options.iou}`,
    imgSizeText(options.imgsz),
    `最多 ${formatNumber(options.max_det)} 个框`,
  ].join(" · ")
}

// ---- 数据集 ----

/** 数据集名称的简写：一个、两个直接列出，更多时“A、B 等 5 个”。 */
export function datasetNamesText(datasets: readonly { name: string }[]) {
  if (datasets.length === 0) {
    return "—"
  }
  if (datasets.length <= 2) {
    return datasets.map((dataset) => dataset.name).join("、")
  }
  return `${datasets
    .slice(0, 2)
    .map((dataset) => dataset.name)
    .join("、")} 等 ${formatNumber(datasets.length)} 个`
}

/** 属于项目且有图片的数据集（按名称排序）：选择模型后默认选中它们。 */
export function projectDatasetIds(
  datasets: readonly Dataset[],
  projectId: number
): number[] {
  return datasets
    .filter(
      (dataset) =>
        dataset.image_count > 0 &&
        dataset.projects.some((project) => project.id === projectId)
    )
    .sort((a, b) => nameCollator.compare(a.name, b.name))
    .map((dataset) => dataset.id)
}

/**
 * 所选的数据集（按选择的顺序，去掉列表中已经没有的）与它们的图片总数。
 * 选择的顺序就是审图任务中图片的顺序。
 */
export function selectedDatasets(
  datasets: readonly Dataset[],
  selected: Iterable<number>
) {
  const byId = new Map(datasets.map((dataset) => [dataset.id, dataset]))
  const chosen: Dataset[] = []
  for (const id of selected) {
    const dataset = byId.get(id)
    if (dataset) {
      chosen.push(dataset)
    }
  }
  return {
    datasets: chosen,
    images: chosen.reduce((sum, dataset) => sum + dataset.image_count, 0),
  }
}

/** 所选数据集的问题（没有选、太多、没有图片、图片太多）；没有问题时为 null。 */
export function datasetSelectionError(
  count: number,
  images: number
): string | null {
  if (count === 0) {
    return "请至少选择一个数据集"
  }
  if (count > REVIEW_MAX_DATASETS) {
    return `一个审图任务最多选择 ${REVIEW_MAX_DATASETS} 个数据集`
  }
  if (images === 0) {
    return "所选数据集中没有图片"
  }
  if (images > REVIEW_MAX_IMAGES) {
    return `所选数据集共有 ${formatNumber(images)} 张图片，一个审图任务最多 ${formatNumber(REVIEW_MAX_IMAGES)} 张，请少选一些数据集`
  }
  return null
}

// ---- 图片数 ----

/**
 * 识别完成、审核状态为 `statuses` 之一的图片数：审图包与合并只包括识别完成
 * 的图片。还没有识别的图片都是待审核（识别完成后才能标记），所以从待审核中
 * 减去它们。
 */
export function inferredImageCount(
  counts: ReviewCounts,
  statuses: Iterable<ReviewImageStatus>
) {
  let sum = 0
  for (const status of new Set(statuses)) {
    sum +=
      status === "pending"
        ? Math.max(0, counts.pending - (counts.total - counts.inferred))
        : counts[status]
  }
  return sum
}

/** 已审核与需加样的图片数。 */
export function reviewedCount(counts: ReviewCounts) {
  return counts.approved + counts.needs_sample
}

/** 审核状态按界面上的顺序排列（待审核、已审核、需加样），去掉重复的。 */
export function orderedStatuses(
  statuses: Iterable<ReviewImageStatus>
): ReviewImageStatus[] {
  const set = new Set(statuses)
  return REVIEW_IMAGE_STATUSES.filter((status) => set.has(status))
}

/** 审核状态的名称，如“已审核、需加样”。 */
export function statusesText(statuses: Iterable<ReviewImageStatus>) {
  return orderedStatuses(statuses)
    .map((status) => reviewImageStatusLabels[status])
    .join("、")
}

/**
 * “网页审图”“审图”入口的地址：不筛选审核状态（列表显示全部图片），编辑器打开
 * 第一张待审核的图片，没有时打开第一张（见 params 的 `pickStartImage`）。
 * `datasetId` 时只看这个数据集。
 */
export function reviewEntryPath(
  review: Pick<Review, "id">,
  datasetId: number | null = null
) {
  return reviewWorkspacePath(review.id, datasetId !== null ? { datasetId } : {})
}

// ---- 审图包 ----

/**
 * 下载审图包的请求：选中了全部审核状态时省略 `statuses`，全部数据集
 * （`datasetId` 为 null）时省略 `dataset_ids`。
 */
export function packageInput(
  statuses: Iterable<ReviewImageStatus>,
  datasetId: number | null
): CreateReviewPackageInput {
  const ordered = orderedStatuses(statuses)
  const input: CreateReviewPackageInput = {}
  if (ordered.length < REVIEW_IMAGE_STATUSES.length) {
    input.statuses = ordered
  }
  if (datasetId !== null) {
    input.dataset_ids = [datasetId]
  }
  return input
}

// ---- 上传与合并 ----

/** 合并的请求：全部数据集（`datasetId` 为 null）时省略 `dataset_ids`。 */
export function mergeInput(
  statuses: Iterable<ReviewImageStatus>,
  datasetId: number | null,
  onExisting: CreateReviewMergeInput["on_existing"]
): CreateReviewMergeInput {
  const input: CreateReviewMergeInput = {
    statuses: orderedStatuses(statuses),
    on_existing: onExisting,
  }
  if (datasetId !== null) {
    input.dataset_ids = [datasetId]
  }
  return input
}

/** 校验要上传的审图结果文件，合法时返回 null。 */
export function validateResultsFile(file: {
  name: string
  size: number
}): string | null {
  if (!/\.zip$/i.test(file.name)) {
    return "请选择 .zip 文件（本地审图程序导出的审图结果，或重新压缩的审图包）"
  }
  if (file.size === 0) {
    return "文件是空的"
  }
  if (file.size > REVIEW_RESULTS_MAX_BYTES) {
    return "审图结果不能超过 4 GiB"
  }
  return null
}

/** 上传结果的摘要，如“应用 5 张（审核状态 5、标注 1），没有变化 24 张，冲突 1 张”。 */
export function uploadSummaryText(summary: ReviewUploadSummary) {
  const changes = [
    summary.status_changes > 0
      ? `审核状态 ${formatNumber(summary.status_changes)}`
      : null,
    summary.annotation_changes > 0
      ? `标注 ${formatNumber(summary.annotation_changes)}`
      : null,
  ].filter((part) => part !== null)
  const parts = [
    `应用 ${formatNumber(summary.applied)} 张${changes.length > 0 ? `（${changes.join("、")}）` : ""}`,
    summary.unchanged > 0
      ? `没有变化 ${formatNumber(summary.unchanged)} 张`
      : null,
    summary.conflicts > 0 ? `冲突 ${formatNumber(summary.conflicts)} 张` : null,
    summary.invalid > 0 ? `无效 ${formatNumber(summary.invalid)} 张` : null,
    summary.unknown > 0
      ? `不在任务中 ${formatNumber(summary.unknown)} 张`
      : null,
  ]
  return parts.filter((part) => part !== null).join("，")
}

/** 合并结果的摘要，如“合并 16 张，跳过已有标注 2 张，失败 1 张”。 */
export function mergeSummaryText(summary: ReviewMergeSummary) {
  const parts = [
    `合并 ${formatNumber(summary.merged)} 张`,
    summary.skipped_existing > 0
      ? `跳过已有标注 ${formatNumber(summary.skipped_existing)} 张`
      : null,
    summary.skipped_empty > 0
      ? `没有审图标注 ${formatNumber(summary.skipped_empty)} 张`
      : null,
    summary.conflicts > 0 ? `冲突 ${formatNumber(summary.conflicts)} 张` : null,
    summary.failed > 0 ? `失败 ${formatNumber(summary.failed)} 张` : null,
  ]
  return parts.filter((part) => part !== null).join("，")
}

/** 合并了哪些数据集：全部时为“全部数据集”，否则为名称（已不在任务中的按编号）。 */
export function mergeDatasetsText(
  merge: Pick<ReviewMerge, "dataset_ids">,
  datasets: readonly { id: number; name: string }[]
) {
  const ids = new Set(merge.dataset_ids)
  if (
    datasets.length > 1 &&
    datasets.every((dataset) => ids.has(dataset.id)) &&
    ids.size === datasets.length
  ) {
    return "全部数据集"
  }
  const names = merge.dataset_ids.map(
    (id) => datasets.find((dataset) => dataset.id === id)?.name ?? `#${id}`
  )
  return datasetNamesText(names.map((name) => ({ name })))
}

// ---- 识别信息 ----

/** 训练节点在识别完成时的报告（`Review.result`），未知的为 null。 */
export type InferenceReport = {
  device: string | null
  torchVersion: string | null
  ultralyticsVersion: string | null
  nodeName: string | null
  elapsedSeconds: number | null
  imagesPerSecond: number | null
}

const text = (value: unknown) =>
  typeof value === "string" && value.trim() !== "" ? value : null
const number = (value: unknown) =>
  typeof value === "number" && Number.isFinite(value) && value >= 0
    ? value
    : null

/** 读取 `Review.result`（结构由节点上报，逐个字段检查）。 */
export function inferenceReport(
  result: Review["result"]
): InferenceReport | null {
  if (result === null || typeof result !== "object") {
    return null
  }
  return {
    device: text(result.device),
    torchVersion: text(result.torch_version),
    ultralyticsVersion: text(result.ultralytics_version),
    nodeName: text(result.node_name),
    elapsedSeconds: number(result.elapsed_seconds),
    imagesPerSecond: number(result.images_per_second),
  }
}

/** 识别用时与速度，如“3 分钟 12 秒（12.5 张/秒）”；未知时为 null。 */
export function inferenceSpeedText(report: InferenceReport | null) {
  if (!report || report.elapsedSeconds === null) {
    return null
  }
  const duration = formatDuration(Math.round(report.elapsedSeconds))
  return report.imagesPerSecond !== null
    ? `${duration}（${Number(report.imagesPerSecond.toFixed(1))} 张/秒）`
    : duration
}
