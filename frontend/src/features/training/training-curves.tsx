/**
 * 训练指标实时曲线图（纯 SVG，零第三方图表依赖，兼容完全离线内网环境）。
 *
 * 展示：
 * 1. 验证评估指标曲线（mAP50、mAP50-95、Precision、Recall）
 * 2. 训练与验证损失曲线（box_loss、cls_loss、dfl_loss）
 * 3. 学习率调度曲线（lr）
 */
import { useMemo, useState, type MouseEvent } from "react"

import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"
import type { EpochMetrics } from "@/features/training/api"
import { formatLoss, formatLr, formatMap } from "@/features/training/format"
import { cn } from "@/lib/utils"

type SeriesDef = {
  key: keyof EpochMetrics
  label: string
  color: string
  dashed?: boolean
  format: (value: number) => string
}

const METRIC_SERIES: readonly SeriesDef[] = [
  {
    key: "map50",
    label: "mAP50",
    color: "#10b981",
    format: (v) => formatMap(v),
  },
  {
    key: "map50_95",
    label: "mAP50-95",
    color: "#2563eb",
    format: (v) => formatMap(v),
  },
  {
    key: "precision",
    label: "精确率 P",
    color: "#f59e0b",
    dashed: true,
    format: (v) => formatMap(v),
  },
  {
    key: "recall",
    label: "召回率 R",
    color: "#8b5cf6",
    dashed: true,
    format: (v) => formatMap(v),
  },
]

const LOSS_SERIES: readonly SeriesDef[] = [
  {
    key: "train_box_loss",
    label: "训练 Box Loss",
    color: "#2563eb",
    format: (v) => formatLoss(v),
  },
  {
    key: "train_cls_loss",
    label: "训练 Cls Loss",
    color: "#10b981",
    format: (v) => formatLoss(v),
  },
  {
    key: "train_dfl_loss",
    label: "训练 DFL Loss",
    color: "#f59e0b",
    format: (v) => formatLoss(v),
  },
  {
    key: "val_box_loss",
    label: "验证 Box Loss",
    color: "#ef4444",
    dashed: true,
    format: (v) => formatLoss(v),
  },
  {
    key: "val_cls_loss",
    label: "验证 Cls Loss",
    color: "#8b5cf6",
    dashed: true,
    format: (v) => formatLoss(v),
  },
  {
    key: "val_dfl_loss",
    label: "验证 DFL Loss",
    color: "#ec4899",
    dashed: true,
    format: (v) => formatLoss(v),
  },
]

const LR_SERIES: readonly SeriesDef[] = [
  {
    key: "lr",
    label: "学习率 (LR)",
    color: "#0ea5e9",
    format: (v) => formatLr(v),
  },
]

export function TrainingCurves({
  epochs,
}: {
  epochs: readonly EpochMetrics[]
}) {
  if (epochs.length === 0) {
    return (
      <div className="rounded-xl border border-dashed py-12 text-center text-sm text-muted-foreground">
        暂无轮次指标数据，首个训练轮次（Epoch 1）完成后将自动绘制实时曲线。
      </div>
    )
  }

  return (
    <div className="grid grid-cols-1 gap-4 xl:grid-cols-2">
      <MultiSeriesChart
        title="验证集评估指标 (mAP / P / R)"
        epochs={epochs}
        series={METRIC_SERIES}
        fixedYRange={[0, 1]}
        yTickFormat={(v) => `${Math.round(v * 100)}%`}
      />
      <MultiSeriesChart
        title="训练与验证损失 (Loss)"
        epochs={epochs}
        series={LOSS_SERIES}
        yTickFormat={(v) => (v >= 10 ? v.toFixed(1) : v.toFixed(2))}
      />
      <MultiSeriesChart
        title="学习率变化曲线 (Learning Rate)"
        epochs={epochs}
        series={LR_SERIES}
        yTickFormat={(v) => formatLr(v)}
        className="xl:col-span-2"
      />
    </div>
  )
}

