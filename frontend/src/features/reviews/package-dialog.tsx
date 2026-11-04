/**
 * 下载审图包：所选图片、它们的审图标注（VOC）与 review.json 的 zip，在本地审图
 * 程序中打开。可以只下载某些审核状态、某个数据集的图片。
 */
import { useId, useState, type FormEvent } from "react"
import { useMutation } from "@tanstack/react-query"
import { Download, Info } from "lucide-react"
import { toast } from "sonner"

import { Alert, AlertDescription } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { Field, FieldLabel } from "@/components/ui/field"
import { Spinner } from "@/components/ui/spinner"
import {
  createReviewPackage,
  reviewKeys,
  type Review,
  type ReviewImageStatus,
} from "@/features/reviews/api"
import {
  DatasetScopeSelect,
  StatusCheckboxes,
} from "@/features/reviews/review-fields"
import {
  inferredImageCount,
  packageInput,
} from "@/features/reviews/review-format"
import {
  REVIEW_IMAGE_STATUSES,
  isInferring,
  scopedCounts,
} from "@/features/reviews/review-status"
import { ApiError } from "@/lib/api"
import { startDownload } from "@/lib/download"
import { formatNumber } from "@/lib/format"
import { queryClient } from "@/lib/query-client"

const STATUS_HINTS: Partial<Record<ReviewImageStatus, string>> = {
  pending: "还没有审核的图片",
  approved: "可以在本地复查",
  needs_sample: "可以在本地复查",
}

export function DownloadPackageDialog({
  review,
  open,
  onOpenChange,
}: {
  review: Review
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[calc(100dvh-2rem)] overflow-y-auto sm:max-w-xl">
        {open && (
          <PackageForm review={review} onDone={() => onOpenChange(false)} />
        )}
      </DialogContent>
    </Dialog>
  )
}

function PackageForm({
  review,
  onDone,
}: {
  review: Review
  onDone: () => void
}) {
  const datasetFieldId = useId()
  const [statuses, setStatuses] = useState<Set<ReviewImageStatus>>(
    () => new Set(REVIEW_IMAGE_STATUSES)
  )
  const [datasetId, setDatasetId] = useState<number | null>(null)

  const counts = scopedCounts(review, datasetId)
  const imageCount = inferredImageCount(counts, statuses)
  const notInferred = counts.total - counts.inferred

  const mutation = useMutation({
    mutationFn: () =>
      createReviewPackage(review.id, packageInput(statuses, datasetId)),
    onSuccess: (pkg) => {
      startDownload(pkg.url, pkg.file_name)
      toast.success("已开始下载审图包", {
        description: `${formatNumber(pkg.image_count)} 张图片。压缩包在下载时实时生成，图片较多时需要一些时间，可在浏览器的下载列表中查看进度。`,
      })
      onDone()
    },
    onError: (error) => {
      if (error instanceof ApiError && error.status === 409) {
        // 图片数变了（如别人刚标记了图片）：显示最新的数量。
        void queryClient.invalidateQueries({
          queryKey: reviewKeys.detail(review.id),
          exact: true,
        })
      }
    },
  })

  const submit = (event: FormEvent) => {
    event.preventDefault()
    if (statuses.size > 0 && imageCount > 0) {
      mutation.mutate()
    }
  }

  const pending = mutation.isPending

  return (
    <form
      onSubmit={submit}
      className="grid min-w-0 grid-cols-[minmax(0,1fr)] gap-6"
      noValidate
    >
      <DialogHeader>
        <DialogTitle>下载审图包</DialogTitle>
        <DialogDescription>
          审图包中有所选的图片、它们的审图标注（VOC）与
          review.json，用本地审图程序打开后离线审图，完成后把导出的审图结果上传回这里。
        </DialogDescription>
      </DialogHeader>

      <Field>
        <FieldLabel asChild>
          <span>审核状态</span>
        </FieldLabel>
        <StatusCheckboxes
          statuses={REVIEW_IMAGE_STATUSES}
          value={statuses}
          onChange={(next) => {
            setStatuses(next)
            mutation.reset()
          }}
          counts={counts}
          hints={STATUS_HINTS}
          disabled={pending}
          label="下载哪些审核状态的图片"
        />
      </Field>

      {review.datasets.length > 1 && (
        <Field>
          <FieldLabel htmlFor={datasetFieldId}>数据集</FieldLabel>
          <DatasetScopeSelect
            id={datasetFieldId}
            review={review}
            value={datasetId}
            onChange={(next) => {
              setDatasetId(next)
              mutation.reset()
            }}
            disabled={pending}
          />
        </Field>
      )}

      {notInferred > 0 && (
        <Alert>
          <Info />
          <AlertDescription>
            还有 {formatNumber(notInferred)} 张图片
            {isInferring(review) ? "正在识别" : "没有识别"}
            ，不在审图包中；识别完成后可以再下载一个审图包审核它们。
          </AlertDescription>
        </Alert>
      )}

      <p className="text-sm text-muted-foreground tabular-nums">
        {statuses.size === 0
          ? "请至少选择一种审核状态。"
          : imageCount === 0
            ? "没有符合条件的图片。"
            : `共 ${formatNumber(imageCount)} 张图片。`}
      </p>

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
          disabled={pending}
        >
          取消
        </Button>
        <Button
          type="submit"
          disabled={pending || statuses.size === 0 || imageCount === 0}
        >
          {pending ? <Spinner aria-hidden /> : <Download />}
          下载审图包
        </Button>
      </DialogFooter>
    </form>
  )
}
