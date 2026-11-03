/** 上传面板（features/datasets/upload-tray.tsx）中的一个模型上传任务。 */
import {
  ArrowRight,
  Boxes,
  ChevronDown,
  CircleAlert,
  CircleCheck,
  RotateCw,
  TriangleAlert,
  X,
} from "lucide-react"
import { Link } from "react-router"

import { Button } from "@/components/ui/button"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import {
  TaskProgress,
  TransferProgress,
} from "@/features/datasets/task-progress"
import {
  cancelModelUploadTask,
  commitWithYoloVersion,
  dismissModelUpload,
  isAbandonable,
  isModelUploadActive,
  retryModelUpload,
  type ModelUploadTask,
} from "@/features/models/upload-manager"
import { yoloVersionLabel, yoloVersions } from "@/features/models/yolo-versions"

export function ModelUploadTaskItem({ task }: { task: ModelUploadTask }) {
  const active = isModelUploadActive(task)

  return (
    <li className="flex gap-3 px-4 py-3">
      <div className="flex size-8 shrink-0 items-center justify-center rounded-md border bg-muted/50 text-muted-foreground">
        <Boxes className="size-4" />
      </div>
      <div className="grid min-w-0 flex-1 grid-cols-[minmax(0,1fr)] gap-1.5">
        <div className="flex items-start gap-2">
          <div className="min-w-0 flex-1">
            <p className="truncate text-sm font-medium" title={task.fileName}>
              {task.fileName}
            </p>
            <p className="truncate text-xs text-muted-foreground">
              上传到{" "}
              <Link
                to={`/projects/${task.projectId}?tab=models`}
                className="text-foreground underline-offset-4 hover:underline focus-visible:underline focus-visible:outline-none"
              >
                {task.projectName}
              </Link>
              {" · "}
              {yoloVersionLabel(task.yoloVersion)}
            </p>
          </div>
          <div className="-mt-1 -mr-2 flex shrink-0 items-center">
            {task.status === "error" && task.retryable && (
              <Button
                variant="ghost"
                size="icon-sm"
                onClick={() => retryModelUpload(task.id)}
                aria-label={`重试上传 ${task.fileName}`}
              >
                <RotateCw />
              </Button>
            )}
            {active ? (
              <Button
                variant="ghost"
                size="icon-sm"
                onClick={() => void cancelModelUploadTask(task.id)}
                aria-label={`取消上传 ${task.fileName}`}
              >
                <X />
              </Button>
            ) : (
              <Button
                variant="ghost"
                size="icon-sm"
                onClick={() => dismissModelUpload(task.id)}
                aria-label={
                  isAbandonable(task)
                    ? `取消上传 ${task.fileName}`
                    : `从列表中移除 ${task.fileName}`
                }
              >
                <X />
              </Button>
            )}
          </div>
        </div>
        <ModelUploadStatusLine task={task} />
      </div>
    </li>
  )
}

/** 改选版本后保存：识别出了版本时一键改选，否则从版本列表中选择。 */
function MismatchAction({ task }: { task: ModelUploadTask }) {
  const detected = task.detectedVersion
  if (detected) {
    return (
      <Button
        variant="outline"
        size="xs"
        className="justify-self-start"
        onClick={() => commitWithYoloVersion(task.id, detected)}
      >
        改为 {yoloVersionLabel(detected)} 并保存
      </Button>
    )
  }
  return (
    <DropdownMenu modal={false}>
      <DropdownMenuTrigger asChild>
        <Button variant="outline" size="xs" className="justify-self-start">
          改选版本并保存
          <ChevronDown />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="start">
        {yoloVersions
          .filter((version) => version.value !== task.yoloVersion)
          .map((version) => (
            <DropdownMenuItem
              key={version.value}
              onSelect={() => commitWithYoloVersion(task.id, version.value)}
            >
              {version.label}
            </DropdownMenuItem>
          ))}
      </DropdownMenuContent>
    </DropdownMenu>
  )
}

function ModelUploadStatusLine({ task }: { task: ModelUploadTask }) {
  switch (task.status) {
    case "uploading":
      return (
        <TransferProgress
          uploadedBytes={task.uploadedBytes}
          totalBytes={task.totalBytes}
          speed={task.speed}
        />
      )
    case "committing":
      return (
        <>
          <TaskProgress value={null} label="校验进度" />
          <p className="text-xs text-muted-foreground">
            上传完成，正在校验模型文件…
          </p>
        </>
      )
    case "mismatch":
      return (
        <div className="grid grid-cols-[minmax(0,1fr)] gap-2">
          <p className="flex items-start gap-1.5 text-xs text-amber-700 dark:text-amber-500">
            <TriangleAlert className="mt-px size-3.5 shrink-0" />
            <span className="min-w-0 break-words">{task.error}</span>
          </p>
          <MismatchAction task={task} />
        </div>
      )
    case "done":
      return (
        <p className="flex items-center gap-1.5 text-xs text-muted-foreground">
          <CircleCheck className="size-3.5 shrink-0 text-emerald-600 dark:text-emerald-500" />
          <span>已保存为 v{task.model?.version}</span>
          {task.model && (
            <Link
              to={`/models/${task.model.id}`}
              className="inline-flex items-center gap-0.5 text-foreground underline-offset-4 hover:underline focus-visible:underline focus-visible:outline-none"
            >
              查看
              <ArrowRight className="size-3" />
            </Link>
          )}
        </p>
      )
    case "error":
      return (
        <p className="flex items-start gap-1.5 text-xs text-destructive">
          <CircleAlert className="mt-px size-3.5 shrink-0" />
          <span className="min-w-0 break-words">{task.error}</span>
        </p>
      )
    case "canceled":
      return <p className="text-xs text-muted-foreground">已取消</p>
  }
}
