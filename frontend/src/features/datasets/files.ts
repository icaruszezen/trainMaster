/**
 * 文件识别与标注配对（VOC / X-AnyLabeling / YOLO），镜像后端 internal/dataset
 * 的规则，用于上传前在浏览器里给出“将导入多少张图片、匹配多少个标注”的预估。
 * 真正的导入结果以服务端为准（例如多张图片共用一个 xml / json 时，服务端按
 * `<filename>` / `imagePath` 判断，这里按路径排序取第一张）。
 */
import type { AnnotationFormat } from "@/features/datasets/api"

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

export const ANNOTATION_FORMATS: readonly AnnotationFormat[] = [
  "voc",
  "xanylabeling",
  "yolo",
]

/** 标注格式的显示名称。 */
export const annotationFormatLabels: Record<AnnotationFormat, string> = {
  voc: "Pascal VOC（.xml）",
  xanylabeling: "X-AnyLabeling（.json）",
  yolo: "YOLO（.txt）",
}

/** 简短名称，用于导入记录、统计等空间有限的地方。 */
export const annotationFormatNames: Record<AnnotationFormat, string> = {
  voc: "VOC",
  xanylabeling: "X-AnyLabeling",
  yolo: "YOLO",
}

/** 各格式的标注文件扩展名。 */
export const annotationExtensions: Record<AnnotationFormat, string> = {
  voc: ".xml",
  xanylabeling: ".json",
  yolo: ".txt",
}

/** YOLO 的类别表 `classes.txt`（不区分大小写），它不是标注文件。 */
export function isClassesFile(path: string) {
  return baseName(path).toLowerCase() === "classes.txt"
}

export type FileKind = "image" | "annotation" | "ignored"

/**
 * 按所选标注格式识别文件：图片、该格式的标注候选，其余（包括其它格式的标注
 * 文件，如选了 YOLO 时的 .xml）都忽略。
 */
export function classifyPath(
  path: string,
  format: AnnotationFormat = "voc"
): FileKind {
  if (isJunkPath(path)) {
    return "ignored"
  }
  const extension = extensionOf(path)
  if (IMAGE_EXTENSIONS.has(extension)) {
    return "image"
  }
  if (extension !== annotationExtensions[format]) {
    return "ignored"
  }
  return format === "yolo" && isClassesFile(path) ? "ignored" : "annotation"
}

export function isBrowserNativeImage(path: string) {
  return BROWSER_NATIVE_EXTENSIONS.has(extensionOf(path))
}

const VOC_IMAGE_DIRS: ReadonlySet<string> = new Set(["jpegimages", "images"])

type AnnotationDirectory = {
  exact: Map<string, string>
  /** 小写文件名 → 路径；同名（忽略大小写）时保留排序靠前的一个。 */
  folded: Map<string, string>
}

const byPath = (a: string, b: string) => (a < b ? -1 : a > b ? 1 : 0)

/**
 * Ultralytics 布局中图片目录对应的标注目录：把目录中最后一个名为 `images`
 * （不区分大小写）的段换成 `labels`；没有这样的段时返回 null。
 */
function ultralyticsLabelsDir(dir: string): string | null {
  const segments = dir === "" ? [] : dir.split("/")
  for (let index = segments.length - 1; index >= 0; index -= 1) {
    if (segments[index].toLowerCase() === "images") {
      segments[index] = "labels"
      return segments.join("/")
    }
  }
  return null
}

/**
 * 把图片与所选格式的标注文件配对，返回 图片路径 → 标注路径。按顺序取第一个命中：
 *  1. 同目录、同名的标注文件（先精确匹配，再忽略大小写）；
 *  2. VOC：图片位于 `JPEGImages`（或 `images`）目录时，找同级 `Annotations`
 *     目录下的同名 `.xml`；
 *     YOLO：Ultralytics 布局，把图片目录中最后一个 `images` 段换成 `labels`，
 *     在那里找同名 `.txt`（整条路径不区分大小写）；
 *     X-AnyLabeling：只有规则 1（服务端还会参考 json 中的 `imagePath`）。
 * 一个标注文件只配给一张图片：按路径排序靠前的图片优先，其余视为未标注。
 */
