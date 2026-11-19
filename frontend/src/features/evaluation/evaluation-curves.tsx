/**
 * 模型评估交互式曲线图（纯 SVG，零第三方图表依赖，完全离线可用）。
 *
 * 包含：
 * 1. `EvaluationCurves`：单评估 2×2 曲线套件（PR、F1-Conf、Precision-Conf、Recall-Conf），
 *    支持按类别显隐切换、鼠标悬停精确读数、当前置信度竖线标记，以及点击置信度曲线快速跳点。
 * 2. `CompareCurves`：多模型叠加对比曲线（PR All + F1-Conf All）。
 */
import { useMemo, useState, type MouseEvent } from "react"

import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"
import type { CompareItem, CurvesData, CurveSeries } from "@/features/evaluation/api"
import {
  colorForIndex,
  evaluationModelLabel,
  evaluationTitle,
  formatMetricPercent,
  formatThreshold,
} from "@/features/evaluation/format"
import { cn } from "@/lib/utils"

type ChartKind = "pr" | "f1" | "precision" | "recall"

type ResolvedSeries = {
  id: string
  label: string
  color: string
  isAll: boolean
  dashed?: boolean
  x: number[]
  y: number[]
  badge?: string
}

/** 在单调或有序横轴序列中按线性插值取 y(targetX)。 */
function interpolateY(xs: readonly number[], ys: readonly number[], targetX: number): number | null {
  if (xs.length === 0 || xs.length !== ys.length) {
    return null
  }
  if (xs.length === 1) {
    return ys[0]
  }
  // 找到距离 targetX 最近的相邻两点做线性插值（兼容升序与降序 x，如 PR 曲线）。
  let bestIdx = 0
  let bestDist = Infinity
  for (let i = 0; i < xs.length; i++) {
    const d = Math.abs(xs[i] - targetX)
    if (d < bestDist) {
      bestDist = d
      bestIdx = i
    }
  }
  // 尝试用相邻区间线性插值
  const neighbors = [bestIdx - 1, bestIdx + 1].filter(
    (idx) => idx >= 0 && idx < xs.length
  )
  for (const nb of neighbors) {
    const x0 = xs[bestIdx]
    const x1 = xs[nb]
    if ((x0 <= targetX && targetX <= x1) || (x1 <= targetX && targetX <= x0)) {
      const span = x1 - x0
      if (Math.abs(span) < 1e-9) {
        return ys[bestIdx]
      }
      const t = (targetX - x0) / span
      return ys[bestIdx] + t * (ys[nb] - ys[bestIdx])
    }
  }
  return ys[bestIdx]
}

function buildResolvedSeries(
  raw: readonly CurveSeries[] | undefined,
  apMap?: Record<string, number>,
  map50?: number
): ResolvedSeries[] {
  if (!raw || raw.length === 0) {
    return []
  }
  let classIdx = 0
  return raw
    .filter((s) => s.x.length > 0 && s.y.length > 0)
    .map((s) => {
      const isAll = s.name === "all"
      const color = isAll
        ? "#0f172a"
        : colorForIndex(classIdx++, s.color)
      const badge =
        isAll && map50 !== undefined
          ? formatMetricPercent(map50)
          : apMap && s.name in apMap
            ? formatMetricPercent(apMap[s.name])
            : undefined
      return {
        id: s.name,
        label: isAll ? "全部类别 (All)" : s.name,
        color,
        isAll,
        x: s.x,
        y: s.y,
        badge,
      }
    })
}

