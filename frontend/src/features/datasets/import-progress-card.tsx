import { useEffect, type ReactNode } from "react"
import { useMutation, useQuery } from "@tanstack/react-query"
import { FileArchive, Folder, ScanSearch, type LucideIcon } from "lucide-react"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import { Card } from "@/components/ui/card"
import { Spinner } from "@/components/ui/spinner"
import {
  cancelImport,
  datasetKeys,
  importQuery,
  type Dataset,
  type DatasetImport,
  type ImportSource,
} from "@/features/datasets/api"
import {
  jobPercent,
  progressDetail,
  stageLabel,
} from "@/features/datasets/import-status"
import { watchImport } from "@/features/datasets/import-watch"
import { TaskProgress } from "@/features/datasets/task-progress"
import {
  cancelUpload,
  useActiveUploadTask,
  type UploadTask,
} from "@/features/datasets/upload-manager"
import { etaSeconds } from "@/features/datasets/upload-plan"
import {
  formatBytes,
  formatDuration,
  formatNumber,
  formatPercent,
  percentOf,
} from "@/lib/format"
import { queryClient } from "@/lib/query-client"

const sourceIcons: Record<ImportSource, LucideIcon> = {
  archive: FileArchive,
  folder: Folder,
  scan: ScanSearch,
}

function jobTitle(source: ImportSource, fileName: string) {
  switch (source) {
    case "archive":
      return `导入压缩包 ${fileName}`
    case "folder":
      return `导入文件夹 ${fileName}`
    case "scan":
      return "扫描数据集文件夹"
  }
}

/**
 * 详情页顶部的任务进度：本页面发起的上传显示上传进度（来自上传管理器），
 * 否则显示数据集当前的导入 / 扫描任务（可能由其他页面或其他人发起）。
 */
export function ImportProgressCard({ dataset }: { dataset: Dataset }) {
  const task = useActiveUploadTask(dataset.id)

  if (task) {
    return <UploadTaskCard task={task} />
  }
  if (dataset.active_import) {
    return (
      <ServerJobCard
        key={dataset.active_import.id}
        dataset={dataset}
        initial={dataset.active_import}
      />
    )
  }
  return null
}

function ProgressCardLayout({
  icon: Icon,
  title,
  percent,
  status,
  announcement,
  action,
}: {
  icon: LucideIcon
  title: string
  percent: number | null
  status: ReactNode
  /**
   * 读屏播报的简短状态，只在阶段变化时改变。字节数、速度每秒变化多次，
   * 不放进 live region，否则读屏会一直播报。
   */
  announcement: string
  action: ReactNode
}) {
  return (
    <Card className="gap-0 py-0">
      <p className="sr-only" role="status">
        {`${title}：${announcement}`}
      </p>
      <div className="flex items-center gap-4 p-4">
        <div className="relative hidden size-10 shrink-0 items-center justify-center rounded-full bg-muted text-muted-foreground sm:flex">
          <Icon className="size-4" />
          <Spinner
            className="absolute inset-0 size-10 text-primary/30"
            aria-hidden
          />
        </div>
        <div className="grid min-w-0 flex-1 gap-2">
          <div className="flex items-baseline justify-between gap-3">
            <p className="truncate text-sm font-medium" title={title}>
              {title}
            </p>
            {percent !== null && (
              <span className="shrink-0 text-sm font-medium tabular-nums">
                {formatPercent(percent)}
              </span>
            )}
          </div>
          <TaskProgress value={percent} label="任务进度" />
          <div className="flex flex-wrap gap-x-1 text-xs text-muted-foreground tabular-nums">
            {status}
          </div>
        </div>
        {action}
      </div>
    </Card>
  )
}

function UploadTaskCard({ task }: { task: UploadTask }) {
  const cancel = (
    <Button
      variant="outline"
      size="sm"
      onClick={() => void cancelUpload(task.id)}
    >
      取消
    </Button>
  )

  if (task.status === "processing") {
    const detail = progressDetail(task.job)
    return (
      <ProgressCardLayout
        icon={sourceIcons[task.kind]}
        title={jobTitle(task.kind, task.fileName)}
        percent={jobPercent(task.job)}
        status={
          <>
            <span>上传完成，{stageLabel(task.job)}</span>
            {detail && <span>· {detail}</span>}
          </>
        }
        announcement={`上传完成，${stageLabel(task.job)}`}
        action={cancel}
      />
    )
  }

  const percent = percentOf(task.uploadedBytes, task.totalBytes)
  const remaining = etaSeconds(task.totalBytes - task.uploadedBytes, task.speed)
  return (
    <ProgressCardLayout
      icon={sourceIcons[task.kind]}
      title={jobTitle(task.kind, task.fileName)}
      percent={task.status === "committing" ? null : percent}
      status={
        task.status === "committing" ? (
          <span>上传完成，正在提交…</span>
        ) : (
          <>
            <span>
              正在上传 {formatBytes(task.uploadedBytes)} /{" "}
              {formatBytes(task.totalBytes)}
            </span>
            {task.kind === "folder" && (
              <span>
                · {formatNumber(task.uploadedFiles)} /{" "}
                {formatNumber(task.totalFiles)} 个文件
              </span>
            )}
            {task.speed > 0 && <span>· {formatBytes(task.speed)}/s</span>}
            {remaining !== null && (
              <span>· 剩余 {formatDuration(Math.ceil(remaining))}</span>
            )}
          </>
        )
      }
      announcement={
        task.status === "committing" ? "上传完成，正在提交" : "正在上传"
      }
      action={cancel}
    />
  )
}

function ServerJobCard({
  dataset,
  initial,
}: {
  dataset: Dataset
  initial: DatasetImport
}) {
  // 轮询由 watchImport 统一进行；这里只读取同一个查询缓存。
  useEffect(() => {
    watchImport(initial, dataset.name)
  }, [initial, dataset.name])

  const { data: job = initial } = useQuery({
    ...importQuery(dataset.id, initial.id),
    initialData: initial,
    enabled: false,
  })

  const cancel = useMutation({
    mutationFn: () => cancelImport(dataset.id, job.id),
    onSuccess: (updated) => {
      queryClient.setQueryData(datasetKeys.import(dataset.id, job.id), updated)
      void queryClient.invalidateQueries({ queryKey: datasetKeys.all })
      toast("已取消任务")
    },
    onError: (error) => {
      toast.error("无法取消任务", { description: error.message })
    },
  })

  const detail = progressDetail(job)
  const orphanedUpload = job.status === "uploading"

  return (
    <ProgressCardLayout
      icon={sourceIcons[job.source]}
      title={jobTitle(job.source, job.file_name)}
      percent={jobPercent(job)}
      announcement={stageLabel(job)}
      status={
        <>
          <span>{stageLabel(job)}</span>
          {detail && <span>· {detail}</span>}
          {orphanedUpload && (
            <span className="basis-full">
              上传页面可能已关闭或网络已中断。如果没有人在继续上传，可以取消该任务后重新导入（超过
              24 小时未完成的上传会被自动取消）。
            </span>
          )}
        </>
      }
      action={
        <Button
          variant="outline"
          size="sm"
          onClick={() => cancel.mutate()}
          disabled={cancel.isPending}
        >
          {cancel.isPending && <Spinner aria-hidden />}
          取消
        </Button>
      }
    />
  )
}
