/**
 * 新建审图任务：选择模型与数据集（可以多个，按选择的顺序排列图片），可选名称、
 * 说明与识别参数。创建后排队等训练节点识别，识别结果写成只属于这个审图任务的
 * VOC 标注，不会改动数据集。
 */
import { useId, useMemo, useState, type FormEvent } from "react"
import { useMutation, useQuery } from "@tanstack/react-query"
import { ArrowRight, ChevronDown, Database, Upload } from "lucide-react"
import { Link, useNavigate } from "react-router"
import { toast } from "sonner"

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
  FieldGroup,
  FieldLabel,
} from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectLabel,
  SelectSeparator,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { Skeleton } from "@/components/ui/skeleton"
import { Spinner } from "@/components/ui/spinner"
import { Textarea } from "@/components/ui/textarea"
import { datasetKeys, datasetsQuery } from "@/features/datasets/api"
import { DescriptionCounter } from "@/features/datasets/dataset-actions"
import { LoadError } from "@/features/datasets/load-error"
import { modelKeys, modelsQuery, type Model } from "@/features/models/api"
import { modelTitle } from "@/features/models/model-info"
import {
  imgSizeText,
  modelArchLabel,
  taskLabel,
} from "@/features/models/yolo-versions"
import { DatasetPicker } from "@/features/projects/dataset-picker"
import { createReview, reviewKeys, type Review } from "@/features/reviews/api"
import { reviewDetailPath } from "@/features/reviews/params"
import { cacheReview } from "@/features/reviews/review-actions"
import {
  changedOptionCount,
  datasetSelectionError,
  defaultOptionsDraft,
  defaultReviewName,
  groupModelsByProject,
  optionsDraftErrors,
  optionsFromDraft,
  projectDatasetIds,
  reviewModelBlocker,
  selectedDatasets,
  validateReviewDescription,
  validateReviewName,
  type ReviewOptionsDraft,
} from "@/features/reviews/review-format"
import { isPermanentError } from "@/lib/api"
import { formatNumber } from "@/lib/format"
import { queryClient } from "@/lib/query-client"
import { cn } from "@/lib/utils"

/** 常用的输入尺寸，一键填入正方形。 */
const SIZE_PRESETS = [320, 480, 640, 960, 1280]

type CreateReviewDialogProps = {
  open: boolean
  onOpenChange: (open: boolean) => void
  /** 预先选中的模型（模型详情页的“用这个模型审图”）。 */
  modelId?: number | null
  /**
   * 预先选中的数据集（数据集页面的“审核记录”）。省略时选中模型所属项目中
   * 有图片的数据集。
   */
  datasetIds?: readonly number[]
  /** 创建成功后调用；省略时进入审图任务的详情页。 */
  onCreated?: (review: Review) => void
}

/** 新建审图任务。由调用方在每次打开时更换 key，表单从头开始。 */
export function CreateReviewDialog({
  open,
  onOpenChange,
  modelId = null,
  datasetIds,
  onCreated,
}: CreateReviewDialogProps) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[calc(100dvh-2rem)] overflow-y-auto sm:max-w-2xl">
        <CreateReviewForm
          initialModelId={modelId}
          initialDatasetIds={datasetIds}
          onDone={() => onOpenChange(false)}
          onCreated={onCreated}
        />
      </DialogContent>
    </Dialog>
  )
}

