/**
 * 漏检 / 误检样本浏览器 + 交互式检测框叠加检视弹窗 + 同源双模型并排对比检视弹窗。
 *
 * 特性：
 * - 支持按错误类型（漏检/误检/误分类/过杀/漏杀/完美预测）、类别、混淆矩阵下钻条件、搜索词和排序筛选
 * - 缩略图使用后端 `?size=sm` 接口加载
 * - 点击样本打开高精 SVG 框叠加检视器：可单独开关 GT / TP / FP / FN / 误分类 / 置信度标签，
 *   支持缩放平移、键盘 A / D 快速切换上下张、点击列表高亮对应框，以及一键跳转「标注工作台」修正
 */
import {
  useEffect,
  useMemo,
  useRef,
  useState,
  type MouseEvent as ReactMouseEvent,
  type WheelEvent as ReactWheelEvent,
} from "react"
import { useQuery } from "@tanstack/react-query"
import {
  ChevronLeft,
  ChevronRight,
  Download,
  ExternalLink,
  Eye,
  ImageOff,
  PenTool,
  RotateCcw,
  Search,
  X,
  ZoomIn,
  ZoomOut,
} from "lucide-react"
import { Link } from "react-router"

import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Card, CardContent } from "@/components/ui/card"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { Skeleton } from "@/components/ui/skeleton"
import {
  evaluationExportUrl,
  evaluationSampleDetailQuery,
  evaluationSamplesQuery,
  sampleThumbnailUrl,
  type CompareDiffItem,
  type MatchedGTBox,
  type MatchedPredBox,
  type SampleDetail,
  type SampleErrorType,
  type SampleFilters,
  type SampleItem,
  type SampleSort,
} from "@/features/evaluation/api"
import {
  formatThreshold,
  SAMPLE_ERROR_TYPE_LABELS,
  SAMPLE_SORT_LABELS,
} from "@/features/evaluation/format"
import { OutcomeBadge } from "@/features/evaluation/status"
import { startDownload } from "@/lib/download"
import { formatNumber } from "@/lib/format"
import { cn } from "@/lib/utils"

export type LayerVisibility = {
  gt: boolean
  tp: boolean
  fp: boolean
  fn: boolean
  misclassified: boolean
  labels: boolean
}

const DEFAULT_LAYERS: LayerVisibility = {
  gt: true,
  tp: true,
  fp: true,
  fn: true,
  misclassified: true,
  labels: true,
}

const ERROR_FILTER_CHIPS: readonly SampleErrorType[] = [
  "any_error",
  "fn",
  "fp",
  "misclassified",
  "overkill",
  "escape",
  "perfect",
  "all",
]

const PAGE_SIZE = 24

