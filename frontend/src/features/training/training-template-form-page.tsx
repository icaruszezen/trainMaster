/**
 * 新建 / 编辑 / 查看训练参数模板页：
 * - `/training/templates/new`（支持 `?from=<templateId>` 复制已有模板，也支持页内从内置/自定义模板快速预填）
 * - `/training/templates/:templateId/edit`（自定义模板可编辑；系统内置模板自动进入只读详情模式，支持切换默认、复制或直接训练）
 */
import { useId, useMemo, useState, type FormEvent } from "react"
import { useMutation, useQuery } from "@tanstack/react-query"
import {
  AlertTriangle,
  ArrowLeft,
  Bookmark,
  BookmarkCheck,
  Copy,
  Lock,
  Rocket,
  ShieldCheck,
  Sparkles,
  Star,
} from "lucide-react"
import { Link, useNavigate, useParams, useSearchParams } from "react-router"
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
import { Switch } from "@/components/ui/switch"
import { Textarea } from "@/components/ui/textarea"
import { LoadError } from "@/features/datasets/load-error"
import {
  cacheTrainingTemplate,
  createTrainingTemplate,
  trainingParamsSchemaQuery,
  trainingTemplateQuery,
  trainingTemplatesQuery,
  updateTrainingTemplate,
  type ParamsSchemaResponse,
  type TrainingParamTemplate,
} from "@/features/training/api"
import {
  newTrainingJobPath,
  newTrainingTemplatePath,
  parsePositiveId,
  TRAINING_TEMPLATES_PATH,
} from "@/features/training/paths"
import {
  buildTemplateDefaultParams,
  buildTemplateParamsPayload,
  countModifiedParams,
  formatCompactParamValue,
  partitionTemplatesBySource,
  splitSpecsByTier,
  suggestedTemplateCopyName,
  validateTrainingParams,
} from "@/features/training/training-params"
import { TrainingParamsEditor } from "@/features/training/training-params-editor"
import { usePageTitle } from "@/hooks/use-page-title"
import { ApiError } from "@/lib/api"

const BLANK_PREFILL_VALUE = "__blank__"

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
  const templatesListQuery = useQuery({
    ...trainingTemplatesQuery(),
    enabled: !isEditRoute,
  })

  usePageTitle(
    isEditRoute
      ? editQuery.data
        ? editQuery.data.is_builtin
          ? `查看：${editQuery.data.name}`
          : `编辑：${editQuery.data.name}`
        : "参数模板详情"
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
    if (editQuery.data.is_builtin) {
      return (
        <BuiltinTemplateDetailView
          schema={schemaQuery.data}
          template={editQuery.data}
        />
      )
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
      availableTemplates={!isEditRoute ? (templatesListQuery.data?.items ?? []) : []}
    />
  )
}

