/**
 * 下载模型：PT 直接下载；ONNX 先请求转换（由训练节点执行，结果缓存在服务器），
 * 已有同样选项的结果时直接下载，否则显示转换进度，完成后自动开始下载。
 * 关闭对话框不会取消转换。
 */
import {
  useEffect,
  useId,
  useMemo,
  useRef,
  useState,
  type FormEvent,
  type ReactNode,
} from "react"
import { useMutation, useQuery } from "@tanstack/react-query"
import {
  Check,
  CircleAlert,
  CircleCheck,
  Download,
  Info,
  RotateCw,
  Server,
  TriangleAlert,
} from "lucide-react"
import { Link } from "react-router"
import { toast } from "sonner"

import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
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
  Field,
  FieldDescription,
  FieldError,
  FieldLabel,
} from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { RadioGroup, RadioGroupItem } from "@/components/ui/radio-group"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { Spinner } from "@/components/ui/spinner"
import { Switch } from "@/components/ui/switch"
import { TaskProgress } from "@/features/datasets/task-progress"
import {
  deleteModelExport,
  isActiveExport,
  modelExportQuery,
  modelExportsQuery,
  modelExistsQuery,
  modelKeys,
  refreshModelExports,
  requestModelExport,
  type ExportOptions,
  type Model,
  type ModelExport,
  type Postprocess,
} from "@/features/models/api"
import {
  exportProgressView,
  exportStatusText,
  exportStep,
  exportSteps,
  isNotFound,
  modelTitle,
  type ExportProgressView,
  shapeText,
} from "@/features/models/model-info"
import {
  allowedPostprocess,
  defaultExportOptions,
  describePostprocess,
  exportFileName,
  exportOptionsText,
  fitExportOptions,
  modelArchLabel,
  modelFileName,
  opsetChoices,
  postprocessConflict,
  rawOnlyNote,
  sameExportOptions,
  taskLabel,
  validateImgSize,
} from "@/features/models/yolo-versions"
import { CopyButton } from "@/features/training/command-block"
import { useNow } from "@/hooks/use-now"
import { startDownload } from "@/lib/download"
import { formatBytes, formatDuration, formatNumber } from "@/lib/format"
import { queryClient } from "@/lib/query-client"
import { cn } from "@/lib/utils"

export type DownloadFormat = "pt" | "onnx"

/** 训练节点需要的最低版本（带转换任务的版本）。 */
const MIN_TRAINER_VERSION = "0.2.0"
/** 常用的输入尺寸，一键填入正方形。 */
const SIZE_PRESETS = [320, 480, 640, 960, 1280]

type DownloadModelDialogProps = {
  /** 为 null 时关闭。每次打开时由调用方更换 key，选项从默认值开始。 */
  model: Model | null
  initialFormat: DownloadFormat
  /** 打开时直接显示这个转换的进度（转换记录中“查看进度 / 原因”）。 */
  exportJob?: ModelExport | null
  onOpenChange: (open: boolean) => void
}

export function DownloadModelDialog({
  model,
  initialFormat,
  exportJob = null,
  onOpenChange,
}: DownloadModelDialogProps) {
  return (
    <Dialog open={model !== null} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[calc(100dvh-2rem)] overflow-y-auto sm:max-w-xl">
        {model && (
          <DownloadModel
            model={model}
            initialFormat={initialFormat}
            exportJob={exportJob}
            onDone={() => onOpenChange(false)}
          />
        )}
      </DialogContent>
    </Dialog>
  )
}

function modelSummary(model: Model) {
  const classes = model.info.class_names.length
  return [
    modelArchLabel(model),
    taskLabel(model.task),
    classes > 0 ? `${formatNumber(classes)} 个类别` : null,
  ]
    .filter(Boolean)
    .join(" · ")
}

/** 请求中的转换：对话框从选项切换到进度。 */
type Tracking = { exportId: number; requestedAt: number }

/** 已有的转换从创建时开始计算等待时间。 */
function trackingOf(job: ModelExport): Tracking {
  const created = Date.parse(job.created_at)
  return {
    exportId: job.id,
    requestedAt: Number.isNaN(created) ? Date.now() : created,
  }
}

