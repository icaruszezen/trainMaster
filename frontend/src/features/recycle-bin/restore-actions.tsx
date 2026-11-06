/**
 * 从回收站恢复：一键恢复，名称已被占用（409 `name_taken`）时打开换名对话框，
 * 用新名称再恢复一次。数据集换的是数据集名称，模型换的是随它恢复的项目名称。
 */
import { useId, useState, type FormEvent } from "react"
import { useMutation, useQuery } from "@tanstack/react-query"
import { useNavigate } from "react-router"
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
import {
  Field,
  FieldDescription,
  FieldError,
  FieldLabel,
} from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import { Spinner } from "@/components/ui/spinner"
import { datasetsQuery } from "@/features/datasets/api"
import { validateDatasetName } from "@/features/datasets/naming"
import { projectsQuery } from "@/features/projects/api"
import { validateProjectName } from "@/features/projects/naming"
import {
  refreshAfterDatasetMove,
  refreshAfterModelMove,
  refreshAfterVersionMove,
  refreshRecycleBin,
  restoreDataset,
  restoreModel,
  restoreVersion,
} from "@/features/recycle-bin/api"
import {
  suggestRestoreName,
  type RecycleItem,
} from "@/features/recycle-bin/recycle-format"
import { ApiError } from "@/lib/api"

/** 名称被占用、需要换名恢复的一项（审图版本没有名称冲突）。 */
type RenameTarget = {
  item: Extract<RecycleItem, { kind: "dataset" | "model" }>
  message: string
}

function isNameTaken(error: unknown) {
  return error instanceof ApiError && error.code === "name_taken"
}

/** 恢复一项（可以带新名称），成功后刷新相关的查询并提示，带“查看”。 */
function useRestoreMutation(onRestored?: () => void) {
  const navigate = useNavigate()
  return useMutation({
    mutationFn: async ({
      item,
      name,
    }: {
      item: RecycleItem
      name?: string
    }): Promise<void> => {
      switch (item.kind) {
        case "dataset": {
          const dataset = await restoreDataset(item.dataset.id, name)
          refreshAfterDatasetMove()
          toast.success(`已恢复数据集“${dataset.name}”`, {
            action: {
              label: "查看",
              onClick: () => navigate(`/datasets/${dataset.id}`),
            },
          })
          return
        }
        case "version": {
          const version = await restoreVersion(item.version.id)
          refreshAfterVersionMove(version.dataset_id)
          toast.success(`已恢复${item.title}`, {
            description: `回到数据集“${item.version.dataset_name}”的审图版本中。`,
            action: {
              label: "查看",
              onClick: () =>
                navigate(`/datasets/${version.dataset_id}?tab=versions`),
            },
          })
          return
        }
        case "model": {
          const model = await restoreModel(item.model.id, name)
          refreshAfterModelMove()
          toast.success(`已恢复模型“${model.project.name} v${model.version}”`, {
            description: item.model.project_deleted
              ? `项目“${model.project.name}”随之恢复。`
              : undefined,
            action: {
              label: "查看",
              onClick: () => navigate(`/models/${model.id}`),
            },
          })
          return
        }
      }
    },
    onSuccess: () => onRestored?.(),
    onError: (error) => {
      // 已经恢复或永久删除（404）、数据集也在回收站中（409）：列表已过期。
      if (error instanceof ApiError && error.status !== 0) {
        refreshRecycleBin()
      }
    },
  })
}

/**
 * 回收站页面的恢复操作：`restore(item)` 一键恢复；`pendingKey` 是正在恢复的
 * 一项；`dialogs` 是换名对话框，放在页面中。
 */
export function useRestoreActions() {
  const [rename, setRename] = useState<RenameTarget | null>(null)
  const mutation = useRestoreMutation()

  const restore = (item: RecycleItem) => {
    mutation.mutate(
      { item },
      {
        onError: (error) => {
          if (
            isNameTaken(error) &&
            (item.kind === "dataset" || item.kind === "model")
          ) {
            setRename({ item, message: error.message })
            return
          }
          toast.error(`无法恢复“${item.title}”`, {
            description: error.message,
          })
        },
      }
    )
  }

  return {
    restore,
    pendingKey: mutation.isPending ? mutation.variables.item.key : null,
    dialogs: (
      <Dialog
        open={rename !== null}
        onOpenChange={(open) => !open && setRename(null)}
      >
        <DialogContent className="sm:max-w-md">
          {rename && (
            <RenameRestoreForm
              key={rename.item.key}
              target={rename}
              onDone={() => setRename(null)}
            />
          )}
        </DialogContent>
      </Dialog>
    ),
  }
}

function RenameRestoreForm({
  target,
  onDone,
}: {
  target: RenameTarget
  onDone: () => void
}) {
  const inputId = useId()
  const { item } = target
  const isDataset = item.kind === "dataset"
  const original = isDataset ? item.dataset.name : item.model.project.name
  // 已有的名称（不区分大小写），用于建议一个没被占用的名称。
  const datasets = useQuery({ ...datasetsQuery(), enabled: isDataset })
  const projects = useQuery({ ...projectsQuery(), enabled: !isDataset })
  const taken = (isDataset ? datasets.data?.items : projects.data?.items)?.map(
    (entry) => entry.name
  )
  const [name, setName] = useState<string | null>(null)
  const value = name ?? suggestRestoreName(original, taken ?? [])
  const [submitted, setSubmitted] = useState(false)
  const mutation = useRestoreMutation(onDone)

  const validate = isDataset ? validateDatasetName : validateProjectName
  const serverError =
    mutation.error && isNameTaken(mutation.error)
      ? mutation.error.message
      : null
  const fieldError =
    (submitted || name !== null ? validate(value) : null) ?? serverError
  const otherError =
    mutation.error && !isNameTaken(mutation.error)
      ? mutation.error.message
      : null

  const submit = (event: FormEvent) => {
    event.preventDefault()
    setSubmitted(true)
    if (validate(value)) {
      return
    }
    mutation.mutate({ item, name: value.trim() })
  }

  return (
    <form onSubmit={submit} className="grid gap-6" noValidate>
      <DialogHeader>
        <DialogTitle className="break-words">
          {isDataset ? "换一个名称恢复数据集" : "换一个名称恢复项目"}
        </DialogTitle>
        <DialogDescription className="break-words">
          {target.message}
        </DialogDescription>
      </DialogHeader>
      <Field data-invalid={fieldError ? true : undefined}>
        <FieldLabel htmlFor={inputId}>
          {isDataset ? "数据集名称" : "项目名称"}
        </FieldLabel>
        <Input
          id={inputId}
          value={value}
          onChange={(event) => setName(event.target.value)}
          aria-invalid={fieldError ? true : undefined}
          disabled={mutation.isPending}
          autoComplete="off"
          autoFocus
        />
        {fieldError ? (
          <FieldError>{fieldError}</FieldError>
        ) : (
          <FieldDescription>
            {isDataset
              ? item.dataset.delete_files
                ? "数据集以新名称恢复；文件夹名被占用时，文件夹也改用新名称。"
                : "只改数据集的显示名称，磁盘上的文件夹不变。"
              : `模型“${item.title}”所属的项目随它恢复，项目以新名称恢复。`}
          </FieldDescription>
        )}
      </Field>
      {otherError && (
        <p role="alert" className="text-sm text-destructive">
          {otherError}
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
          恢复
        </Button>
      </DialogFooter>
    </form>
  )
}