function BuiltinTemplateDetailView({
  schema,
  template,
}: {
  schema: ParamsSchemaResponse
  template: TrainingParamTemplate
}) {
  const params = useMemo(
    () => buildTemplateParamsPayload(schema.specs, template.params),
    [schema.specs, template.params]
  )
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

  const toggleDefaultMutation = useMutation({
    mutationFn: (nextDefault: boolean) =>
      updateTrainingTemplate(template.id, { is_default: nextDefault }),
    onSuccess: (updated) => {
      cacheTrainingTemplate(updated)
      toast.success(
        updated.is_default
          ? `已将“${updated.name}”设为默认模板`
          : `已取消“${updated.name}”的默认模板标记`,
        {
          description: updated.is_default
            ? "打开“新建训练任务”时将自动加载此模板的参数配置。"
            : "后续新建训练任务将恢复为 Ultralytics 官方默认参数。",
        }
      )
    },
    onError: (error) => {
      toast.error("更新默认模板失败", { description: error.message })
    },
  })

  return (
    <div className="grid min-w-0 gap-6">
      <div className="grid gap-2">
        <Button variant="ghost" size="sm" className="-ml-2 w-fit" asChild>
          <Link to={TRAINING_TEMPLATES_PATH}>
            <ArrowLeft />
            返回参数模板
          </Link>
        </Button>

        <PageHeader
          title={template.name}
          description={template.description || undefined}
          actions={
            <div className="flex flex-wrap items-center gap-2">
              <Badge variant="secondary" className="gap-1 font-normal">
                <ShieldCheck className="size-3.5 text-primary" />
                系统内置 · 只读预设
              </Badge>
              {template.is_default && (
                <Badge className="gap-1 bg-amber-500/15 text-amber-700 hover:bg-amber-500/20 dark:text-amber-400">
                  <Star className="size-3 fill-current" />
                  默认模板
                </Badge>
              )}
            </div>
          }
        />
      </div>

      <Alert>
        <Lock className="size-4" />
        <AlertTitle>系统内置模板不支持直接修改或删除</AlertTitle>
        <AlertDescription>
          你可以在下方搜索、展开高级选项或查看完整 args.yaml；如需定制修改，请点击“复制为自定义模板”，或直接“用此模板训练”并在任务表单中临时微调。
        </AlertDescription>
      </Alert>

      {/* 1. 基本信息与默认开关 */}
      <Card>
        <CardHeader>
          <CardTitle>1. 模板基本信息</CardTitle>
          <CardDescription>
            系统内置模板的名称与说明保持统一标准，但可随时设为全局默认模板。
          </CardDescription>
        </CardHeader>
        <CardContent className="grid gap-4">
          <div className="grid grid-cols-1 gap-4 md:grid-cols-2">
            <div className="grid content-start gap-1.5">
              <span className="text-xs font-medium text-muted-foreground">
                模板名称
              </span>
              <p className="rounded-md border bg-muted/25 px-3 py-2 text-sm font-medium">
                {template.name}
              </p>
            </div>
            <div className="grid content-start gap-1.5">
              <span className="text-xs font-medium text-muted-foreground">
                适用场景说明
              </span>
              <p className="rounded-md border bg-muted/25 px-3 py-2 text-sm leading-relaxed text-muted-foreground">
                {template.description || "暂无说明"}
              </p>
            </div>
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
              checked={template.is_default}
              disabled={toggleDefaultMutation.isPending}
              onCheckedChange={(checked) =>
                toggleDefaultMutation.mutate(checked)
              }
            />
          </label>
        </CardContent>
      </Card>

      {/* 2. 只读参数预览 */}
      <Card>
        <CardContent className="pt-6">
          <TrainingParamsEditor
            schema={schema}
            values={params}
            onChange={() => {}}
            mode="template"
            readOnly
          />
        </CardContent>
      </Card>

      {/* 3. 底部操作栏 */}
      <div className="sticky bottom-3 z-10 flex flex-wrap items-center justify-between gap-3 rounded-xl border bg-card/95 px-4 py-3 shadow-md backdrop-blur">
        <div className="flex flex-wrap items-center gap-2 text-xs font-medium text-foreground">
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
                相对默认调整 {totalModified} 项（常用 {commonModified} / 高级{" "}
                {advancedModified}）
              </span>
            </>
          )}
        </div>

        <div className="flex flex-wrap items-center gap-2">
          <Button type="button" variant="outline" asChild>
            <Link to={TRAINING_TEMPLATES_PATH}>返回列表</Link>
          </Button>
          <Button type="button" variant="secondary" asChild>
            <Link to={newTrainingTemplatePath(template.id)}>
              <Copy />
              复制为自定义模板
            </Link>
          </Button>
          <Button type="button" asChild>
            <Link to={newTrainingJobPath({ templateId: template.id })}>
              <Rocket />
              用此模板训练
            </Link>
          </Button>
        </div>
      </div>
    </div>
  )
}

