import { buildGuideDocx } from "@/features/guide/export-docx"
import { buildGuideOfflineHtml } from "@/features/guide/export-html"
import { renderFiguresForExport } from "@/features/guide/figure-render"
import type { GuideChapter, GuideGroup } from "@/features/guide/types"
import { startDownload } from "@/lib/download"

export type GuideExportFormat = "docx-all" | "docx-chapter" | "html-all"

export type GuideExportProgress = {
  stage: "rendering" | "packaging"
  completed: number
  total: number
}

export type GuideExportResult = {
  fileName: string
  sizeBytes: number
  figureCount: number
}

const DOCX_MIME =
  "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
const HTML_MIME = "text/html;charset=utf-8"

/** 清理文件名中的非法路径字符，保证跨平台下载保存安全。 */
export function sanitizeFileSegment(raw: string): string {
  return raw
    .replace(/[\\/:*?"<>|]+/g, "-")
    .replace(/\s+/g, "-")
    .replace(/-+/g, "-")
    .replace(/^-|-$/g, "")
}

function triggerBlobDownload(blob: Blob, fileName: string) {
  const objectUrl = URL.createObjectURL(blob)
  startDownload(objectUrl, fileName)
  // 留足时间让浏览器接管 Blob 下载后再释放对象 URL
  window.setTimeout(() => {
    URL.revokeObjectURL(objectUrl)
  }, 60_000)
}

/**
 * 在浏览器端将操作指南内容与带标注高清截图实时打包为 Word（.docx）或单文件离线网页（.html）
 * 并直接触发本地下载，全程不依赖服务端转换。
 */
export async function exportGuideDocument(options: {
  format: GuideExportFormat
  chapters: GuideChapter[]
  groups: GuideGroup[]
  activeChapter?: GuideChapter
  onProgress?: (progress: GuideExportProgress) => void
}): Promise<GuideExportResult> {
  const { format, chapters, groups, activeChapter, onProgress } = options

  const targetChapters =
    format === "docx-chapter"
      ? activeChapter
        ? [activeChapter]
        : chapters.slice(0, 1)
      : chapters

  const figures = targetChapters.flatMap((chapter) =>
    chapter.sections.flatMap((section) => section.figures ?? [])
  )

  const renderedImages = await renderFiguresForExport(figures, {
    includeCallouts: true,
    quality: 0.88,
    maxWidth: 1600,
    onProgress: (completed, total) => {
      onProgress?.({ stage: "rendering", completed, total })
    },
  })

  onProgress?.({
    stage: "packaging",
    completed: figures.length,
    total: figures.length,
  })

  let blob: Blob
  let fileName: string

  if (format === "html-all") {
    const html = buildGuideOfflineHtml(targetChapters, groups, renderedImages)
    blob = new Blob([html], { type: HTML_MIME })
    fileName = "trainMaster-操作指南.html"
  } else {
    const isSingle = format === "docx-chapter"
    const docxBytes = buildGuideDocx(targetChapters, groups, renderedImages, {
      singleChapter: isSingle,
    })
    const safeBuffer = new Uint8Array(docxBytes)
    blob = new Blob([safeBuffer], { type: DOCX_MIME })
    if (isSingle && targetChapters[0]) {
      const slug =
        sanitizeFileSegment(targetChapters[0].shortTitle) ||
        targetChapters[0].id
      fileName = `trainMaster-操作指南-${slug}.docx`
    } else {
      fileName = "trainMaster-操作指南.docx"
    }
  }

  triggerBlobDownload(blob, fileName)

  return {
    fileName,
    sizeBytes: blob.size,
    figureCount: renderedImages.size,
  }
}
