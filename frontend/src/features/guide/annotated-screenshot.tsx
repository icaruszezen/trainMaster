import { useState } from "react"
import { Eye, EyeOff, Maximize2 } from "lucide-react"

import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import type { GuideCallout, GuideFigure } from "@/features/guide/types"
import { cn } from "@/lib/utils"

type AnnotatedScreenshotProps = {
  figure: GuideFigure
}

/**
 * 交互式标注截图组件：
 * - 底层始终展示纯净浅色 WebP 原图，深色模式下自动加柔和边框与衬底过渡；
 * - 顶层按元素真实百分比坐标叠加 ①②③ 编号与可选区域高亮框；
 * - 悬停或点击图上编号与下方说明卡片双向联动；
 * - 支持一键隐藏标注查看原图，以及弹窗灯箱大图检视。
 */
export function AnnotatedScreenshot({ figure }: AnnotatedScreenshotProps) {
  const [showCallouts, setShowCallouts] = useState(true)
  const [hoveredIndex, setHoveredIndex] = useState<number | null>(null)
  const [pinnedIndex, setPinnedIndex] = useState<number | null>(null)
  const [zoomOpen, setZoomOpen] = useState(false)

  const activeIndex = hoveredIndex ?? pinnedIndex
  const hasCallouts = figure.callouts.length > 0

  const togglePin = (index: number) => {
    setPinnedIndex((prev) => (prev === index ? null : index))
  }

  return (
    <>
      <figure className="overflow-hidden rounded-xl border bg-card shadow-xs">
        <figcaption className="flex flex-wrap items-center justify-between gap-2 border-b bg-muted/40 px-4 py-2.5">
          <div className="flex min-w-0 items-center gap-2">
            <span className="text-sm font-medium text-foreground">
              {figure.caption}
            </span>
            {hasCallouts && (
              <Badge variant="secondary" className="shrink-0 text-[11px]">
                {figure.callouts.length} 处界面标注
              </Badge>
            )}
          </div>
          <div className="flex items-center gap-1.5">
            {hasCallouts && (
              <Button
                type="button"
                variant="ghost"
                size="sm"
                className="h-7 px-2 text-xs text-muted-foreground hover:text-foreground"
                onClick={() => setShowCallouts((prev) => !prev)}
                aria-pressed={!showCallouts}
              >
                {showCallouts ? (
                  <>
                    <EyeOff className="size-3.5" />
                    隐藏标注
                  </>
                ) : (
                  <>
                    <Eye className="size-3.5" />
                    显示标注
                  </>
                )}
              </Button>
            )}
            <Button
              type="button"
              variant="ghost"
              size="sm"
              className="h-7 px-2 text-xs text-muted-foreground hover:text-foreground"
              onClick={() => setZoomOpen(true)}
            >
              <Maximize2 className="size-3.5" />
              放大查看
            </Button>
          </div>
        </figcaption>

        <div className="bg-muted/25 p-2.5 sm:p-3.5 dark:bg-zinc-900/75">
          <ScreenshotCanvas
            figure={figure}
            showCallouts={showCallouts}
            activeIndex={activeIndex}
            onHover={setHoveredIndex}
            onSelect={togglePin}
            onOpenZoom={() => setZoomOpen(true)}
          />
        </div>

        {hasCallouts && (
          <CalloutLegend
            callouts={figure.callouts}
            activeIndex={activeIndex}
            dimmed={!showCallouts}
            onHover={(idx) => {
              if (!showCallouts && idx !== null) {
                setShowCallouts(true)
              }
              setHoveredIndex(idx)
            }}
            onSelect={(idx) => {
              if (!showCallouts) {
                setShowCallouts(true)
              }
              togglePin(idx)
            }}
          />
        )}
      </figure>

      <Dialog open={zoomOpen} onOpenChange={setZoomOpen}>
        <DialogContent className="max-h-[94dvh] gap-3 overflow-y-auto p-4 sm:max-w-[min(96vw,1360px)] sm:p-5">
          <DialogHeader className="pr-8">
            <div className="flex flex-wrap items-center justify-between gap-2">
              <DialogTitle className="text-base">{figure.caption}</DialogTitle>
              {hasCallouts && (
                <Button
                  type="button"
                  variant="outline"
                  size="sm"
                  className="h-7 px-2.5 text-xs"
                  onClick={() => setShowCallouts((prev) => !prev)}
                >
                  {showCallouts ? (
                    <>
                      <EyeOff className="size-3.5" />
                      隐藏标注查看原图
                    </>
                  ) : (
                    <>
                      <Eye className="size-3.5" />
                      显示编号标注
                    </>
                  )}
                </Button>
              )}
            </div>
            <DialogDescription className="text-xs">
              悬停或点击图中的序号圆点，可与下方操作说明联动高亮对应界面区域。
            </DialogDescription>
          </DialogHeader>

          <div className="rounded-lg bg-muted/30 p-2 dark:bg-zinc-900/80">
            <ScreenshotCanvas
              figure={figure}
              showCallouts={showCallouts}
              activeIndex={activeIndex}
              onHover={setHoveredIndex}
              onSelect={togglePin}
              large
            />
          </div>

          {hasCallouts && (
            <CalloutLegend
              callouts={figure.callouts}
              activeIndex={activeIndex}
              dimmed={!showCallouts}
              onHover={setHoveredIndex}
              onSelect={togglePin}
              className="rounded-lg border"
            />
          )}
        </DialogContent>
      </Dialog>
    </>
  )
}

