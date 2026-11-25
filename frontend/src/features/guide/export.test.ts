import { describe, expect, it } from "vitest"

import {
  getGuideChapter,
  guideChapters,
  guideGroups,
} from "@/features/guide/chapters"
import { sanitizeFileSegment } from "@/features/guide/download-guide"
import { buildGuideDocx, escapeXml } from "@/features/guide/export-docx"
import {
  buildGuideOfflineHtml,
  bytesToBase64,
  escapeHtml,
} from "@/features/guide/export-html"
import type { RenderedFigureImage } from "@/features/guide/figure-render"
import type { GuideChapter } from "@/features/guide/types"
import { buildZipArchive, crc32, encodeUtf8 } from "@/features/guide/zip"

const textDecoder = new TextDecoder("utf-8")

/** 轻量 PKZIP 解析器（用于单测验证 Local Header / Central Directory / EOCD 与 CRC32）。 */
function parseZipEntries(zipBytes: Uint8Array): Map<string, Uint8Array> {
  const view = new DataView(
    zipBytes.buffer,
    zipBytes.byteOffset,
    zipBytes.byteLength
  )
  const eocdOffset = zipBytes.byteLength - 22
  expect(view.getUint32(eocdOffset, true)).toBe(0x06054b50)

  const totalEntries = view.getUint16(eocdOffset + 10, true)
  const centralDirSize = view.getUint32(eocdOffset + 12, true)
  const centralDirOffset = view.getUint32(eocdOffset + 16, true)
  expect(centralDirOffset + centralDirSize).toBe(eocdOffset)

  const files = new Map<string, Uint8Array>()
  let cdCursor = centralDirOffset

  for (let i = 0; i < totalEntries; i++) {
    expect(view.getUint32(cdCursor, true)).toBe(0x02014b50)
    const method = view.getUint16(cdCursor + 10, true)
    expect(method).toBe(0)
    const expectedCrc = view.getUint32(cdCursor + 16, true)
    const compressedSize = view.getUint32(cdCursor + 20, true)
    const uncompressedSize = view.getUint32(cdCursor + 24, true)
    expect(compressedSize).toBe(uncompressedSize)

    const nameLen = view.getUint16(cdCursor + 28, true)
    const extraLen = view.getUint16(cdCursor + 30, true)
    const commentLen = view.getUint16(cdCursor + 32, true)
    const localHeaderOffset = view.getUint32(cdCursor + 42, true)

    const name = textDecoder.decode(
      zipBytes.subarray(cdCursor + 46, cdCursor + 46 + nameLen)
    )
    cdCursor += 46 + nameLen + extraLen + commentLen

    // 验证对应的 Local File Header 与实际内容 CRC
    expect(view.getUint32(localHeaderOffset, true)).toBe(0x04034b50)
    const localNameLen = view.getUint16(localHeaderOffset + 26, true)
    const localExtraLen = view.getUint16(localHeaderOffset + 28, true)
    const dataStart = localHeaderOffset + 30 + localNameLen + localExtraLen
    const data = zipBytes.subarray(dataStart, dataStart + compressedSize)
    expect(crc32(data)).toBe(expectedCrc)

    files.set(name, data)
  }

  expect(cdCursor).toBe(eocdOffset)
  return files
}

function makeFakeRenderedImages(
  chapters: GuideChapter[]
): Map<string, RenderedFigureImage> {
  // 最小合法 JPEG 字节头尾占位，用于纯 Node 环境单测
  const fakeJpeg = new Uint8Array([
    0xff, 0xd8, 0xff, 0xe0, 0x00, 0x10, 0xff, 0xd9,
  ])
  const map = new Map<string, RenderedFigureImage>()
  for (const ch of chapters) {
    for (const sec of ch.sections) {
      for (const fig of sec.figures ?? []) {
        map.set(fig.id, {
          bytes: fakeJpeg,
          width: fig.width,
          height: fig.height,
          contentType: "image/jpeg",
          extension: "jpg",
        })
      }
    }
  }
  return map
}

