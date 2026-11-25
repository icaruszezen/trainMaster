import type { GuideFigure } from "@/features/guide/types"

export type RenderedFigureImage = {
  bytes: Uint8Array
  width: number
  height: number
  contentType: "image/jpeg" | "image/png"
  extension: "jpg" | "png"
}

export type RenderFiguresOptions = {
  /** 是否在导出图片上叠加高亮框与 ①②③ 编号标注（默认 true）。 */
  includeCallouts?: boolean
  /** JPEG 压缩质量（0–1，默认 0.88）。 */
  quality?: number
  /** 最大输出宽度（像素，默认 1600）。 */
  maxWidth?: number
  /** 每完成一张图时回调进度。 */
  onProgress?: (completed: number, total: number) => void
}

function loadHtmlImage(src: string): Promise<HTMLImageElement> {
  return new Promise((resolve, reject) => {
    const img = new Image()
    img.decoding = "async"
    img.onload = () => resolve(img)
    img.onerror = () => reject(new Error(`加载截图失败：${src}`))
    img.src = src
  })
}

function drawRoundedRectPath(
  ctx: CanvasRenderingContext2D,
  x: number,
  y: number,
  width: number,
  height: number,
  radius: number
) {
  const r = Math.min(radius, width / 2, height / 2)
  ctx.beginPath()
  ctx.moveTo(x + r, y)
  ctx.lineTo(x + width - r, y)
  ctx.quadraticCurveTo(x + width, y, x + width, y + r)
  ctx.lineTo(x + width, y + height - r)
  ctx.quadraticCurveTo(x + width, y + height, x + width - r, y + height)
  ctx.lineTo(x + r, y + height)
  ctx.quadraticCurveTo(x, y + height, x, y + height - r)
  ctx.lineTo(x, y + r)
  ctx.quadraticCurveTo(x, y, x + r, y)
  ctx.closePath()
}

function canvasToBytes(
  canvas: HTMLCanvasElement,
  mimeType: "image/jpeg" | "image/png",
  quality: number
): Promise<Uint8Array> {
  return new Promise((resolve, reject) => {
    canvas.toBlob(
      (blob) => {
        if (!blob) {
          reject(new Error("Canvas 导出图片失败"))
          return
        }
        blob
          .arrayBuffer()
          .then((buffer) => resolve(new Uint8Array(buffer)))
          .catch(reject)
      },
      mimeType,
      quality
    )
  })
}

function yieldToBrowser(): Promise<void> {
  return new Promise((resolve) => {
    window.setTimeout(resolve, 0)
  })
}

/**
 * 将单张操作指南截图渲染为带编号标注的高清 JPEG 字节流，确保离线 Word 与 HTML
 * 文档中的配图清晰可读，并与网页交互标注样式保持一致。
 */
