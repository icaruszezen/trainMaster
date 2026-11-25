import {
  guideWorkflowStages,
  type GuideWorkflowStage,
} from "@/features/guide/chapters"
import type { RenderedFigureImage } from "@/features/guide/figure-render"
import type {
  GuideChapter,
  GuideGroup,
  GuideSection,
  GuideTip,
} from "@/features/guide/types"

export type BuildGuideHtmlOptions = {
  title?: string
  generatedAt?: Date
  workflowStages?: GuideWorkflowStage[]
}

/** HTML 文本与属性值转义。 */
export function escapeHtml(raw: string): string {
  return raw
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;")
    .replace(/'/g, "&#39;")
}

const BASE64_ALPHABET =
  "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"

/**
 * 纯 JS Base64 编码器，避免大数组展开触发调用栈溢出，同时兼容浏览器与 Node 测试环境。
 */
export function bytesToBase64(bytes: Uint8Array): string {
  const chunks: string[] = []
  const len = bytes.length
  for (let i = 0; i < len; i += 3) {
    const a = bytes[i]
    const b = i + 1 < len ? bytes[i + 1] : 0
    const c = i + 2 < len ? bytes[i + 2] : 0
    const triple = (a << 16) | (b << 8) | c

    chunks.push(
      BASE64_ALPHABET[(triple >>> 18) & 0x3f] +
        BASE64_ALPHABET[(triple >>> 12) & 0x3f] +
        (i + 1 < len ? BASE64_ALPHABET[(triple >>> 6) & 0x3f] : "=") +
        (i + 2 < len ? BASE64_ALPHABET[triple & 0x3f] : "=")
    )
  }
  return chunks.join("")
}

function formatDate(date: Date): string {
  const y = date.getFullYear()
  const m = String(date.getMonth() + 1).padStart(2, "0")
  const d = String(date.getDate()).padStart(2, "0")
  return `${y}-${m}-${d}`
}

function stripLeadingStepNumber(title: string): string {
  return title.replace(/^\d+\.\s*/, "")
}

function renderTipHtml(tip: GuideTip): string {
  const label =
    tip.variant === "warning" ? "注意" : tip.variant === "tip" ? "技巧" : "说明"
  return (
    `<div class="tip tip-${escapeHtml(tip.variant)}">` +
    `<div class="tip-title"><span class="tip-badge">${label}</span>${escapeHtml(tip.title)}</div>` +
    `<p>${escapeHtml(tip.content)}</p>` +
    `</div>`
  )
}

function renderSectionHtml(
  section: GuideSection,
  chapterIndex: number,
  sectionIndex: number,
  renderedImages: ReadonlyMap<string, RenderedFigureImage>,
  figureCounter: { count: number }
): string {
  const parts: string[] = []
  parts.push(
    `<section class="guide-section" id="${escapeHtml(`sec-${chapterIndex}-${section.id}`)}">`
  )
  parts.push(
    `<h3><span class="sec-num">${sectionIndex}</span>${escapeHtml(section.title)}</h3>`
  )

  if (section.summary) {
    parts.push(`<p class="sec-summary">${escapeHtml(section.summary)}</p>`)
  }

  if (section.paragraphs && section.paragraphs.length > 0) {
    for (const p of section.paragraphs) {
      parts.push(`<p class="body-p">${escapeHtml(p)}</p>`)
    }
  }

  if (section.figures && section.figures.length > 0) {
    for (const figure of section.figures) {
      figureCounter.count += 1
      const rendered = renderedImages.get(figure.id)
      parts.push(`<figure class="fig-card">`)
      parts.push(
        `<figcaption><span>图 ${chapterIndex}-${figureCounter.count}：${escapeHtml(figure.caption)}</span>` +
          (figure.callouts.length > 0
            ? `<span class="fig-count">${figure.callouts.length} 处界面标注</span>`
            : "") +
          `</figcaption>`
      )
      if (rendered) {
        const dataUri = `data:${rendered.contentType};base64,${bytesToBase64(rendered.bytes)}`
        parts.push(
          `<div class="fig-stage"><img src="${dataUri}" alt="${escapeHtml(figure.alt)}" width="${rendered.width}" height="${rendered.height}" loading="lazy"/></div>`
        )
      } else {
        parts.push(
          `<div class="fig-missing">配图：${escapeHtml(figure.caption)}（${escapeHtml(figure.alt)}）</div>`
        )
      }
      if (figure.callouts.length > 0) {
        parts.push(`<ol class="callout-grid">`)
        for (const callout of figure.callouts) {
          parts.push(
            `<li class="callout-item">` +
              `<span class="callout-pin">${callout.index}</span>` +
              `<div><div class="callout-label">${escapeHtml(callout.label)}</div>` +
              `<div class="callout-desc">${escapeHtml(callout.description)}</div></div>` +
              `</li>`
          )
        }
        parts.push(`</ol>`)
      }
      parts.push(`</figure>`)
    }
  }

  if (section.steps && section.steps.length > 0) {
    parts.push(`<ol class="step-list">`)
    section.steps.forEach((step, idx) => {
      parts.push(
        `<li class="step-item">` +
          `<span class="step-num">${idx + 1}</span>` +
          `<div class="step-body">` +
          `<div class="step-title">${escapeHtml(stripLeadingStepNumber(step.title))}</div>` +
          `<p>${escapeHtml(step.description)}</p>` +
          (step.tip
            ? `<p class="step-tip">提示：${escapeHtml(step.tip)}</p>`
            : "") +
          `</div></li>`
      )
    })
    parts.push(`</ol>`)
  }

  if (section.table) {
    parts.push(`<div class="table-wrap">`)
    if (section.table.caption) {
      parts.push(
        `<div class="table-caption">${escapeHtml(section.table.caption)}</div>`
      )
    }
    parts.push(`<table><thead><tr>`)
    for (const h of section.table.headers) {
      parts.push(`<th>${escapeHtml(h)}</th>`)
    }
    parts.push(`</tr></thead><tbody>`)
    for (const row of section.table.rows) {
      parts.push(`<tr>`)
      row.forEach((cell, idx) => {
        parts.push(
          `<td${idx === 0 ? ' class="col-key"' : ""}>${escapeHtml(cell)}</td>`
        )
      })
      parts.push(`</tr>`)
    }
    parts.push(`</tbody></table></div>`)
  }

  if (section.shortcuts && section.shortcuts.length > 0) {
    parts.push(`<div class="shortcut-grid">`)
    for (const group of section.shortcuts) {
      parts.push(`<div class="shortcut-card">`)
      parts.push(`<h4>${escapeHtml(group.title)}</h4><dl>`)
      for (const item of group.items) {
        const keysHtml = item.keys
          .map((k) => `<kbd>${escapeHtml(k)}</kbd>`)
          .join(" ")
        parts.push(
          `<div class="shortcut-row"><dt>${keysHtml}</dt><dd>${escapeHtml(item.description)}</dd></div>`
        )
      }
      parts.push(`</dl></div>`)
    }
    parts.push(`</div>`)
  }

  if (section.tips && section.tips.length > 0) {
    parts.push(`<div class="tip-stack">`)
    for (const tip of section.tips) {
      parts.push(renderTipHtml(tip))
    }
    parts.push(`</div>`)
  }

  parts.push(`</section>`)
  return parts.join("\n")
}

/**
 * 构建自包含的单文件离线 HTML 操作手册（所有标注截图内联为 Base64 Data URI），
 * 适用于产线内网、无网环境查阅及直接浏览器打印另存为 PDF。
 */
export function buildGuideOfflineHtml(
  chapters: GuideChapter[],
  groups: GuideGroup[],
  renderedImages: ReadonlyMap<string, RenderedFigureImage>,
  options?: BuildGuideHtmlOptions
): string {
  const title = options?.title ?? "trainMaster 平台界面操作指南"
  const generatedAt = options?.generatedAt ?? new Date()
  const workflowStages = options?.workflowStages ?? guideWorkflowStages
  const groupTitleById = new Map(groups.map((g) => [g.id, g.title]))
  const totalFigures = chapters.reduce(
    (sum, ch) =>
      sum + ch.sections.reduce((acc, s) => acc + (s.figures?.length ?? 0), 0),
    0
  )

  const sidebarGroupsHtml = groups
    .map((group) => {
      const groupChapters = chapters.filter((c) => c.groupId === group.id)
      if (groupChapters.length === 0) {
        return ""
      }
      const items = groupChapters
        .map((c) => {
          const idx = chapters.findIndex((x) => x.id === c.id) + 1
          return (
            `<li><a href="#ch-${escapeHtml(c.id)}" data-title="${escapeHtml(c.title.toLowerCase())}">` +
            `<span class="nav-idx">${String(idx).padStart(2, "0")}</span>` +
            `<span>${escapeHtml(c.shortTitle)}</span></a></li>`
          )
        })
        .join("")
      return `<div class="nav-group"><div class="nav-group-title">${escapeHtml(group.title)}</div><ul>${items}</ul></div>`
    })
    .filter(Boolean)
    .join("\n")

  const chaptersHtml = chapters
    .map((chapter, idx) => {
      const chapterNum = idx + 1
      const groupTitle = groupTitleById.get(chapter.groupId) ?? "操作指南"
      const figureCounter = { count: 0 }
      const workflowHtml =
        chapter.id === "quickstart" && workflowStages.length > 0
          ? `<div class="workflow-card">` +
            `<div class="workflow-head">端到端工业视觉闭环路线</div>` +
            `<ol class="workflow-grid">` +
            workflowStages
              .map(
                (stage) =>
                  `<li><a href="#ch-${escapeHtml(stage.chapterId)}">` +
                  `<div class="wf-title"><span class="wf-step">${escapeHtml(stage.step)}</span>${escapeHtml(stage.title)}</div>` +
                  `<p>${escapeHtml(stage.desc)}</p></a></li>`
              )
              .join("") +
            `</ol></div>`
          : ""

      const sectionsHtml = chapter.sections
        .map((section, sIdx) =>
          renderSectionHtml(
            section,
            chapterNum,
            sIdx + 1,
            renderedImages,
            figureCounter
          )
        )
        .join("\n")

      return (
        `<article class="chapter" id="ch-${escapeHtml(chapter.id)}">` +
        `<header class="chapter-header">` +
        `<div class="chapter-meta">` +
        `<span class="badge">${escapeHtml(groupTitle)}</span>` +
        `<span>第 ${String(chapterNum).padStart(2, "0")} / ${String(chapters.length).padStart(2, "0")} 章</span>` +
        `<span class="route-pill">${escapeHtml(chapter.targetPath)}</span>` +
        `</div>` +
        `<h2>${escapeHtml(chapter.title)}</h2>` +
        `<p class="chapter-summary">${escapeHtml(chapter.summary)}</p>` +
        `</header>` +
        workflowHtml +
        sectionsHtml +
        `</article>`
      )
    })
    .join("\n")

  return `<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="UTF-8" />
<meta name="viewport" content="width=device-width, initial-scale=1.0" />
<title>${escapeHtml(title)}</title>
<style>
  :root {
    --bg: #f8fafc;
    --card: #ffffff;
    --border: #e2e8f0;
    --ink: #0f172a;
    --body: #334155;
    --muted: #64748b;
    --rose: #e11d48;
    --rose-soft: #fff1f2;
  }
  * { box-sizing: border-box; }
  html { scroll-behavior: smooth; }
  body {
    margin: 0;
    background: var(--bg);
    color: var(--ink);
    font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", "PingFang SC", "Hiragino Sans GB", "Microsoft YaHei", sans-serif;
    line-height: 1.65;
  }
  .layout {
    display: grid;
    grid-template-columns: 272px minmax(0, 1fr);
    min-height: 100vh;
  }
  .sidebar {
    position: sticky;
    top: 0;
    height: 100vh;
    overflow-y: auto;
    background: var(--card);
    border-right: 1px solid var(--border);
    padding: 20px 16px;
    display: flex;
    flex-direction: column;
    gap: 14px;
  }
  .brand { font-size: 15px; font-weight: 700; color: var(--ink); }
  .brand-sub { font-size: 12px; color: var(--muted); margin-top: 2px; }
  .toolbar { display: flex; gap: 8px; }
  .filter-input {
    width: 100%;
    padding: 7px 10px;
    border: 1px solid var(--border);
    border-radius: 8px;
    font-size: 12px;
    outline: none;
  }
  .filter-input:focus { border-color: #94a3b8; }
  .print-btn {
    border: 1px solid var(--border);
    background: var(--card);
    color: var(--ink);
    padding: 7px 10px;
    border-radius: 8px;
    font-size: 12px;
    font-weight: 500;
    cursor: pointer;
    width: 100%;
  }
  .print-btn:hover { background: #f1f5f9; }
  .nav-group { margin-bottom: 12px; }
  .nav-group-title {
    font-size: 11px;
    font-weight: 600;
    color: var(--muted);
    padding: 0 8px 4px;
  }
  .sidebar ul { list-style: none; margin: 0; padding: 0; }
  .sidebar li a {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 6px 8px;
    border-radius: 7px;
    color: var(--body);
    text-decoration: none;
    font-size: 13px;
  }
  .sidebar li a:hover { background: #f1f5f9; color: var(--ink); }
  .nav-idx {
    font-size: 11px;
    color: var(--muted);
    font-variant-numeric: tabular-nums;
  }
  main {
    max-width: 1040px;
    width: 100%;
    margin: 0 auto;
    padding: 28px 32px 64px;
  }
  .doc-hero {
    background: var(--card);
    border: 1px solid var(--border);
    border-radius: 14px;
    padding: 24px 28px;
    margin-bottom: 28px;
  }
  .doc-hero-eyebrow { font-size: 12px; font-weight: 600; color: var(--rose); }
  .doc-hero h1 { margin: 6px 0 8px; font-size: 26px; line-height: 1.3; }
  .doc-hero p { margin: 0; font-size: 14px; color: var(--muted); }
  .hero-meta {
    display: flex;
    flex-wrap: wrap;
    gap: 14px;
    margin-top: 14px;
    font-size: 12px;
    color: var(--muted);
  }
  .chapter {
    background: var(--card);
    border: 1px solid var(--border);
    border-radius: 14px;
    padding: 24px 28px;
    margin-bottom: 28px;
  }
  .chapter-header { border-bottom: 1px solid var(--border); padding-bottom: 16px; margin-bottom: 20px; }
  .chapter-meta {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 8px;
    font-size: 12px;
    color: var(--muted);
    margin-bottom: 8px;
  }
  .badge {
    background: #f1f5f9;
    color: var(--ink);
    font-weight: 600;
    padding: 2px 8px;
    border-radius: 999px;
  }
  .route-pill {
    font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
    background: #f8fafc;
    border: 1px solid var(--border);
    padding: 1px 7px;
    border-radius: 6px;
    font-size: 11px;
  }
  .chapter-header h2 { margin: 0 0 6px; font-size: 22px; }
  .chapter-summary { margin: 0; font-size: 14px; color: var(--body); }
  .workflow-card {
    border: 1px solid var(--border);
    border-radius: 12px;
    padding: 16px;
    background: #f8fafc;
    margin-bottom: 24px;
  }
  .workflow-head { font-size: 14px; font-weight: 600; margin-bottom: 12px; }
  .workflow-grid {
    list-style: none;
    margin: 0;
    padding: 0;
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(220px, 1fr));
    gap: 10px;
  }
  .workflow-grid a {
    display: block;
    height: 100%;
    text-decoration: none;
    color: inherit;
    background: var(--card);
    border: 1px solid var(--border);
    border-radius: 10px;
    padding: 12px;
  }
  .workflow-grid a:hover { border-color: #94a3b8; }
  .wf-title { display: flex; align-items: center; gap: 6px; font-size: 13px; font-weight: 600; margin-bottom: 4px; }
  .wf-step {
    background: var(--rose-soft);
    color: var(--rose);
    font-size: 11px;
    font-weight: 700;
    padding: 1px 6px;
    border-radius: 5px;
  }
  .workflow-grid p { margin: 0; font-size: 12px; color: var(--muted); }
  .guide-section { margin-top: 26px; }
  .guide-section h3 {
    display: flex;
    align-items: center;
    gap: 8px;
    margin: 0 0 8px;
    font-size: 17px;
  }
  .sec-num {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 24px;
    height: 24px;
    border-radius: 6px;
    background: #f1f5f9;
    color: var(--muted);
    font-size: 12px;
    font-weight: 700;
  }
  .sec-summary { margin: 0 0 12px; font-size: 14px; color: var(--muted); }
  .body-p { margin: 0 0 10px; font-size: 14px; color: var(--body); }
  .fig-card {
    margin: 14px 0 18px;
    border: 1px solid var(--border);
    border-radius: 12px;
    overflow: hidden;
    background: var(--card);
    break-inside: avoid;
  }
  .fig-card figcaption {
    display: flex;
    flex-wrap: wrap;
    justify-content: space-between;
    gap: 8px;
    padding: 10px 14px;
    background: #f8fafc;
    border-bottom: 1px solid var(--border);
    font-size: 13px;
    font-weight: 600;
  }
  .fig-count {
    font-size: 11px;
    font-weight: 600;
    color: var(--muted);
    background: #e2e8f0;
    padding: 1px 8px;
    border-radius: 999px;
  }
  .fig-stage { padding: 12px; background: #f8fafc; }
  .fig-stage img {
    display: block;
    width: 100%;
    height: auto;
    border-radius: 8px;
    border: 1px solid var(--border);
  }
  .fig-missing { padding: 16px; font-size: 13px; color: var(--muted); background: #f8fafc; }
  .callout-grid {
    list-style: none;
    margin: 0;
    padding: 12px;
    border-top: 1px solid var(--border);
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(300px, 1fr));
    gap: 8px;
  }
  .callout-item {
    display: flex;
    align-items: flex-start;
    gap: 10px;
    padding: 8px 10px;
    border: 1px solid var(--border);
    border-radius: 8px;
    background: #ffffff;
  }
  .callout-pin {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 22px;
    height: 22px;
    flex-shrink: 0;
    border-radius: 999px;
    background: var(--rose);
    color: #ffffff;
    font-size: 12px;
    font-weight: 700;
    margin-top: 1px;
  }
  .callout-label { font-size: 13px; font-weight: 600; color: var(--ink); }
  .callout-desc { font-size: 12px; color: var(--body); margin-top: 2px; }
  .step-list { list-style: none; margin: 12px 0 16px; padding: 0; display: grid; gap: 10px; }
  .step-item {
    display: flex;
    gap: 12px;
    align-items: flex-start;
    padding: 12px 14px;
    border: 1px solid var(--border);
    border-radius: 10px;
    background: #f8fafc;
    break-inside: avoid;
  }
  .step-num {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 24px;
    height: 24px;
    flex-shrink: 0;
    border-radius: 999px;
    background: var(--ink);
    color: #ffffff;
    font-size: 12px;
    font-weight: 700;
  }
  .step-title { font-size: 14px; font-weight: 600; }
  .step-body p { margin: 4px 0 0; font-size: 13px; color: var(--body); }
  .step-tip { font-size: 12px !important; color: var(--muted) !important; }
  .table-wrap {
    margin: 12px 0 16px;
    border: 1px solid var(--border);
    border-radius: 10px;
    overflow-x: auto;
    background: var(--card);
  }
  .table-caption {
    padding: 8px 14px;
    background: #f8fafc;
    border-bottom: 1px solid var(--border);
    font-size: 12px;
    font-weight: 600;
    color: var(--muted);
  }
  table { width: 100%; border-collapse: collapse; font-size: 13px; }
  th, td { padding: 9px 12px; border-bottom: 1px solid var(--border); text-align: left; vertical-align: top; }
  thead th { background: #f1f5f9; font-weight: 600; color: var(--ink); white-space: nowrap; }
  tbody tr:last-child td { border-bottom: none; }
  td.col-key { font-weight: 600; white-space: nowrap; color: var(--ink); }
  .shortcut-grid {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(280px, 1fr));
    gap: 12px;
    margin: 12px 0 16px;
  }
  .shortcut-card {
    border: 1px solid var(--border);
    border-radius: 10px;
    padding: 12px 14px;
    background: var(--card);
    break-inside: avoid;
  }
  .shortcut-card h4 { margin: 0 0 8px; font-size: 13px; }
  .shortcut-card dl { margin: 0; }
  .shortcut-row {
    display: flex;
    justify-content: space-between;
    gap: 12px;
    padding: 6px 0;
    border-top: 1px solid #f1f5f9;
    font-size: 12px;
  }
  .shortcut-row:first-child { border-top: none; padding-top: 0; }
  .shortcut-row dt { display: flex; flex-wrap: wrap; gap: 4px; flex-shrink: 0; }
  .shortcut-row dd { margin: 0; color: var(--muted); text-align: right; }
  kbd {
    display: inline-block;
    padding: 1px 6px;
    font-size: 11px;
    font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
    background: #f1f5f9;
    border: 1px solid #cbd5e1;
    border-radius: 5px;
    color: var(--ink);
  }
  .tip-stack { display: grid; gap: 10px; margin-top: 12px; }
  .tip {
    border: 1px solid var(--border);
    border-left-width: 4px;
    border-radius: 8px;
    padding: 10px 14px;
    break-inside: avoid;
  }
  .tip-tip { border-left-color: #059669; background: #ecfdf5; }
  .tip-warning { border-left-color: #d97706; background: #fffbeb; }
  .tip-note { border-left-color: #64748b; background: #f8fafc; }
  .tip-title { font-size: 13px; font-weight: 600; margin-bottom: 3px; }
  .tip-badge {
    display: inline-block;
    margin-right: 6px;
    font-size: 11px;
    padding: 0 6px;
    border-radius: 4px;
    background: rgba(15, 23, 42, 0.08);
  }
  .tip p { margin: 0; font-size: 13px; color: var(--body); }
  @media (max-width: 960px) {
    .layout { grid-template-columns: 1fr; }
    .sidebar { position: static; height: auto; border-right: none; border-bottom: 1px solid var(--border); }
    main { padding: 18px 16px 40px; }
  }
  @media print {
    body { background: #ffffff; }
    .sidebar { display: none; }
    .layout { display: block; }
    main { max-width: none; padding: 0; }
    .doc-hero, .chapter { border: none; border-radius: 0; padding: 0 0 18px; break-after: page; }
    .chapter:last-child { break-after: auto; }
  }
</style>
</head>
<body>
<div class="layout">
  <aside class="sidebar">
    <div>
      <div class="brand">trainMaster 操作指南</div>
      <div class="brand-sub">离线完整版 · ${escapeHtml(formatDate(generatedAt))}</div>
    </div>
    <input id="nav-filter" class="filter-input" type="search" placeholder="筛选章节名称…" aria-label="筛选章节" />
    <button type="button" class="print-btn" onclick="window.print()">打印 / 另存为 PDF</button>
    <nav aria-label="离线手册目录">
      ${sidebarGroupsHtml}
    </nav>
  </aside>
  <main>
    <header class="doc-hero">
      <div class="doc-hero-eyebrow">trainMaster · 工业视觉质检平台离线操作手册</div>
      <h1>${escapeHtml(title)}</h1>
      <p>包含全部 ${chapters.length} 个已上线功能模块的真实标注截图、操作步骤、快捷键与常见注意事项，支持断网环境直接查阅或打印归档。</p>
      <div class="hero-meta">
        <span>章节总数：${chapters.length} 章</span>
        <span>界面配图：${totalFigures} 张</span>
        <span>导出日期：${escapeHtml(formatDate(generatedAt))}</span>
      </div>
    </header>
    ${chaptersHtml}
  </main>
</div>
<script>
  (function () {
    var input = document.getElementById("nav-filter");
    if (!input) return;
    input.addEventListener("input", function () {
      var q = input.value.trim().toLowerCase();
      var links = document.querySelectorAll(".sidebar li a");
      links.forEach(function (a) {
        var matched = !q || (a.getAttribute("data-title") || "").indexOf(q) !== -1;
        a.parentElement.style.display = matched ? "" : "none";
      });
    });
  })();
</script>
</body>
</html>`
}
