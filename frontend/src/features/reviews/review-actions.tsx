/**
 * 审图任务的操作：更多操作菜单、修改名称与说明、重新识别与删除确认。列表页与
 * 详情页共用（数据集页面的审核记录只有查看与审图）。
 */
import { useId, useState, type FormEvent } from "react"
import { useMutation } from "@tanstack/react-query"
import {
  Ellipsis,
  Info,
  Pencil,
  RotateCcw,
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
  FieldError,
  FieldGroup,
  FieldLabel,
} from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import { Spinner } from "@/components/ui/spinner"
import { Textarea } from "@/components/ui/textarea"
import { DescriptionCounter } from "@/features/datasets/dataset-actions"
import {
  deleteReview,
  retryReview,
  reviewKeys,
  reviewSource,
  reviewSourceKeys,
  updateReview,
  type Review,
} from "@/features/reviews/api"
import {
  reviewDetailPath,
  reviewWorkspacePath,
} from "@/features/reviews/params"
import {
  validateReviewDescription,
  validateReviewName,
} from "@/features/reviews/review-format"
import { ApiError } from "@/lib/api"
import { formatNumber } from "@/lib/format"
import { queryClient } from "@/lib/query-client"

/** 列表与详情页上的操作，打开对应的对话框（见 useReviewActions）。 */
export type ReviewActions = {
  edit: (review: Review) => void
  retry: (review: Review) => void
  remove: (review: Review) => void
  /** 正在重新识别的审图任务（按钮显示进度）。 */
  retrying: number | null
}

/** 修改、删除后刷新列表与数据集的审核记录（详情另外更新）。 */
function refreshReviewLists() {
  void queryClient.invalidateQueries({ queryKey: reviewKeys.lists() })
  void queryClient.invalidateQueries({ queryKey: reviewKeys.datasets() })
}

/** 详情接口返回的最新审图任务写入缓存（编辑器的任务信息也是这个查询）。 */
export function cacheReview(review: Review) {
  queryClient.setQueryData(reviewKeys.detail(review.id), review)
}

type ReviewActionsMenuProps = {
  review: Review
  actions: ReviewActions
  /** 列表中显示“网页审图”与“查看详情”；详情页的标题栏中已有，不显示。 */
  showLinks?: boolean
  className?: string
}

export function ReviewActionsMenu({
  review,
  actions,
  showLinks = true,
  className,
}: ReviewActionsMenuProps) {
  return (
    // 非模态：菜单项打开对话框时避免两层焦点陷阱互相干扰。
    <DropdownMenu modal={false}>
      <DropdownMenuTrigger asChild>
        <Button
          variant="ghost"
          size="icon-sm"
          className={className}
          aria-label={`更多操作：${review.name}`}
        >
          <Ellipsis />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="w-44">
        {showLinks && (
          <>
            <DropdownMenuItem asChild disabled={review.counts.inferred === 0}>
              <Link to={reviewWorkspacePath(review.id, { status: "pending" })}>
                <ScanEye />
                网页审图
              </Link>
            </DropdownMenuItem>
            <DropdownMenuItem asChild>
              <Link to={reviewDetailPath(review.id)}>
                <Info />
                查看详情
              </Link>
            </DropdownMenuItem>
          </>
        )}
        <DropdownMenuItem onSelect={() => actions.edit(review)}>
          <Pencil />
          修改名称和说明
        </DropdownMenuItem>
        {review.status === "failed" && (
          <DropdownMenuItem
            onSelect={() => actions.retry(review)}
            disabled={actions.retrying === review.id || !review.model_exists}
          >
            <RotateCcw />
            重新识别
          </DropdownMenuItem>
        )}
        <DropdownMenuSeparator />
        <DropdownMenuItem
          variant="destructive"
          onSelect={() => actions.remove(review)}
        >
          <Trash2 />
          删除
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  )
}

/**
 * 页面上审图任务操作的状态与对话框：`actions` 交给列表 / 菜单，`dialogs`
 * 渲染在页面中。
 */
export function useReviewActions({
  onDeleted,
}: { onDeleted?: (review: Review) => void } = {}) {
  const [editing, setEditing] = useState<Review | null>(null)
  const [deleting, setDeleting] = useState<Review | null>(null)

  const retry = useMutation({
    mutationFn: (review: Review) => retryReview(review.id),
    onSuccess: (saved) => {
      cacheReview(saved)
      refreshReviewLists()
      const left = saved.counts.total - saved.counts.inferred
      toast.success(`已重新排队“${saved.name}”`, {
        description:
          left > 0
            ? `训练节点会继续识别剩下的 ${formatNumber(left)} 张图片，已识别的结果保留。`
            : undefined,
      })
    },
    onError: (error, review) => {
      toast.error("无法重新识别", { description: error.message })
      if (error instanceof ApiError && error.status === 409) {
        // 状态已经变了（如别人已经重新识别），或模型已被删除：显示最新的状态。
        void queryClient.invalidateQueries({
          queryKey: reviewKeys.detail(review.id),
          exact: true,
        })
        refreshReviewLists()
      }
    },
  })

  const actions: ReviewActions = {
    edit: setEditing,
    retry: (review) => retry.mutate(review),
    remove: setDeleting,
    retrying: retry.isPending ? (retry.variables?.id ?? null) : null,
  }

  const dialogs = (
    <>
      <EditReviewDialog
        review={editing}
        onOpenChange={(open) => !open && setEditing(null)}
      />
      <DeleteReviewDialog
        review={deleting}
        onOpenChange={(open) => !open && setDeleting(null)}
        onDeleted={onDeleted}
      />
    </>
  )

  return { actions, dialogs }
}