export function pairAnnotations(
  imagePaths: readonly string[],
  annotationPaths: readonly string[],
  format: AnnotationFormat = "voc"
): Map<string, string> {
  const extension = annotationExtensions[format]
  const directories = new Map<string, AnnotationDirectory>()
  // 父目录 → 其下名为 Annotations（不区分大小写）的目录列表（VOC）。
  const annotationDirs = new Map<string, string[]>()
  // 小写的完整路径 → 路径（YOLO 的 labels/ 目录）。
  const foldedPaths = new Map<string, string>()

  for (const path of [...annotationPaths].sort(byPath)) {
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
    if (!foldedPaths.has(path.toLowerCase())) {
      foldedPaths.set(path.toLowerCase(), path)
    }
  }

  const lookup = (dir: string, fileName: string) => {
    const entry = directories.get(dir)
    return (
      entry?.exact.get(fileName) ?? entry?.folded.get(fileName.toLowerCase())
    )
  }

  const candidateOf = (image: string): string | undefined => {
    const dir = dirName(image)
    const fileName = `${stemOf(baseName(image))}${extension}`
    const sameDir = lookup(dir, fileName)
    if (sameDir) {
      return sameDir
    }
    if (format === "voc" && VOC_IMAGE_DIRS.has(baseName(dir).toLowerCase())) {
      for (const annotationDir of annotationDirs.get(dirName(dir)) ?? []) {
        const match = lookup(annotationDir, fileName)
        if (match) {
          return match
        }
      }
    }
    if (format === "yolo") {
      const labelsDir = ultralyticsLabelsDir(dir)
      if (labelsDir !== null) {
        const path = labelsDir === "" ? fileName : `${labelsDir}/${fileName}`
        return foldedPaths.get(path.toLowerCase())
      }
    }
    return undefined
  }

  const pairs = new Map<string, string>()
  const claimed = new Set<string>()
  for (const image of [...imagePaths].sort(byPath)) {
    const match = candidateOf(image)
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
  /** 统计所依据的标注格式。 */
  format: AnnotationFormat
  images: number
  /** 所选格式的标注候选（`.xml` / `.json` / `.txt`）的数量。 */
  annotations: number
  /** 预计能配对上标注的图片数。 */
  matched: number
  /** 没有配对到图片的标注文件数量（服务端不会导入）。 */
  orphaned: number
  /**
   * 既不是图片也不是所选格式标注的文件（含 classes.txt、其它格式的标注）、
   * 系统垃圾文件：不上传。
   */
  ignored: number
  /** 超过单文件上限、无法通过文件夹上传的图片 / 标注。 */
  oversized: number
  /** 将上传的文件（图片与标注），保持原有顺序。 */
  uploads: T[]
  uploadBytes: number
}

/** 按所选格式统计一次文件夹选择：识别图片与标注、预估配对、挑出要上传的文件。 */
export function summarizeFolder<T extends FolderFile>(
  files: readonly T[],
  format: AnnotationFormat = "voc"
): FolderSummary<T> {
  const uploads: T[] = []
  const imagePaths: string[] = []
  const annotationPaths: string[] = []
  let ignored = 0
  let oversized = 0
  let uploadBytes = 0

  for (const file of files) {
    const kind = classifyPath(file.path, format)
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
      annotationPaths.push(file.path)
    }
  }

  const matched = pairAnnotations(imagePaths, annotationPaths, format).size
  return {
    format,
    images: imagePaths.length,
    annotations: annotationPaths.length,
    matched,
    orphaned: annotationPaths.length - matched,
    ignored,
    oversized,
    uploads,
    uploadBytes,
  }
}

/**
 * 按所选格式一个标注都没配对上、换一种格式却能配对时，建议改用那种格式
 * （取配对最多的一种）；否则返回 null。
 */
export function suggestAnnotationFormat<T extends FolderFile>(
  files: readonly T[],
  summary: Pick<FolderSummary<T>, "format" | "images" | "matched">
): { format: AnnotationFormat; matched: number } | null {
  if (summary.images === 0 || summary.matched > 0) {
    return null
  }
  let best: { format: AnnotationFormat; matched: number } | null = null
  for (const format of ANNOTATION_FORMATS) {
    if (format === summary.format) {
      continue
    }
    const { matched } = summarizeFolder(files, format)
    if (matched > (best?.matched ?? 0)) {
      best = { format, matched }
    }
  }
  return best
}

/**
 * 文件夹中的类别表 `classes.txt`（不区分大小写）：有多个时取层级最浅的，
 * 同一层取路径排序靠前的；没有时返回 null。
 */
export function findClassesFile<T extends FolderFile>(
  files: readonly T[]
): T | null {
  let best: T | null = null
  let bestDepth = Infinity
  for (const file of files) {
    if (!isClassesFile(file.path) || isJunkPath(file.path)) {
      continue
    }
    const depth = file.path.split("/").length
    if (
      depth < bestDepth ||
      (depth === bestDepth && best !== null && file.path < best.path)
    ) {
      best = file
      bestDepth = depth
    }
  }
  return best
}
