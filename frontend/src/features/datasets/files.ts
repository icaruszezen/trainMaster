/**
 * 文件识别与 VOC 标注配对，镜像后端 internal/dataset 的规则，用于上传前在
 * 浏览器里给出“将导入多少张图片、匹配多少个标注”的预估。真正的导入结果以
 * 服务端为准（例如多张图片共用一个 xml 时，服务端按 xml 的 `<filename>` 判断，
 * 这里按路径排序取第一张）。
 */

export const IMAGE_EXTENSIONS: ReadonlySet<string> = new Set([
  ".jpg",
  ".jpeg",
  ".png",
  ".bmp",
  ".gif",
  ".webp",
  ".tif",
  ".tiff",
  ".avif",
])

/** 浏览器能直接显示的格式：缩略图生成失败时可以回退到原图。 */
const BROWSER_NATIVE_EXTENSIONS: ReadonlySet<string> = new Set([
  ".jpg",
  ".jpeg",
  ".png",
  ".gif",
  ".webp",
  ".avif",
  ".bmp",
])

const JUNK_FILE_NAMES: ReadonlySet<string> = new Set([
  "thumbs.db",
  "desktop.ini",
])

/** `<input accept>` 用的压缩包扩展名列表（后端按文件头识别格式，不看扩展名）。 */
export const ARCHIVE_ACCEPT =
  ".zip,.tar,.gz,.tgz,.bz2,.tbz2,.xz,.txz,.zst,.tzst,.7z,.rar"

const ARCHIVE_FORMATS: [suffix: string, label: string][] = [
  [".tar.gz", "TAR.GZ"],
  [".tar.bz2", "TAR.BZ2"],
  [".tar.xz", "TAR.XZ"],
  [".tar.zst", "TAR.ZST"],
  [".tgz", "TAR.GZ"],
  [".tbz2", "TAR.BZ2"],
  [".txz", "TAR.XZ"],
  [".tzst", "TAR.ZST"],
  [".zip", "ZIP"],
  [".tar", "TAR"],
  [".gz", "GZ"],
  [".bz2", "BZ2"],
  [".xz", "XZ"],
  [".zst", "ZST"],
  [".7z", "7Z"],
  [".rar", "RAR"],
]

/** 压缩包格式标签（如 `TAR.GZ`）；不是支持的压缩包扩展名时返回 null。 */
export function archiveFormatLabel(fileName: string): string | null {
  const lower = fileName.toLowerCase()
  const match = ARCHIVE_FORMATS.find(([suffix]) => lower.endsWith(suffix))
  return match ? match[1] : null
}

/** 最后一级文件名。路径统一用 `/` 分隔。 */
export function baseName(path: string) {
  return path.slice(path.lastIndexOf("/") + 1)
}

/** 所在目录（不含结尾的 `/`），根目录下的文件为 `""`。 */
export function dirName(path: string) {
  const slash = path.lastIndexOf("/")
  return slash === -1 ? "" : path.slice(0, slash)
}

/** 小写扩展名（含点），没有扩展名或是以点开头的隐藏文件名时为 `""`。 */
export function extensionOf(path: string) {
  const name = baseName(path)
  const dot = name.lastIndexOf(".")
  return dot > 0 ? name.slice(dot).toLowerCase() : ""
}

function stemOf(name: string) {
  const dot = name.lastIndexOf(".")
  return dot > 0 ? name.slice(0, dot) : name
}

/**
 * 系统生成的垃圾文件：任一路径段以 `.` 开头（隐藏文件、macOS 的 `._foo`）、
 * `__MACOSX` 目录、`Thumbs.db`、`desktop.ini`。
 */
export function isJunkPath(path: string) {
  const segments = path.split("/")
  if (segments.some((s) => s.startsWith(".") || s === "__MACOSX")) {
    return true
  }
  return JUNK_FILE_NAMES.has(baseName(path).toLowerCase())
}

/** 是否应在遍历文件夹时整个跳过（不再深入）的目录名。 */
export function isJunkDirectoryName(name: string) {
  return name.startsWith(".") || name === "__MACOSX"
}

export type FileKind = "image" | "annotation" | "ignored"

export function classifyPath(path: string): FileKind {
  if (isJunkPath(path)) {
    return "ignored"
  }
  const extension = extensionOf(path)
  if (IMAGE_EXTENSIONS.has(extension)) {
    return "image"
  }
  return extension === ".xml" ? "annotation" : "ignored"
}

export function isBrowserNativeImage(path: string) {
  return BROWSER_NATIVE_EXTENSIONS.has(extensionOf(path))
}

const VOC_IMAGE_DIRS: ReadonlySet<string> = new Set(["jpegimages", "images"])

