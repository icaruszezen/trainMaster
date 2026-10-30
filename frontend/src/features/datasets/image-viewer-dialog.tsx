import {
  useEffect,
  useMemo,
  useRef,
  useState,
  type KeyboardEvent,
  type ReactNode,
} from "react"
import { useQuery } from "@tanstack/react-query"
import {
  ChevronLeft,
  ChevronRight,
  CircleAlert,
  ExternalLink,
  ImageOff,
  X,
} from "lucide-react"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogTitle,
} from "@/components/ui/dialog"
import { Kbd } from "@/components/ui/kbd"
import { Label } from "@/components/ui/label"
import { Skeleton } from "@/components/ui/skeleton"
import { Spinner } from "@/components/ui/spinner"
import { Switch } from "@/components/ui/switch"
import {
  fileUrl,
  imageDetailQuery,
  thumbnailUrl,
  type AnnotationObject,
  type DatasetImage,
} from "@/features/datasets/api"
import { isBrowserNativeImage } from "@/features/datasets/files"
import { annotationPanel } from "@/features/datasets/images"
import { labelColor, readableTextColor } from "@/features/datasets/label-colors"
import { formatBytes, formatDateTime, formatNumber } from "@/lib/format"
import { cn } from "@/lib/utils"

type ImageViewerDialogProps = {
  datasetId: number
  /** 当前已加载的图片列表，上一张 / 下一张在其中切换。 */
  images: DatasetImage[]
  index: number | null
  onIndexChange: (index: number | null) => void
  total: number
  hasMore: boolean
  /** 加载下一页，返回加载后的图片数；失败时抛出错误。 */
  onLoadMore: () => Promise<number>
}

/** 查看大图并叠加 VOC 标注框；←/→ 切换，Esc 关闭。 */
export function ImageViewerDialog({
  datasetId,
  images,
  index,
  onIndexChange,
  total,
  hasMore,
  onLoadMore,
}: ImageViewerDialogProps) {
  const [showBoxes, setShowBoxes] = useState(true)
  const [loadingMore, setLoadingMore] = useState(false)
  const image = index === null ? undefined : images[index]
  // 加载下一页期间用户可能已经切换到别的图片或关闭了查看器。
  const indexRef = useRef(index)
  useEffect(() => {
    indexRef.current = index
  }, [index])

  const canPrev = index !== null && index > 0
  const canNext = index !== null && (index < images.length - 1 || hasMore)

  const goPrev = () => {
    if (index !== null && index > 0) {
      onIndexChange(index - 1)
    }
  }
  const goNext = async () => {
    if (index === null || loadingMore) {
      return
    }
    if (index < images.length - 1) {
      onIndexChange(index + 1)
    } else if (hasMore) {
      // 已经是加载到的最后一张：先加载下一页。加载失败或下一页是空的
      // （例如图片刚被移除）时停在当前这张。
      setLoadingMore(true)
      try {
        const loaded = await onLoadMore()
        if (indexRef.current === index && index + 1 < loaded) {
          onIndexChange(index + 1)
        }
      } catch (error) {
        toast.error("无法加载下一张图片", {
          description: error instanceof Error ? error.message : undefined,
        })
      } finally {
        setLoadingMore(false)
      }
    }
  }

  const onKeyDown = (event: KeyboardEvent) => {
    if (event.key === "ArrowLeft") {
      event.preventDefault()
      goPrev()
    } else if (event.key === "ArrowRight") {
      event.preventDefault()
      void goNext()
    }
  }

  // 预加载前后两张的大图，切换时更快。
  useEffect(() => {
    if (index === null) {
      return
    }
    for (const neighbour of [images[index - 1], images[index + 1]]) {
      if (neighbour?.width) {
        new Image().src = thumbnailUrl(datasetId, neighbour, "lg")
      }
    }
  }, [datasetId, images, index])

  return (
    <Dialog
      open={image !== undefined}
      onOpenChange={(open) => !open && onIndexChange(null)}
    >
      <DialogContent
        showCloseButton={false}
        onKeyDown={onKeyDown}
        className="flex h-[100dvh] max-h-none w-full max-w-none flex-col gap-0 overflow-hidden rounded-none p-0 sm:h-[min(92dvh,1000px)] sm:max-w-[min(1440px,calc(100vw-3rem))] sm:rounded-xl"
      >
        {image && index !== null && (
          <>
            <header className="flex h-14 shrink-0 items-center gap-3 border-b px-4">
              <div className="min-w-0 flex-1">
                <DialogTitle
                  className="truncate text-sm font-medium"
                  title={image.path}
                >
                  {image.file_name}
                </DialogTitle>
                <DialogDescription className="text-xs tabular-nums">
                  第 {formatNumber(index + 1)} / {formatNumber(total)} 张
                </DialogDescription>
              </div>
              <div className="flex items-center gap-2">
                <Switch
                  id="viewer-show-boxes"
                  checked={showBoxes}
                  onCheckedChange={setShowBoxes}
                />
                <Label
                  htmlFor="viewer-show-boxes"
                  className="text-sm font-normal"
                >
                  <span className="hidden sm:inline">显示标注</span>
                  <span className="sm:hidden">标注</span>
                </Label>
              </div>
              <Button
                variant="ghost"
                size="sm"
                asChild
                className="hidden sm:inline-flex"
              >
                <a
                  href={fileUrl(datasetId, image)}
                  target="_blank"
                  rel="noopener noreferrer"
                >
                  <ExternalLink />
                  打开原图
                </a>
              </Button>
              <DialogClose asChild>
                <Button variant="ghost" size="icon-sm" aria-label="关闭">
                  <X />
                </Button>
              </DialogClose>
            </header>
            <ViewerBody
              key={image.id}
              datasetId={datasetId}
              image={image}
              showBoxes={showBoxes}
              canPrev={canPrev}
              canNext={canNext}
              loadingNext={loadingMore}
              onPrev={goPrev}
              onNext={() => void goNext()}
            />
          </>
        )}
      </DialogContent>
    </Dialog>
  )
}

