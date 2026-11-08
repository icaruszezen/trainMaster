/**
 * 数据版本的操作（列表、项目页与详情页共用）：更多操作菜单，以及下载、修改
 * 备注、重新生成与删除的对话框。可用的操作取决于状态：
 *
 * - 已生成：下载、修改备注、以此为模板新建、移入回收站（提示中可以撤销）；
 * - 生成失败：重新生成、修改备注、删除（永久，需要确认）；
 * - 排队中 / 生成中：修改备注、停止并删除（需要确认）。
 */
import { useId, useState, type FormEvent } from "react"
import { useMutation, useQuery } from "@tanstack/react-query"
import {
  CopyPlus,
  Download,
  Ellipsis,
  Info,
  OctagonX,
  Pencil,
  RotateCcw,
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
  dataVersionKeys,
  dataVersionQuery,
  deleteDataVersion,
  isActiveDataVersion,
  refreshAfterDataVersionMove,
  refreshDataVersions,
  restoreDataVersion,
  retryDataVersion,
  updateDataVersion,
  type DataVersion,
  type DataVersionStatus,
} from "@/features/data-versions/api"
import { watchDataVersionBuild } from "@/features/data-versions/build-watch"
import { DownloadDataVersionDialog } from "@/features/data-versions/download-data-version-dialog"
import {
  dataVersionFullTitle,
  dataVersionTitle,
} from "@/features/data-versions/format"
import {
  dataVersionDetailPath,
  newDataVersionPath,
} from "@/features/data-versions/paths"
import { RECYCLE_RETENTION_DAYS } from "@/features/recycle-bin/recycle-format"
import { toastMovedToBin } from "@/features/recycle-bin/recycle-toast"
import { validateVersionNote } from "@/features/reviews/review-format"
import type { ListResponse } from "@/lib/api"
import { queryClient } from "@/lib/query-client"

export type DataVersionMenuItem =
  "details" | "download" | "retry" | "edit-note" | "template" | "delete"

/** 菜单中的操作，按显示顺序（delete 前有分隔线）。 */
export function dataVersionMenuItems(
  status: DataVersionStatus,
  { showDetails = true }: { showDetails?: boolean } = {}
): DataVersionMenuItem[] {
  const items: DataVersionMenuItem[] = []
  if (status === "ready") {
    items.push("download")
  }
  if (status === "failed") {
    items.push("retry")
  }
  if (showDetails) {
    items.push("details")
  }
  items.push("edit-note")
  if (status === "ready") {
    items.push("template")
  }
  items.push("delete")
  return items
}

/** 删除操作的名称：已生成的进回收站，失败的永久删除，进行中的先停止。 */
export function deleteLabel(status: DataVersionStatus) {
  switch (status) {
    case "ready":
      return "移入回收站"
    case "failed":
      return "删除"
    default:
      return "停止并删除"
  }
}

/** 列表与详情页上的操作，打开对应的对话框（见 useDataVersionActions）。 */
export type DataVersionActions = {
  download: (version: DataVersion) => void
  editNote: (version: DataVersion) => void
  retry: (version: DataVersion) => void
  remove: (version: DataVersion) => void
}

type MenuProps = {
  version: DataVersion
  actions: DataVersionActions
  /** 列表中显示“查看详情”；详情页不显示。 */
  showDetails?: boolean
  className?: string
}

export function DataVersionActionsMenu({
  version,
  actions,
  showDetails = true,
  className,
}: MenuProps) {
  return (
    // 非模态：菜单项打开对话框时避免两层焦点陷阱互相干扰。
    <DropdownMenu modal={false}>
      <DropdownMenuTrigger asChild>
        <Button
          variant="ghost"
          size="icon-sm"
          className={className}
          aria-label={`更多操作：${dataVersionFullTitle(version)}`}
        >
          <Ellipsis />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="w-48">
        <DataVersionMenuItems
          version={version}
          actions={actions}
          showDetails={showDetails}
        />
      </DropdownMenuContent>
    </DropdownMenu>
  )
}

