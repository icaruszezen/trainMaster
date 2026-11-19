/**
 * 多模型横向对比页（`/evaluation/compare?ids=1,2,...`）：
 * - 支持自由增删 2–6 个已完成评估任务、指定基线模型（Baseline）
 * - 支持两种阈值口径：「各自默认阈值」vs「统一阈值（实时 Conf / IoU 滑块）」
 * - KPI 差异矩阵（每项指标自动高亮最优值 + 相对基线 Δ 增减）
 * - 纯 SVG 多模型 PR & F1-Conf 叠加曲线
 * - 各类别 AP50 / F1 对比表 & 推理速度横向条形图
 * - 当所有对比评估为同源数据源（`same_source === true`）时，自动解锁「同源逐图差异」标签页：
 *   可按「漏检减少 / 新增漏检 / 误检减少 / 新增误检 / OK↔NG 翻转」筛选样本，点击打开双模型左右并排框叠加对比弹窗
 */
import { useEffect, useMemo, useState } from "react"
import { useQuery } from "@tanstack/react-query"
import {
  ArrowLeft,
  ArrowRightLeft,
  CheckCircle2,
  Download,
  ExternalLink,
  Eye,
  FileCode2,
  FileSpreadsheet,
  GitCompare,
  Plus,
  Search,
  Sparkles,
  X,
} from "lucide-react"
import { Link, useSearchParams } from "react-router"

import { PageHeader } from "@/components/page-header"
import { ScrollingTabsList } from "@/components/scrolling-tabs-list"
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
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { Input } from "@/components/ui/input"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { Skeleton } from "@/components/ui/skeleton"
import { Slider } from "@/components/ui/slider"
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import { Tabs, TabsContent, TabsTrigger } from "@/components/ui/tabs"
import { LoadError } from "@/features/datasets/load-error"
import {
  evaluationCompareDiffQuery,
  evaluationCompareExportUrl,
  evaluationCompareQuery,
  evaluationsQuery,
  sampleThumbnailUrl,
  type CompareDiffItem,
  type CompareDiffType,
  type CompareItem,
  type CompareMode,
} from "@/features/evaluation/api"
import { CompareCurves } from "@/features/evaluation/evaluation-curves"
import {
  colorForIndex,
  COMPARE_DIFF_LABELS,
  evaluationModelLabel,
  evaluationSourceSummary,
  evaluationTitle,
  formatFps,
  formatMetricDelta,
  formatMetricPercent,
  formatMs,
  formatThreshold,
} from "@/features/evaluation/format"
import {
  evaluationDetailPath,
  EVALUATIONS_PATH,
  parseCompareIds,
} from "@/features/evaluation/paths"
import { CompareDiffModal } from "@/features/evaluation/sample-inspector"
import { OutcomeBadge } from "@/features/evaluation/status"
import { usePageTitle } from "@/hooks/use-page-title"
import { startDownload } from "@/lib/download"
import { formatNumber } from "@/lib/format"
import { cn } from "@/lib/utils"

type CompareTab = "metrics" | "diff"

