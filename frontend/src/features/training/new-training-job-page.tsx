/**
 * 新建训练任务页（`/training/jobs/new`）。
 *
 * 支持 URL 参数预填：
 * - `?project=<id>`：预选项目
 * - `?data_version=<id>`：预选数据版本
 * - `?base_model=<id>`：预选基础权重模型
 * - `?init_mode=scratch|finetune|resume`：预选训练模式
 * - `?from=<jobId>`：从已有训练任务完整克隆模型设置与全部 86 项 YOLO 参数
 */
import { useId, useMemo, useState, type FormEvent } from "react"
import { useMutation, useQuery } from "@tanstack/react-query"
import {
  AlertTriangle,
  ArrowLeft,
  Layers,
  Plus,
  Rocket,
  Sparkles,
} from "lucide-react"
import { Link, useNavigate, useSearchParams } from "react-router"
import { toast } from "sonner"

import { PageHeader } from "@/components/page-header"
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"
import { Field, FieldLabel } from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { Skeleton } from "@/components/ui/skeleton"
import { Spinner } from "@/components/ui/spinner"
import { Switch } from "@/components/ui/switch"
import { Textarea } from "@/components/ui/textarea"
import {
  dataVersionsQuery,
  type DataVersion,
} from "@/features/data-versions/api"
import {
  dataVersionTitle,
  splitSummary,
  versionSplitCounts,
} from "@/features/data-versions/format"
import { newDataVersionPath } from "@/features/data-versions/paths"
import { LoadError } from "@/features/datasets/load-error"
import {
  ALL_MODELS,
  modelsQuery,
  type Model,
  type YoloVersion,
} from "@/features/models/api"
import {
  modelArchLabel,
  taskLabel,
  yoloVersionLabel,
  yoloVersions,
} from "@/features/models/yolo-versions"
import { projectsQuery, type Project } from "@/features/projects/api"
import {
  cacheTrainingJob,
  createTrainingJob,
  trainingJobQuery,
  trainingParamsSchemaQuery,
  type InitMode,
  type ParamsSchemaResponse,
  type TrainingJob,
} from "@/features/training/api"
import {
  INIT_MODE_DESCRIPTIONS,
  INIT_MODE_LABELS,
} from "@/features/training/format"
import {
  parsePositiveId,
  TRAINING_JOBS_PATH,
  trainingJobDetailPath,
  trainingJobsPath,
} from "@/features/training/paths"
import {
  buildDefaultParams,
  countModifiedParams,
  validateTrainingParams,
} from "@/features/training/training-params"
import { TrainingParamsEditor } from "@/features/training/training-params-editor"
import { formatBytes, formatNumber } from "@/lib/format"
import { cn } from "@/lib/utils"

const INIT_MODES: readonly InitMode[] = ["scratch", "finetune", "resume"]

export function NewTrainingJobPage() {
  const [searchParams] = useSearchParams()

  const urlProjectId = parsePositiveId(searchParams.get("project"))
  const urlDataVersionId = parsePositiveId(searchParams.get("data_version"))
  const urlBaseModelId = parsePositiveId(searchParams.get("base_model"))
  const rawInitMode = searchParams.get("init_mode")
  const urlInitMode: InitMode | null = INIT_MODES.includes(
    rawInitMode as InitMode
  )
    ? (rawInitMode as InitMode)
    : urlBaseModelId !== null
      ? "finetune"
      : null
  const fromJobId = parsePositiveId(searchParams.get("from"))

  const schemaQuery = useQuery(trainingParamsSchemaQuery())
  const projects = useQuery(projectsQuery())
  const fromJobQuery = useQuery({
    ...trainingJobQuery(fromJobId ?? 0),
    enabled: fromJobId !== null,
  })
  const needBaseModelProject = urlBaseModelId !== null && urlProjectId === null
  const initialModels = useQuery({
    ...modelsQuery(ALL_MODELS),
    enabled: needBaseModelProject,
  })

  if (
    schemaQuery.isPending ||
    projects.isPending ||
    (fromJobId !== null && fromJobQuery.isPending) ||
    (needBaseModelProject && initialModels.isPending)
  ) {
    return (
      <div className="grid gap-4" aria-busy="true" aria-label="正在加载">
        <Skeleton className="h-12 w-64" />
        <Skeleton className="h-52 rounded-xl" />
        <Skeleton className="h-80 rounded-xl" />
      </div>
    )
  }

  if (schemaQuery.error || !schemaQuery.data) {
    return (
      <LoadError
        title="无法加载训练参数规格"
        error={schemaQuery.error}
        retrying={schemaQuery.isFetching}
        onRetry={() => schemaQuery.refetch()}
      />
    )
  }

  if (projects.error || !projects.data) {
    return (
      <LoadError
        title="无法加载项目列表"
        error={projects.error}
        retrying={projects.isFetching}
        onRetry={() => projects.refetch()}
      />
    )
  }

  const baseModelProjectId =
    urlBaseModelId !== null
      ? (initialModels.data?.items.find((m) => m.id === urlBaseModelId)?.project
          .id ?? null)
      : null

  const formKey = `${fromJobQuery.data?.id ?? "new"}:${urlProjectId ?? ""}:${urlDataVersionId ?? ""}:${urlBaseModelId ?? ""}:${urlInitMode ?? ""}`

  return (
    <NewTrainingJobForm
      key={formKey}
      schema={schemaQuery.data}
      projects={projects.data.items}
      fromJob={fromJobQuery.data ?? null}
      urlProjectId={urlProjectId}
      urlDataVersionId={urlDataVersionId}
      urlBaseModelId={urlBaseModelId}
      urlInitMode={urlInitMode}
      baseModelProjectId={baseModelProjectId}
    />
  )
}

