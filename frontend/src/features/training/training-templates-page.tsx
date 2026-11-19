/**
 * 训练参数模板管理页（`/training/templates`）：
 * - 浏览、搜索全部全局 YOLO 参数模板
 * - 展示核心参数与偏离官方默认的高级参数摘要
 * - 支持一键“用此模板训练”、新建、编辑、复制、设为/取消默认模板及删除
 */
import { useMemo, useState } from "react"
import { useMutation, useQuery } from "@tanstack/react-query"
import {
  Copy,
  Ellipsis,
  Pencil,
  Plus,
  Rocket,
  Search,
  SearchX,
  SlidersHorizontal,
  Star,
  StarOff,
  Trash2,
  X,
} from "lucide-react"
import { Link } from "react-router"
import { toast } from "sonner"

import { PageHeader } from "@/components/page-header"
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
import { Card, CardContent, CardFooter, CardHeader } from "@/components/ui/card"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import {
  Empty,
  EmptyContent,
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from "@/components/ui/empty"
import { Input } from "@/components/ui/input"
import { Skeleton } from "@/components/ui/skeleton"
import { Spinner } from "@/components/ui/spinner"
import { LoadError } from "@/features/datasets/load-error"
import {
  cacheTrainingTemplate,
  deleteTrainingTemplate,
  invalidateTrainingTemplates,
  trainingKeys,
  trainingParamsSchemaQuery,
  trainingTemplatesQuery,
  updateTrainingTemplate,
  type ParamsSchemaResponse,
  type TrainingParamTemplate,
} from "@/features/training/api"
import {
  editTrainingTemplatePath,
  newTrainingJobPath,
  newTrainingTemplatePath,
} from "@/features/training/paths"
import {
  formatCompactParamValue,
  isParamModified,
  listModifiedParams,
  splitSpecsByTier,
} from "@/features/training/training-params"
import { formatDateTime, formatNumber, formatRelativeTime } from "@/lib/format"
import { queryClient } from "@/lib/query-client"
import { cn } from "@/lib/utils"

const ANCHOR_KEYS = [
  "epochs",
  "batch",
  "imgsz",
  "optimizer",
  "lr0",
  "mosaic",
] as const

const MAX_ADVANCED_CHIPS = 6

export function TrainingTemplatesPage() {
  const [keyword, setKeyword] = useState("")
  const [deleting, setDeleting] = useState<TrainingParamTemplate | null>(null)

  const templatesQuery = useQuery(trainingTemplatesQuery(keyword))
  const schemaQuery = useQuery(trainingParamsSchemaQuery())

  const toggleDefaultMutation = useMutation({
    mutationFn: ({ id, isDefault }: { id: number; isDefault: boolean }) =>
      updateTrainingTemplate(id, { is_default: isDefault }),
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

  const items = templatesQuery.data?.items ?? []
  const hasFilter = keyword.trim() !== ""

  return (
    <>
      <PageHeader
        title="参数模板"
        description="将常用的 Ultralytics YOLO 训练参数（常用项与高级选项）保存为可复用模板，发起训练时可一键加载再按需微调，也支持指定一个默认模板自动预填。"
        actions={
          <Button asChild>
            <Link to={newTrainingTemplatePath()}>
              <Plus />
              新建参数模板
            </Link>
          </Button>
        }
      />

      <div className="flex flex-wrap items-center gap-3">
        <div className="relative min-w-52 flex-1 sm:max-w-sm">
          <Search className="pointer-events-none absolute top-1/2 left-3 size-4 -translate-y-1/2 text-muted-foreground" />
          <Input
            value={keyword}
            onChange={(event) => setKeyword(event.target.value)}
            placeholder="搜索模板名称或说明…"
            className="pr-8 pl-9"
            aria-label="搜索参数模板"
          />
          {keyword && (
            <Button
              type="button"
              variant="ghost"
              size="icon-xs"
              className="absolute top-1/2 right-1.5 -translate-y-1/2 text-muted-foreground"
              onClick={() => setKeyword("")}
              aria-label="清空搜索"
            >
              <X />
            </Button>
          )}
        </div>

        <p className="flex items-center gap-2 text-sm text-muted-foreground tabular-nums">
          {templatesQuery.isFetching && templatesQuery.data && (
            <Spinner className="size-3.5" aria-hidden />
          )}
          {templatesQuery.data && `共 ${formatNumber(items.length)} 个模板`}
        </p>
      </div>

      {templatesQuery.isPending ? (
        <div
          className="grid grid-cols-1 gap-4 xl:grid-cols-2"
          aria-busy="true"
          aria-label="正在加载"
        >
          {[0, 1, 2, 3].map((idx) => (
            <Skeleton key={idx} className="h-52 rounded-xl" />
          ))}
        </div>
      ) : templatesQuery.data === undefined ? (
        <LoadError
          title="无法获取参数模板列表"
          error={templatesQuery.error}
          retrying={templatesQuery.isFetching}
          onRetry={() => templatesQuery.refetch()}
        />
      ) : items.length === 0 ? (
        hasFilter ? (
          <Empty className="border bg-card">
            <EmptyHeader>
              <EmptyMedia variant="icon">
                <SearchX />
              </EmptyMedia>
              <EmptyTitle className="text-base">
                没有匹配“{keyword.trim()}”的参数模板
              </EmptyTitle>
              <EmptyDescription>
                尝试更换关键词、清空搜索，或直接创建一个新的参数模板。
              </EmptyDescription>
            </EmptyHeader>
            <EmptyContent>
              <div className="flex flex-wrap justify-center gap-2">
                <Button
                  variant="outline"
                  size="sm"
                  onClick={() => setKeyword("")}
                >
                  清空搜索
                </Button>
                <Button size="sm" asChild>
                  <Link to={newTrainingTemplatePath()}>
                    <Plus />
                    新建参数模板
                  </Link>
                </Button>
              </div>
            </EmptyContent>
          </Empty>
        ) : (
          <Empty className="border bg-card">
            <EmptyHeader className="max-w-lg">
              <EmptyMedia variant="icon">
                <SlidersHorizontal />
              </EmptyMedia>
              <EmptyTitle>还没有参数模板</EmptyTitle>
              <EmptyDescription>
                将常用的训练超参数（例如高分辨率小目标配置、强数据增强策略或快速验证预设）保存为模板；之后在“新建训练任务”中一键加载即可微调或直接开跑。
              </EmptyDescription>
            </EmptyHeader>
            <EmptyContent>
              <div className="flex flex-wrap justify-center gap-2">
                <Button asChild>
                  <Link to={newTrainingTemplatePath()}>
                    <Plus />
                    新建参数模板
                  </Link>
                </Button>
                <Button variant="outline" asChild>
                  <Link to={newTrainingJobPath()}>
                    <Rocket />
                    去新建训练任务
                  </Link>
                </Button>
              </div>
            </EmptyContent>
          </Empty>
        )
      ) : (
        <div className="grid grid-cols-1 gap-4 xl:grid-cols-2">
          {items.map((template) => (
            <TemplateCard
              key={template.id}
              template={template}
              schema={schemaQuery.data}
              togglingDefault={
                toggleDefaultMutation.isPending &&
                toggleDefaultMutation.variables?.id === template.id
              }
              onToggleDefault={() =>
                toggleDefaultMutation.mutate({
                  id: template.id,
                  isDefault: !template.is_default,
                })
              }
              onDelete={() => setDeleting(template)}
            />
          ))}
        </div>
      )}

      <DeleteTemplateDialog
        template={deleting}
        onOpenChange={(open) => !open && setDeleting(null)}
      />
    </>
  )
}

function TemplateCard({
  template,
  schema,
  togglingDefault,
  onToggleDefault,
  onDelete,
}: {
  template: TrainingParamTemplate
  schema: ParamsSchemaResponse | undefined
  togglingDefault: boolean
  onToggleDefault: () => void
  onDelete: () => void
}) {
  const summary = useMemo(() => {
    if (!schema) {
      return null
    }
    const { common, advanced } = splitSpecsByTier(schema.specs)
    const specByKey = new Map(schema.specs.map((s) => [s.key, s]))
    const modifiedCommon = listModifiedParams(common, template.params)
    const modifiedAdvanced = listModifiedParams(advanced, template.params)
    const anchors = ANCHOR_KEYS.map((key) => {
      const spec = specByKey.get(key)
      const val =
        key in template.params ? template.params[key] : spec?.default
      const modified = spec ? isParamModified(spec, template.params) : false
      return { key, val, modified }
    })
    const extraModifiedCommon = modifiedCommon.filter(
      ({ spec }) =>
        !(ANCHOR_KEYS as readonly string[]).includes(spec.key)
    )
    return {
      totalModified: modifiedCommon.length + modifiedAdvanced.length,
      modifiedCommonCount: modifiedCommon.length,
      modifiedAdvanced,
      extraModifiedCommon,
      anchors,
    }
  }, [schema, template.params])

  const extraChips = summary
    ? [...summary.extraModifiedCommon, ...summary.modifiedAdvanced]
    : []
  const hiddenChipsCount = Math.max(0, extraChips.length - MAX_ADVANCED_CHIPS)

  return (
    <Card
      className={cn(
        "flex flex-col justify-between gap-0 py-0 transition-colors",
        template.is_default && "border-primary/45 bg-primary/[0.015]"
      )}
    >
      <CardHeader className="flex flex-row items-start justify-between gap-3 px-5 pt-5 pb-3">
        <div className="grid min-w-0 gap-1.5">
          <div className="flex flex-wrap items-center gap-2">
            <Link
              to={editTrainingTemplatePath(template.id)}
              className="truncate text-base font-semibold underline-offset-4 hover:underline"
            >
              {template.name}
            </Link>
            {template.is_default && (
              <Badge className="gap-1 bg-amber-500/15 text-amber-700 hover:bg-amber-500/20 dark:text-amber-400">
                <Star className="size-3 fill-current" />
                默认模板
              </Badge>
            )}
          </div>
          {template.description ? (
            <p className="line-clamp-2 text-xs leading-relaxed text-muted-foreground">
              {template.description}
            </p>
          ) : (
            <p className="text-xs text-muted-foreground/70">暂无模板说明</p>
          )}
        </div>

        <DropdownMenu modal={false}>
          <DropdownMenuTrigger asChild>
            <Button
              variant="ghost"
              size="icon-sm"
              className="-mt-1 -mr-1.5 shrink-0"
              aria-label={`更多操作：${template.name}`}
            >
              <Ellipsis />
            </Button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end" className="w-48">
            <DropdownMenuItem asChild>
              <Link to={editTrainingTemplatePath(template.id)}>
                <Pencil />
                编辑模板
              </Link>
            </DropdownMenuItem>
            <DropdownMenuItem
              disabled={togglingDefault}
              onSelect={onToggleDefault}
            >
              {template.is_default ? <StarOff /> : <Star />}
              {template.is_default ? "取消默认模板" : "设为默认模板"}
            </DropdownMenuItem>
            <DropdownMenuItem asChild>
              <Link to={newTrainingTemplatePath(template.id)}>
                <Copy />
                复制为新模板
              </Link>
            </DropdownMenuItem>
            <DropdownMenuSeparator />
            <DropdownMenuItem variant="destructive" onSelect={onDelete}>
              <Trash2 />
              删除模板
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
      </CardHeader>

      <CardContent className="grid gap-3 px-5 pb-4">
        {/* 核心参数网格 */}
        <div className="grid grid-cols-3 gap-2 rounded-lg border bg-muted/25 p-2.5 sm:grid-cols-6">
          {(summary?.anchors ??
            ANCHOR_KEYS.map((key) => ({
              key,
              val: template.params[key],
              modified: false,
            }))
          ).map(({ key, val, modified }) => (
            <div key={key} className="grid min-w-0 gap-0.5">
              <span className="truncate font-mono text-[10px] text-muted-foreground">
                {key}
              </span>
              <span
                className={cn(
                  "truncate font-mono text-xs tabular-nums",
                  modified
                    ? "font-semibold text-primary"
                    : "font-medium text-foreground"
                )}
                title={`${key}=${formatCompactParamValue(val)}`}
              >
                {formatCompactParamValue(val)}
              </span>
            </div>
          ))}
        </div>

        {/* 自定义项与高级参数摘要 */}
        {summary && (
          <div className="grid gap-1.5 text-xs">
            <div className="flex flex-wrap items-center gap-2 text-muted-foreground tabular-nums">
              <span>
                相对官方默认自定义{" "}
                <strong className="font-medium text-foreground">
                  {summary.totalModified}
                </strong>{" "}
                项
              </span>
              {summary.totalModified > 0 && (
                <>
                  <span>·</span>
                  <span>
                    常用 {summary.modifiedCommonCount} 项 / 高级{" "}
                    {summary.modifiedAdvanced.length} 项
                  </span>
                </>
              )}
            </div>

            {extraChips.length > 0 && (
              <div className="flex flex-wrap items-center gap-1.5">
                {extraChips.slice(0, MAX_ADVANCED_CHIPS).map(({ spec, value }) => (
                  <span
                    key={spec.key}
                    title={spec.label}
                    className={cn(
                      "inline-flex items-center gap-1 rounded-md border px-1.5 py-0.5 font-mono text-[11px]",
                      spec.common
                        ? "bg-background text-foreground"
                        : "border-primary/25 bg-primary/[0.04] text-foreground"
                    )}
                  >
                    <span className="text-muted-foreground">{spec.key}=</span>
                    <span className="font-semibold text-primary">
                      {formatCompactParamValue(value)}
                    </span>
                  </span>
                ))}
                {hiddenChipsCount > 0 && (
                  <Badge
                    variant="outline"
                    className="h-5 px-1.5 text-[11px] font-normal tabular-nums"
                  >
                    +{hiddenChipsCount} 项
                  </Badge>
                )}
              </div>
            )}
          </div>
        )}
      </CardContent>

      <CardFooter className="flex flex-wrap items-center justify-between gap-2 border-t bg-muted/15 px-5 py-3">
        <span
          className="text-xs text-muted-foreground tabular-nums"
          title={formatDateTime(template.updated_at)}
        >
          更新于 {formatRelativeTime(template.updated_at)}
        </span>

        <div className="flex items-center gap-2">
          <Button variant="outline" size="sm" asChild>
            <Link to={editTrainingTemplatePath(template.id)}>
              <Pencil />
              编辑
            </Link>
          </Button>
          <Button size="sm" asChild>
            <Link to={newTrainingJobPath({ templateId: template.id })}>
              <Rocket />
              用此模板训练
            </Link>
          </Button>
        </div>
      </CardFooter>
    </Card>
  )
}

function DeleteTemplateDialog({
  template,
  onOpenChange,
}: {
  template: TrainingParamTemplate | null
  onOpenChange: (open: boolean) => void
}) {
  const mutation = useMutation({
    mutationFn: (target: TrainingParamTemplate) =>
      deleteTrainingTemplate(target.id),
    onSuccess: (_, target) => {
      queryClient.removeQueries({
        queryKey: trainingKeys.templateDetail(target.id),
      })
      invalidateTrainingTemplates()
      toast.success(`已删除参数模板“${target.name}”`)
      onOpenChange(false)
    },
  })

  return (
    <AlertDialog open={template !== null} onOpenChange={onOpenChange}>
      <AlertDialogContent>
        {template && (
          <>
            <AlertDialogHeader>
              <AlertDialogTitle className="break-words">
                删除参数模板“{template.name}”？
              </AlertDialogTitle>
              <AlertDialogDescription>
                删除后该模板将从模板列表中永久移除。历史训练任务已完整锁定当时的参数快照与来源模板名称，不受影响。
                {template.is_default &&
                  " 该模板当前为默认模板，删除后新建训练任务将恢复为官方默认参数。"}
              </AlertDialogDescription>
            </AlertDialogHeader>

            {mutation.error && (
              <p role="alert" className="text-sm break-words text-destructive">
                {mutation.error.message}
              </p>
            )}

            <AlertDialogFooter>
              <AlertDialogCancel disabled={mutation.isPending}>
                取消
              </AlertDialogCancel>
              <Button
                variant="destructive"
                disabled={mutation.isPending}
                onClick={() => mutation.mutate(template)}
              >
                {mutation.isPending && <Spinner aria-hidden />}
                删除模板
              </Button>
            </AlertDialogFooter>
          </>
        )}
      </AlertDialogContent>
    </AlertDialog>
  )
}