type XmlDirectory = {
  exact: Map<string, string>
  /** 小写文件名 → 路径；同名（忽略大小写）时保留排序靠前的一个。 */
  folded: Map<string, string>
}

const byPath = (a: string, b: string) => (a < b ? -1 : a > b ? 1 : 0)

/**
 * 把图片与 VOC 标注文件配对，返回 图片路径 → xml 路径。按顺序取第一个命中：
 *  1. 同目录、同名的 `.xml`（先精确匹配，再忽略大小写）；
 *  2. Pascal VOC 布局：图片位于 `JPEGImages`（或 `images`）目录时，
 *     找同级 `Annotations` 目录下的同名 `.xml`。
 * 一个 xml 只配给一张图片：按路径排序靠前的图片优先，其余视为未标注。
 */
export function pairAnnotations(
  imagePaths: readonly string[],
  xmlPaths: readonly string[]
): Map<string, string> {
  const directories = new Map<string, XmlDirectory>()
  // 父目录 → 其下名为 Annotations（不区分大小写）的目录列表。
  const annotationDirs = new Map<string, string[]>()

  for (const path of [...xmlPaths].sort(byPath)) {
    const dir = dirName(path)
    let entry = directories.get(dir)
    if (!entry) {
      entry = { exact: new Map(), folded: new Map() }
      directories.set(dir, entry)
      if (baseName(dir).toLowerCase() === "annotations") {
        const parent = dirName(dir)
        annotationDirs.set(parent, [...(annotationDirs.get(parent) ?? []), dir])
      }
    }
    const name = baseName(path)
    entry.exact.set(name, path)
    if (!entry.folded.has(name.toLowerCase())) {
      entry.folded.set(name.toLowerCase(), path)
    }
  }

  const lookup = (dir: string, fileName: string) => {
    const entry = directories.get(dir)
    return (
      entry?.exact.get(fileName) ?? entry?.folded.get(fileName.toLowerCase())
    )
  }

  const pairs = new Map<string, string>()
  const claimed = new Set<string>()
  for (const image of [...imagePaths].sort(byPath)) {
    const dir = dirName(image)
    const xmlName = `${stemOf(baseName(image))}.xml`

    let match = lookup(dir, xmlName)
    if (!match && VOC_IMAGE_DIRS.has(baseName(dir).toLowerCase())) {
      for (const annotationDir of annotationDirs.get(dirName(dir)) ?? []) {
        match = lookup(annotationDir, xmlName)
        if (match) {
          break
        }
      }
    }

    if (match && !claimed.has(match)) {
      claimed.add(match)
      pairs.set(image, match)
    }
  }
  return pairs
}

/** 文件夹上传中单个文件的上限：批量上传的单次请求不超过 256 MiB。 */
export const FOLDER_FILE_MAX_BYTES = 255 * 1024 * 1024

export type FolderFile = { path: string; size: number }

export type FolderSummary<T extends FolderFile> = {
  images: number
  /** VOC 标注候选（`.xml`）的数量。 */
  annotations: number
  /** 预计能配对上标注的图片数。 */
  matched: number
  /** 没有配对到图片的 xml 数量（服务端不会导入）。 */
  orphaned: number
  /** 既不是图片也不是 xml 的文件、系统垃圾文件：不上传。 */
  ignored: number
  /** 超过单文件上限、无法通过文件夹上传的图片 / xml。 */
  oversized: number
  /** 将上传的文件（图片与 xml），保持原有顺序。 */
  uploads: T[]
  uploadBytes: number
}

/** 统计一次文件夹选择：识别图片与标注、预估配对、挑出要上传的文件。 */
export function summarizeFolder<T extends FolderFile>(
  files: readonly T[]
): FolderSummary<T> {
  const uploads: T[] = []
  const imagePaths: string[] = []
  const xmlPaths: string[] = []
  let ignored = 0
  let oversized = 0
  let uploadBytes = 0

  for (const file of files) {
    const kind = classifyPath(file.path)
    if (kind === "ignored") {
      ignored += 1
      continue
    }
    if (file.size > FOLDER_FILE_MAX_BYTES) {
      oversized += 1
      continue
    }
    uploads.push(file)
    uploadBytes += file.size
    if (kind === "image") {
      imagePaths.push(file.path)
    } else {
      xmlPaths.push(file.path)
    }
  }

  const matched = pairAnnotations(imagePaths, xmlPaths).size
  return {
    images: imagePaths.length,
    annotations: xmlPaths.length,
    matched,
    orphaned: xmlPaths.length - matched,
    ignored,
    oversized,
    uploads,
    uploadBytes,
  }
}