export function SampleBrowser({
  evaluationId,
  conf,
  iou,
  classNames,
  drilldown,
  onClearDrilldown,
}: {
  evaluationId: number
  conf: number
  iou: number
  classNames: readonly string[]
  drilldown: { gtClass: string; predClass: string } | null
  onClearDrilldown: () => void
}) {
  const [errorType, setErrorType] = useState<SampleErrorType>("any_error")
  const [className, setClassName] = useState<string>("")
  const [search, setSearch] = useState("")
  const [debouncedSearch, setDebouncedSearch] = useState("")
  const [sort, setSort] = useState<SampleSort>("errors_desc")
  const [activeSampleId, setActiveSampleId] = useState<number | null>(null)

  useEffect(() => {
    const t = setTimeout(() => {
      setDebouncedSearch(search)
    }, 220)
    return () => clearTimeout(t)
  }, [search])

  // 当下钻条件、阈值或搜索词变化时自动回到第 1 页
  const pageResetKey = `${drilldown?.gtClass ?? ""}|${drilldown?.predClass ?? ""}|${conf}|${iou}|${debouncedSearch}`
  const [pageState, setPageState] = useState<{ key: string; value: number }>({
    key: pageResetKey,
    value: 0,
  })
  const page = pageState.key === pageResetKey ? pageState.value : 0
  const setPage = (next: number | ((prev: number) => number)) => {
    setPageState((prev) => {
      const current = prev.key === pageResetKey ? prev.value : 0
      const value = typeof next === "function" ? next(current) : next
      return { key: pageResetKey, value }
    })
  }

  const filters: SampleFilters = useMemo(
    () => ({
      conf,
      iou,
      errorType: drilldown ? "all" : errorType,
      className: drilldown ? undefined : className || undefined,
      gtClass: drilldown?.gtClass,
      predClass: drilldown?.predClass,
      q: debouncedSearch || undefined,
      sort,
      offset: page * PAGE_SIZE,
      limit: PAGE_SIZE,
    }),
    [conf, iou, drilldown, errorType, className, debouncedSearch, sort, page]
  )

  const query = useQuery(evaluationSamplesQuery(evaluationId, filters))
  const items = useMemo(() => query.data?.items ?? [], [query.data])
  const total = query.data?.total ?? 0
  const totalPages = Math.max(1, Math.ceil(total / PAGE_SIZE))

  const activeIdx = useMemo(
    () =>
      activeSampleId !== null
        ? items.findIndex((it) => it.id === activeSampleId)
        : -1,
    [items, activeSampleId]
  )

  const handleExportSamplesCsv = () => {
    const url = evaluationExportUrl(evaluationId, "samples_csv", {
      conf,
      iou,
      sampleFilters: filters,
    })
    startDownload(url, `evaluation-${evaluationId}-samples.csv`)
  }

  return (
    <div className="grid gap-4">
      {/* 筛选控制条 */}
      <Card className="py-3.5">
        <CardContent className="grid gap-3 px-4">
          {drilldown && (
            <div className="flex flex-wrap items-center justify-between gap-2 rounded-lg border border-primary/35 bg-primary/5 px-3 py-2 text-xs">
              <span>
                已从混淆矩阵下钻筛选：真实{" "}
                <strong className="font-semibold text-foreground">
                  {drilldown.gtClass === "background"
                    ? "背景 (无框)"
                    : drilldown.gtClass}
                </strong>{" "}
                → 预测{" "}
                <strong className="font-semibold text-foreground">
                  {drilldown.predClass === "background"
                    ? "背景 (漏检)"
                    : drilldown.predClass}
                </strong>
              </span>
              <Button
                type="button"
                variant="ghost"
                size="xs"
                onClick={onClearDrilldown}
              >
                <X className="size-3.5" />
                清除下钻条件
              </Button>
            </div>
          )}

          {/* 错误类型快速筛选 Chip */}
          {!drilldown && (
            <div className="flex flex-wrap items-center gap-1.5">
              {ERROR_FILTER_CHIPS.map((type) => {
                const active = errorType === type
                return (
                  <button
                    key={type}
                    type="button"
                    onClick={() => {
                      setErrorType(type)
                      setPage(0)
                    }}
                    className={cn(
                      "rounded-full border px-3 py-1 text-xs font-medium transition-colors",
                      active
                        ? "border-primary bg-primary text-primary-foreground"
                        : "bg-background text-muted-foreground hover:bg-muted hover:text-foreground"
                    )}
                  >
                    {SAMPLE_ERROR_TYPE_LABELS[type]}
                  </button>
                )
              })}
            </div>
          )}

          {/* 类别、搜索、排序与导出 */}
          <div className="flex flex-wrap items-center gap-2.5">
            {!drilldown && classNames.length > 0 && (
              <Select
                value={className || "__all__"}
                onValueChange={(v) => {
                  setClassName(v === "__all__" ? "" : v)
                  setPage(0)
                }}
              >
                <SelectTrigger className="h-8 w-40 text-xs">
                  <SelectValue placeholder="按类别筛选" />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="__all__">全部类别</SelectItem>
                  {classNames.map((name) => (
                    <SelectItem key={name} value={name}>
                      {name}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            )}

            <div className="relative min-w-44 flex-1 sm:max-w-64">
              <Search className="pointer-events-none absolute top-1/2 left-2.5 size-3.5 -translate-y-1/2 text-muted-foreground" />
              <Input
                value={search}
                onChange={(e) => setSearch(e.target.value)}
                placeholder="搜索文件名或路径…"
                className="h-8 pl-8 text-xs"
              />
              {search && (
                <button
                  type="button"
                  onClick={() => setSearch("")}
                  className="absolute top-1/2 right-2 -translate-y-1/2 text-muted-foreground hover:text-foreground"
                  aria-label="清除搜索"
                >
                  <X className="size-3.5" />
                </button>
              )}
            </div>

            <Select
              value={sort}
              onValueChange={(v) => {
                setSort(v as SampleSort)
                setPage(0)
              }}
            >
              <SelectTrigger className="h-8 w-44 text-xs">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {(Object.keys(SAMPLE_SORT_LABELS) as SampleSort[]).map(
                  (key) => (
                    <SelectItem key={key} value={key}>
                      {SAMPLE_SORT_LABELS[key]}
                    </SelectItem>
                  )
                )}
              </SelectContent>
            </Select>

            <div className="ml-auto flex items-center gap-2">
              <span className="text-xs text-muted-foreground tabular-nums">
                共 {formatNumber(total)} 张
              </span>
              <Button
                type="button"
                variant="outline"
                size="sm"
                className="h-8 text-xs"
                onClick={handleExportSamplesCsv}
                disabled={total === 0}
              >
                <Download className="size-3.5" />
                导出筛选 CSV
              </Button>
            </div>
          </div>
        </CardContent>
      </Card>

      {/* 样本缩略图网格 */}
      {query.isPending ? (
        <div className="grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-4 xl:grid-cols-6">
          {Array.from({ length: 12 }).map((_, i) => (
            <Skeleton key={i} className="aspect-[4/3] rounded-xl" />
          ))}
        </div>
      ) : items.length === 0 ? (
        <div className="rounded-xl border border-dashed py-14 text-center text-sm text-muted-foreground">
          当前筛选条件（Conf {formatThreshold(conf)} · IoU {formatThreshold(iou)}）下没有符合条件的样本。
        </div>
      ) : (
        <div className="grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-4 xl:grid-cols-6">
          {items.map((sample) => (
            <SampleThumbCard
              key={sample.id}
              sample={sample}
              onClick={() => setActiveSampleId(sample.id)}
            />
          ))}
        </div>
      )}

      {/* 分页条 */}
      {total > PAGE_SIZE && (
        <div className="flex items-center justify-between gap-2 text-xs text-muted-foreground">
          <span className="tabular-nums">
            第 {page + 1} / {totalPages} 页（显示 {page * PAGE_SIZE + 1}–
            {Math.min((page + 1) * PAGE_SIZE, total)}，共 {formatNumber(total)} 张）
          </span>
          <div className="flex items-center gap-1.5">
            <Button
              type="button"
              variant="outline"
              size="sm"
              className="h-7 text-xs"
              disabled={page <= 0}
              onClick={() => setPage((p) => Math.max(0, p - 1))}
            >
              <ChevronLeft className="size-3.5" />
              上一页
            </Button>
            <Button
              type="button"
              variant="outline"
              size="sm"
              className="h-7 text-xs"
              disabled={page + 1 >= totalPages}
              onClick={() => setPage((p) => Math.min(totalPages - 1, p + 1))}
            >
              下一页
              <ChevronRight className="size-3.5" />
            </Button>
          </div>
        </div>
      )}

      {/* 单样本检测框叠加检视弹窗 */}
      <SampleDetailModal
        evaluationId={evaluationId}
        sampleId={activeSampleId}
        conf={conf}
        iou={iou}
        hasPrev={activeIdx > 0}
        hasNext={activeIdx >= 0 && activeIdx < items.length - 1}
        positionLabel={
          activeIdx >= 0 ? `${activeIdx + 1} / ${items.length}` : undefined
        }
        onPrev={() => {
          if (activeIdx > 0) {
            setActiveSampleId(items[activeIdx - 1].id)
          }
        }}
        onNext={() => {
          if (activeIdx >= 0 && activeIdx < items.length - 1) {
            setActiveSampleId(items[activeIdx + 1].id)
          }
        }}
        onClose={() => setActiveSampleId(null)}
      />
    </div>
  )
}

function SampleThumbCard({
  sample,
  onClick,
}: {
  sample: SampleItem
  onClick: () => void
}) {
  const [imgErr, setImgErr] = useState(false)
  const thumbSrc = sampleThumbnailUrl(sample, "sm")

  return (
    <button
      type="button"
      onClick={onClick}
      className="group flex flex-col overflow-hidden rounded-xl border bg-card text-left transition-all hover:border-primary/60 hover:shadow-md focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none"
    >
      <div className="relative aspect-[4/3] w-full overflow-hidden bg-muted/60">
        {!imgErr && thumbSrc ? (
          <img
            src={thumbSrc}
            alt={sample.file_name}
            loading="lazy"
            onError={() => setImgErr(true)}
            className="size-full object-cover transition-transform duration-200 group-hover:scale-[1.03]"
          />
        ) : (
          <div className="flex size-full flex-col items-center justify-center gap-1 text-xs text-muted-foreground">
            <ImageOff className="size-5 opacity-60" />
            <span>原图不可用</span>
          </div>
        )}
        <div className="absolute top-1.5 left-1.5">
          <OutcomeBadge
            outcome={sample.image_outcome}
            className="h-5 bg-background/90 px-1.5 text-[10px] shadow-xs backdrop-blur-xs"
          />
        </div>
        {sample.split && (
          <span className="absolute top-1.5 right-1.5 rounded bg-black/60 px-1.5 py-0.5 text-[10px] font-medium text-white uppercase">
            {sample.split}
          </span>
        )}
        <div className="absolute inset-x-0 bottom-0 flex items-center justify-end gap-1 bg-gradient-to-t from-black/75 via-black/30 to-transparent p-1.5 opacity-0 transition-opacity group-hover:opacity-100">
          <span className="inline-flex items-center gap-1 rounded bg-white/90 px-1.5 py-0.5 text-[10px] font-medium text-zinc-900">
            <Eye className="size-3" />
            查看检测框
          </span>
        </div>
      </div>

      <div className="grid gap-1.5 p-2.5">
        <div
          className="truncate text-xs font-medium text-foreground"
          title={sample.path}
        >
          {sample.file_name}
        </div>
        <div className="flex flex-wrap items-center gap-1 text-[10px] tabular-nums">
          {sample.fn_count > 0 && (
            <span className="rounded bg-amber-500/15 px-1.5 py-0.5 font-medium text-amber-800 dark:text-amber-400">
              漏检 {sample.fn_count}
            </span>
          )}
          {sample.fp_count > 0 && (
            <span className="rounded bg-rose-500/15 px-1.5 py-0.5 font-medium text-rose-700 dark:text-rose-400">
              误检 {sample.fp_count}
            </span>
          )}
          {sample.misclassified_count > 0 && (
            <span className="rounded bg-violet-500/15 px-1.5 py-0.5 font-medium text-violet-700 dark:text-violet-400">
              误分类 {sample.misclassified_count}
            </span>
          )}
          {sample.fn_count === 0 &&
            sample.fp_count === 0 &&
            sample.misclassified_count === 0 && (
              <span className="rounded bg-emerald-500/15 px-1.5 py-0.5 font-medium text-emerald-700 dark:text-emerald-400">
                TP {sample.tp_count} · 无错误
              </span>
            )}
        </div>
      </div>
    </button>
  )
}

export function SampleDetailModal({
  evaluationId,
  sampleId,
  conf,
  iou,
  hasPrev,
  hasNext,
  positionLabel,
  onPrev,
  onNext,
  onClose,
}: {
  evaluationId: number
  sampleId: number | null
  conf: number
  iou: number
  hasPrev: boolean
  hasNext: boolean
  positionLabel?: string
  onPrev: () => void
  onNext: () => void
  onClose: () => void
}) {
  const [layers, setLayers] = useState<LayerVisibility>(DEFAULT_LAYERS)
  const [highlightKey, setHighlightKey] = useState<string | null>(null)

  const query = useQuery({
    ...evaluationSampleDetailQuery(evaluationId, sampleId ?? 0, conf, iou),
    enabled: sampleId !== null,
  })

  // 键盘 A / D / 左右方向键切换上下张
  useEffect(() => {
    if (sampleId === null) {
      return
    }
    const onKeyDown = (e: KeyboardEvent) => {
      if (
        e.target instanceof HTMLInputElement ||
        e.target instanceof HTMLTextAreaElement
      ) {
        return
      }
      if (e.key === "a" || e.key === "A" || e.key === "ArrowLeft") {
        if (hasPrev) {
          e.preventDefault()
          onPrev()
        }
      } else if (e.key === "d" || e.key === "D" || e.key === "ArrowRight") {
        if (hasNext) {
          e.preventDefault()
          onNext()
        }
      }
    }
    window.addEventListener("keydown", onKeyDown)
    return () => window.removeEventListener("keydown", onKeyDown)
  }, [sampleId, hasPrev, hasNext, onPrev, onNext])

  const detail = query.data

  return (
    <Dialog open={sampleId !== null} onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="flex max-h-[92vh] max-w-[min(96vw,78rem)] flex-col gap-3 overflow-hidden p-4 sm:max-w-[min(96vw,78rem)] sm:p-5">
        <DialogHeader className="flex flex-row flex-wrap items-center justify-between gap-2 space-y-0 pr-8">
          <div className="min-w-0">
            <DialogTitle className="flex flex-wrap items-center gap-2 text-base">
              <span className="truncate">
                {detail?.file_name ?? "加载样本中…"}
              </span>
              {detail && <OutcomeBadge outcome={detail.image_outcome} />}
              {positionLabel && (
                <Badge variant="outline" className="font-normal tabular-nums">
                  {positionLabel}
                </Badge>
              )}
            </DialogTitle>
            <DialogDescription className="truncate text-xs">
              {detail
                ? `${detail.path} · ${detail.width}×${detail.height} · Conf ${formatThreshold(conf)} · IoU ${formatThreshold(iou)}`
                : "正在读取匹配结果与检测框坐标…"}
            </DialogDescription>
          </div>

          <div className="flex flex-wrap items-center gap-1.5">
            <Button
              type="button"
              variant="outline"
              size="sm"
              className="h-8 text-xs"
              disabled={!hasPrev}
              onClick={onPrev}
              title="上一张 (A / ←)"
            >
              <ChevronLeft className="size-3.5" />
              上一张
            </Button>
            <Button
              type="button"
              variant="outline"
              size="sm"
              className="h-8 text-xs"
              disabled={!hasNext}
              onClick={onNext}
              title="下一张 (D / →)"
            >
              下一张
              <ChevronRight className="size-3.5" />
            </Button>
            {detail?.annotate_url && (
              <Button size="sm" className="h-8 text-xs" asChild>
                <Link to={detail.annotate_url}>
                  <PenTool className="size-3.5" />
                  去标注工作台修正
                  <ExternalLink className="size-3" />
                </Link>
              </Button>
            )}
          </div>
        </DialogHeader>

        {/* 图层开关栏 */}
        <LayerToggleBar layers={layers} onChange={setLayers} />

        {/* 主体：左画布 + 右侧框明细 */}
        {query.isPending && !detail ? (
          <Skeleton className="h-[62vh] w-full rounded-xl" />
        ) : detail ? (
          <div className="grid min-h-0 flex-1 grid-cols-1 gap-4 overflow-hidden lg:grid-cols-[1fr_21rem]">
            <BoundingBoxViewport
              key={detail.id}
              detail={detail}
              layers={layers}
              highlightKey={highlightKey}
              onHoverBox={setHighlightKey}
            />

            <div className="flex min-h-0 flex-col gap-3 overflow-y-auto rounded-xl border bg-muted/20 p-3 text-xs">
              <div className="grid grid-cols-4 gap-1.5 text-center tabular-nums">
                <MiniMetric label="TP" value={detail.tp_count} tone="blue" />
                <MiniMetric label="漏检 FN" value={detail.fn_count} tone="amber" />
                <MiniMetric label="误检 FP" value={detail.fp_count} tone="rose" />
                <MiniMetric
                  label="误分类"
                  value={detail.misclassified_count}
                  tone="violet"
                />
              </div>

              {detail.inference_error && (
                <div className="rounded-lg border border-destructive/40 bg-destructive/10 p-2 text-destructive">
                  推理异常：{detail.inference_error}
                </div>
              )}

              <BoxListSection
                title={`预测框 (${detail.predictions.length})`}
                emptyText="当前阈值下无预测框"
                items={detail.predictions.map((p, i) => ({
                  key: `pred-${i}`,
                  label: p.name,
                  tag:
                    p.match === "tp"
                      ? "TP"
                      : p.match === "misclassified"
                        ? `误分类 (实为 ${p.matched_gt_name ?? "?"})`
                        : "FP",
                  tone: p.match,
                  meta: `conf ${(p.conf * 100).toFixed(1)}%${p.iou ? ` · IoU ${p.iou.toFixed(2)}` : ""}`,
                }))}
                highlightKey={highlightKey}
                onHover={setHighlightKey}
              />

              <BoxListSection
                title={`真实标注框 GT (${detail.gt_objects.length})`}
                emptyText="无真实目标（OK 背景样本）"
                items={detail.gt_objects.map((g, i) => ({
                  key: `gt-${i}`,
                  label: g.name,
                  tag:
                    g.match === "tp"
                      ? "已检出"
                      : g.match === "misclassified"
                        ? `误判为 ${g.matched_pred_name ?? "?"}`
                        : "漏检 FN",
                  tone: g.match === "fn" ? "fn" : g.match,
                  meta: g.iou ? `IoU ${g.iou.toFixed(2)}` : "未匹配",
                }))}
                highlightKey={highlightKey}
                onHover={setHighlightKey}
              />
            </div>
          </div>
        ) : (
          <div className="py-16 text-center text-sm text-muted-foreground">
            无法加载样本详情。
          </div>
        )}
      </DialogContent>
    </Dialog>
  )
}

/** 同源双模型对比：单张图片左右并排检测框叠加检视弹窗。 */
export function CompareDiffModal({
  item,
  baseLabel,
  targetLabel,
  onClose,
}: {
  item: CompareDiffItem | null
  baseLabel: string
  targetLabel: string
  onClose: () => void
}) {
  const [layers, setLayers] = useState<LayerVisibility>(DEFAULT_LAYERS)

  return (
    <Dialog open={item !== null} onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="flex max-h-[92vh] max-w-[min(96vw,84rem)] flex-col gap-3 overflow-hidden p-4 sm:max-w-[min(96vw,84rem)] sm:p-5">
        <DialogHeader className="flex flex-row flex-wrap items-center justify-between gap-2 space-y-0 pr-8">
          <div className="min-w-0">
            <DialogTitle className="flex flex-wrap items-center gap-2 text-base">
              <span className="truncate">
                双模型逐图并排对比：{item?.file_name ?? ""}
              </span>
              {item && (
                <div className="flex items-center gap-1.5 text-xs tabular-nums">
                  {item.delta_fn !== 0 && (
                    <Badge
                      variant="outline"
                      className={
                        item.delta_fn < 0
                          ? "border-emerald-500/40 bg-emerald-500/10 text-emerald-700"
                          : "border-destructive/40 bg-destructive/10 text-destructive"
                      }
                    >
                      ΔFN {item.delta_fn > 0 ? `+${item.delta_fn}` : item.delta_fn}
                    </Badge>
                  )}
                  {item.delta_fp !== 0 && (
                    <Badge
                      variant="outline"
                      className={
                        item.delta_fp < 0
                          ? "border-emerald-500/40 bg-emerald-500/10 text-emerald-700"
                          : "border-destructive/40 bg-destructive/10 text-destructive"
                      }
                    >
                      ΔFP {item.delta_fp > 0 ? `+${item.delta_fp}` : item.delta_fp}
                    </Badge>
                  )}
                </div>
              )}
            </DialogTitle>
            <DialogDescription className="truncate text-xs">
              {item?.path}
            </DialogDescription>
          </div>

          {item?.base.annotate_url && (
            <Button size="sm" variant="outline" className="h-8 text-xs" asChild>
              <Link to={item.base.annotate_url}>
                <PenTool className="size-3.5" />
                去标注工作台查看
              </Link>
            </Button>
          )}
        </DialogHeader>

        <LayerToggleBar layers={layers} onChange={setLayers} />

        {item && (
          <div className="grid min-h-0 flex-1 grid-cols-1 gap-3 overflow-hidden md:grid-cols-2">
            <div className="flex min-h-0 flex-col gap-2">
              <div className="flex flex-wrap items-center justify-between gap-2 rounded-lg border bg-muted/30 px-3 py-1.5 text-xs">
                <span className="font-semibold">基线：{baseLabel}</span>
                <div className="flex items-center gap-1.5 tabular-nums">
                  <OutcomeBadge outcome={item.base.image_outcome} />
                  <span>
                    TP {item.base.tp_count} / FN {item.base.fn_count} / FP{" "}
                    {item.base.fp_count}
                  </span>
                </div>
              </div>
              <BoundingBoxViewport detail={item.base} layers={layers} />
            </div>

            <div className="flex min-h-0 flex-col gap-2">
              <div className="flex flex-wrap items-center justify-between gap-2 rounded-lg border bg-muted/30 px-3 py-1.5 text-xs">
                <span className="font-semibold">对比：{targetLabel}</span>
                <div className="flex items-center gap-1.5 tabular-nums">
                  <OutcomeBadge outcome={item.target.image_outcome} />
                  <span>
                    TP {item.target.tp_count} / FN {item.target.fn_count} / FP{" "}
                    {item.target.fp_count}
                  </span>
                </div>
              </div>
              <BoundingBoxViewport detail={item.target} layers={layers} />
            </div>
          </div>
        )}
      </DialogContent>
    </Dialog>
  )
}

function LayerToggleBar({
  layers,
  onChange,
}: {
  layers: LayerVisibility
  onChange: (next: LayerVisibility) => void
}) {
  const items: {
    key: keyof LayerVisibility
    label: string
    dotColor?: string
    dashed?: boolean
  }[] = [
    { key: "gt", label: "真实框 GT", dotColor: "#10b981", dashed: true },
    { key: "tp", label: "正确 TP", dotColor: "#2563eb" },
    { key: "fp", label: "误检 FP", dotColor: "#ef4444" },
    { key: "fn", label: "漏检 FN", dotColor: "#f59e0b", dashed: true },
    { key: "misclassified", label: "误分类", dotColor: "#8b5cf6" },
    { key: "labels", label: "显示标签与置信度" },
  ]

  return (
    <div className="flex flex-wrap items-center gap-1.5">
      <span className="mr-1 text-xs text-muted-foreground">叠加图层：</span>
      {items.map((it) => {
        const active = layers[it.key]
        return (
          <button
            key={it.key}
            type="button"
            onClick={() => onChange({ ...layers, [it.key]: !active })}
            className={cn(
              "inline-flex items-center gap-1.5 rounded-md border px-2 py-1 text-xs transition-all",
              active
                ? "bg-background text-foreground shadow-2xs"
                : "border-transparent bg-muted/50 text-muted-foreground opacity-55"
            )}
          >
            {it.dotColor && (
              <span
                className="inline-block size-2.5 rounded-xs"
                style={{
                  backgroundColor: it.dashed ? "transparent" : it.dotColor,
                  border: `2px ${it.dashed ? "dashed" : "solid"} ${it.dotColor}`,
                }}
              />
            )}
            {it.label}
          </button>
        )
      })}
    </div>
  )
}

/** 交互式图片 + SVG 检测框叠加视口（支持滚轮/按钮缩放与拖拽平移）。 */
export function BoundingBoxViewport({
  detail,
  layers,
  highlightKey,
  onHoverBox,
}: {
  detail: SampleDetail
  layers: LayerVisibility
  highlightKey?: string | null
  onHoverBox?: (key: string | null) => void
}) {
  const [zoom, setZoom] = useState(1)
  const [pan, setPan] = useState({ x: 0, y: 0 })
  const [naturalSize, setNaturalSize] = useState<{ w: number; h: number }>({
    w: detail.width || 640,
    h: detail.height || 640,
  })
  const [imgError, setImgError] = useState(false)
  const dragRef = useRef<{
    startX: number
    startY: number
    origX: number
    origY: number
  } | null>(null)

  const imgW = naturalSize.w || 640
  const imgH = naturalSize.h || 640
  const baseStroke = Math.max(1.8, Math.min(imgW, imgH) / 280)
  const fontSize = Math.max(11, Math.min(imgW, imgH) / 42)

  const handleWheel = (e: ReactWheelEvent<HTMLDivElement>) => {
    e.preventDefault()
    const factor = e.deltaY < 0 ? 1.18 : 1 / 1.18
    setZoom((z) => Math.max(0.5, Math.min(5, Math.round(z * factor * 100) / 100)))
  }

  const handleMouseDown = (e: ReactMouseEvent<HTMLDivElement>) => {
    if (e.button !== 0) {
      return
    }
    dragRef.current = {
      startX: e.clientX,
      startY: e.clientY,
      origX: pan.x,
      origY: pan.y,
    }
  }

  const handleMouseMove = (e: ReactMouseEvent<HTMLDivElement>) => {
    if (!dragRef.current) {
      return
    }
    setPan({
      x: dragRef.current.origX + (e.clientX - dragRef.current.startX),
      y: dragRef.current.origY + (e.clientY - dragRef.current.startY),
    })
  }

  const handleMouseUp = () => {
    dragRef.current = null
  }

  const resetView = () => {
    setZoom(1)
    setPan({ x: 0, y: 0 })
  }

  const visibleGTs: { box: MatchedGTBox; idx: number }[] = []
  detail.gt_objects.forEach((box, idx) => {
    if (box.match === "fn") {
      if (layers.fn || layers.gt) {
        visibleGTs.push({ box, idx })
      }
    } else if (layers.gt) {
      visibleGTs.push({ box, idx })
    }
  })

  const visiblePreds: { box: MatchedPredBox; idx: number }[] = []
  detail.predictions.forEach((box, idx) => {
    if (box.match === "tp" && layers.tp) {
      visiblePreds.push({ box, idx })
    } else if (box.match === "fp" && layers.fp) {
      visiblePreds.push({ box, idx })
    } else if (box.match === "misclassified" && layers.misclassified) {
      visiblePreds.push({ box, idx })
    }
  })

  return (
    <div className="relative flex min-h-[24rem] flex-1 flex-col overflow-hidden rounded-xl border bg-zinc-950 select-none">
      {/* 缩放工具条 */}
      <div className="absolute top-2.5 right-2.5 z-20 flex items-center gap-1 rounded-lg border border-white/15 bg-zinc-900/85 px-1.5 py-1 text-xs text-zinc-100 backdrop-blur-xs">
        <button
          type="button"
          onClick={() => setZoom((z) => Math.max(0.5, z / 1.25))}
          className="rounded p-1 hover:bg-white/15"
          title="缩小"
        >
          <ZoomOut className="size-3.5" />
        </button>
        <span className="min-w-11 text-center font-mono text-[11px] tabular-nums">
          {Math.round(zoom * 100)}%
        </span>
        <button
          type="button"
          onClick={() => setZoom((z) => Math.min(5, z * 1.25))}
          className="rounded p-1 hover:bg-white/15"
          title="放大"
        >
          <ZoomIn className="size-3.5" />
        </button>
        <button
          type="button"
          onClick={resetView}
          className="rounded p-1 hover:bg-white/15"
          title="重置视图"
        >
          <RotateCcw className="size-3.5" />
        </button>
      </div>

      {/* 可缩放平移画布 */}
      <div
        className="flex flex-1 cursor-grab items-center justify-center overflow-hidden p-4 active:cursor-grabbing"
        onWheel={handleWheel}
        onMouseDown={handleMouseDown}
        onMouseMove={handleMouseMove}
        onMouseUp={handleMouseUp}
        onMouseLeave={handleMouseUp}
      >
        {detail.source_missing || imgError ? (
          <div className="flex flex-col items-center gap-2 text-center text-sm text-zinc-400">
            <ImageOff className="size-10 opacity-60" />
            <p>原始图片文件已不存在或已归档，仍可在右侧查看结构化检测框与匹配指标。</p>
          </div>
        ) : (
          <div
            style={{
              transform: `translate(${pan.x}px, ${pan.y}px) scale(${zoom})`,
              transformOrigin: "center center",
            }}
            className="relative inline-block max-h-full max-w-full transition-transform duration-75"
          >
            <img
              src={detail.file_url}
              alt={detail.file_name}
              draggable={false}
              onLoad={(e) => {
                const img = e.currentTarget
                if (img.naturalWidth > 0 && img.naturalHeight > 0) {
                  setNaturalSize({
                    w: img.naturalWidth,
                    h: img.naturalHeight,
                  })
                }
              }}
              onError={() => setImgError(true)}
              className="block max-h-[58vh] max-w-full object-contain"
            />
            <svg
              viewBox={`0 0 ${imgW} ${imgH}`}
              preserveAspectRatio="none"
              className="pointer-events-none absolute inset-0 size-full"
            >
              {/* 真实标注框（含漏检 FN 高亮） */}
              {visibleGTs.map(({ box, idx }) => {
                const key = `gt-${idx}`
                const isFN = box.match === "fn"
                const color = isFN ? "#f59e0b" : "#10b981"
                const active = highlightKey === key
                const w = Math.max(1, box.xmax - box.xmin)
                const h = Math.max(1, box.ymax - box.ymin)
                const tagText = isFN ? `FN 漏检: ${box.name}` : `GT: ${box.name}`
                const pillW = Math.max(
                  fontSize * 4,
                  tagText.length * fontSize * 0.68 + fontSize
                )
                const pillH = fontSize * 1.45
                const pillY = Math.max(0, box.ymin - pillH)
                return (
                  <g
                    key={key}
                    className="pointer-events-auto"
                    onMouseEnter={() => onHoverBox?.(key)}
                    onMouseLeave={() => onHoverBox?.(null)}
                  >
                    <rect
                      x={box.xmin}
                      y={box.ymin}
                      width={w}
                      height={h}
                      fill={color}
                      fillOpacity={active ? 0.24 : isFN ? 0.14 : 0.05}
                      stroke={color}
                      strokeWidth={active ? baseStroke * 2 : baseStroke * 1.2}
                      strokeDasharray={`${baseStroke * 3} ${baseStroke * 2}`}
                    />
                    {layers.labels && (
                      <g>
                        <rect
                          x={box.xmin}
                          y={pillY}
                          width={pillW}
                          height={pillH}
                          rx={baseStroke * 1.2}
                          fill={color}
                          fillOpacity={0.92}
                        />
                        <text
                          x={box.xmin + fontSize * 0.4}
                          y={pillY + fontSize * 1.05}
                          fill="#ffffff"
                          fontSize={fontSize}
                          fontWeight="600"
                        >
                          {tagText}
                        </text>
                      </g>
                    )}
                  </g>
                )
              })}

              {/* 预测框（TP / FP / Misclassified） */}
              {visiblePreds.map(({ box, idx }) => {
                const key = `pred-${idx}`
                const color =
                  box.match === "tp"
                    ? "#3b82f6"
                    : box.match === "misclassified"
                      ? "#8b5cf6"
                      : "#ef4444"
                const active = highlightKey === key
                const w = Math.max(1, box.xmax - box.xmin)
                const h = Math.max(1, box.ymax - box.ymin)
                const prefix =
                  box.match === "tp"
                    ? "TP"
                    : box.match === "misclassified"
                      ? "误分类"
                      : "FP"
                const tagText = `${prefix} ${box.name} ${(box.conf * 100).toFixed(0)}%`
                const pillW = Math.max(
                  fontSize * 5,
                  tagText.length * fontSize * 0.62 + fontSize
                )
                const pillH = fontSize * 1.45
                // 预测框标签尽量放在框下沿或上沿，减少与 GT 重叠
                const pillY =
                  box.ymax + pillH <= imgH ? box.ymax : Math.max(0, box.ymin - pillH)
                return (
                  <g
                    key={key}
                    className="pointer-events-auto"
                    onMouseEnter={() => onHoverBox?.(key)}
                    onMouseLeave={() => onHoverBox?.(null)}
                  >
                    <rect
                      x={box.xmin}
                      y={box.ymin}
                      width={w}
                      height={h}
                      fill={color}
                      fillOpacity={active ? 0.25 : 0.08}
                      stroke={color}
                      strokeWidth={active ? baseStroke * 2.2 : baseStroke * 1.3}
                    />
                    {layers.labels && (
                      <g>
                        <rect
                          x={box.xmin}
                          y={pillY}
                          width={pillW}
                          height={pillH}
                          rx={baseStroke * 1.2}
                          fill={color}
                          fillOpacity={0.94}
                        />
                        <text
                          x={box.xmin + fontSize * 0.4}
                          y={pillY + fontSize * 1.05}
                          fill="#ffffff"
                          fontSize={fontSize}
                          fontWeight="600"
                        >
                          {tagText}
                        </text>
                      </g>
                    )}
                  </g>
                )
              })}
            </svg>
          </div>
        )}
      </div>
    </div>
  )
}

function MiniMetric({
  label,
  value,
  tone,
}: {
  label: string
  value: number
  tone: "blue" | "amber" | "rose" | "violet"
}) {
  const toneClass = {
    blue: "border-blue-500/30 bg-blue-500/10 text-blue-700 dark:text-blue-400",
    amber:
      "border-amber-500/30 bg-amber-500/10 text-amber-800 dark:text-amber-400",
    rose: "border-rose-500/30 bg-rose-500/10 text-rose-700 dark:text-rose-400",
    violet:
      "border-violet-500/30 bg-violet-500/10 text-violet-700 dark:text-violet-400",
  }[tone]
  return (
    <div className={cn("rounded-lg border px-1.5 py-1.5", toneClass)}>
      <div className="text-[10px] opacity-80">{label}</div>
      <div className="text-sm font-semibold">{formatNumber(value)}</div>
    </div>
  )
}

function BoxListSection({
  title,
  emptyText,
  items,
  highlightKey,
  onHover,
}: {
  title: string
  emptyText: string
  items: {
    key: string
    label: string
    tag: string
    tone: string
    meta: string
  }[]
  highlightKey: string | null
  onHover: (key: string | null) => void
}) {
  return (
    <div className="grid gap-1.5">
      <div className="font-semibold text-foreground">{title}</div>
      {items.length === 0 ? (
        <div className="rounded-md border border-dashed px-2.5 py-2 text-muted-foreground">
          {emptyText}
        </div>
      ) : (
        <div className="grid gap-1">
          {items.map((it) => {
            const active = highlightKey === it.key
            const badgeClass =
              it.tone === "tp"
                ? "bg-blue-500/15 text-blue-700 dark:text-blue-400"
                : it.tone === "fp"
                  ? "bg-rose-500/15 text-rose-700 dark:text-rose-400"
                  : it.tone === "fn"
                    ? "bg-amber-500/15 text-amber-800 dark:text-amber-400"
                    : "bg-violet-500/15 text-violet-700 dark:text-violet-400"
            return (
              <div
                key={it.key}
                onMouseEnter={() => onHover(it.key)}
                onMouseLeave={() => onHover(null)}
                className={cn(
                  "flex items-center justify-between gap-2 rounded-md border bg-background px-2.5 py-1.5 transition-colors",
                  active && "border-primary ring-1 ring-primary"
                )}
              >
                <div className="min-w-0">
                  <div className="truncate font-medium text-foreground">
                    {it.label}
                  </div>
                  <div className="text-[11px] text-muted-foreground tabular-nums">
                    {it.meta}
                  </div>
                </div>
                <span
                  className={cn(
                    "shrink-0 rounded px-1.5 py-0.5 text-[10px] font-medium",
                    badgeClass
                  )}
                >
                  {it.tag}
                </span>
              </div>
            )
          })}
        </div>
      )}
    </div>
  )
}
