/** 选择导入来源：压缩包、文件夹（点击选择或拖放），以及文件夹的预检摘要。 */
import {
  useMemo,
  useRef,
  useState,
  type DragEvent,
  type ReactNode,
} from "react"
import {
  CircleAlert,
  FileArchive,
  Folder,
  FolderUp,
  Info,
  TriangleAlert,
  X,
  type LucideIcon,
} from "lucide-react"

import { Button } from "@/components/ui/button"
import { Spinner } from "@/components/ui/spinner"
import type { AnnotationFormat } from "@/features/datasets/api"
import {
  annotationExtensions,
  annotationFormatNames,
  ARCHIVE_ACCEPT,
  archiveFormatLabel,
  suggestAnnotationFormat,
  summarizeFolder,
  type FolderSummary,
} from "@/features/datasets/files"
import {
  entriesFromDataTransfer,
  folderFromEntries,
  folderFromFileList,
  isDirectoryEntry,
  type PickedFile,
} from "@/features/datasets/folder-input"
import {
  formatBytes,
  formatNumber,
  formatPercent,
  percentOf,
} from "@/lib/format"
import { cn } from "@/lib/utils"

type DropzoneProps = {
  icon: LucideIcon
  title: string
  hint: ReactNode
  buttonLabel: string
  onBrowse: () => void
  onDropData: (dataTransfer: DataTransfer) => void
  busy?: ReactNode
  error?: string | null
  disabled?: boolean
}

/**
 * 拖放区域。整个区域可点击选择；键盘用户使用其中的按钮。
 * 用计数器跟踪 dragenter / dragleave，避免经过子元素时高亮闪烁。
 */
function Dropzone({
  icon: Icon,
  title,
  hint,
  buttonLabel,
  onBrowse,
  onDropData,
  busy,
  error,
  disabled,
}: DropzoneProps) {
  const depth = useRef(0)
  const [dragging, setDragging] = useState(false)

  const onDragEnter = (event: DragEvent) => {
    event.preventDefault()
    depth.current += 1
    setDragging(true)
  }
  const onDragLeave = () => {
    depth.current = Math.max(0, depth.current - 1)
    if (depth.current === 0) {
      setDragging(false)
    }
  }
  const onDragOver = (event: DragEvent) => {
    event.preventDefault()
    event.dataTransfer.dropEffect = disabled ? "none" : "copy"
  }
  const onDrop = (event: DragEvent) => {
    event.preventDefault()
    depth.current = 0
    setDragging(false)
    if (!disabled) {
      onDropData(event.dataTransfer)
    }
  }

  return (
    <div className="grid gap-2">
      <div
        onClick={disabled || busy ? undefined : onBrowse}
        onDragEnter={onDragEnter}
        onDragLeave={onDragLeave}
        onDragOver={onDragOver}
        onDrop={onDrop}
        aria-busy={busy ? true : undefined}
        className={cn(
          "flex min-h-48 cursor-pointer flex-col items-center justify-center gap-3 rounded-xl border-2 border-dashed px-6 py-8 text-center transition-colors",
          "hover:border-ring/60 hover:bg-muted/40",
          dragging && "border-primary bg-accent",
          error && "border-destructive/50",
          (disabled || busy) && "cursor-default"
        )}
      >
        {busy ? (
          <div className="flex flex-col items-center gap-3 text-sm text-muted-foreground">
            <Spinner className="size-6" />
            {busy}
          </div>
        ) : (
          <>
            <div
              className={cn(
                "flex size-12 items-center justify-center rounded-full bg-muted text-muted-foreground transition-colors",
                dragging && "bg-background text-foreground"
              )}
            >
              <Icon className="size-6" />
            </div>
            <div className="grid gap-1">
              <p className="text-sm font-medium">{title}</p>
              <p className="text-xs text-muted-foreground">{hint}</p>
            </div>
            <Button
              type="button"
              variant="outline"
              size="sm"
              disabled={disabled}
              onClick={(event) => {
                event.stopPropagation()
                onBrowse()
              }}
            >
              {buttonLabel}
            </Button>
          </>
        )}
      </div>
      {error && (
        <p
          role="alert"
          className="flex items-start gap-1.5 text-sm text-destructive"
        >
          <CircleAlert className="mt-0.5 size-4 shrink-0" />
          {error}
        </p>
      )}
    </div>
  )
}