export function EvaluationCurves({
  curves,
  activeConf,
  onSelectConf,
}: {
  curves: CurvesData
  activeConf: number
  onSelectConf?: (conf: number) => void
}) {
  const allSeriesNames = useMemo(() => {
    const names = new Set<string>()
    for (const list of [curves.pr, curves.f1, curves.precision, curves.recall]) {
      for (const s of list ?? []) {
        names.add(s.name)
      }
    }
    return Array.from(names)
  }, [curves])

  // 默认展示 "all" 及前 6 个类别，避免多类场景曲线过于拥挤
  const [hidden, setHidden] = useState<ReadonlySet<string>>(() => {
    const nonAll = allSeriesNames.filter((n) => n !== "all")
    if (nonAll.length <= 6) {
      return new Set()
    }
    return new Set(nonAll.slice(6))
  })

  const prSeries = useMemo(
    () => buildResolvedSeries(curves.pr, curves.class_ap50, curves.map50),
    [curves.pr, curves.class_ap50, curves.map50]
  )
  const f1Series = useMemo(
    () => buildResolvedSeries(curves.f1),
    [curves.f1]
  )
  const pSeries = useMemo(
    () => buildResolvedSeries(curves.precision),
    [curves.precision]
  )
  const rSeries = useMemo(
    () => buildResolvedSeries(curves.recall),
    [curves.recall]
  )

  const legendItems = useMemo(() => {
    const source =
      prSeries.length > 0
        ? prSeries
        : f1Series.length > 0
          ? f1Series
          : pSeries
    return source
  }, [prSeries, f1Series, pSeries])

  const toggleSeries = (id: string) => {
    setHidden((prev) => {
      const next = new Set(prev)
      if (next.has(id)) {
        next.delete(id)
      } else if (next.size < Math.max(1, legendItems.length - 1)) {
        next.add(id)
      }
      return next
    })
  }

  const showAll = () => setHidden(new Set())
  const onlyAll = () =>
    setHidden(new Set(legendItems.filter((s) => !s.isAll).map((s) => s.id)))

  const hasAny =
    prSeries.length > 0 ||
    f1Series.length > 0 ||
    pSeries.length > 0 ||
    rSeries.length > 0

  if (!hasAny) {
    return (
      <div className="rounded-xl border border-dashed py-12 text-center text-sm text-muted-foreground">
        暂无评估曲线数据（可能数据源中不含任何标注框或评估尚未完成）。
      </div>
    )
  }

  return (
    <div className="grid gap-4">
      {legendItems.length > 1 && (
        <div className="flex flex-wrap items-center justify-between gap-2 rounded-xl border bg-card px-4 py-2.5">
          <div className="flex flex-wrap items-center gap-1.5">
            <span className="mr-1 text-xs font-medium text-muted-foreground">
              曲线图例：
            </span>
            {legendItems.map((s) => {
              const active = !hidden.has(s.id)
              return (
                <button
                  key={s.id}
                  type="button"
                  onClick={() => toggleSeries(s.id)}
                  className={cn(
                    "inline-flex items-center gap-1.5 rounded-md border px-2 py-0.5 text-xs transition-all",
                    active
                      ? "bg-background text-foreground shadow-2xs"
                      : "border-transparent bg-muted/50 text-muted-foreground opacity-55 hover:opacity-80"
                  )}
                >
                  <span
                    className={cn(
                      "inline-block rounded-full",
                      s.isAll ? "h-1 w-3 bg-foreground" : "size-2"
                    )}
                    style={s.isAll ? undefined : { backgroundColor: s.color }}
                  />
                  <span className={cn(s.isAll && "font-semibold")}>
                    {s.label}
                  </span>
                  {s.badge && (
                    <span className="text-[11px] text-muted-foreground tabular-nums">
                      {s.badge}
                    </span>
                  )}
                </button>
              )
            })}
          </div>
          <div className="flex items-center gap-1.5">
            <button
              type="button"
              onClick={showAll}
              className="rounded px-2 py-0.5 text-xs text-muted-foreground hover:bg-muted hover:text-foreground"
            >
              全部显示
            </button>
            <button
              type="button"
              onClick={onlyAll}
              className="rounded px-2 py-0.5 text-xs text-muted-foreground hover:bg-muted hover:text-foreground"
            >
              仅看总计
            </button>
          </div>
        </div>
      )}

      <div className="grid grid-cols-1 gap-4 xl:grid-cols-2">
        <SingleCurveChart
          kind="pr"
          title="PR 曲线（精确率 - 召回率）"
          subtitle={`mAP50 = ${formatMetricPercent(curves.map50)} · mAP50-95 = ${formatMetricPercent(curves.map50_95)}`}
          xLabel="召回率 (Recall)"
          yLabel="精确率 (Precision)"
          series={prSeries.filter((s) => !hidden.has(s.id))}
        />
        <SingleCurveChart
          kind="f1"
          title="F1 - 置信度曲线"
          subtitle={`最佳 F1 = ${formatMetricPercent(curves.best_f1)} @ conf ${formatThreshold(curves.best_f1_conf)}`}
          xLabel="置信度阈值 (Confidence)"
          yLabel="F1 分数"
          series={f1Series.filter((s) => !hidden.has(s.id))}
          activeX={activeConf}
          bestPoint={
            curves.best_f1 > 0
              ? { x: curves.best_f1_conf, y: curves.best_f1 }
              : undefined
          }
          onClickX={onSelectConf}
        />
        <SingleCurveChart
          kind="precision"
          title="精确率 - 置信度曲线 (P-Conf)"
          subtitle="点击曲线横轴可快速切换当前置信度阈值"
          xLabel="置信度阈值 (Confidence)"
          yLabel="精确率 (Precision)"
          series={pSeries.filter((s) => !hidden.has(s.id))}
          activeX={activeConf}
          onClickX={onSelectConf}
        />
        <SingleCurveChart
          kind="recall"
          title="召回率 - 置信度曲线 (R-Conf)"
          subtitle="点击曲线横轴可快速切换当前置信度阈值"
          xLabel="置信度阈值 (Confidence)"
          yLabel="召回率 (Recall)"
          series={rSeries.filter((s) => !hidden.has(s.id))}
          activeX={activeConf}
          onClickX={onSelectConf}
        />
      </div>
    </div>
  )
}

