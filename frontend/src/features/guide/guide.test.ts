import { describe, expect, it } from "vitest"

import { findActiveNavItem, navItems } from "@/config/navigation"
import {
  getGuideChapter,
  guideChapters,
  guideGroups,
} from "@/features/guide/chapters"
import {
  DEFAULT_GUIDE_CHAPTER,
  GUIDE_PATH,
  guideChapterForModule,
  guideChapterPath,
  isGuideChapterId,
} from "@/features/guide/paths"
import { searchGuideChapters } from "@/features/guide/search"

describe("guide content integrity", () => {
  it("covers every implemented web module with a unique chapter", () => {
    const ids = guideChapters.map((c) => c.id)
    expect(new Set(ids).size).toBe(ids.length)

    const readyModules = navItems.filter(
      (item) => item.status === "ready" && item.id !== "guide"
    )
    for (const mod of readyModules) {
      const chapterId = guideChapterForModule(mod.id)
      const chapter = getGuideChapter(chapterId)
      expect(chapter, `module ${mod.id}`).toBeDefined()
      expect(chapter?.moduleId).toBe(mod.id)
    }
  })

  it("assigns every chapter to a defined group and valid target route", () => {
    const groupIds = new Set(guideGroups.map((g) => g.id))
    for (const chapter of guideChapters) {
      expect(groupIds.has(chapter.groupId), chapter.id).toBe(true)
      expect(chapter.sections.length, chapter.id).toBeGreaterThan(0)
      expect(
        findActiveNavItem(chapter.targetPath),
        `${chapter.id} -> ${chapter.targetPath}`
      ).toBeDefined()
    }
  })

  it("gives every chapter at least one screenshot with valid callout coordinates", () => {
    for (const chapter of guideChapters) {
      const figures = chapter.sections.flatMap((s) => s.figures ?? [])
      expect(figures.length, `${chapter.id} figures`).toBeGreaterThan(0)
      for (const fig of figures) {
        expect(fig.src, fig.id).toBeTruthy()
        expect(fig.width, fig.id).toBeGreaterThan(0)
        expect(fig.height, fig.id).toBeGreaterThan(0)
        fig.callouts.forEach((c, idx) => {
          expect(c.index, `${fig.id}#${idx}`).toBe(idx + 1)
          expect(c.x).toBeGreaterThanOrEqual(0)
          expect(c.x).toBeLessThanOrEqual(100)
          expect(c.y).toBeGreaterThanOrEqual(0)
          expect(c.y).toBeLessThanOrEqual(100)
          if (c.box) {
            expect(c.box.x).toBeGreaterThanOrEqual(0)
            expect(c.box.y).toBeGreaterThanOrEqual(0)
            expect(c.box.width).toBeGreaterThan(0)
            expect(c.box.height).toBeGreaterThan(0)
            expect(c.box.x + c.box.width).toBeLessThanOrEqual(100.5)
            expect(c.box.y + c.box.height).toBeLessThanOrEqual(100.5)
          }
        })
      }
    }
  })
})

describe("guide paths and contextual help mapping", () => {
  it("formats canonical guide URLs", () => {
    expect(guideChapterPath(DEFAULT_GUIDE_CHAPTER)).toBe(GUIDE_PATH)
    expect(guideChapterPath("datasets")).toBe("/system/guide/datasets")
    expect(isGuideChapterId("evaluations")).toBe(true)
    expect(isGuideChapterId("unknown-chapter")).toBe(false)
  })

  it("falls back to quickstart for planned or unknown modules", () => {
    expect(guideChapterForModule("annotation-tasks")).toBe("quickstart")
    expect(guideChapterForModule("inference")).toBe("quickstart")
    expect(guideChapterForModule(undefined)).toBe("quickstart")
    expect(guideChapterForModule("reviews")).toBe("reviews")
  })
})

describe("searchGuideChapters", () => {
  it("returns all chapters for blank queries", () => {
    expect(searchGuideChapters(guideChapters, "   ")).toHaveLength(
      guideChapters.length
    )
  })

  it("finds chapters by UI terms, shortcuts, and multi-word queries", () => {
    expect(
      searchGuideChapters(guideChapters, "ONNX").map((m) => m.chapter.id)
    ).toContain("models")
    expect(
      searchGuideChapters(guideChapters, "混淆矩阵").map((m) => m.chapter.id)
    ).toContain("evaluations")
    expect(
      searchGuideChapters(guideChapters, "Shift+D").map((m) => m.chapter.id)
    ).toContain("annotation")
    expect(
      searchGuideChapters(guideChapters, "回收站 7 天").map((m) => m.chapter.id)
    ).toContain("recycle-bin")
    expect(searchGuideChapters(guideChapters, "不存在的关键词xyz")).toEqual([])
  })
})