function ScreenshotCanvas({
  figure,
  showCallouts,
  activeIndex,
  onHover,
  onSelect,
  onOpenZoom,
  large = false,
}: {
  figure: GuideFigure
  showCallouts: boolean
  activeIndex: number | null
  onHover: (index: number | null) => void
  onSelect: (index: number) => void
  onOpenZoom?: () => void
  large?: boolean
}) {
  return (
    <div className="relative mx-auto overflow-hidden rounded-lg border border-border/80 bg-white shadow-xs dark:border-white/15">
      <img
        src={figure.src}
        alt={figure.alt}
        width={figure.width}
        height={figure.height}
        loading={large ? "eager" : "lazy"}
        decoding="async"
        onClick={onOpenZoom}
        className={cn(
          "block h-auto w-full select-none",
          onOpenZoom && "cursor-zoom-in"
        )}
      />

      {showCallouts && figure.callouts.length > 0 && (
        <div
          className="pointer-events-none absolute inset-0"
          aria-label="界面标注叠加层"
        >
          {figure.callouts.map((callout) => {
            if (!callout.box) {
              return null
            }
            const isActive = activeIndex === callout.index
            const hasOtherActive = activeIndex !== null && !isActive
            return (
              <div
                key={`box-${callout.index}`}
                style={{
                  left: `${callout.box.x}%`,
                  top: `${callout.box.y}%`,
                  width: `${callout.box.width}%`,
                  height: `${callout.box.height}%`,
                }}
                className={cn(
                  "absolute rounded-md border-2 transition-all duration-150",
                  isActive
                    ? "z-10 border-rose-600 bg-rose-500/15 shadow-[0_0_0_3px_rgba(225,29,72,0.22)]"
                    : hasOtherActive
                      ? "border-rose-500/25 bg-transparent"
                      : "border-rose-500/60 bg-rose-500/6"
                )}
              />
            )
          })}

          {figure.callouts.map((callout) => {
            const isActive = activeIndex === callout.index
            const hasOtherActive = activeIndex !== null && !isActive
            return (
              <button
                key={`pin-${callout.index}`}
                type="button"
                style={{
                  left: `${callout.x}%`,
                  top: `${callout.y}%`,
                }}
                aria-label={`标注 ${callout.index}：${callout.label}`}
                aria-pressed={isActive}
                onMouseEnter={() => onHover(callout.index)}
                onMouseLeave={() => onHover(null)}
                onFocus={() => onHover(callout.index)}
                onBlur={() => onHover(null)}
                onClick={(event) => {
                  event.stopPropagation()
                  onSelect(callout.index)
                }}
                className={cn(
                  "pointer-events-auto absolute inline-flex -translate-x-1/2 -translate-y-1/2 items-center justify-center rounded-full font-semibold tabular-nums shadow-md ring-2 ring-white transition-all duration-150 focus-visible:ring-[3px] focus-visible:ring-rose-400 focus-visible:outline-none",
                  large
                    ? "size-7 text-xs"
                    : "size-5.5 text-[11px] sm:size-6 sm:text-xs",
                  isActive
                    ? "z-30 scale-120 bg-rose-600 text-white ring-rose-100"
                    : hasOtherActive
                      ? "z-10 bg-rose-600/60 text-white/95 hover:scale-110 hover:bg-rose-600"
                      : "z-20 bg-rose-600 text-white hover:scale-110"
                )}
              >
                {callout.index}
              </button>
            )
          })}
        </div>
      )}
    </div>
  )
}

function CalloutLegend({
  callouts,
  activeIndex,
  dimmed,
  onHover,
  onSelect,
  className,
}: {
  callouts: GuideCallout[]
  activeIndex: number | null
  dimmed: boolean
  onHover: (index: number | null) => void
  onSelect: (index: number) => void
  className?: string
}) {
  return (
    <ol
      className={cn(
        "grid gap-2 border-t bg-muted/20 p-3 sm:grid-cols-2 sm:p-3.5",
        dimmed && "opacity-75",
        className
      )}
    >
      {callouts.map((callout) => {
        const isActive = activeIndex === callout.index
        return (
          <li key={callout.index}>
            <button
              type="button"
              onMouseEnter={() => onHover(callout.index)}
              onMouseLeave={() => onHover(null)}
              onFocus={() => onHover(callout.index)}
              onBlur={() => onHover(null)}
              onClick={() => onSelect(callout.index)}
              aria-pressed={isActive}
              className={cn(
                "flex h-full w-full items-start gap-2.5 rounded-lg border px-3 py-2 text-left transition-colors focus-visible:ring-[3px] focus-visible:ring-ring/50 focus-visible:outline-none",
                isActive
                  ? "border-rose-500 bg-rose-50/85 shadow-2xs dark:border-rose-400/75 dark:bg-rose-950/45"
                  : "border-border/60 bg-background/85 hover:border-rose-300 hover:bg-rose-50/40 dark:hover:border-rose-500/40 dark:hover:bg-rose-950/25"
              )}
            >
              <span
                className={cn(
                  "mt-0.5 inline-flex size-5 shrink-0 items-center justify-center rounded-full text-[11px] font-semibold tabular-nums transition-colors",
                  isActive
                    ? "bg-rose-600 text-white"
                    : "bg-rose-600/90 text-white"
                )}
              >
                {callout.index}
              </span>
              <span className="min-w-0 flex-1 space-y-0.5">
                <span className="block text-xs font-medium text-foreground sm:text-sm">
                  {callout.label}
                </span>
                <span className="block text-xs leading-relaxed text-muted-foreground">
                  {callout.description}
                </span>
              </span>
            </button>
          </li>
        )
      })}
    </ol>
  )
}
