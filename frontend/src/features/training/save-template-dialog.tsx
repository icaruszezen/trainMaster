/**
 * “存为参数模板”通用弹窗：
 * 用于从新建训练任务页或已有训练任务详情页将当前 YOLO 参数快速保存为可复用模板。
 */
import { useId, useMemo, useState, type FormEvent } from "react"
import { useMutation } from "@tanstack/react-query"
import { BookmarkPlus, Star } from "lucide-react"
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
import { Field, FieldLabel } from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import { Spinner } from "@/components/ui/spinner"
import { Switch } from "@/components/ui/switch"
import { Textarea } from "@/components/ui/textarea"
import {
  cacheTrainingTemplate,
  createTrainingTemplate,
  type InitMode,
  type ParamsSchemaResponse,
  type TrainingParamTemplate,
} from "@/features/training/api"
import {
  buildTemplateParamsPayload,
  formatCompactParamValue,
  listModifiedParams,
  validateTrainingParams,
} from "@/features/training/training-params"

const MAX_PREVIEW_CHIPS = 10

export type SaveTemplateDialogProps = {
  open: boolean
  onOpenChange: (open: boolean) => void
  schema: ParamsSchemaResponse
  params: Record<string, unknown>
  /** 来源训练任务的初始化模式（用于剥离 scratch 模式下自动联动的 pretrained=false）。 */
  initMode?: InitMode
  defaultName?: string
  defaultDescription?: string
  title?: string
  description?: string
  onSaved?: (template: TrainingParamTemplate) => void
}

export function SaveTemplateDialog({
  open,
  onOpenChange,
  schema,
  params,
  initMode,
  defaultName = "",
  defaultDescription = "",
  title = "保存为参数模板",
  description = "保存当前 YOLO 训练参数配置（含高级选项），之后发起训练时可一键加载并按需微调。",
  onSaved,
}: SaveTemplateDialogProps) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-lg">
        {open && (
          <SaveTemplateForm
            schema={schema}
            params={params}
            initMode={initMode}
            defaultName={defaultName}
            defaultDescription={defaultDescription}
            title={title}
            description={description}
            onSaved={onSaved}
            onClose={() => onOpenChange(false)}
          />
        )}
      </DialogContent>
    </Dialog>
  )
}

