import type { GuideChapter } from "@/features/guide/types"

export type GuideSearchMatch = {
  chapter: GuideChapter
  /** 命中的首个小节标题，便于在搜索结果中展示定位上下文。 */
  matchedSectionTitle?: string
  matchedSectionId?: string
}

/**
 * 在全部操作指南章节中执行不区分大小写的关键词检索：
 * 匹配标题、概述、关键词、小节标题、截图说明与交互标注、操作步骤、表格和快捷键。
 */
export function searchGuideChapters(
  chapters: readonly GuideChapter[],
  rawQuery: string
): GuideSearchMatch[] {
  const query = rawQuery.trim().toLowerCase()
  if (!query) {
    return chapters.map((chapter) => ({ chapter }))
  }

  const tokens = query.split(/\s+/).filter(Boolean)
  const matches: GuideSearchMatch[] = []

  for (const chapter of chapters) {
    const chapterText = [
      chapter.title,
      chapter.shortTitle,
      chapter.summary,
      chapter.targetLabel,
      ...chapter.keywords,
    ]
      .join(" ")
      .toLowerCase()

    const sectionTexts = chapter.sections.map((section) => {
      const parts: string[] = [section.title, section.summary ?? ""]
      if (section.paragraphs) {
        parts.push(...section.paragraphs)
      }
      if (section.figures) {
        for (const fig of section.figures) {
          parts.push(fig.caption, fig.alt)
          for (const c of fig.callouts) {
            parts.push(c.label, c.description)
          }
        }
      }
      if (section.steps) {
        for (const step of section.steps) {
          parts.push(step.title, step.description, step.tip ?? "")
        }
      }
      if (section.table) {
        parts.push(
          section.table.caption ?? "",
          ...section.table.headers,
          ...section.table.rows.flat()
        )
      }
      if (section.shortcuts) {
        for (const grp of section.shortcuts) {
          parts.push(grp.title)
          for (const item of grp.items) {
            parts.push(...item.keys, item.description)
          }
        }
      }
      if (section.tips) {
        for (const tip of section.tips) {
          parts.push(tip.title, tip.content)
        }
      }
      return {
        id: section.id,
        title: section.title,
        text: parts.join(" ").toLowerCase(),
      }
    })

    const combined = `${chapterText} ${sectionTexts.map((s) => s.text).join(" ")}`
    if (!tokens.every((token) => combined.includes(token))) {
      continue
    }

    const firstSection = sectionTexts.find((s) =>
      tokens.some((token) => s.text.includes(token))
    )
    matches.push({
      chapter,
      matchedSectionId: firstSection?.id,
      matchedSectionTitle: firstSection?.title,
    })
  }

  return matches
}
