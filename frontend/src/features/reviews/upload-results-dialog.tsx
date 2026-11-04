/**
 * 上传审图结果：本地审图程序导出的 zip（也接受重新压缩的审图包，如用 LabelImg
 * 修改过 VOC 之后）。平台逐张应用审核状态与修改过的标注；下载审图包之后在平台上
 * 又被修改过的图片记为冲突，勾选覆盖时用本地的结果。
 */
import { useEffect, useId, useRef, useState, type DragEvent } from "react"
import { useMutation } from "@tanstack/react-query"
import {
  CircleCheck,
  FileArchive,
  RotateCcw,
  TriangleAlert,
  Upload,
} from "lucide-react"
import { toast } from "sonner"

import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
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
import { Spinner } from "@/components/ui/spinner"
import {
  TaskProgress,
  TransferProgress,
} from "@/features/datasets/task-progress"
import { smoothSpeed } from "@/features/datasets/upload-plan"
import {
  refreshReviewContent,
  uploadReviewResults,
  type Review,
  type ReviewUpload,
} from "@/features/reviews/api"
import {
  uploadSummaryText,
  validateResultsFile,
} from "@/features/reviews/review-format"
import { ProblemList } from "@/features/reviews/review-parts"
import { isAbortError } from "@/lib/api"
import { formatBytes, formatNumber } from "@/lib/format"
import { cn } from "@/lib/utils"

type Transfer = { loaded: number; total: number; speed: number }

export function UploadResultsDialog({
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
          <UploadForm review={review} onDone={() => onOpenChange(false)} />
        )}
      </DialogContent>
    </Dialog>
  )
}