/** 菜单项（单独导出，便于测试）。 */
export function DataVersionMenuItems({
  version,
  actions,
  showDetails = true,
}: Omit<MenuProps, "className">) {
  return dataVersionMenuItems(version.status, { showDetails }).map((item) => {
    switch (item) {
      case "download":
        return (
          <DropdownMenuItem
            key={item}
            onSelect={() => actions.download(version)}
          >
            <Download />
            下载…
          </DropdownMenuItem>
        )
      case "retry":
        return (
          <DropdownMenuItem key={item} onSelect={() => actions.retry(version)}>
            <RotateCcw />
            重新生成
          </DropdownMenuItem>
        )
      case "details":
        return (
          <DropdownMenuItem key={item} asChild>
            <Link to={dataVersionDetailPath(version.id)}>
              <Info />
              查看详情
            </Link>
          </DropdownMenuItem>
        )
      case "edit-note":
        return (
          <DropdownMenuItem
            key={item}
            onSelect={() => actions.editNote(version)}
          >
            <Pencil />
            修改备注
          </DropdownMenuItem>
        )
      case "template":
        return (
          <DropdownMenuItem key={item} asChild>
            <Link
              to={newDataVersionPath({
                projectId: version.project_id,
                from: version.id,
              })}
            >
              <CopyPlus />
              以此为模板新建
            </Link>
          </DropdownMenuItem>
        )
      case "delete":
        return [
          <DropdownMenuSeparator key="separator" />,
          <DropdownMenuItem
            key={item}
            variant="destructive"
            onSelect={() => actions.remove(version)}
          >
            {version.status === "ready" || version.status === "failed" ? (
              <Trash2 />
            ) : (
              <OctagonX />
            )}
            {deleteLabel(version.status)}
          </DropdownMenuItem>,
        ]
    }
  })
}

/** 版本变化后写入缓存（详情与各列表中的这一项），再在后台刷新。 */
export function cacheDataVersion(version: DataVersion) {
  queryClient.setQueryData(dataVersionKeys.detail(version.id), version)
  queryClient.setQueriesData<ListResponse<DataVersion>>(
    { queryKey: dataVersionKeys.lists() },
    (list) =>
      list && {
        ...list,
        items: list.items.map((item) =>
          item.id === version.id ? version : item
        ),
      }
  )
  void refreshDataVersions()
}

/**
 * 页面上数据版本操作的状态与对话框：`actions` 交给列表 / 菜单，`dialogs`
 * 渲染在页面中。`onDeleted` 在删除成功后调用（详情页返回列表）。
 */
export function useDataVersionActions({
  onDeleted,
}: { onDeleted?: (version: DataVersion) => void } = {}) {
  const [downloading, setDownloading] = useState<DataVersion | null>(null)
  const [editing, setEditing] = useState<DataVersion | null>(null)
  const [deleting, setDeleting] = useState<DataVersion | null>(null)
  // 每次打开下载对话框换一个 key，选项从默认值开始。
  const [downloadKey, setDownloadKey] = useState(0)

  const retry = useMutation({
    mutationFn: (version: DataVersion) => retryDataVersion(version.id),
    onSuccess: (queued) => {
      cacheDataVersion(queued)
      watchDataVersionBuild(queued)
      toast.success(`已开始重新生成${dataVersionTitle(queued)}`, {
        description:
          "使用同样的类别、设置与数据集，按数据集现在的内容生成；完成后会提示。",
      })
    },
    onError: (error, version) => {
      void refreshDataVersions()
      toast.error(`无法重新生成${dataVersionTitle(version)}`, {
        description: error.message,
      })
    },
  })

  const actions: DataVersionActions = {
    download: (version) => {
      setDownloadKey((key) => key + 1)
      setDownloading(version)
    },
    editNote: setEditing,
    retry: (version) => {
      if (!retry.isPending) {
        retry.mutate(version)
      }
    },
    remove: setDeleting,
  }

  const dialogs = (
    <>
      <DownloadDataVersionDialog
        key={downloadKey}
        version={downloading}
        onOpenChange={(open) => !open && setDownloading(null)}
      />
      <EditNoteDialog
        version={editing}
        onOpenChange={(open) => !open && setEditing(null)}
      />
      <DeleteDataVersionDialog
        version={deleting}
        onOpenChange={(open) => !open && setDeleting(null)}
        onDeleted={onDeleted}
      />
    </>
  )

  return { actions, dialogs, retrying: retry.isPending }
}

/** 修改版本备注（任何状态都可以）。 */
export function EditNoteDialog({
  version,
  onOpenChange,
}: {
  version: DataVersion | null
  onOpenChange: (open: boolean) => void
}) {
  return (
    <Dialog open={version !== null} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-md">
        {version && (
          <EditNoteForm version={version} onDone={() => onOpenChange(false)} />
        )}
      </DialogContent>
    </Dialog>
  )
}