function SelectedSource({
  icon: Icon,
  title,
  meta,
  onRemove,
  removeLabel,
  disabled,
  children,
}: {
  icon: LucideIcon
  title: string
  meta: ReactNode
  onRemove: () => void
  removeLabel: string
  disabled?: boolean
  children?: ReactNode
}) {
  return (
    <div className="grid gap-4 rounded-xl border bg-muted/30 p-4">
      <div className="flex items-center gap-3">
        <div className="flex size-10 shrink-0 items-center justify-center rounded-lg border bg-background text-muted-foreground">
          <Icon className="size-5" />
        </div>
        <div className="min-w-0 flex-1">
          <p className="truncate text-sm font-medium" title={title}>
            {title}
          </p>
          <p className="text-xs text-muted-foreground tabular-nums">{meta}</p>
        </div>
        <Button
          type="button"
          variant="ghost"
          size="icon-sm"
          onClick={onRemove}
          disabled={disabled}
          aria-label={removeLabel}
        >
          <X />
        </Button>
      </div>
      {children}
    </div>
  )
}

/** 压缩包中会导入哪些文件（随所选标注格式变化）。 */
const archiveNotes: Record<AnnotationFormat, string> = {
  voc: "上传后在服务器上解压，只导入图片与 VOC 标注（.xml）",
  xanylabeling:
    "上传后在服务器上解压，只导入图片与 X-AnyLabeling 标注（.json），标注转换为 VOC（.xml）保存",
  yolo: "上传后在服务器上解压，只导入图片与 YOLO 标注（.txt），按类别顺序转换为 VOC（.xml）保存",
}

/** 文件夹中的哪些文件会被识别为标注（随所选标注格式变化）。 */
const folderHints: Record<AnnotationFormat, string> = {
  voc: "包含所有子文件夹；与图片同目录、同名的 .xml 会被识别为 VOC 标注",
  xanylabeling:
    "包含所有子文件夹；与图片同目录、同名的 .json 会被识别为 X-AnyLabeling 标注",
  yolo: "包含所有子文件夹；与图片同目录或对应 labels/ 目录下同名的 .txt 会被识别为 YOLO 标注",
}

type ArchivePickerProps = {
  file: File | null
  onChange: (file: File | null) => void
  /** 压缩包中标注文件的格式。 */
  format: AnnotationFormat
  disabled?: boolean
}

export function ArchivePicker({
  file,
  onChange,
  format,
  disabled,
}: ArchivePickerProps) {
  const inputRef = useRef<HTMLInputElement>(null)
  const [error, setError] = useState<string | null>(null)

  const accept = (candidate: File) => {
    if (!archiveFormatLabel(candidate.name)) {
      setError(
        "不支持的文件类型，请选择 zip、tar、tar.gz、tar.bz2、tar.xz、tar.zst、7z 或 rar 压缩包"
      )
      return
    }
    if (candidate.size === 0) {
      setError("压缩包是空文件")
      return
    }
    setError(null)
    onChange(candidate)
  }

  const onDropData = (dataTransfer: DataTransfer) => {
    const entries = entriesFromDataTransfer(dataTransfer)
    if (entries.some(isDirectoryEntry)) {
      setError("拖入的是文件夹，请切换到“上传文件夹”")
      return
    }
    const files = Array.from(dataTransfer.files)
    if (files.length !== 1) {
      setError(files.length === 0 ? "没有读取到文件" : "一次只能导入一个压缩包")
      return
    }
    accept(files[0])
  }

  if (file) {
    return (
      <SelectedSource
        icon={FileArchive}
        title={file.name}
        meta={`${archiveFormatLabel(file.name) ?? "压缩包"} · ${formatBytes(file.size)}`}
        onRemove={() => onChange(null)}
        removeLabel="移除压缩包"
        disabled={disabled}
      >
        <p className="flex items-start gap-1.5 text-xs text-pretty text-muted-foreground">
          <Info className="mt-px size-3.5 shrink-0" />
          {archiveNotes[format]}；若压缩包内只有一个顶层文件夹，会去掉这一层。
        </p>
      </SelectedSource>
    )
  }

  return (
    <>
      <Dropzone
        icon={FileArchive}
        title="拖放压缩包到这里，或点击选择"
        hint="支持 zip、tar、tar.gz、tar.bz2、tar.xz、tar.zst、7z、rar，大文件分块上传"
        buttonLabel="选择压缩包"
        onBrowse={() => inputRef.current?.click()}
        onDropData={onDropData}
        error={error}
        disabled={disabled}
      />
      <input
        ref={inputRef}
        type="file"
        accept={ARCHIVE_ACCEPT}
        className="sr-only"
        tabIndex={-1}
        aria-hidden
        onChange={(event) => {
          const selected = event.target.files?.[0]
          event.target.value = ""
          if (selected) {
            accept(selected)
          }
        }}
      />
    </>
  )
}

