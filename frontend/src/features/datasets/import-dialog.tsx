import { useId, useRef, useState, type FormEvent } from "react"
import { FileArchive, FolderUp } from "lucide-react"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { Label } from "@/components/ui/label"
import { RadioGroup, RadioGroupItem } from "@/components/ui/radio-group"
import { Spinner } from "@/components/ui/spinner"
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs"
import {
  createImport,
  type ConflictPolicy,
  type Dataset,
} from "@/features/datasets/api"
import {
  ImportFormatFields,
  useImportFormat,
} from "@/features/datasets/import-format-fields"
import { refreshStartedImport } from "@/features/datasets/import-watch"
import {
  ArchivePicker,
  FolderPicker,
  useFolderSummary,
  type FolderSelection,
} from "@/features/datasets/source-pickers"
import {
  startArchiveUpload,
  startFolderUpload,
} from "@/features/datasets/upload-manager"
import { cn } from "@/lib/utils"

type ImportTab = "archive" | "folder"

const conflictOptions: {
  value: ConflictPolicy
  label: string
  description: string
}[] = [
  {
    value: "skip",
    label: "跳过",
    description: "保留数据集中已有的图片与标注，不导入同名文件",
  },
  {
    value: "overwrite",
    label: "覆盖",
    description: "用导入的图片替换同名图片；导入中带标注时一并替换标注",
  },
]

type ImportDialogProps = {
  dataset: Dataset
  open: boolean
  onOpenChange: (open: boolean) => void
}

