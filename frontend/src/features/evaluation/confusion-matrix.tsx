/**
 * 交互式检测混淆矩阵热力图（(N+1) × (N+1)）。
 *
 * 行 = 真实标签（含最后一行「背景」，即误检 FP）；
 * 列 = 预测标签（含最后一列「背景」，即漏检 FN）。
 * 支持「数量 / 行归一化(召回率) / 列归一化(精确率)」切换，点击任意非零单元格
 * 直接下钻到「漏检误检样本」并按 (gt_class, pred_class) 精准筛选。
 */
import { useMemo, useState } from "react"
import { ArrowUpRight } from "lucide-react"

import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import type { ConfusionMatrix } from "@/features/evaluation/api"
import { formatNumber } from "@/lib/format"
import { cn } from "@/lib/utils"

type NormMode = "count" | "row" | "col"

const BG_KEY = "background"

function labelDisplay(raw: string): string {
  return raw === BG_KEY ? "背景 (无框)" : raw
}

export function ConfusionMatrixView({
  confusion,
  onDrilldown,
}: {
  confusion: ConfusionMatrix
  onDrilldown?: (gtClass: string, predClass: string) => void
}) {
  const [mode, setMode] = useState<NormMode>("count")
  const [hovered, setHovered] = useState<{ r: number; c: number } | null>(null)

  const labels = confusion.labels ?? []
  const matrix = useMemo(() => confusion.matrix ?? [], [confusion.matrix])
  const size = labels.length

  const stats = useMemo(() => {
    const rowSums = new Array<number>(size).fill(0)
    const colSums = new Array<number>(size).fill(0)
    let maxDiag = 1
    let maxOff = 1
    let total = 0
    for (let r = 0; r < size; r++) {
      for (let c = 0; c < size; c++) {
        const v = matrix[r]?.[c] ?? 0
        rowSums[r] += v
        colSums[c] += v
        total += v
        if (r === c && r < size - 1) {
          maxDiag = Math.max(maxDiag, v)
        } else if (!(r === size - 1 && c === size - 1)) {
          maxOff = Math.max(maxOff, v)
        }
      }
    }
    return { rowSums, colSums, maxDiag, maxOff, total }
  }, [matrix, size])

  if (size === 0 || matrix.length === 0) {
    return (
      <div className="rounded-xl border border-dashed py-12 text-center text-sm text-muted-foreground">
        暂无混淆矩阵数据。
      </div>
    )
  }

  const cellValueText = (r: number, c: number, raw: number): string => {
    if (r === size - 1 && c === size - 1) {
      return "—"
    }
    if (raw === 0) {
      return mode === "count" ? "0" : "0%"
    }
    if (mode === "count") {
      return formatNumber(raw)
    }
    const denom = mode === "row" ? stats.rowSums[r] : stats.colSums[c]
    if (denom <= 0) {
      return "0%"
    }
    return `${((raw / denom) * 100).toFixed(1)}%`
  }

  const cellIntensity = (r: number, c: number, raw: number): number => {
    if (raw <= 0 || (r === size - 1 && c === size - 1)) {
      return 0
    }
    if (mode === "row") {
      return stats.rowSums[r] > 0 ? raw / stats.rowSums[r] : 0
    }
    if (mode === "col") {
      return stats.colSums[c] > 0 ? raw / stats.colSums[c] : 0
    }
    const maxRef = r === c && r < size - 1 ? stats.maxDiag : stats.maxOff
    return Math.min(1, raw / Math.max(1, maxRef))
  }

  const hoveredInfo = (() => {
    if (!hovered) {
      return null
    }
    const { r, c } = hovered
    if (r === size - 1 && c === size - 1) {
      return null
    }
    const raw = matrix[r]?.[c] ?? 0
    const gt = labels[r]
    const pred = labels[c]
    const rowPct = stats.rowSums[r] > 0 ? (raw / stats.rowSums[r]) * 100 : 0
    const colPct = stats.colSums[c] > 0 ? (raw / stats.colSums[c]) * 100 : 0
    let desc: string
    if (gt === BG_KEY) {
      desc = `误检 FP：无真实目标的背景区域被误报为「${pred}」`
    } else if (pred === BG_KEY) {
      desc = `漏检 FN：真实类别「${gt}」未被检出`
    } else if (gt === pred) {
      desc = `正确检出 TP：真实「${gt}」准确预测为「${pred}」`
    } else {
      desc = `误分类：真实「${gt}」被误判为「${pred}」`
    }
    return { gt, pred, raw, rowPct, colPct, desc }
  })()

  return (
    <Card className="py-4">
      <CardHeader className="flex flex-row flex-wrap items-start justify-between gap-3 px-4 pb-3 sm:px-6">
        <div className="space-y-1">
          <CardTitle className="text-base font-semibold">
            目标检测混淆矩阵（含背景行 / 列）
          </CardTitle>
          <CardDescription>
            行表示真实标签，列表示模型预测；绿色对角线为正确检出，橙红色非对角线为误分类、漏检与误检。点击任意非零格可直接查看对应样本。
          </CardDescription>
        </div>

        <div className="inline-flex rounded-lg border bg-muted/40 p-0.5 text-xs">
          {(
            [
              ["count", "目标数量"],
              ["row", "行归一化 (召回率)"],
              ["col", "列归一化 (精确率)"],
            ] as const
          ).map(([key, label]) => (
            <button
              key={key}
              type="button"
              onClick={() => setMode(key)}
              className={cn(
                "rounded-md px-2.5 py-1 font-medium transition-colors",
                mode === key
                  ? "bg-background text-foreground shadow-2xs"
                  : "text-muted-foreground hover:text-foreground"
              )}
            >
              {label}
            </button>
          ))}
        </div>
      </CardHeader>

      <CardContent className="grid gap-3 px-4 sm:px-6">
        <div className="overflow-x-auto rounded-lg border">
          <table className="w-full border-collapse text-xs">
            <thead>
              <tr className="bg-muted/40">
                <th className="sticky left-0 z-10 min-w-32 border-r border-b bg-muted/60 px-3 py-2 text-left font-medium text-muted-foreground">
                  真实 \ 预测
                </th>
                {labels.map((label, c) => (
                  <th
                    key={`col-${label}-${c}`}
                    className={cn(
                      "min-w-22 border-b px-2 py-2 text-center font-medium",
                      label === BG_KEY &&
                        "border-l bg-amber-500/5 text-amber-800 dark:text-amber-400"
                    )}
                    title={labelDisplay(label)}
                  >
                    <div className="mx-auto max-w-24 truncate">
                      {label === BG_KEY ? "漏检 (FN)" : label}
                    </div>
                  </th>
                ))}
                <th className="min-w-18 border-b border-l bg-muted/40 px-2 py-2 text-center font-medium text-muted-foreground">
                  行合计
                </th>
              </tr>
            </thead>
            <tbody>
              {labels.map((gtLabel, r) => {
                const isBgRow = gtLabel === BG_KEY
                return (
                  <tr
                    key={`row-${gtLabel}-${r}`}
                    className={cn(isBgRow && "border-t-2 border-border")}
                  >
                    <th
                      className={cn(
                        "sticky left-0 z-10 border-r border-b px-3 py-2 text-left font-medium",
                        isBgRow
                          ? "bg-rose-500/5 text-rose-800 dark:text-rose-400"
                          : "bg-card text-foreground"
                      )}
                      title={labelDisplay(gtLabel)}
                    >
                      <div className="max-w-36 truncate">
                        {isBgRow ? "背景 (误检 FP)" : gtLabel}
                      </div>
                    </th>
                    {labels.map((predLabel, c) => {
                      const raw = matrix[r]?.[c] ?? 0
                      const isBgCorner = r === size - 1 && c === size - 1
                      const isDiag = r === c && !isBgCorner
                      const intensity = cellIntensity(r, c, raw)
                      const clickable = Boolean(onDrilldown && raw > 0 && !isBgCorner)
                      const bgStyle = (() => {
                        if (isBgCorner || raw === 0) {
                          return undefined
                        }
                        const alpha = (0.12 + intensity * 0.62).toFixed(2)
                        if (isDiag) {
                          return {
                            backgroundColor: `rgba(16, 185, 129, ${alpha})`,
                          }
                        }
                        if (gtLabel === BG_KEY || predLabel === BG_KEY) {
                          return {
                            backgroundColor: `rgba(239, 68, 68, ${alpha})`,
                          }
                        }
                        return {
                          backgroundColor: `rgba(245, 158, 11, ${alpha})`,
                        }
                      })()

                      const highContrast = intensity > 0.65

                      return (
                        <td
                          key={`cell-${r}-${c}`}
                          className={cn(
                            "border-r border-b p-0 text-center tabular-nums last:border-r-0",
                            predLabel === BG_KEY && "border-l"
                          )}
                        >
                          <button
                            type="button"
                            disabled={!clickable}
                            onMouseEnter={() => setHovered({ r, c })}
                            onMouseLeave={() => setHovered(null)}
                            onClick={() => {
                              if (clickable && onDrilldown) {
                                onDrilldown(gtLabel, predLabel)
                              }
                            }}
                            style={bgStyle}
                            className={cn(
                              "group relative flex h-12 w-full flex-col items-center justify-center px-2 transition-all",
                              clickable
                                ? "cursor-pointer hover:ring-2 hover:ring-primary/70 hover:ring-inset"
                                : "cursor-default",
                              raw === 0 && "text-muted-foreground/45",
                              highContrast && "font-semibold text-white"
                            )}
                          >
                            <span className="text-sm leading-tight">
                              {cellValueText(r, c, raw)}
                            </span>
                            {mode !== "count" && raw > 0 && !isBgCorner && (
                              <span
                                className={cn(
                                  "text-[10px] leading-tight",
                                  highContrast
                                    ? "text-white/85"
                                    : "text-muted-foreground"
                                )}
                              >
                                ({formatNumber(raw)})
                              </span>
                            )}
                            {clickable && (
                              <ArrowUpRight className="absolute top-1 right-1 size-3 opacity-0 transition-opacity group-hover:opacity-80" />
                            )}
                          </button>
                        </td>
                      )
                    })}
                    <td className="border-b border-l bg-muted/20 px-2 py-2 text-center font-medium text-muted-foreground tabular-nums">
                      {formatNumber(stats.rowSums[r])}
                    </td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        </div>

        {/* 底部悬停解读条 */}
        <div className="flex min-h-9 flex-wrap items-center justify-between gap-2 rounded-lg bg-muted/40 px-3 py-2 text-xs">
          {hoveredInfo ? (
            <>
              <span className="font-medium text-foreground">
                {hoveredInfo.desc}
              </span>
              <span className="text-muted-foreground tabular-nums">
                数量 <strong className="text-foreground">{formatNumber(hoveredInfo.raw)}</strong>
                {" · "}行占比 {hoveredInfo.rowPct.toFixed(1)}%
                {" · "}列占比 {hoveredInfo.colPct.toFixed(1)}%
                {onDrilldown && hoveredInfo.raw > 0 && " · 点击下钻查看图片"}
              </span>
            </>
          ) : (
            <span className="text-muted-foreground">
              将鼠标悬停在矩阵单元格上可查看精确占比；点击任意非零格将自动跳转至「漏检误检样本」查看相关图片。
            </span>
          )}
        </div>
      </CardContent>
    </Card>
  )
}
