/** 下载数据集：选择导出格式（YOLO 需要类别顺序），由浏览器直接下载服务端生成的 zip。 */
import { useMemo, useRef, useState, type FormEvent } from "react"
import { useMutation, useQuery } from "@tanstack/react-query"
import { Download, Info, TriangleAlert } from "lucide-react"
import { toast } from "sonner"

import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { Spinner } from "@/components/ui/spinner"
import {
  createExport,
  importsQuery,
  labelsQuery,
  type AnnotationFormat,
  type Dataset,
} from "@/features/datasets/api"
import { AnnotationFormatPicker } from "@/features/datasets/annotation-format-picker"
import {
  compareClassesWithLabels,
  defaultClassOrder,
  lastYoloImport,
  parseClassList,
  toClassListText,
} from "@/features/datasets/class-list"
import { ClassListField } from "@/features/datasets/class-list-field"
import { formatBytes, formatNumber } from "@/lib/format"

/** 每种格式下载后的目录结构（与服务端的打包方式一致）。 */
const exportDescriptions: Record<AnnotationFormat, string> = {
  voc: "图片与配对的 .xml 按原有目录结构打包",
  xanylabeling:
    "图片按原有目录结构，有标注的图片旁生成同名 .json（X-AnyLabeling 4.x）",
  yolo: "Ultralytics 布局：images/、labels/（同名 .txt），附 classes.txt 与 data.yaml",
}

/** 警告中最多列出的标签数。 */
const LIST_LIMIT = 30

/** 交给浏览器原生下载：压缩包可能有几十 GB，不能读进内存。 */
function startDownload(url: string, fileName: string) {
  const link = document.createElement("a")
  link.href = url
  link.download = fileName
  link.style.display = "none"
  document.body.append(link)
  link.click()
  link.remove()
}

/** 数据集能否下载：文件夹存在且至少有一张图片。 */
export function canExportDataset(dataset: Dataset) {
  return !dataset.directory_missing && dataset.image_count > 0
}

type ExportDialogProps = {
  dataset: Dataset | null
  onOpenChange: (open: boolean) => void
}

export function ExportDialog({ dataset, onOpenChange }: ExportDialogProps) {
  return (
    <Dialog open={dataset !== null} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[calc(100dvh-2rem)] overflow-y-auto sm:max-w-2xl">
        {dataset && (
          <ExportForm dataset={dataset} onDone={() => onOpenChange(false)} />
        )}
      </DialogContent>
    </Dialog>
  )
}