/** 多模型对比叠加曲线：PR (All) 与 F1-Conf (All)。 */
export function CompareCurves({
  items,
  unifiedConf,
  onSelectConf,
}: {
  items: readonly CompareItem[]
  unifiedConf?: number
  onSelectConf?: (conf: number) => void
}) {
  const prSeries = useMemo<ResolvedSeries[]>(() => {
    const out: ResolvedSeries[] = []
    items.forEach((item, idx) => {
      if (!item.pr_all || item.pr_all.x.length === 0) {
        return
      }
      out.push({
        id: `eval-${item.evaluation.id}`,
        label: `${evaluationTitle(item.evaluation)} (${evaluationModelLabel(item.evaluation)})`,
        color: colorForIndex(idx),
        isAll: false,
        x: item.pr_all.x,
        y: item.pr_all.y,
        badge: `mAP50 ${formatMetricPercent(item.summary.map50)}`,
      })
    })
    return out
  }, [items])

  const f1Series = useMemo<ResolvedSeries[]>(() => {
    const out: ResolvedSeries[] = []
    items.forEach((item, idx) => {
      if (!item.f1_all || item.f1_all.x.length === 0) {
        return
      }
      out.push({
        id: `eval-${item.evaluation.id}`,
        label: `${evaluationTitle(item.evaluation)} (${evaluationModelLabel(item.evaluation)})`,
        color: colorForIndex(idx),
        isAll: false,
        x: item.f1_all.x,
        y: item.f1_all.y,
        badge: `F1 ${formatMetricPercent(item.summary.best_f1)}`,
      })
    })
    return out
  }, [items])

  return (
    <div className="grid grid-cols-1 gap-4 xl:grid-cols-2">
      <SingleCurveChart
        kind="pr"
        title="多模型 PR 曲线对比（All）"
        subtitle="越靠右上角说明整体检测精度与召回表现越优"
        xLabel="召回率 (Recall)"
        yLabel="精确率 (Precision)"
        series={prSeries}
        showInlineLegend
      />
      <SingleCurveChart
        kind="f1"
        title="多模型 F1 - 置信度曲线对比（All）"
        subtitle={
          unifiedConf !== undefined
            ? `当前统一置信度：${formatThreshold(unifiedConf)}（点击曲线可快速调整）`
            : "展示各模型在不同置信度下的 F1 稳定性与峰值区间"
        }
        xLabel="置信度阈值 (Confidence)"
        yLabel="F1 分数"
        series={f1Series}
        activeX={unifiedConf}
        onClickX={onSelectConf}
        showInlineLegend
      />
    </div>
  )
}

