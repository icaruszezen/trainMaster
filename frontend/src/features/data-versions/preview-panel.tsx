/**
 * “生成数据版本”页面的实时预览：划分后的图片数、各类别的分布与警告、与上一个
 * 数据版本的对比、不会纳入的图片。桌面宽度在右侧一栏（随页面吸顶），手机宽度
 * 在表单下方，另有吸底的概况栏（见 PreviewBar）。
 */
import type { ReactNode } from "react"
import {
  CircleAlert,
  GitCompareArrows,
  Info,
  RefreshCw,
  TriangleAlert,
} from "lucide-react"

import { Button } from "@/components/ui/button"
import {
  Card,
  CardAction,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"
import { Skeleton } from "@/components/ui/skeleton"
import { Spinner } from "@/components/ui/spinner"
import {
  SPLITS,
  type DataVersionPreview,
  type Split,
} from "@/features/data-versions/api"
import { ClassDistribution } from "@/features/data-versions/class-distribution"
import type { PreviewWarning } from "@/features/data-versions/form-logic"
import {
  compareText,
  SKIPPED_HINTS,
  skippedEntries,
  SPLIT_SET_LABELS,
  splitSummary,
} from "@/features/data-versions/format"
import { useMediaQuery } from "@/hooks/use-media-query"
import { formatNumber, formatPercent, percentOf } from "@/lib/format"
import { cn } from "@/lib/utils"

export type PreviewState =
  | { kind: "invalid"; reasons: string[] }
  | { kind: "loading" }
  | { kind: "error"; error: Error; retrying: boolean; onRetry: () => void }
  | { kind: "ready"; preview: DataVersionPreview; stale: boolean }

type PreviewPanelProps = {
  id?: string
  state: PreviewState
  /** 上一次成功的预览（设置无效或出错时仍然显示，标为过期）。 */
  last: DataVersionPreview | undefined
  testPercent: number
  warnings: readonly PreviewWarning[]
  /** 放在面板底部（桌面宽度的“生成”按钮）。 */
  footer?: ReactNode
  className?: string
}

export function PreviewPanel({
  id,
  state,
  last,
  testPercent,
  warnings,
  footer,
  className,
}: PreviewPanelProps) {
  const preview = state.kind === "ready" ? state.preview : last
  const stale = state.kind !== "ready" || state.stale

  return (
    <Card id={id} className={cn("gap-4 py-4", className)} aria-live="polite">
      <CardHeader className="px-4">
        <CardTitle>预览</CardTitle>
        <CardDescription>
          按数据集的索引统计；生成时会逐一读取文件，数量可能略有不同。
        </CardDescription>
        {state.kind === "ready" && state.stale && (
          <CardAction>
            <Spinner className="size-4" aria-label="正在更新预览" />
          </CardAction>
        )}
      </CardHeader>
      <CardContent className="grid gap-4 px-4">
        {state.kind === "invalid" && (
          <Notice tone="muted">
            {state.reasons.length > 0
              ? `完善设置后显示预览：${state.reasons.join("；")}`
              : "完善设置后显示预览。"}
          </Notice>
        )}
        {state.kind === "error" && (
          <div
            role="alert"
            className="flex flex-wrap items-center gap-x-2 gap-y-1 text-xs text-destructive"
          >
            <span className="min-w-0 break-words">
              无法计算预览：{state.error.message}
            </span>
            <Button
              type="button"
              variant="outline"
              size="xs"
              className="text-foreground"
              onClick={state.onRetry}
              disabled={state.retrying}
            >
              <RefreshCw className={cn(state.retrying && "animate-spin")} />
              {state.retrying ? "正在重试…" : "重试"}
            </Button>
          </div>
        )}
        {preview ? (
          <div
            className={cn(
              "grid min-w-0 gap-4 transition-opacity",
              stale && "opacity-60"
            )}
          >
            <PreviewContent
              preview={preview}
              testPercent={testPercent}
              warnings={state.kind === "ready" ? warnings : []}
            />
          </div>
        ) : (
          state.kind === "loading" && <PreviewSkeleton />
        )}
        {footer}
      </CardContent>
    </Card>
  )
}

function PreviewContent({
  preview,
  testPercent,
  warnings,
}: {
  preview: DataVersionPreview
  testPercent: number
  warnings: readonly PreviewWarning[]
}) {
  // 手机宽度只显示各划分的目标数，放不下图片数。
  const wide = useMediaQuery("(min-width: 40rem)")
  const showTest = testPercent > 0 || preview.splits.test.images > 0
  const splits: Split[] = showTest ? [...SPLITS] : ["train", "val"]
  const skipped = skippedEntries(preview.skipped)

  return (
    <>
      <p className="text-sm">
        将纳入{" "}
        <span className="font-semibold tabular-nums">
          {formatNumber(preview.image_count)}
        </span>{" "}
        张图片、
        <span className="tabular-nums">
          {formatNumber(preview.object_count)}
        </span>{" "}
        个目标
        {preview.augmented_count > 0 && (
          <>
            ，另生成约{" "}
            <span className="tabular-nums">
              {formatNumber(preview.augmented_count)}
            </span>{" "}
            张增强图片（训练集）
          </>
        )}
        。
      </p>

      <ul className="grid gap-2.5" aria-label="划分">
        {splits.map((split) => {
          const counts = preview.splits[split]
          return (
            <li key={split} className="grid gap-1">
              <div className="flex items-baseline justify-between gap-2 text-sm">
                <span>{SPLIT_SET_LABELS[split]}</span>
                <span className="font-medium tabular-nums">
                  {formatNumber(counts.images)} 张
                  <span className="ml-1 text-xs font-normal text-muted-foreground">
                    {formatPercent(
                      percentOf(counts.images, preview.image_count)
                    )}
                  </span>
                </span>
              </div>
              <div
                className="h-1.5 overflow-hidden rounded-full bg-muted"
                aria-hidden
              >
                <div
                  className={cn(
                    "h-full rounded-full",
                    split === "train"
                      ? "bg-primary"
                      : split === "val"
                        ? "bg-primary/60"
                        : "bg-primary/35"
                  )}
                  style={{
                    width: `${percentOf(counts.images, preview.image_count)}%`,
                  }}
                />
              </div>
              <p className="text-xs text-muted-foreground tabular-nums">
                {formatNumber(counts.objects)} 个目标 · 负样本{" "}
                {formatNumber(counts.negatives)} 张
                {counts.by_folder > 0 &&
                  ` · 按文件夹 ${formatNumber(counts.by_folder)} 张`}
              </p>
            </li>
          )
        })}
      </ul>

      {warnings.length > 0 && <WarningList warnings={warnings} />}

      {preview.labels.length > 0 && (
        <ClassDistribution
          labels={preview.labels}
          showTest={showTest}
          showImages={wide}
        />
      )}

      {preview.compare && (
        <Notice tone="muted" icon={GitCompareArrows}>
          {compareText(preview.compare, { annotations: false })}
        </Notice>
      )}

      {(skipped.length > 0 ||
        preview.dropped_objects > 0 ||
        preview.degenerate_objects > 0) && (
        <div className="grid gap-1.5">
          <h3 className="text-xs font-medium text-muted-foreground">
            不会纳入
          </h3>
          <ul className="grid gap-1 text-xs">
            {skipped.map((entry) => (
              <li
                key={entry.key}
                className="flex items-baseline justify-between gap-2"
                title={SKIPPED_HINTS[entry.key]}
              >
                <span className="min-w-0">{entry.label}</span>
                <span className="shrink-0 tabular-nums">
                  {formatNumber(entry.count)} 张
                </span>
              </li>
            ))}
            {preview.dropped_objects > 0 && (
              <li className="flex items-baseline justify-between gap-2">
                <span className="min-w-0">忽略的标签的框</span>
                <span className="shrink-0 tabular-nums">
                  {formatNumber(preview.dropped_objects)} 个
                </span>
              </li>
            )}
            {preview.degenerate_objects > 0 && (
              <li className="flex items-baseline justify-between gap-2">
                <span className="min-w-0">超出图片、没有面积的框</span>
                <span className="shrink-0 tabular-nums">
                  {formatNumber(preview.degenerate_objects)} 个
                </span>
              </li>
            )}
          </ul>
        </div>
      )}
    </>
  )
}

export function WarningList({
  warnings,
}: {
  warnings: readonly PreviewWarning[]
}) {
  return (
    <ul className="grid gap-1.5" aria-label="警告">
      {warnings.map((warning) => (
        <li
          key={warning.key}
          className={cn(
            "flex items-start gap-1.5 text-xs break-words",
            warning.tone === "error"
              ? "text-destructive"
              : "text-amber-700 dark:text-amber-500"
          )}
        >
          {warning.tone === "error" ? (
            <CircleAlert className="mt-px size-3.5 shrink-0" />
          ) : (
            <TriangleAlert className="mt-px size-3.5 shrink-0" />
          )}
          <span className="min-w-0">{warning.text}</span>
        </li>
      ))}
    </ul>
  )
}

function Notice({
  children,
  tone,
  icon: Icon = Info,
}: {
  children: ReactNode
  tone: "muted"
  icon?: typeof Info
}) {
  return (
    <p
      className={cn(
        "flex items-start gap-1.5 text-xs text-pretty",
        tone === "muted" && "text-muted-foreground"
      )}
    >
      <Icon className="mt-px size-3.5 shrink-0" />
      <span className="min-w-0">{children}</span>
    </p>
  )
}

function PreviewSkeleton() {
  return (
    <div className="grid gap-3" aria-busy="true" aria-label="正在计算预览">
      <Skeleton className="h-4 w-3/4" />
      <Skeleton className="h-8 w-full" />
      <Skeleton className="h-8 w-full" />
      <Skeleton className="h-24 w-full" />
    </div>
  )
}

/**
 * 手机与平板宽度：吸在页面底部的概况栏，显示划分后的图片数、警告数与
 * “生成”按钮（提交 `form`）。
 */
export function PreviewBar({
  preview,
  warnings,
  pending,
  formId,
  onShowPreview,
  className,
}: {
  preview: DataVersionPreview | undefined
  warnings: readonly PreviewWarning[]
  pending: boolean
  formId: string
  onShowPreview: () => void
  className?: string
}) {
  const errors = warnings.filter((w) => w.tone === "error").length
  return (
    <div
      className={cn(
        "sticky bottom-0 z-10 -mx-4 -mb-4 border-t bg-background/95 px-4 py-3 backdrop-blur supports-[backdrop-filter]:bg-background/80 md:-mx-6 md:-mb-6 md:px-6",
        className
      )}
    >
      <div className="flex items-center gap-3">
        <button
          type="button"
          onClick={onShowPreview}
          className="grid min-w-0 flex-1 gap-0.5 rounded-md text-left outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50"
          aria-label="查看预览"
        >
          <span className="truncate text-sm font-medium tabular-nums">
            {preview
              ? splitSummary({
                  train: preview.splits.train.images,
                  val: preview.splits.val.images,
                  test: preview.splits.test.images,
                })
              : "完善设置后显示预览"}
          </span>
          <span
            className={cn(
              "truncate text-xs tabular-nums",
              errors > 0
                ? "text-destructive"
                : warnings.length > 0
                  ? "text-amber-700 dark:text-amber-500"
                  : "text-muted-foreground"
            )}
          >
            {warnings.length > 0
              ? `${formatNumber(warnings.length)} 个警告 · 查看预览`
              : "没有警告 · 查看预览"}
          </span>
        </button>
        <Button type="submit" form={formId} disabled={pending}>
          {pending && <Spinner aria-hidden />}
          生成
        </Button>
      </div>
    </div>
  )
}