function UploadForm({
  review,
  onDone,
}: {
  review: Review
  onDone: () => void
}) {
  const inputId = useId()
  const overwriteId = useId()
  const inputRef = useRef<HTMLInputElement>(null)
  const abortRef = useRef<AbortController | null>(null)
  const sampleRef = useRef({ time: 0, bytes: 0 })
  const [file, setFile] = useState<File | null>(null)
  const [overwrite, setOverwrite] = useState(false)
  const [dragging, setDragging] = useState(false)
  const [transfer, setTransfer] = useState<Transfer | null>(null)

  const mutation = useMutation({
    mutationFn: ({
      file: zip,
      overwrite: force,
    }: {
      file: File
      overwrite: boolean
    }) => {
      const controller = new AbortController()
      abortRef.current = controller
      sampleRef.current = { time: performance.now(), bytes: 0 }
      setTransfer({ loaded: 0, total: zip.size, speed: 0 })
      return uploadReviewResults(review.id, zip, {
        fileName: zip.name,
        overwrite: force,
        signal: controller.signal,
        onProgress: (loaded, total) => {
          const now = performance.now()
          const sample = sampleRef.current
          setTransfer((previous) => {
            const elapsed = now - sample.time
            if (elapsed < 500 && previous) {
              return { ...previous, loaded, total: total || zip.size }
            }
            sampleRef.current = { time: now, bytes: loaded }
            return {
              loaded,
              total: total || zip.size,
              speed: smoothSpeed(
                previous?.speed ?? 0,
                loaded - sample.bytes,
                elapsed
              ),
            }
          })
        },
      })
    },
    onSuccess: (upload) => {
      refreshReviewContent(review.id)
      const { summary } = upload
      if (summary.conflicts > 0 || summary.invalid > 0 || summary.unknown > 0) {
        toast.warning("已上传审图结果，部分图片没有应用", {
          description: uploadSummaryText(summary),
        })
      } else {
        toast.success("已上传审图结果", {
          description: uploadSummaryText(summary),
        })
      }
    },
    onSettled: () => {
      abortRef.current = null
    },
  })

  const fileError = file ? validateResultsFile(file) : null
  const uploading = mutation.isPending
  const sent = transfer !== null && transfer.loaded >= transfer.total
  const result = mutation.data

  // 关闭对话框时取消还没有传完的上传；已经传完的由平台继续处理（结果见上传记录）。
  const sentRef = useRef(sent)
  useEffect(() => {
    sentRef.current = sent
  }, [sent])
  useEffect(
    () => () => {
      if (!sentRef.current) {
        abortRef.current?.abort()
      }
    },
    []
  )

  const choose = (next: File | null) => {
    setFile(next)
    mutation.reset()
    setTransfer(null)
  }
  const start = (force: boolean) => {
    if (!file || validateResultsFile(file)) {
      return
    }
    setOverwrite(force)
    mutation.mutate({ file, overwrite: force })
  }
  const cancel = () => {
    abortRef.current?.abort()
  }
  const onDrop = (event: DragEvent) => {
    event.preventDefault()
    setDragging(false)
    if (uploading) {
      return
    }
    const dropped = event.dataTransfer.files[0]
    if (dropped) {
      choose(dropped)
    }
  }

  const aborted = mutation.error !== null && isAbortError(mutation.error)

  return (
    <div className="grid min-w-0 grid-cols-[minmax(0,1fr)] gap-6">
      <DialogHeader>
        <DialogTitle>上传审图结果</DialogTitle>
        <DialogDescription>
          选择本地审图程序“导出审图结果”生成的
          zip。平台读取其中的审核状态与修改过的标注，逐张应用到这个审图任务。
        </DialogDescription>
      </DialogHeader>

      {result ? (
        <UploadResult upload={result} />
      ) : (
        <>
          <label
            htmlFor={inputId}
            onDragOver={(event) => {
              event.preventDefault()
              if (!uploading) {
                setDragging(true)
              }
            }}
            onDragLeave={() => setDragging(false)}
            onDrop={onDrop}
            className={cn(
              "flex min-w-0 cursor-pointer flex-col items-center gap-2 rounded-lg border border-dashed px-4 py-6 text-center transition-colors hover:bg-muted/40 has-[:focus-visible]:ring-[3px] has-[:focus-visible]:ring-ring/50",
              dragging && "border-primary bg-muted/40",
              uploading && "pointer-events-none opacity-60"
            )}
          >
            <input
              ref={inputRef}
              id={inputId}
              type="file"
              accept=".zip,application/zip"
              className="sr-only"
              disabled={uploading}
              onChange={(event) => {
                choose(event.target.files?.[0] ?? null)
                // 允许再次选择同一个文件。
                event.target.value = ""
              }}
            />
            <FileArchive className="size-6 text-muted-foreground" />
            {file ? (
              <span className="grid min-w-0 gap-0.5">
                <span
                  className="truncate text-sm font-medium"
                  title={file.name}
                >
                  {file.name}
                </span>
                <span className="text-xs text-muted-foreground tabular-nums">
                  {formatBytes(file.size)} · 点击更换文件
                </span>
              </span>
            ) : (
              <span className="grid gap-0.5">
                <span className="text-sm font-medium">
                  选择或拖入审图结果（.zip）
                </span>
                <span className="text-xs text-muted-foreground">
                  通常名为“…-审图结果.zip”，在审图包旁边
                </span>
              </span>
            )}
          </label>
          {fileError && (
            <p role="alert" className="-mt-3 text-sm text-destructive">
              {fileError}
            </p>
          )}

          <label
            htmlFor={overwriteId}
            className={cn(
              "flex cursor-pointer items-start gap-3 rounded-md border p-3 text-sm has-[:focus-visible]:ring-[3px] has-[:focus-visible]:ring-ring/50",
              uploading && "pointer-events-none opacity-60"
            )}
          >
            <Checkbox
              id={overwriteId}
              checked={overwrite}
              onCheckedChange={(value) => setOverwrite(value === true)}
              disabled={uploading}
              className="mt-0.5"
            />
            <span className="grid gap-1">
              <span className="font-medium">
                平台上被修改过的图片也用本地结果覆盖
              </span>
              <span className="text-xs text-muted-foreground">
                不勾选时，下载审图包之后又在网页上审核或修改过的图片记为冲突，保留平台上的结果。
              </span>
            </span>
          </label>

          {transfer && uploading && (
            <div className="grid gap-1.5">
              {sent ? (
                <>
                  <TaskProgress value={null} label="正在读取审图结果" />
                  <p className="flex items-center gap-1.5 text-xs text-muted-foreground">
                    <Spinner className="size-3" aria-hidden />
                    已上传，正在读取并应用审图结果…
                  </p>
                </>
              ) : (
                <TransferProgress
                  uploadedBytes={transfer.loaded}
                  totalBytes={transfer.total}
                  speed={transfer.speed}
                />
              )}
            </div>
          )}

          {mutation.error && !aborted && (
            <Alert variant="destructive">
              <TriangleAlert />
              <AlertTitle>没有上传</AlertTitle>
              <AlertDescription>
                <p className="break-words">{mutation.error.message}</p>
              </AlertDescription>
            </Alert>
          )}
          {aborted && (
            <p className="text-sm text-muted-foreground">已取消上传。</p>
          )}
        </>
      )}

      <DialogFooter>
        {result ? (
          <>
            {result.summary.conflicts > 0 && !result.overwrite && file && (
              <Button
                variant="outline"
                onClick={() => start(true)}
                disabled={uploading}
              >
                {uploading ? <Spinner aria-hidden /> : <RotateCcw />}
                用本地结果覆盖冲突的图片
              </Button>
            )}
            <Button onClick={onDone}>完成</Button>
          </>
        ) : uploading ? (
          sent ? (
            <Button variant="outline" onClick={onDone}>
              在后台继续
            </Button>
          ) : (
            <Button variant="outline" onClick={cancel}>
              取消上传
            </Button>
          )
        ) : (
          <>
            <Button variant="outline" onClick={onDone}>
              取消
            </Button>
            <Button
              onClick={() => start(overwrite)}
              disabled={!file || fileError !== null}
            >
              <Upload />
              上传
            </Button>
          </>
        )}
      </DialogFooter>
    </div>
  )
}

