/** 模型的操作：更多操作菜单、下载 PT、编辑说明与删除确认。列表页、项目页与详情页共用。 */
import { useId, useState, type FormEvent } from "react"
import { useMutation } from "@tanstack/react-query"
import {
  Download,
  Ellipsis,
  FileDown,
  Info,
  Pencil,
  Trash2,
} from "lucide-react"
import { Link } from "react-router"
import { toast } from "sonner"

import {
  AlertDialog,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog"
import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { Field, FieldError, FieldLabel } from "@/components/ui/field"
import { Spinner } from "@/components/ui/spinner"
import { Textarea } from "@/components/ui/textarea"
import { DescriptionCounter } from "@/features/datasets/dataset-actions"
import {
  deleteModel,
  modelKeys,
  updateModel,
  type Model,
  type ModelExport,
} from "@/features/models/api"
import {
  DownloadModelDialog,
  type DownloadFormat,
} from "@/features/models/download-model-dialog"
import { modelTitle } from "@/features/models/model-info"
import { validateModelDescription } from "@/features/models/naming"
import { modelFileName } from "@/features/models/yolo-versions"
import { refreshAfterModelMove, restoreModel } from "@/features/recycle-bin/api"
import { RECYCLE_RETENTION_DAYS } from "@/features/recycle-bin/recycle-format"
import { toastMovedToBin } from "@/features/recycle-bin/recycle-toast"
import { startDownload } from "@/lib/download"
import { formatBytes, formatNumber } from "@/lib/format"
import { queryClient } from "@/lib/query-client"

/** 直接下载模型的 PT 文件（`<项目名>_v<版本>.pt`）。 */
export function downloadModelFile(model: Model) {
  startDownload(
    model.download_url,
    modelFileName(model.project.name, model.version)
  )
  toast.success("已开始下载", {
    description: `${modelTitle(model)}（${formatBytes(model.size_bytes)}），可在浏览器的下载列表中查看进度。`,
  })
}

/** 列表与详情页上的操作，打开对应的对话框（见 useModelActions）。 */
export type ModelActions = {
  download: (model: Model, format: DownloadFormat) => void
  /** 打开下载对话框并显示某个转换的进度或失败原因。 */
  track: (model: Model, job: ModelExport) => void
  edit: (model: Model) => void
  remove: (model: Model) => void
}

type ModelActionsMenuProps = {
  model: Model
  actions: ModelActions
  /** 列表中显示“查看详情”；详情页不显示。 */
  showDetails?: boolean
  className?: string
}

export function ModelActionsMenu({
  model,
  actions,
  showDetails = true,
  className,
}: ModelActionsMenuProps) {
  return (
    // 非模态：菜单项打开对话框时避免两层焦点陷阱互相干扰。
    <DropdownMenu modal={false}>
      <DropdownMenuTrigger asChild>
        <Button
          variant="ghost"
          size="icon-sm"
          className={className}
          aria-label={`更多操作：${modelTitle(model)}`}
        >
          <Ellipsis />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="w-44">
        <DropdownMenuItem
          onSelect={() => actions.download(model, "pt")}
          disabled={model.file_missing}
        >
          <Download />
          下载 PT
        </DropdownMenuItem>
        <DropdownMenuItem
          onSelect={() => actions.download(model, "onnx")}
          disabled={model.file_missing}
        >
          <FileDown />
          下载 ONNX…
        </DropdownMenuItem>
        {showDetails && (
          <DropdownMenuItem asChild>
            <Link to={`/models/${model.id}`}>
              <Info />
              查看详情
            </Link>
          </DropdownMenuItem>
        )}
        <DropdownMenuItem onSelect={() => actions.edit(model)}>
          <Pencil />
          编辑说明
        </DropdownMenuItem>
        <DropdownMenuSeparator />
        <DropdownMenuItem
          variant="destructive"
          onSelect={() => actions.remove(model)}
        >
          <Trash2 />
          删除
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  )
}

/**
 * 页面上模型操作的状态与对话框：`actions` 交给列表 / 菜单，`dialogs` 渲染在页面中。
 * PT 直接下载；ONNX 打开下载对话框。
 */
export function useModelActions({
  onDeleted,
}: { onDeleted?: (model: Model) => void } = {}) {
  const [downloading, setDownloading] = useState<{
    model: Model
    format: DownloadFormat
    job: ModelExport | null
  } | null>(null)
  const [editing, setEditing] = useState<Model | null>(null)
  const [deleting, setDeleting] = useState<Model | null>(null)
  // 每次打开下载对话框换一个 key，选项从默认值开始。
  const [downloadKey, setDownloadKey] = useState(0)

  const actions: ModelActions = {
    download: (model, format) => {
      if (format === "pt") {
        downloadModelFile(model)
        return
      }
      setDownloadKey((key) => key + 1)
      setDownloading({ model, format, job: null })
    },
    track: (model, job) => {
      // 先写入缓存，对话框打开时直接显示（随后轮询最新状态）。
      queryClient.setQueryData(modelKeys.export(model.id, job.id), job)
      setDownloadKey((key) => key + 1)
      setDownloading({ model, format: "onnx", job })
    },
    edit: setEditing,
    remove: setDeleting,
  }

  const dialogs = (
    <>
      <DownloadModelDialog
        key={downloadKey}
        model={downloading?.model ?? null}
        initialFormat={downloading?.format ?? "onnx"}
        exportJob={downloading?.job ?? null}
        onOpenChange={(open) => !open && setDownloading(null)}
      />
      <EditModelDialog
        model={editing}
        onOpenChange={(open) => !open && setEditing(null)}
      />
      <DeleteModelDialog
        model={deleting}
        onOpenChange={(open) => !open && setDeleting(null)}
        onDeleted={onDeleted}
      />
    </>
  )

  return { actions, dialogs }
}

/** 修改模型的说明。 */
export function EditModelDialog({
  model,
  onOpenChange,
}: {
  model: Model | null
  onOpenChange: (open: boolean) => void
}) {
  return (
    <Dialog open={model !== null} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-md">
        {model && (
          <EditModelForm model={model} onDone={() => onOpenChange(false)} />
        )}
      </DialogContent>
    </Dialog>
  )
}

function EditModelForm({
  model,
  onDone,
}: {
  model: Model
  onDone: () => void
}) {
  const descriptionId = useId()
  const [description, setDescription] = useState(model.description)

  const mutation = useMutation({
    mutationFn: () =>
      updateModel(model.id, { description: description.trim() }),
    onSuccess: (saved) => {
      queryClient.setQueryData(modelKeys.detail(saved.id), saved)
      void queryClient.invalidateQueries({ queryKey: modelKeys.lists() })
      void queryClient.invalidateQueries({
        queryKey: modelKeys.project(saved.project.id),
      })
      toast.success("已保存说明")
      onDone()
    },
  })

  const error = validateModelDescription(description)
  const unchanged = description.trim() === model.description

  const submit = (event: FormEvent) => {
    event.preventDefault()
    if (error) {
      return
    }
    if (unchanged) {
      onDone()
      return
    }
    mutation.mutate()
  }

  return (
    <form onSubmit={submit} className="grid gap-6" noValidate>
      <DialogHeader>
        <DialogTitle>编辑说明</DialogTitle>
        <DialogDescription>
          {modelTitle(model)}：可以记录训练数据、改动与效果，方便区分各个版本。
        </DialogDescription>
      </DialogHeader>
      <Field data-invalid={error ? true : undefined}>
        <FieldLabel htmlFor={descriptionId}>说明</FieldLabel>
        <Textarea
          id={descriptionId}
          value={description}
          onChange={(event) => setDescription(event.target.value)}
          placeholder="如：补充夜班数据，mAP50 提升到 0.91（可选）"
          aria-invalid={error ? true : undefined}
          disabled={mutation.isPending}
          className="min-h-24"
          autoFocus
        />
        {error ? (
          <FieldError>{error}</FieldError>
        ) : (
          <DescriptionCounter value={description} />
        )}
      </Field>
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
          {mutation.isPending && <Spinner aria-hidden />}
          保存
        </Button>
      </DialogFooter>
    </form>
  )
}

/** 删除模型：移入回收站，到期后记录、模型文件与 ONNX 转换结果一起永久删除。 */
export function DeleteModelDialog({
  model,
  onOpenChange,
  onDeleted,
}: {
  model: Model | null
  onOpenChange: (open: boolean) => void
  onDeleted?: (model: Model) => void
}) {
  return (
    <AlertDialog open={model !== null} onOpenChange={onOpenChange}>
      <AlertDialogContent>
        {model && (
          <DeleteModelForm
            model={model}
            onDeleted={() => {
              onOpenChange(false)
              onDeleted?.(model)
            }}
          />
        )}
      </AlertDialogContent>
    </AlertDialog>
  )
}

function DeleteModelForm({
  model,
  onDeleted,
}: {
  model: Model
  onDeleted: () => void
}) {
  const title = modelTitle(model)
  const mutation = useMutation({
    mutationFn: () => deleteModel(model.id),
    onSuccess: () => {
      queryClient.removeQueries({ queryKey: modelKeys.detail(model.id) })
      // 使用它的审图任务等它恢复，也要刷新。
      refreshAfterModelMove()
      toastMovedToBin({
        title: `已将模型“${title}”移入回收站`,
        undo: () => restoreModel(model.id).then(refreshAfterModelMove),
        undoneTitle: `已恢复模型“${title}”`,
      })
      onDeleted()
    },
  })

  return (
    <>
      <AlertDialogHeader>
        <AlertDialogTitle className="break-words">
          删除模型“{title}”？
        </AlertDialogTitle>
        <AlertDialogDescription>
          模型会移入回收站，保留 {RECYCLE_RETENTION_DAYS}{" "}
          天，期间可以恢复；到期后自动永久删除模型文件 {model.file_name}（
          {formatBytes(model.size_bytes)}）
          {model.export_count > 0 &&
            `、${formatNumber(model.export_count)} 个 ONNX 转换结果`}
          与模型记录。
        </AlertDialogDescription>
      </AlertDialogHeader>
      <p className="text-sm text-muted-foreground">
        正在进行的转换会停止；使用它的审图任务等它恢复后再识别。版本号 v
        {model.version} 不会再分配给之后上传的模型。
      </p>
      {mutation.error && (
        <p role="alert" className="text-sm text-destructive">
          {mutation.error.message}
        </p>
      )}
      <AlertDialogFooter>
        <AlertDialogCancel disabled={mutation.isPending}>
          取消
        </AlertDialogCancel>
        <Button
          variant="destructive"
          onClick={() => mutation.mutate()}
          disabled={mutation.isPending}
        >
          {mutation.isPending && <Spinner aria-hidden />}
          移入回收站
        </Button>
      </AlertDialogFooter>
    </>
  )
}
