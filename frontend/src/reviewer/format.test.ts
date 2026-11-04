import { describe, expect, it } from "vitest"

import type { ReviewCounts } from "@/features/reviews/api"
import type { ExportResult, Session } from "@/reviewer/api"
import {
  baseName,
  dirName,
  exportSummary,
  isOpenable,
  openingProgress,
  openOutcome,
  pageTitle,
  packageKey,
  progressSummary,
  typedPath,
} from "@/reviewer/format"

const counts = (patch: Partial<ReviewCounts> = {}): ReviewCounts => ({
  total: 40,
  inferred: 40,
  inference_failed: 0,
  pending: 28,
  approved: 9,
  needs_sample: 3,
  edited: 2,
  merged: 0,
  ...patch,
})

describe("openingProgress", () => {
  it("shows bytes while extracting and images while loading", () => {
    expect(
      openingProgress({
        path: "a",
        stage: "extracting",
        done: 512,
        total: 2048,
      })
    ).toEqual({ percent: 25, detail: "512 B / 2 KB" })
    expect(
      openingProgress({ path: "a", stage: "loading", done: 3, total: 4 })
    ).toEqual({ percent: 75, detail: "3 / 4 张" })
  })

  it("has no percentage while the total is unknown", () => {
    expect(
      openingProgress({ path: "a", stage: "checking", done: 0, total: 0 })
    ).toEqual({ percent: null, detail: "" })
  })
})

describe("paths", () => {
  it("splits Windows and POSIX paths", () => {
    expect(baseName("C:\\Users\\张三\\Downloads\\审图-1-外观.zip")).toBe(
      "审图-1-外观.zip"
    )
    expect(baseName("/home/u/审图-1/")).toBe("审图-1")
    expect(baseName("C:\\")).toBe("C:")
    expect(dirName("C:\\Users\\张三\\审图-1")).toBe("C:\\Users\\张三")
    expect(dirName("C:\\审图-1")).toBe("C:\\")
    expect(dirName("/审图-1")).toBe("/")
    expect(dirName("审图-1")).toBe("")
  })

  it("opens packages and zip files only", () => {
    expect(isOpenable({ kind: "package" })).toBe(true)
    expect(isOpenable({ kind: "zip" })).toBe(true)
    expect(isOpenable({ kind: "folder" })).toBe(false)
    expect(isOpenable({ kind: "drive" })).toBe(false)
  })
})

describe("summaries", () => {
  it("summarizes the review progress", () => {
    expect(progressSummary(counts())).toBe("已审核 12 / 40 张（需加样 3 张）")
    expect(progressSummary(counts({ needs_sample: 0 }))).toBe(
      "已审核 9 / 40 张"
    )
  })

  it("summarizes exported results", () => {
    const result = {
      status_changes: 3,
      annotation_changes: 1,
      unchanged: 36,
    } as ExportResult
    expect(exportSummary(result)).toBe(
      "3 张图片改了审核状态，1 张图片改了标注；36 张没有修改。"
    )
    expect(
      exportSummary({ ...result, status_changes: 0, annotation_changes: 0 })
    ).toContain("没有任何修改")
  })

  it("titles the page after the review", () => {
    expect(pageTitle(null)).toBe("trainMaster 本地审图")
    expect(
      pageTitle({ review: { id: 1, name: "外观", description: "" } })
    ).toBe("外观 - trainMaster 本地审图")
  })
})

describe("openOutcome", () => {
  const pkg = {} as NonNullable<Session["package"]>
  it("waits while opening", () => {
    expect(
      openOutcome(
        {
          opening: { path: "a", stage: "loading", done: 0, total: 1 },
          open_error: null,
          package: null,
        },
        "a"
      )
    ).toEqual({ done: false, failure: null })
  })

  it("is done once the package is open", () => {
    expect(
      openOutcome({ opening: null, open_error: null, package: pkg }, "a")
    ).toEqual({ done: true, failure: null })
    expect(
      openOutcome({ opening: null, open_error: null, package: pkg }, null)
    ).toEqual({ done: false, failure: null })
  })

  it("reports the failure of this opening only", () => {
    const failure = { path: "a", message: "坏了" }
    expect(
      openOutcome({ opening: null, open_error: failure, package: pkg }, "a")
    ).toEqual({ done: false, failure })
    expect(
      openOutcome({ opening: null, open_error: failure, package: pkg }, "b")
    ).toEqual({ done: true, failure: null })
  })
})

describe("typedPath", () => {
  it("accepts a path copied with Explorer's “复制为路径”", () => {
    expect(typedPath('"C:\\Users\\张三\\Downloads\\审图-12-外观.zip"')).toBe(
      "C:\\Users\\张三\\Downloads\\审图-12-外观.zip"
    )
    expect(typedPath('  "D:\\审图包\\审图-12"\n')).toBe("D:\\审图包\\审图-12")
    expect(typedPath('" /home/a/审图-12.zip "')).toBe("/home/a/审图-12.zip")
  })

  it("keeps an unquoted path and drops surrounding blanks", () => {
    expect(typedPath("  C:\\a\\b.zip\t")).toBe("C:\\a\\b.zip")
    expect(typedPath("/home/a/b.zip")).toBe("/home/a/b.zip")
  })

  it("handles blanks and quotes without a path", () => {
    expect(typedPath("   ")).toBe("")
    expect(typedPath('""')).toBe("")
    expect(typedPath('"')).toBe('"')
  })

  it("only strips a pair that wraps the whole path", () => {
    expect(typedPath('"C:\\a.zip')).toBe('"C:\\a.zip')
    expect(typedPath('C:\\a.zip"')).toBe('C:\\a.zip"')
  })
})

describe("packageKey", () => {
  it("tells a re-downloaded package or another folder apart", () => {
    const a = { package_id: "p1", path: "C:\\审图-12" }
    expect(packageKey(a)).toBe(packageKey({ ...a }))
    expect(packageKey(a)).not.toBe(packageKey({ ...a, package_id: "p2" }))
    expect(packageKey(a)).not.toBe(packageKey({ ...a, path: "D:\\审图-12" }))
    // 字段之间有分隔，拼接后不会相同。
    expect(packageKey({ package_id: "p1C", path: ":\\x" })).not.toBe(
      packageKey({ package_id: "p1", path: "C:\\x" })
    )
  })
})
