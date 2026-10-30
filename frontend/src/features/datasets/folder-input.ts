/**
 * 读取用户选择的文件夹：`<input webkitdirectory>` 与拖放（递归遍历目录）。
 * 结果中的路径相对于所选文件夹（已去掉文件夹自身的名字），用 `/` 分隔。
 */
import { isJunkDirectoryName } from "@/features/datasets/files"

export type PickedFile = { path: string; file: File; size: number }

export type PickedFolder = {
  /** 所选文件夹的名字；拖入多个文件 / 文件夹时为 `""`。 */
  name: string
  /** 按路径排序。 */
  files: PickedFile[]
}

const byPath = (a: PickedFile, b: PickedFile) =>
  a.path < b.path ? -1 : a.path > b.path ? 1 : 0

/**
 * `<input type="file" webkitdirectory>` 的选择结果。
 * `webkitRelativePath` 形如 `根文件夹/子目录/a.jpg`，去掉第一段作为相对路径。
 */
export function folderFromFileList(
  fileList: ArrayLike<Pick<File, "name" | "size" | "webkitRelativePath">>
): PickedFolder {
  let name = ""
  const files: PickedFile[] = []
  for (const file of Array.from(fileList)) {
    const relative = file.webkitRelativePath || file.name
    const slash = relative.indexOf("/")
    if (slash !== -1 && !name) {
      name = relative.slice(0, slash)
    }
    files.push({
      path: slash === -1 ? relative : relative.slice(slash + 1),
      file: file as File,
      size: file.size,
    })
  }
  return { name, files: files.sort(byPath) }
}

/**
 * 取出拖放的条目。必须在 drop 事件处理函数中同步调用：事件结束后
 * DataTransferItem 就失效了。
 */
export function entriesFromDataTransfer(
  dataTransfer: DataTransfer
): FileSystemEntry[] {
  const entries: FileSystemEntry[] = []
  for (const item of Array.from(dataTransfer.items)) {
    if (item.kind !== "file") {
      continue
    }
    const entry = item.webkitGetAsEntry()
    if (entry) {
      entries.push(entry)
    }
  }
  return entries
}

export function isDirectoryEntry(
  entry: FileSystemEntry
): entry is FileSystemDirectoryEntry {
  return entry.isDirectory
}

function isFileEntry(entry: FileSystemEntry): entry is FileSystemFileEntry {
  return entry.isFile
}

/** 读出目录的全部子条目：readEntries 每次最多返回一部分，要一直读到空为止。 */
function readAllEntries(directory: FileSystemDirectoryEntry) {
  const reader = directory.createReader()
  const entries: FileSystemEntry[] = []
  return new Promise<FileSystemEntry[]>((resolve, reject) => {
    const readBatch = () => {
      reader.readEntries((batch) => {
        if (batch.length === 0) {
          resolve(entries)
          return
        }
        entries.push(...batch)
        readBatch()
      }, reject)
    }
    readBatch()
  })
}

function fileOf(entry: FileSystemFileEntry) {
  return new Promise<File>((resolve, reject) => entry.file(resolve, reject))
}

/**
 * 递归读取拖放的条目。只拖入一个文件夹时，以它为根（路径不含它的名字）；
 * 拖入多个条目或零散文件时，路径从各条目自身算起。
 * 隐藏目录与 `__MACOSX` 不会进入（其中的文件不导入）。
 */
export async function folderFromEntries(
  entries: readonly FileSystemEntry[],
  onProgress?: (filesFound: number) => void
): Promise<PickedFolder> {
  const files: PickedFile[] = []

  const visit = async (entry: FileSystemEntry, prefix: string) => {
    if (isFileEntry(entry)) {
      const file = await fileOf(entry)
      files.push({ path: prefix + entry.name, file, size: file.size })
      onProgress?.(files.length)
    } else if (isDirectoryEntry(entry) && !isJunkDirectoryName(entry.name)) {
      await visitChildren(entry, `${prefix}${entry.name}/`)
    }
  }
  const visitChildren = async (
    directory: FileSystemDirectoryEntry,
    prefix: string
  ) => {
    const children = await readAllEntries(directory)
    await Promise.all(children.map((child) => visit(child, prefix)))
  }

  const [first] = entries
  if (entries.length === 1 && first && isDirectoryEntry(first)) {
    await visitChildren(first, "")
    return { name: first.name, files: files.sort(byPath) }
  }

  await Promise.all(entries.map((entry) => visit(entry, "")))
  return { name: "", files: files.sort(byPath) }
}