export async function renderAnnotatedFigure(
  figure: GuideFigure,
  options?: Pick<
    RenderFiguresOptions,
    "includeCallouts" | "quality" | "maxWidth"
  >
): Promise<RenderedFigureImage> {
  const includeCallouts = options?.includeCallouts ?? true
  const quality = options?.quality ?? 0.88
  const maxWidth = options?.maxWidth ?? 1600

  const img = await loadHtmlImage(figure.src)
  const sourceWidth = img.naturalWidth || figure.width || 1600
  const sourceHeight = img.naturalHeight || figure.height || 1000
  const scale = sourceWidth > maxWidth ? maxWidth / sourceWidth : 1
  const width = Math.max(1, Math.round(sourceWidth * scale))
  const height = Math.max(1, Math.round(sourceHeight * scale))

  const canvas = document.createElement("canvas")
  canvas.width = width
  canvas.height = height
  const ctx = canvas.getContext("2d")
  if (!ctx) {
    throw new Error("当前浏览器不支持 Canvas 2D 渲染")
  }

  // 白底填充，避免 JPEG 出现黑边
  ctx.fillStyle = "#ffffff"
  ctx.fillRect(0, 0, width, height)
  ctx.drawImage(img, 0, 0, width, height)

  // 柔和外框，让浅色界面截图在白色文档页上边界分明
  ctx.strokeStyle = "rgba(15, 23, 42, 0.14)"
  ctx.lineWidth = 2
  ctx.strokeRect(1, 1, width - 2, height - 2)

  if (includeCallouts && figure.callouts.length > 0) {
    const boxLineWidth = Math.max(2.5, Math.round(width * 0.0022 * 10) / 10)
    const boxRadius = Math.max(6, Math.round(width * 0.006))

    // 第一遍：绘制区域高亮框
    for (const callout of figure.callouts) {
      if (!callout.box) {
        continue
      }
      const bx = (callout.box.x / 100) * width
      const by = (callout.box.y / 100) * height
      const bw = (callout.box.width / 100) * width
      const bh = (callout.box.height / 100) * height

      ctx.save()
      drawRoundedRectPath(ctx, bx, by, bw, bh, boxRadius)
      ctx.fillStyle = "rgba(225, 29, 72, 0.08)"
      ctx.fill()
      ctx.strokeStyle = "rgba(225, 29, 72, 0.85)"
      ctx.lineWidth = boxLineWidth
      ctx.stroke()
      ctx.restore()
    }

    // 第二遍：绘制带白边与投影的玫瑰红序号圆点
    const radius = Math.max(16, Math.round(width * 0.0145))
    const ringWidth = Math.max(3, Math.round(radius * 0.16))
    const fontSize = Math.round(radius * 1.15)
    const margin = radius + ringWidth + 2

    for (const callout of figure.callouts) {
      const rawX = (callout.x / 100) * width
      const rawY = (callout.y / 100) * height
      const cx = Math.min(width - margin, Math.max(margin, rawX))
      const cy = Math.min(height - margin, Math.max(margin, rawY))

      // 白色外环 + 轻投影
      ctx.save()
      ctx.shadowColor = "rgba(15, 23, 42, 0.34)"
      ctx.shadowBlur = 8
      ctx.shadowOffsetX = 0
      ctx.shadowOffsetY = 2
      ctx.beginPath()
      ctx.arc(cx, cy, radius + ringWidth, 0, Math.PI * 2)
      ctx.fillStyle = "#ffffff"
      ctx.fill()
      ctx.restore()

      // 玫瑰红内圆
      ctx.save()
      ctx.beginPath()
      ctx.arc(cx, cy, radius, 0, Math.PI * 2)
      ctx.fillStyle = "#e11d48"
      ctx.fill()

      // 居中数字
      ctx.fillStyle = "#ffffff"
      ctx.font = `700 ${fontSize}px -apple-system, BlinkMacSystemFont, "Segoe UI", "PingFang SC", "Microsoft YaHei", sans-serif`
      ctx.textAlign = "center"
      ctx.textBaseline = "middle"
      ctx.fillText(String(callout.index), cx, cy + 1)
      ctx.restore()
    }
  }

  const bytes = await canvasToBytes(canvas, "image/jpeg", quality)
  return {
    bytes,
    width,
    height,
    contentType: "image/jpeg",
    extension: "jpg",
  }
}

/**
 * 批量渲染去重后的截图列表，按张回报进度并主动让出主线程，避免界面卡顿。
 */
export async function renderFiguresForExport(
  figures: GuideFigure[],
  options?: RenderFiguresOptions
): Promise<Map<string, RenderedFigureImage>> {
  const uniqueFigures: GuideFigure[] = []
  const seen = new Set<string>()
  for (const figure of figures) {
    if (!seen.has(figure.id)) {
      seen.add(figure.id)
      uniqueFigures.push(figure)
    }
  }

  const rendered = new Map<string, RenderedFigureImage>()
  const total = uniqueFigures.length
  options?.onProgress?.(0, total)

  for (let i = 0; i < uniqueFigures.length; i++) {
    const figure = uniqueFigures[i]
    try {
      const image = await renderAnnotatedFigure(figure, options)
      rendered.set(figure.id, image)
    } catch {
      // 单张截图加载异常时回退为文字占位说明，避免阻断整份手册导出
    }
    options?.onProgress?.(i + 1, total)
    await yieldToBrowser()
  }

  return rendered
}
