/**
 * 数据集详情的“审图版本”标签页：基础版本（数据集自己的标注文件）与合并审图任务
 * 生成的审图版本（审图1.0、1.1…，新的在前）。审图版本可以修改备注、删除，下载
 * 时可以只导出版本中需加样的图片，或用版本覆盖基础版本中对应图片的标注后一起
 * 导出；在下载对话框中还可以多选版本组合导出。
 */
import { useId, useState, type FormEvent } from "react"
import { useMutation, useQuery } from "@tanstack/react-query"
import {
  Download,
  Ellipsis,
  FileStack,
  GitMerge,
  Layers,
  Pencil,
  ScanEye,
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
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import {
  Empty,
  EmptyContent,
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from "@/components/ui/empty"
import { Field, FieldError, FieldLabel } from "@/components/ui/field"
import { Skeleton } from "@/components/ui/skeleton"
import { Spinner } from "@/components/ui/spinner"
import { Textarea } from "@/components/ui/textarea"
import {
  datasetKeys,
  datasetVersionsQuery,
  deleteDatasetVersion,
  updateDatasetVersion,
  type Dataset,
  type DatasetVersion,
  type VersionSelection,
} from "@/features/datasets/api"
import { DescriptionCounter } from "@/features/datasets/dataset-actions"
import { canExportDataset } from "@/features/datasets/export-dialog"
import { LoadError } from "@/features/datasets/load-error"
import {
  BASE_ONLY,
  readyVersions,
  versionCountsText,
  versionName,
  versionSelection,
  versionSourceText,
} from "@/features/datasets/versions"
import {
  refreshAfterVersionMove,
  refreshRecycleBin,
  restoreVersion,
} from "@/features/recycle-bin/api"
import { RECYCLE_RETENTION_DAYS } from "@/features/recycle-bin/recycle-format"
import { toastMovedToBin } from "@/features/recycle-bin/recycle-toast"
import { reviewKeys } from "@/features/reviews/api"
import { reviewDetailPath } from "@/features/reviews/params"
import {
  statusesText,
  validateVersionNote,
} from "@/features/reviews/review-format"
import type { ListResponse } from "@/lib/api"
import { formatDateTime, formatNumber, formatRelativeTime } from "@/lib/format"
import { queryClient } from "@/lib/query-client"

type DatasetReviewVersionsProps = {
  dataset: Dataset
  /** 打开下载对话框，选中这些版本。 */
  onExport: (selection: VersionSelection) => void
  /** 切换到“审核记录”标签页。 */
  onShowReviews: () => void
}

export function DatasetReviewVersions({
  dataset,
  onExport,
  onShowReviews,
}: DatasetReviewVersionsProps) {
  const query = useQuery(datasetVersionsQuery(dataset.id))
  const [editing, setEditing] = useState<DatasetVersion | null>(null)
  const [deleting, setDeleting] = useState<DatasetVersion | null>(null)
  const exportable = canExportDataset(dataset)

  if (query.isPending) {
    return <Skeleton className="h-48 w-full rounded-xl" />
  }
  if (query.data === undefined) {
    return (
      <LoadError
        title="无法获取审图版本"
        error={query.error}
        retrying={query.isFetching}
        onRetry={() => query.refetch()}
      />
    )
  }

  const items = query.data.items
  if (items.length === 0) {
    return (
      <Empty className="border">
        <EmptyHeader>
          <EmptyMedia variant="icon">
            <FileStack />
          </EmptyMedia>
          <EmptyTitle className="text-base">还没有审图版本</EmptyTitle>
          <EmptyDescription>
            在“审核记录”中把审图任务合并到数据集时，会生成这个数据集的审图版本（审图1.0、审图1.1…）。下载时可以选择版本，只导出需加样的图片，或覆盖基础版本中对应图片的标注后一起导出。
          </EmptyDescription>
        </EmptyHeader>
        <EmptyContent>
          <Button size="sm" variant="outline" onClick={onShowReviews}>
            <ScanEye />
            查看审核记录
          </Button>
        </EmptyContent>
      </Empty>
    )
  }

  const ready = readyVersions(items)
  return (
    <div className="grid gap-3">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <p className="min-w-0 text-sm text-pretty text-muted-foreground">
          每个审图版本保存合并时的审图标注，不修改数据集的标注文件（基础版本）。下载时可以多选版本：同一张图片以新版本的标注为准。
        </p>
        <Button
          size="sm"
          variant="outline"
          onClick={() =>
            onExport(
              versionSelection(
                true,
                ready.map((version) => version.id)
              )
            )
          }
          disabled={!exportable || ready.length === 0}
          title="下载基础版本与审图版本的组合，可在对话框中调整选择"
        >
          <Download />
          选择版本下载
        </Button>
      </div>

      <ul className="grid grid-cols-[minmax(0,1fr)] gap-3">
        <li>
          <BaseVersionCard
            dataset={dataset}
            exportable={exportable}
            onExport={onExport}
          />
        </li>
        {items.map((version) => (
          <li key={version.id}>
            <VersionCard
              version={version}
              exportable={exportable}
              onExport={onExport}
              onEdit={() => setEditing(version)}
              onDelete={() => setDeleting(version)}
            />
          </li>
        ))}
      </ul>

      <EditVersionNoteDialog
        datasetId={dataset.id}
        version={editing}
        onOpenChange={(open) => !open && setEditing(null)}
      />
      <DeleteVersionDialog
        datasetId={dataset.id}
        version={deleting}
        onOpenChange={(open) => !open && setDeleting(null)}
      />
    </div>
  )
}

const cardClass =
  "grid grid-cols-[minmax(0,1fr)] gap-3 rounded-xl border bg-card p-4 text-card-foreground shadow-sm"

function BaseVersionCard({
  dataset,
  exportable,
  onExport,
}: {
  dataset: Dataset
  exportable: boolean
  onExport: (selection: VersionSelection) => void
}) {
  return (
    <article className={cardClass}>
      <div className="flex min-w-0 flex-wrap items-center gap-2">
        <Layers className="size-4 text-muted-foreground" aria-hidden />
        <h3 className="font-medium">基础版本</h3>
        <Badge variant="secondary" className="font-normal">
          数据集的标注文件
        </Badge>
      </div>
      <p className="text-sm text-pretty text-muted-foreground">
        数据集文件夹中的图片与标注文件：
        <span className="tabular-nums">
          {formatNumber(dataset.image_count)} 张图片，
          {formatNumber(dataset.annotated_count)} 张有标注，
          {formatNumber(dataset.object_count)} 个目标
        </span>
        。标注工作台与标签分布显示的是基础版本。
      </p>
      <div className="flex flex-wrap justify-end gap-1">
        <Button
          variant="outline"
          size="sm"
          onClick={() => onExport(BASE_ONLY)}
          disabled={!exportable}
        >
          <Download />
          下载基础版本
        </Button>
      </div>
    </article>
  )
}

function VersionCard({
  version,
  exportable,
  onExport,
  onEdit,
  onDelete,
}: {
  version: DatasetVersion
  exportable: boolean
  onExport: (selection: VersionSelection) => void
  onEdit: () => void
  onDelete: () => void
}) {
  const building = version.status === "building"
  const name = versionName(version)
  const source = versionSourceText(version)
  const time = version.finished_at ?? version.created_at
  const statuses = statusesText(version.source.statuses)

  return (
    <article className={cardClass}>
      <div className="flex min-w-0 items-start gap-2">
        <div className="flex min-w-0 flex-1 flex-wrap items-center gap-2">
          <GitMerge className="size-4 text-muted-foreground" aria-hidden />
          <h3 className="font-medium">{name}</h3>
          {building ? (
            <Badge variant="outline" className="font-normal">
              <Spinner className="size-3" aria-hidden />
              正在生成
            </Badge>
          ) : (
            <Badge variant="secondary" className="font-normal">
              审图版本
            </Badge>
          )}
        </div>
        {!building && (
          // 非模态：菜单项打开对话框时避免两层焦点陷阱互相干扰。
          <DropdownMenu modal={false}>
            <DropdownMenuTrigger asChild>
              <Button
                variant="ghost"
                size="icon-sm"
                className="-my-1"
                aria-label={`更多操作：${name}`}
              >
                <Ellipsis />
              </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end" className="w-40">
              <DropdownMenuItem onSelect={onEdit}>
                <Pencil />
                修改备注
              </DropdownMenuItem>
              {version.review && (
                <DropdownMenuItem asChild>
                  <Link to={reviewDetailPath(version.review.id)}>
                    <ScanEye />
                    查看审图任务
                  </Link>
                </DropdownMenuItem>
              )}
              <DropdownMenuSeparator />
              <DropdownMenuItem variant="destructive" onSelect={onDelete}>
                <Trash2 />
                删除版本
              </DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>
        )}
      </div>

      {version.note && (
        <p className="text-sm break-words whitespace-pre-line">
          {version.note}
        </p>
      )}

      <div className="grid gap-0.5 text-xs text-muted-foreground">
        <p className="tabular-nums">
          {building
            ? "合并还在进行，完成后编号并统计图片数。"
            : versionCountsText(version)}
        </p>
        <p className="truncate" title={`${source}，合并${statuses}的图片`}>
          {source} · 合并{statuses}的图片
        </p>
        <p title={formatDateTime(time)}>
          {building ? "开始于" : "生成于"} {formatRelativeTime(time)}
        </p>
      </div>

      {!building && (
        <div className="flex flex-wrap justify-end gap-1">
          <Button
            variant="outline"
            size="sm"
            onClick={() => onExport(versionSelection(false, [version.id]))}
            disabled={!exportable || version.needs_sample_count === 0}
            title={
              version.needs_sample_count === 0
                ? "这个版本中没有需加样的图片"
                : "只导出这个版本中需加样的图片与它们的审图标注"
            }
          >
            <Download />
            只导出需加样
          </Button>
          <Button
            variant="outline"
            size="sm"
            onClick={() => onExport(versionSelection(true, [version.id]))}
            disabled={!exportable}
            title="导出数据集的全部图片：这个版本中有的图片用版本的标注，其他图片用基础版本的标注"
          >
            <Download />
            覆盖基础版本导出
          </Button>
        </div>
      )}
    </article>
  )
}

/** 版本修改或删除后刷新：版本列表、下载的统计、数据集的版本数与合并记录。 */
function refreshVersions(datasetId: number) {
  void queryClient.invalidateQueries({
    queryKey: datasetKeys.detail(datasetId),
  })
  void queryClient.invalidateQueries({ queryKey: datasetKeys.lists() })
  void queryClient.invalidateQueries({ queryKey: reviewKeys.all })
}

function EditVersionNoteDialog({
  datasetId,
  version,
  onOpenChange,
}: {
  datasetId: number
  version: DatasetVersion | null
  onOpenChange: (open: boolean) => void
}) {
  return (
    <Dialog open={version !== null} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-md">
        {version && (
          <EditNoteForm
            datasetId={datasetId}
            version={version}
            onDone={() => onOpenChange(false)}
          />
        )}
      </DialogContent>
    </Dialog>
  )
}

function EditNoteForm({
  datasetId,
  version,
  onDone,
}: {
  datasetId: number
  version: DatasetVersion
  onDone: () => void
}) {
  const noteId = useId()
  const [note, setNote] = useState(version.note)
  // 备注为空的提示在第一次提交后才显示。
  const [submitted, setSubmitted] = useState(false)
  const noteError = validateVersionNote(note)
  const shownError =
    noteError && (submitted || note.trim() !== "") ? noteError : null

  const mutation = useMutation({
    mutationFn: () =>
      updateDatasetVersion(datasetId, version.id, { note: note.trim() }),
    onSuccess: (updated) => {
      queryClient.setQueryData<ListResponse<DatasetVersion>>(
        datasetKeys.versions(datasetId),
        (list) =>
          list && {
            ...list,
            items: list.items.map((item) =>
              item.id === updated.id ? updated : item
            ),
          }
      )
      refreshVersions(datasetId)
      toast.success(`已修改${updated.name}的备注`)
      onDone()
    },
  })

  const submit = (event: FormEvent) => {
    event.preventDefault()
    setSubmitted(true)
    if (noteError === null && !mutation.isPending) {
      mutation.mutate()
    }
  }

  return (
    <form onSubmit={submit} className="grid min-w-0 gap-6" noValidate>
      <DialogHeader>
        <DialogTitle>修改{versionName(version)}的备注</DialogTitle>
        <DialogDescription>
          备注说明这个版本的内容，下载时选择版本可以看到。
        </DialogDescription>
      </DialogHeader>
      <Field data-invalid={shownError ? true : undefined}>
        <FieldLabel htmlFor={noteId}>版本备注</FieldLabel>
        <Textarea
          id={noteId}
          value={note}
          onChange={(event) => {
            setNote(event.target.value)
            mutation.reset()
          }}
          aria-invalid={shownError ? true : undefined}
          aria-required
          disabled={mutation.isPending}
          className="min-h-24"
          autoFocus
        />
        {shownError ? (
          <FieldError>{shownError}</FieldError>
        ) : (
          <DescriptionCounter value={note} />
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

function DeleteVersionDialog({
  datasetId,
  version,
  onOpenChange,
}: {
  datasetId: number
  version: DatasetVersion | null
  onOpenChange: (open: boolean) => void
}) {
  return (
    <AlertDialog open={version !== null} onOpenChange={onOpenChange}>
      <AlertDialogContent>
        {version && (
          <DeleteVersionForm
            datasetId={datasetId}
            version={version}
            onDone={() => onOpenChange(false)}
          />
        )}
      </AlertDialogContent>
    </AlertDialog>
  )
}

function DeleteVersionForm({
  datasetId,
  version,
  onDone,
}: {
  datasetId: number
  version: DatasetVersion
  onDone: () => void
}) {
  const name = versionName(version)
  const mutation = useMutation({
    mutationFn: () => deleteDatasetVersion(datasetId, version.id),
    onSuccess: () => {
      queryClient.setQueryData<ListResponse<DatasetVersion>>(
        datasetKeys.versions(datasetId),
        (list) =>
          list && {
            items: list.items.filter((item) => item.id !== version.id),
            total: Math.max(0, list.total - 1),
          }
      )
      refreshVersions(datasetId)
      refreshRecycleBin()
      toastMovedToBin({
        title: `已将${name}移入回收站`,
        undo: () =>
          restoreVersion(version.id).then(() =>
            refreshAfterVersionMove(datasetId)
          ),
        undoneTitle: `已恢复${name}`,
      })
      onDone()
    },
  })

  return (
    <>
      <AlertDialogHeader>
        <AlertDialogTitle>删除{name}？</AlertDialogTitle>
        <AlertDialogDescription>
          版本会移入回收站，保留 {RECYCLE_RETENTION_DAYS}{" "}
          天，期间可以恢复（编号不变）；到期后自动永久删除这个版本的审图标注（
          {formatNumber(version.image_count)} 张图片）。编号“{name}
          ”不会再分配给别的版本。
        </AlertDialogDescription>
      </AlertDialogHeader>
      <p className="text-sm text-pretty text-muted-foreground">
        数据集的标注文件（基础版本）与审图任务不受影响，需要时可以再次合并审图任务，生成新的版本。
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