describe("zip archive builder", () => {
  it("computes standard IEEE 802.3 CRC-32 checksums", () => {
    expect(crc32(new Uint8Array(0))).toBe(0x00000000)
    expect(crc32(encodeUtf8("123456789"))).toBe(0xcbf43926)
  })

  it("packages UTF-8 filenames and payloads into a valid PKZIP archive", () => {
    const zip = buildZipArchive([
      {
        name: "hello.txt",
        data: encodeUtf8("trainMaster"),
        modifiedAt: new Date("2026-05-01T08:00:00Z"),
      },
      {
        name: "目录/操作指南.txt",
        data: encodeUtf8("工业视觉质检平台"),
        modifiedAt: new Date("2026-05-01T08:00:00Z"),
      },
    ])

    const entries = parseZipEntries(zip)
    expect(entries.size).toBe(2)
    expect(textDecoder.decode(entries.get("hello.txt"))).toBe("trainMaster")
    expect(textDecoder.decode(entries.get("目录/操作指南.txt"))).toBe(
      "工业视觉质检平台"
    )
  })
})

describe("Word .docx guide exporter", () => {
  it("escapes XML special characters and strips invalid XML 1.0 control chars", () => {
    expect(escapeXml(`a & b < c > "d" 'e'\u0000\u0007\t\n`)).toBe(
      "a &amp; b &lt; c &gt; &quot;d&quot; &apos;e&apos;\t\n"
    )
  })

  it("builds a complete OOXML .docx for all 13 guide chapters with 27 images", () => {
    const renderedImages = makeFakeRenderedImages(guideChapters)
    const docx = buildGuideDocx(guideChapters, guideGroups, renderedImages, {
      generatedAt: new Date("2026-06-15T10:30:00Z"),
    })

    const entries = parseZipEntries(docx)
    const requiredParts = [
      "[Content_Types].xml",
      "_rels/.rels",
      "docProps/core.xml",
      "docProps/app.xml",
      "word/styles.xml",
      "word/header1.xml",
      "word/footer1.xml",
      "word/_rels/document.xml.rels",
      "word/document.xml",
    ]
    for (const part of requiredParts) {
      expect(entries.has(part), `missing ${part}`).toBe(true)
    }

    const mediaFiles = [...entries.keys()].filter((name) =>
      name.startsWith("word/media/")
    )
    expect(mediaFiles).toHaveLength(renderedImages.size)

    const docXml = textDecoder.decode(entries.get("word/document.xml"))
    const relsXml = textDecoder.decode(
      entries.get("word/_rels/document.xml.rels")
    )

    // 包含封面、总目录与所有章节标题
    expect(docXml).toContain("目录总览")
    expect(docXml).toContain("端到端工业视觉闭环路线")
    expect(docXml).toContain("<w:titlePg/>")
    for (const chapter of guideChapters) {
      expect(docXml).toContain(escapeXml(chapter.title))
    }

    // 确认步骤编号不会出现重复前缀（如 "1. 1."）
    expect(docXml).not.toContain(">1. 创建项目")

    // 所有 docPr 与 cNvPr id 必须全局唯一且为正整数
    const drawingIds = [
      ...docXml.matchAll(/<(?:wp:docPr|pic:cNvPr)\s+id="(\d+)"/g),
    ].map((m) => Number(m[1]))
    expect(drawingIds.length).toBe(renderedImages.size * 2)
    expect(new Set(drawingIds).size).toBe(drawingIds.length)
    expect(Math.min(...drawingIds)).toBeGreaterThanOrEqual(1)

    // 每一个图片引用 r:embed 都能在 rels 与 word/media 中一一对应
    const relTargets = new Map<string, string>()
    for (const m of relsXml.matchAll(
      /<Relationship Id="([^"]+)"[^>]+Target="([^"]+)"/g
    )) {
      relTargets.set(m[1], m[2])
    }
    const embedIds = [...docXml.matchAll(/r:embed="(rId\d+)"/g)].map(
      (m) => m[1]
    )
    expect(embedIds.length).toBe(renderedImages.size)
    for (const rId of embedIds) {
      const target = relTargets.get(rId)
      expect(target, `missing rel ${rId}`).toBeDefined()
      expect(entries.has(`word/${target}`), `missing word/${target}`).toBe(true)
    }
  })

  it("builds a compact single-chapter .docx without full-book cover or TOC", () => {
    const annotationChapter = getGuideChapter("annotation")!
    const images = makeFakeRenderedImages([annotationChapter])
    const docx = buildGuideDocx([annotationChapter], guideGroups, images, {
      singleChapter: true,
      generatedAt: new Date("2026-06-15T10:30:00Z"),
    })

    const entries = parseZipEntries(docx)
    const docXml = textDecoder.decode(entries.get("word/document.xml"))
    expect(docXml).not.toContain("目录总览")
    expect(docXml).not.toContain("<w:titlePg/>")
    expect(docXml).toContain(escapeXml(annotationChapter.title))

    const mediaFiles = [...entries.keys()].filter((name) =>
      name.startsWith("word/media/")
    )
    expect(mediaFiles).toHaveLength(images.size)
  })

  it("gracefully falls back when figure images are unavailable and escapes unsafe text", () => {
    const syntheticChapter: GuideChapter = {
      id: "quickstart",
      groupId: "quickstart",
      title: `测试章节 <XML> & "引号"`,
      shortTitle: "测试章节",
      summary: "包含 <tag> & '单引号' 的摘要",
      moduleId: "dashboard",
      targetPath: "/",
      targetLabel: "工作台",
      keywords: [],
      sections: [
        {
          id: "s1",
          title: "缺失图片回退",
          figures: [
            {
              id: "fig-missing",
              src: "/missing.webp",
              alt: "替代文案 <alt>",
              caption: "示例图 & 标题",
              width: 1600,
              height: 1000,
              callouts: [],
            },
          ],
        },
      ],
    }

    const docx = buildGuideDocx([syntheticChapter], guideGroups, new Map(), {
      singleChapter: true,
    })
    const entries = parseZipEntries(docx)
    const docXml = textDecoder.decode(entries.get("word/document.xml"))
    expect(docXml).not.toContain("r:embed=")
    expect(docXml).toContain("示例图 &amp; 标题")
    expect(docXml).toContain("测试章节 &lt;XML&gt; &amp; &quot;引号&quot;")
  })
})