/** 选中的文件夹。预检摘要随所选标注格式变化，见 {@link useFolderSummary}。 */
export type FolderSelection = {
  /** 文件夹名；拖入多个条目时为 `""`。 */
  name: string
  /** 选中的全部文件（含会被忽略的），按路径排序。 */
  files: PickedFile[]
}

/** 按所选标注格式统计文件夹：要上传哪些文件、预计配对多少标注。 */
export function useFolderSummary(
  selection: FolderSelection | null,
  format: AnnotationFormat
) {
  return useMemo(
    () => (selection ? summarizeFolder(selection.files, format) : null),
    [selection, format]
  )
}

type FolderPickerProps = {
  selection: FolderSelection | null
  /** `useFolderSummary(selection, format)` 的结果。 */
  summary: FolderSummary<PickedFile> | null
  onChange: (selection: FolderSelection | null) => void
  /** 文件夹中标注文件的格式。 */
  format: AnnotationFormat
  /** 传入时，所选格式配对不上而其它格式可以时提示改用。 */
  onFormatChange?: (format: AnnotationFormat) => void
  disabled?: boolean
}

export function FolderPicker({
  selection,
  summary,
  onChange,
  format,
  onFormatChange,
  disabled,
}: FolderPickerProps) {
  const inputRef = useRef<HTMLInputElement | null>(null)
  const [reading, setReading] = useState<number | null>(null)
  const [error, setError] = useState<string | null>(null)
  const suggestion = useMemo(
    () =>
      selection && summary && onFormatChange
        ? suggestAnnotationFormat(selection.files, summary)
        : null,
    [selection, summary, onFormatChange]
  )

  const select = (name: string, files: PickedFile[]) => {
    if (files.length === 0) {
      setError("所选文件夹是空的")
      return
    }
    setError(null)
    onChange({ name, files })
  }

  const onDropData = (dataTransfer: DataTransfer) => {
    // 必须同步取出条目：drop 事件结束后 DataTransferItem 就失效了。
    const entries = entriesFromDataTransfer(dataTransfer)
    if (entries.length === 0) {
      setError("没有读取到文件夹，请改用“选择文件夹”")
      return
    }
    setReading(0)
    setError(null)
    folderFromEntries(entries, (found) => {
      if (found % 200 === 0) {
        setReading(found)
      }
    })
      .then((folder) => select(folder.name, folder.files))
      .catch(() => setError("无法读取拖入的文件夹，请改用“选择文件夹”"))
      .finally(() => setReading(null))
  }

  if (selection && summary) {
    return (
      <SelectedSource
        icon={Folder}
        title={selection.name || "拖入的文件"}
        meta={`共 ${formatNumber(selection.files.length)} 个文件 · 将上传 ${formatBytes(summary.uploadBytes)}`}
        onRemove={() => onChange(null)}
        removeLabel="重新选择文件夹"
        disabled={disabled}
      >
        <FolderSummaryView summary={summary} />
        {suggestion && onFormatChange && (
          <p className="flex flex-wrap items-center gap-x-1 gap-y-0.5 text-xs text-muted-foreground">
            <Info className="size-3.5 shrink-0" />按{" "}
            {annotationFormatNames[summary.format]} 格式没有配对到标注；按{" "}
            {annotationFormatNames[suggestion.format]} 格式可配对{" "}
            {formatNumber(suggestion.matched)} 张图片。
            <Button
              type="button"
              variant="link"
              size="sm"
              className="h-auto px-0 py-0 text-xs underline"
              onClick={() => onFormatChange(suggestion.format)}
              disabled={disabled}
            >
              改用 {annotationFormatNames[suggestion.format]}
            </Button>
          </p>
        )}
      </SelectedSource>
    )
  }

  return (
    <>
      <Dropzone
        icon={FolderUp}
        title="拖放文件夹到这里，或点击选择"
        hint={folderHints[format]}
        buttonLabel="选择文件夹"
        onBrowse={() => inputRef.current?.click()}
        onDropData={onDropData}
        error={error}
        disabled={disabled}
        busy={
          reading !== null &&
          (reading > 0
            ? `正在读取文件夹…已发现 ${formatNumber(reading)} 个文件`
            : "正在读取文件夹…")
        }
      />
      <input
        ref={(input) => {
          inputRef.current = input
          // React 不认识 webkitdirectory 属性，直接设置到 DOM 上。
          if (input) {
            input.webkitdirectory = true
          }
        }}
        type="file"
        multiple
        className="sr-only"
        tabIndex={-1}
        aria-hidden
        onChange={(event) => {
          const { files } = event.target
          if (files && files.length > 0) {
            const folder = folderFromFileList(files)
            select(folder.name, folder.files)
          }
          event.target.value = ""
        }}
      />
    </>
  )
}

