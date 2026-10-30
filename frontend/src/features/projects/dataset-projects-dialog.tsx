import { useId, useMemo, useState, type FormEvent } from "react"
import { useMutation, useQuery } from "@tanstack/react-query"
import { FolderKanban, Plus, Search, X } from "lucide-react"
import { toast } from "sonner"

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
import { Input } from "@/components/ui/input"
import {
  InputGroup,
  InputGroupAddon,
  InputGroupButton,
  InputGroupInput,
} from "@/components/ui/input-group"
import { Skeleton } from "@/components/ui/skeleton"
import { Spinner } from "@/components/ui/spinner"
import {
  datasetKeys,
  setDatasetProjects,
  type Dataset,
} from "@/features/datasets/api"
import { LoadError } from "@/features/datasets/load-error"
import { isNameTaken } from "@/features/datasets/naming"
import {
  cacheNewProject,
  createProject,
  projectsQuery,
  refreshMembership,
  type Project,
} from "@/features/projects/api"
import { validateProjectName } from "@/features/projects/naming"
import { formatNumber } from "@/lib/format"
import { queryClient } from "@/lib/query-client"
import { cn } from "@/lib/utils"

/** 超过这个数量时显示搜索框。 */
const SEARCH_THRESHOLD = 8

const nameCollator = new Intl.Collator("zh-CN", { numeric: true })

type DatasetProjectsDialogProps = {
  dataset: Dataset | null
  onOpenChange: (open: boolean) => void
}

/** 选择数据集纳入的项目（可多选），也可以在这里直接新建项目。 */
export function DatasetProjectsDialog({
  dataset,
  onOpenChange,
}: DatasetProjectsDialogProps) {
  return (
    <Dialog open={dataset !== null} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-md">
        {dataset && (
          <DatasetProjectsForm
            dataset={dataset}
            onDone={() => onOpenChange(false)}
          />
        )}
      </DialogContent>
    </Dialog>
  )
}

function sameSet(a: ReadonlySet<number>, b: ReadonlySet<number>) {
  return a.size === b.size && [...a].every((id) => b.has(id))
}

function DatasetProjectsForm({
  dataset,
  onDone,
}: {
  dataset: Dataset
  onDone: () => void
}) {
  const listId = useId()
  const projects = useQuery(projectsQuery())
  const initial = useMemo(
    () => new Set(dataset.projects.map((project) => project.id)),
    [dataset.projects]
  )
  const [selected, setSelected] = useState<Set<number>>(() => new Set(initial))
  const [search, setSearch] = useState("")
  const q = search.trim().toLowerCase()

  const sorted = useMemo(
    () =>
      [...(projects.data?.items ?? [])].sort((a, b) =>
        nameCollator.compare(a.name, b.name)
      ),
    [projects.data]
  )
  const visible = q
    ? sorted.filter((project) => project.name.toLowerCase().includes(q))
    : sorted

  // 只比较、提交列表中的项目：缓存中的数据集可能还带着已被删除的项目，
  // 它不在列表里，也就无法取消勾选。
  const known = new Set(sorted.map((project) => project.id))
  const chosen = new Set([...selected].filter((id) => known.has(id)))
  const unchanged = sameSet(
    chosen,
    new Set([...initial].filter((id) => known.has(id)))
  )

  const save = useMutation({
    mutationFn: () => setDatasetProjects(dataset.id, [...chosen]),
    onSuccess: (updated) => {
      queryClient.setQueryData(datasetKeys.detail(updated.id), updated)
      refreshMembership()
      toast.success(
        updated.projects.length > 0
          ? `“${updated.name}”已纳入 ${formatNumber(updated.projects.length)} 个项目`
          : `“${updated.name}”已不属于任何项目`
      )
      onDone()
    },
    // 多半是项目刚被别人删除：刷新后列表与勾选都是最新的。
    onError: () => refreshMembership(),
  })

  const toggle = (id: number, checked: boolean) => {
    setSelected((previous) => {
      const next = new Set(previous)
      if (checked) {
        next.add(id)
      } else {
        next.delete(id)
      }
      return next
    })
  }

  const submit = () => {
    if (unchanged) {
      onDone()
      return
    }
    save.mutate()
  }

  return (
    <div className="grid gap-5">
      <DialogHeader>
        <DialogTitle>纳入项目</DialogTitle>
        <DialogDescription>
          选择数据集“{dataset.name}”所属的项目，一个数据集可以属于多个项目。
        </DialogDescription>
      </DialogHeader>

      {projects.isPending ? (
        <div className="grid gap-2">
          {[0, 1, 2].map((row) => (
            <Skeleton key={row} className="h-9 w-full" />
          ))}
        </div>
      ) : projects.data === undefined ? (
        <LoadError
          title="无法获取项目列表"
          error={projects.error}
          retrying={projects.isFetching}
          onRetry={() => projects.refetch()}
        />
      ) : (
        <div className="grid gap-2">
          {sorted.length > SEARCH_THRESHOLD && (
            <InputGroup className="h-8">
              <InputGroupAddon>
                <Search />
              </InputGroupAddon>
              <InputGroupInput
                value={search}
                onChange={(event) => setSearch(event.target.value)}
                placeholder="搜索项目"
                aria-label="搜索项目"
                aria-controls={listId}
              />
              {search && (
                <InputGroupAddon align="inline-end">
                  <InputGroupButton
                    size="icon-xs"
                    onClick={() => setSearch("")}
                    aria-label="清除搜索"
                  >
                    <X />
                  </InputGroupButton>
                </InputGroupAddon>
              )}
            </InputGroup>
          )}
          {sorted.length === 0 ? (
            <div className="flex flex-col items-center gap-2 rounded-lg border border-dashed px-4 py-6 text-center text-sm text-muted-foreground">
              <FolderKanban className="size-5" />
              还没有项目，可以在下方直接新建。
            </div>
          ) : (
            <ul
              id={listId}
              aria-label="项目"
              className="grid max-h-64 gap-0.5 overflow-y-auto rounded-lg border p-1"
            >
              {visible.length === 0 ? (
                <li className="px-3 py-6 text-center text-sm text-muted-foreground">
                  没有名称包含“{search.trim()}”的项目
                </li>
              ) : (
                visible.map((project) => (
                  <ProjectOption
                    key={project.id}
                    project={project}
                    checked={selected.has(project.id)}
                    onCheckedChange={(checked) => toggle(project.id, checked)}
                    disabled={save.isPending}
                  />
                ))
              )}
            </ul>
          )}
          <QuickCreateProject
            existingNames={sorted.map((project) => project.name)}
            disabled={save.isPending}
            onCreated={(project) => {
              toggle(project.id, true)
              setSearch("")
            }}
          />
        </div>
      )}

      {save.error && (
        <p role="alert" className="text-sm text-destructive">
          {save.error.message}
        </p>
      )}
      <DialogFooter>
        <Button variant="outline" onClick={onDone} disabled={save.isPending}>
          取消
        </Button>
        <Button
          onClick={submit}
          disabled={save.isPending || projects.data === undefined}
        >
          {save.isPending && <Spinner aria-hidden />}
          保存
        </Button>
      </DialogFooter>
    </div>
  )
}

