/**
 * 上传模型：选择项目、YOLO 版本与 .pt 文件（可选说明）。提交后对话框显示上传
 * 进度，关闭对话框上传在后台继续（右下角的上传面板）；版本与文件不一致时可以
 * 直接改选识别出的版本再保存，文件不是可用的模型时显示原因。
 */
import { useId, useState, type FormEvent } from "react"
import { useQuery } from "@tanstack/react-query"
import {
  ArrowRight,
  Boxes,
  CircleAlert,
  CircleCheck,
  FileBox,
  Info,
  Plus,
  RotateCw,
  TriangleAlert,
} from "lucide-react"
import { Link } from "react-router"
import { toast } from "sonner"

import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
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
  Field,
  FieldDescription,
  FieldError,
  FieldLabel,
} from "@/components/ui/field"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { Skeleton } from "@/components/ui/skeleton"
import { Spinner } from "@/components/ui/spinner"
import { Textarea } from "@/components/ui/textarea"
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group"
import { DescriptionCounter } from "@/features/datasets/dataset-actions"
import {
  entriesFromDataTransfer,
  isDirectoryEntry,
} from "@/features/datasets/folder-input"
import { Dropzone, SelectedSource } from "@/features/datasets/source-pickers"
import {
  TaskProgress,
  TransferProgress,
} from "@/features/datasets/task-progress"
import { createModelUpload, type YoloVersion } from "@/features/models/api"
import { modelTaskText } from "@/features/models/model-list"
import {
  MODEL_ACCEPT,
  validateModelDescription,
  validateModelFile,
} from "@/features/models/naming"
import {
  cancelModelUploadTask,
  commitWithYoloVersion,
  dismissModelUpload,
  isModelUploadActive,
  isModelUploadRunning,
  retryModelUpload,
  startModelUpload,
  useWatchedModelUpload,
  type ModelUploadTask,
} from "@/features/models/upload-manager"
import {
  findYoloVersion,
  modelArchLabel,
  yoloVersionLabel,
  yoloVersions,
} from "@/features/models/yolo-versions"
import {
  projectKeys,
  projectsQuery,
  type Project,
} from "@/features/projects/api"
import { ApiError } from "@/lib/api"
import { formatBytes, formatNumber, formatRelativeTime } from "@/lib/format"
import { queryClient } from "@/lib/query-client"

const nameCollator = new Intl.Collator("zh-CN", { numeric: true })

type UploadModelDialogProps = {
  open: boolean
  onOpenChange: (open: boolean) => void
  /** 预选的项目（从项目页打开时）。 */
  projectId?: number | null
}

/** 每次打开时由调用方更换 key，表单从头开始。 */
export function UploadModelDialog({
  open,
  onOpenChange,
  projectId = null,
}: UploadModelDialogProps) {
  const [selectedProject, setSelectedProject] = useState<number | null>(
    projectId
  )
  const [yoloVersion, setYoloVersion] = useState<YoloVersion | null>(null)
  const [file, setFile] = useState<File | null>(null)
  const [description, setDescription] = useState("")
  const [submitted, setSubmitted] = useState(false)
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [taskId, setTaskId] = useState<string | null>(null)
  // 只在对话框打开时跟随任务：关闭后结果由上传管理器提示。
  const task = useWatchedModelUpload(open ? taskId : null)

  const dirty =
    file !== null || description.trim() !== "" || yoloVersion !== null
  const active = task !== undefined && isModelUploadActive(task)

  const close = () => {
    if (task && isModelUploadRunning(task)) {
      toast("上传在后台继续", {
        description: "可在右下角的上传面板中查看进度；上传期间请不要关闭页面。",
      })
    }
    onOpenChange(false)
  }

  /** 回到表单（如换一个文件）：从面板中移除已结束的任务。 */
  const backToForm = ({ keepFile }: { keepFile: boolean }) => {
    if (task && !isModelUploadActive(task)) {
      dismissModelUpload(task.id)
    }
    setTaskId(null)
    setSubmitted(false)
    if (!keepFile) {
      setFile(null)
    }
  }

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        if (next) {
          onOpenChange(true)
        } else if (!submitting) {
          close()
        }
      }}
    >
      <DialogContent
        className="max-h-[calc(100dvh-2rem)] overflow-y-auto sm:max-w-xl"
        onInteractOutside={(event) => {
          if (submitting || active || (task === undefined && dirty)) {
            event.preventDefault()
          }
        }}
        onDragOver={(event) => event.preventDefault()}
        onDrop={(event) => event.preventDefault()}
      >
        {task ? (
          <UploadTaskView
            task={task}
            onClose={close}
            onUploadAnother={() => {
              backToForm({ keepFile: false })
              setDescription("")
            }}
            onChooseFile={() => backToForm({ keepFile: false })}
            onRestart={() => backToForm({ keepFile: true })}
          />
        ) : (
          <UploadForm
            projectId={selectedProject}
            onProjectChange={setSelectedProject}
            yoloVersion={yoloVersion}
            onYoloVersionChange={setYoloVersion}
            file={file}
            onFileChange={setFile}
            description={description}
            onDescriptionChange={setDescription}
            submitted={submitted}
            submitting={submitting}
            error={error}
            onCancel={close}
            onSubmit={async (project) => {
              setSubmitted(true)
              setError(null)
              if (
                !project ||
                !yoloVersion ||
                !file ||
                validateModelFile(file) ||
                validateModelDescription(description)
              ) {
                return
              }
              setSubmitting(true)
              try {
                const upload = await createModelUpload({
                  project_id: project.id,
                  yolo_version: yoloVersion,
                  file_name: file.name,
                  file_size: file.size,
                  description: description.trim(),
                })
                setTaskId(
                  startModelUpload({
                    project: { id: project.id, name: project.name },
                    upload,
                    file,
                  })
                )
              } catch (cause) {
                setError(
                  cause instanceof Error ? cause.message : "创建上传任务失败"
                )
                // 多半是项目刚被删除：刷新项目列表。
                if (cause instanceof ApiError && cause.status === 422) {
                  void queryClient.invalidateQueries({
                    queryKey: projectKeys.lists(),
                  })
                }
              } finally {
                setSubmitting(false)
              }
            }}
          />
        )}
      </DialogContent>
    </Dialog>
  )
}