function DownloadModel({
  model,
  initialFormat,
  exportJob,
  onDone,
}: {
  model: Model
  initialFormat: DownloadFormat
  exportJob: ModelExport | null
  onDone: () => void
}) {
  const defaults = useMemo(() => defaultExportOptions(model), [model])
  const [format, setFormat] = useState<DownloadFormat>(initialFormat)
  // 已有的转换记录可能早于现在的规则（如动态尺寸 + 嵌入 NMS），回填时换为可用的选项。
  const [options, setOptions] = useState<OptionsDraft>(() =>
    toDraft(fitExportOptions(exportJob?.options ?? defaults, model))
  )
  const [tracking, setTracking] = useState<Tracking | null>(() =>
    exportJob ? trackingOf(exportJob) : null
  )

  const request = useMutation({
    mutationFn: (exportOptions: ExportOptions) =>
      requestModelExport(model.id, { format: "onnx", ...exportOptions }),
    onSuccess: (job) => {
      queryClient.setQueryData(modelKeys.export(model.id, job.id), job)
      void queryClient.invalidateQueries({
        queryKey: modelKeys.exports(model.id),
      })
      if (job.status === "succeeded" && job.download_url) {
        // 同样的选项转换过：直接下载缓存的结果。
        startDownload(job.download_url, job.file_name)
        toast.success("已开始下载", {
          description: `${job.file_name}（已转换过，直接使用缓存的结果）`,
        })
        onDone()
        return
      }
      setTracking({ exportId: job.id, requestedAt: Date.now() })
    },
  })

  if (tracking) {
    return (
      <ExportProgress
        model={model}
        tracking={tracking}
        onRetry={(job) => request.mutate(job.options)}
        onEditOptions={(job) => {
          if (job) {
            setOptions(toDraft(fitExportOptions(job.options, model)))
          }
          request.reset()
          setTracking(null)
        }}
        onDone={onDone}
        retrying={request.isPending}
        retryError={request.error}
      />
    )
  }

  return (
    <DownloadForm
      model={model}
      format={format}
      onFormatChange={(next) => {
        setFormat(next)
        request.reset()
      }}
      options={options}
      onOptionsChange={(next) => {
        setOptions(next)
        request.reset()
      }}
      defaults={defaults}
      pending={request.isPending}
      error={request.error}
      onSubmit={(exportOptions) => {
        if (exportOptions === null) {
          startDownload(
            model.download_url,
            modelFileName(model.project.name, model.version)
          )
          toast.success("已开始下载", {
            description: `${modelTitle(model)}（${formatBytes(model.size_bytes)}），可在浏览器的下载列表中查看进度。`,
          })
          onDone()
        } else {
          // 表单已经排除了不能组合的选项，这里再保证一次不会发出。
          request.mutate(fitExportOptions(exportOptions, model))
        }
      }}
      onCancel={onDone}
    />
  )
}

/** 表单中的选项：尺寸是输入框中的文字，校验通过后才能提交。 */
type OptionsDraft = {
  height: string
  width: string
  dynamic: boolean
  postprocess: Postprocess
  /** `"auto"` 或 opset 数字。 */
  opset: string
}

function toDraft(options: ExportOptions): OptionsDraft {
  return {
    height: String(options.imgsz[0]),
    width: String(options.imgsz[1]),
    dynamic: options.dynamic,
    postprocess: options.postprocess,
    opset: options.opset === null ? "auto" : String(options.opset),
  }
}

/** 选项合法时得到规范化的选项，否则为 null。 */
function fromDraft(draft: OptionsDraft): ExportOptions | null {
  if (validateImgSize(draft.height) || validateImgSize(draft.width)) {
    return null
  }
  return {
    imgsz: [Number(draft.height.trim()), Number(draft.width.trim())],
    dynamic: draft.dynamic,
    postprocess: draft.postprocess,
    opset: draft.opset === "auto" ? null : Number(draft.opset),
  }
}

