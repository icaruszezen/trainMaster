/**
 * 模型详情（`/models/:modelId`）：元信息、从 .pt 中解析出的模型与训练信息、
 * 类别列表与 ONNX 转换记录。
 */
import { useEffect, useMemo, useRef, useState, type ReactNode } from "react"
import { useMutation, useQuery } from "@tanstack/react-query"
import {
  ArrowLeft,
  Boxes,
  CircleCheck,
  Clock,
  Cpu,
  Download,
  FileBox,
  FileDown,
  FileWarning,
  FolderKanban,
  HardDrive,
  Layers,
  ListTree,
  Pencil,
  Rocket,
  Ruler,
  Search,
  Tags,
  Trash2,
  TriangleAlert,
  X,
} from "lucide-react"
import { Link, useNavigate, useParams } from "react-router"
import { toast } from "sonner"

import { PageHeader } from "@/components/page-header"
import { StatCard } from "@/components/stat-card"
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
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
  Card,
  CardAction,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"
import {
  Empty,
  EmptyContent,
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from "@/components/ui/empty"
import {
  InputGroup,
  InputGroupAddon,
  InputGroupButton,
  InputGroupInput,
} from "@/components/ui/input-group"
import { Skeleton } from "@/components/ui/skeleton"
import { Spinner } from "@/components/ui/spinner"
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import { LoadError } from "@/features/datasets/load-error"
import {
  deleteModelExport,
  isActiveExport,
  modelExportsQuery,
  modelKeys,
  modelQuery,
  refreshModelExports,
  type Model,
  type ModelExport,
  type VersionSource,
} from "@/features/models/api"
import {
  ModelActionsMenu,
  useModelActions,
  type ModelActions,
} from "@/features/models/model-actions"
import {
  allMetrics,
  epochText,
  exportStatusText,
  formatMetric,
  formatParameters,
  modelTitle,
  sourceLabels,
  summaryMetrics,
  trainedAt,
} from "@/features/models/model-info"
import {
  exportOptionsText,
  modelArchLabel,
  modelFileName,
  taskLabel,
  yoloVersionLabel,
} from "@/features/models/yolo-versions"
import {
  ModelReviewsLink,
  ReviewWithModelButton,
} from "@/features/reviews/model-review-entry"
import { RECYCLE_BIN_PATH } from "@/features/recycle-bin/recycle-format"
import { CopyButton } from "@/features/training/command-block"
import {
  newTrainingJobPath,
  trainingJobDetailPath,
} from "@/features/training/paths"
import { useMediaQuery } from "@/hooks/use-media-query"
import { usePageTitle } from "@/hooks/use-page-title"
import { ApiError } from "@/lib/api"
import { startDownload } from "@/lib/download"
import {
  formatBytes,
  formatDateTime,
  formatDuration,
  formatNumber,
  formatRelativeTime,
} from "@/lib/format"
import { queryClient } from "@/lib/query-client"
import { cn } from "@/lib/utils"

/** 路径参数必须是正整数（与后端一致），否则视为不存在。 */
function parseModelId(value: string | undefined) {
  if (!value || !/^[1-9]\d{0,15}$/.test(value)) {
    return null
  }
  const id = Number(value)
  return Number.isSafeInteger(id) ? id : null
}

export function ModelDetailPage() {
  const { modelId } = useParams()
  const id = parseModelId(modelId)

  if (id === null) {
    return <ModelNotFound />
  }
  // 切换模型时整体重置页面状态（对话框等）。
  return <ModelDetail key={id} id={id} />
}

function ModelNotFound() {
  usePageTitle("模型不存在")

  return (
    <Empty className="min-h-[60svh]">
      <EmptyHeader>
        <EmptyMedia variant="icon">
          <FileWarning />
        </EmptyMedia>
        <EmptyTitle>模型不存在</EmptyTitle>
        <EmptyDescription>
          该模型可能已被删除，或者链接有误。删除的模型在回收站中保留 7
          天，期间可以恢复。
        </EmptyDescription>
      </EmptyHeader>
      <EmptyContent>
        <div className="flex flex-wrap justify-center gap-2">
          <Button asChild>
            <Link to="/models">
              <ArrowLeft />
              返回模型仓库
            </Link>
          </Button>
          <Button variant="outline" asChild>
            <Link to={RECYCLE_BIN_PATH}>
              <Trash2 />
              查看回收站
            </Link>
          </Button>
        </div>
      </EmptyContent>
    </Empty>
  )
}

function ModelDetail({ id }: { id: number }) {
  const navigate = useNavigate()
  const query = useQuery(modelQuery(id))
  const { actions, dialogs } = useModelActions({
    onDeleted: () => navigate("/models", { replace: true }),
  })

  usePageTitle(query.data ? modelTitle(query.data) : null)

  if (query.isPending) {
    return <DetailSkeleton />
  }
  if (query.error instanceof ApiError && query.error.status === 404) {
    return <ModelNotFound />
  }
  // 后台刷新失败时继续显示已加载的数据，不卸载打开着的对话框。
  if (query.data === undefined) {
    return (
      <LoadError
        title="无法获取模型"
        error={query.error}
        retrying={query.isFetching}
        onRetry={() => query.refetch()}
      />
    )
  }

  const model = query.data

  return (
    <>
      <div className="grid grid-cols-1 gap-3">
        <PageHeader
          title={modelTitle(model)}
          description={model.description || undefined}
          actions={
            <>
              {!model.file_missing && (
                <Button variant="outline" asChild>
                  <Link
                    to={newTrainingJobPath({
                      projectId: model.project.id,
                      baseModelId: model.id,
                      initMode: model.info.training_checkpoint
                        ? "resume"
                        : "finetune",
                    })}
                  >
                    <Rocket />
                    {model.info.training_checkpoint
                      ? "断点续训 / 微调"
                      : "微调训练"}
                  </Link>
                </Button>
              )}
              <ReviewWithModelButton model={model} />
              <Button
                variant="outline"
                onClick={() => actions.download(model, "pt")}
                disabled={model.file_missing}
              >
                <Download />
                下载 PT
              </Button>
              <Button
                onClick={() => actions.download(model, "onnx")}
                disabled={model.file_missing}
              >
                <FileDown />
                下载 ONNX
              </Button>
              <ModelActionsMenu
                model={model}
                actions={actions}
                showDetails={false}
                className="size-9"
              />
            </>
          }
        />
        <ModelMeta model={model} onEdit={() => actions.edit(model)} />
      </div>

      <ModelAlerts model={model} />
      <ModelStats model={model} />

      <div className="grid gap-6 lg:grid-cols-2">
        <TrainingInfo model={model} />
        <FileInfo model={model} />
      </div>

      <ClassList names={model.info.class_names} />
      <ModelExports model={model} actions={actions} />

      {dialogs}
    </>
  )
}

function ModelMeta({ model, onEdit }: { model: Model; onEdit: () => void }) {
  return (
    <div className="flex flex-wrap items-center gap-x-4 gap-y-2 text-xs text-muted-foreground">
      <Badge variant="outline" className="font-normal">
        {sourceLabels[model.source] ?? model.source}
      </Badge>
      <Link
        to={`/projects/${model.project.id}?tab=models`}
        className="inline-flex min-w-0 items-center gap-1 underline-offset-4 hover:text-foreground hover:underline"
      >
        <FolderKanban className="size-3.5 shrink-0" />
        <span className="max-w-60 truncate">{model.project.name}</span>
      </Link>
      <span
        className="inline-flex min-w-0 items-center gap-1"
        title="上传时的文件名"
      >
        <FileBox className="size-3.5 shrink-0" />
        <span className="max-w-60 truncate">{model.file_name}</span>
      </span>
      <span title={formatDateTime(model.created_at)}>
        {model.source === "training" ? "保存于" : "上传于"}{" "}
        {formatRelativeTime(model.created_at)}
      </span>
      <ModelReviewsLink modelId={model.id} />
      <Button
        variant="ghost"
        size="xs"
        className="-ml-2 text-muted-foreground"
        onClick={onEdit}
      >
        <Pencil />
        {model.description ? "编辑说明" : "添加说明"}
      </Button>
    </div>
  )
}

/** 文件缺失、所选版本与文件中识别出的不一致时的提示。 */
function ModelAlerts({ model }: { model: Model }) {
  const detected = model.info.detected_yolo_version
  const mismatch = detected !== null && detected !== model.yolo_version
  if (!model.file_missing && !mismatch) {
    return null
  }
  return (
    <div className="grid gap-3">
      {model.file_missing && (
        <Alert variant="destructive">
          <FileWarning />
          <AlertTitle>模型文件缺失</AlertTitle>
          <AlertDescription>
            服务器上找不到这个模型的文件（可能被手动删除或移动），无法下载或转换。可以删除这条记录后重新上传。
          </AlertDescription>
        </Alert>
      )}
      {mismatch && (
        <Alert className="border-amber-500/40 [&>svg]:text-amber-600 dark:[&>svg]:text-amber-500">
          <TriangleAlert />
          <AlertTitle>YOLO 版本与文件不一致</AlertTitle>
          <AlertDescription>
            上传时选择的是 {yoloVersionLabel(model.yolo_version)}
            ，文件中识别出的是 {yoloVersionLabel(detected)}。转换 ONNX
            时按所选版本确定可用的后处理方式；如果选错了，可以删除后重新上传。
          </AlertDescription>
        </Alert>
      )}
    </div>
  )
}

function ModelStats({ model }: { model: Model }) {
  const { info } = model
  const classes = info.class_names.length
  return (
    <div className="grid grid-cols-2 gap-4 md:grid-cols-3 xl:grid-cols-6">
      <StatCard
        title="YOLO 版本"
        value={yoloVersionLabel(model.yolo_version)}
        icon={Layers}
      >
        <span className="truncate">
          {modelArchLabel(model)}
          {info.end2end && " · 端到端"}
        </span>
      </StatCard>
      <StatCard title="任务" value={taskLabel(model.task)} icon={Boxes}>
        <span className="truncate" title={info.head || undefined}>
          {info.head ? `输出层 ${info.head}` : "输出层未知"}
        </span>
      </StatCard>
      <StatCard
        title="输入尺寸"
        value={info.image_size !== null ? info.image_size : "—"}
        icon={Ruler}
      >
        <span>
          {info.image_size !== null
            ? "训练尺寸，ONNX 默认"
            : "未记录，ONNX 默认 640"}
        </span>
      </StatCard>
      <StatCard
        title="参数量"
        value={formatParameters(info.parameters)}
        icon={Cpu}
      >
        <span>输入 {info.channels > 0 ? info.channels : 3} 通道</span>
      </StatCard>
      <StatCard
        title="类别数"
        value={classes > 0 ? formatNumber(classes) : "—"}
        icon={Tags}
      >
        <span className="truncate" title={info.class_names.join("、")}>
          {classes > 0 ? classPreview(info.class_names) : "文件中没有类别名称"}
        </span>
      </StatCard>
      <StatCard
        title="文件大小"
        value={formatBytes(model.size_bytes)}
        icon={HardDrive}
      >
        <span className="tabular-nums">
          {model.export_count > 0
            ? `${formatNumber(model.export_count)} 个 ONNX 转换结果`
            : "还没有转换过 ONNX"}
        </span>
      </StatCard>
    </div>
  )
}

function classPreview(names: string[]) {
  const shown = names.slice(0, 3).join("、")
  return names.length > 3 ? `${shown} 等` : shown
}

function Section({ title, children }: { title?: string; children: ReactNode }) {
  return (
    <section className="grid min-w-0 gap-2">
      {title && <h3 className="text-sm font-semibold">{title}</h3>}
      <dl className="grid gap-y-2 text-sm">{children}</dl>
    </section>
  )
}

function Fact({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="grid min-w-0 grid-cols-[6.5rem_1fr] items-baseline gap-3 sm:grid-cols-[8rem_1fr]">
      <dt className="text-muted-foreground">{label}</dt>
      <dd className="min-w-0 break-words">{children}</dd>
    </div>
  )
}

const unknown = <span className="text-muted-foreground">—</span>

function TrainingInfo({ model }: { model: Model }) {
  const { info } = model
  const summary = summaryMetrics(info.metrics)
  const metrics = allMetrics(info.metrics)
  const [showAll, setShowAll] = useState(false)
  const time = trainedAt(info)
  const empty =
    !info.ultralytics_version &&
    !time &&
    info.epochs === null &&
    info.epoch === null &&
    metrics.length === 0 &&
    !info.base_model &&
    !info.train_data

  return (
    <Card className="min-w-0">
      <CardHeader>
        <CardTitle>训练信息</CardTitle>
        <CardDescription>保存在模型文件中的训练记录。</CardDescription>
      </CardHeader>
      <CardContent className="grid gap-5">
        {empty ? (
          <p className="text-sm text-muted-foreground">
            文件中没有训练记录（可能是导出或裁剪过的权重）。
          </p>
        ) : (
          <>
            {summary.length > 0 && (
              <div
                className={cn(
                  "grid gap-px overflow-hidden rounded-lg border bg-border",
                  summary.length > 2
                    ? "grid-cols-2 sm:grid-cols-4"
                    : "grid-cols-2"
                )}
              >
                {summary.map((metric) => (
                  <div
                    key={metric.key}
                    className="grid gap-0.5 bg-card px-3 py-2.5"
                  >
                    <span className="text-xs text-muted-foreground">
                      {metric.label}
                    </span>
                    <span className="text-lg font-semibold tabular-nums">
                      {formatMetric(metric.value)}
                    </span>
                  </div>
                ))}
              </div>
            )}
            <Section>
              <Fact label="Ultralytics">
                {info.ultralytics_version || unknown}
              </Fact>
              <Fact label="训练时间">
                {time instanceof Date
                  ? formatDateTime(time)
                  : (time ?? unknown)}
              </Fact>
              <Fact label="轮数">{epochText(info)}</Fact>
              {info.best_fitness !== null && (
                <Fact label="最佳得分">
                  <span className="tabular-nums">
                    {formatMetric(info.best_fitness)}
                  </span>
                </Fact>
              )}
              <Fact label="基础模型">
                {info.base_model ? (
                  <span className="font-mono text-xs">{info.base_model}</span>
                ) : (
                  unknown
                )}
              </Fact>
              <Fact label="训练数据">
                {info.train_data ? (
                  <span className="font-mono text-xs break-all">
                    {info.train_data}
                  </span>
                ) : (
                  unknown
                )}
              </Fact>
              {model.source === "training" &&
                model.training_job_id !== null && (
                  <Fact label="训练任务">
                    <Link
                      to={trainingJobDetailPath(model.training_job_id)}
                      className="font-medium underline-offset-4 hover:underline"
                    >
                      训练任务 #{model.training_job_id}
                    </Link>
                  </Fact>
                )}
            </Section>
            {info.training_checkpoint && (
              <p className="flex items-start gap-1.5 text-xs text-muted-foreground">
                <Clock className="mt-px size-3.5 shrink-0" />
                文件中含有优化器状态（last.pt
                这类可以继续训练的检查点），比只含权重的文件大。
              </p>
            )}
            {metrics.length > summary.length && (
              <div className="grid gap-2">
                <Button
                  variant="ghost"
                  size="sm"
                  className="justify-self-start"
                  onClick={() => setShowAll((value) => !value)}
                  aria-expanded={showAll}
                >
                  <ListTree />
                  {showAll ? "收起全部指标" : `全部指标（${metrics.length}）`}
                </Button>
                {showAll && (
                  <dl className="grid gap-x-6 gap-y-1.5 rounded-lg border p-3 text-sm sm:grid-cols-2">
                    {metrics.map((metric) => (
                      <div
                        key={metric.key}
                        className="flex min-w-0 items-baseline justify-between gap-3"
                      >
                        <dt
                          className="truncate text-muted-foreground"
                          title={metric.key}
                        >
                          {metric.label}
                        </dt>
                        <dd className="tabular-nums">
                          {formatMetric(metric.value)}
                        </dd>
                      </div>
                    ))}
                  </dl>
                )}
              </div>
            )}
          </>
        )}
      </CardContent>
    </Card>
  )
}

const versionSourceLabels: Record<VersionSource, string> = {
  yaml_file: "模型配置文件名",
  architecture: "网络结构",
  "": "无法识别",
}

function FileInfo({ model }: { model: Model }) {
  const { info } = model
  return (
    <Card className="min-w-0">
      <CardHeader>
        <CardTitle>文件信息</CardTitle>
        <CardDescription>上传时校验并解析的模型文件。</CardDescription>
      </CardHeader>
      <CardContent className="grid gap-5">
        <Section>
          <Fact label="原始文件名">
            <span className="break-all">{model.file_name}</span>
          </Fact>
          <Fact label="下载文件名">
            <span className="font-mono text-xs break-all">
              {modelFileName(model.project.name, model.version)}
            </span>
          </Fact>
          <Fact label="大小">
            <span className="tabular-nums">
              {formatBytes(model.size_bytes)}（{formatNumber(model.size_bytes)}{" "}
              字节）
            </span>
          </Fact>
          <Fact label="SHA256">
            <span className="flex min-w-0 items-start gap-1">
              <span className="min-w-0 font-mono text-xs leading-5 break-all">
                {model.sha256}
              </span>
              <CopyButton
                text={model.sha256}
                label="复制 SHA256"
                className="-my-0.5 shrink-0"
              />
            </span>
          </Fact>
        </Section>
        <Section title="网络结构">
          <Fact label="版本识别">
            {info.detected_yolo_version
              ? `${yoloVersionLabel(info.detected_yolo_version)}（依据${versionSourceLabels[info.version_source] ?? info.version_source}）`
              : `无法识别，以所选的 ${yoloVersionLabel(model.yolo_version)} 为准`}
          </Fact>
          <Fact label="模型配置">
            {info.yaml_file ? (
              <span className="font-mono text-xs">{info.yaml_file}</span>
            ) : (
              unknown
            )}
          </Fact>
          <Fact label="模型类">
            {info.model_class ? (
              <span className="font-mono text-xs">{info.model_class}</span>
            ) : (
              unknown
            )}
          </Fact>
          <Fact label="输出层">
            {info.head ? (
              <span className="font-mono text-xs">{info.head}</span>
            ) : (
              unknown
            )}
          </Fact>
          <Fact label="端到端">
            {info.end2end ? "是（NMS-free 检测头）" : "否"}
          </Fact>
        </Section>
      </CardContent>
    </Card>
  )
}

/** 折叠时显示的类别数。 */
const COLLAPSED_CLASSES = 24

function ClassList({ names }: { names: string[] }) {
  const [search, setSearch] = useState("")
  const [expanded, setExpanded] = useState(false)
  const q = search.trim().toLowerCase()

  const matched = useMemo(
    () =>
      names
        .map((name, index) => ({ name, index }))
        .filter(
          ({ name, index }) =>
            q === "" || name.toLowerCase().includes(q) || String(index) === q
        ),
    [names, q]
  )
  const collapsible = q === "" && matched.length > COLLAPSED_CLASSES
  const shown =
    collapsible && !expanded ? matched.slice(0, COLLAPSED_CLASSES) : matched

  return (
    <Card className="min-w-0">
      <CardHeader>
        <CardTitle className="flex items-center gap-2">
          类别
          <span className="text-sm font-normal text-muted-foreground tabular-nums">
            {formatNumber(names.length)}
          </span>
        </CardTitle>
        <CardDescription>编号即模型输出中的类别编号。</CardDescription>
      </CardHeader>
      <CardContent className="grid gap-3">
        {names.length > 12 && (
          <InputGroup className="sm:w-64">
            <InputGroupAddon>
              <Search />
            </InputGroupAddon>
            <InputGroupInput
              value={search}
              onChange={(event) => setSearch(event.target.value)}
              placeholder="搜索类别或编号"
              aria-label="搜索类别"
            />
            {search && (
              <InputGroupAddon align="inline-end">
                <InputGroupButton
                  size="icon-xs"
                  onClick={() => setSearch("")}
                  aria-label="清除搜索"
                >
                  <X />
                </InputGroupButton>
              </InputGroupAddon>
            )}
          </InputGroup>
        )}
        {names.length === 0 ? (
          <p className="text-sm text-muted-foreground">
            模型文件中没有类别名称。
          </p>
        ) : matched.length === 0 ? (
          <p className="text-sm text-muted-foreground">
            没有名称或编号匹配“{search.trim()}”的类别。
          </p>
        ) : (
          <ol className="grid grid-cols-[repeat(auto-fill,minmax(8.5rem,1fr))] gap-2">
            {shown.map(({ name, index }) => (
              <li
                key={index}
                className="flex min-w-0 items-center gap-2 rounded-md border px-2.5 py-1.5 text-sm"
              >
                <span className="shrink-0 font-mono text-xs text-muted-foreground tabular-nums">
                  {index}
                </span>
                <span className="truncate" title={name}>
                  {name || (
                    <span className="text-muted-foreground">（空）</span>
                  )}
                </span>
              </li>
            ))}
          </ol>
        )}
        {collapsible && (
          <Button
            variant="ghost"
            size="sm"
            className="justify-self-start"
            onClick={() => setExpanded((value) => !value)}
            aria-expanded={expanded}
          >
            {expanded
              ? "收起"
              : `显示全部 ${formatNumber(matched.length)} 个类别`}
          </Button>
        )}
      </CardContent>
    </Card>
  )
}

function ModelExports({
  model,
  actions,
}: {
  model: Model
  actions: ModelActions
}) {
  const query = useQuery(modelExportsQuery(model.id))
  const wide = useMediaQuery("(min-width: 48rem)")
  const [deleting, setDeleting] = useState<ModelExport | null>(null)

  // 转换在对话框之外完成（如关闭对话框后）时，刷新模型上的转换数。
  const succeeded = query.data?.items.filter(
    (job) => job.status === "succeeded"
  ).length
  const seen = useRef<number | undefined>(undefined)
  useEffect(() => {
    if (succeeded === undefined) {
      return
    }
    if (seen.current !== undefined && seen.current !== succeeded) {
      void queryClient.invalidateQueries({
        queryKey: modelKeys.detail(model.id),
        exact: true,
      })
      void queryClient.invalidateQueries({ queryKey: modelKeys.lists() })
      void queryClient.invalidateQueries({
        queryKey: [...modelKeys.all, "project"],
      })
    }
    seen.current = succeeded
  }, [succeeded, model.id])

  const jobs = query.data?.items ?? []

  return (
    <Card className="min-w-0">
      <CardHeader>
        <CardTitle className="flex items-center gap-2">
          ONNX 转换记录
          {jobs.length > 0 && (
            <span className="text-sm font-normal text-muted-foreground tabular-nums">
              {formatNumber(jobs.length)}
            </span>
          )}
        </CardTitle>
        <CardDescription>
          由在线的训练节点转换；同样的选项只转换一次，之后直接下载缓存的结果。
        </CardDescription>
        <CardAction>
          <Button
            variant="outline"
            size="sm"
            onClick={() => actions.download(model, "onnx")}
            disabled={model.file_missing}
          >
            <FileDown />
            <span className="hidden sm:inline">转换并下载</span>
            <span className="sm:hidden">转换</span>
          </Button>
        </CardAction>
      </CardHeader>
      <CardContent>
        {query.isPending ? (
          <div className="grid gap-2">
            <Skeleton className="h-12 w-full" />
            <Skeleton className="h-12 w-full" />
          </div>
        ) : query.data === undefined ? (
          <LoadError
            title="无法获取转换记录"
            error={query.error}
            retrying={query.isFetching}
            onRetry={() => query.refetch()}
          />
        ) : jobs.length === 0 ? (
          <p className="rounded-lg border border-dashed px-4 py-6 text-center text-sm text-muted-foreground">
            还没有转换过 ONNX。点击“下载
            ONNX”选择输入尺寸与后处理方式后自动转换。
          </p>
        ) : wide ? (
          <div className="overflow-hidden rounded-lg border">
            <Table>
              <TableHeader>
                <TableRow className="hover:bg-transparent">
                  <TableHead className="pl-4">选项</TableHead>
                  <TableHead>状态</TableHead>
                  <TableHead className="text-right">大小</TableHead>
                  <TableHead className="hidden lg:table-cell">节点</TableHead>
                  <TableHead className="hidden lg:table-cell">时间</TableHead>
                  <TableHead className="pr-4">
                    <span className="sr-only">操作</span>
                  </TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {jobs.map((job) => (
                  <TableRow key={job.id}>
                    <TableCell className="w-full max-w-0 pl-4">
                      <div className="grid min-w-0 gap-0.5">
                        <span className="truncate font-medium">
                          {exportOptionsText(job.options)}
                        </span>
                        <span
                          className="truncate font-mono text-xs text-muted-foreground"
                          title={job.file_name}
                        >
                          {job.file_name}
                        </span>
                      </div>
                    </TableCell>
                    <TableCell>
                      <ExportStatus job={job} />
                    </TableCell>
                    <TableCell className="text-right whitespace-nowrap tabular-nums">
                      {job.size_bytes !== null
                        ? formatBytes(job.size_bytes)
                        : "—"}
                    </TableCell>
                    <TableCell className="hidden max-w-40 truncate text-muted-foreground lg:table-cell">
                      {job.node?.name ?? "—"}
                    </TableCell>
                    <TableCell className="hidden whitespace-nowrap text-muted-foreground lg:table-cell">
                      <ExportTime job={job} />
                    </TableCell>
                    <TableCell className="pr-4">
                      <ExportRowActions
                        job={job}
                        onDownload={() => downloadExport(job)}
                        onTrack={() => actions.track(model, job)}
                        onDelete={() => setDeleting(job)}
                      />
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </div>
        ) : (
          <ul className="divide-y rounded-lg border">
            {jobs.map((job) => (
              <li
                key={job.id}
                className="grid grid-cols-[minmax(0,1fr)] gap-2 p-3"
              >
                <div className="grid min-w-0 gap-0.5">
                  <span className="text-sm font-medium break-words">
                    {exportOptionsText(job.options)}
                  </span>
                  <span className="truncate font-mono text-xs text-muted-foreground">
                    {job.file_name}
                  </span>
                </div>
                <div className="flex items-start justify-between gap-2">
                  <ExportStatus job={job} />
                  <ExportRowActions
                    job={job}
                    onDownload={() => downloadExport(job)}
                    onTrack={() => actions.track(model, job)}
                    onDelete={() => setDeleting(job)}
                  />
                </div>
                <p className="truncate text-xs text-muted-foreground tabular-nums">
                  {[
                    job.size_bytes !== null
                      ? formatBytes(job.size_bytes)
                      : null,
                    job.node?.name,
                  ]
                    .filter(Boolean)
                    .join(" · ")}
                  {(job.size_bytes !== null || job.node) && " · "}
                  <ExportTime job={job} />
                </p>
              </li>
            ))}
          </ul>
        )}
      </CardContent>

      <DeleteExportDialog
        model={model}
        job={deleting}
        onOpenChange={(open) => !open && setDeleting(null)}
      />
    </Card>
  )
}

function downloadExport(job: ModelExport) {
  if (!job.download_url) {
    return
  }
  startDownload(job.download_url, job.file_name)
  toast.success("已开始下载", {
    description:
      job.size_bytes !== null
        ? `${job.file_name}（${formatBytes(job.size_bytes)}）`
        : job.file_name,
  })
}

function ExportStatus({ job }: { job: ModelExport }) {
  const text = exportStatusText(job)
  switch (job.status) {
    case "succeeded":
      return (
        <Badge variant="outline" className="shrink-0 font-normal">
          <CircleCheck className="text-emerald-600 dark:text-emerald-500" />
          {text}
        </Badge>
      )
    case "failed":
      return (
        <Badge
          variant="destructive"
          className="max-w-48 shrink-0 font-normal"
          title={job.error || undefined}
        >
          <TriangleAlert />
          {text}
        </Badge>
      )
    default:
      return (
        <span className="grid shrink-0 justify-items-start gap-0.5">
          <Badge variant="secondary" className="font-normal">
            {job.status === "running" ? <Spinner /> : <Clock />}
            {text}
          </Badge>
          {job.status === "queued" && job.workers_online === 0 && (
            <span className="text-xs text-amber-700 dark:text-amber-500">
              没有在线的训练节点
            </span>
          )}
        </span>
      )
  }
}

function ExportTime({ job }: { job: ModelExport }) {
  const finished = job.finished_at ?? null
  const time = finished ?? job.created_at
  const duration =
    job.status === "succeeded" && job.result?.duration_ms
      ? formatDuration(job.result.duration_ms / 1000)
      : null
  return (
    <span title={formatDateTime(time)}>
      {formatRelativeTime(time)}
      {duration && `（用时 ${duration}）`}
    </span>
  )
}

function ExportRowActions({
  job,
  onDownload,
  onTrack,
  onDelete,
}: {
  job: ModelExport
  onDownload: () => void
  onTrack: () => void
  onDelete: () => void
}) {
  const active = isActiveExport(job)
  return (
    <div className="flex shrink-0 items-center justify-end gap-1">
      {job.status === "succeeded" && job.download_url ? (
        <Button variant="outline" size="sm" onClick={onDownload}>
          <Download />
          下载
        </Button>
      ) : (
        <Button variant="ghost" size="sm" onClick={onTrack}>
          {active ? "查看进度" : "查看原因"}
        </Button>
      )}
      <Button
        variant="ghost"
        size="icon-sm"
        onClick={onDelete}
        aria-label={active ? "取消转换" : "删除转换结果"}
        title={active ? "取消转换" : "删除"}
      >
        {active ? <X /> : <Trash2 />}
      </Button>
    </div>
  )
}

function DeleteExportDialog({
  model,
  job,
  onOpenChange,
}: {
  model: Model
  job: ModelExport | null
  onOpenChange: (open: boolean) => void
}) {
  return (
    <AlertDialog open={job !== null} onOpenChange={onOpenChange}>
      <AlertDialogContent>
        {job && (
          <DeleteExportForm
            model={model}
            job={job}
            onDone={() => onOpenChange(false)}
          />
        )}
      </AlertDialogContent>
    </AlertDialog>
  )
}

function DeleteExportForm({
  model,
  job,
  onDone,
}: {
  model: Model
  job: ModelExport
  onDone: () => void
}) {
  const active = isActiveExport(job)
  const mutation = useMutation({
    mutationFn: () => deleteModelExport(model.id, job.id),
    onSuccess: () => {
      queryClient.removeQueries({
        queryKey: modelKeys.export(model.id, job.id),
      })
      refreshModelExports(model.id)
      toast.success(active ? "已取消转换" : "已删除转换结果")
      onDone()
    },
  })

  return (
    <>
      <AlertDialogHeader>
        <AlertDialogTitle>
          {active ? "取消这次转换？" : "删除转换结果？"}
        </AlertDialogTitle>
        <AlertDialogDescription className="break-words">
          {active
            ? `${exportOptionsText(job.options)}：训练节点会放弃这次转换。`
            : job.status === "succeeded"
              ? `将删除 ${job.file_name}${job.size_bytes !== null ? `（${formatBytes(job.size_bytes)}）` : ""}；之后用同样的选项下载需要重新转换。`
              : `将删除这条失败的转换记录（${exportOptionsText(job.options)}）。`}
        </AlertDialogDescription>
      </AlertDialogHeader>
      {mutation.error && (
        <p role="alert" className="text-sm text-destructive">
          {mutation.error.message}
        </p>
      )}
      <AlertDialogFooter>
        <AlertDialogCancel disabled={mutation.isPending}>
          {active ? "继续转换" : "取消"}
        </AlertDialogCancel>
        <Button
          variant="destructive"
          onClick={() => mutation.mutate()}
          disabled={mutation.isPending}
        >
          {mutation.isPending && <Spinner aria-hidden />}
          {active ? "取消转换" : "删除"}
        </Button>
      </AlertDialogFooter>
    </>
  )
}

function DetailSkeleton() {
  return (
    <div className="grid gap-6" aria-busy="true" aria-label="正在加载">
      <div className="grid gap-3">
        <Skeleton className="h-8 w-64" />
        <Skeleton className="h-5 w-96 max-w-full" />
      </div>
      <div className="grid grid-cols-2 gap-4 md:grid-cols-3 xl:grid-cols-6">
        {[0, 1, 2, 3, 4, 5].map((index) => (
          <Skeleton key={index} className="h-[118px] rounded-xl" />
        ))}
      </div>
      <div className="grid gap-6 lg:grid-cols-2">
        <Skeleton className="h-72 rounded-xl" />
        <Skeleton className="h-72 rounded-xl" />
      </div>
    </div>
  )
}