function ProjectOption({
  project,
  checked,
  onCheckedChange,
  disabled,
}: {
  project: Project
  checked: boolean
  onCheckedChange: (checked: boolean) => void
  disabled: boolean
}) {
  const id = useId()

  return (
    <li>
      <label
        htmlFor={id}
        className={cn(
          "flex cursor-pointer items-center gap-3 rounded-md px-2 py-2 transition-colors hover:bg-muted/60 has-[:focus-visible]:bg-muted/60",
          checked && "bg-muted/40",
          disabled && "pointer-events-none opacity-60"
        )}
      >
        <Checkbox
          id={id}
          checked={checked}
          onCheckedChange={(value) => onCheckedChange(value === true)}
          disabled={disabled}
        />
        <span className="min-w-0 flex-1 truncate text-sm font-medium">
          {project.name}
        </span>
        <span className="text-xs whitespace-nowrap text-muted-foreground tabular-nums">
          {formatNumber(project.dataset_count)} 个数据集
        </span>
      </label>
    </li>
  )
}

/** 在列表下方直接新建一个项目，创建后自动勾选。 */
function QuickCreateProject({
  existingNames,
  disabled,
  onCreated,
}: {
  existingNames: string[]
  disabled: boolean
  onCreated: (project: Project) => void
}) {
  const [name, setName] = useState("")
  const [submitted, setSubmitted] = useState(false)

  const create = useMutation({
    mutationFn: () => createProject({ name: name.trim() }),
    onSuccess: (project) => {
      // 立即出现在上方的列表中（并被勾选），不必等列表刷新。
      cacheNewProject(project)
      setName("")
      setSubmitted(false)
      onCreated(project)
    },
  })

  const localError =
    validateProjectName(name) ??
    (isNameTaken(name, existingNames) ? "已存在同名项目" : null)
  const error =
    (submitted && name.trim() !== "" ? localError : null) ??
    (create.error?.message || null)

  const submit = (event: FormEvent) => {
    event.preventDefault()
    // 表单在对话框里：不要触发外层的提交或关闭。
    event.stopPropagation()
    setSubmitted(true)
    if (localError) {
      return
    }
    create.mutate()
  }

  return (
    <form onSubmit={submit} className="grid gap-1.5" noValidate>
      <div className="flex gap-2">
        <Input
          value={name}
          onChange={(event) => {
            setName(event.target.value)
            create.reset()
          }}
          placeholder="新项目名称"
          aria-label="新项目名称"
          aria-invalid={error ? true : undefined}
          disabled={disabled || create.isPending}
          autoComplete="off"
          className="h-8"
        />
        <Button
          type="submit"
          variant="outline"
          size="sm"
          disabled={disabled || create.isPending || name.trim() === ""}
        >
          {create.isPending ? <Spinner aria-hidden /> : <Plus />}
          新建项目
        </Button>
      </div>
      {error && (
        <p role="alert" className="text-xs text-destructive">
          {error}
        </p>
      )}
    </form>
  )
}
