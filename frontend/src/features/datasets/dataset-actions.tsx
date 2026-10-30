import { useId, useState, type FormEvent } from "react"
import { useMutation } from "@tanstack/react-query"
import { Ellipsis, Pencil, RefreshCw, Trash2 } from "lucide-react"
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
import { Checkbox } from "@/components/ui/checkbox"
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
import {
  Field,
  FieldDescription,
  FieldError,
  FieldGroup,
  FieldLabel,
} from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import { Spinner } from "@/components/ui/spinner"
import { Textarea } from "@/components/ui/textarea"
import {
  datasetKeys,
  deleteDataset,
  scanDataset,
  updateDataset,
  type Dataset,
} from "@/features/datasets/api"
import {
  refreshStartedImport,
  watchImport,
} from "@/features/datasets/import-watch"
import {
  characterCount,
  DATASET_DESCRIPTION_MAX_LENGTH,
  validateDatasetName,
  validateDescription,
} from "@/features/datasets/naming"
import { ApiError } from "@/lib/api"
import { formatNumber } from "@/lib/format"
import { queryClient } from "@/lib/query-client"
import { cn } from "@/lib/utils"

/** 重新扫描数据集文件夹；开始后在后台观察任务，结束时提示结果。 */
export function useScanDataset() {
  return useMutation({
    mutationFn: (dataset: Dataset) => scanDataset(dataset.id),
    onSuccess: (job, dataset) => {
      queryClient.setQueryData<Dataset>(
        datasetKeys.detail(dataset.id),
        (old) => (old ? { ...old, active_import: job } : old)
      )
      refreshStartedImport(dataset.id)
      watchImport(job, dataset.name)
      toast(`已开始扫描“${dataset.name}”`, {
        description: "扫描在后台进行，完成后会提示结果。",
      })
    },
    onError: (error, dataset) => {
      toast.error(`无法扫描“${dataset.name}”`, { description: error.message })
    },
  })
}

type DatasetActionsMenuProps = {
  dataset: Dataset
  onEdit: () => void
  onDelete: () => void
  /** 不传时菜单中不显示“重新扫描”（详情页把它放在了工具栏上）。 */
  onScan?: () => void
  className?: string
}

export function DatasetActionsMenu({
  dataset,
  onEdit,
  onDelete,
  onScan,
  className,
}: DatasetActionsMenuProps) {
  return (
    // 非模态：菜单项打开对话框时避免两层焦点陷阱互相干扰。
    <DropdownMenu modal={false}>
      <DropdownMenuTrigger asChild>
        <Button
          variant="ghost"
          size="icon-sm"
          className={className}
          aria-label={`更多操作：${dataset.name}`}
        >
          <Ellipsis />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="w-40">
        {onScan && (
          <DropdownMenuItem
            onSelect={onScan}
            disabled={dataset.active_import !== null}
          >
            <RefreshCw />
            重新扫描
          </DropdownMenuItem>
        )}
        <DropdownMenuItem onSelect={onEdit}>
          <Pencil />
          编辑信息
        </DropdownMenuItem>
        <DropdownMenuSeparator />
        <DropdownMenuItem variant="destructive" onSelect={onDelete}>
          <Trash2 />
          删除
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  )
}

/** 服务端的校验错误归到对应的字段上。 */
function fieldOfServerError(error: Error): "name" | "description" | null {
  if (!(error instanceof ApiError)) {
    return null
  }
  if (error.code === "validation_failed") {
    return error.message.includes("描述") ? "description" : "name"
  }
  return error.code === "conflict" ? "name" : null
}

type EditDatasetDialogProps = {
  dataset: Dataset | null
  onOpenChange: (open: boolean) => void
}

/** 修改名称（只改显示名称，文件夹不变）与描述。 */
export function EditDatasetDialog({
  dataset,
  onOpenChange,
}: EditDatasetDialogProps) {
  return (
    <Dialog open={dataset !== null} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-md">
        {dataset && (
          <EditDatasetForm
            dataset={dataset}
            onDone={() => onOpenChange(false)}
          />
        )}
      </DialogContent>
    </Dialog>
  )
}