function SummaryTile({
  label,
  value,
  detail,
}: {
  label: string
  value: number
  detail?: string
}) {
  return (
    <div className="rounded-lg border bg-background px-3 py-2.5">
      <p className="text-xs text-muted-foreground">{label}</p>
      <p className="mt-0.5 text-lg leading-tight font-semibold tabular-nums">
        {formatNumber(value)}
      </p>
      {detail && (
        <p className="mt-0.5 text-xs text-muted-foreground tabular-nums">
          {detail}
        </p>
      )}
    </div>
  )
}

/** 文件夹预检：图片 / 标注数量、预计配对结果与会被忽略的文件。 */
export function FolderSummaryView({
  summary,
}: {
  summary: FolderSummary<PickedFile>
}) {
  const extension = annotationExtensions[summary.format]
  const notes: { tone: "warning" | "info"; text: string }[] = []
  if (summary.oversized > 0) {
    notes.push({
      tone: "warning",
      text: `${formatNumber(summary.oversized)} 个文件超过 255 MB，无法通过文件夹上传，可打包为压缩包后导入`,
    })
  }
  if (summary.orphaned > 0) {
    notes.push({
      tone: "info",
      text: `${formatNumber(summary.orphaned)} 个标注文件没有同名图片，不会导入`,
    })
  }
  if (summary.ignored > 0) {
    notes.push({
      tone: "info",
      text: `${formatNumber(summary.ignored)} 个其他文件（非图片、非 ${extension} 标注或系统文件）不会上传`,
    })
  }

  return (
    <div className="grid gap-3">
      <div className="grid grid-cols-2 gap-2 sm:grid-cols-4">
        <SummaryTile label="图片" value={summary.images} />
        <SummaryTile
          label={`${annotationFormatNames[summary.format]} 标注`}
          value={summary.annotations}
        />
        <SummaryTile
          label="已配对"
          value={summary.matched}
          detail={
            summary.images > 0
              ? `占图片 ${formatPercent(percentOf(summary.matched, summary.images))}`
              : undefined
          }
        />
        <SummaryTile label="忽略" value={summary.ignored + summary.oversized} />
      </div>
      {summary.images === 0 ? (
        <p
          role="alert"
          className="flex items-start gap-1.5 text-sm text-destructive"
        >
          <CircleAlert className="mt-0.5 size-4 shrink-0" />
          没有找到可导入的图片（支持 jpg、png、bmp、gif、webp、tif、avif）
        </p>
      ) : (
        notes.length > 0 && (
          <ul className="grid gap-1.5 text-xs text-muted-foreground">
            {notes.map((note) => (
              <li key={note.text} className="flex items-start gap-1.5">
                {note.tone === "warning" ? (
                  <TriangleAlert className="mt-px size-3.5 shrink-0 text-amber-600 dark:text-amber-500" />
                ) : (
                  <Info className="mt-px size-3.5 shrink-0" />
                )}
                {note.text}
              </li>
            ))}
          </ul>
        )
      )}
    </div>
  )
}