function CreateReviewForm({
  initialModelId,
  initialDatasetIds,
  onDone,
  onCreated,
}: {
  initialModelId: number | null
  initialDatasetIds: readonly number[] | undefined
  onDone: () => void
  onCreated?: (review: Review) => void
}) {
  const navigate = useNavigate()
  const modelFieldId = useId()
  const nameId = useId()
  const descriptionId = useId()

  const models = useQuery(modelsQuery())
  const datasets = useQuery(datasetsQuery())

  const [modelId, setModelId] = useState<number | null>(initialModelId)
  // null：还没有手动选择，跟随模型所属项目的数据集；之后保持用户的选择。
  const [chosen, setChosen] = useState<Set<number> | null>(
    initialDatasetIds ? new Set(initialDatasetIds) : null
  )
  const [name, setName] = useState("")
  const [description, setDescription] = useState("")
  // null：没有修改过，跟随模型的默认值（输入尺寸是模型的训练尺寸）。
  const [optionsDraft, setOptionsDraft] = useState<ReviewOptionsDraft | null>(
    null
  )
  const [advanced, setAdvanced] = useState(false)
  const [submitted, setSubmitted] = useState(false)

  const model = models.data?.items.find((item) => item.id === modelId) ?? null
  const groups = useMemo(
    () => groupModelsByProject(models.data?.items ?? []),
    [models.data]
  )
  const allDatasets = useMemo(() => datasets.data?.items ?? [], [datasets.data])
  const selection = useMemo(
    () =>
      chosen ??
      new Set(model ? projectDatasetIds(allDatasets, model.project.id) : []),
    [chosen, model, allDatasets]
  )
  const picked = selectedDatasets(allDatasets, selection)
  const defaults = defaultOptionsDraft(model)
  const options = optionsDraft ?? defaults
  const optionErrors = optionsDraftErrors(options)
  const optionsInvalid = Object.values(optionErrors).some((e) => e !== null)

  const mutation = useMutation({
    mutationFn: () => {
      const requestOptions = optionsFromDraft(options)
      return createReview({
        name: name.trim() || undefined,
        description: description.trim() || undefined,
        model_id: modelId ?? 0,
        dataset_ids: picked.datasets.map((dataset) => dataset.id),
        options: requestOptions ?? undefined,
      })
    },
    onSuccess: (review) => {
      cacheReview(review)
      void queryClient.invalidateQueries({ queryKey: reviewKeys.lists() })
      void queryClient.invalidateQueries({ queryKey: reviewKeys.datasets() })
      toast.success(`已创建审图任务“${review.name}”`, {
        description:
          review.workers_online > 0
            ? `${formatNumber(review.counts.total)} 张图片排队等待训练节点识别，识别完成的图片可以立即开始审核。`
            : `${formatNumber(review.counts.total)} 张图片排队等待识别。现在没有可以识别的训练节点，节点上线后会自动开始。`,
      })
      onDone()
      if (onCreated) {
        onCreated(review)
      } else {
        navigate(reviewDetailPath(review.id))
      }
    },
    onError: (error) => {
      // 多半是所选的模型或数据集刚被删除、模型文件丢失：刷新后显示最新的列表。
      if (isPermanentError(error)) {
        void queryClient.invalidateQueries({ queryKey: modelKeys.lists() })
        void queryClient.invalidateQueries({ queryKey: datasetKeys.lists() })
      }
    },
  })

  const modelBlocker = model ? reviewModelBlocker(model) : null
  const modelError =
    modelBlocker ??
    (submitted && modelId === null
      ? "请选择模型"
      : modelId !== null && models.data && !model
        ? "所选模型不存在或已被删除"
        : null)
  const datasetError = datasets.data
    ? datasetSelectionError(picked.datasets.length, picked.images)
    : null
  const nameError = validateReviewName(name, { optional: true })
  const descriptionError = validateReviewDescription(description)
  const changedOptions = changedOptionCount(options, defaults)

  const submit = (event: FormEvent) => {
    event.preventDefault()
    setSubmitted(true)
    if (optionsInvalid) {
      setAdvanced(true)
    }
    if (
      modelId === null ||
      !model ||
      modelBlocker ||
      datasetError ||
      nameError ||
      descriptionError ||
      optionsInvalid
    ) {
      return
    }
    mutation.mutate()
  }

  const changeModel = (id: number) => {
    setModelId(id)
    mutation.reset()
  }
  const changeDatasets = (next: Set<number>) => {
    setChosen(next)
    mutation.reset()
  }
  const updateOptions = (changes: Partial<ReviewOptionsDraft>) =>
    setOptionsDraft({ ...options, ...changes })

  const placeholderName = model
    ? defaultReviewName(model.project.name, model.version)
    : "如：外观检测 v3 审图"
  const noModels = models.data !== undefined && models.data.items.length === 0
  const pending = mutation.isPending

  return (
    <form
      onSubmit={submit}
      className="grid min-w-0 grid-cols-[minmax(0,1fr)] gap-6"
      noValidate
    >
      <DialogHeader>
        <DialogTitle>新建审图任务</DialogTitle>
        <DialogDescription>
          用模型识别所选数据集中的图片，再逐张确认识别结果。识别结果只保存在这个审图任务中，不会改动数据集的标注。
        </DialogDescription>
      </DialogHeader>

      <FieldGroup className="gap-5">
        <Field data-invalid={modelError ? true : undefined}>
          <FieldLabel htmlFor={modelFieldId}>模型</FieldLabel>
          {models.isPending ? (
            <Skeleton className="h-9 w-full" />
          ) : models.data === undefined ? (
            <LoadError
              title="无法获取模型列表"
              error={models.error}
              retrying={models.isFetching}
              onRetry={() => models.refetch()}
            />
          ) : noModels ? (
            <EmptyChoice
              icon={Upload}
              text="还没有模型。审图需要一个检测（或分割、姿态、旋转框）模型，请先在模型仓库上传。"
              to="/models"
              action="前往模型仓库"
            />
          ) : (
            <ModelSelect
              id={modelFieldId}
              groups={groups}
              value={modelId}
              model={model}
              onChange={changeModel}
              disabled={pending}
              invalid={modelError !== null}
            />
          )}
          {modelError ? (
            <FieldError>{modelError}</FieldError>
          ) : (
            model && (
              <FieldDescription>
                {modelArchLabel(model)} · {taskLabel(model.task)}
                {model.info.class_names.length > 0 &&
                  ` · ${formatNumber(model.info.class_names.length)} 类`}
                {model.description && ` · ${model.description}`}
              </FieldDescription>
            )
          )}
        </Field>

        <Field data-invalid={submitted && datasetError ? true : undefined}>
          <FieldLabel asChild>
            <span>数据集</span>
          </FieldLabel>
          <FieldDescription>
            {model && chosen === null
              ? `默认选中项目“${model.project.name}”中的数据集；可以选择多个，图片按选择的顺序排列。`
              : "可以选择多个，图片按选择的顺序排列。"}
          </FieldDescription>
          {datasets.isPending ? (
            <Skeleton className="h-32 w-full" />
          ) : datasets.data === undefined ? (
            <LoadError
              title="无法获取数据集列表"
              error={datasets.error}
              retrying={datasets.isFetching}
              onRetry={() => datasets.refetch()}
            />
          ) : allDatasets.length === 0 ? (
            <EmptyChoice
              icon={Database}
              text="还没有数据集，请先在数据集页面上传图片或添加已有目录。"
              to="/datasets"
              action="前往数据集"
            />
          ) : (
            <DatasetPicker
              datasets={allDatasets}
              selected={selection}
              onSelectedChange={changeDatasets}
              disabled={pending}
              label="选择要审图的数据集"
            />
          )}
          {datasets.data && (
            <p
              className={cn(
                "text-sm tabular-nums",
                submitted && datasetError
                  ? "text-destructive"
                  : "text-muted-foreground"
              )}
              role={submitted && datasetError ? "alert" : undefined}
            >
              {submitted && datasetError
                ? datasetError
                : picked.datasets.length > 0
                  ? `共 ${formatNumber(picked.images)} 张图片${datasetError ? `：${datasetError}` : ""}`
                  : "还没有选择数据集"}
            </p>
          )}
        </Field>

        <Field data-invalid={nameError ? true : undefined}>
          <FieldLabel htmlFor={nameId}>名称</FieldLabel>
          <Input
            id={nameId}
            value={name}
            onChange={(event) => setName(event.target.value)}
            placeholder={placeholderName}
            aria-invalid={nameError ? true : undefined}
            disabled={pending}
            autoComplete="off"
          />
          {nameError ? (
            <FieldError>{nameError}</FieldError>
          ) : (
            <FieldDescription>
              {model
                ? `可选，不填时为“${placeholderName}”。`
                : "可选，不填时用模型的项目与版本命名，如“外观检测 v3 审图”。"}
            </FieldDescription>
          )}
        </Field>

        <Field data-invalid={descriptionError ? true : undefined}>
          <FieldLabel htmlFor={descriptionId}>说明</FieldLabel>
          <Textarea
            id={descriptionId}
            value={description}
            onChange={(event) => setDescription(event.target.value)}
            placeholder="审图的目的、批次、注意事项等（可选）"
            aria-invalid={descriptionError ? true : undefined}
            disabled={pending}
            className="min-h-16"
          />
          {descriptionError ? (
            <FieldError>{descriptionError}</FieldError>
          ) : (
            description.trim() !== "" && (
              <DescriptionCounter value={description} />
            )
          )}
        </Field>

        <div className="grid min-w-0 gap-4">
          <Button
            type="button"
            variant="ghost"
            size="sm"
            className="-ml-2 justify-self-start"
            onClick={() => setAdvanced((value) => !value)}
            aria-expanded={advanced}
          >
            <ChevronDown
              className={cn("transition-transform", !advanced && "-rotate-90")}
            />
            识别参数
            {changedOptions > 0 ? (
              <Badge variant="secondary" className="font-normal">
                已修改 {changedOptions} 项
              </Badge>
            ) : (
              !advanced && (
                <span className="font-normal text-muted-foreground">
                  置信度 ≥ {options.conf.trim()} ·{" "}
                  {optionErrors.imgsz
                    ? "输入尺寸有误"
                    : imgSizeText([
                        Number(options.height),
                        Number(options.width),
                      ])}
                </span>
              )
            )}
          </Button>
          {advanced && (
            <OptionsFields
              options={options}
              defaults={defaults}
              errors={optionErrors}
              trainedSize={model?.info.image_size ?? null}
              onChange={updateOptions}
              onReset={() => setOptionsDraft(null)}
              disabled={pending}
            />
          )}
        </div>
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
          disabled={pending}
        >
          取消
        </Button>
        <Button
          type="submit"
          disabled={
            pending ||
            noModels ||
            modelBlocker !== null ||
            (submitted && datasetError !== null)
          }
        >
          {pending && <Spinner aria-hidden />}
          {picked.images > 0 && !datasetError
            ? `开始识别 ${formatNumber(picked.images)} 张`
            : "开始识别"}
        </Button>
      </DialogFooter>
    </form>
  )
}