function ExportForm({
  dataset,
  onDone,
}: {
  dataset: Dataset
  onDone: () => void
}) {
  const classFieldRef = useRef<HTMLTextAreaElement>(null)
  const [format, setFormat] = useState<AnnotationFormat>("voc")
  // 用户编辑过的类别顺序；null 表示沿用由数据集标签生成的默认顺序。
  const [classesDraft, setClassesDraft] = useState<string | null>(null)
  const [submitted, setSubmitted] = useState(false)

  const labels = useQuery(labelsQuery(dataset.id))
  const labelItems = labels.data?.items
  // 数据集由 YOLO 导入时，默认沿用导入时的类别顺序，类别编号保持不变。
  const imports = useQuery(importsQuery(dataset.id))
  const yoloImport = imports.data && lastYoloImport(imports.data.items)
  const baseClasses = yoloImport?.classes
  const defaultText = useMemo(
    () => toClassListText(defaultClassOrder(labelItems ?? [], baseClasses)),
    [labelItems, baseClasses]
  )
  const classesText = classesDraft ?? defaultText
  const classList = useMemo(() => parseClassList(classesText), [classesText])
  const coverage = useMemo(
    () =>
      labelItems && classList.error === null
        ? compareClassesWithLabels(classList.classes, labelItems)
        : null,
    [labelItems, classList]
  )

  const isYolo = format === "yolo"

  const mutation = useMutation({
    mutationFn: () =>
      createExport(
        dataset.id,
        isYolo ? { format, classes: classList.classes } : { format }
      ),
    onSuccess: (result) => {
      startDownload(result.url, result.file_name)
      toast.success("已开始下载", {
        description:
          "压缩包在下载时实时生成，数据集较大时需要一些时间，可在浏览器的下载列表中查看进度。",
      })
      onDone()
    },
  })

  const changeFormat = (next: AnnotationFormat) => {
    setFormat(next)
    mutation.reset()
  }
  const changeClasses = (text: string) => {
    setClassesDraft(text)
    mutation.reset()
  }

  const submit = (event: FormEvent) => {
    event.preventDefault()
    if (mutation.isPending) {
      return
    }
    setSubmitted(true)
    if (isYolo && classList.error) {
      classFieldRef.current?.focus()
      return
    }
    mutation.mutate()
  }

  const missing = coverage?.missing ?? []
  const missingObjects = missing.reduce(
    (sum, label) => sum + label.object_count,
    0
  )
  const unused = coverage?.unused ?? []

  return (
    <form onSubmit={submit} className="grid min-w-0 gap-6" noValidate>
      <DialogHeader>
        <DialogTitle>下载数据集</DialogTitle>
        <DialogDescription>
          将“{dataset.name}”的 {formatNumber(dataset.image_count)} 张图片（
          {formatBytes(dataset.total_bytes)}
          ）与标注打包为 zip。数据集在服务器上以 VOC
          格式保存，其它格式在下载时转换。
        </DialogDescription>
      </DialogHeader>

      <AnnotationFormatPicker
        label="下载格式"
        value={format}
        onChange={changeFormat}
        descriptions={exportDescriptions}
        disabled={mutation.isPending}
      />

      {isYolo && (
        <div className="grid gap-4">
          <ClassListField
            value={classesText}
            onChange={changeClasses}
            result={classList}
            showRequired={submitted}
            description={
              labels.isPending || imports.isPending
                ? "正在读取数据集中的标签…"
                : labels.error
                  ? `无法读取数据集中的标签（${labels.error.message}），请手动填写类别顺序。`
                  : yoloImport
                    ? `默认沿用导入“${yoloImport.file_name}”时的类别顺序，其余标签按名称排在后面；可直接编辑，或读取训练时使用的 classes.txt。`
                    : "默认按数据集中的标签名称排序；可直接编辑，或读取训练时使用的 classes.txt 以保持类别编号一致。"
            }
            onReset={
              classesDraft !== null && classesDraft !== defaultText
                ? () => setClassesDraft(null)
                : undefined
            }
            disabled={mutation.isPending}
            textareaRef={classFieldRef}
          />

          {missing.length > 0 && (
            <Alert>
              <TriangleAlert className="text-amber-600 dark:text-amber-500" />
              <AlertTitle>
                {formatNumber(missing.length)} 个标签不在类别列表中
              </AlertTitle>
              <AlertDescription>
                <p>
                  这些标签的目标不会导出（共 {formatNumber(missingObjects)}{" "}
                  个目标）：
                </p>
                <ul className="flex flex-wrap gap-1.5">
                  {missing.slice(0, LIST_LIMIT).map((label) => (
                    <li key={label.name} className="max-w-full">
                      <Badge
                        variant="outline"
                        className="max-w-56 font-normal"
                        title={label.name}
                      >
                        <span className="truncate">{label.name}</span>
                        <span className="text-muted-foreground tabular-nums">
                          {formatNumber(label.object_count)}
                        </span>
                      </Badge>
                    </li>
                  ))}
                  {missing.length > LIST_LIMIT && (
                    <li>
                      <Badge variant="secondary" className="font-normal">
                        还有 {formatNumber(missing.length - LIST_LIMIT)} 个
                      </Badge>
                    </li>
                  )}
                </ul>
              </AlertDescription>
            </Alert>
          )}

          {unused.length > 0 && (
            <p className="flex items-start gap-1.5 text-xs text-pretty text-muted-foreground">
              <Info className="mt-px size-3.5 shrink-0" />
              <span>
                数据集中没有这些类别的目标：
                {unused.slice(0, LIST_LIMIT).join("、")}
                {unused.length > LIST_LIMIT &&
                  ` 等 ${formatNumber(unused.length)} 个`}
                。它们仍会按顺序写入 classes.txt。
              </span>
            </p>
          )}
        </div>
      )}

      {format !== "voc" && dataset.invalid_count > 0 && (
        <p className="flex items-start gap-1.5 text-xs text-pretty text-muted-foreground">
          <TriangleAlert className="mt-px size-3.5 shrink-0 text-amber-600 dark:text-amber-500" />
          {formatNumber(dataset.invalid_count)}{" "}
          张图片的标注无法解析，导出时只包含图片。
        </p>
      )}

      {mutation.error && (
        <p role="alert" className="text-sm text-destructive">
          {mutation.error.message}
        </p>
      )}

      <DialogFooter>
        <Button
          type="button"
          variant="outline"
          onClick={onDone}
          disabled={mutation.isPending}
        >
          取消
        </Button>
        <Button type="submit" disabled={mutation.isPending}>
          {mutation.isPending ? <Spinner aria-hidden /> : <Download />}
          下载
        </Button>
      </DialogFooter>
    </form>
  )
}