function NewTrainingJobForm({
  schema,
  projects,
  fromJob,
  urlProjectId,
  urlDataVersionId,
  urlBaseModelId,
  urlInitMode,
  baseModelProjectId,
}: {
  schema: ParamsSchemaResponse
  projects: Project[]
  fromJob: TrainingJob | null
  urlProjectId: number | null
  urlDataVersionId: number | null
  urlBaseModelId: number | null
  urlInitMode: InitMode | null
  baseModelProjectId: number | null
}) {
  const navigate = useNavigate()

  const initialMode: InitMode = fromJob
    ? fromJob.init_mode
    : (urlInitMode ?? "scratch")

  const [selectedProjectId, setSelectedProjectId] = useState<number | null>(
    () =>
      fromJob?.project_id ??
      urlProjectId ??
      baseModelProjectId ??
      projects[0]?.id ??
      null
  )
  const projectId =
    selectedProjectId !== null &&
    projects.some((p) => p.id === selectedProjectId)
      ? selectedProjectId
      : (projects[0]?.id ?? null)

  const [selectedDataVersionId, setSelectedDataVersionId] = useState<
    number | null
  >(() =>
    fromJob
      ? fromJob.data_version_live
        ? fromJob.data_version_id
        : null
      : urlDataVersionId
  )
  const [initMode, setInitMode] = useState<InitMode>(initialMode)
  const [yoloVersion, setYoloVersion] = useState<YoloVersion>(
    () => fromJob?.yolo_version ?? "yolo11"
  )
  const [modelScale, setModelScale] = useState<string>(
    () => fromJob?.model_scale || "n"
  )
  const [selectedBaseModelId, setSelectedBaseModelId] = useState<number | null>(
    () => (fromJob ? fromJob.base_model_id : urlBaseModelId)
  )
  const [name, setName] = useState<string>(() =>
    fromJob?.name ? `${fromJob.name}-副本` : ""
  )
  const [description, setDescription] = useState<string>(
    () => fromJob?.description ?? ""
  )
  const [autoRegister, setAutoRegister] = useState<boolean>(
    () => fromJob?.auto_register ?? true
  )
  const [params, setParams] = useState<Record<string, unknown>>(() => {
    const merged = buildDefaultParams(schema.specs, initialMode)
    if (fromJob) {
      for (const [k, v] of Object.entries(fromJob.params)) {
        merged[k] = Array.isArray(v) ? [...v] : v
      }
    }
    return merged
  })
  const [formError, setFormError] = useState<string | null>(null)

  const dataVersions = useQuery({
    ...dataVersionsQuery(projectId),
    enabled: projectId !== null,
  })

  const readyVersions = useMemo(
    () =>
      (dataVersions.data?.items ?? []).filter(
        (v): v is DataVersion => v.status === "ready"
      ),
    [dataVersions.data?.items]
  )

  const dataVersionId =
    selectedDataVersionId !== null &&
    readyVersions.some((v) => v.id === selectedDataVersionId)
      ? selectedDataVersionId
      : (readyVersions[0]?.id ?? null)

  const models = useQuery({
    ...modelsQuery(ALL_MODELS),
    enabled: initMode !== "scratch",
  })

  const eligibleModels = useMemo(() => {
    const all = (models.data?.items ?? []).filter((m) => !m.file_missing)
    const filtered =
      initMode === "resume"
        ? all.filter((m) => m.info.training_checkpoint)
        : all
    // 当前项目的模型排在前面，便于优先选择
    return [...filtered].sort((a, b) => {
      const aOwn = a.project.id === projectId ? 0 : 1
      const bOwn = b.project.id === projectId ? 0 : 1
      if (aOwn !== bOwn) {
        return aOwn - bOwn
      }
      return b.id - a.id
    })
  }, [models.data?.items, initMode, projectId])

  const baseModelId =
    initMode === "scratch"
      ? null
      : selectedBaseModelId !== null &&
          eligibleModels.some((m) => m.id === selectedBaseModelId)
        ? selectedBaseModelId
        : (eligibleModels[0]?.id ?? null)

  const selectedVersion = readyVersions.find((v) => v.id === dataVersionId)
  const selectedBaseModel: Model | undefined = eligibleModels.find(
    (m) => m.id === baseModelId
  )
  const availableScales = useMemo(
    () => schema.scales[yoloVersion] ?? [],
    [schema.scales, yoloVersion]
  )

  const handleInitModeChange = (nextMode: InitMode) => {
    setInitMode(nextMode)
    setFormError(null)
    setParams((prev) => ({
      ...prev,
      resume: nextMode === "resume",
      pretrained: nextMode !== "scratch",
    }))
  }

  const handleYoloVersionChange = (nextVersion: YoloVersion) => {
    setYoloVersion(nextVersion)
    const scales = schema.scales[nextVersion] ?? []
    if (scales.length > 0 && !scales.some((s) => s.value === modelScale)) {
      setModelScale(scales[0].value)
    }
  }

  const createMutation = useMutation({
    mutationFn: createTrainingJob,
    onSuccess: (created) => {
      cacheTrainingJob(created)
      toast.success("训练任务已提交", {
        description:
          created.workers_online > 0
            ? "已进入训练队列，在线训练节点即将开始训练。"
            : "已进入训练队列；当前暂无在线训练节点，节点上线后会自动开始。",
      })
      navigate(trainingJobDetailPath(created.id))
    },
  })

  const nameId = useId()
  const descId = useId()
  const projectSelectId = useId()
  const versionSelectId = useId()
  const baseModelSelectId = useId()

  const modifiedCount = countModifiedParams(schema.specs, params)

  const submit = (event: FormEvent) => {
    event.preventDefault()
    setFormError(null)
    if (createMutation.isPending) {
      return
    }
    if (projectId === null) {
      setFormError("请先选择一个所属项目")
      return
    }
    if (dataVersionId === null) {
      setFormError("请选择一个已就绪的数据版本作为训练数据集")
      return
    }
    if (initMode !== "scratch" && baseModelId === null) {
      setFormError(
        initMode === "resume"
          ? "请选择一个包含优化器状态的训练检查点模型（如 last.pt）"
          : "请选择一个用于微调的基础模型"
      )
      return
    }
    const paramErr = validateTrainingParams(schema.specs, params)
    if (paramErr) {
      setFormError(`参数 ${paramErr.key}：${paramErr.message}`)
      return
    }

    createMutation.mutate({
      project_id: projectId,
      data_version_id: dataVersionId,
      name: name.trim(),
      description: description.trim(),
      init_mode: initMode,
      yolo_version:
        initMode === "scratch" ? yoloVersion : selectedBaseModel?.yolo_version,
      model_scale: initMode === "scratch" ? modelScale : undefined,
      base_model_id: initMode === "scratch" ? null : baseModelId,
      params,
      auto_register: autoRegister,
    })
  }

  const backPath =
    urlProjectId !== null ? trainingJobsPath(urlProjectId) : TRAINING_JOBS_PATH

  return (
    <form onSubmit={submit} className="grid min-w-0 gap-6" noValidate>
      <div className="grid gap-2">
        <Button variant="ghost" size="sm" className="-ml-2 w-fit" asChild>
          <Link to={backPath}>
            <ArrowLeft />
            返回训练任务
          </Link>
        </Button>

        <PageHeader
          title="新建训练任务"
          description="选择项目与冻结的数据版本，指定模型初始化方式，并按需编辑 Ultralytics YOLO 全量训练参数（默认已预填官方推荐参数）。"
        />
      </div>

      {fromJob && (
        <Alert>
          <Sparkles className="size-4" />
          <AlertTitle>已复用训练任务 #{fromJob.id} 的配置与全量参数</AlertTitle>
          <AlertDescription>
            包含模型设置以及 {modifiedCount} 项自定义 YOLO
            训练参数，提交前可继续修改任意字段。
          </AlertDescription>
        </Alert>
      )}

      {/* 1. 项目、数据版本与基本信息 */}
      <Card>
        <CardHeader>
          <CardTitle>1. 训练数据与基本信息</CardTitle>
          <CardDescription>
            训练任务使用冻结的数据版本，保证每一次训练的图像、标注与类别顺序完全可复现、可追溯。
          </CardDescription>
        </CardHeader>
        <CardContent className="grid gap-5">
          <div className="grid grid-cols-1 gap-4 md:grid-cols-2">
            <Field>
              <FieldLabel htmlFor={projectSelectId}>所属项目</FieldLabel>
              <Select
                value={projectId === null ? "" : String(projectId)}
                disabled={createMutation.isPending}
                onValueChange={(v) => {
                  setSelectedProjectId(Number(v))
                  setFormError(null)
                }}
              >
                <SelectTrigger id={projectSelectId} className="w-full">
                  <SelectValue placeholder="选择项目" />
                </SelectTrigger>
                <SelectContent>
                  {projects.map((p) => (
                    <SelectItem key={p.id} value={String(p.id)}>
                      {p.name}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </Field>

            <Field>
              <FieldLabel htmlFor={versionSelectId}>训练数据版本</FieldLabel>
              {dataVersions.isPending ? (
                <Skeleton className="h-9 w-full" />
              ) : readyVersions.length === 0 ? (
                <div className="flex flex-wrap items-center justify-between gap-2 rounded-md border border-dashed px-3 py-2 text-xs text-muted-foreground">
                  <span>该项目暂无已生成（ready）的数据版本</span>
                  {projectId !== null && (
                    <Button variant="outline" size="xs" asChild>
                      <Link to={newDataVersionPath({ projectId })}>
                        <Plus />
                        去生成数据版本
                      </Link>
                    </Button>
                  )}
                </div>
              ) : (
                <Select
                  value={dataVersionId === null ? "" : String(dataVersionId)}
                  disabled={createMutation.isPending}
                  onValueChange={(v) => {
                    setSelectedDataVersionId(Number(v))
                    setFormError(null)
                  }}
                >
                  <SelectTrigger id={versionSelectId} className="w-full">
                    <SelectValue placeholder="选择已就绪的数据版本" />
                  </SelectTrigger>
                  <SelectContent>
                    {readyVersions.map((ver) => (
                      <SelectItem key={ver.id} value={String(ver.id)}>
                        {dataVersionTitle(ver)} · 共{" "}
                        {formatNumber(ver.image_count)} 张（{ver.classes.length}{" "}
                        类）
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              )}
            </Field>
          </div>

          {selectedVersion && (
            <div className="grid gap-2 rounded-lg border bg-muted/30 p-3.5 text-xs">
              <div className="flex flex-wrap items-center gap-2">
                <Layers className="size-4 text-muted-foreground" />
                <span className="font-medium text-foreground">
                  {dataVersionTitle(selectedVersion)}
                </span>
                <span className="text-muted-foreground">·</span>
                <span className="text-muted-foreground tabular-nums">
                  {splitSummary(versionSplitCounts(selectedVersion))}
                </span>
                <span className="text-muted-foreground">·</span>
                <span className="text-muted-foreground tabular-nums">
                  {selectedVersion.classes.length} 个类别
                </span>
              </div>
              {selectedVersion.classes.length > 0 && (
                <div className="flex flex-wrap gap-1">
                  {selectedVersion.classes.slice(0, 16).map((cls, idx) => (
                    <Badge
                      key={idx}
                      variant="outline"
                      className="h-5 px-1.5 text-[11px] font-normal"
                    >
                      {idx}: {cls.name}
                    </Badge>
                  ))}
                  {selectedVersion.classes.length > 16 && (
                    <span className="self-center text-[11px] text-muted-foreground">
                      等共 {selectedVersion.classes.length} 类
                    </span>
                  )}
                </div>
              )}
              {selectedVersion.val_count === 0 && Boolean(params.val) && (
                <p className="flex items-center gap-1.5 text-amber-600 dark:text-amber-500">
                  <AlertTriangle className="size-3.5 shrink-0" />
                  该数据版本的验证集图片数为 0；建议在下方“验证与可视化设置”中将
                  split 改为 train，或关闭 val。
                </p>
              )}
            </div>
          )}

          <div className="grid grid-cols-1 gap-4 md:grid-cols-2">
            <Field>
              <FieldLabel htmlFor={nameId}>任务名称（可选）</FieldLabel>
              <Input
                id={nameId}
                value={name}
                maxLength={120}
                disabled={createMutation.isPending}
                placeholder="如：安全帽检测-YOLO11s-高分辨率实验"
                onChange={(event) => setName(event.target.value)}
              />
            </Field>

            <Field>
              <FieldLabel htmlFor={descId}>备注说明（可选）</FieldLabel>
              <Textarea
                id={descId}
                value={description}
                maxLength={1000}
                disabled={createMutation.isPending}
                placeholder="记录本次训练的调参目的、数据差异或实验备注…"
                className="min-h-9 py-2"
                rows={1}
                onChange={(event) => setDescription(event.target.value)}
              />
            </Field>
          </div>
        </CardContent>
      </Card>

      {/* 2. 模型与初始化方式 */}
      <Card>
        <CardHeader>
          <CardTitle>2. 模型架构与权重初始化</CardTitle>
          <CardDescription>
            支持内网全离线从头构建网络训练，或复用模型仓库中的已有权重进行迁移微调与断点续训。
          </CardDescription>
        </CardHeader>
        <CardContent className="grid gap-5">
          <div className="grid grid-cols-1 gap-3 md:grid-cols-3">
            {INIT_MODES.map((mode) => {
              const selected = initMode === mode
              return (
                <button
                  key={mode}
                  type="button"
                  disabled={createMutation.isPending}
                  onClick={() => handleInitModeChange(mode)}
                  className={cn(
                    "grid content-start gap-1.5 rounded-xl border p-3.5 text-left transition-colors",
                    selected
                      ? "border-primary bg-primary/[0.04] ring-1 ring-primary"
                      : "hover:bg-accent/50"
                  )}
                >
                  <div className="flex items-center justify-between gap-2">
                    <span className="text-sm font-medium">
                      {INIT_MODE_LABELS[mode]}
                    </span>
                    <Badge
                      variant={selected ? "default" : "outline"}
                      className="text-[11px] font-normal"
                    >
                      {mode}
                    </Badge>
                  </div>
                  <p className="text-xs leading-relaxed text-muted-foreground">
                    {INIT_MODE_DESCRIPTIONS[mode]}
                  </p>
                </button>
              )
            })}
          </div>

          {initMode === "scratch" ? (
            <div className="grid grid-cols-1 gap-4 md:grid-cols-2">
              <Field>
                <FieldLabel>YOLO 版本</FieldLabel>
                <Select
                  value={yoloVersion}
                  disabled={createMutation.isPending}
                  onValueChange={(v) =>
                    handleYoloVersionChange(v as YoloVersion)
                  }
                >
                  <SelectTrigger className="w-full">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {yoloVersions.map((v) => (
                      <SelectItem key={v.value} value={v.value}>
                        {v.label} — {v.description}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </Field>

              <Field>
                <FieldLabel>模型网络规模 (Scale)</FieldLabel>
                <Select
                  value={modelScale}
                  disabled={createMutation.isPending}
                  onValueChange={setModelScale}
                >
                  <SelectTrigger className="w-full">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {availableScales.map((sc) => (
                      <SelectItem key={sc.value} value={sc.value}>
                        {sc.label} — {sc.description}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </Field>
            </div>
          ) : (
            <Field>
              <FieldLabel htmlFor={baseModelSelectId}>
                {initMode === "resume"
                  ? "选择断点检查点模型（含优化器状态的 last.pt）"
                  : "选择预训练基础模型（.pt）"}
              </FieldLabel>
              {models.isPending ? (
                <Skeleton className="h-9 w-full" />
              ) : eligibleModels.length === 0 ? (
                <div className="rounded-md border border-dashed px-3.5 py-3 text-xs text-muted-foreground">
                  {initMode === "resume"
                    ? "模型仓库中暂无包含优化器状态的训练检查点（如已注册的 last.pt）。你可以改选“预训练微调”或“从头训练”，也可以先在模型仓库上传检查点。"
                    : "模型仓库中暂无可用的 .pt 模型。你可以先在“模型仓库”上传官方预训练权重（如 yolo11n.pt），或改选“从头训练”。"}
                </div>
              ) : (
                <Select
                  value={baseModelId === null ? "" : String(baseModelId)}
                  disabled={createMutation.isPending}
                  onValueChange={(v) => {
                    setSelectedBaseModelId(Number(v))
                    setFormError(null)
                  }}
                >
                  <SelectTrigger id={baseModelSelectId} className="w-full">
                    <SelectValue placeholder="选择模型" />
                  </SelectTrigger>
                  <SelectContent>
                    {eligibleModels.map((m) => (
                      <SelectItem key={m.id} value={String(m.id)}>
                        [{m.project.name}] 模型 v{m.version} ·{" "}
                        {modelArchLabel(m)}（{taskLabel(m.task)}，
                        {formatBytes(m.size_bytes)}）
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              )}

              {selectedBaseModel && (
                <p className="text-xs text-muted-foreground">
                  架构：{modelArchLabel(selectedBaseModel)} · 任务：
                  {taskLabel(selectedBaseModel.task)} · 原始文件名：
                  {selectedBaseModel.file_name}
                  {selectedBaseModel.info.epochs !== null &&
                    ` · 已保存轮数信息：${selectedBaseModel.info.epoch ?? "最终"} / ${selectedBaseModel.info.epochs}`}
                </p>
              )}
            </Field>
          )}

          <label className="flex cursor-pointer items-center justify-between gap-4 rounded-xl border bg-muted/20 p-3.5">
            <div className="grid gap-0.5">
              <span className="text-sm font-medium">
                训练完成后自动将最佳权重（best.pt）注册到模型仓库
              </span>
              <span className="text-xs text-muted-foreground">
                开启后，训练成功会自动在所属项目下生成新的模型版本，可直接用于
                ONNX 转换与模型审图。
              </span>
            </div>
            <Switch
              checked={autoRegister}
              disabled={createMutation.isPending}
              onCheckedChange={setAutoRegister}
            />
          </label>
        </CardContent>
      </Card>

      {/* 3. YOLO 全量参数编辑器 */}
      <Card>
        <CardContent className="pt-6">
          <TrainingParamsEditor
            schema={schema}
            values={params}
            onChange={setParams}
            initMode={initMode}
            disabled={createMutation.isPending}
          />
        </CardContent>
      </Card>

      {/* 4. 底部提交栏 */}
      <div className="sticky bottom-3 z-10 flex flex-wrap items-center justify-between gap-3 rounded-xl border bg-card/95 px-4 py-3 shadow-md backdrop-blur">
        <div className="grid gap-0.5 text-xs">
          <div className="flex flex-wrap items-center gap-2 font-medium text-foreground">
            <span>
              {initMode === "scratch"
                ? `${yoloVersionLabel(yoloVersion)}${modelScale}.yaml（从头训练）`
                : selectedBaseModel
                  ? `基于模型 v${selectedBaseModel.version}（${INIT_MODE_LABELS[initMode]}）`
                  : INIT_MODE_LABELS[initMode]}
            </span>
            <span>·</span>
            <span className="tabular-nums">
              epochs={String(params.epochs ?? 100)}
            </span>
            <span>·</span>
            <span className="tabular-nums">
              batch={String(params.batch ?? 16)}
            </span>
            <span>·</span>
            <span className="tabular-nums">
              imgsz=
              {Array.isArray(params.imgsz)
                ? `[${params.imgsz.join(",")}]`
                : String(params.imgsz ?? 640)}
            </span>
            {modifiedCount > 0 && (
              <>
                <span>·</span>
                <span className="text-primary">
                  已自定义 {modifiedCount} 项参数
                </span>
              </>
            )}
          </div>
          {(formError || createMutation.error) && (
            <p role="alert" className="text-destructive">
              {formError ?? createMutation.error?.message}
            </p>
          )}
        </div>

        <div className="flex items-center gap-2">
          <Button
            type="button"
            variant="outline"
            disabled={createMutation.isPending}
            asChild
          >
            <Link to={backPath}>取消</Link>
          </Button>
          <Button
            type="submit"
            disabled={createMutation.isPending || readyVersions.length === 0}
          >
            {createMutation.isPending ? <Spinner aria-hidden /> : <Rocket />}
            开始训练
          </Button>
        </div>
      </div>
    </form>
  )
}