function EditDatasetForm({
  dataset,
  onDone,
}: {
  dataset: Dataset
  onDone: () => void
}) {
  const nameId = useId()
  const descriptionId = useId()
  const [name, setName] = useState(dataset.name)
  const [description, setDescription] = useState(dataset.description)
  const [submitted, setSubmitted] = useState(false)

  const mutation = useMutation({
    mutationFn: () => {
      const changes: { name?: string; description?: string } = {}
      if (name.trim() !== dataset.name) {
        changes.name = name.trim()
      }
      if (description.trim() !== dataset.description) {
        changes.description = description.trim()
      }
      return updateDataset(dataset.id, changes)
    },
    onSuccess: (updated) => {
      queryClient.setQueryData(datasetKeys.detail(updated.id), updated)
      void queryClient.invalidateQueries({ queryKey: datasetKeys.lists() })
      toast.success("已保存数据集信息")
      onDone()
    },
  })

  const serverField = mutation.error ? fieldOfServerError(mutation.error) : null
  const nameError =
    (submitted || name !== dataset.name ? validateDatasetName(name) : null) ??
    (serverField === "name" ? mutation.error?.message : null)
  const descriptionError =
    validateDescription(description) ??
    (serverField === "description" ? mutation.error?.message : null)
  const unchanged =
    name.trim() === dataset.name && description.trim() === dataset.description

  const submit = (event: FormEvent) => {
    event.preventDefault()
    setSubmitted(true)
    if (validateDatasetName(name) || validateDescription(description)) {
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
        <DialogTitle>编辑数据集信息</DialogTitle>
        <DialogDescription>
          修改的是显示名称，磁盘上的文件夹{" "}
          <code className="font-mono text-foreground">{dataset.dir_name}</code>{" "}
          保持不变。
        </DialogDescription>
      </DialogHeader>
      <FieldGroup className="gap-5">
        <Field data-invalid={nameError ? true : undefined}>
          <FieldLabel htmlFor={nameId}>名称</FieldLabel>
          <Input
            id={nameId}
            value={name}
            onChange={(event) => setName(event.target.value)}
            aria-invalid={nameError ? true : undefined}
            disabled={mutation.isPending}
            autoComplete="off"
          />
          {nameError && <FieldError>{nameError}</FieldError>}
        </Field>
        <Field data-invalid={descriptionError ? true : undefined}>
          <FieldLabel htmlFor={descriptionId}>描述</FieldLabel>
          <Textarea
            id={descriptionId}
            value={description}
            onChange={(event) => setDescription(event.target.value)}
            placeholder="数据来源、产线、批次等（可选）"
            aria-invalid={descriptionError ? true : undefined}
            disabled={mutation.isPending}
            className="min-h-24"
          />
          {descriptionError ? (
            <FieldError>{descriptionError}</FieldError>
          ) : (
            <DescriptionCounter value={description} />
          )}
        </Field>
      </FieldGroup>
      {mutation.error && !serverField && (
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

export function DescriptionCounter({ value }: { value: string }) {
  const count = characterCount(value.trim())
  return (
    <FieldDescription
      className={cn(
        "text-right text-xs tabular-nums",
        count > DATASET_DESCRIPTION_MAX_LENGTH && "text-destructive"
      )}
    >
      {count} / {DATASET_DESCRIPTION_MAX_LENGTH}
    </FieldDescription>
  )
}

type DeleteDatasetDialogProps = {
  dataset: Dataset | null
  onOpenChange: (open: boolean) => void
  onDeleted?: (dataset: Dataset) => void
}

export function DeleteDatasetDialog({
  dataset,
  onOpenChange,
  onDeleted,
}: DeleteDatasetDialogProps) {
  return (
    <AlertDialog open={dataset !== null} onOpenChange={onOpenChange}>
      <AlertDialogContent>
        {dataset && (
          <DeleteDatasetForm
            dataset={dataset}
            onDeleted={() => {
              onOpenChange(false)
              onDeleted?.(dataset)
            }}
          />
        )}
      </AlertDialogContent>
    </AlertDialog>
  )
}

function DeleteDatasetForm({
  dataset,
  onDeleted,
}: {
  dataset: Dataset
  onDeleted: () => void
}) {
  const checkboxId = useId()
  const [deleteFiles, setDeleteFiles] = useState(false)

  const mutation = useMutation({
    mutationFn: () => deleteDataset(dataset.id, deleteFiles),
    onSuccess: (result) => {
      queryClient.removeQueries({ queryKey: datasetKeys.detail(dataset.id) })
      void queryClient.invalidateQueries({ queryKey: datasetKeys.lists() })
      void queryClient.invalidateQueries({
        queryKey: datasetKeys.directories(),
      })
      if (result?.files_removed === false) {
        // 记录已删除，但文件夹没能完全删除（如权限不足）：说明留在了哪里。
        toast.warning(`已删除数据集“${dataset.name}”`, {
          description: result.message,
          duration: 15_000,
        })
      } else {
        toast.success(`已删除数据集“${dataset.name}”`, {
          description: deleteFiles ? "数据集文件夹已从磁盘删除。" : undefined,
        })
      }
      onDeleted()
    },
  })

  return (
    <>
      <AlertDialogHeader>
        <AlertDialogTitle>删除数据集“{dataset.name}”？</AlertDialogTitle>
        <AlertDialogDescription>
          将删除数据集记录、图片索引、导入记录与缩略图缓存。
        </AlertDialogDescription>
      </AlertDialogHeader>

      <div className="grid gap-3 text-sm">
        <label
          htmlFor={checkboxId}
          className={cn(
            "flex cursor-pointer items-start gap-3 rounded-lg border p-3 transition-colors",
            deleteFiles && "border-destructive/50 bg-destructive/5"
          )}
        >
          <Checkbox
            id={checkboxId}
            checked={deleteFiles}
            onCheckedChange={(checked) => setDeleteFiles(checked === true)}
            disabled={mutation.isPending}
            className="mt-0.5"
          />
          <span className="grid gap-1">
            <span className="font-medium">
              同时删除磁盘上的数据集文件夹（不可恢复）
            </span>
            <code className="font-mono text-xs break-all text-muted-foreground">
              {dataset.path}
            </code>
          </span>
        </label>
        {deleteFiles ? (
          <p className="text-destructive">
            整个文件夹将被永久删除，无法找回：包括其中的所有文件和子文件夹，不只是已索引的{" "}
            {formatNumber(dataset.image_count)} 张图片与标注文件。
          </p>
        ) : (
          <p className="text-muted-foreground">
            磁盘上的文件夹会保留，之后可以通过“添加已有目录”重新添加为数据集。
          </p>
        )}
        {mutation.error && (
          <p role="alert" className="text-destructive">
            {mutation.error.message}
          </p>
        )}
      </div>

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
          {deleteFiles ? "删除数据集和文件夹" : "删除数据集"}
        </Button>
      </AlertDialogFooter>
    </>
  )
}
