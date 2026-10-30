import { useId, useMemo, useState, type FormEvent } from "react"
import { useMutation, useQuery } from "@tanstack/react-query"
import { Ellipsis, Pencil, Trash2 } from "lucide-react"
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
import {
  Field,
  FieldDescription,
  FieldError,
  FieldGroup,
  FieldLabel,
} from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import { Skeleton } from "@/components/ui/skeleton"
import { Spinner } from "@/components/ui/spinner"
import { Textarea } from "@/components/ui/textarea"
import { datasetsQuery } from "@/features/datasets/api"
import { DescriptionCounter } from "@/features/datasets/dataset-actions"
import { isNameTaken, validateDescription } from "@/features/datasets/naming"
import {
  cacheNewProject,
  createProject,
  deleteProject,
  projectKeys,
  projectsQuery,
  refreshMembership,
  updateProject,
  type Project,
} from "@/features/projects/api"
import { DatasetPicker } from "@/features/projects/dataset-picker"
import { validateProjectName } from "@/features/projects/naming"
import { ApiError } from "@/lib/api"
import { formatNumber } from "@/lib/format"
import { queryClient } from "@/lib/query-client"

type ProjectActionsMenuProps = {
  project: Project
  onEdit: () => void
  onDelete: () => void
  className?: string
}

export function ProjectActionsMenu({
  project,
  onEdit,
  onDelete,
  className,
}: ProjectActionsMenuProps) {
  return (
    // 非模态：菜单项打开对话框时避免两层焦点陷阱互相干扰。
    <DropdownMenu modal={false}>
      <DropdownMenuTrigger asChild>
        <Button
          variant="ghost"
          size="icon-sm"
          className={className}
          aria-label={`更多操作：${project.name}`}
        >
          <Ellipsis />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="w-40">
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
    return error.message.includes("描述")
      ? "description"
      : error.message.includes("名称")
        ? "name"
        : null
  }
  return error.code === "conflict" ? "name" : null
}

/** 新建项目，可同时选择纳入的数据集。表单在每次打开时从头开始。 */
export function CreateProjectDialog({
  open,
  onOpenChange,
  onCreated,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  /** 创建成功后调用（如跳转到项目详情）。 */
  onCreated?: (project: Project) => void
}) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-lg">
        <ProjectForm onDone={() => onOpenChange(false)} onCreated={onCreated} />
      </DialogContent>
    </Dialog>
  )
}

/** 修改项目的名称与描述。 */
export function EditProjectDialog({
  project,
  onOpenChange,
}: {
  project: Project | null
  onOpenChange: (open: boolean) => void
}) {
  return (
    <Dialog open={project !== null} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-md">
        {project && (
          <ProjectForm project={project} onDone={() => onOpenChange(false)} />
        )}
      </DialogContent>
    </Dialog>
  )
}