type UploadFormProps = {
  projectId: number | null
  onProjectChange: (id: number) => void
  yoloVersion: YoloVersion | null
  onYoloVersionChange: (version: YoloVersion) => void
  file: File | null
  onFileChange: (file: File | null) => void
  description: string
  onDescriptionChange: (description: string) => void
  submitted: boolean
  submitting: boolean
  error: string | null
  onCancel: () => void
  /** 所选项目在列表中已不存在时为 undefined。 */
  onSubmit: (project: Project | undefined) => void
}

function UploadForm({
  projectId,
  onProjectChange,
  yoloVersion,
  onYoloVersionChange,
  file,
  onFileChange,
  description,
  onDescriptionChange,
  submitted,
  submitting,
  error,
  onCancel,
  onSubmit,
}: UploadFormProps) {
  const projectFieldId = useId()
  const versionLabelId = useId()
  const descriptionId = useId()

  const projects = useQuery(projectsQuery())
  const sorted = [...(projects.data?.items ?? [])].sort((a, b) =>
    nameCollator.compare(a.name, b.name)
  )
  const project = sorted.find((candidate) => candidate.id === projectId)

  const projectError =
    submitted && !project
      ? projectId !== null && projects.data
        ? "所选项目已不存在，请重新选择"
        : "请选择项目"
      : null
  const versionError = submitted && !yoloVersion ? "请选择 YOLO 版本" : null
  const fileError = submitted && !file ? "请选择模型文件" : null
  const descriptionError = validateModelDescription(description)
  const version = findYoloVersion(yoloVersion)

  const submit = (event: FormEvent) => {
    event.preventDefault()
    if (!submitting) {
      onSubmit(project)
    }
  }

  return (
    <form onSubmit={submit} className="grid min-w-0 gap-6" noValidate>
      <DialogHeader>
        <DialogTitle>上传模型</DialogTitle>
        <DialogDescription>
          上传 Ultralytics 训练得到的 .pt
          权重，保存为所选项目的一个新版本。服务器会识别其中的任务、类别与输入尺寸，下载时可以自动转换为
          ONNX。
        </DialogDescription>
      </DialogHeader>

      <Field data-invalid={projectError ? true : undefined}>
        <FieldLabel htmlFor={projectFieldId}>项目</FieldLabel>
        {projects.isPending ? (
          <Skeleton className="h-9 w-full" />
        ) : projects.data === undefined ? (
          <p role="alert" className="text-sm text-destructive">
            无法获取项目列表：{projects.error?.message}
          </p>
        ) : sorted.length === 0 ? (
          <Alert>
            <Info />
            <AlertTitle>还没有项目</AlertTitle>
            <AlertDescription>
              <p>
                模型需要归属到一个项目（如某条产线的缺陷检测），请先新建项目。
              </p>
              <Button variant="outline" size="sm" asChild>
                <Link to="/projects">
                  <Plus />
                  新建项目
                </Link>
              </Button>
            </AlertDescription>
          </Alert>
        ) : (
          <Select
            value={project ? String(project.id) : ""}
            onValueChange={(value) => onProjectChange(Number(value))}
            disabled={submitting}
          >
            <SelectTrigger
              id={projectFieldId}
              className="w-full min-w-0 [&>span]:truncate"
              aria-invalid={projectError ? true : undefined}
            >
              <SelectValue placeholder="选择模型所属的项目" />
            </SelectTrigger>
            <SelectContent className="max-w-[calc(100vw-2rem)]">
              {sorted.map((candidate) => (
                <SelectItem key={candidate.id} value={String(candidate.id)}>
                  <span className="truncate">{candidate.name}</span>
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        )}
        {projectError ? (
          <FieldError>{projectError}</FieldError>
        ) : project ? (
          <FieldDescription>
            {project.latest_model
              ? `项目中已有 ${formatNumber(project.model_count)} 个模型，最新为 v${project.latest_model.version}（${yoloVersionLabel(project.latest_model.yolo_version)}，${formatRelativeTime(project.latest_model.created_at)}上传）。`
              : "这是项目中的第一个模型，将保存为 v1。"}
          </FieldDescription>
        ) : null}
      </Field>

      <Field data-invalid={versionError ? true : undefined}>
        <FieldLabel asChild>
          <span id={versionLabelId}>YOLO 版本</span>
        </FieldLabel>
        <ToggleGroup
          type="single"
          variant="outline"
          spacing={2}
          value={yoloVersion ?? ""}
          onValueChange={(value) => {
            if (value) {
              onYoloVersionChange(value as YoloVersion)
            }
          }}
          aria-labelledby={versionLabelId}
          className="grid w-full grid-cols-3 sm:grid-cols-6"
          disabled={submitting}
        >
          {yoloVersions.map((item) => (
            <ToggleGroupItem
              key={item.value}
              value={item.value}
              className="w-full data-[state=on]:border-primary data-[state=on]:bg-primary data-[state=on]:text-primary-foreground"
            >
              {item.label}
            </ToggleGroupItem>
          ))}
        </ToggleGroup>
        {versionError ? (
          <FieldError>{versionError}</FieldError>
        ) : (
          <FieldDescription>
            {version
              ? `${version.label}：${version.description}。`
              : "选择训练这个模型时使用的版本，"}
            上传后会与文件中的网络结构核对。
          </FieldDescription>
        )}
      </Field>

      <div className="grid gap-2">
        <span className="text-sm font-medium">模型文件</span>
        <ModelFilePicker
          file={file}
          onChange={onFileChange}
          disabled={submitting}
          missing={fileError}
        />
      </div>

      <Field data-invalid={descriptionError ? true : undefined}>
        <FieldLabel htmlFor={descriptionId}>说明</FieldLabel>
        <Textarea
          id={descriptionId}
          value={description}
          onChange={(event) => onDescriptionChange(event.target.value)}
          placeholder="训练数据、改动与效果等（可选）"
          aria-invalid={descriptionError ? true : undefined}
          disabled={submitting}
          className="min-h-20"
        />
        {descriptionError ? (
          <FieldError>{descriptionError}</FieldError>
        ) : (
          <DescriptionCounter value={description} />
        )}
      </Field>

      {error && (
        <p role="alert" className="text-sm text-destructive">
          {error}
        </p>
      )}

      <DialogFooter>
        <Button
          type="button"
          variant="outline"
          onClick={onCancel}
          disabled={submitting}
        >
          取消
        </Button>
        <Button
          type="submit"
          disabled={submitting || (projects.data?.items.length ?? 0) === 0}
        >
          {submitting && <Spinner aria-hidden />}
          上传
        </Button>
      </DialogFooter>
    </form>
  )
}

/** 选择或拖入一个 .pt 文件。 */
function ModelFilePicker({
  file,
  onChange,
  disabled,
  missing,
}: {
  file: File | null
  onChange: (file: File | null) => void
  disabled?: boolean
  /** 提交时还没有选择文件的提示。 */
  missing: string | null
}) {
  const [error, setError] = useState<string | null>(null)
  const [input, setInput] = useState<HTMLInputElement | null>(null)

  const accept = (candidate: File) => {
    const problem = validateModelFile(candidate)
    setError(problem)
    if (!problem) {
      onChange(candidate)
    }
  }

  const onDropData = (dataTransfer: DataTransfer) => {
    if (entriesFromDataTransfer(dataTransfer).some(isDirectoryEntry)) {
      setError("拖入的是文件夹，请选择其中的 .pt 文件")
      return
    }
    const files = Array.from(dataTransfer.files)
    if (files.length !== 1) {
      setError(
        files.length === 0 ? "没有读取到文件" : "一次只能上传一个模型文件"
      )
      return
    }
    accept(files[0])
  }

  if (file) {
    return (
      <SelectedSource
        icon={FileBox}
        title={file.name}
        meta={`PyTorch 权重 · ${formatBytes(file.size)}`}
        onRemove={() => onChange(null)}
        removeLabel="移除模型文件"
        disabled={disabled}
      >
        <p className="flex items-start gap-1.5 text-xs text-pretty text-muted-foreground">
          <Info className="mt-px size-3.5 shrink-0" />
          分块上传，网络中断后可以从断点继续；服务器只读取文件中的信息，不会执行其中的代码。
        </p>
      </SelectedSource>
    )
  }

  return (
    <>
      <Dropzone
        icon={FileBox}
        title="拖放 .pt 文件到这里，或点击选择"
        hint="如 runs/detect/train/weights/best.pt，最大 8 GB"
        buttonLabel="选择文件"
        onBrowse={() => input?.click()}
        onDropData={onDropData}
        error={error ?? missing}
        disabled={disabled}
      />
      <input
        ref={setInput}
        type="file"
        accept={MODEL_ACCEPT}
        className="sr-only"
        tabIndex={-1}
        aria-hidden
        onChange={(event) => {
          const selected = event.target.files?.[0]
          event.target.value = ""
          if (selected) {
            accept(selected)
          }
        }}
      />
    </>
  )
}

function UploadTaskView({
  task,
  onClose,
  onUploadAnother,
  onChooseFile,
  onRestart,
}: {
  task: ModelUploadTask
  onClose: () => void
  /** 完成后继续上传下一个（保留项目与版本）。 */
  onUploadAnother: () => void
  /** 文件不可用：换一个文件。 */
  onChooseFile: () => void
  /** 已取消：用同样的文件与选项重新开始。 */
  onRestart: () => void
}) {
  const titles: Record<ModelUploadTask["status"], string> = {
    uploading: "正在上传模型",
    committing: "正在校验模型",
    mismatch: "YOLO 版本不一致",
    done: "上传完成",
    error: task.errorCode === "invalid_model" ? "无法识别模型文件" : "上传失败",
    canceled: "已取消上传",
  }

  return (
    <div className="grid min-w-0 gap-6">
      <DialogHeader>
        <DialogTitle>{titles[task.status]}</DialogTitle>
        <DialogDescription className="break-words">
          {task.fileName} → {task.projectName}
        </DialogDescription>
      </DialogHeader>

      <UploadTaskBody task={task} />

      <DialogFooter>
        <UploadTaskActions
          task={task}
          onClose={onClose}
          onUploadAnother={onUploadAnother}
          onChooseFile={onChooseFile}
          onRestart={onRestart}
        />
      </DialogFooter>
    </div>
  )
}

function UploadTaskBody({ task }: { task: ModelUploadTask }) {
  const [chosen, setChosen] = useState<YoloVersion | null>(null)

  switch (task.status) {
    case "uploading":
      return (
        <div className="grid gap-3">
          <div className="grid gap-1.5">
            <TransferProgress
              uploadedBytes={task.uploadedBytes}
              totalBytes={task.totalBytes}
              speed={task.speed}
            />
          </div>
          <p className="text-xs text-muted-foreground">
            可以关闭对话框，上传会在后台继续（右下角的上传面板显示进度）；上传期间请不要关闭页面。
          </p>
        </div>
      )
    case "committing":
      return (
        <div className="grid gap-1.5">
          <TaskProgress value={null} label="校验进度" />
          <p className="text-xs text-muted-foreground">
            上传完成，正在计算校验值并识别模型的任务与类别…
          </p>
        </div>
      )
    case "mismatch":
      return (
        <div className="grid gap-4">
          <Alert className="border-amber-500/40 [&>svg]:text-amber-600 dark:[&>svg]:text-amber-500">
            <TriangleAlert />
            <AlertTitle>文件中的模型不是所选的版本</AlertTitle>
            <AlertDescription>
              <p className="break-words">{task.error}</p>
              <p>文件已经上传到服务器，改选版本后直接保存，不需要重新上传。</p>
            </AlertDescription>
          </Alert>
          {!task.detectedVersion && (
            <div className="grid gap-2">
              <span className="text-sm font-medium">改选版本</span>
              <ToggleGroup
                type="single"
                variant="outline"
                spacing={2}
                value={chosen ?? ""}
                onValueChange={(value) => {
                  if (value) {
                    setChosen(value as YoloVersion)
                  }
                }}
                aria-label="改选 YOLO 版本"
                className="grid w-full grid-cols-3 sm:grid-cols-6"
              >
                {yoloVersions
                  .filter((item) => item.value !== task.yoloVersion)
                  .map((item) => (
                    <ToggleGroupItem
                      key={item.value}
                      value={item.value}
                      className="w-full data-[state=on]:border-primary data-[state=on]:bg-primary data-[state=on]:text-primary-foreground"
                    >
                      {item.label}
                    </ToggleGroupItem>
                  ))}
              </ToggleGroup>
              <Button
                type="button"
                className="justify-self-end"
                disabled={!chosen}
                onClick={() => chosen && commitWithYoloVersion(task.id, chosen)}
              >
                按{chosen ? ` ${yoloVersionLabel(chosen)} ` : "所选版本"}保存
              </Button>
            </div>
          )}
        </div>
      )
    case "done": {
      const model = task.model
      return (
        <div className="flex items-start gap-3 rounded-lg border bg-muted/30 p-4">
          <CircleCheck className="mt-0.5 size-5 shrink-0 text-emerald-600 dark:text-emerald-500" />
          <div className="grid min-w-0 gap-1">
            <p className="font-medium">已上传为 v{model?.version ?? "?"}</p>
            {model && (
              <p className="text-sm text-muted-foreground">
                {modelArchLabel(model)} · {modelTaskText(model)}
                {model.info.image_size !== null &&
                  ` · 输入 ${model.info.image_size}`}
                {" · "}
                {formatBytes(model.size_bytes)}
              </p>
            )}
          </div>
        </div>
      )
    }
    case "error":
      return (
        <Alert variant="destructive">
          <CircleAlert />
          <AlertTitle>
            {task.errorCode === "invalid_model"
              ? "文件不是平台支持的 YOLO 模型"
              : "上传没有完成"}
          </AlertTitle>
          <AlertDescription>
            <p className="break-words">{task.error}</p>
            {task.retryable && (
              <p>已上传的部分保留在服务器上，重试时从断点继续。</p>
            )}
          </AlertDescription>
        </Alert>
      )
    case "canceled":
      return (
        <p className="text-sm text-muted-foreground">
          上传已取消，服务器上的临时文件已删除。
        </p>
      )
  }
}

function UploadTaskActions({
  task,
  onClose,
  onUploadAnother,
  onChooseFile,
  onRestart,
}: {
  task: ModelUploadTask
  onClose: () => void
  onUploadAnother: () => void
  onChooseFile: () => void
  onRestart: () => void
}) {
  const cancel = () => void cancelModelUploadTask(task.id)

  switch (task.status) {
    case "uploading":
    case "committing":
      return (
        <>
          <Button type="button" variant="ghost" onClick={cancel}>
            取消上传
          </Button>
          <Button type="button" variant="outline" onClick={onClose}>
            在后台继续
          </Button>
        </>
      )
    case "mismatch":
      return (
        <>
          <Button type="button" variant="outline" onClick={cancel}>
            取消上传
          </Button>
          {task.detectedVersion && (
            <Button
              type="button"
              onClick={() =>
                task.detectedVersion &&
                commitWithYoloVersion(task.id, task.detectedVersion)
              }
            >
              改为 {yoloVersionLabel(task.detectedVersion)} 并保存
            </Button>
          )}
        </>
      )
    case "done":
      return (
        <>
          <Button type="button" variant="outline" onClick={onUploadAnother}>
            <Plus />
            继续上传
          </Button>
          {task.model && (
            <Button asChild>
              <Link to={`/models/${task.model.id}`} onClick={onClose}>
                <Boxes />
                查看模型
                <ArrowRight />
              </Link>
            </Button>
          )}
        </>
      )
    case "error":
      return task.retryable ? (
        <>
          <Button
            type="button"
            variant="outline"
            onClick={() => {
              // 放弃：取消服务端保留的部分（见 dismissModelUpload）。
              dismissModelUpload(task.id)
              onClose()
            }}
          >
            放弃上传
          </Button>
          <Button type="button" onClick={() => retryModelUpload(task.id)}>
            <RotateCw />
            重试
          </Button>
        </>
      ) : (
        <>
          <Button type="button" variant="outline" onClick={onClose}>
            关闭
          </Button>
          <Button type="button" onClick={onChooseFile}>
            重新选择文件
          </Button>
        </>
      )
    case "canceled":
      return (
        <>
          <Button type="button" variant="outline" onClick={onClose}>
            关闭
          </Button>
          <Button type="button" onClick={onRestart}>
            重新上传
          </Button>
        </>
      )
  }
}