type ViewerBodyProps = {
  datasetId: number
  image: DatasetImage
  showBoxes: boolean
  canPrev: boolean
  canNext: boolean
  loadingNext: boolean
  onPrev: () => void
  onNext: () => void
}

function ViewerBody({
  datasetId,
  image,
  showBoxes,
  canPrev,
  canNext,
  loadingNext,
  onPrev,
  onNext,
}: ViewerBodyProps) {
  const detail = useQuery(imageDetailQuery(datasetId, image.id))
  const [highlight, setHighlight] = useState<string | null>(null)
  const objects = useMemo(() => detail.data?.objects ?? [], [detail.data])

  const groups = useMemo(() => {
    const counts = new Map<string, number>()
    for (const object of objects) {
      counts.set(object.name, (counts.get(object.name) ?? 0) + 1)
    }
    return [...counts.entries()].sort(
      (a, b) => b[1] - a[1] || (a[0] < b[0] ? -1 : 1)
    )
  }, [objects])
  const difficultCount = objects.filter((object) => object.difficult).length

  const width = detail.data?.width ?? image.width
  const height = detail.data?.height ?? image.height
  const annotation = annotationPanel(image, detail.data)

  return (
    <div className="flex min-h-0 flex-1 flex-col lg:flex-row">
      <div className="relative flex h-[55dvh] shrink-0 bg-muted/50 lg:h-auto lg:min-w-0 lg:flex-1">
        <ImageStage
          datasetId={datasetId}
          image={image}
          width={width}
          height={height}
          objects={showBoxes ? objects : []}
          highlight={highlight}
        />
        <Button
          variant="outline"
          size="icon"
          className="absolute top-1/2 left-3 -translate-y-1/2 rounded-full bg-background/80 shadow-sm backdrop-blur"
          onClick={onPrev}
          disabled={!canPrev}
          aria-label="上一张"
        >
          <ChevronLeft />
        </Button>
        <Button
          variant="outline"
          size="icon"
          className="absolute top-1/2 right-3 -translate-y-1/2 rounded-full bg-background/80 shadow-sm backdrop-blur"
          onClick={onNext}
          disabled={!canNext || loadingNext}
          aria-label="下一张"
        >
          {loadingNext ? <Spinner /> : <ChevronRight />}
        </Button>
      </div>

      <aside className="min-h-0 flex-1 overflow-y-auto border-t lg:w-80 lg:flex-none lg:border-t-0 lg:border-l">
        <div className="grid gap-6 p-4">
          <section className="grid gap-3">
            <h3 className="text-xs font-medium text-muted-foreground">文件</h3>
            <dl className="grid gap-2.5 text-sm">
              <InfoRow label="路径">
                <span className="font-mono text-xs break-all">
                  {image.path}
                </span>
              </InfoRow>
              <InfoRow label="尺寸">
                {width && height ? (
                  <span className="tabular-nums">
                    {formatNumber(width)} × {formatNumber(height)}
                  </span>
                ) : (
                  <span className="text-muted-foreground">未知</span>
                )}
              </InfoRow>
              <InfoRow label="大小">
                <span className="tabular-nums">
                  {formatBytes(image.size_bytes)}
                </span>
              </InfoRow>
              <InfoRow label="修改时间">
                <span className="tabular-nums">
                  {formatDateTime(image.mod_time)}
                </span>
              </InfoRow>
            </dl>
            <Button variant="outline" size="sm" asChild className="sm:hidden">
              <a
                href={fileUrl(datasetId, image)}
                target="_blank"
                rel="noopener noreferrer"
              >
                <ExternalLink />
                打开原图
              </a>
            </Button>
          </section>

          <section className="grid gap-3">
            <h3 className="text-xs font-medium text-muted-foreground">标注</h3>
            {annotation.path !== null && (
              <p className="font-mono text-xs break-all">{annotation.path}</p>
            )}
            {annotation.noFile && (
              <p className="text-sm text-muted-foreground">无标注文件</p>
            )}
            {annotation.error && (
              <p className="flex items-start gap-1.5 rounded-md border border-destructive/30 bg-destructive/5 px-3 py-2 text-xs break-words text-destructive">
                <CircleAlert className="mt-px size-3.5 shrink-0" />
                <span className="min-w-0">{annotation.error}</span>
              </p>
            )}
            {detail.isPending ? (
              <div className="grid gap-2">
                <Skeleton className="h-7 w-full" />
                <Skeleton className="h-7 w-2/3" />
              </div>
            ) : detail.error && detail.data === undefined ? (
              <p className="text-xs text-destructive">
                无法读取标注：{detail.error.message}
              </p>
            ) : groups.length > 0 ? (
              <>
                <p className="text-sm">
                  共{" "}
                  <span className="font-medium tabular-nums">
                    {formatNumber(objects.length)}
                  </span>{" "}
                  个目标
                  {difficultCount > 0 && (
                    <span className="text-muted-foreground">
                      ，其中 {formatNumber(difficultCount)} 个标记为
                      difficult（虚线框）
                    </span>
                  )}
                </p>
                <ul
                  className="grid gap-1"
                  onMouseLeave={() => setHighlight(null)}
                >
                  {groups.map(([name, count]) => (
                    <li key={name}>
                      <button
                        type="button"
                        onMouseEnter={() => setHighlight(name)}
                        onFocus={() => setHighlight(name)}
                        onBlur={() => setHighlight(null)}
                        className={cn(
                          "flex w-full items-center gap-2.5 rounded-md px-2 py-1.5 text-left text-sm transition-colors outline-none hover:bg-muted focus-visible:ring-[3px] focus-visible:ring-ring/50",
                          highlight === name && "bg-muted"
                        )}
                        aria-label={`突出显示 ${name}`}
                      >
                        <span
                          className="size-2.5 shrink-0 rounded-sm"
                          style={{ backgroundColor: labelColor(name) }}
                          aria-hidden
                        />
                        <span className="min-w-0 flex-1 truncate">{name}</span>
                        <span className="text-muted-foreground tabular-nums">
                          {formatNumber(count)}
                        </span>
                      </button>
                    </li>
                  ))}
                </ul>
              </>
            ) : (
              annotation.empty && (
                <p className="text-sm text-muted-foreground">
                  空标注：标注文件中没有目标（负样本）
                </p>
              )
            )}
          </section>

          <p className="hidden items-center gap-1.5 text-xs text-muted-foreground lg:flex">
            <Kbd>←</Kbd>
            <Kbd>→</Kbd>
            切换图片
            <Kbd className="ml-2">Esc</Kbd>
            关闭
          </p>
        </div>
      </aside>
    </div>
  )
}

