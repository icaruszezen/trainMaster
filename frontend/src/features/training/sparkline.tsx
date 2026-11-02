import { useId } from "react"

import {
  HISTORY_CAPACITY,
  sparklineAreaPath,
  sparklineLinePath,
  sparklineSegments,
} from "@/features/training/sparkline-points"
import { cn } from "@/lib/utils"

// 坐标系的大小；SVG 横向拉伸到容器宽度，线宽不随拉伸变化。
const WIDTH = 120
const HEIGHT = 24

/**
 * 迷你折线图：纯 SVG，无依赖。纵轴固定为 0–100%，空白处表示还没有历史。
 * `label` 是读屏软件读到的完整描述（如“最近 10 分钟利用率：当前 87%…”）。
 */
export function Sparkline({
  values,
  label,
  className,
}: {
  values: readonly (number | null)[]
  label: string
  className?: string
}) {
  // 用作 url(#id)，去掉 React 生成的 id 中可能出现的特殊字符。
  const gradientId = `spark${useId().replace(/[^\w-]/g, "")}`
  const segments = sparklineSegments(values, {
    width: WIDTH,
    height: HEIGHT,
    slots: HISTORY_CAPACITY,
    inset: 1.5,
  })

  return (
    <svg
      role="img"
      aria-label={label}
      data-slot="sparkline"
      viewBox={`0 0 ${WIDTH} ${HEIGHT}`}
      preserveAspectRatio="none"
      className={cn("h-6 w-full overflow-visible text-primary", className)}
    >
      <title>{label}</title>
      <defs>
        <linearGradient id={gradientId} x1="0" x2="0" y1="0" y2="1">
          <stop offset="0%" stopColor="currentColor" stopOpacity={0.18} />
          <stop offset="100%" stopColor="currentColor" stopOpacity={0} />
        </linearGradient>
      </defs>
      {/* 基线：没有数据时也能看出图的范围。 */}
      <line
        x1={0}
        x2={WIDTH}
        y1={HEIGHT - 0.5}
        y2={HEIGHT - 0.5}
        className="stroke-border"
        strokeWidth={1}
        vectorEffect="non-scaling-stroke"
      />
      <path
        d={sparklineAreaPath(segments, HEIGHT)}
        fill={`url(#${gradientId})`}
      />
      <path
        d={sparklineLinePath(segments)}
        fill="none"
        stroke="currentColor"
        strokeWidth={1.5}
        strokeLinecap="round"
        strokeLinejoin="round"
        vectorEffect="non-scaling-stroke"
      />
    </svg>
  )
}