/** 修改审图任务的名称与说明。 */
export function EditReviewDialog({
  review,
  onOpenChange,
}: {
  review: Review | null
  onOpenChange: (open: boolean) => void
}) {
  return (
    <Dialog open={review !== null} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-md">
        {review && (
          <EditReviewForm review={review} onDone={() => onOpenChange(false)} />
        )}
      </DialogContent>
    </Dialog>
  )
}

function EditReviewForm({
  review,
  onDone,
}: {
  review: Review
  onDone: () => void
}) {
  const nameId = useId()
  const descriptionId = useId()
  const [name, setName] = useState(review.name)
  const [description, setDescription] = useState(review.description)

  const changes: { name?: string; description?: string } = {}
  if (name.trim() !== review.name) {
    changes.name = name.trim()
  }
  if (description.trim() !== review.description) {
    changes.description = description.trim()
  }

  const mutation = useMutation({
    mutationFn: () => updateReview(review.id, changes),
    onSuccess: (saved) => {
      cacheReview(saved)
      refreshReviewLists()
      toast.success("已保存")
      onDone()
    },
  })

  const nameError = validateReviewName(name)
  const descriptionError = validateReviewDescription(description)

  const submit = (event: FormEvent) => {
    event.preventDefault()
    if (nameError || descriptionError) {
      return
    }
    if (Object.keys(changes).length === 0) {
      onDone()
      return
    }
    mutation.mutate()
  }

  return (
    <form onSubmit={submit} className="grid gap-6" noValidate>
      <DialogHeader>
        <DialogTitle>修改名称和说明</DialogTitle>
        <DialogDescription>
          审图标注、审核状态与识别结果不受影响。
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
            autoFocus
          />
          {nameError && <FieldError>{nameError}</FieldError>}
        </Field>
        <Field data-invalid={descriptionError ? true : undefined}>
          <FieldLabel htmlFor={descriptionId}>说明</FieldLabel>
          <Textarea
            id={descriptionId}
            value={description}
            onChange={(event) => setDescription(event.target.value)}
            placeholder="审图的目的、批次、注意事项等（可选）"
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
      </FieldGroup>
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

/** 删除审图任务：审核状态与审图标注一起删除，数据集不受影响。 */
export function DeleteReviewDialog({
  review,
  onOpenChange,
  onDeleted,
}: {
  review: Review | null
  onOpenChange: (open: boolean) => void
  onDeleted?: (review: Review) => void
}) {
  return (
    <AlertDialog open={review !== null} onOpenChange={onOpenChange}>
      <AlertDialogContent>
        {review && (
          <DeleteReviewForm
            review={review}
            onDeleted={() => {
              onOpenChange(false)
              onDeleted?.(review)
            }}
          />
        )}
      </AlertDialogContent>
    </AlertDialog>
  )
}

function DeleteReviewForm({
  review,
  onDeleted,
}: {
  review: Review
  onDeleted: () => void
}) {
  const merging = review.active_merge !== null
  const mutation = useMutation({
    mutationFn: () => deleteReview(review.id),
    onSuccess: (result) => {
      queryClient.removeQueries({ queryKey: reviewKeys.detail(review.id) })
      queryClient.removeQueries({
        queryKey: reviewSourceKeys.imageDetails(reviewSource(review.id)),
      })
      refreshReviewLists()
      if (result?.files_removed === false) {
        // 记录已删除，但任务文件夹没能完全删除（如权限不足）：说明留在了哪里。
        toast.warning(`已删除审图任务“${review.name}”`, {
          description: result.message,
          duration: 15_000,
        })
      } else {
        toast.success(`已删除审图任务“${review.name}”`)
      }
      onDeleted()
    },
    onError: (error) => {
      if (error instanceof ApiError && error.status === 409) {
        // 多半是刚开始了合并：显示最新的状态。
        void queryClient.invalidateQueries({
          queryKey: reviewKeys.detail(review.id),
          exact: true,
        })
      }
    },
  })

  const reviewed = review.counts.approved + review.counts.needs_sample

  return (
    <>
      <AlertDialogHeader>
        <AlertDialogTitle className="break-words">
          删除审图任务“{review.name}”？
        </AlertDialogTitle>
        <AlertDialogDescription>
          将删除 {formatNumber(review.counts.total)} 张图片的识别结果、审图标注
          {reviewed > 0 && `与 ${formatNumber(reviewed)} 张图片的审核状态`}
          ，无法恢复。
        </AlertDialogDescription>
      </AlertDialogHeader>
      <p className="text-sm text-muted-foreground">
        数据集中的图片与标注不受影响，已经合并到数据集的标注会保留。
        {(review.status === "queued" || review.status === "running") &&
          "正在进行的识别会停止。"}
      </p>
      {merging && (
        <p role="alert" className="text-sm text-destructive">
          这个审图任务正在合并到数据集，请等合并完成后再删除。
        </p>
      )}
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
          disabled={mutation.isPending || merging}
        >
          {mutation.isPending && <Spinner aria-hidden />}
          删除审图任务
        </Button>
      </AlertDialogFooter>
    </>
  )
}