function SingleCurveChart({
  kind,
  title,
  subtitle,
  xLabel,
  yLabel,
  series,
  activeX,
  bestPoint,
  onClickX,
  showInlineLegend = false,
}: {
  kind: ChartKind
  title: string
  subtitle?: string
  xLabel: string
  yLabel: string
  series: readonly ResolvedSeries[]
  activeX?: number
  bestPoint?: { x: number; y: number }
  onClickX?: (x: number) => void
  showInlineLegend?: boolean
}) {
  const [hoverX, setHoverX] = useState<number | null>(null)

  const width = 560
  const height = 240
  const padLeft = 48
  const padRight = 18
  const padTop = 16
  const padBottom = 34
  const plotW = width - padLeft - padRight
  const plotH = height - padTop - padBottom

  const xOf = (val: number) =>
    padLeft + Math.max(0, Math.min(1, val)) * plotW
  const yOf = (val: number) =>
    padTop + plotH - Math.max(0, Math.min(1, val)) * plotH

  const extractXFromEvent = (event: MouseEvent<SVGSVGElement>): number | null => {
    const rect = event.currentTarget.getBoundingClientRect()
    if (rect.width <= 0) {
      return null
    }
    const svgX = ((event.clientX - rect.left) / rect.width) * width
    const ratio = (svgX - padLeft) / plotW
    if (ratio < -0.02 || ratio > 1.02) {
      return null
    }
    return Math.max(0, Math.min(1, ratio))
  }

  const handleMouseMove = (event: MouseEvent<SVGSVGElement>) => {
    setHoverX(extractXFromEvent(event))
  }

  const handleClick = (event: MouseEvent<SVGSVGElement>) => {
    if (!onClickX) {
      return
    }
    const x = extractXFromEvent(event)
    if (x !== null) {
      const clamped = Math.max(0.05, Math.min(0.99, Math.round(x * 100) / 100))
      onClickX(clamped)
    }
  }

  const ticks = [0, 0.25, 0.5, 0.75, 1]
  const isPR = kind === "pr"

  return (
    <Card className="gap-2.5 py-4">
      <CardHeader className="px-4 pb-0">
        <div className="flex flex-wrap items-baseline justify-between gap-2">
          <CardTitle className="text-sm font-medium">{title}</CardTitle>
          {subtitle && (
            <span className="text-xs text-muted-foreground tabular-nums">
              {subtitle}
            </span>
          )}
        </div>
        {showInlineLegend && series.length > 0 && (
          <div className="mt-1.5 flex flex-wrap items-center gap-x-3 gap-y-1">
            {series.map((s) => (
              <span
                key={s.id}
                className="inline-flex items-center gap-1.5 text-xs text-muted-foreground"
              >
                <span
                  className="inline-block size-2 rounded-full"
                  style={{ backgroundColor: s.color }}
                />
                <span className="max-w-44 truncate text-foreground">
                  {s.label}
                </span>
                {s.badge && (
                  <span className="tabular-nums">({s.badge})</span>
                )}
              </span>
            ))}
          </div>
        )}
      </CardHeader>

      <CardContent className="grid gap-2 px-4">
        {series.length === 0 ? (
          <div className="py-12 text-center text-xs text-muted-foreground">
            暂无该项曲线数据
          </div>
        ) : (
          <>
            <svg
              viewBox={`0 0 ${width} ${height}`}
              className={cn(
                "h-56 w-full overflow-visible select-none",
                onClickX && "cursor-crosshair"
              )}
              onMouseMove={handleMouseMove}
              onMouseLeave={() => setHoverX(null)}
              onClick={handleClick}
              role="img"
              aria-label={title}
            >
              {/* 水平网格线 & Y 轴刻度 */}
              {ticks.map((t) => {
                const y = yOf(t)
                return (
                  <g key={`y-${t}`}>
                    <line
                      x1={padLeft}
                      x2={width - padRight}
                      y1={y}
                      y2={y}
                      className="stroke-border"
                      strokeDasharray={t === 0 ? undefined : "3 3"}
                      strokeWidth={1}
                    />
                    <text
                      x={padLeft - 6}
                      y={y + 3.5}
                      textAnchor="end"
                      className="fill-muted-foreground text-[10px] tabular-nums"
                    >
                      {Math.round(t * 100)}%
                    </text>
                  </g>
                )
              })}

              {/* 垂直网格线 & X 轴刻度 */}
              {ticks.map((t) => {
                const x = xOf(t)
                return (
                  <g key={`x-${t}`}>
                    <line
                      x1={x}
                      x2={x}
                      y1={padTop}
                      y2={height - padBottom}
                      className="stroke-border/70"
                      strokeDasharray={t === 0 ? undefined : "3 3"}
                      strokeWidth={1}
                    />
                    <text
                      x={x}
                      y={height - padBottom + 13}
                      textAnchor="middle"
                      className="fill-muted-foreground text-[10px] tabular-nums"
                    >
                      {isPR ? `${Math.round(t * 100)}%` : t.toFixed(2)}
                    </text>
                  </g>
                )
              })}

              {/* 轴标题 */}
              <text
                x={padLeft + plotW / 2}
                y={height - 3}
                textAnchor="middle"
                className="fill-muted-foreground text-[10px]"
              >
                {xLabel}
              </text>
              <text
                x={12}
                y={padTop + plotH / 2}
                textAnchor="middle"
                transform={`rotate(-90 12 ${padTop + plotH / 2})`}
                className="fill-muted-foreground text-[10px]"
              >
                {yLabel}
              </text>

              {/* 曲线折线 */}
              {series.map((s) => {
                const d = s.x
                  .map(
                    (xv, idx) =>
                      `${idx === 0 ? "M" : "L"}${xOf(xv).toFixed(1)},${yOf(s.y[idx]).toFixed(1)}`
                  )
                  .join(" ")
                return (
                  <path
                    key={s.id}
                    d={d}
                    fill="none"
                    stroke={s.isAll ? "currentColor" : s.color}
                    className={cn(s.isAll && "text-foreground")}
                    strokeWidth={s.isAll ? 2.6 : 1.75}
                    strokeOpacity={s.isAll ? 1 : 0.85}
                    strokeLinecap="round"
                    strokeLinejoin="round"
                  />
                )
              })}

              {/* 最佳 F1 峰值点 */}
              {bestPoint && (
                <g>
                  <circle
                    cx={xOf(bestPoint.x)}
                    cy={yOf(bestPoint.y)}
                    r={4.5}
                    fill="#10b981"
                    stroke="var(--background)"
                    strokeWidth={1.8}
                  />
                </g>
              )}

              {/* 当前激活置信度竖线 */}
              {activeX !== undefined && (
                <g>
                  <line
                    x1={xOf(activeX)}
                    x2={xOf(activeX)}
                    y1={padTop}
                    y2={height - padBottom}
                    stroke="#2563eb"
                    strokeWidth={1.6}
                    strokeDasharray="4 3"
                  />
                  <rect
                    x={Math.min(
                      width - padRight - 58,
                      Math.max(padLeft, xOf(activeX) - 29)
                    )}
                    y={padTop - 2}
                    width={58}
                    height={15}
                    rx={3}
                    fill="#2563eb"
                  />
                  <text
                    x={
                      Math.min(
                        width - padRight - 58,
                        Math.max(padLeft, xOf(activeX) - 29)
                      ) + 29
                    }
                    y={padTop + 9}
                    textAnchor="middle"
                    fill="#ffffff"
                    className="text-[9.5px] font-medium tabular-nums"
                  >
                    conf {activeX.toFixed(2)}
                  </text>
                </g>
              )}

              {/* 鼠标悬停十字线与读数点 */}
              {hoverX !== null && (
                <g>
                  <line
                    x1={xOf(hoverX)}
                    x2={xOf(hoverX)}
                    y1={padTop}
                    y2={height - padBottom}
                    className="stroke-foreground/35"
                    strokeDasharray="2 2"
                  />
                  {series.map((s) => {
                    const yv = interpolateY(s.x, s.y, hoverX)
                    if (yv === null) {
                      return null
                    }
                    return (
                      <circle
                        key={s.id}
                        cx={xOf(hoverX)}
                        cy={yOf(yv)}
                        r={3.5}
                        fill={s.isAll ? "currentColor" : s.color}
                        className={cn(s.isAll && "text-foreground")}
                        stroke="var(--background)"
                        strokeWidth={1.4}
                      />
                    )
                  })}
                </g>
              )}
            </svg>

            {/* 底部精确读数条 */}
            <div className="flex min-h-6 flex-wrap items-center gap-x-3 gap-y-1 text-xs text-muted-foreground tabular-nums">
              {hoverX !== null ? (
                <>
                  <span className="font-medium text-foreground">
                    {isPR
                      ? `Recall ${formatMetricPercent(hoverX)}`
                      : `Conf ${formatThreshold(hoverX)}`}
                  </span>
                  {series.slice(0, 6).map((s) => {
                    const yv = interpolateY(s.x, s.y, hoverX)
                    if (yv === null) {
                      return null
                    }
                    return (
                      <span
                        key={s.id}
                        className="inline-flex items-center gap-1"
                      >
                        <span
                          className={cn(
                            "inline-block size-1.5 rounded-full",
                            s.isAll && "bg-foreground"
                          )}
                          style={
                            s.isAll ? undefined : { backgroundColor: s.color }
                          }
                        />
                        <span className="max-w-28 truncate">{s.label}:</span>
                        <strong className="font-medium text-foreground">
                          {formatMetricPercent(yv)}
                        </strong>
                      </span>
                    )
                  })}
                  {series.length > 6 && (
                    <span>+{series.length - 6} 条曲线</span>
                  )}
                </>
              ) : (
                <span>
                  {onClickX
                    ? "悬停查看各曲线精确值；点击横轴任意位置可直接跳转到该置信度。"
                    : "悬停曲线查看精确坐标数值。"}
                </span>
              )}
            </div>
          </>
        )}
      </CardContent>
    </Card>
  )
}