describe("offline HTML guide exporter", () => {
  it("encodes arbitrary byte arrays to standard base64", () => {
    for (const size of [0, 1, 2, 3, 17, 4096]) {
      const sample = new Uint8Array(size)
      let binary = ""
      for (let i = 0; i < size; i++) {
        sample[i] = (i * 37 + 13) & 0xff
        binary += String.fromCharCode(sample[i])
      }
      const encoded = bytesToBase64(sample)
      expect(encoded).toBe(btoa(binary))
      const decoded = atob(encoded)
      expect(decoded.length).toBe(size)
    }
  })

  it("generates self-contained HTML with embedded data URI figures and safe escaping", () => {
    const renderedImages = makeFakeRenderedImages(guideChapters)
    const html = buildGuideOfflineHtml(
      guideChapters,
      guideGroups,
      renderedImages,
      {
        title: `trainMaster <操作指南> & "离线版"`,
        generatedAt: new Date("2026-06-15T10:30:00Z"),
      }
    )

    expect(html.startsWith("<!DOCTYPE html>")).toBe(true)
    expect(html).toContain(escapeHtml(`trainMaster <操作指南> & "离线版"`))
    expect(html).toContain("data:image/jpeg;base64,")
    for (const chapter of guideChapters) {
      expect(html).toContain(`id="ch-${chapter.id}"`)
    }
  })
})

describe("sanitizeFileSegment", () => {
  it("strips path separators and illegal filename characters", () => {
    expect(sanitizeFileSegment("数据版本 / 标注:管理*?")).toBe(
      "数据版本-标注-管理"
    )
    expect(sanitizeFileSegment("  快速上手与工作台  ")).toBe("快速上手与工作台")
  })
})