/** 上传的结果：各项计数与没有应用的图片。 */
function UploadResult({ upload }: { upload: ReviewUpload }) {
  const { summary } = upload
  const problemTotal = summary.conflicts + summary.invalid + summary.unknown
  return (
    <div className="grid min-w-0 gap-4">
      <div className="flex items-start gap-3 rounded-lg border bg-muted/30 p-3">
        {problemTotal > 0 ? (
          <TriangleAlert className="mt-0.5 size-5 shrink-0 text-amber-600 dark:text-amber-500" />
        ) : (
          <CircleCheck className="mt-0.5 size-5 shrink-0 text-emerald-600 dark:text-emerald-500" />
        )}
        <div className="grid min-w-0 flex-1 gap-1">
          <p className="text-sm font-medium">
            {summary.applied > 0
              ? `已应用 ${formatNumber(summary.applied)} 张图片的审图结果`
              : "没有需要应用的修改"}
          </p>
          <p className="text-xs break-all text-muted-foreground">
            {upload.file_name} · 共 {formatNumber(summary.images)} 张
            {upload.overwrite && " · 覆盖平台上的修改"}
          </p>
        </div>
      </div>
      <UploadSummaryGrid upload={upload} />
      {summary.conflicts > 0 && !upload.overwrite && (
        <p className="text-sm text-muted-foreground">
          冲突的图片在下载审图包之后又在网页上被修改过，保留了平台上的结果。确认本地的结果更准确时，可以用本地结果覆盖它们。
        </p>
      )}
      <ProblemList problems={upload.problems} total={problemTotal} />
    </div>
  )
}

/** 上传结果的各项计数。 */
export function UploadSummaryGrid({ upload }: { upload: ReviewUpload }) {
  const { summary } = upload
  const items = [
    { label: "审核状态", value: summary.status_changes },
    { label: "标注", value: summary.annotation_changes },
    { label: "没有变化", value: summary.unchanged },
    { label: "冲突", value: summary.conflicts, warn: true },
    { label: "无效", value: summary.invalid, warn: true },
    { label: "不在任务中", value: summary.unknown, warn: true },
  ]
  return (
    <dl className="flex flex-wrap gap-2">
      {items.map((item) => (
        <div
          key={item.label}
          className="grid min-w-20 flex-auto gap-0.5 rounded-md border px-3 py-2"
        >
          <dt className="text-xs whitespace-nowrap text-muted-foreground">
            {item.label}
          </dt>
          <dd
            className={cn(
              "text-base font-semibold tabular-nums",
              item.warn &&
                item.value > 0 &&
                "text-amber-700 dark:text-amber-500"
            )}
          >
            {formatNumber(item.value)}
          </dd>
        </div>
      ))}
    </dl>
  )
}