function EditNoteForm({
  version,
  onDone,
}: {
  version: DataVersion
  onDone: () => void
}) {
  const noteId = useId()
  const [note, setNote] = useState(version.note)
  // 备注为空的提示在第一次提交后才显示。
  const [submitted, setSubmitted] = useState(false)
  const error = validateVersionNote(note)
  const shownError = error && (submitted || note.trim() !== "") ? error : null

  const mutation = useMutation({
    mutationFn: () => updateDataVersion(version.id, { note: note.trim() }),
    onSuccess: (updated) => {
      cacheDataVersion(updated)
      toast.success(`已修改${dataVersionTitle(updated)}的备注`)
      onDone()
    },
  })

  const submit = (event: FormEvent) => {
    event.preventDefault()
    setSubmitted(true)
    if (error !== null || mutation.isPending) {
      return
    }
    if (note.trim() === version.note) {
      onDone()
      return
    }
    mutation.mutate()
  }

  return (
    <form onSubmit={submit} className="grid min-w-0 gap-6" noValidate>
      <DialogHeader>
        <DialogTitle>修改{dataVersionTitle(version)}的备注</DialogTitle>
        <DialogDescription>
          {version.project_name}
          ：备注说明这个版本的数据与用途（如补充了哪些数据），列表与训练时选择版本可以看到。
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
        <p role="alert" className="text-sm break-words text-destructive">
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

/** 删除确认：已生成的移入回收站，失败的永久删除，进行中的停止并删除。 */
export function DeleteDataVersionDialog({
  version,
  onOpenChange,
  onDeleted,
}: {
  version: DataVersion | null
  onOpenChange: (open: boolean) => void
  onDeleted?: (version: DataVersion) => void
}) {
  return (
    <AlertDialog open={version !== null} onOpenChange={onOpenChange}>
      <AlertDialogContent>
        {version && (
          <DeleteForm
            version={version}
            onDeleted={() => {
              onOpenChange(false)
              onDeleted?.(version)
            }}
          />
        )}
      </AlertDialogContent>
    </AlertDialog>
  )
}

function DeleteForm({
  version: opened,
  onDeleted,
}: {
  version: DataVersion
  onDeleted: () => void
}) {
  const mutation = useMutation({
    // 带着确认时的状态：服务器按删除时的实际状态处理，进行中的版本可能刚刚
    // 生成完成。
    mutationFn: (status: DataVersionStatus) =>
      deleteDataVersion(opened.id).then(() => status),
    onSuccess: (status) => {
      queryClient.removeQueries({
        queryKey: dataVersionKeys.detail(opened.id),
      })
      if (status === "ready") {
        void refreshAfterDataVersionMove()
        toastMovedToBin({
          title: `已将“${title}”移入回收站`,
          undo: () =>
            restoreDataVersion(opened.id).then(
              () => void refreshAfterDataVersionMove()
            ),
          undoneTitle: `已恢复“${title}”`,
        })
      } else {
        // 进行中的版本在最后一次刷新之后生成完成时，服务器把它移入了回收站。
        void (status === "failed"
          ? refreshDataVersions()
          : refreshAfterDataVersionMove())
        toast.success(
          status === "failed" ? `已删除“${title}”` : `已停止并删除“${title}”`
        )
      }
      onDeleted()
    },
    onError: () => {
      // 版本可能已被别人删除或状态已变化：刷新后列表是最新的。
      void refreshDataVersions()
    },
  })

  // 进行中的版本在对话框打开期间继续刷新：按最新的状态说明与删除。
  const { data: version = opened } = useQuery({
    ...dataVersionQuery(opened.id),
    enabled: isActiveDataVersion(opened) && mutation.isIdle,
  })
  const title = dataVersionFullTitle(version)
  const status = version.status
  const number = `版本号 v${version.version} 不会再分配给之后的数据版本。`
  return (
    <>
      <AlertDialogHeader>
        <AlertDialogTitle className="break-words">
          {status === "ready"
            ? `将“${title}”移入回收站？`
            : status === "failed"
              ? `删除“${title}”？`
              : `停止并删除“${title}”？`}
        </AlertDialogTitle>
        <AlertDialogDescription>
          {status === "ready"
            ? `数据版本会移入回收站，保留 ${RECYCLE_RETENTION_DAYS} 天，期间可以恢复；到期后自动永久删除它的文件夹（图片、标签与文件列表）与记录。数据集不受影响。`
            : status === "failed"
              ? "生成失败的数据版本会被永久删除，不能恢复。数据集不受影响。"
              : "生成会立即停止，已经生成的文件与这条记录一起永久删除，不能恢复。数据集不受影响。"}
        </AlertDialogDescription>
      </AlertDialogHeader>
      <p className="text-sm text-muted-foreground">{number}</p>
      {mutation.error && (
        <p role="alert" className="text-sm break-words text-destructive">
          {mutation.error.message}
        </p>
      )}
      <AlertDialogFooter>
        <AlertDialogCancel disabled={mutation.isPending}>
          取消
        </AlertDialogCancel>
        <Button
          variant="destructive"
          onClick={() => mutation.mutate(status)}
          disabled={mutation.isPending}
        >
          {mutation.isPending && <Spinner aria-hidden />}
          {deleteLabel(status)}
        </Button>
      </AlertDialogFooter>
    </>
  )
}