/** 按项目分组的模型选择框：不能用于审图的模型显示原因、不能选择。 */
function ModelSelect({
  id,
  groups,
  value,
  model,
  onChange,
  disabled,
  invalid,
}: {
  id: string
  groups: ReturnType<typeof groupModelsByProject>
  value: number | null
  model: Model | null
  onChange: (id: number) => void
  disabled: boolean
  invalid: boolean
}) {
  return (
    <Select
      value={value === null ? "" : String(value)}
      onValueChange={(next) => onChange(Number(next))}
      disabled={disabled}
    >
      <SelectTrigger
        id={id}
        className="w-full"
        aria-invalid={invalid ? true : undefined}
      >
        <SelectValue placeholder="选择模型">
          {model ? (
            <span className="truncate">
              {modelTitle(model)}
              <span className="text-muted-foreground">
                {" "}
                · {modelArchLabel(model)}
              </span>
            </span>
          ) : value !== null ? (
            `模型 #${value}`
          ) : undefined}
        </SelectValue>
      </SelectTrigger>
      <SelectContent position="popper" className="max-h-80">
        {groups.map((group, index) => (
          <SelectGroup key={group.project.id}>
            {index > 0 && <SelectSeparator />}
            <SelectLabel className="truncate">{group.project.name}</SelectLabel>
            {group.models.map((item) => {
              const blocker = reviewModelBlocker(item)
              return (
                <SelectItem
                  key={item.id}
                  value={String(item.id)}
                  disabled={blocker !== null}
                >
                  <span className="flex min-w-0 items-baseline gap-2">
                    <span className="shrink-0 font-medium tabular-nums">
                      v{item.version}
                    </span>
                    <span className="truncate text-muted-foreground">
                      {modelArchLabel(item)} · {taskLabel(item.task)}
                      {blocker
                        ? `（${blocker}）`
                        : item.description && ` · ${item.description}`}
                    </span>
                  </span>
                </SelectItem>
              )
            })}
          </SelectGroup>
        ))}
      </SelectContent>
    </Select>
  )
}