export function EvaluationComparePage() {
  usePageTitle("多模型评估对比")
  const [searchParams, setSearchParams] = useSearchParams()

  const ids = useMemo(
    () => parseCompareIds(searchParams.get("ids")),
    [searchParams]
  )
  const modeParam = searchParams.get("mode")
  const [mode, setMode] = useState<CompareMode>(
    modeParam === "unified" ? "unified" : "default"
  )
  const [sliderConf, setSliderConf] = useState(() => {
    const c = Number(searchParams.get("conf"))
    return Number.isFinite(c) && c >= 0.05 && c <= 0.99 ? c : 0.25
  })
  const [sliderIou, setSliderIou] = useState(() => {
    const v = Number(searchParams.get("iou"))
    return Number.isFinite(v) && v >= 0.05 && v <= 0.99 ? v : 0.5
  })
  const [activeConf, setActiveConf] = useState(sliderConf)
  const [activeIou, setActiveIou] = useState(sliderIou)
  const [baselineId, setBaselineId] = useState<number | null>(ids[0] ?? null)
  const tabParam = searchParams.get("tab")
  const tab: CompareTab = tabParam === "diff" ? "diff" : "metrics"
  const setTab = (next: CompareTab) => {
    setSearchParams(
      (prev) => {
        const out = new URLSearchParams(prev)
        if (next === "diff") {
          out.set("tab", "diff")
        } else {
          out.delete("tab")
        }
        return out
      },
      { replace: true }
    )
  }

  useEffect(() => {
    const timer = setTimeout(() => {
      setActiveConf(sliderConf)
      setActiveIou(sliderIou)
    }, 150)
    return () => clearTimeout(timer)
  }, [sliderConf, sliderIou])

  const allSucceeded = useQuery(evaluationsQuery({ status: "succeeded" }))
  const compareQuery = useQuery({
    ...evaluationCompareQuery(
      ids,
      mode,
      mode === "unified" ? activeConf : undefined,
      mode === "unified" ? activeIou : undefined
    ),
    enabled: ids.length >= 2,
  })

  const setIdsInUrl = (nextIds: number[]) => {
    setSearchParams(
      (prev) => {
        const out = new URLSearchParams(prev)
        if (nextIds.length > 0) {
          out.set("ids", nextIds.join(","))
        } else {
          out.delete("ids")
        }
        return out
      },
      { replace: true }
    )
  }

  const addEvalId = (newId: number) => {
    if (!ids.includes(newId) && ids.length < 6) {
      setIdsInUrl([...ids, newId])
    }
  }

  const removeEvalId = (remId: number) => {
    setIdsInUrl(ids.filter((id) => id !== remId))
  }

  const availableToAdd = useMemo(
    () =>
      (allSucceeded.data?.items ?? []).filter((ev) => !ids.includes(ev.id)),
    [allSucceeded.data, ids]
  )

  const data = compareQuery.data
  const items = data?.items ?? []
  const baseline =
    items.find((it) => it.evaluation.id === baselineId) ?? items[0] ?? null

  return (
    <div className="grid min-w-0 gap-5 pb-12">
      <div className="grid gap-2">
        <Button variant="ghost" size="sm" className="-ml-2 w-fit" asChild>
          <Link to={EVALUATIONS_PATH}>
            <ArrowLeft />
            返回模型评估列表
          </Link>
        </Button>

        <PageHeader
          title="多模型评估对比"
          description="将 2–6 个已完成评估并排对照，支持统一度量阈值重算、PR/F1 叠加曲线、各类别明细、推理速度对比以及同源逐图差异检视。"
          actions={
            ids.length >= 2 && (
              <DropdownMenu modal={false}>
                <DropdownMenuTrigger asChild>
                  <Button variant="outline" size="sm">
                    <Download />
                    导出对比报告
                  </Button>
                </DropdownMenuTrigger>
                <DropdownMenuContent align="end" className="w-52">
                  <DropdownMenuItem
                    onSelect={() =>
                      window.open(
                        evaluationCompareExportUrl(ids, "html", {
                          mode,
                          conf: mode === "unified" ? activeConf : undefined,
                          iou: mode === "unified" ? activeIou : undefined,
                          inline: true,
                        }),
                        "_blank",
                        "noopener,noreferrer"
                      )
                    }
                  >
                    <ExternalLink />
                    在线预览离线 HTML 报告
                  </DropdownMenuItem>
                  <DropdownMenuItem
                    onSelect={() =>
                      startDownload(
                        evaluationCompareExportUrl(ids, "html", {
                          mode,
                          conf: mode === "unified" ? activeConf : undefined,
                          iou: mode === "unified" ? activeIou : undefined,
                        }),
                        `evaluation-compare-${ids.join("-")}.html`
                      )
                    }
                  >
                    <FileCode2 />
                    下载离线 HTML 对比报告
                  </DropdownMenuItem>
                  <DropdownMenuItem
                    onSelect={() =>
                      startDownload(
                        evaluationCompareExportUrl(ids, "csv", {
                          mode,
                          conf: mode === "unified" ? activeConf : undefined,
                          iou: mode === "unified" ? activeIou : undefined,
                        }),
                        `evaluation-compare-${ids.join("-")}.csv`
                      )
                    }
                  >
                    <FileSpreadsheet />
                    下载对比指标 CSV
                  </DropdownMenuItem>
                </DropdownMenuContent>
              </DropdownMenu>
            )
          }
        />
      </div>

      {/* 顶部选择栏 + 阈值模式切换 */}
      <Card className="py-4">
        <CardContent className="grid gap-4 px-5">
          <div className="flex flex-wrap items-center gap-2">
            <span className="text-xs font-medium text-muted-foreground">
              参与对比（{ids.length}/6）：
            </span>
            {items.map((it, idx) => {
              const isBase = baseline?.evaluation.id === it.evaluation.id
              return (
                <div
                  key={it.evaluation.id}
                  className={cn(
                    "inline-flex items-center gap-1.5 rounded-lg border px-2.5 py-1 text-xs",
                    isBase
                      ? "border-primary bg-primary/[0.06]"
                      : "bg-background"
                  )}
                >
                  <span
                    className="inline-block size-2 rounded-full"
                    style={{ backgroundColor: colorForIndex(idx) }}
                  />
                  <button
                    type="button"
                    onClick={() => setBaselineId(it.evaluation.id)}
                    className="max-w-44 truncate font-medium hover:underline"
                    title="点击设为基线 (Baseline)"
                  >
                    {evaluationTitle(it.evaluation)}
                  </button>
                  {isBase && (
                    <Badge
                      variant="secondary"
                      className="h-4 px-1 text-[10px] font-medium"
                    >
                      基线
                    </Badge>
                  )}
                  <button
                    type="button"
                    onClick={() => removeEvalId(it.evaluation.id)}
                    className="text-muted-foreground hover:text-foreground"
                    aria-label="移出对比"
                  >
                    <X className="size-3.5" />
                  </button>
                </div>
              )
            })}

            {ids.length < 6 && availableToAdd.length > 0 && (
              <Select
                value=""
                onValueChange={(v) => addEvalId(Number(v))}
              >
                <SelectTrigger className="h-8 w-52 text-xs">
                  <Plus className="size-3.5" />
                  <SelectValue placeholder="添加已完成评估…" />
                </SelectTrigger>
                <SelectContent>
                  {availableToAdd.map((ev) => (
                    <SelectItem key={ev.id} value={String(ev.id)}>
                      {evaluationTitle(ev)} ({evaluationModelLabel(ev)})
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            )}

            {data && (
              <Badge
                variant="outline"
                className={cn(
                  "ml-auto text-xs",
                  data.same_source
                    ? "border-emerald-500/40 bg-emerald-500/10 text-emerald-700 dark:text-emerald-400"
                    : "border-amber-500/40 bg-amber-500/10 text-amber-800 dark:text-amber-400"
                )}
              >
                {data.same_source ? (
                  <>
                    <CheckCircle2 className="size-3.5" />
                    同源验证集（已开启逐图差异）
                  </>
                ) : (
                  "跨数据源对比（仅对比聚合指标）"
                )}
              </Badge>
            )}
          </div>

          {/* 阈值口径控制 */}
          <div className="flex flex-wrap items-center gap-4 border-t pt-3">
            <div className="inline-flex rounded-lg border bg-muted/40 p-0.5 text-xs">
              <button
                type="button"
                onClick={() => setMode("default")}
                className={cn(
                  "rounded-md px-3 py-1 font-medium transition-colors",
                  mode === "default"
                    ? "bg-background text-foreground shadow-2xs"
                    : "text-muted-foreground hover:text-foreground"
                )}
              >
                各自默认阈值
              </button>
              <button
                type="button"
                onClick={() => setMode("unified")}
                className={cn(
                  "rounded-md px-3 py-1 font-medium transition-colors",
                  mode === "unified"
                    ? "bg-background text-foreground shadow-2xs"
                    : "text-muted-foreground hover:text-foreground"
                )}
              >
                统一阈值公平对比
              </button>
            </div>

            {mode === "unified" ? (
              <div className="flex flex-1 flex-wrap items-center gap-6">
                <div className="flex min-w-48 flex-1 items-center gap-3 text-xs">
                  <span className="shrink-0 font-medium">
                    统一 Conf{" "}
                    <strong className="font-mono text-primary tabular-nums">
                      {formatThreshold(sliderConf)}
                    </strong>
                  </span>
                  <Slider
                    value={[sliderConf]}
                    min={0.05}
                    max={0.99}
                    step={0.01}
                    onValueChange={([v]) => setSliderConf(v)}
                  />
                </div>
                <div className="flex min-w-44 flex-1 items-center gap-3 text-xs">
                  <span className="shrink-0 font-medium">
                    统一 IoU{" "}
                    <strong className="font-mono tabular-nums">
                      {formatThreshold(sliderIou)}
                    </strong>
                  </span>
                  <Slider
                    value={[sliderIou]}
                    min={0.05}
                    max={0.99}
                    step={0.05}
                    onValueChange={([v]) => setSliderIou(v)}
                  />
                </div>
              </div>
            ) : (
              <span className="text-xs text-muted-foreground">
                点击上方任意评估标签可切换基线模型（Δ 增量均相对于基线计算）。
              </span>
            )}
          </div>
        </CardContent>
      </Card>

      {ids.length < 2 ? (
        <Alert>
          <ArrowRightLeft className="size-4" />
          <AlertTitle>请至少选择 2 个已完成的评估任务</AlertTitle>
          <AlertDescription>
            从上方下拉菜单中添加评估任务，或在模型评估列表中勾选 2–6 条记录后进入对比。
          </AlertDescription>
        </Alert>
      ) : compareQuery.isPending && !data ? (
        <div className="grid gap-4">
          <Skeleton className="h-64 rounded-xl" />
          <Skeleton className="h-64 rounded-xl" />
        </div>
      ) : compareQuery.error && !data ? (
        <LoadError
          title="无法加载对比数据"
          error={compareQuery.error}
          retrying={compareQuery.isFetching}
          onRetry={() => compareQuery.refetch()}
        />
      ) : data && baseline ? (
        <Tabs
          value={data.same_source ? tab : "metrics"}
          onValueChange={(v) => setTab(v as CompareTab)}
          className="grid min-w-0 gap-4"
        >
          <ScrollingTabsList value={data.same_source ? tab : "metrics"}>
            <TabsTrigger value="metrics" className="gap-1.5">
              <Sparkles className="size-4" />
              指标对比与曲线
            </TabsTrigger>
            <TabsTrigger
              value="diff"
              disabled={!data.same_source}
              className="gap-1.5"
            >
              <GitCompare className="size-4" />
              同源逐图差异
              {!data.same_source && (
                <span className="text-[11px] text-muted-foreground">
                  (需同数据源)
                </span>
              )}
            </TabsTrigger>
          </ScrollingTabsList>

          <TabsContent value="metrics" className="grid gap-5">
            {/* 1. KPI 差异矩阵 */}
            <KPIDiffTable items={items} baseline={baseline} />

            {/* 2. 多模型叠加 PR & F1-Conf 曲线 */}
            <CompareCurves
              items={items}
              unifiedConf={mode === "unified" ? sliderConf : undefined}
              onSelectConf={
                mode === "unified"
                  ? (c) => {
                      setSliderConf(c)
                      setActiveConf(c)
                    }
                  : undefined
              }
            />

            {/* 3. 各类别 AP50 & F1 对比表 */}
            {data.class_names.length > 0 && (
              <PerClassCompareTable
                classNames={data.class_names}
                items={items}
              />
            )}

            {/* 4. 推理速度横向条形对比 */}
            <SpeedCompareCard items={items} />
          </TabsContent>

          <TabsContent value="diff" className="grid gap-4">
            {data.same_source && (
              <SameSourceDiffTab
                items={items}
                mode={mode}
                conf={mode === "unified" ? activeConf : undefined}
                iou={mode === "unified" ? activeIou : undefined}
                defaultBaseId={baseline.evaluation.id}
              />
            )}
          </TabsContent>
        </Tabs>
      ) : null}
    </div>
  )
}

type MetricRowDef = {
  key: string
  label: string
  hint: string
  higherIsBetter: boolean
  isPercent?: boolean
  getValue: (item: CompareItem) => number | null
  format: (v: number | null) => string
  formatDelta: (delta: number) => string
}

const KPI_ROWS: readonly MetricRowDef[] = [
  {
    key: "map50",
    label: "mAP50",
    hint: "COCO IoU=0.50 平均精度",
    higherIsBetter: true,
    isPercent: true,
    getValue: (it) => it.summary.map50,
    format: (v) => formatMetricPercent(v),
    formatDelta: (d) => formatMetricDelta(d),
  },
  {
    key: "map50_95",
    label: "mAP50-95",
    hint: "COCO 10-IoU 严格综合精度",
    higherIsBetter: true,
    isPercent: true,
    getValue: (it) => it.summary.map50_95,
    format: (v) => formatMetricPercent(v),
    formatDelta: (d) => formatMetricDelta(d),
  },
  {
    key: "f1",
    label: "目标级 F1",
    hint: "当前阈值下的综合 F1",
    higherIsBetter: true,
    isPercent: true,
    getValue: (it) => it.summary.f1,
    format: (v) => formatMetricPercent(v),
    formatDelta: (d) => formatMetricDelta(d),
  },
  {
    key: "precision",
    label: "精确率 (Precision)",
    hint: "预测框中的正确比例",
    higherIsBetter: true,
    isPercent: true,
    getValue: (it) => it.summary.precision,
    format: (v) => formatMetricPercent(v),
    formatDelta: (d) => formatMetricDelta(d),
  },
  {
    key: "recall",
    label: "召回率 (Recall)",
    hint: "真实目标的检出比例",
    higherIsBetter: true,
    isPercent: true,
    getValue: (it) => it.summary.recall,
    format: (v) => formatMetricPercent(v),
    formatDelta: (d) => formatMetricDelta(d),
  },
  {
    key: "escape_rate",
    label: "漏杀率 (NG→OK)",
    hint: "越低越好 · 缺陷品漏判比例",
    higherIsBetter: false,
    isPercent: true,
    getValue: (it) =>
      it.ok_ng.ng_images > 0 ? it.ok_ng.escape_rate : null,
    format: (v) => formatMetricPercent(v),
    formatDelta: (d) => formatMetricDelta(d),
  },
  {
    key: "overkill_rate",
    label: "过杀率 (OK→NG)",
    hint: "越低越好 · 良品误报比例",
    higherIsBetter: false,
    isPercent: true,
    getValue: (it) =>
      it.ok_ng.ok_images > 0 ? it.ok_ng.overkill_rate : null,
    format: (v) => formatMetricPercent(v),
    formatDelta: (d) => formatMetricDelta(d),
  },
  {
    key: "total_ms",
    label: "单图耗时 (ms)",
    hint: "越低越好 · 端到端平均耗时",
    higherIsBetter: false,
    getValue: (it) =>
      it.summary.speed && it.summary.speed.total_ms > 0
        ? it.summary.speed.total_ms
        : null,
    format: (v) => formatMs(v),
    formatDelta: (d) => `${d > 0 ? "+" : ""}${d.toFixed(1)} ms`,
  },
  {
    key: "fps",
    label: "吞吐速度 (FPS)",
    hint: "越高越好 · 每秒处理帧数",
    higherIsBetter: true,
    getValue: (it) =>
      it.summary.speed && it.summary.speed.fps > 0
        ? it.summary.speed.fps
        : null,
    format: (v) => formatFps(v),
    formatDelta: (d) => `${d > 0 ? "+" : ""}${d.toFixed(1)}`,
  },
]

function KPIDiffTable({
  items,
  baseline,
}: {
  items: readonly CompareItem[]
  baseline: CompareItem
}) {
  return (
    <Card className="py-4">
      <CardHeader className="px-5 pb-3">
        <CardTitle className="text-base">核心指标差异矩阵（相对基线 Δ）</CardTitle>
        <CardDescription>
          绿色高亮每行最优值；数值下方展示相对基线「{evaluationTitle(baseline.evaluation)}」的变化量（绿优红劣）。
        </CardDescription>
      </CardHeader>
      <CardContent className="px-0">
        <div className="overflow-x-auto">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead className="min-w-44 pl-5">评估指标</TableHead>
                {items.map((it, idx) => (
                  <TableHead key={it.evaluation.id} className="min-w-36 text-right">
                    <div className="inline-flex flex-col items-end gap-0.5">
                      <Link
                        to={evaluationDetailPath(it.evaluation.id)}
                        className="inline-flex items-center gap-1.5 font-semibold text-foreground hover:underline"
                      >
                        <span
                          className="inline-block size-2 rounded-full"
                          style={{ backgroundColor: colorForIndex(idx) }}
                        />
                        <span className="max-w-36 truncate">
                          {evaluationTitle(it.evaluation)}
                        </span>
                      </Link>
                      <span className="text-[11px] font-normal text-muted-foreground tabular-nums">
                        {evaluationModelLabel(it.evaluation)} · @{formatThreshold(it.conf)}
                      </span>
                    </div>
                  </TableHead>
                ))}
              </TableRow>
            </TableHeader>
            <TableBody>
              <TableRow className="bg-muted/20 text-xs">
                <TableCell className="pl-5 text-muted-foreground">
                  数据源
                </TableCell>
                {items.map((it) => (
                  <TableCell
                    key={it.evaluation.id}
                    className="text-right text-muted-foreground"
                  >
                    {evaluationSourceSummary(it.evaluation.source)}
                  </TableCell>
                ))}
              </TableRow>

              {KPI_ROWS.map((row) => {
                const vals = items.map((it) => row.getValue(it))
                const valid = vals.filter((v): v is number => v !== null)
                const best =
                  valid.length > 0
                    ? row.higherIsBetter
                      ? Math.max(...valid)
                      : Math.min(...valid)
                    : null
                const baseVal = row.getValue(baseline)

                return (
                  <TableRow key={row.key}>
                    <TableCell className="pl-5">
                      <div className="font-medium text-foreground">
                        {row.label}
                      </div>
                      <div className="text-[11px] text-muted-foreground">
                        {row.hint}
                      </div>
                    </TableCell>
                    {items.map((it, idx) => {
                      const val = vals[idx]
                      const isBest =
                        val !== null &&
                        best !== null &&
                        Math.abs(val - best) < 1e-6 &&
                        valid.length > 1
                      const isBase = it.evaluation.id === baseline.evaluation.id
                      const delta =
                        !isBase && val !== null && baseVal !== null
                          ? val - baseVal
                          : null
                      const improved =
                        delta !== null &&
                        Math.abs(delta) > 1e-5 &&
                        (row.higherIsBetter ? delta > 0 : delta < 0)
                      const worsened =
                        delta !== null &&
                        Math.abs(delta) > 1e-5 &&
                        (row.higherIsBetter ? delta < 0 : delta > 0)

                      return (
                        <TableCell
                          key={it.evaluation.id}
                          className={cn(
                            "text-right tabular-nums",
                            isBest && "bg-emerald-500/[0.07]"
                          )}
                        >
                          <div
                            className={cn(
                              "text-sm font-semibold",
                              isBest && "text-emerald-700 dark:text-emerald-400"
                            )}
                          >
                            {row.format(val)}
                          </div>
                          {isBase ? (
                            <div className="text-[10px] text-muted-foreground">
                              基线
                            </div>
                          ) : delta !== null ? (
                            <div
                              className={cn(
                                "text-[11px] font-medium",
                                improved
                                  ? "text-emerald-600 dark:text-emerald-400"
                                  : worsened
                                    ? "text-destructive"
                                    : "text-muted-foreground"
                              )}
                            >
                              {row.formatDelta(delta)}
                            </div>
                          ) : null}
                        </TableCell>
                      )
                    })}
                  </TableRow>
                )
              })}
            </TableBody>
          </Table>
        </div>
      </CardContent>
    </Card>
  )
}

function PerClassCompareTable({
  classNames,
  items,
}: {
  classNames: readonly string[]
  items: readonly CompareItem[]
}) {
  return (
    <Card className="py-4">
      <CardHeader className="px-5 pb-3">
        <CardTitle className="text-base">
          各类别 AP50 / F1 横向对比（并集 {classNames.length} 类）
        </CardTitle>
      </CardHeader>
      <CardContent className="px-0">
        <div className="overflow-x-auto">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead className="pl-5">类别</TableHead>
                {items.map((it, idx) => (
                  <TableHead key={it.evaluation.id} className="text-right">
                    <span className="inline-flex items-center gap-1.5">
                      <span
                        className="inline-block size-2 rounded-full"
                        style={{ backgroundColor: colorForIndex(idx) }}
                      />
                      {evaluationTitle(it.evaluation)}
                    </span>
                  </TableHead>
                ))}
              </TableRow>
            </TableHeader>
            <TableBody>
              {classNames.map((name) => {
                const rowCells = items.map(
                  (it) => it.classes.find((c) => c.name === name) ?? null
                )
                const ap50s = rowCells
                  .map((c) => c?.ap50 ?? null)
                  .filter((v): v is number => v !== null)
                const bestAP = ap50s.length > 0 ? Math.max(...ap50s) : null

                return (
                  <TableRow key={name}>
                    <TableCell className="pl-5 font-medium">{name}</TableCell>
                    {rowCells.map((cell, idx) => {
                      const ap = cell?.ap50 ?? null
                      const isBest =
                        ap !== null &&
                        bestAP !== null &&
                        Math.abs(ap - bestAP) < 1e-6 &&
                        ap50s.length > 1
                      return (
                        <TableCell
                          key={items[idx].evaluation.id}
                          className={cn(
                            "text-right text-xs tabular-nums",
                            isBest && "bg-emerald-500/[0.07]"
                          )}
                        >
                          {cell ? (
                            <div>
                              <span
                                className={cn(
                                  "font-semibold",
                                  isBest &&
                                    "text-emerald-700 dark:text-emerald-400"
                                )}
                              >
                                AP50 {formatMetricPercent(cell.ap50)}
                              </span>
                              <div className="text-[11px] text-muted-foreground">
                                F1 {formatMetricPercent(cell.f1)} · FN {cell.fn}
                              </div>
                            </div>
                          ) : (
                            <span className="text-muted-foreground">—</span>
                          )}
                        </TableCell>
                      )
                    })}
                  </TableRow>
                )
              })}
            </TableBody>
          </Table>
        </div>
      </CardContent>
    </Card>
  )
}

function SpeedCompareCard({ items }: { items: readonly CompareItem[] }) {
  const withSpeed = items.filter(
    (it) => it.summary.speed && it.summary.speed.total_ms > 0
  )
  if (withSpeed.length === 0) {
    return null
  }
  const maxMs = Math.max(...withSpeed.map((it) => it.summary.speed!.total_ms))

  return (
    <Card className="py-4">
      <CardHeader className="px-5 pb-3">
        <CardTitle className="text-base">推理耗时拆解与吞吐对比</CardTitle>
        <CardDescription>
          按单张图片平均耗时（预处理 + 模型推理 + 后处理）对比部署效率。
        </CardDescription>
      </CardHeader>
      <CardContent className="grid gap-3 px-5 text-xs">
        {withSpeed.map((it, idx) => {
          const sp = it.summary.speed!
          const pct = Math.max(4, Math.round((sp.total_ms / maxMs) * 100))
          return (
            <div key={it.evaluation.id} className="grid gap-1">
              <div className="flex flex-wrap items-center justify-between gap-2 tabular-nums">
                <span className="inline-flex items-center gap-1.5 font-medium">
                  <span
                    className="inline-block size-2 rounded-full"
                    style={{ backgroundColor: colorForIndex(idx) }}
                  />
                  {evaluationTitle(it.evaluation)} ({evaluationModelLabel(it.evaluation)})
                </span>
                <span className="text-muted-foreground">
                  <strong className="text-foreground">
                    {formatMs(sp.total_ms)}
                  </strong>{" "}
                  ({formatFps(sp.fps)}) · 推理 {formatMs(sp.inference_ms)}
                  {sp.device ? ` · ${sp.device}` : ""}
                </span>
              </div>
              <div className="h-2.5 overflow-hidden rounded-full bg-muted">
                <div
                  className="h-full rounded-full transition-all"
                  style={{
                    width: `${pct}%`,
                    backgroundColor: colorForIndex(idx),
                  }}
                />
              </div>
            </div>
          )
        })}
      </CardContent>
    </Card>
  )
}

const DIFF_FILTER_TYPES: readonly CompareDiffType[] = [
  "changed",
  "improved",
  "regressed",
  "fp_reduced",
  "fp_added",
  "outcome_changed",
  "same",
]

function SameSourceDiffTab({
  items,
  mode,
  conf,
  iou,
  defaultBaseId,
}: {
  items: readonly CompareItem[]
  mode: CompareMode
  conf?: number
  iou?: number
  defaultBaseId: number
}) {
  const [baseId, setBaseId] = useState(defaultBaseId)
  const [targetId, setTargetId] = useState(() => {
    const other = items.find((i) => i.evaluation.id !== defaultBaseId)
    return other ? other.evaluation.id : defaultBaseId
  })
  const [diffType, setDiffType] = useState<CompareDiffType>("changed")
  const [search, setSearch] = useState("")
  const [activeDiff, setActiveDiff] = useState<CompareDiffItem | null>(null)

  const diffQuery = useQuery({
    ...evaluationCompareDiffQuery({
      baseId,
      targetId,
      mode,
      conf,
      iou,
      diffType,
      q: search || undefined,
      limit: 60,
    }),
    enabled: baseId !== targetId,
  })

  const baseItem = items.find((i) => i.evaluation.id === baseId) ?? items[0]
  const targetItem = items.find((i) => i.evaluation.id === targetId) ?? items[1]

  const counts = diffQuery.data?.counts ?? {}
  const diffItems = diffQuery.data?.items ?? []

  return (
    <div className="grid gap-4">
      <Card className="py-4">
        <CardContent className="grid gap-3 px-5">
          <div className="flex flex-wrap items-center gap-3 text-xs">
            <div className="flex items-center gap-2">
              <span className="font-medium text-muted-foreground">基线模型：</span>
              <Select
                value={String(baseId)}
                onValueChange={(v) => setBaseId(Number(v))}
              >
                <SelectTrigger className="h-8 w-52 text-xs">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {items.map((it) => (
                    <SelectItem
                      key={it.evaluation.id}
                      value={String(it.evaluation.id)}
                    >
                      {evaluationTitle(it.evaluation)}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>

            <span className="font-semibold text-muted-foreground">VS</span>

            <div className="flex items-center gap-2">
              <span className="font-medium text-muted-foreground">对比模型：</span>
              <Select
                value={String(targetId)}
                onValueChange={(v) => setTargetId(Number(v))}
              >
                <SelectTrigger className="h-8 w-52 text-xs">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {items.map((it) => (
                    <SelectItem
                      key={it.evaluation.id}
                      value={String(it.evaluation.id)}
                    >
                      {evaluationTitle(it.evaluation)}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>

            <div className="relative ml-auto min-w-44 sm:w-56">
              <Search className="pointer-events-none absolute top-1/2 left-2.5 size-3.5 -translate-y-1/2 text-muted-foreground" />
              <Input
                value={search}
                onChange={(e) => setSearch(e.target.value)}
                placeholder="按文件名筛选…"
                className="h-8 pl-8 text-xs"
              />
            </div>
          </div>

          {/* 差异分类 Chip */}
          <div className="flex flex-wrap items-center gap-1.5">
            {DIFF_FILTER_TYPES.map((type) => {
              const active = diffType === type
              const cnt = counts[type]
              return (
                <button
                  key={type}
                  type="button"
                  onClick={() => setDiffType(type)}
                  className={cn(
                    "inline-flex items-center gap-1.5 rounded-full border px-3 py-1 text-xs font-medium transition-colors",
                    active
                      ? "border-primary bg-primary text-primary-foreground"
                      : "bg-background text-muted-foreground hover:bg-muted hover:text-foreground"
                  )}
                >
                  {COMPARE_DIFF_LABELS[type]}
                  {cnt !== undefined && (
                    <span className="opacity-80 tabular-nums">
                      ({formatNumber(cnt)})
                    </span>
                  )}
                </button>
              )
            })}
          </div>
        </CardContent>
      </Card>

      {baseId === targetId ? (
        <div className="rounded-xl border border-dashed py-12 text-center text-sm text-muted-foreground">
          请选择两个不同的评估任务以查看逐图差异。
        </div>
      ) : diffQuery.isPending ? (
        <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-3">
          {[0, 1, 2, 3, 4, 5].map((i) => (
            <Skeleton key={i} className="h-32 rounded-xl" />
          ))}
        </div>
      ) : diffItems.length === 0 ? (
        <div className="rounded-xl border border-dashed py-12 text-center text-sm text-muted-foreground">
          当前差异分类下没有样本。
        </div>
      ) : (
        <div className="grid grid-cols-1 gap-3 md:grid-cols-2 xl:grid-cols-3">
          {diffItems.map((item) => (
            <button
              key={item.sample_key}
              type="button"
              onClick={() => setActiveDiff(item)}
              className="group flex gap-3 overflow-hidden rounded-xl border bg-card p-3 text-left transition-all hover:border-primary/60 hover:shadow-md"
            >
              <div className="relative size-24 shrink-0 overflow-hidden rounded-lg bg-muted">
                <img
                  src={sampleThumbnailUrl(item.base, "sm")}
                  alt={item.file_name}
                  loading="lazy"
                  className="size-full object-cover"
                />
                <span className="absolute right-1 bottom-1 inline-flex items-center gap-0.5 rounded bg-black/70 px-1.5 py-0.5 text-[10px] text-white opacity-0 transition-opacity group-hover:opacity-100">
                  <Eye className="size-3" />
                  并排看框
                </span>
              </div>

              <div className="grid min-w-0 flex-1 content-between gap-1.5 text-xs">
                <div>
                  <div className="truncate font-semibold text-foreground">
                    {item.file_name}
                  </div>
                  <div className="mt-1 flex flex-wrap items-center gap-1">
                    <Badge variant="secondary" className="h-4 px-1.5 text-[10px]">
                      {COMPARE_DIFF_LABELS[item.diff_category]}
                    </Badge>
                    {item.delta_fn !== 0 && (
                      <Badge
                        variant="outline"
                        className={cn(
                          "h-4 px-1.5 text-[10px] tabular-nums",
                          item.delta_fn < 0
                            ? "border-emerald-500/40 bg-emerald-500/10 text-emerald-700"
                            : "border-destructive/40 bg-destructive/10 text-destructive"
                        )}
                      >
                        ΔFN {item.delta_fn > 0 ? `+${item.delta_fn}` : item.delta_fn}
                      </Badge>
                    )}
                    {item.delta_fp !== 0 && (
                      <Badge
                        variant="outline"
                        className={cn(
                          "h-4 px-1.5 text-[10px] tabular-nums",
                          item.delta_fp < 0
                            ? "border-emerald-500/40 bg-emerald-500/10 text-emerald-700"
                            : "border-destructive/40 bg-destructive/10 text-destructive"
                        )}
                      >
                        ΔFP {item.delta_fp > 0 ? `+${item.delta_fp}` : item.delta_fp}
                      </Badge>
                    )}
                  </div>
                </div>

                <div className="flex flex-wrap items-center gap-1.5 text-[11px] text-muted-foreground">
                  <OutcomeBadge
                    outcome={item.base.image_outcome}
                    className="h-4 px-1 text-[10px]"
                  />
                  <span>→</span>
                  <OutcomeBadge
                    outcome={item.target.image_outcome}
                    className="h-4 px-1 text-[10px]"
                  />
                </div>
              </div>
            </button>
          ))}
        </div>
      )}

      <CompareDiffModal
        item={activeDiff}
        baseLabel={evaluationTitle(baseItem.evaluation)}
        targetLabel={evaluationTitle(targetItem.evaluation)}
        onClose={() => setActiveDiff(null)}
      />
    </div>
  )
}
