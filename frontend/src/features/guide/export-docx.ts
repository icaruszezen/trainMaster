import {
  guideWorkflowStages,
  type GuideWorkflowStage,
} from "@/features/guide/chapters"
import type { RenderedFigureImage } from "@/features/guide/figure-render"
import type {
  GuideCallout,
  GuideChapter,
  GuideFigure,
  GuideGroup,
  GuideSection,
  GuideShortcutGroup,
  GuideStep,
  GuideTable,
  GuideTip,
} from "@/features/guide/types"
import {
  buildZipArchive,
  encodeUtf8,
  type ZipEntry,
} from "@/features/guide/zip"

export type BuildGuideDocxOptions = {
  /** 文档标题（默认「trainMaster 操作指南」）。 */
  title?: string
  /** 是否导出单章节精简版（不生成整册封面与总目录，默认依据章节数自动判断）。 */
  singleChapter?: boolean
  /** 导出时间戳（默认当前时间，便于单测固定）。 */
  generatedAt?: Date
  /** 端到端闭环阶段说明（默认取内置 6 步工作流）。 */
  workflowStages?: GuideWorkflowStage[]
}

const W_NS = "http://schemas.openxmlformats.org/wordprocessingml/2006/main"
const R_NS =
  "http://schemas.openxmlformats.org/officeDocument/2006/relationships"
const WP_NS =
  "http://schemas.openxmlformats.org/drawingml/2006/wordprocessingDrawing"
const A_NS = "http://schemas.openxmlformats.org/drawingml/2006/main"
const PIC_NS = "http://schemas.openxmlformats.org/drawingml/2006/picture"

/** A4 页面尺寸与正文字宽（单位：twips / dxa，1 inch = 1440 dxa）。 */
const PAGE_WIDTH_DXA = 11906 // 210mm
const PAGE_HEIGHT_DXA = 16838 // 297mm
const PAGE_MARGIN_DXA = 1134 // 20mm
const CONTENT_WIDTH_DXA = PAGE_WIDTH_DXA - PAGE_MARGIN_DXA * 2 // 9638 dxa (~170mm)

/** 图片在 A4 版心中的最大显示尺寸（单位：EMU，1 cm = 360000 EMU）。 */
const MAX_IMAGE_WIDTH_EMU = 5940000 // ~16.5 cm
const MAX_IMAGE_HEIGHT_EMU = 7200000 // ~20.0 cm