function InfoRow({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="grid grid-cols-[4.5rem_minmax(0,1fr)] gap-2">
      <dt className="text-muted-foreground">{label}</dt>
      <dd className="min-w-0">{children}</dd>
    </div>
  )
}

/** 观察元素的内容尺寸。 */
function useElementSize<T extends HTMLElement>() {
  const ref = useRef<T>(null)
  const [size, setSize] = useState({ width: 0, height: 0 })

  useEffect(() => {
    const element = ref.current
    if (!element) {
      return
    }
    const observer = new ResizeObserver(([entry]) => {
      const { width, height } = entry.contentRect
      setSize({ width, height })
    })
    observer.observe(element)
    return () => observer.disconnect()
  }, [])

  return [ref, size] as const
}

// 目标太多时只画框，不画标签名，避免遮挡图片。
const MAX_LABELED_OBJECTS = 80

type ImageStageProps = {
  datasetId: number
  image: DatasetImage
  width: number | null
  height: number | null
  objects: AnnotationObject[]
  highlight: string | null
}

/**
 * 按原图尺寸等比缩放到可用区域，标注框用 viewBox 为原图像素尺寸的 SVG 叠加，
 * 与显示尺寸无关。原图尺寸未知时直接加载原图，从图片本身读取尺寸。
 */