function ProjectForm({
  project,
  onDone,
  onCreated,
}: {
  project?: Project
  onDone: () => void
  onCreated?: (project: Project) => void
}) {
  const nameId = useId()
  const descriptionId = useId()
  const isEdit = project !== undefined
  const [name, setName] = useState(project?.name ?? "")
  const [description, setDescription] = useState(project?.description ?? "")
  const [datasetIds, setDatasetIds] = useState<Set<number>>(new Set())
  const [submitted, setSubmitted] = useState(false)

  const datasets = useQuery({ ...datasetsQuery(), enabled: !isEdit })
  // 列表刷新后已不存在的数据集（如刚被别人删除）不提交。
  const chosenDatasetIds =
    datasets.data?.items
      .filter((dataset) => datasetIds.has(dataset.id))
      .map((dataset) => dataset.id) ?? []

  const projects = useQuery(projectsQuery())
  const otherNames = useMemo(
    () =>
      projects.data?.items
        .filter((item) => item.id !== project?.id)
        .map((item) => item.name) ?? [],
    [projects.data, project?.id]
  )

  const mutation = useMutation({
    mutationFn: async () => {
      if (!isEdit) {
        return createProject({
          name: name.trim(),
          description: description.trim(),
          dataset_ids: chosenDatasetIds,
        })
      }
      const changes: { name?: string; description?: string } = {}
      if (name.trim() !== project.name) {
        changes.name = name.trim()
      }
      if (description.trim() !== project.description) {
        changes.description = description.trim()
      }
      return updateProject(project.id, changes)
    },
    onSuccess: (saved) => {
      if (isEdit) {
        queryClient.setQueryData(projectKeys.detail(saved.id), saved)
        if (saved.name !== project.name) {
          // 数据集上的项目标记显示项目名称。
          refreshMembership()
        } else {
          void queryClient.invalidateQueries({ queryKey: projectKeys.lists() })
        }
        toast.success("已保存项目信息")
      } else {
        cacheNewProject(saved)
        if (saved.dataset_count > 0) {
          refreshMembership()
        }
        toast.success(`已创建项目“${saved.name}”`, {
          description:
            saved.dataset_count > 0
              ? `已纳入 ${formatNumber(saved.dataset_count)} 个数据集。`
              : undefined,
        })
        onCreated?.(saved)
      }
      onDone()
    },
    // 新建时多半是所选数据集刚被删除：刷新后不再提交它，重试即可。
    onError: () => {
      if (!isEdit) {
        refreshMembership()
      }
    },
  })

  const serverField = mutation.error ? fieldOfServerError(mutation.error) : null
  const localNameError =
    validateProjectName(name) ??
    (isNameTaken(name, otherNames) ? "已存在同名项目" : null)
  const nameError =
    (submitted || name !== (project?.name ?? "") ? localNameError : null) ??
    (serverField === "name" ? mutation.error?.message : null)
  const descriptionError =
    validateDescription(description) ??
    (serverField === "description" ? mutation.error?.message : null)
  const unchanged =
    isEdit &&
    name.trim() === project.name &&
    description.trim() === project.description

  const submit = (event: FormEvent) => {
    event.preventDefault()
    setSubmitted(true)
    if (localNameError || validateDescription(description)) {
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
        <DialogTitle>{isEdit ? "编辑项目信息" : "新建项目"}</DialogTitle>
        <DialogDescription>
          {isEdit
            ? "修改项目的名称与描述，项目中的数据集不受影响。"
            : "项目用于按检测任务组织数据集。一个数据集可以纳入多个项目，之后也可以随时增减。"}
        </DialogDescription>
      </DialogHeader>
      <FieldGroup className="gap-5">
        <Field data-invalid={nameError ? true : undefined}>
          <FieldLabel htmlFor={nameId}>名称</FieldLabel>
          <Input
            id={nameId}
            value={name}
            onChange={(event) => setName(event.target.value)}
            placeholder="如：A 线外观缺陷检测"
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
            placeholder="检测对象、缺陷类型、产线等（可选）"
            aria-invalid={descriptionError ? true : undefined}
            disabled={mutation.isPending}
            className="min-h-20"
          />
          {descriptionError ? (
            <FieldError>{descriptionError}</FieldError>
          ) : (
            <DescriptionCounter value={description} />
          )}
        </Field>
        {!isEdit && (
          <InitialDatasetsField
            selected={datasetIds}
            onSelectedChange={setDatasetIds}
            disabled={mutation.isPending}
          />
        )}
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
          {isEdit ? "保存" : "创建项目"}
        </Button>
      </DialogFooter>
    </form>
  )
}

/** 新建项目时可选的数据集；还没有数据集时不显示。 */
function InitialDatasetsField({
  selected,
  onSelectedChange,
  disabled,
}: {
  selected: ReadonlySet<number>
  onSelectedChange: (selected: Set<number>) => void
  disabled: boolean
}) {
  const datasets = useQuery(datasetsQuery())

  if (datasets.isPending) {
    return <Skeleton className="h-24 w-full" />
  }
  if (!datasets.data || datasets.data.items.length === 0) {
    return null
  }
  return (
    <Field>
      <FieldLabel asChild>
        <span>纳入数据集</span>
      </FieldLabel>
      <FieldDescription>可选，之后也可以在项目页面添加。</FieldDescription>
      <DatasetPicker
        datasets={datasets.data.items}
        selected={selected}
        onSelectedChange={onSelectedChange}
        disabled={disabled}
        label="选择要纳入的数据集"
      />
    </Field>
  )
}

type DeleteProjectDialogProps = {
  project: Project | null
  onOpenChange: (open: boolean) => void
  onDeleted?: (project: Project) => void
}

/** 删除项目：只删除项目本身，其中的数据集保留。 */
export function DeleteProjectDialog({
  project,
  onOpenChange,
  onDeleted,
}: DeleteProjectDialogProps) {
  return (
    <AlertDialog open={project !== null} onOpenChange={onOpenChange}>
      <AlertDialogContent>
        {project && (
          <DeleteProjectForm
            project={project}
            onDeleted={() => {
              onOpenChange(false)
              onDeleted?.(project)
            }}
          />
        )}
      </AlertDialogContent>
    </AlertDialog>
  )
}

function DeleteProjectForm({
  project,
  onDeleted,
}: {
  project: Project
  onDeleted: () => void
}) {
  const mutation = useMutation({
    mutationFn: () => deleteProject(project.id),
    onSuccess: () => {
      queryClient.removeQueries({ queryKey: projectKeys.detail(project.id) })
      // 数据集上的项目标记与按项目筛选的结果都会变化。
      refreshMembership()
      toast.success(`已删除项目“${project.name}”`)
      onDeleted()
    },
  })

  return (
    <>
      <AlertDialogHeader>
        <AlertDialogTitle>删除项目“{project.name}”？</AlertDialogTitle>
        <AlertDialogDescription>
          {project.dataset_count > 0
            ? `项目中的 ${formatNumber(project.dataset_count)} 个数据集不会被删除，只是不再属于该项目。`
            : "该项目中还没有数据集。"}
        </AlertDialogDescription>
      </AlertDialogHeader>
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
          删除项目
        </Button>
      </AlertDialogFooter>
    </>
  )
}
