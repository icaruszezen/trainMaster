/**
 * 数据集命名规则，与后端 internal/dataset 的校验保持一致。
 * 新建（上传）的数据集同时以名称作为 datasets/ 下的文件夹名，因此规则兼顾
 * Windows 与 Linux 的文件名限制；服务端的校验仍是最终结果。
 */

export const DATASET_NAME_MAX_LENGTH = 64
export const DATASET_NAME_MAX_BYTES = 200
export const DATASET_DESCRIPTION_MAX_LENGTH = 500

const FORBIDDEN_CHARS = /[\\/:*?"<>|]/
// eslint-disable-next-line no-control-regex -- 控制字符正是要拒绝的内容
const CONTROL_CHARS = /[\u0000-\u001f\u007f]/
const FORBIDDEN_OR_CONTROL_GLOBAL =
  // eslint-disable-next-line no-control-regex -- 同上
  /[\\/:*?"<>|\u0000-\u001f\u007f]/g
// Windows 设备名，带扩展名（如 con.txt）也不行；Windows 会忽略扩展名前的
// 空格，所以“CON .txt”同样是保留名称（与后端 isWindowsReservedName 一致）。
const RESERVED_NAME = /^(con|prn|aux|nul|com[1-9]|lpt[1-9]) *(\.|$)/i

const encoder = new TextEncoder()

/** 按 Unicode 码点计数（与 Go 的 rune 计数一致），而不是 UTF-16 码元。 */
export function characterCount(value: string) {
  return Array.from(value).length
}

/**
 * 校验数据集名称，合法时返回 null，否则返回中文错误提示。
 * 名称会先去掉首尾空白（提交时同样会 trim）。
 */
export function validateDatasetName(raw: string): string | null {
  const name = raw.trim()
  if (name === "") {
    return "名称不能为空"
  }
  if (characterCount(name) > DATASET_NAME_MAX_LENGTH) {
    return `名称不能超过 ${DATASET_NAME_MAX_LENGTH} 个字符`
  }
  if (encoder.encode(name).length > DATASET_NAME_MAX_BYTES) {
    return "名称过长，请减少特殊字符（如表情符号）"
  }
  if (FORBIDDEN_CHARS.test(name) || CONTROL_CHARS.test(name)) {
    return '名称不能包含 \\ / : * ? " < > | 等字符'
  }
  if (name.startsWith(".")) {
    return "名称不能以“.”开头"
  }
  if (name.endsWith(".")) {
    return "名称不能以“.”或空格结尾"
  }
  if (RESERVED_NAME.test(name)) {
    return "该名称是系统保留名称"
  }
  return null
}

export function validateDescription(description: string): string | null {
  if (characterCount(description.trim()) > DATASET_DESCRIPTION_MAX_LENGTH) {
    return `描述不能超过 ${DATASET_DESCRIPTION_MAX_LENGTH} 个字符`
  }
  return null
}

/** 截断到不超过长度与字节数限制（按码点截断，不会切开代理对）。 */
function truncateName(name: string) {
  let result = ""
  let count = 0
  let bytes = 0
  for (const char of name) {
    const size = encoder.encode(char).length
    if (
      count + 1 > DATASET_NAME_MAX_LENGTH ||
      bytes + size > DATASET_NAME_MAX_BYTES
    ) {
      break
    }
    result += char
    count += 1
    bytes += size
  }
  return result
}

/**
 * 由文件名 / 文件夹名生成默认的数据集名称：替换非法字符、去掉开头的“.”
 * 与结尾的“.”和空格、截断到长度限制。结果可能仍不合法（如保留名称、
 * 空字符串），由 validateDatasetName 给出提示。
 */
export function suggestDatasetName(source: string) {
  const cleaned = source
    .replace(FORBIDDEN_OR_CONTROL_GLOBAL, "_")
    .trim()
    .replace(/^\.+/, "")
  return truncateName(cleaned)
    .replace(/[.\s]+$/, "")
    .trim()
}

/** 复合扩展名在前，保证 `a.tar.gz` 去掉的是 `.tar.gz` 而不是 `.gz`。 */
const ARCHIVE_SUFFIXES = [
  ".tar.gz",
  ".tar.bz2",
  ".tar.xz",
  ".tar.zst",
  ".tar.zstd",
  ".tgz",
  ".tbz2",
  ".tbz",
  ".txz",
  ".tzst",
]

/**
 * 压缩包文件名去掉扩展名：`批次1.tar.gz` → `批次1`、`data.v2.zip` → `data.v2`。
 * 没有扩展名时原样返回。
 */
export function archiveBaseName(fileName: string) {
  const lower = fileName.toLowerCase()
  for (const suffix of ARCHIVE_SUFFIXES) {
    if (lower.endsWith(suffix) && lower.length > suffix.length) {
      return fileName.slice(0, -suffix.length)
    }
  }
  const dot = fileName.lastIndexOf(".")
  return dot > 0 ? fileName.slice(0, dot) : fileName
}

/** 名称是否与已有名称重复（不区分大小写，与服务端一致）。 */
export function isNameTaken(name: string, existing: Iterable<string>) {
  const key = name.trim().toLowerCase()
  for (const candidate of existing) {
    if (candidate.trim().toLowerCase() === key) {
      return true
    }
  }
  return false
}