/** 识别参数：置信度阈值、IoU 阈值、输入尺寸与最多检测框数。 */
function OptionsFields({
  options,
  defaults,
  errors,
  trainedSize,
  onChange,
  onReset,
  disabled,
}: {
  options: ReviewOptionsDraft
  defaults: ReviewOptionsDraft
  errors: ReturnType<typeof optionsDraftErrors>
  trainedSize: number | null
  onChange: (changes: Partial<ReviewOptionsDraft>) => void
  onReset: () => void
  disabled: boolean
}) {
  const confId = useId()
  const iouId = useId()
  const heightId = useId()
  const maxDetId = useId()
  const defaultSize = `${defaults.height}${trainedSize !== null && trainedSize > 0 ? "（模型的训练尺寸）" : ""}`

  return (
    <div className="grid min-w-0 gap-5 rounded-lg border p-4">
      <div className="grid gap-5 sm:grid-cols-3">
        <Field data-invalid={errors.conf ? true : undefined}>
          <FieldLabel htmlFor={confId}>置信度阈值</FieldLabel>
          <Input
            id={confId}
            value={options.conf}
            onChange={(event) => onChange({ conf: event.target.value })}
            inputMode="decimal"
            className="tabular-nums"
            aria-invalid={errors.conf ? true : undefined}
            disabled={disabled}
          />
          {errors.conf ? (
            <FieldError>{errors.conf}</FieldError>
          ) : (
            <FieldDescription>
              0.01–1，低于它的框不保留，默认 {defaults.conf}
            </FieldDescription>
          )}
        </Field>
        <Field data-invalid={errors.iou ? true : undefined}>
          <FieldLabel htmlFor={iouId}>IoU 阈值</FieldLabel>
          <Input
            id={iouId}
            value={options.iou}
            onChange={(event) => onChange({ iou: event.target.value })}
            inputMode="decimal"
            className="tabular-nums"
            aria-invalid={errors.iou ? true : undefined}
            disabled={disabled}
          />
          {errors.iou ? (
            <FieldError>{errors.iou}</FieldError>
          ) : (
            <FieldDescription>
              0.1–0.95，NMS 合并重叠框，默认 {defaults.iou}
            </FieldDescription>
          )}
        </Field>
        <Field data-invalid={errors.maxDet ? true : undefined}>
          <FieldLabel htmlFor={maxDetId}>最多检测框数</FieldLabel>
          <Input
            id={maxDetId}
            value={options.maxDet}
            onChange={(event) => onChange({ maxDet: event.target.value })}
            inputMode="numeric"
            className="tabular-nums"
            aria-invalid={errors.maxDet ? true : undefined}
            disabled={disabled}
          />
          {errors.maxDet ? (
            <FieldError>{errors.maxDet}</FieldError>
          ) : (
            <FieldDescription>
              每张图片 1–1000，默认 {defaults.maxDet}
            </FieldDescription>
          )}
        </Field>
      </div>
      <Field data-invalid={errors.imgsz ? true : undefined}>
        <FieldLabel htmlFor={heightId}>输入尺寸（高 × 宽）</FieldLabel>
        <div className="flex flex-wrap items-center gap-2">
          <div className="flex items-center gap-2">
            <Input
              id={heightId}
              value={options.height}
              onChange={(event) => onChange({ height: event.target.value })}
              inputMode="numeric"
              className="w-24 tabular-nums"
              aria-label="高"
              aria-invalid={
                errors.imgsz && options.height !== "" ? true : undefined
              }
              disabled={disabled}
            />
            <span className="text-muted-foreground" aria-hidden>
              ×
            </span>
            <Input
              value={options.width}
              onChange={(event) => onChange({ width: event.target.value })}
              inputMode="numeric"
              className="w-24 tabular-nums"
              aria-label="宽"
              aria-invalid={errors.imgsz ? true : undefined}
              disabled={disabled}
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
                    onChange({ height: String(size), width: String(size) })
                  }
                  disabled={disabled}
                  aria-pressed={active}
                >
                  {size}
                </Button>
              )
            })}
          </div>
        </div>
        {errors.imgsz ? (
          <FieldError>{errors.imgsz}</FieldError>
        ) : (
          <FieldDescription>
            32–4096，32 的倍数，默认 {defaultSize}。图片缩放到这个尺寸后识别。
          </FieldDescription>
        )}
      </Field>
      <Button
        type="button"
        variant="ghost"
        size="xs"
        className="justify-self-start text-muted-foreground"
        onClick={onReset}
        disabled={disabled}
      >
        恢复默认值
      </Button>
    </div>
  )
}

/** 没有可选的模型或数据集时的提示与入口。 */
function EmptyChoice({
  icon: Icon,
  text,
  to,
  action,
}: {
  icon: typeof Upload
  text: string
  to: string
  action: string
}) {
  return (
    <div className="flex flex-col items-center gap-3 rounded-lg border border-dashed px-4 py-6 text-center text-sm text-muted-foreground">
      <Icon className="size-5" />
      <p>{text}</p>
      <Button variant="outline" size="sm" asChild>
        <Link to={to}>
          {action}
          <ArrowRight />
        </Link>
      </Button>
    </div>
  )
}
