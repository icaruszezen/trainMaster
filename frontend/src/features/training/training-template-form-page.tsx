/**
 * 新建 / 编辑训练参数模板页：
 * - `/training/templates/new`（支持 `?from=<templateId>` 复制已有模板）
 * - `/training/templates/:templateId/edit`
 */
import { useId, useMemo, useState, type FormEvent } from "react"
import { useMutation, useQuery } from "@tanstack/react-query"
import {
  AlertTriangle,
  ArrowLeft,
  BookmarkCheck,
  Rocket,
  Sparkles,
  Star,
} from "lucide-react"
import { Link, useNavigate, useParams, useSearchParams } from "react-router"
import { toast } from "sonner"

import { PageHeader } from "@/components/page-header"
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import {
  Card,
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
import { Field, FieldLabel } from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import { Skeleton } from "@/components/ui/skeleton"
import { Spinner } from "@/components/ui/spinner"
import { Switch } from "@/components/ui/switch"
import { Textarea } from "@/components/ui/textarea"
import { LoadError } from "@/features/datasets/load-error"
import {
  cacheTrainingTemplate,
  createTrainingTemplate,
  trainingParamsSchemaQuery,
  trainingTemplateQuery,
  updateTrainingTemplate,
  type ParamsSchemaResponse,
  type TrainingParamTemplate,
} from "@/features/training/api"
import {
  newTrainingJobPath,
  parsePositiveId,
  TRAINING_TEMPLATES_PATH,
} from "@/features/training/paths"
import {
  buildTemplateDefaultParams,
  buildTemplateParamsPayload,
  countModifiedParams,
  formatCompactParamValue,
  splitSpecsByTier,
  validateTrainingParams,
} from "@/features/training/training-params"
import { TrainingParamsEditor } from "@/features/training/training-params-editor"
import { usePageTitle } from "@/hooks/use-page-title"
import { ApiError } from "@/lib/api"

export function TrainingTemplateFormPage() {
  const { templateId: rawTemplateId } = useParams()
  const [searchParams] = useSearchParams()

  const isEditRoute = rawTemplateId !== undefined
  const editId = isEditRoute ? parsePositiveId(rawTemplateId) : null
  const fromId = !isEditRoute ? parsePositiveId(searchParams.get("from")) : null

  const schemaQuery = useQuery(trainingParamsSchemaQuery())
  const editQuery = useQuery({
    ...trainingTemplateQuery(editId ?? 0),
    enabled: isEditRoute && editId !== null,
  })
  const fromQuery = useQuery({
    ...trainingTemplateQuery(fromId ?? 0),
    enabled: !isEditRoute && fromId !== null,
  })

  usePageTitle(
    isEditRoute
      ? editQuery.data
        ? `编辑：${editQuery.data.name}`
        : "编辑参数模板"
      : fromQuery.data
        ? `复制：${fromQuery.data.name}`
        : "新建参数模板"
  )

  if (isEditRoute && editId === null) {
    return <TemplateNotFoundState />
  }

  if (
    schemaQuery.isPending ||
    (isEditRoute && editQuery.isPending) ||
    (!isEditRoute && fromId !== null && fromQuery.isPending)
  ) {
    return (
      <div className="grid gap-4" aria-busy="true" aria-label="正在加载">
        <Skeleton className="h-12 w-64" />
        <Skeleton className="h-44 rounded-xl" />
        <Skeleton className="h-96 rounded-xl" />
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

  if (isEditRoute) {
    if (editQuery.error && !editQuery.data) {
      if (editQuery.error instanceof ApiError && editQuery.error.status === 404) {
        return <TemplateNotFoundState />
      }
      return (
        <LoadError
          title="无法加载参数模板"
          error={editQuery.error}
          retrying={editQuery.isFetching}
          onRetry={() => editQuery.refetch()}
        />
      )
    }
    if (!editQuery.data) {
      return <TemplateNotFoundState />
    }
  }

  const formKey = isEditRoute
    ? `edit:${editQuery.data!.id}:${editQuery.data!.updated_at}`
    : `new:${fromQuery.data?.id ?? "blank"}`

  return (
    <TrainingTemplateForm
      key={formKey}
      schema={schemaQuery.data}
      editingTemplate={isEditRoute ? editQuery.data! : null}
      fromTemplate={!isEditRoute ? (fromQuery.data ?? null) : null}
      fromMissing={!isEditRoute && fromId !== null && Boolean(fromQuery.error)}
    />
  )
}

function TrainingTemplateForm({
  schema,
  editingTemplate,
  fromTemplate,
  fromMissing,
}: {
  schema: ParamsSchemaResponse
  editingTemplate: TrainingParamTemplate | null
  fromTemplate: TrainingParamTemplate | null
  fromMissing: boolean
}) {
  const navigate = useNavigate()
  const nameId = useId()
  const descId = useId()

  const isEdit = editingTemplate !== null
  const seed = editingTemplate ?? fromTemplate

  const [name, setName] = useState(() => {
    if (editingTemplate) {
      return editingTemplate.name
    }
    if (fromTemplate) {
      return `${fromTemplate.name}-副本`
    }
    return ""
  })
  const [description, setDescription] = useState(() => seed?.description ?? "")
  const [isDefault, setIsDefault] = useState(() =>
    editingTemplate ? editingTemplate.is_default : false
  )
  const [params, setParams] = useState<Record<string, unknown>>(() =>
    seed
      ? buildTemplateParamsPayload(schema.specs, seed.params)
      : buildTemplateDefaultParams(schema.specs)
  )
  const [formError, setFormError] = useState<string | null>(null)

  const { common: commonSpecs, advanced: advancedSpecs } = useMemo(
    () => splitSpecsByTier(schema.specs),
    [schema.specs]
  )
  const commonModified = useMemo(
    () => countModifiedParams(commonSpecs, params),
    [commonSpecs, params]
  )
  const advancedModified = useMemo(
    () => countModifiedParams(advancedSpecs, params),
    [advancedSpecs, params]
  )
  const totalModified = commonModified + advancedModified

  const saveMutation = useMutation({
    mutationFn: async ({
      goTrain,
    }: {
      goTrain: boolean
    }): Promise<{ saved: TrainingParamTemplate; goTrain: boolean }> => {
      const payload = buildTemplateParamsPayload(schema.specs, params)
      const saved = isEdit
        ? await updateTrainingTemplate(editingTemplate.id, {
            name: name.trim(),
            description: description.trim(),
            params: payload,
            is_default: isDefault,
          })
        : await createTrainingTemplate({
            name: name.trim(),
            description: description.trim(),
            params: payload,
            is_default: isDefault,
          })
      return { saved, goTrain }
    },
    onSuccess: ({ saved, goTrain }) => {
      cacheTrainingTemplate(saved)
      toast.success(
        isEdit
          ? `已更新参数模板“${saved.name}”`
          : `已创建参数模板“${saved.name}”`,
        {
          description: saved.is_default
            ? "已设为默认模板，后续新建训练任务时将自动加载。"
            : undefined,
        }
      )
      if (goTrain) {
        navigate(newTrainingJobPath({ templateId: saved.id }))
      } else {
        navigate(TRAINING_TEMPLATES_PATH)
      }
    },
  })

  const runSave = (goTrain: boolean) => {
    setFormError(null)
    if (saveMutation.isPending) {
      return
    }
    if (!name.trim()) {
      setFormError("请输入模板名称")
      return
    }
    const payload = buildTemplateParamsPayload(schema.specs, params)
    const paramErr = validateTrainingParams(schema.specs, payload)
    if (paramErr) {
      setFormError(`参数 ${paramErr.key}：${paramErr.message}`)
      return
    }
    saveMutation.mutate({ goTrain })
  }

  const handleSubmit = (event: FormEvent) => {
    event.preventDefault()
    runSave(false)
  }

  return (
    <form onSubmit={handleSubmit} className="grid min-w-0 gap-6" noValidate>
      <div className="grid gap-2">
        <Button variant="ghost" size="sm" className="-ml-2 w-fit" asChild>
          <Link to={TRAINING_TEMPLATES_PATH}>
            <ArrowLeft />
            返回参数模板
          </Link>
        </Button>

        <PageHeader
          title={isEdit ? "编辑参数模板" : "新建参数模板"}
          description={
            isEdit
              ? "修改模板名称、说明与 YOLO 参数预设；已创建的训练任务仍保留提交时的参数快照，不受修改影响。"
              : "配置一组可复用的 Ultralytics YOLO 训练参数，保存后即可在“新建训练任务”中一键加载并微调。"
          }
        />
      </div>

      {fromTemplate && (
        <Alert>
          <Sparkles className="size-4" />
          <AlertTitle>已从模板“{fromTemplate.name}”复制全部参数</AlertTitle>
          <AlertDescription>
            已预填原模板的参数设置，修改名称或参数后即可保存为新模板。
          </AlertDescription>
        </Alert>
      )}

      {fromMissing && (
        <Alert>
          <AlertTriangle className="size-4" />
          <AlertTitle>未找到来源参数模板</AlertTitle>
          <AlertDescription>
            原模板可能已被删除，已自动切换为 Ultralytics 官方默认参数。
          </AlertDescription>
        </Alert>
      )}

      {/* 1. 基本信息 */}
      <Card>
        <CardHeader>
          <CardTitle>1. 模板基本信息</CardTitle>
          <CardDescription>
            给模板起一个清晰的名称并说明适用场景，方便团队在不同检测项目之间快速复用。
          </CardDescription>
        </CardHeader>
        <CardContent className="grid gap-5">
          <div className="grid grid-cols-1 gap-4 md:grid-cols-2">
            <Field>
              <FieldLabel htmlFor={nameId}>模板名称</FieldLabel>
              <Input
                id={nameId}
                value={name}
                maxLength={120}
                disabled={saveMutation.isPending}
                placeholder="如：PCB 表面缺陷高分检测（1280 + AdamW）"
                onChange={(event) => {
                  setName(event.target.value)
                  setFormError(null)
                  saveMutation.reset()
                }}
              />
            </Field>

            <Field>
              <FieldLabel htmlFor={descId}>模板说明（可选）</FieldLabel>
              <Textarea
                id={descId}
                value={description}
                maxLength={2000}
                disabled={saveMutation.isPending}
                placeholder="记录适用缺陷特征、显存要求或关键调参备注…"
                className="min-h-9 py-2"
                rows={1}
                onChange={(event) => {
                  setDescription(event.target.value)
                  saveMutation.reset()
                }}
              />
            </Field>
          </div>

          <label className="flex cursor-pointer items-center justify-between gap-4 rounded-xl border bg-muted/20 p-3.5">
            <div className="grid gap-0.5">
              <span className="flex items-center gap-1.5 text-sm font-medium">
                <Star className="size-4 text-amber-500" />
                设为默认参数模板
              </span>
              <span className="text-xs text-muted-foreground">
                开启后，打开“新建训练任务”页面时会自动加载此模板的参数（全局最多 1
                个默认模板，开启将自动取消原默认模板）。
              </span>
            </div>
            <Switch
              checked={isDefault}
              disabled={saveMutation.isPending}
              onCheckedChange={setIsDefault}
            />
          </label>
        </CardContent>
      </Card>

      {/* 2. 参数编辑器 */}
      <Card>
        <CardContent className="pt-6">
          <TrainingParamsEditor
            schema={schema}
            values={params}
            onChange={(next) => {
              setParams(next)
              setFormError(null)
            }}
            mode="template"
            disabled={saveMutation.isPending}
          />
        </CardContent>
      </Card>

      {/* 3. 底部保存栏 */}
      <div className="sticky bottom-3 z-10 flex flex-wrap items-center justify-between gap-3 rounded-xl border bg-card/95 px-4 py-3 shadow-md backdrop-blur">
        <div className="grid gap-0.5 text-xs">
          <div className="flex flex-wrap items-center gap-2 font-medium text-foreground">
            <span className="tabular-nums">
              epochs={formatCompactParamValue(params.epochs)}
            </span>
            <span>·</span>
            <span className="tabular-nums">
              batch={formatCompactParamValue(params.batch)}
            </span>
            <span>·</span>
            <span className="tabular-nums">
              imgsz={formatCompactParamValue(params.imgsz)}
            </span>
            <span>·</span>
            <span className="tabular-nums">
              lr0={formatCompactParamValue(params.lr0)}
            </span>
            {totalModified > 0 && (
              <>
                <span>·</span>
                <span className="text-primary tabular-nums">
                  自定义 {totalModified} 项（常用 {commonModified} / 高级{" "}
                  {advancedModified}）
                </span>
              </>
            )}
          </div>
          {(formError || saveMutation.error) && (
            <p role="alert" className="text-destructive">
              {formError ?? saveMutation.error?.message}
            </p>
          )}
        </div>

        <div className="flex flex-wrap items-center gap-2">
          <Button
            type="button"
            variant="outline"
            disabled={saveMutation.isPending}
            asChild
          >
            <Link to={TRAINING_TEMPLATES_PATH}>取消</Link>
          </Button>
          <Button
            type="button"
            variant="secondary"
            disabled={saveMutation.isPending}
            onClick={() => runSave(true)}
          >
            {saveMutation.isPending && saveMutation.variables?.goTrain ? (
              <Spinner aria-hidden />
            ) : (
              <Rocket />
            )}
            保存并去训练
          </Button>
          <Button type="submit" disabled={saveMutation.isPending}>
            {saveMutation.isPending && !saveMutation.variables?.goTrain ? (
              <Spinner aria-hidden />
            ) : (
              <BookmarkCheck />
            )}
            {isEdit ? "保存修改" : "创建模板"}
          </Button>
        </div>
      </div>
    </form>
  )
}

function TemplateNotFoundState() {
  return (
    <Empty className="border bg-card">
      <EmptyHeader>
        <EmptyMedia variant="icon">
          <AlertTriangle />
        </EmptyMedia>
        <EmptyTitle>找不到这个参数模板</EmptyTitle>
        <EmptyDescription>
          该参数模板可能已被删除，或链接中的模板编号不存在。
        </EmptyDescription>
      </EmptyHeader>
      <EmptyContent>
        <Button asChild>
          <Link to={TRAINING_TEMPLATES_PATH}>返回参数模板列表</Link>
        </Button>
      </EmptyContent>
    </Empty>
  )
}