/** 去除 XML 1.0 不允许的控制字符并转义全部特殊符号。 */
export function escapeXml(raw: string): string {
  let sanitized = ""
  for (let i = 0; i < raw.length; i++) {
    const code = raw.charCodeAt(i)
    if (
      code === 0x09 ||
      code === 0x0a ||
      code === 0x0d ||
      (code >= 0x20 && code !== 0x7f)
    ) {
      sanitized += raw[i]
    }
  }
  return sanitized
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;")
    .replace(/'/g, "&apos;")
}

function formatIsoUtcNoMs(date: Date): string {
  return date.toISOString().replace(/\.\d{3}Z$/, "Z")
}

function formatChineseDate(date: Date): string {
  const y = date.getFullYear()
  const m = String(date.getMonth() + 1).padStart(2, "0")
  const d = String(date.getDate()).padStart(2, "0")
  return `${y}-${m}-${d}`
}

function stripLeadingStepNumber(title: string): string {
  return title.replace(/^\d+\.\s*/, "")
}

type RunStyle = {
  bold?: boolean
  italic?: boolean
  color?: string
  sizeHalfPt?: number
  fontAscii?: string
  fontEastAsia?: string
  shading?: string
}

function wRun(text: string, style?: RunStyle): string {
  const rPrParts: string[] = []
  if (style?.fontAscii || style?.fontEastAsia) {
    const ascii = escapeXml(style.fontAscii ?? "Calibri")
    const eastAsia = escapeXml(style.fontEastAsia ?? "微软雅黑")
    rPrParts.push(
      `<w:rFonts w:ascii="${ascii}" w:hAnsi="${ascii}" w:eastAsia="${eastAsia}" w:cs="${ascii}"/>`
    )
  }
  if (style?.bold) {
    rPrParts.push("<w:b/>")
  }
  if (style?.italic) {
    rPrParts.push("<w:i/>")
  }
  if (style?.color) {
    rPrParts.push(`<w:color w:val="${style.color}"/>`)
  }
  if (style?.sizeHalfPt !== undefined) {
    rPrParts.push(
      `<w:sz w:val="${style.sizeHalfPt}"/><w:szCs w:val="${style.sizeHalfPt}"/>`
    )
  }
  if (style?.shading) {
    rPrParts.push(
      `<w:shd w:val="clear" w:color="auto" w:fill="${style.shading}"/>`
    )
  }

  const rPr = rPrParts.length > 0 ? `<w:rPr>${rPrParts.join("")}</w:rPr>` : ""
  const lines = text.split("\n")
  const textXml = lines
    .map((line, index) => {
      const needsPreserve = /^\s|\s$/.test(line)
      const attr = needsPreserve ? ' xml:space="preserve"' : ""
      const part = `<w:t${attr}>${escapeXml(line)}</w:t>`
      return index === 0 ? part : `<w:br/>${part}`
    })
    .join("")

  return `<w:r>${rPr}${textXml}</w:r>`
}

type ParagraphOptions = {
  styleId?: string
  keepNext?: boolean
  keepLines?: boolean
  pageBreakBefore?: boolean
  borderBottomColor?: string
  borderBottomSz?: number
  borderLeftColor?: string
  borderLeftSz?: number
  shading?: string
  spaceBefore?: number
  spaceAfter?: number
  lineSpacing?: number
  indentLeft?: number
  align?: "left" | "center" | "right" | "both"
}

function wParagraph(runsXml: string, options?: ParagraphOptions): string {
  const pPrParts: string[] = []
  if (options?.styleId) {
    pPrParts.push(`<w:pStyle w:val="${escapeXml(options.styleId)}"/>`)
  }
  if (options?.keepNext) {
    pPrParts.push("<w:keepNext/>")
  }
  if (options?.keepLines) {
    pPrParts.push("<w:keepLines/>")
  }
  if (options?.pageBreakBefore) {
    pPrParts.push("<w:pageBreakBefore/>")
  }
  if (options?.borderBottomColor || options?.borderLeftColor) {
    const bdrParts: string[] = []
    if (options.borderLeftColor) {
      bdrParts.push(
        `<w:left w:val="single" w:sz="${options.borderLeftSz ?? 18}" w:space="8" w:color="${options.borderLeftColor}"/>`
      )
    }
    if (options.borderBottomColor) {
      bdrParts.push(
        `<w:bottom w:val="single" w:sz="${options.borderBottomSz ?? 6}" w:space="4" w:color="${options.borderBottomColor}"/>`
      )
    }
    pPrParts.push(`<w:pBdr>${bdrParts.join("")}</w:pBdr>`)
  }
  if (options?.shading) {
    pPrParts.push(
      `<w:shd w:val="clear" w:color="auto" w:fill="${options.shading}"/>`
    )
  }
  if (
    options?.spaceBefore !== undefined ||
    options?.spaceAfter !== undefined ||
    options?.lineSpacing !== undefined
  ) {
    const attrs: string[] = []
    if (options.spaceBefore !== undefined) {
      attrs.push(`w:before="${options.spaceBefore}"`)
    }
    if (options.spaceAfter !== undefined) {
      attrs.push(`w:after="${options.spaceAfter}"`)
    }
    if (options.lineSpacing !== undefined) {
      attrs.push(`w:line="${options.lineSpacing}" w:lineRule="auto"`)
    }
    pPrParts.push(`<w:spacing ${attrs.join(" ")}/>`)
  }
  if (options?.indentLeft !== undefined) {
    pPrParts.push(`<w:ind w:left="${options.indentLeft}"/>`)
  }
  if (options?.align) {
    pPrParts.push(`<w:jc w:val="${options.align}"/>`)
  }

  const pPr = pPrParts.length > 0 ? `<w:pPr>${pPrParts.join("")}</w:pPr>` : ""
  return `<w:p>${pPr}${runsXml}</w:p>`
}

function wPageBreak(): string {
  return '<w:p><w:r><w:br w:type="page"/></w:r></w:p>'
}

type CellBorderSpec = {
  color: string
  sz?: number
  val?: "single" | "none"
}

type TableCellOptions = {
  widthDxa: number
  shading?: string
  vAlign?: "top" | "center" | "bottom"
  padTop?: number
  padBottom?: number
  padLeft?: number
  padRight?: number
  borderLeft?: CellBorderSpec
  borderTop?: CellBorderSpec
  borderRight?: CellBorderSpec
  borderBottom?: CellBorderSpec
}

function renderCellBorder(side: string, spec: CellBorderSpec): string {
  const val = spec.val ?? "single"
  if (val === "none") {
    return `<w:${side} w:val="none" w:sz="0" w:space="0" w:color="auto"/>`
  }
  return `<w:${side} w:val="${val}" w:sz="${spec.sz ?? 4}" w:space="0" w:color="${spec.color}"/>`
}

function wTableCell(contentXml: string, options: TableCellOptions): string {
  const tcPrParts: string[] = [
    `<w:tcW w:w="${options.widthDxa}" w:type="dxa"/>`,
  ]
  if (
    options.borderTop ||
    options.borderLeft ||
    options.borderBottom ||
    options.borderRight
  ) {
    const bdrs: string[] = []
    if (options.borderTop) bdrs.push(renderCellBorder("top", options.borderTop))
    if (options.borderLeft)
      bdrs.push(renderCellBorder("left", options.borderLeft))
    if (options.borderBottom)
      bdrs.push(renderCellBorder("bottom", options.borderBottom))
    if (options.borderRight)
      bdrs.push(renderCellBorder("right", options.borderRight))
    tcPrParts.push(`<w:tcBorders>${bdrs.join("")}</w:tcBorders>`)
  }
  if (options.shading) {
    tcPrParts.push(
      `<w:shd w:val="clear" w:color="auto" w:fill="${options.shading}"/>`
    )
  }
  if (
    options.padTop !== undefined ||
    options.padBottom !== undefined ||
    options.padLeft !== undefined ||
    options.padRight !== undefined
  ) {
    tcPrParts.push(
      `<w:tcMar>` +
        `<w:top w:w="${options.padTop ?? 90}" w:type="dxa"/>` +
        `<w:left w:w="${options.padLeft ?? 130}" w:type="dxa"/>` +
        `<w:bottom w:w="${options.padBottom ?? 90}" w:type="dxa"/>` +
        `<w:right w:w="${options.padRight ?? 130}" w:type="dxa"/>` +
        `</w:tcMar>`
    )
  }
  if (options.vAlign) {
    tcPrParts.push(`<w:vAlign w:val="${options.vAlign}"/>`)
  }
  return `<w:tc><w:tcPr>${tcPrParts.join("")}</w:tcPr>${contentXml}</w:tc>`
}

function wTableRow(
  cellsXml: string[],
  options?: { header?: boolean; cantSplit?: boolean }
): string {
  const trPrParts: string[] = []
  if (options?.cantSplit !== false) {
    trPrParts.push("<w:cantSplit/>")
  }
  if (options?.header) {
    trPrParts.push("<w:tblHeader/>")
  }
  const trPr =
    trPrParts.length > 0 ? `<w:trPr>${trPrParts.join("")}</w:trPr>` : ""
  return `<w:tr>${trPr}${cellsXml.join("")}</w:tr>`
}

function wTable(
  colWidthsDxa: number[],
  rowsXml: string[],
  options?: {
    borderColor?: string
    borderNone?: boolean
    cellMarTop?: number
    cellMarBottom?: number
    cellMarLeft?: number
    cellMarRight?: number
  }
): string {
  const totalWidth = colWidthsDxa.reduce((acc, w) => acc + w, 0)
  const borderColor = options?.borderColor ?? "CBD5E1"
  const borders = options?.borderNone
    ? `<w:tblBorders>` +
      `<w:top w:val="none" w:sz="0" w:space="0" w:color="auto"/>` +
      `<w:left w:val="none" w:sz="0" w:space="0" w:color="auto"/>` +
      `<w:bottom w:val="none" w:sz="0" w:space="0" w:color="auto"/>` +
      `<w:right w:val="none" w:sz="0" w:space="0" w:color="auto"/>` +
      `<w:insideH w:val="none" w:sz="0" w:space="0" w:color="auto"/>` +
      `<w:insideV w:val="none" w:sz="0" w:space="0" w:color="auto"/>` +
      `</w:tblBorders>`
    : `<w:tblBorders>` +
      `<w:top w:val="single" w:sz="4" w:space="0" w:color="${borderColor}"/>` +
      `<w:left w:val="single" w:sz="4" w:space="0" w:color="${borderColor}"/>` +
      `<w:bottom w:val="single" w:sz="4" w:space="0" w:color="${borderColor}"/>` +
      `<w:right w:val="single" w:sz="4" w:space="0" w:color="${borderColor}"/>` +
      `<w:insideH w:val="single" w:sz="4" w:space="0" w:color="E2E8F0"/>` +
      `<w:insideV w:val="single" w:sz="4" w:space="0" w:color="E2E8F0"/>` +
      `</w:tblBorders>`

  const tblGrid = colWidthsDxa.map((w) => `<w:gridCol w:w="${w}"/>`).join("")

  return (
    `<w:tbl>` +
    `<w:tblPr>` +
    `<w:tblW w:w="${totalWidth}" w:type="dxa"/>` +
    `<w:jc w:val="center"/>` +
    borders +
    `<w:tblLayout w:type="fixed"/>` +
    `<w:tblCellMar>` +
    `<w:top w:w="${options?.cellMarTop ?? 80}" w:type="dxa"/>` +
    `<w:left w:w="${options?.cellMarLeft ?? 120}" w:type="dxa"/>` +
    `<w:bottom w:w="${options?.cellMarBottom ?? 80}" w:type="dxa"/>` +
    `<w:right w:w="${options?.cellMarRight ?? 120}" w:type="dxa"/>` +
    `</w:tblCellMar>` +
    `</w:tblPr>` +
    `<w:tblGrid>${tblGrid}</w:tblGrid>` +
    rowsXml.join("") +
    `</w:tbl>`
  )
}

function computeImageEmu(
  width: number,
  height: number
): {
  cx: number
  cy: number
} {
  const safeW = Math.max(1, width)
  const safeH = Math.max(1, height)
  let cx = MAX_IMAGE_WIDTH_EMU
  let cy = Math.round((cx * safeH) / safeW)
  if (cy > MAX_IMAGE_HEIGHT_EMU) {
    cy = MAX_IMAGE_HEIGHT_EMU
    cx = Math.round((cy * safeW) / safeH)
  }
  return { cx, cy }
}

function wDrawingParagraph(params: {
  relId: string
  docPrId: number
  picPrId: number
  name: string
  description: string
  cx: number
  cy: number
  keepNext?: boolean
}): string {
  const safeName = escapeXml(params.name)
  const safeDescr = escapeXml(params.description)
  const drawingXml =
    `<w:r>` +
    `<w:drawing>` +
    `<wp:inline distT="0" distB="0" distL="0" distR="0">` +
    `<wp:extent cx="${params.cx}" cy="${params.cy}"/>` +
    `<wp:effectExtent l="0" t="0" r="0" b="0"/>` +
    `<wp:docPr id="${params.docPrId}" name="${safeName}" descr="${safeDescr}"/>` +
    `<wp:cNvGraphicFramePr>` +
    `<a:graphicFrameLocks xmlns:a="${A_NS}" noChangeAspect="1"/>` +
    `</wp:cNvGraphicFramePr>` +
    `<a:graphic xmlns:a="${A_NS}">` +
    `<a:graphicData uri="${PIC_NS}">` +
    `<pic:pic xmlns:pic="${PIC_NS}">` +
    `<pic:nvPicPr>` +
    `<pic:cNvPr id="${params.picPrId}" name="${safeName}" descr="${safeDescr}"/>` +
    `<pic:cNvPicPr/>` +
    `</pic:nvPicPr>` +
    `<pic:blipFill>` +
    `<a:blip r:embed="${params.relId}"/>` +
    `<a:stretch><a:fillRect/></a:stretch>` +
    `</pic:blipFill>` +
    `<pic:spPr>` +
    `<a:xfrm><a:off x="0" y="0"/><a:ext cx="${params.cx}" cy="${params.cy}"/></a:xfrm>` +
    `<a:prstGeom prst="rect"><a:avLst/></a:prstGeom>` +
    `</pic:spPr>` +
    `</pic:pic>` +
    `</a:graphicData>` +
    `</a:graphic>` +
    `</wp:inline>` +
    `</w:drawing>` +
    `</w:r>`

  return wParagraph(drawingXml, {
    align: "center",
    spaceBefore: 60,
    spaceAfter: params.keepNext ? 60 : 120,
    keepNext: params.keepNext,
  })
}

function distributeColumnWidths(columnCount: number): number[] {
  if (columnCount <= 1) {
    return [CONTENT_WIDTH_DXA]
  }
  if (columnCount === 2) {
    return [2600, CONTENT_WIDTH_DXA - 2600]
  }
  const first = Math.round(CONTENT_WIDTH_DXA * 0.24)
  const restCount = columnCount - 1
  const each = Math.floor((CONTENT_WIDTH_DXA - first) / restCount)
  const widths = [first]
  let used = first
  for (let i = 0; i < restCount; i++) {
    if (i === restCount - 1) {
      widths.push(CONTENT_WIDTH_DXA - used)
    } else {
      widths.push(each)
      used += each
    }
  }
  return widths
}

function buildCalloutBox(params: {
  label: string
  title: string
  content: string
  accentColor: string
  fillColor: string
}): string {
  const cellContent =
    wParagraph(
      wRun(`[${params.label}] `, {
        bold: true,
        color: params.accentColor,
        sizeHalfPt: 20,
      }) +
        wRun(params.title, {
          bold: true,
          color: "0F172A",
          sizeHalfPt: 20,
        }),
      { spaceBefore: 0, spaceAfter: 50, keepNext: true }
    ) +
    wParagraph(wRun(params.content, { color: "334155", sizeHalfPt: 19 }), {
      spaceBefore: 0,
      spaceAfter: 0,
      lineSpacing: 300,
    })

  const cell = wTableCell(cellContent, {
    widthDxa: CONTENT_WIDTH_DXA,
    shading: params.fillColor,
    padTop: 110,
    padBottom: 110,
    padLeft: 170,
    padRight: 150,
    borderLeft: { color: params.accentColor, sz: 24 },
    borderTop: { color: "E2E8F0", sz: 4 },
    borderRight: { color: "E2E8F0", sz: 4 },
    borderBottom: { color: "E2E8F0", sz: 4 },
  })

  return (
    wTable([CONTENT_WIDTH_DXA], [wTableRow([cell], { cantSplit: true })], {
      borderNone: true,
    }) + wParagraph("", { spaceBefore: 0, spaceAfter: 80 })
  )
}

function buildTipXml(tip: GuideTip): string {
  if (tip.variant === "warning") {
    return buildCalloutBox({
      label: "注意",
      title: tip.title,
      content: tip.content,
      accentColor: "D97706",
      fillColor: "FFFBEB",
    })
  }
  if (tip.variant === "tip") {
    return buildCalloutBox({
      label: "技巧",
      title: tip.title,
      content: tip.content,
      accentColor: "059669",
      fillColor: "ECFDF5",
    })
  }
  return buildCalloutBox({
    label: "说明",
    title: tip.title,
    content: tip.content,
    accentColor: "475569",
    fillColor: "F8FAFC",
  })
}

function buildCalloutLegendTable(callouts: GuideCallout[]): string {
  const colWidths = [680, CONTENT_WIDTH_DXA - 680]
  const rows = callouts.map((callout) => {
    const numCell = wTableCell(
      wParagraph(
        wRun(String(callout.index), {
          bold: true,
          color: "E11D48",
          sizeHalfPt: 20,
        }),
        { align: "center", spaceBefore: 0, spaceAfter: 0 }
      ),
      {
        widthDxa: colWidths[0],
        shading: "FFF1F2",
        vAlign: "top",
        padTop: 85,
        padBottom: 85,
        padLeft: 60,
        padRight: 60,
      }
    )

    const descCell = wTableCell(
      wParagraph(
        wRun(`${callout.label}：`, {
          bold: true,
          color: "0F172A",
          sizeHalfPt: 19,
        }) +
          wRun(callout.description, {
            color: "334155",
            sizeHalfPt: 19,
          }),
        { spaceBefore: 0, spaceAfter: 0, lineSpacing: 300 }
      ),
      {
        widthDxa: colWidths[1],
        shading: "FFFFFF",
        vAlign: "top",
        padTop: 85,
        padBottom: 85,
        padLeft: 130,
        padRight: 130,
      }
    )

    return wTableRow([numCell, descCell], { cantSplit: true })
  })

  return (
    wTable(colWidths, rows, { borderColor: "E2E8F0" }) +
    wParagraph("", { spaceBefore: 0, spaceAfter: 100 })
  )
}

function buildStepsXml(steps: GuideStep[]): string {
  const colWidths = [740, CONTENT_WIDTH_DXA - 740]
  const rows = steps.map((step, idx) => {
    const cleanTitle = stripLeadingStepNumber(step.title)
    const indexCell = wTableCell(
      wParagraph(
        wRun(String(idx + 1).padStart(2, "0"), {
          bold: true,
          color: "1E293B",
          sizeHalfPt: 19,
        }),
        { align: "center", spaceBefore: 0, spaceAfter: 0 }
      ),
      {
        widthDxa: colWidths[0],
        shading: "F1F5F9",
        vAlign: "top",
        padTop: 95,
        padBottom: 95,
        padLeft: 60,
        padRight: 60,
      }
    )

    const bodyParts = [
      wParagraph(
        wRun(cleanTitle, { bold: true, color: "0F172A", sizeHalfPt: 20 }),
        { spaceBefore: 0, spaceAfter: 45 }
      ),
      wParagraph(wRun(step.description, { color: "334155", sizeHalfPt: 19 }), {
        spaceBefore: 0,
        spaceAfter: step.tip ? 45 : 0,
        lineSpacing: 300,
      }),
    ]
    if (step.tip) {
      bodyParts.push(
        wParagraph(
          wRun("提示：", { bold: true, color: "64748B", sizeHalfPt: 18 }) +
            wRun(step.tip, { color: "64748B", sizeHalfPt: 18 }),
          { spaceBefore: 0, spaceAfter: 0, lineSpacing: 280 }
        )
      )
    }

    const contentCell = wTableCell(bodyParts.join(""), {
      widthDxa: colWidths[1],
      shading: "FFFFFF",
      vAlign: "top",
      padTop: 95,
      padBottom: 95,
      padLeft: 140,
      padRight: 140,
    })

    return wTableRow([indexCell, contentCell], { cantSplit: true })
  })

  return (
    wTable(colWidths, rows, { borderColor: "CBD5E1" }) +
    wParagraph("", { spaceBefore: 0, spaceAfter: 100 })
  )
}

function buildDataTableXml(table: GuideTable): string {
  const parts: string[] = []
  if (table.caption) {
    parts.push(
      wParagraph(
        wRun(table.caption, { bold: true, color: "475569", sizeHalfPt: 18 }),
        { spaceBefore: 60, spaceAfter: 50, keepNext: true }
      )
    )
  }

  const colCount = Math.max(
    table.headers.length,
    ...table.rows.map((r) => r.length)
  )
  const colWidths = distributeColumnWidths(colCount)

  const headerCells = colWidths.map((widthDxa, idx) =>
    wTableCell(
      wParagraph(
        wRun(table.headers[idx] ?? "", {
          bold: true,
          color: "0F172A",
          sizeHalfPt: 18,
        }),
        { spaceBefore: 0, spaceAfter: 0 }
      ),
      {
        widthDxa,
        shading: "F1F5F9",
        vAlign: "center",
        padTop: 90,
        padBottom: 90,
      }
    )
  )

  const bodyRows = table.rows.map((row, rowIdx) => {
    const fill = rowIdx % 2 === 1 ? "F8FAFC" : "FFFFFF"
    const cells = colWidths.map((widthDxa, cellIdx) =>
      wTableCell(
        wParagraph(
          wRun(row[cellIdx] ?? "", {
            bold: cellIdx === 0,
            color: cellIdx === 0 ? "0F172A" : "334155",
            sizeHalfPt: 18,
          }),
          { spaceBefore: 0, spaceAfter: 0, lineSpacing: 280 }
        ),
        {
          widthDxa,
          shading: fill,
          vAlign: "top",
          padTop: 80,
          padBottom: 80,
        }
      )
    )
    return wTableRow(cells, { cantSplit: true })
  })

  parts.push(
    wTable(
      colWidths,
      [wTableRow(headerCells, { header: true, cantSplit: true }), ...bodyRows],
      { borderColor: "CBD5E1" }
    )
  )
  parts.push(wParagraph("", { spaceBefore: 0, spaceAfter: 100 }))
  return parts.join("")
}

function buildShortcutGroupsXml(groups: GuideShortcutGroup[]): string {
  const colWidths = [2600, CONTENT_WIDTH_DXA - 2600]
  return groups
    .map((group) => {
      const heading = wParagraph(
        wRun(`快捷键 · ${group.title}`, {
          bold: true,
          color: "0F172A",
          sizeHalfPt: 20,
        }),
        { spaceBefore: 100, spaceAfter: 50, keepNext: true }
      )
      const headerRow = wTableRow(
        [
          wTableCell(
            wParagraph(
              wRun("快捷键", { bold: true, color: "0F172A", sizeHalfPt: 18 }),
              { spaceBefore: 0, spaceAfter: 0 }
            ),
            { widthDxa: colWidths[0], shading: "F1F5F9" }
          ),
          wTableCell(
            wParagraph(
              wRun("操作说明", { bold: true, color: "0F172A", sizeHalfPt: 18 }),
              { spaceBefore: 0, spaceAfter: 0 }
            ),
            { widthDxa: colWidths[1], shading: "F1F5F9" }
          ),
        ],
        { header: true, cantSplit: true }
      )

      const rows = group.items.map((item, idx) =>
        wTableRow(
          [
            wTableCell(
              wParagraph(
                wRun(item.keys.join(" / "), {
                  bold: true,
                  color: "0F172A",
                  sizeHalfPt: 18,
                  fontAscii: "Consolas",
                  fontEastAsia: "微软雅黑",
                }),
                { spaceBefore: 0, spaceAfter: 0 }
              ),
              {
                widthDxa: colWidths[0],
                shading: idx % 2 === 1 ? "F8FAFC" : "FFFFFF",
              }
            ),
            wTableCell(
              wParagraph(
                wRun(item.description, { color: "334155", sizeHalfPt: 18 }),
                { spaceBefore: 0, spaceAfter: 0, lineSpacing: 280 }
              ),
              {
                widthDxa: colWidths[1],
                shading: idx % 2 === 1 ? "F8FAFC" : "FFFFFF",
              }
            ),
          ],
          { cantSplit: true }
        )
      )

      return (
        heading +
        wTable(colWidths, [headerRow, ...rows], { borderColor: "CBD5E1" }) +
        wParagraph("", { spaceBefore: 0, spaceAfter: 90 })
      )
    })
    .join("")
}

function buildCoverAndTocXml(params: {
  title: string
  chapters: GuideChapter[]
  groupTitleById: Map<string, string>
  totalFigures: number
  generatedAt: Date
  workflowStages: GuideWorkflowStage[]
}): string {
  const parts: string[] = []

  // 顶部留白与产品标识条
  parts.push(wParagraph("", { spaceBefore: 600, spaceAfter: 0 }))
  parts.push(
    wParagraph(
      wRun("trainMaster · 工业视觉质检平台", {
        bold: true,
        color: "E11D48",
        sizeHalfPt: 22,
      }),
      { spaceBefore: 0, spaceAfter: 120 }
    )
  )
  parts.push(
    wParagraph(
      wRun(params.title, {
        bold: true,
        color: "0F172A",
        sizeHalfPt: 52,
      }),
      {
        spaceBefore: 0,
        spaceAfter: 160,
        borderBottomColor: "0F172A",
        borderBottomSz: 16,
      }
    )
  )
  parts.push(
    wParagraph(
      wRun(
        `面向质检工程师、标注员与算法工程师的离线图文手册，涵盖全部 ${params.chapters.length} 个已上线功能模块的界面说明、操作步骤与快捷键速查。`,
        { color: "475569", sizeHalfPt: 22 }
      ),
      { spaceBefore: 160, spaceAfter: 320, lineSpacing: 340 }
    )
  )

  // 文档概览信息表
  const metaWidths = [2400, CONTENT_WIDTH_DXA - 2400]
  const metaItems: [string, string][] = [
    [
      "适用系统",
      "trainMaster 工业视觉训练与质检平台（Web 端 + Windows 离线审图器）",
    ],
    [
      "内容规模",
      `共 ${params.chapters.length} 章，包含 ${params.totalFigures} 张带编号标注的真实界面截图`,
    ],
    ["导出日期", formatChineseDate(params.generatedAt)],
    [
      "阅读提示",
      "在 Word / WPS 中打开「视图 → 导航窗格」，可按章节与小节大纲一键跳转定位。",
    ],
  ]
  const metaRows = metaItems.map(([k, v]) =>
    wTableRow(
      [
        wTableCell(
          wParagraph(wRun(k, { bold: true, color: "475569", sizeHalfPt: 19 }), {
            spaceBefore: 0,
            spaceAfter: 0,
          }),
          { widthDxa: metaWidths[0], shading: "F8FAFC" }
        ),
        wTableCell(
          wParagraph(wRun(v, { color: "0F172A", sizeHalfPt: 19 }), {
            spaceBefore: 0,
            spaceAfter: 0,
          }),
          { widthDxa: metaWidths[1], shading: "FFFFFF" }
        ),
      ],
      { cantSplit: true }
    )
  )
  parts.push(wTable(metaWidths, metaRows, { borderColor: "CBD5E1" }))
  parts.push(wParagraph("", { spaceBefore: 0, spaceAfter: 260 }))

  // 端到端 6 步工作流概览
  if (params.workflowStages.length > 0) {
    parts.push(
      wParagraph(
        wRun("端到端工业视觉闭环路线", {
          bold: true,
          color: "0F172A",
          sizeHalfPt: 24,
        }),
        { spaceBefore: 120, spaceAfter: 100, keepNext: true }
      )
    )
    const wfWidths = [800, 2200, CONTENT_WIDTH_DXA - 3000]
    const wfHeader = wTableRow(
      [
        wTableCell(
          wParagraph(wRun("阶段", { bold: true, sizeHalfPt: 18 }), {
            align: "center",
            spaceBefore: 0,
            spaceAfter: 0,
          }),
          { widthDxa: wfWidths[0], shading: "F1F5F9" }
        ),
        wTableCell(
          wParagraph(wRun("环节名称", { bold: true, sizeHalfPt: 18 }), {
            spaceBefore: 0,
            spaceAfter: 0,
          }),
          { widthDxa: wfWidths[1], shading: "F1F5F9" }
        ),
        wTableCell(
          wParagraph(wRun("核心操作", { bold: true, sizeHalfPt: 18 }), {
            spaceBefore: 0,
            spaceAfter: 0,
          }),
          { widthDxa: wfWidths[2], shading: "F1F5F9" }
        ),
      ],
      { header: true, cantSplit: true }
    )
    const wfRows = params.workflowStages.map((stage, idx) =>
      wTableRow(
        [
          wTableCell(
            wParagraph(
              wRun(stage.step, {
                bold: true,
                color: "E11D48",
                sizeHalfPt: 18,
              }),
              { align: "center", spaceBefore: 0, spaceAfter: 0 }
            ),
            {
              widthDxa: wfWidths[0],
              shading: idx % 2 === 1 ? "F8FAFC" : "FFFFFF",
            }
          ),
          wTableCell(
            wParagraph(
              wRun(stage.title, {
                bold: true,
                color: "0F172A",
                sizeHalfPt: 18,
              }),
              { spaceBefore: 0, spaceAfter: 0 }
            ),
            {
              widthDxa: wfWidths[1],
              shading: idx % 2 === 1 ? "F8FAFC" : "FFFFFF",
            }
          ),
          wTableCell(
            wParagraph(wRun(stage.desc, { color: "334155", sizeHalfPt: 18 }), {
              spaceBefore: 0,
              spaceAfter: 0,
              lineSpacing: 270,
            }),
            {
              widthDxa: wfWidths[2],
              shading: idx % 2 === 1 ? "F8FAFC" : "FFFFFF",
            }
          ),
        ],
        { cantSplit: true }
      )
    )
    parts.push(
      wTable(wfWidths, [wfHeader, ...wfRows], { borderColor: "CBD5E1" })
    )
  }

  // 目录总览页
  parts.push(wPageBreak())
  parts.push(
    wParagraph(
      wRun("目录总览", { bold: true, color: "0F172A", sizeHalfPt: 30 }),
      {
        spaceBefore: 0,
        spaceAfter: 120,
        borderBottomColor: "CBD5E1",
        borderBottomSz: 8,
        keepNext: true,
      }
    )
  )
  parts.push(
    wParagraph(
      wRun(
        "本手册按实际操作顺序共分为以下章节。各章标题均采用标准 Word 标题样式（Heading 1 / Heading 2），可在左侧导航窗格中直接展开跳转。",
        { color: "64748B", sizeHalfPt: 19 }
      ),
      { spaceBefore: 40, spaceAfter: 160 }
    )
  )

  const tocWidths = [860, 1780, 2600, CONTENT_WIDTH_DXA - 860 - 1780 - 2600]
  const tocHeader = wTableRow(
    [
      wTableCell(
        wParagraph(wRun("章节", { bold: true, sizeHalfPt: 18 }), {
          align: "center",
          spaceBefore: 0,
          spaceAfter: 0,
        }),
        { widthDxa: tocWidths[0], shading: "F1F5F9" }
      ),
      wTableCell(
        wParagraph(wRun("所属分组", { bold: true, sizeHalfPt: 18 }), {
          spaceBefore: 0,
          spaceAfter: 0,
        }),
        { widthDxa: tocWidths[1], shading: "F1F5F9" }
      ),
      wTableCell(
        wParagraph(wRun("章节标题与页面路径", { bold: true, sizeHalfPt: 18 }), {
          spaceBefore: 0,
          spaceAfter: 0,
        }),
        { widthDxa: tocWidths[2], shading: "F1F5F9" }
      ),
      wTableCell(
        wParagraph(wRun("内容简介", { bold: true, sizeHalfPt: 18 }), {
          spaceBefore: 0,
          spaceAfter: 0,
        }),
        { widthDxa: tocWidths[3], shading: "F1F5F9" }
      ),
    ],
    { header: true, cantSplit: true }
  )

  const tocRows = params.chapters.map((chapter, idx) => {
    const fill = idx % 2 === 1 ? "F8FAFC" : "FFFFFF"
    const groupTitle = params.groupTitleById.get(chapter.groupId) ?? "操作指南"
    return wTableRow(
      [
        wTableCell(
          wParagraph(
            wRun(`第 ${String(idx + 1).padStart(2, "0")} 章`, {
              bold: true,
              color: "0F172A",
              sizeHalfPt: 17,
            }),
            { align: "center", spaceBefore: 0, spaceAfter: 0 }
          ),
          { widthDxa: tocWidths[0], shading: fill }
        ),
        wTableCell(
          wParagraph(wRun(groupTitle, { color: "475569", sizeHalfPt: 17 }), {
            spaceBefore: 0,
            spaceAfter: 0,
          }),
          { widthDxa: tocWidths[1], shading: fill }
        ),
        wTableCell(
          wParagraph(
            wRun(chapter.title, {
              bold: true,
              color: "0F172A",
              sizeHalfPt: 18,
            }),
            { spaceBefore: 0, spaceAfter: 30 }
          ) +
            wParagraph(
              wRun(chapter.targetPath, {
                color: "64748B",
                sizeHalfPt: 16,
                fontAscii: "Consolas",
                fontEastAsia: "微软雅黑",
              }),
              { spaceBefore: 0, spaceAfter: 0 }
            ),
          { widthDxa: tocWidths[2], shading: fill }
        ),
        wTableCell(
          wParagraph(
            wRun(chapter.summary, { color: "334155", sizeHalfPt: 17 }),
            { spaceBefore: 0, spaceAfter: 0, lineSpacing: 260 }
          ),
          { widthDxa: tocWidths[3], shading: fill }
        ),
      ],
      { cantSplit: true }
    )
  })

  parts.push(
    wTable(tocWidths, [tocHeader, ...tocRows], { borderColor: "CBD5E1" })
  )
  return parts.join("")
}

function buildStylesXml(): string {
  return (
    `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
    `<w:styles xmlns:w="${W_NS}">` +
    `<w:docDefaults>` +
    `<w:rPrDefault>` +
    `<w:rPr>` +
    `<w:rFonts w:ascii="Calibri" w:hAnsi="Calibri" w:eastAsia="微软雅黑" w:cs="Calibri"/>` +
    `<w:color w:val="1E293B"/>` +
    `<w:sz w:val="20"/>` +
    `<w:szCs w:val="20"/>` +
    `<w:lang w:val="en-US" w:eastAsia="zh-CN"/>` +
    `</w:rPr>` +
    `</w:rPrDefault>` +
    `<w:pPrDefault>` +
    `<w:pPr><w:spacing w:after="100" w:line="300" w:lineRule="auto"/></w:pPr>` +
    `</w:pPrDefault>` +
    `</w:docDefaults>` +
    `<w:style w:type="paragraph" w:default="1" w:styleId="Normal">` +
    `<w:name w:val="Normal"/>` +
    `<w:qFormat/>` +
    `</w:style>` +
    `<w:style w:type="paragraph" w:styleId="Heading1">` +
    `<w:name w:val="heading 1"/>` +
    `<w:basedOn w:val="Normal"/>` +
    `<w:next w:val="Normal"/>` +
    `<w:qFormat/>` +
    `<w:pPr>` +
    `<w:keepNext/>` +
    `<w:keepLines/>` +
    `<w:pBdr><w:bottom w:val="single" w:sz="12" w:space="6" w:color="0F172A"/></w:pBdr>` +
    `<w:spacing w:before="80" w:after="160"/>` +
    `<w:outlineLvl w:val="0"/>` +
    `</w:pPr>` +
    `<w:rPr>` +
    `<w:b/>` +
    `<w:color w:val="0F172A"/>` +
    `<w:sz w:val="34"/>` +
    `<w:szCs w:val="34"/>` +
    `</w:rPr>` +
    `</w:style>` +
    `<w:style w:type="paragraph" w:styleId="Heading2">` +
    `<w:name w:val="heading 2"/>` +
    `<w:basedOn w:val="Normal"/>` +
    `<w:next w:val="Normal"/>` +
    `<w:qFormat/>` +
    `<w:pPr>` +
    `<w:keepNext/>` +
    `<w:keepLines/>` +
    `<w:spacing w:before="240" w:after="100"/>` +
    `<w:outlineLvl w:val="1"/>` +
    `</w:pPr>` +
    `<w:rPr>` +
    `<w:b/>` +
    `<w:color w:val="0F172A"/>` +
    `<w:sz w:val="26"/>` +
    `<w:szCs w:val="26"/>` +
    `</w:rPr>` +
    `</w:style>` +
    `<w:style w:type="paragraph" w:styleId="Caption">` +
    `<w:name w:val="caption"/>` +
    `<w:basedOn w:val="Normal"/>` +
    `<w:pPr>` +
    `<w:keepNext/>` +
    `<w:spacing w:before="140" w:after="60"/>` +
    `</w:pPr>` +
    `<w:rPr>` +
    `<w:b/>` +
    `<w:color w:val="334155"/>` +
    `<w:sz w:val="19"/>` +
    `<w:szCs w:val="19"/>` +
    `</w:rPr>` +
    `</w:style>` +
    `</w:styles>`
  )
}

function buildHeaderXml(docTitle: string): string {
  return (
    `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
    `<w:hdr xmlns:w="${W_NS}">` +
    wParagraph(wRun(docTitle, { color: "64748B", sizeHalfPt: 16 }), {
      align: "right",
      spaceBefore: 0,
      spaceAfter: 0,
      borderBottomColor: "E2E8F0",
      borderBottomSz: 4,
    }) +
    `</w:hdr>`
  )
}

function wFieldRuns(fieldName: "PAGE" | "NUMPAGES"): string {
  const rPr = `<w:rPr><w:color w:val="64748B"/><w:sz w:val="16"/><w:szCs w:val="16"/></w:rPr>`
  return (
    `<w:r>${rPr}<w:fldChar w:fldCharType="begin"/></w:r>` +
    `<w:r>${rPr}<w:instrText xml:space="preserve"> ${fieldName} </w:instrText></w:r>` +
    `<w:r>${rPr}<w:fldChar w:fldCharType="separate"/></w:r>` +
    `<w:r>${rPr}<w:t>1</w:t></w:r>` +
    `<w:r>${rPr}<w:fldChar w:fldCharType="end"/></w:r>`
  )
}

function buildFooterXml(): string {
  return (
    `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
    `<w:ftr xmlns:w="${W_NS}">` +
    wParagraph(
      wRun("第 ", { color: "64748B", sizeHalfPt: 16 }) +
        wFieldRuns("PAGE") +
        wRun(" 页 / 共 ", { color: "64748B", sizeHalfPt: 16 }) +
        wFieldRuns("NUMPAGES") +
        wRun(" 页", { color: "64748B", sizeHalfPt: 16 }),
      { align: "center", spaceBefore: 0, spaceAfter: 0 }
    ) +
    `</w:ftr>`
  )
}

function buildContentTypesXml(): string {
  return (
    `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
    `<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">` +
    `<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>` +
    `<Default Extension="xml" ContentType="application/xml"/>` +
    `<Default Extension="jpg" ContentType="image/jpeg"/>` +
    `<Default Extension="png" ContentType="image/png"/>` +
    `<Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/>` +
    `<Override PartName="/word/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.styles+xml"/>` +
    `<Override PartName="/word/header1.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.header+xml"/>` +
    `<Override PartName="/word/footer1.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.footer+xml"/>` +
    `<Override PartName="/docProps/core.xml" ContentType="application/vnd.openxmlformats-package.core-properties+xml"/>` +
    `<Override PartName="/docProps/app.xml" ContentType="application/vnd.openxmlformats-officedocument.extended-properties+xml"/>` +
    `</Types>`
  )
}

function buildRootRelsXml(): string {
  return (
    `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
    `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
    `<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/>` +
    `<Relationship Id="rId2" Type="http://schemas.openxmlformats.org/package/2006/relationships/metadata/core-properties" Target="docProps/core.xml"/>` +
    `<Relationship Id="rId3" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/extended-properties" Target="docProps/app.xml"/>` +
    `</Relationships>`
  )
}

function buildCorePropsXml(title: string, generatedAt: Date): string {
  const iso = formatIsoUtcNoMs(generatedAt)
  return (
    `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
    `<cp:coreProperties ` +
    `xmlns:cp="http://schemas.openxmlformats.org/package/2006/metadata/core-properties" ` +
    `xmlns:dc="http://purl.org/dc/elements/1.1/" ` +
    `xmlns:dcterms="http://purl.org/dc/terms/" ` +
    `xmlns:dcmitype="http://purl.org/dc/dcmitype/" ` +
    `xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance">` +
    `<dc:title>${escapeXml(title)}</dc:title>` +
    `<dc:subject>trainMaster 工业视觉平台用户界面操作指南</dc:subject>` +
    `<dc:creator>trainMaster</dc:creator>` +
    `<cp:lastModifiedBy>trainMaster</cp:lastModifiedBy>` +
    `<dcterms:created xsi:type="dcterms:W3CDTF">${iso}</dcterms:created>` +
    `<dcterms:modified xsi:type="dcterms:W3CDTF">${iso}</dcterms:modified>` +
    `</cp:coreProperties>`
  )
}

function buildAppPropsXml(): string {
  return (
    `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
    `<Properties xmlns="http://schemas.openxmlformats.org/officeDocument/2006/extended-properties">` +
    `<Application>trainMaster Guide Exporter</Application>` +
    `</Properties>`
  )
}

/**
 * 纯前端零依赖构建符合 OOXML 标准的 Word `.docx` 文档字节流。
 * 支持全册（含封面、6 步路线表、总目录、分页章节与导航大纲）及单章节精简导出，
 * 可在浏览器与 Node 单元测试环境中直接运行。
 */
export function buildGuideDocx(
  chapters: GuideChapter[],
  groups: GuideGroup[],
  renderedImages: ReadonlyMap<string, RenderedFigureImage>,
  options?: BuildGuideDocxOptions
): Uint8Array {
  const generatedAt = options?.generatedAt ?? new Date()
  const isSingleChapter = options?.singleChapter ?? chapters.length === 1
  const docTitle =
    options?.title ??
    (isSingleChapter && chapters[0]
      ? `trainMaster 操作指南 — ${chapters[0].title}`
      : "trainMaster 平台界面操作指南")

  const groupTitleById = new Map(groups.map((g) => [g.id, g.title]))
  const totalFigures = chapters.reduce(
    (sum, ch) =>
      sum + ch.sections.reduce((acc, s) => acc + (s.figures?.length ?? 0), 0),
    0
  )

  const mediaEntries: ZipEntry[] = []
  const docRels: { id: string; type: string; target: string }[] = [
    {
      id: "rId1",
      type: "http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles",
      target: "styles.xml",
    },
    {
      id: "rId2",
      type: "http://schemas.openxmlformats.org/officeDocument/2006/relationships/header",
      target: "header1.xml",
    },
    {
      id: "rId3",
      type: "http://schemas.openxmlformats.org/officeDocument/2006/relationships/footer",
      target: "footer1.xml",
    },
  ]

  let nextRelSeq = 4
  let nextDrawingId = 1
  const mediaRelByFigureId = new Map<
    string,
    { relId: string; width: number; height: number }
  >()

  const registerFigureImage = (figure: GuideFigure) => {
    const existing = mediaRelByFigureId.get(figure.id)
    if (existing) {
      return existing
    }
    const rendered = renderedImages.get(figure.id)
    if (!rendered) {
      return null
    }
    const relId = `rId${nextRelSeq++}`
    const fileIndex = mediaEntries.length + 1
    const fileName = `fig_${fileIndex}.${rendered.extension}`
    mediaEntries.push({
      name: `word/media/${fileName}`,
      data: rendered.bytes,
      modifiedAt: generatedAt,
    })
    docRels.push({
      id: relId,
      type: "http://schemas.openxmlformats.org/officeDocument/2006/relationships/image",
      target: `media/${fileName}`,
    })
    const info = {
      relId,
      width: rendered.width,
      height: rendered.height,
    }
    mediaRelByFigureId.set(figure.id, info)
    return info
  }

  const bodyParts: string[] = []

  if (!isSingleChapter) {
    bodyParts.push(
      buildCoverAndTocXml({
        title: docTitle,
        chapters,
        groupTitleById,
        totalFigures,
        generatedAt,
        workflowStages: options?.workflowStages ?? guideWorkflowStages,
      })
    )
  } else if (chapters[0]) {
    bodyParts.push(
      wParagraph(
        wRun(
          `trainMaster 平台界面操作指南 · 导出日期 ${formatChineseDate(generatedAt)}`,
          {
            color: "64748B",
            sizeHalfPt: 17,
          }
        ),
        { spaceBefore: 0, spaceAfter: 80 }
      )
    )
  }

  const renderSection = (
    section: GuideSection,
    chapterNumber: number,
    sectionIndex: number,
    figureCounter: { count: number }
  ) => {
    bodyParts.push(
      wParagraph(wRun(`${sectionIndex}. ${section.title}`), {
        styleId: "Heading2",
        keepNext: true,
      })
    )

    if (section.summary) {
      bodyParts.push(
        wParagraph(wRun(section.summary, { color: "475569", sizeHalfPt: 20 }), {
          spaceBefore: 0,
          spaceAfter: 100,
          lineSpacing: 300,
        })
      )
    }

    if (section.paragraphs && section.paragraphs.length > 0) {
      for (const paragraph of section.paragraphs) {
        bodyParts.push(
          wParagraph(wRun(paragraph, { color: "1E293B", sizeHalfPt: 20 }), {
            spaceBefore: 0,
            spaceAfter: 90,
            lineSpacing: 300,
          })
        )
      }
    }

    if (section.figures && section.figures.length > 0) {
      for (const figure of section.figures) {
        figureCounter.count += 1
        const figLabel = `图 ${chapterNumber}-${figureCounter.count}：${figure.caption}`
        bodyParts.push(
          wParagraph(wRun(figLabel), { styleId: "Caption", keepNext: true })
        )

        const media = registerFigureImage(figure)
        if (media) {
          const { cx, cy } = computeImageEmu(media.width, media.height)
          const docPrId = nextDrawingId++
          const picPrId = nextDrawingId++
          bodyParts.push(
            wDrawingParagraph({
              relId: media.relId,
              docPrId,
              picPrId,
              name: figure.id,
              description: figure.alt || figure.caption,
              cx,
              cy,
              keepNext: figure.callouts.length > 0,
            })
          )
        } else {
          bodyParts.push(
            buildCalloutBox({
              label: "配图",
              title: figure.caption,
              content:
                figure.alt || "请在浏览器在线指南页面中查看交互式高清截图。",
              accentColor: "64748B",
              fillColor: "F8FAFC",
            })
          )
        }

        if (figure.callouts.length > 0) {
          bodyParts.push(buildCalloutLegendTable(figure.callouts))
        }
      }
    }

    if (section.steps && section.steps.length > 0) {
      bodyParts.push(buildStepsXml(section.steps))
    }

    if (section.table) {
      bodyParts.push(buildDataTableXml(section.table))
    }

    if (section.shortcuts && section.shortcuts.length > 0) {
      bodyParts.push(buildShortcutGroupsXml(section.shortcuts))
    }

    if (section.tips && section.tips.length > 0) {
      for (const tip of section.tips) {
        bodyParts.push(buildTipXml(tip))
      }
    }
  }

  chapters.forEach((chapter, chapterIdx) => {
    const chapterNumber = chapterIdx + 1
    const groupTitle = groupTitleById.get(chapter.groupId) ?? "操作指南"

    if (!isSingleChapter) {
      bodyParts.push(wPageBreak())
    }

    bodyParts.push(
      wParagraph(
        wRun(`所属分组：${groupTitle}    |    页面路径：`, {
          color: "64748B",
          sizeHalfPt: 17,
        }) +
          wRun(chapter.targetPath, {
            color: "0F172A",
            sizeHalfPt: 17,
            fontAscii: "Consolas",
            fontEastAsia: "微软雅黑",
          }),
        { spaceBefore: 0, spaceAfter: 60, keepNext: true }
      )
    )

    const headingTitle = isSingleChapter
      ? chapter.title
      : `第 ${String(chapterNumber).padStart(2, "0")} 章  ${chapter.title}`
    bodyParts.push(
      wParagraph(wRun(headingTitle), { styleId: "Heading1", keepNext: true })
    )

    // 章节导读摘要框
    bodyParts.push(
      buildCalloutBox({
        label: "本章导读",
        title: `对应功能入口：${chapter.targetLabel}`,
        content: chapter.summary,
        accentColor: "0F172A",
        fillColor: "F8FAFC",
      })
    )

    const figureCounter = { count: 0 }
    chapter.sections.forEach((section, sectionIdx) => {
      renderSection(section, chapterNumber, sectionIdx + 1, figureCounter)
    })
  })

  // 确保文档结尾前为段落，并附加 A4 页面配置与页眉页脚引用
  const sectPr =
    `<w:sectPr>` +
    `<w:headerReference w:type="default" r:id="rId2"/>` +
    `<w:footerReference w:type="default" r:id="rId3"/>` +
    `<w:pgSz w:w="${PAGE_WIDTH_DXA}" w:h="${PAGE_HEIGHT_DXA}"/>` +
    `<w:pgMar w:top="${PAGE_MARGIN_DXA}" w:right="${PAGE_MARGIN_DXA}" w:bottom="${PAGE_MARGIN_DXA}" w:left="${PAGE_MARGIN_DXA}" w:header="680" w:footer="680" w:gutter="0"/>` +
    (isSingleChapter ? "" : `<w:titlePg/>`) +
    `</w:sectPr>`

  const documentXml =
    `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
    `<w:document xmlns:w="${W_NS}" xmlns:r="${R_NS}" xmlns:wp="${WP_NS}">` +
    `<w:body>` +
    bodyParts.join("") +
    sectPr +
    `</w:body>` +
    `</w:document>`

  const documentRelsXml =
    `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
    `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
    docRels
      .map(
        (rel) =>
          `<Relationship Id="${rel.id}" Type="${rel.type}" Target="${escapeXml(rel.target)}"/>`
      )
      .join("") +
    `</Relationships>`

  const entries: ZipEntry[] = [
    {
      name: "[Content_Types].xml",
      data: encodeUtf8(buildContentTypesXml()),
      modifiedAt: generatedAt,
    },
    {
      name: "_rels/.rels",
      data: encodeUtf8(buildRootRelsXml()),
      modifiedAt: generatedAt,
    },
    {
      name: "docProps/core.xml",
      data: encodeUtf8(buildCorePropsXml(docTitle, generatedAt)),
      modifiedAt: generatedAt,
    },
    {
      name: "docProps/app.xml",
      data: encodeUtf8(buildAppPropsXml()),
      modifiedAt: generatedAt,
    },
    {
      name: "word/styles.xml",
      data: encodeUtf8(buildStylesXml()),
      modifiedAt: generatedAt,
    },
    {
      name: "word/header1.xml",
      data: encodeUtf8(buildHeaderXml(docTitle)),
      modifiedAt: generatedAt,
    },
    {
      name: "word/footer1.xml",
      data: encodeUtf8(buildFooterXml()),
      modifiedAt: generatedAt,
    },
    {
      name: "word/_rels/document.xml.rels",
      data: encodeUtf8(documentRelsXml),
      modifiedAt: generatedAt,
    },
    {
      name: "word/document.xml",
      data: encodeUtf8(documentXml),
      modifiedAt: generatedAt,
    },
    ...mediaEntries,
  ]

  return buildZipArchive(entries)
}