function MultiSeriesChart({
  title,
  epochs,
  series,
  fixedYRange,
  yTickFormat,
  className,
}: {
  title: string
  epochs: readonly EpochMetrics[]
  series: readonly SeriesDef[]
  fixedYRange?: [number, number]
  yTickFormat: (value: number) => string
  className?: string
}) {
  const [hidden, setHidden] = useState<ReadonlySet<string>>(new Set())
  const [hoverIdx, setHoverIdx] = useState<number | null>(null)

  const availableSeries = useMemo(
    () =>
      series.filter((s) =>
        epochs.some((ep) => {
          const val = ep[s.key]
          return typeof val === "number" && Number.isFinite(val)
        })
      ),
    [epochs, series]
  )

  const visibleSeries = useMemo(
    () => availableSeries.filter((s) => !hidden.has(String(s.key))),
    [availableSeries, hidden]
  )

  const width = 600
  const height = 220
  const padLeft = 52
  const padRight = 18
  const padTop = 14
  const padBottom = 28
  const plotW = width - padLeft - padRight
  const plotH = height - padTop - padBottom

  const minEpoch = epochs[0]?.epoch ?? 1
  const maxEpoch = Math.max(
    minEpoch + 1,
    epochs[epochs.length - 1]?.total_epochs ?? 0,
    epochs[epochs.length - 1]?.epoch ?? minEpoch + 1
  )

  const [yMin, yMax] = useMemo(() => {
    if (fixedYRange) {
      return fixedYRange
    }
    let lo = Infinity
    let hi = -Infinity
    for (const s of visibleSeries) {
      for (const ep of epochs) {
        const v = ep[s.key]
        if (typeof v === "number" && Number.isFinite(v)) {
          lo = Math.min(lo, v)
          hi = Math.max(hi, v)
        }
      }
    }
    if (!Number.isFinite(lo) || !Number.isFinite(hi)) {
      return [0, 1]
    }
    if (lo === hi) {
      const delta = lo === 0 ? 1 : Math.abs(lo) * 0.1
      return [Math.max(0, lo - delta), hi + delta]
    }
    const pad = (hi - lo) * 0.08
    return [Math.max(0, lo - pad), hi + pad]
  }, [epochs, fixedYRange, visibleSeries])

  const xOf = (epoch: number) =>
    padLeft + ((epoch - minEpoch) / Math.max(1, maxEpoch - minEpoch)) * plotW

  const yOf = (val: number) =>
    padTop + plotH - ((val - yMin) / Math.max(1e-9, yMax - yMin)) * plotH

  const toggleSeries = (key: string) => {
    setHidden((prev) => {
      const next = new Set(prev)
      if (next.has(key)) {
        next.delete(key)
      } else if (next.size < availableSeries.length - 1) {
        next.add(key)
      }
      return next
    })
  }

  const handleMouseMove = (event: MouseEvent<SVGSVGElement>) => {
    if (epochs.length === 0) {
      return
    }
    const rect = event.currentTarget.getBoundingClientRect()
    if (rect.width <= 0) {
      return
    }
    const svgX = ((event.clientX - rect.left) / rect.width) * width
    let best = 0
    let bestDist = Infinity
    epochs.forEach((ep, idx) => {
      const dist = Math.abs(xOf(ep.epoch) - svgX)
      if (dist < bestDist) {
        bestDist = dist
        best = idx
      }
    })
    setHoverIdx(best)
  }

  const hoveredEpoch = hoverIdx !== null ? epochs[hoverIdx] : null
  const yTicks = [0, 0.25, 0.5, 0.75, 1].map(
    (ratio) => yMin + (yMax - yMin) * ratio
  )

  return (
    <Card className={cn("gap-3 py-4", className)}>
      <CardHeader className="flex flex-row flex-wrap items-center justify-between gap-2 px-4 pb-0">
        <CardTitle className="text-sm font-medium">{title}</CardTitle>
        <div className="flex flex-wrap items-center gap-1.5">
          {availableSeries.map((s) => {
            const key = String(s.key)
            const active = !hidden.has(key)
            return (
              <button
                key={key}
                type="button"
                onClick={() => toggleSeries(key)}
                className={cn(
                  "inline-flex items-center gap-1.5 rounded-md border px-2 py-0.5 text-xs transition-opacity",
                  active
                    ? "bg-background text-foreground"
                    : "border-transparent bg-muted/50 text-muted-foreground opacity-55"
                )}
              >
                <span
                  className="inline-block size-2 rounded-full"
                  style={{ backgroundColor: s.color }}
                />
                {s.label}
              </button>
            )
          })}
        </div>
      </CardHeader>

      <CardContent className="grid gap-2 px-4">
        {availableSeries.length === 0 ? (
          <div className="py-10 text-center text-xs text-muted-foreground">
            当前训练任务未产生该类指标数据
          </div>
        ) : (
          <>
            <svg
              viewBox={`0 0 ${width} ${height}`}
              className="h-52 w-full overflow-visible select-none"
              onMouseMove={handleMouseMove}
              onMouseLeave={() => setHoverIdx(null)}
              role="img"
              aria-label={title}
            >
              {yTicks.map((tick, idx) => {
                const y = yOf(tick)
                return (
                  <g key={idx}>
                    <line
                      x1={padLeft}
                      x2={width - padRight}
                      y1={y}
                      y2={y}
                      className="stroke-border"
                      strokeDasharray={idx === 0 ? undefined : "3 3"}
                      strokeWidth={1}
                    />
                    <text
                      x={padLeft - 6}
                      y={y + 3.5}
                      textAnchor="end"
                      className="fill-muted-foreground text-[10px] tabular-nums"
                    >
                      {yTickFormat(tick)}
                    </text>
                  </g>
                )
              })}

              <text
                x={padLeft}
                y={height - 6}
                textAnchor="start"
                className="fill-muted-foreground text-[10px] tabular-nums"
              >
                Epoch {minEpoch}
              </text>
              <text
                x={width - padRight}
                y={height - 6}
                textAnchor="end"
                className="fill-muted-foreground text-[10px] tabular-nums"
              >
                Epoch {maxEpoch}
              </text>

              {visibleSeries.map((s) => {
                const pts = epochs
                  .map((ep) => {
                    const v = ep[s.key]
                    return typeof v === "number" && Number.isFinite(v)
                      ? { x: xOf(ep.epoch), y: yOf(v), v }
                      : null
                  })
                  .filter(
                    (p): p is { x: number; y: number; v: number } => p !== null
                  )
                if (pts.length === 0) {
                  return null
                }
                const d = pts
                  .map(
                    (p, idx) =>
                      `${idx === 0 ? "M" : "L"}${p.x.toFixed(1)},${p.y.toFixed(1)}`
                  )
                  .join(" ")
                return (
                  <g key={String(s.key)}>
                    <path
                      d={d}
                      fill="none"
                      stroke={s.color}
                      strokeWidth={2}
                      strokeDasharray={s.dashed ? "4 3" : undefined}
                      strokeLinecap="round"
                      strokeLinejoin="round"
                    />
                    {pts.length === 1 && (
                      <circle
                        cx={pts[0].x}
                        cy={pts[0].y}
                        r={3.5}
                        fill={s.color}
                      />
                    )}
                  </g>
                )
              })}

              {hoveredEpoch && (
                <g>
                  <line
                    x1={xOf(hoveredEpoch.epoch)}
                    x2={xOf(hoveredEpoch.epoch)}
                    y1={padTop}
                    y2={height - padBottom}
                    className="stroke-foreground/30"
                    strokeDasharray="2 2"
                  />
                  {visibleSeries.map((s) => {
                    const v = hoveredEpoch[s.key]
                    if (typeof v !== "number" || !Number.isFinite(v)) {
                      return null
                    }
                    return (
                      <circle
                        key={String(s.key)}
                        cx={xOf(hoveredEpoch.epoch)}
                        cy={yOf(v)}
                        r={4}
                        fill={s.color}
                        stroke="var(--background)"
                        strokeWidth={1.5}
                      />
                    )
                  })}
                </g>
              )}
            </svg>

            <div className="flex min-h-6 flex-wrap items-center gap-x-3 gap-y-1 text-xs text-muted-foreground tabular-nums">
              {hoveredEpoch ? (
                <>
                  <span className="font-medium text-foreground">
                    轮次 {hoveredEpoch.epoch}
                  </span>
                  {visibleSeries.map((s) => {
                    const v = hoveredEpoch[s.key]
                    if (typeof v !== "number" || !Number.isFinite(v)) {
                      return null
                    }
                    return (
                      <span
                        key={String(s.key)}
                        className="inline-flex items-center gap-1"
                      >
                        <span
                          className="inline-block size-1.5 rounded-full"
                          style={{ backgroundColor: s.color }}
                        />
                        {s.label}:{" "}
                        <strong className="font-medium text-foreground">
                          {s.format(v)}
                        </strong>
                      </span>
                    )
                  })}
                </>
              ) : (
                <span>
                  鼠标悬停曲线可查看每一轮的精确数值；点击上方图例可显示或隐藏曲线。
                </span>
              )}
            </div>
          </>
        )}
      </CardContent>
    </Card>
  )
}