function SaveTemplateForm({
  schema,
  params,
  initMode,
  defaultName,
  defaultDescription,
  title,
  description,
  onSaved,
  onClose,
}: {
  schema: ParamsSchemaResponse
  params: Record<string, unknown>
  initMode?: InitMode
  defaultName: string
  defaultDescription: string
  title: string
  description: string
  onSaved?: (template: TrainingParamTemplate) => void
  onClose: () => void
}) {
  const nameId = useId()
  const descId = useId()

  const [name, setName] = useState(defaultName)
  const [note, setNote] = useState(defaultDescription)
  const [isDefault, setIsDefault] = useState(false)
  const [validationError, setValidationError] = useState<string | null>(null)

  const payload = useMemo(
    () => buildTemplateParamsPayload(schema.specs, params, initMode),
    [schema.specs, params, initMode]
  )
  const modifiedItems = useMemo(
    () => listModifiedParams(schema.specs, payload),
    [schema.specs, payload]
  )
  const modifiedCommonCount = useMemo(
    () => modifiedItems.filter((item) => item.spec.common).length,
    [modifiedItems]
  )
  const modifiedAdvancedCount = modifiedItems.length - modifiedCommonCount

  const mutation = useMutation({
    mutationFn: () =>
      createTrainingTemplate({
        name: name.trim(),
        description: note.trim(),
        params: payload,
        is_default: isDefault,
      }),
    onSuccess: (created) => {
      cacheTrainingTemplate(created)
      toast.success(`已保存参数模板“${created.name}”`, {
        description: created.is_default
          ? "已设为默认模板，后续新建训练任务时将自动加载。"
          : "可在“参数模板”页面管理，或在新建训练任务时快速加载。",
      })
      onSaved?.(created)
      onClose()
    },
  })

  const submit = (event: FormEvent) => {
    event.preventDefault()
    event.stopPropagation()
    setValidationError(null)
    if (mutation.isPending) {
      return
    }
    const trimmed = name.trim()
    if (!trimmed) {
      setValidationError("请输入模板名称")
      return
    }
    const paramErr = validateTrainingParams(schema.specs, payload)
    if (paramErr) {
      setValidationError(`参数 ${paramErr.key}：${paramErr.message}`)
      return
    }
    mutation.mutate()
  }

  const hiddenCount = Math.max(0, modifiedItems.length - MAX_PREVIEW_CHIPS)

  return (
    <form onSubmit={submit} className="grid min-w-0 gap-5" noValidate>
      <DialogHeader>
        <DialogTitle className="flex items-center gap-2">
          <BookmarkPlus className="size-4 text-primary" />
          {title}
        </DialogTitle>
        <DialogDescription>{description}</DialogDescription>
      </DialogHeader>

      <div className="grid gap-4">
        <Field>
          <FieldLabel htmlFor={nameId}>模板名称</FieldLabel>
          <Input
            id={nameId}
            value={name}
            maxLength={120}
            autoFocus
            disabled={mutation.isPending}
            placeholder="如：高分辨率小目标检测（1280 + AdamW）"
            onChange={(event) => {
              setName(event.target.value)
              setValidationError(null)
              mutation.reset()
            }}
          />
        </Field>

        <Field>
          <FieldLabel htmlFor={descId}>模板说明（可选）</FieldLabel>
          <Textarea
            id={descId}
            value={note}
            maxLength={2000}
            disabled={mutation.isPending}
            placeholder="记录适用缺陷类型、推荐显卡显存或关键调参思路…"
            className="min-h-20"
            onChange={(event) => {
              setNote(event.target.value)
              mutation.reset()
            }}
          />
        </Field>

        <label className="flex cursor-pointer items-center justify-between gap-4 rounded-xl border bg-muted/20 p-3">
          <div className="grid gap-0.5">
            <span className="flex items-center gap-1.5 text-sm font-medium">
              <Star className="size-3.5 text-amber-500" />
              设为默认模板
            </span>
            <span className="text-xs text-muted-foreground">
              开启后，打开“新建训练任务”时将自动加载该模板（若已有默认模板会自动替换）。
            </span>
          </div>
          <Switch
            checked={isDefault}
            disabled={mutation.isPending}
            onCheckedChange={setIsDefault}
          />
        </label>

        {/* 待保存参数摘要 */}
        <div className="grid gap-2 rounded-xl border bg-muted/25 p-3 text-xs">
          <div className="flex flex-wrap items-center justify-between gap-2">
            <span className="font-medium text-foreground">
              将保存全部 {schema.specs.length} 项 YOLO 参数
            </span>
            <span className="text-muted-foreground tabular-nums">
              {modifiedItems.length > 0
                ? `自定义 ${modifiedItems.length} 项（常用 ${modifiedCommonCount} · 高级 ${modifiedAdvancedCount}）`
                : "与官方默认一致"}
            </span>
          </div>

          {modifiedItems.length > 0 ? (
            <div className="flex flex-wrap items-center gap-1.5">
              {modifiedItems
                .slice(0, MAX_PREVIEW_CHIPS)
                .map(({ spec, value }) => (
                  <span
                    key={spec.key}
                    className="inline-flex items-center gap-1 rounded-md border bg-background px-1.5 py-0.5 font-mono text-[11px]"
                  >
                    <span className="text-muted-foreground">{spec.key}=</span>
                    <span className="font-semibold text-foreground">
                      {formatCompactParamValue(value)}
                    </span>
                  </span>
                ))}
              {hiddenCount > 0 && (
                <Badge
                  variant="outline"
                  className="h-5 px-1.5 text-[11px] font-normal tabular-nums"
                >
                  +{hiddenCount} 项
                </Badge>
              )}
            </div>
          ) : (
            <p className="text-muted-foreground">
              当前参数均与 Ultralytics 官方默认值相同；
              <code className="rounded bg-muted px-1 font-mono text-[11px]">
                resume
              </code>{" "}
              将统一保持为 false，加载时由训练初始化模式决定。
            </p>
          )}
        </div>
      </div>

      {(validationError || mutation.error) && (
        <p role="alert" className="text-sm break-words text-destructive">
          {validationError ?? mutation.error?.message}
        </p>
      )}

      <DialogFooter>
        <Button
          type="button"
          variant="outline"
          disabled={mutation.isPending}
          onClick={onClose}
        >
          取消
        </Button>
        <Button type="submit" disabled={mutation.isPending}>
          {mutation.isPending ? <Spinner aria-hidden /> : <BookmarkPlus />}
          保存模板
        </Button>
      </DialogFooter>
    </form>
  )
}
