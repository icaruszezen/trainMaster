import { describe, expect, it } from "vitest"

import type { NodeHistoryPoint } from "@/features/training/compute-api"
import {
  gpuUtilizationSeries,
  historySeries,
  historySpanSeconds,
  seriesStats,
  sparklineAreaPath,
  sparklineLinePath,
  sparklineSegments,
} from "@/features/training/sparkline-points"

function point(
  t: string,
  cpu: number | null,
  gpus: NodeHistoryPoint["gpus"] = []
): NodeHistoryPoint {
  return { t, cpu_percent: cpu, memory_used_bytes: null, gpus }
}

const history: NodeHistoryPoint[] = [
  point("2026-11-02T07:30:00Z", 10, [
    { index: 0, utilization_percent: 80, memory_used_bytes: 1 },
  ]),
  point("2026-11-02T07:30:10Z", null, [
    { index: 1, utilization_percent: 5, memory_used_bytes: 1 },
  ]),
  point("2026-11-02T07:30:20Z", 30, [
    { index: 0, utilization_percent: 100, memory_used_bytes: 1 },
    { index: 1, utilization_percent: 7, memory_used_bytes: 1 },
  ]),
]

describe("history series", () => {
  it("picks one value per point and keeps gaps", () => {
    expect(historySeries(history, (p) => p.cpu_percent)).toEqual([10, null, 30])
    expect(gpuUtilizationSeries(history, 0)).toEqual([80, null, 100])
    expect(gpuUtilizationSeries(history, 1)).toEqual([null, 5, 7])
    expect(gpuUtilizationSeries(history, 2)).toEqual([null, null, null])
  })

  it("turns non-finite values into gaps", () => {
    expect(
      historySeries([point("x", Number.NaN)], (p) => p.cpu_percent)
    ).toEqual([null])
  })

  it("measures the time span of the history", () => {
    expect(historySpanSeconds(history)).toBe(20)
    expect(historySpanSeconds(history.slice(0, 1))).toBe(0)
    expect(historySpanSeconds([point("bad", 1), point("worse", 2)])).toBe(0)
  })
})

describe("sparklineSegments", () => {
  const box = { width: 100, height: 20, inset: 0 }

  it("maps 0–100 onto the height and spreads points over the width", () => {
    expect(sparklineSegments([0, 50, 100], box)).toEqual([
      [
        { x: 0, y: 20 },
        { x: 50, y: 10 },
        { x: 100, y: 0 },
      ],
    ])
  })

  it("splits the line at gaps", () => {
    expect(sparklineSegments([0, null, 100, 100], box)).toEqual([
      [{ x: 0, y: 20 }],
      [
        { x: 66.67, y: 0 },
        { x: 100, y: 0 },
      ],
    ])
  })

  it("anchors a short history to the right edge", () => {
    expect(sparklineSegments([25, 75], { ...box, slots: 5 })).toEqual([
      [
        { x: 75, y: 15 },
        { x: 100, y: 5 },
      ],
    ])
    // 只有一个点时画在最右侧。
    expect(sparklineSegments([40], { ...box, slots: 60 })).toEqual([
      [{ x: 100, y: 12 }],
    ])
  })

  it("clamps values outside the range and honours the inset", () => {
    expect(
      sparklineSegments([-20, 140], { width: 10, height: 12, inset: 1 })
    ).toEqual([
      [
        { x: 0, y: 11 },
        { x: 10, y: 1 },
      ],
    ])
  })

  it("supports a custom range", () => {
    expect(sparklineSegments([20, 40], { ...box, min: 20, max: 40 })).toEqual([
      [
        { x: 0, y: 20 },
        { x: 100, y: 0 },
      ],
    ])
  })

  it("returns nothing without data or size", () => {
    expect(sparklineSegments([], box)).toEqual([])
    expect(sparklineSegments([null, null], box)).toEqual([])
    expect(sparklineSegments([1, 2], { ...box, width: 0 })).toEqual([])
  })
})

describe("sparkline paths", () => {
  const segments = [
    [{ x: 0, y: 20 }],
    [
      { x: 50, y: 10 },
      { x: 100, y: 0 },
    ],
  ]

  it("draws each segment and lone points as zero-length lines", () => {
    expect(sparklineLinePath(segments)).toBe("M0,20h0M50,10L100,0")
    expect(sparklineLinePath([])).toBe("")
  })

  it("closes the area down to the baseline and skips lone points", () => {
    expect(sparklineAreaPath(segments, 20)).toBe("M50,20L50,10L100,0L100,20Z")
    expect(sparklineAreaPath([[{ x: 1, y: 1 }]], 20)).toBe("")
  })
})

describe("seriesStats", () => {
  it("reports the latest, lowest and highest value", () => {
    expect(seriesStats([null, 30, 90, null, 45, null])).toEqual({
      last: 45,
      min: 30,
      max: 90,
    })
    expect(seriesStats([null])).toBeNull()
    expect(seriesStats([])).toBeNull()
  })
})
