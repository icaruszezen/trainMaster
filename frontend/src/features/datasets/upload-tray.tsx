import { useState } from "react"
import {
  ChevronDown,
  CircleAlert,
  CircleCheck,
  FileArchive,
  Folder,
  RotateCw,
  Upload,
  X,
} from "lucide-react"
import { Link } from "react-router"

import { Button } from "@/components/ui/button"
import { Spinner } from "@/components/ui/spinner"
import {
  jobPercent,
  progressDetail,
  stageLabel,
  summaryText,
} from "@/features/datasets/import-status"
import { TaskProgress } from "@/features/datasets/task-progress"
import {
  cancelUpload,
  dismissFinishedUploads,
  dismissUpload,
  isAbandonable,
  isUploadRunning,
  retryUpload,
  useUploadTasks,
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
import { cn } from "@/lib/utils"

/** 右下角的上传面板：列出进行中与最近完成的上传，没有任务时不显示。 */
export function UploadTray() {
  const tasks = useUploadTasks()
  const [collapsed, setCollapsed] = useState(false)

  if (tasks.length === 0) {
    return null
  }

  const running = tasks.filter(isUploadRunning)
  const uploading = running.filter((task) => task.status === "uploading")
  const totalBytes = uploading.reduce((sum, task) => sum + task.totalBytes, 0)
  const uploadedBytes = uploading.reduce(
    (sum, task) => sum + task.uploadedBytes,
    0
  )
  const failed = tasks.filter((task) => task.status === "error").length

  const title =
    uploading.length > 0
      ? `正在上传 ${uploading.length} 项`
      : running.length > 0
        ? `正在处理 ${running.length} 项`
        : failed > 0
          ? `${failed} 项导入失败`
          : "导入已完成"

  return (
    <section
      aria-label="上传任务"
      className="fixed inset-x-0 bottom-0 z-40 sm:inset-x-auto sm:right-4 sm:bottom-4 sm:w-[400px]"
    >
      <div className="overflow-hidden rounded-t-xl border bg-popover text-popover-foreground shadow-lg sm:rounded-xl">
        <div className="flex items-center gap-3 border-b px-4 py-2.5">
          <div className="flex size-7 shrink-0 items-center justify-center rounded-md bg-muted text-muted-foreground">
            {running.length > 0 ? (
              <Upload className="size-4" />
            ) : failed > 0 ? (
              <CircleAlert className="size-4 text-destructive" />
            ) : (
              <CircleCheck className="size-4 text-emerald-600 dark:text-emerald-500" />
            )}
          </div>
          <div className="min-w-0 flex-1">
            <p className="truncate text-sm font-medium">{title}</p>
            {uploading.length > 0 && totalBytes > 0 && (
              <p className="text-xs text-muted-foreground tabular-nums">
                {formatPercent(percentOf(uploadedBytes, totalBytes))} ·{" "}
                {formatBytes(uploadedBytes)} / {formatBytes(totalBytes)}
              </p>
            )}
          </div>
          <Button
            variant="ghost"
            size="icon-sm"
            onClick={() => setCollapsed((value) => !value)}
            aria-expanded={!collapsed}
            aria-label={collapsed ? "展开上传列表" : "收起上传列表"}
          >
            <ChevronDown
              className={cn("transition-transform", collapsed && "rotate-180")}
            />
          </Button>
          {running.length === 0 && (
            <Button
              variant="ghost"
              size="icon-sm"
              onClick={dismissFinishedUploads}
              aria-label="关闭上传列表"
            >
              <X />
            </Button>
          )}
        </div>
        {!collapsed && (
          <ul className="max-h-[min(20rem,50svh)] divide-y overflow-y-auto">
            {tasks.map((task) => (
              <UploadTaskItem key={task.id} task={task} />
            ))}
          </ul>
        )}
      </div>
    </section>
  )
}

function UploadTaskItem({ task }: { task: UploadTask }) {
  const Icon = task.kind === "archive" ? FileArchive : Folder
  const running = isUploadRunning(task)

  return (
    <li className="flex gap-3 px-4 py-3">
      <div className="flex size-8 shrink-0 items-center justify-center rounded-md border bg-muted/50 text-muted-foreground">
        <Icon className="size-4" />
      </div>
      <div className="grid min-w-0 flex-1 gap-1.5">
        <div className="flex items-start gap-2">
          <div className="min-w-0 flex-1">
            <p className="truncate text-sm font-medium" title={task.fileName}>
              {task.fileName}
            </p>
            <p className="truncate text-xs text-muted-foreground">
              导入到{" "}
              <Link
                to={`/datasets/${task.datasetId}`}
                className="text-foreground underline-offset-4 hover:underline focus-visible:underline focus-visible:outline-none"
              >
                {task.datasetName}
              </Link>
            </p>
          </div>
          <div className="-mt-1 -mr-2 flex shrink-0 items-center">
            {task.status === "error" && task.retryable && (
              <Button
                variant="ghost"
                size="icon-sm"
                onClick={() => retryUpload(task.id)}
                aria-label={`重试上传 ${task.fileName}`}
              >
                <RotateCw />
              </Button>
            )}
            {running ? (
              <Button
                variant="ghost"
                size="icon-sm"
                onClick={() => void cancelUpload(task.id)}
                aria-label={`取消导入 ${task.fileName}`}
              >
                <X />
              </Button>
            ) : (
              <Button
                variant="ghost"
                size="icon-sm"
                onClick={() => dismissUpload(task.id)}
                aria-label={
                  isAbandonable(task)
                    ? `取消导入 ${task.fileName}`
                    : `从列表中移除 ${task.fileName}`
                }
              >
                <X />
              </Button>
            )}
          </div>
        </div>
        <UploadTaskStatus task={task} />
      </div>
    </li>
  )
}

function UploadTaskStatus({ task }: { task: UploadTask }) {
  switch (task.status) {
    case "uploading": {
      const remaining = etaSeconds(
        task.totalBytes - task.uploadedBytes,
        task.speed
      )
      const percent = percentOf(task.uploadedBytes, task.totalBytes)
      return (
        <>
          <TaskProgress value={percent} label="上传进度" />
          <p className="flex flex-wrap gap-x-1 text-xs text-muted-foreground tabular-nums">
            <span>
              {formatBytes(task.uploadedBytes)} / {formatBytes(task.totalBytes)}
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
          </p>
        </>
      )
    }
    case "committing":
      return (
        <>
          <TaskProgress value={null} label="提交进度" />
          <p className="text-xs text-muted-foreground">上传完成，正在提交…</p>
        </>
      )
    case "processing": {
      const detail = progressDetail(task.job)
      return (
        <>
          <TaskProgress value={jobPercent(task.job)} label="处理进度" />
          <p className="flex flex-wrap items-center gap-x-1 text-xs text-muted-foreground tabular-nums">
            <Spinner className="mr-0.5 size-3" aria-hidden />
            {stageLabel(task.job)}
            {detail && <span>· {detail}</span>}
          </p>
        </>
      )
    }
    case "done":
      return (
        <p className="flex items-start gap-1.5 text-xs text-muted-foreground">
          <CircleCheck className="mt-px size-3.5 shrink-0 text-emerald-600 dark:text-emerald-500" />
          <span>{summaryText(task.job) || "导入完成"}</span>
        </p>
      )
    case "error":
      return (
        <p className="flex items-start gap-1.5 text-xs text-destructive">
          <CircleAlert className="mt-px size-3.5 shrink-0" />
          <span className="break-words">{task.error}</span>
        </p>
      )
    case "canceled":
      return <p className="text-xs text-muted-foreground">已取消</p>
  }
}