function TrainingTemplateForm({
  schema,
  editingTemplate,
  fromTemplate,
  fromMissing,
  availableTemplates,
}: {
  schema: ParamsSchemaResponse
  editingTemplate: TrainingParamTemplate | null
  fromTemplate: TrainingParamTemplate | null
  fromMissing: boolean
  availableTemplates: TrainingParamTemplate[]
}) {
  const navigate = useNavigate()
  const nameId = useId()
  const descId = useId()
  const prefillSelectId = useId()

  const isEdit = editingTemplate !== null
  const seed = editingTemplate ?? fromTemplate

  const [name, setName] = useState(() => {
    if (editingTemplate) {
      return editingTemplate.name
    }
    if (fromTemplate) {
      return suggestedTemplateCopyName(fromTemplate)
    }
    return ""
  })
  const [description, setDescription] = useState(() => seed?.description ?? "")
  const [isDefault, setIsDefault] = useState(() =>
    editingTemplate ? editingTemplate.is_default : false
  )
  const [prefillSourceId, setPrefillSourceId] = useState<number | null>(() =>
    !isEdit && fromTemplate ? fromTemplate.id : null
  )
  const [params, setParams] = useState<Record<string, unknown>>(() =>
    seed
      ? buildTemplateParamsPayload(schema.specs, seed.params)
      : buildTemplateDefaultParams(schema.specs)
  )
  const [formError, setFormError] = useState<string | null>(null)

  const { custom: customTemplates, builtin: builtinTemplates } = useMemo(
    () => partitionTemplatesBySource(availableTemplates),
    [availableTemplates]
  )
  const prefillTemplate = useMemo(
    () =>
      prefillSourceId !== null
        ? (availableTemplates.find((t) => t.id === prefillSourceId) ??
          (fromTemplate?.id === prefillSourceId ? fromTemplate : null))
        : null,
    [availableTemplates, prefillSourceId, fromTemplate]
  )

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

  const handlePrefillSelect = (rawValue: string) => {
    setFormError(null)
    if (rawValue === BLANK_PREFILL_VALUE) {
      setPrefillSourceId(null)
      setParams(buildTemplateDefaultParams(schema.specs))
      toast.info("已重置为 Ultralytics 官方默认参数")
      return
    }
    const id = Number(rawValue)
    const matched = availableTemplates.find((t) => t.id === id)
    if (!matched) {
      return
    }
    setPrefillSourceId(matched.id)
    setParams(buildTemplateParamsPayload(schema.specs, matched.params))
    toast.success(`已从“${matched.name}”预填全部参数`, {
      description: "可在下方按需微调任意常用与高级参数，然后保存为新模板。",
    })
  }

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

  const prefillHeaderSlot =
    !isEdit && availableTemplates.length > 0 ? (
      <div className="grid gap-2 rounded-xl border bg-muted/25 p-3.5">
        <div className="flex flex-wrap items-center gap-2.5">
          <label
            htmlFor={prefillSelectId}
            className="flex shrink-0 items-center gap-1.5 text-sm font-medium"
          >
            <Bookmark className="size-4 text-primary" />
            从模板快速预填
          </label>

          <Select
            value={
              prefillTemplate ? String(prefillTemplate.id) : BLANK_PREFILL_VALUE
            }
            disabled={saveMutation.isPending}
            onValueChange={handlePrefillSelect}
          >
            <SelectTrigger
              id={prefillSelectId}
              className="w-full bg-background sm:w-88"
              aria-label="选择预填模板"
            >
              <SelectValue placeholder="从官方默认或已有模板起步" />
            </SelectTrigger>
            <SelectContent>
              {customTemplates.length > 0 && (
                <SelectGroup>
                  <SelectLabel>自定义模板</SelectLabel>
                  {customTemplates.map((tpl) => (
                    <SelectItem key={tpl.id} value={String(tpl.id)}>
                      {tpl.is_default ? `★ ${tpl.name}（默认）` : tpl.name}
                    </SelectItem>
                  ))}
                </SelectGroup>
              )}
              {customTemplates.length > 0 && builtinTemplates.length > 0 && (
                <SelectSeparator />
              )}
              {builtinTemplates.length > 0 && (
                <SelectGroup>
                  <SelectLabel>系统内置模板</SelectLabel>
                  {builtinTemplates.map((tpl) => (
                    <SelectItem key={tpl.id} value={String(tpl.id)}>
                      {tpl.is_default ? `★ ${tpl.name}（默认）` : tpl.name}
                    </SelectItem>
                  ))}
                </SelectGroup>
              )}
              <SelectSeparator />
              <SelectItem value={BLANK_PREFILL_VALUE}>
                从空白起步（Ultralytics 官方默认）
              </SelectItem>
            </SelectContent>
          </Select>
        </div>

        <p className="text-xs text-muted-foreground">
          {prefillTemplate
            ? `已以${prefillTemplate.is_builtin ? "系统内置模板" : "模板"}“${prefillTemplate.name}”的参数为起点预填；微调下方参数后即可保存为新的自定义模板。`
            : "可直接基于系统内置预设或已有模板一键预填参数，再按产线需求微调保存。"}
        </p>
      </div>
    ) : undefined

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
              : "配置一组可复用的 Ultralytics YOLO 训练参数（可从空白或系统内置预设起步），保存后即可在“新建训练任务”中一键加载。"
          }
        />
      </div>

      {fromTemplate && (
        <Alert>
          <Sparkles className="size-4" />
          <AlertTitle>
            已从{fromTemplate.is_builtin ? "系统内置模板" : "模板"}“
            {fromTemplate.name}”复制全部参数
          </AlertTitle>
          <AlertDescription>
            已预填原模板的参数设置，修改名称或参数后即可保存为新的自定义模板。
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
            headerSlot={prefillHeaderSlot}
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