/** 向已有数据集导入压缩包或文件夹。每次打开时由调用方更换 key 以重置状态。 */
export function ImportDialog({
  dataset,
  open,
  onOpenChange,
}: ImportDialogProps) {
  const conflictId = useId()
  const classFieldRef = useRef<HTMLTextAreaElement>(null)
  const [tab, setTab] = useState<ImportTab>("archive")
  const [archive, setArchive] = useState<File | null>(null)
  const [folder, setFolder] = useState<FolderSelection | null>(null)
  const [onConflict, setOnConflict] = useState<ConflictPolicy>("skip")
  const [submitted, setSubmitted] = useState(false)
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const annotation = useImportFormat()
  const folderSummary = useFolderSummary(folder, annotation.format)

  const hasSource =
    tab === "archive"
      ? archive !== null
      : folderSummary !== null && folderSummary.images > 0
  const dirty = archive !== null || folder !== null || annotation.dirty

  const selectFolder = (selection: FolderSelection | null) => {
    setFolder(selection)
    annotation.prefillFromFolder(selection)
  }

  const submit = async (event: FormEvent) => {
    event.preventDefault()
    if (submitting || !hasSource) {
      return
    }
    setSubmitted(true)
    if (annotation.invalid) {
      classFieldRef.current?.focus()
      return
    }
    setSubmitting(true)
    setError(null)
    try {
      if (tab === "archive" && archive) {
        const job = await createImport(dataset.id, {
          source: "archive",
          file_name: archive.name,
          file_size: archive.size,
          on_conflict: onConflict,
          ...annotation.importFields,
        })
        startArchiveUpload({ dataset, job, file: archive })
      } else if (folder && folderSummary) {
        const { uploads, uploadBytes } = folderSummary
        const job = await createImport(dataset.id, {
          source: "folder",
          file_name: folder.name || dataset.dir_name,
          file_size: uploadBytes,
          file_count: uploads.length,
          on_conflict: onConflict,
          ...annotation.importFields,
        })
        startFolderUpload({ dataset, job, files: uploads })
      }
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "创建导入任务失败")
      setSubmitting(false)
      return
    }

    refreshStartedImport(dataset.id)
    toast.success("已开始上传", {
      description: "可在上传面板中查看进度；上传期间请不要关闭页面。",
    })
    setSubmitting(false)
    onOpenChange(false)
  }

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        if (!submitting) {
          onOpenChange(next)
        }
      }}
    >
      <DialogContent
        className="max-h-[calc(100dvh-2rem)] overflow-y-auto sm:max-w-2xl"
        onInteractOutside={(event) => {
          if (dirty || submitting) {
            event.preventDefault()
          }
        }}
        onDragOver={(event) => event.preventDefault()}
        onDrop={(event) => event.preventDefault()}
      >
        <form onSubmit={submit} className="grid min-w-0 gap-6" noValidate>
          <DialogHeader>
            <DialogTitle>导入数据</DialogTitle>
            <DialogDescription>
              导入到{" "}
              <code className="font-mono text-foreground">
                datasets/{dataset.dir_name}/
              </code>
              ，保持原有的子目录结构。标注可以是 VOC、X-AnyLabeling 或 YOLO
              格式，导入后统一转换为 VOC（.xml）保存。
            </DialogDescription>
          </DialogHeader>

          <Tabs
            value={tab}
            onValueChange={(value) => {
              setTab(value as ImportTab)
              setError(null)
              // 文件夹里的 classes.txt 只对上传文件夹有效。
              annotation.prefillFromFolder(value === "folder" ? folder : null)
            }}
            className="min-w-0 gap-4"
          >
            <TabsList className="grid w-full grid-cols-2">
              <TabsTrigger value="archive" disabled={submitting}>
                <FileArchive />
                压缩包
              </TabsTrigger>
              <TabsTrigger value="folder" disabled={submitting}>
                <FolderUp />
                文件夹
              </TabsTrigger>
            </TabsList>
            <TabsContent value="archive">
              <ArchivePicker
                file={archive}
                onChange={setArchive}
                format={annotation.format}
                disabled={submitting}
              />
            </TabsContent>
            <TabsContent value="folder">
              <FolderPicker
                selection={folder}
                summary={folderSummary}
                onChange={selectFolder}
                format={annotation.format}
                onFormatChange={annotation.setFormat}
                disabled={submitting}
              />
            </TabsContent>
          </Tabs>

          <ImportFormatFields
            state={annotation}
            showErrors={submitted}
            disabled={submitting}
            classFieldRef={classFieldRef}
          />

          <div className="grid gap-3">
            <Label id={conflictId}>同名文件</Label>
            <RadioGroup
              value={onConflict}
              onValueChange={(value) => setOnConflict(value as ConflictPolicy)}
              aria-labelledby={conflictId}
              className="grid gap-2 sm:grid-cols-2"
              disabled={submitting}
            >
              {conflictOptions.map((option) => {
                const itemId = `${conflictId}-${option.value}`
                return (
                  <label
                    key={option.value}
                    htmlFor={itemId}
                    className={cn(
                      "flex cursor-pointer items-start gap-3 rounded-lg border p-3 transition-colors hover:bg-muted/40",
                      onConflict === option.value &&
                        "border-primary/60 bg-muted/40"
                    )}
                  >
                    <RadioGroupItem
                      id={itemId}
                      value={option.value}
                      className="mt-0.5"
                    />
                    <span className="grid gap-1">
                      <span className="text-sm font-medium">
                        {option.label}
                        {option.value === "skip" && (
                          <span className="font-normal text-muted-foreground">
                            （默认）
                          </span>
                        )}
                      </span>
                      <span className="text-xs text-muted-foreground">
                        {option.description}
                      </span>
                    </span>
                  </label>
                )
              })}
            </RadioGroup>
          </div>

          {error && (
            <p role="alert" className="text-sm text-destructive">
              {error}
            </p>
          )}

          <DialogFooter>
            <Button
              type="button"
              variant="outline"
              onClick={() => onOpenChange(false)}
              disabled={submitting}
            >
              取消
            </Button>
            <Button type="submit" disabled={submitting || !hasSource}>
              {submitting && <Spinner aria-hidden />}
              开始导入
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
