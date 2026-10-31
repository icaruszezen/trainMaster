/**
 * YOLO 类别顺序（classes.txt）的解析与校验，规则与后端一致：每行一个类别名称，
 * 第 N 行对应类别编号 N-1。服务端的校验仍是最终结果。
 */
import type { DatasetImport, DatasetLabel } from "@/features/datasets/api"
import { characterCount } from "@/features/datasets/naming"

export const CLASS_NAME_MAX_LENGTH = 255
export const CLASS_LIST_MAX_COUNT = 1000
/** 读取类别文件的大小上限（1000 个最长的类别名称也远小于它）。 */
export const CLASS_FILE_MAX_BYTES = 2 * 1024 * 1024

export const CLASS_LIST_REQUIRED_MESSAGE =
  "YOLO 格式需要提供类别顺序（每行一个类别名称）"

// 与 Go 的 unicode.IsControl 一致：C0、DEL 与 C1 控制字符。
// eslint-disable-next-line no-control-regex -- 控制字符正是要拒绝的内容
const CONTROL_CHARS = /[\u0000-\u001f\u007f-\u009f]/

export type ClassListResult = {
  /** 校验通过时的类别列表（下标即类别编号）；有错误时为 `[]`。 */
  classes: string[]
  /** 中文错误说明；没有填写任何类别时为 {@link CLASS_LIST_REQUIRED_MESSAGE}。 */
  error: string | null
}

const fail = (error: string): ClassListResult => ({ classes: [], error })

/**
 * 解析文本框 / classes.txt 的内容。每行去掉首尾空白，开头和结尾的空行忽略，
 * 中间的空行是错误（否则后面的类别编号都会错位）。错误中的行号是原文中的行号。
 */
export function parseClassList(text: string): ClassListResult {
  const lines = text
    .replace(/^\uFEFF/, "")
    .split(/\r\n|\n|\r/)
    .map((line) => line.trim())
  let start = 0
  let end = lines.length
  while (start < end && lines[start] === "") {
    start += 1
  }
  while (end > start && lines[end - 1] === "") {
    end -= 1
  }
  if (start === end) {
    return fail(CLASS_LIST_REQUIRED_MESSAGE)
  }
  if (end - start > CLASS_LIST_MAX_COUNT) {
    return fail(`类别数量不能超过 ${CLASS_LIST_MAX_COUNT} 个`)
  }

  const classes: string[] = []
  const lineOf = new Map<string, number>()
  for (let index = start; index < end; index += 1) {
    const name = lines[index]
    const line = index + 1
    if (name === "") {
      return fail(
        `第 ${line} 行为空：类别编号按行计算，请删除空行或填写类别名称`
      )
    }
    if (characterCount(name) > CLASS_NAME_MAX_LENGTH) {
      return fail(
        `第 ${line} 行的类别名称不能超过 ${CLASS_NAME_MAX_LENGTH} 个字符`
      )
    }
    if (CONTROL_CHARS.test(name)) {
      return fail(`第 ${line} 行的类别名称包含制表符或控制字符`)
    }
    const previous = lineOf.get(name)
    if (previous !== undefined) {
      return fail(`类别“${name}”重复（第 ${previous} 行和第 ${line} 行）`)
    }
    lineOf.set(name, line)
    classes.push(name)
  }
  return { classes, error: null }
}

/** 类别列表 → 文本框内容（每行一个）。 */
export function toClassListText(classes: readonly string[]) {
  return classes.join("\n")
}

/**
 * 解码类别文件：先按 UTF-8（严格模式）解码，失败时按 GB18030（Windows
 * 中文系统的常见编码）解码。去掉 BOM，换行统一为 `\n`。
 */
export function decodeClassFile(bytes: ArrayBuffer | Uint8Array) {
  let text: string
  try {
    text = new TextDecoder("utf-8", { fatal: true }).decode(bytes)
  } catch {
    text = new TextDecoder("gb18030").decode(bytes)
  }
  return text.replace(/^\uFEFF/, "").replace(/\r\n?/g, "\n")
}

/** 读取用户选择的 classes.txt / .names 文件，失败时抛出带中文说明的 Error。 */
export async function readClassFile(file: Blob): Promise<string> {
  if (file.size > CLASS_FILE_MAX_BYTES) {
    throw new Error("类别文件不能超过 2 MB，请确认选择的是 classes.txt")
  }
  let bytes: ArrayBuffer
  try {
    bytes = await file.arrayBuffer()
  } catch {
    throw new Error("无法读取类别文件")
  }
  return decodeClassFile(bytes)
}

const byCodePoint = (a: string, b: string) => (a < b ? -1 : a > b ? 1 : 0)

/**
 * 下载为 YOLO 时的默认类别顺序：数据集中的标签按名称（码点）排序。给出
 * `base`（最近一次 YOLO 导入的类别顺序）时先沿用它，类别编号与导入时一致，
 * 其余标签按名称排在后面。
 */
export function defaultClassOrder(
  labels: readonly Pick<DatasetLabel, "name">[],
  base: readonly string[] = []
) {
  const known = new Set(base)
  const rest = labels
    .map((label) => label.name)
    .filter((name) => !known.has(name))
    .sort(byCodePoint)
  return [...base, ...rest]
}

/** 最近一次成功的 YOLO 导入（导入记录按时间倒序）；没有时为 undefined。 */
export function lastYoloImport<
  T extends Pick<DatasetImport, "annotation_format" | "status" | "classes">,
>(imports: readonly T[]) {
  return imports.find(
    (job) =>
      job.annotation_format === "yolo" &&
      job.status === "succeeded" &&
      job.classes.length > 0
  )
}

/**
 * 对照类别列表与数据集的标签：`missing` 是数据集中有、但不在列表里的标签
 * （它们的目标不会导出），`unused` 是列表中有、但数据集里没有目标的类别。
 */
export function compareClassesWithLabels<T extends Pick<DatasetLabel, "name">>(
  classes: readonly string[],
  labels: readonly T[]
): { missing: T[]; unused: string[] } {
  const classSet = new Set(classes)
  const labelNames = new Set(labels.map((label) => label.name))
  return {
    missing: labels.filter((label) => !classSet.has(label.name)),
    unused: classes.filter((name) => !labelNames.has(name)),
  }
}
