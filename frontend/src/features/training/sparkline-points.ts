/**
 * 把历史指标转换为迷你折线图（sparkline）的坐标（纯函数，便于测试）。
 *
 * 横轴按“点的序号”而不是时间排布：服务端每次心跳追加一个点，间隔基本相同。
 * 最新的点固定在最右侧；历史还不满 `slots` 个点时折线从右侧开始，
 * 不会把几十秒的数据拉伸成整条曲线。
 */
import type { NodeHistoryPoint } from "@/features/training/compute-api"

/** 服务端最多保存的历史点数。 */
export const HISTORY_CAPACITY = 60

export type SparklinePoint = { x: number; y: number }

export type SparklineOptions = {
  width: number
  height: number
  /** 纵轴范围，默认 0–100（百分比）；超出范围的值贴在边上。 */
  min?: number
  max?: number
  /** 横轴的总点数，默认等于 values 的长度。 */
  slots?: number
  /** 上下留出的空白（像素），避免线条被裁掉一半。 */
  inset?: number
}

const round = (value: number) => Math.round(value * 100) / 100

/** 从历史中取出一列数值，如 CPU 利用率或某块显卡的利用率。 */
export function historySeries(
  history: readonly NodeHistoryPoint[],
  pick: (point: NodeHistoryPoint) => number | null | undefined
): (number | null)[] {
  return history.map((point) => {
    const value = pick(point)
    return typeof value === "number" && Number.isFinite(value) ? value : null
  })
}

/** 某块显卡的利用率历史；该点没有这块卡的数据时为 null。 */
export function gpuUtilizationSeries(
  history: readonly NodeHistoryPoint[],
  index: number
) {
  return historySeries(
    history,
    (point) =>
      point.gpus.find((gpu) => gpu.index === index)?.utilization_percent
  )
}

/** 换算成 SVG 坐标；遇到缺失的值（null）断开，返回连续的若干段。 */
export function sparklineSegments(
  values: readonly (number | null)[],
  { width, height, min = 0, max = 100, slots, inset = 1 }: SparklineOptions
): SparklinePoint[][] {
  const count = values.length
  if (count === 0 || !(width > 0) || !(height > 0)) {
    return []
  }

  const totalSlots = Math.max(slots ?? count, count, 2)
  const step = width / (totalSlots - 1)
  const range = max - min
  const top = inset
  const bottom = height - inset

  const segments: SparklinePoint[][] = []
  let current: SparklinePoint[] = []
  values.forEach((value, index) => {
    if (value === null || !Number.isFinite(value)) {
      if (current.length > 0) {
        segments.push(current)
        current = []
      }
      return
    }
    const ratio = range > 0 ? (value - min) / range : 0
    const clamped = Math.min(1, Math.max(0, ratio))
    current.push({
      x: round(width - (count - 1 - index) * step),
      y: round(bottom - clamped * (bottom - top)),
    })
  })
  if (current.length > 0) {
    segments.push(current)
  }
  return segments
}

/** 折线的 path：每段一条折线；孤立的点画成长度为 0 的线（配合圆头显示为圆点）。 */
export function sparklineLinePath(segments: readonly SparklinePoint[][]) {
  return segments
    .map((points) => {
      const [first, ...rest] = points
      const head = `M${first.x},${first.y}`
      return rest.length === 0
        ? `${head}h0`
        : `${head}${rest.map((p) => `L${p.x},${p.y}`).join("")}`
    })
    .join("")
}

/** 折线下方的填充区域，`baseline` 为底边的 y 坐标；孤立的点没有面积。 */
export function sparklineAreaPath(
  segments: readonly SparklinePoint[][],
  baseline: number
) {
  return segments
    .filter((points) => points.length > 1)
    .map((points) => {
      const first = points[0]
      const last = points[points.length - 1]
      const line = points.map((p) => `L${p.x},${p.y}`).join("")
      return `M${first.x},${baseline}${line}L${last.x},${baseline}Z`
    })
    .join("")
}

export type SeriesStats = { last: number; min: number; max: number }

/** 最新值、最小值与最大值（读屏文字与悬停提示使用）；没有任何数据时为 null。 */
export function seriesStats(values: readonly (number | null)[]) {
  const known = values.filter(
    (value): value is number => value !== null && Number.isFinite(value)
  )
  if (known.length === 0) {
    return null
  }
  return {
    last: known[known.length - 1],
    min: Math.min(...known),
    max: Math.max(...known),
  } satisfies SeriesStats
}

/** 历史覆盖的时长（秒），少于两个点或时间无法解析时为 0。 */
export function historySpanSeconds(history: readonly NodeHistoryPoint[]) {
  if (history.length < 2) {
    return 0
  }
  const first = Date.parse(history[0].t)
  const last = Date.parse(history[history.length - 1].t)
  if (Number.isNaN(first) || Number.isNaN(last) || last <= first) {
    return 0
  }
  return Math.round((last - first) / 1000)
}