function DownloadForm({
  model,
  format,
  onFormatChange,
  options,
  onOptionsChange,
  defaults,
  pending,
  error,
  onSubmit,
  onCancel,
}: {
  model: Model
  format: DownloadFormat
  onFormatChange: (format: DownloadFormat) => void
  options: OptionsDraft
  onOptionsChange: (options: OptionsDraft) => void
  defaults: ExportOptions
  pending: boolean
  error: Error | null
  /** PT 时为 null。 */
  onSubmit: (options: ExportOptions | null) => void
  onCancel: () => void
}) {
  const formatId = useId()
  const heightId = useId()
  const widthId = useId()
  const dynamicId = useId()
  const postprocessId = useId()
  const opsetId = useId()

  const exports = useQuery({
    ...modelExportsQuery(model.id),
    enabled: format === "onnx",
  })
  const allowed = allowedPostprocess(model)
  const onlyRaw = rawOnlyNote(model)
  const heightError = validateImgSize(options.height)
  const widthError = validateImgSize(options.width)
  const sizeError = heightError ?? widthError
  const exportOptions = fromDraft(options)
  const cached =
    exportOptions !== null &&
    exports.data?.items.some(
      (job) =>
        job.status === "succeeded" &&
        sameExportOptions(job.options, exportOptions)
    )
  const running =
    exportOptions !== null &&
    exports.data?.items.some(
      (job) =>
        isActiveExport(job) && sameExportOptions(job.options, exportOptions)
    )
  const imgsz: [number, number] = exportOptions?.imgsz ?? defaults.imgsz
  const classCount =
    model.info.class_names.length > 0 ? model.info.class_names.length : null
  const isOnnx = format === "onnx"

  const update = (changes: Partial<OptionsDraft>) =>
    onOptionsChange({ ...options, ...changes })

  const submit = (event: FormEvent) => {
    event.preventDefault()
    if (pending || model.file_missing) {
      return
    }
    if (!isOnnx) {
      onSubmit(null)
      return
    }
    if (exportOptions) {
      onSubmit(exportOptions)
    }
  }

  return (
    <form
      onSubmit={submit}
      className="grid min-w-0 grid-cols-[minmax(0,1fr)] gap-6"
      noValidate
    >
      <DialogHeader>
        <DialogTitle>下载模型</DialogTitle>
        <DialogDescription className="break-words">
          {modelTitle(model)} · {modelSummary(model)}
        </DialogDescription>
      </DialogHeader>

      {model.file_missing && (
        <Alert variant="destructive">
          <CircleAlert />
          <AlertTitle>模型文件不存在</AlertTitle>
          <AlertDescription>
            服务器上找不到这个模型的文件，无法下载或转换。可以删除该模型后重新上传。
          </AlertDescription>
        </Alert>
      )}

      <div className="grid gap-3">
        <Label id={formatId}>格式</Label>
        <RadioGroup
          value={format}
          onValueChange={(value) => onFormatChange(value as DownloadFormat)}
          aria-labelledby={formatId}
          className="grid gap-2 sm:grid-cols-2"
          disabled={pending}
        >
          <ChoiceCard
            id={`${formatId}-pt`}
            value="pt"
            selected={format === "pt"}
            title="PT"
            badge={formatBytes(model.size_bytes)}
            description="原始的 PyTorch 权重，Ultralytics 可以直接加载或继续训练"
          />
          <ChoiceCard
            id={`${formatId}-onnx`}
            value="onnx"
            selected={format === "onnx"}
            title="ONNX"
            badge="自动转换"
            description="由训练节点转换，用于 ONNX Runtime、OpenCV DNN、TensorRT 等部署"
          />
        </RadioGroup>
      </div>

      {isOnnx && (
        <div className="grid min-w-0 gap-5">
          <Field data-invalid={sizeError ? true : undefined}>
            <FieldLabel htmlFor={heightId}>输入尺寸（高 × 宽）</FieldLabel>
            <div className="flex flex-wrap items-center gap-2">
              <div className="flex items-center gap-2">
                <Input
                  id={heightId}
                  value={options.height}
                  onChange={(event) => update({ height: event.target.value })}
                  inputMode="numeric"
                  className="w-24 tabular-nums"
                  aria-label="高"
                  aria-invalid={heightError ? true : undefined}
                  disabled={pending}
                />
                <span className="text-muted-foreground" aria-hidden>
                  ×
                </span>
                <Input
                  id={widthId}
                  value={options.width}
                  onChange={(event) => update({ width: event.target.value })}
                  inputMode="numeric"
                  className="w-24 tabular-nums"
                  aria-label="宽"
                  aria-invalid={widthError ? true : undefined}
                  disabled={pending}
                />
              </div>
              <div className="flex flex-wrap gap-1">
                {SIZE_PRESETS.map((size) => {
                  const active =
                    options.height === String(size) &&
                    options.width === String(size)
                  return (
                    <Button
                      key={size}
                      type="button"
                      variant={active ? "secondary" : "ghost"}
                      size="xs"
                      className="tabular-nums"
                      onClick={() =>
                        update({ height: String(size), width: String(size) })
                      }
                      disabled={pending}
                      aria-pressed={active}
                    >
                      {size}
                    </Button>
                  )
                })}
              </div>
            </div>
            {sizeError ? (
              <FieldError>{sizeError}</FieldError>
            ) : (
              <FieldDescription>
                默认与训练时的尺寸相同
                {model.info.image_size !== null
                  ? `（${model.info.image_size}）`
                  : "（文件中没有记录，使用 640）"}
                ；必须是 32 的倍数。
              </FieldDescription>
            )}
          </Field>

          <div className="flex items-start justify-between gap-4">
            <div className="grid gap-1">
              <Label htmlFor={dynamicId}>动态尺寸</Label>
              <p className="text-xs text-muted-foreground">
                batch、高、宽可以在推理时改变（不能与嵌入 NMS
                同时使用）；上面的尺寸是导出时使用的示例输入。
              </p>
            </div>
            <Switch
              id={dynamicId}
              checked={options.dynamic}
              // 打开时已选的嵌入 NMS 换为默认的后处理方式。
              onCheckedChange={(checked) =>
                onOptionsChange(
                  fitExportOptions({ ...options, dynamic: checked }, model)
                )
              }
              disabled={pending}
            />
          </div>

          <div className="grid gap-3">
            <Label id={postprocessId}>后处理方式</Label>
            <RadioGroup
              value={options.postprocess}
              onValueChange={(value) =>
                update({ postprocess: value as Postprocess })
              }
              aria-labelledby={postprocessId}
              className="grid gap-2"
              disabled={pending}
            >
              {allowed.map((mode) => {
                const info = describePostprocess(mode, {
                  task: model.task,
                  classCount,
                  imgsz,
                  dynamic: options.dynamic,
                })
                const conflict = postprocessConflict(mode, options.dynamic)
                return (
                  <ChoiceCard
                    key={mode}
                    id={`${postprocessId}-${mode}`}
                    value={mode}
                    selected={options.postprocess === mode}
                    title={info.label}
                    code={mode}
                    badge={mode === defaults.postprocess ? "默认" : undefined}
                    description={info.summary}
                    disabled={conflict !== null}
                    note={conflict ?? undefined}
                  >
                    {info.outputs.length > 0 && (
                      <ul className="grid gap-1 text-xs text-muted-foreground">
                        {info.outputs.map((output) => (
                          <li key={output.shape} className="min-w-0">
                            <code className="mr-1.5 font-mono text-foreground">
                              {output.shape}
                            </code>
                            {output.meaning}
                          </li>
                        ))}
                      </ul>
                    )}
                  </ChoiceCard>
                )
              })}
            </RadioGroup>
            {onlyRaw && (
              <p className="text-xs text-muted-foreground">{onlyRaw}</p>
            )}
          </div>

          <div className="flex flex-wrap items-center justify-between gap-x-4 gap-y-2">
            <div className="grid gap-1">
              <Label htmlFor={opsetId}>opset</Label>
              <p className="text-xs text-muted-foreground">
                ONNX 算子集版本；部署环境较旧时选择较低的版本。
              </p>
            </div>
            <Select
              value={options.opset}
              onValueChange={(value) => update({ opset: value })}
              disabled={pending}
            >
              <SelectTrigger id={opsetId} className="w-36">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="auto">自动（推荐）</SelectItem>
                {opsetChoices.map((opset) => (
                  <SelectItem key={opset} value={String(opset)}>
                    {opset}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>

          {exportOptions && (
            <div className="grid gap-1.5 rounded-lg border bg-muted/30 px-3 py-2.5 text-xs">
              <p className="flex flex-wrap items-center gap-x-2 gap-y-1 text-muted-foreground">
                文件名
                {cached ? (
                  <Badge variant="outline" className="font-normal">
                    <Check className="text-emerald-600 dark:text-emerald-500" />
                    已转换过，直接下载
                  </Badge>
                ) : running ? (
                  <Badge variant="outline" className="font-normal">
                    <Spinner className="size-3" aria-hidden />
                    正在转换
                  </Badge>
                ) : null}
              </p>
              <code className="font-mono break-all text-foreground">
                {exportFileName(
                  model.project.name,
                  model.version,
                  exportOptions
                )}
              </code>
            </div>
          )}
        </div>
      )}

      {error && (
        <p role="alert" className="text-sm text-destructive">
          {error.message}
        </p>
      )}

      <DialogFooter>
        <Button
          type="button"
          variant="outline"
          onClick={onCancel}
          disabled={pending}
        >
          取消
        </Button>
        <Button type="submit" disabled={pending || model.file_missing}>
          {pending ? <Spinner aria-hidden /> : <Download />}
          {!isOnnx ? "下载 PT" : cached ? "下载 ONNX" : "转换并下载"}
        </Button>
      </DialogFooter>
    </form>
  )
}

/** 单选卡片：标题、标记与说明，选中时高亮。 */
function ChoiceCard({
  id,
  value,
  selected,
  title,
  code,
  badge,
  description,
  disabled = false,
  note,
  children,
}: {
  id: string
  value: string
  selected: boolean
  title: string
  /** 标题旁的等宽标识，如 `end2end`。 */
  code?: string
  badge?: string
  description: string
  /** 不能选择（原因写在 note 中）。 */
  disabled?: boolean
  /** 说明下方的提示，如为什么不能选择。 */
  note?: string
  children?: ReactNode
}) {
  return (
    <label
      htmlFor={id}
      className={cn(
        "flex min-w-0 cursor-pointer items-start gap-3 rounded-lg border p-3 transition-colors hover:bg-muted/40",
        selected && "border-primary/60 bg-muted/40",
        disabled && "cursor-not-allowed hover:bg-transparent"
      )}
    >
      <RadioGroupItem
        id={id}
        value={value}
        className="mt-0.5"
        disabled={disabled}
      />
      <span className="grid min-w-0 flex-1 gap-1">
        <span
          className={cn(
            "flex flex-wrap items-center gap-x-1.5 gap-y-1 text-sm font-medium",
            disabled && "text-muted-foreground"
          )}
        >
          {title}
          {code && (
            <span className="font-mono text-xs font-normal text-muted-foreground">
              {code}
            </span>
          )}
          {badge && (
            <Badge variant="secondary" className="font-normal tabular-nums">
              {badge}
            </Badge>
          )}
        </span>
        <span className="text-xs text-pretty text-muted-foreground">
          {description}
        </span>
        {note && (
          <span className="flex items-start gap-1.5 text-xs text-pretty text-amber-700 dark:text-amber-500">
            <Info className="mt-px size-3.5 shrink-0" />
            {note}
          </span>
        )}
        {children}
      </span>
    </label>
  )
}

function ExportProgress({
  model,
  tracking,
  onRetry,
  onEditOptions,
  onDone,
  retrying,
  retryError,
}: {
  model: Model
  tracking: Tracking
  /** 失败后用同样的选项再次请求。 */
  onRetry: (job: ModelExport) => void
  /** 回到选项，用 job 的选项回填（没有转换记录时 job 为 null）。 */
  onEditOptions: (job: ModelExport | null) => void
  onDone: () => void
  retrying: boolean
  retryError: Error | null
}) {
  const query = useQuery(modelExportQuery(model.id, tracking.exportId))
  // 转换 404 时确认模型是否还在（模型或项目被删除时转换也随之删除）；无法确认时
  // 按转换被删除处理。
  const exportGone = isNotFound(query.error)
  const modelExists = useQuery({
    ...modelExistsQuery(model.id),
    enabled: exportGone,
  })
  const view = exportProgressView({
    job: query.data,
    error: query.error,
    modelGone:
      modelExists.data !== undefined
        ? !modelExists.data
        : modelExists.isError || !exportGone
          ? false
          : null,
  })
  // 被删除后缓存中仍是删除前的记录：只在没有被删除时使用它。
  const job = "job" in view ? view.job : undefined
  const active = view.kind === "active"
  const now = useNow(1_000)
  const downloaded = useRef<number | null>(null)

  const cancel = useMutation({
    mutationFn: () => deleteModelExport(model.id, tracking.exportId),
    onSuccess: () => {
      queryClient.removeQueries({
        queryKey: modelKeys.export(model.id, tracking.exportId),
      })
      refreshModelExports(model.id)
      toast("已取消转换")
      onEditOptions(job ?? null)
    },
  })

  // 结束或被删除时刷新转换记录与模型的转换数；成功时自动开始下载（每个转换只下载一次）。
  const status = query.data?.status
  useEffect(() => {
    if (exportGone || status === "succeeded" || status === "failed") {
      refreshModelExports(model.id)
    }
  }, [exportGone, status, model.id])
  useEffect(() => {
    if (
      job?.status === "succeeded" &&
      job.download_url &&
      downloaded.current !== job.id
    ) {
      downloaded.current = job.id
      startDownload(job.download_url, job.file_name)
    }
  }, [job])

  // 记录早于现在的规则（如动态尺寸 + 嵌入 NMS）时不能原样重试，只能修改选项。
  const retryable =
    job?.status === "failed" &&
    fitExportOptions(job.options, model).postprocess === job.options.postprocess

  const titles: Record<ExportProgressView["kind"], string> = {
    deleted: "转换任务已被删除",
    model_deleted: "模型已被删除",
    loading: "正在转换为 ONNX",
    unavailable: "正在转换为 ONNX",
    active: "正在转换为 ONNX",
    succeeded: "转换完成",
    failed: "转换失败",
  }

  return (
    <div className="grid min-w-0 grid-cols-[minmax(0,1fr)] gap-6">
      <DialogHeader>
        <DialogTitle>{titles[view.kind]}</DialogTitle>
        <DialogDescription className="break-words">
          {modelTitle(model)}
          {query.data && ` · ${exportOptionsText(query.data.options)}`}
        </DialogDescription>
      </DialogHeader>

      {view.kind === "deleted" ? (
        <Alert>
          <CircleAlert />
          <AlertDescription>
            这个转换任务已在别处（如转换记录中）被删除，不会再继续。可以返回修改选项后重新转换。
          </AlertDescription>
        </Alert>
      ) : view.kind === "model_deleted" ? (
        <Alert>
          <CircleAlert />
          <AlertDescription>
            这个模型（或所在的项目）已被删除，无法继续转换或下载。
          </AlertDescription>
        </Alert>
      ) : view.kind === "unavailable" ? (
        <p role="alert" className="text-sm text-destructive">
          无法获取转换状态：{view.error.message}
        </p>
      ) : view.kind === "loading" ? (
        <div className="flex items-center gap-2 text-sm text-muted-foreground">
          <Spinner aria-hidden />
          正在获取转换状态…
        </div>
      ) : view.kind === "succeeded" ? (
        <ExportSucceeded job={view.job} />
      ) : view.kind === "failed" ? (
        <ExportFailed job={view.job} />
      ) : (
        <ExportRunning
          job={view.job}
          elapsedSeconds={Math.max(0, (now - tracking.requestedAt) / 1000)}
        />
      )}

      {active && (
        <p className="text-xs text-muted-foreground">
          关闭对话框不会取消转换，完成后可以在模型详情页的转换记录中下载。
        </p>
      )}
      {(cancel.error ?? retryError) && (
        <p role="alert" className="text-sm text-destructive">
          {(cancel.error ?? retryError)?.message}
        </p>
      )}

      <DialogFooter>
        {view.kind === "active" ? (
          <>
            <Button
              type="button"
              variant="ghost"
              onClick={() => cancel.mutate()}
              disabled={cancel.isPending}
            >
              {cancel.isPending && <Spinner aria-hidden />}
              取消转换
            </Button>
            <Button type="button" variant="outline" onClick={onDone}>
              在后台继续
            </Button>
          </>
        ) : view.kind === "failed" ? (
          <>
            <Button
              type="button"
              variant={retryable ? "outline" : "default"}
              onClick={() => onEditOptions(view.job)}
              disabled={retrying}
            >
              修改选项
            </Button>
            {retryable && (
              <Button
                type="button"
                onClick={() => onRetry(view.job)}
                disabled={retrying}
              >
                {retrying ? <Spinner aria-hidden /> : <RotateCw />}
                重试
              </Button>
            )}
          </>
        ) : view.kind === "deleted" ? (
          // 用删除前的选项回到表单，方便重新转换。
          <Button
            type="button"
            onClick={() => onEditOptions(query.data ?? null)}
          >
            返回
          </Button>
        ) : view.kind === "model_deleted" ? (
          <Button type="button" onClick={onDone}>
            关闭
          </Button>
        ) : (
          <Button type="button" onClick={onDone}>
            完成
          </Button>
        )}
      </DialogFooter>
    </div>
  )
}

function ExportRunning({
  job,
  elapsedSeconds,
}: {
  job: ModelExport
  elapsedSeconds: number
}) {
  const step = exportStep(job)
  const noWorkers = job.status === "queued" && job.workers_online === 0

  return (
    <div className="grid min-w-0 gap-4">
      <div className="grid gap-2">
        <div className="flex flex-wrap items-center justify-between gap-x-3 gap-y-1 text-sm">
          <span className="flex items-center gap-2 font-medium">
            <Spinner className="size-4" aria-hidden />
            {exportStatusText(job)}
          </span>
          <span className="text-xs text-muted-foreground tabular-nums">
            已等待 {formatDuration(Math.floor(elapsedSeconds))}
          </span>
        </div>
        <TaskProgress value={null} label="转换进度" />
      </div>

      <ol
        className="grid grid-cols-4 gap-1 text-center text-xs"
        aria-label="转换步骤"
      >
        {[{ label: "排队" }, ...exportSteps].map((item, index) => {
          const done = index < step
          const current = index === step
          return (
            <li
              key={item.label}
              className={cn(
                "grid justify-items-center gap-1.5 text-muted-foreground",
                current && "font-medium text-foreground"
              )}
              aria-current={current ? "step" : undefined}
            >
              <span
                className={cn(
                  "flex size-6 items-center justify-center rounded-full border bg-background tabular-nums",
                  done && "border-primary bg-primary text-primary-foreground",
                  current && "border-primary"
                )}
              >
                {done ? <Check className="size-3.5" /> : index + 1}
              </span>
              {item.label}
            </li>
          )
        })}
      </ol>

      {job.node && job.status === "running" && (
        <p className="flex min-w-0 items-center gap-1.5 text-xs text-muted-foreground">
          <Server className="size-3.5 shrink-0" />
          <span className="min-w-0 truncate">
            训练节点 {job.node.name}
            {job.attempts > 1 && `（第 ${job.attempts} 次尝试）`}
          </span>
        </p>
      )}

      {noWorkers && (
        <Alert className="border-amber-500/40 [&>svg]:text-amber-600 dark:[&>svg]:text-amber-500">
          <TriangleAlert />
          <AlertTitle>没有可以执行转换的训练节点</AlertTitle>
          <AlertDescription>
            <p>
              ONNX 转换由训练节点执行（CPU
              节点即可）。需要至少一个在线、版本不低于 {MIN_TRAINER_VERSION}{" "}
              的训练节点，节点上线后会自动开始转换。
            </p>
            <Link
              to="/training/compute"
              className="text-foreground underline underline-offset-4"
            >
              查看计算资源
            </Link>
          </AlertDescription>
        </Alert>
      )}
    </div>
  )
}

function ExportSucceeded({ job }: { job: ModelExport }) {
  const result = job.result

  return (
    <div className="grid min-w-0 gap-4">
      <div className="flex items-start gap-3 rounded-lg border bg-muted/30 p-3">
        <CircleCheck className="mt-0.5 size-5 shrink-0 text-emerald-600 dark:text-emerald-500" />
        <div className="grid min-w-0 flex-1 gap-1">
          <p className="text-sm font-medium">已开始下载</p>
          <p className="text-xs break-all text-muted-foreground">
            {job.file_name}
            {job.size_bytes !== null && `（${formatBytes(job.size_bytes)}）`}
          </p>
          {job.download_url && (
            <Button
              type="button"
              variant="link"
              size="xs"
              className="h-auto justify-self-start p-0"
              onClick={() =>
                job.download_url &&
                startDownload(job.download_url, job.file_name)
              }
            >
              下载没有开始？重新下载
            </Button>
          )}
        </div>
      </div>
      {result && (
        <dl className="grid gap-x-6 gap-y-2 text-sm sm:grid-cols-2">
          <Fact label="opset">{result.opset ?? "—"}</Fact>
          <Fact label="试运行">
            {result.verified === null
              ? "—"
              : result.verified
                ? "已通过（onnxruntime）"
                : "未进行"}
          </Fact>
          {result.inputs.map((tensor) => (
            <Fact key={`in-${tensor.name}`} label={`输入 ${tensor.name}`}>
              <span className="font-mono text-xs">
                {shapeText(tensor.shape)}
              </span>
            </Fact>
          ))}
          {result.outputs.map((tensor) => (
            <Fact key={`out-${tensor.name}`} label={`输出 ${tensor.name}`}>
              <span className="font-mono text-xs">
                {shapeText(tensor.shape)}
              </span>
            </Fact>
          ))}
        </dl>
      )}
    </div>
  )
}

function ExportFailed({ job }: { job: ModelExport }) {
  const [showDetail, setShowDetail] = useState(false)

  return (
    <div className="grid min-w-0 gap-3">
      <Alert variant="destructive">
        <CircleAlert />
        <AlertTitle>转换没有完成</AlertTitle>
        <AlertDescription>
          <p className="break-words">{job.error || "训练节点没有说明原因"}</p>
        </AlertDescription>
      </Alert>
      {job.error_detail && (
        <div className="grid min-w-0 gap-1.5">
          <div className="flex items-center justify-between gap-2">
            <Button
              type="button"
              variant="ghost"
              size="xs"
              onClick={() => setShowDetail((value) => !value)}
              aria-expanded={showDetail}
            >
              {showDetail ? "收起错误详情" : "查看错误详情"}
            </Button>
            {showDetail && (
              <CopyButton text={job.error_detail} label="复制错误详情" />
            )}
          </div>
          {showDetail && (
            <pre className="max-h-60 overflow-auto rounded-md border bg-muted/50 p-3 font-mono text-xs leading-relaxed break-all whitespace-pre-wrap">
              {job.error_detail}
            </pre>
          )}
        </div>
      )}
      {job.node && (
        <p className="text-xs text-muted-foreground">
          训练节点：{job.node.name}
          {job.attempts > 1 && ` · 共尝试 ${job.attempts} 次`}
        </p>
      )}
    </div>
  )
}

function Fact({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="grid min-w-0 grid-cols-[7rem_1fr] items-baseline gap-3">
      <dt className="truncate text-muted-foreground" title={label}>
        {label}
      </dt>
      <dd className="min-w-0 break-words">{children}</dd>
    </div>
  )
}