function ImageStage({
  datasetId,
  image,
  width,
  height,
  objects,
  highlight,
}: ImageStageProps) {
  const [containerRef, container] = useElementSize<HTMLDivElement>()
  const knownSize = width !== null && height !== null && width > 0 && height > 0
  const [source, setSource] = useState<"thumbnail" | "original" | "failed">(
    knownSize ? "thumbnail" : "original"
  )
  const [natural, setNatural] = useState<{ w: number; h: number } | null>(null)
  const [loaded, setLoaded] = useState(false)

  const w = knownSize ? width : natural?.w
  const h = knownSize ? height : natural?.h
  const padding = 24
  const scale =
    w && h && container.width > 0
      ? Math.min(
          (container.width - padding * 2) / w,
          (container.height - padding * 2) / h
        )
      : 0
  const displayWidth = w ? Math.max(0, w * scale) : 0
  const displayHeight = h ? Math.max(0, h * scale) : 0

  const src =
    source === "thumbnail"
      ? thumbnailUrl(datasetId, image, "lg")
      : fileUrl(datasetId, image)

  return (
    <div
      ref={containerRef}
      className="flex min-h-0 min-w-0 flex-1 items-center justify-center overflow-hidden"
    >
      {source === "failed" ? (
        <div className="flex flex-col items-center gap-2 text-sm text-muted-foreground">
          <ImageOff className="size-8" />
          无法在浏览器中显示该图片
        </div>
      ) : (
        <div
          className="relative"
          style={
            scale > 0
              ? { width: displayWidth, height: displayHeight }
              : { width: 0, height: 0 }
          }
        >
          {!loaded && <Skeleton className="absolute inset-0 rounded-none" />}
          <img
            src={src}
            alt={image.file_name}
            decoding="async"
            draggable={false}
            onLoad={(event) => {
              const { naturalWidth, naturalHeight } = event.currentTarget
              setNatural({ w: naturalWidth, h: naturalHeight })
              setLoaded(true)
            }}
            onError={() => {
              setLoaded(false)
              setSource(
                source === "thumbnail" && isBrowserNativeImage(image.path)
                  ? "original"
                  : "failed"
              )
            }}
            className={cn(
              "absolute inset-0 size-full object-fill shadow-sm transition-opacity duration-200",
              loaded ? "opacity-100" : "opacity-0",
              // 尺寸未知时先以原尺寸加载，以便读到 naturalWidth。
              scale === 0 && "invisible"
            )}
          />
          {w && h && loaded && objects.length > 0 && (
            <BoxOverlay
              width={w}
              height={h}
              objects={objects}
              highlight={highlight}
            />
          )}
        </div>
      )}
    </div>
  )
}

function BoxOverlay({
  width,
  height,
  objects,
  highlight,
}: {
  width: number
  height: number
  objects: AnnotationObject[]
  highlight: string | null
}) {
  const showLabels = objects.length <= MAX_LABELED_OBJECTS

  return (
    <div className="pointer-events-none absolute inset-0" aria-hidden>
      <svg
        viewBox={`0 0 ${width} ${height}`}
        preserveAspectRatio="none"
        className="absolute inset-0 size-full overflow-visible"
      >
        {objects.map((object, index) => {
          const color = labelColor(object.name)
          const dimmed = highlight !== null && highlight !== object.name
          return (
            <rect
              key={index}
              x={object.xmin}
              y={object.ymin}
              width={Math.max(0, object.xmax - object.xmin)}
              height={Math.max(0, object.ymax - object.ymin)}
              fill={color}
              fillOpacity={dimmed ? 0 : highlight ? 0.2 : 0.08}
              stroke={color}
              strokeOpacity={dimmed ? 0.25 : 1}
              strokeWidth={highlight === object.name ? 3 : 2}
              strokeDasharray={object.difficult ? "6 4" : undefined}
              vectorEffect="non-scaling-stroke"
              className="transition-[stroke-opacity,fill-opacity] duration-150"
            />
          )
        })}
      </svg>
      {showLabels &&
        objects.map((object, index) => {
          const color = labelColor(object.name)
          const dimmed = highlight !== null && highlight !== object.name
          // 框贴着图片顶部时，标签放到框内，避免被裁掉。
          const inside = object.ymin / height < 0.04
          return (
            <span
              key={index}
              className={cn(
                "absolute max-w-40 truncate rounded-sm px-1 text-[11px] leading-4 font-medium whitespace-nowrap shadow-sm transition-opacity duration-150",
                inside ? "" : "-translate-y-full",
                dimmed && "opacity-20"
              )}
              style={{
                left: `${(object.xmin / width) * 100}%`,
                top: `${(object.ymin / height) * 100}%`,
                backgroundColor: color,
                color: readableTextColor(color),
              }}
            >
              {object.name}
            </span>
          )
        })}
    </div>
  )
}
