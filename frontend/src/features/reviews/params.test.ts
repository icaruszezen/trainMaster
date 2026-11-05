import { describe, expect, it } from "vitest"

import {
  ALL_REVIEW_IMAGES,
  type ReviewImageFilters,
  type ReviewImageStatus,
} from "@/features/reviews/api"
import {
  hasReviewFilters,
  parseReviewFilters,
  pickStartImage,
  reviewDetailPath,
  reviewWorkspacePath,
  sameReviewFilters,
  updateReviewParams,
} from "@/features/reviews/params"

const parse = (search: string) =>
  parseReviewFilters(new URLSearchParams(search))

describe("parseReviewFilters", () => {
  it("falls back to no filter", () => {
    expect(parse("")).toEqual(ALL_REVIEW_IMAGES)
    expect(parse("image=3&other=1")).toEqual(ALL_REVIEW_IMAGES)
  })

  it("reads every filter", () => {
    expect(
      parse(
        "status=needs_sample&dataset_id=7&label=划痕&q=NG&edited=edited&boxes=without&inference=failed"
      )
    ).toEqual({
      status: "needs_sample",
      datasetId: 7,
      label: "划痕",
      q: "NG",
      edited: "edited",
      boxes: "without",
      inference: "failed",
    })
    expect(parse("status=reviewed&edited=unedited&boxes=with")).toMatchObject({
      status: "reviewed",
      edited: "unedited",
      boxes: "with",
    })
  })

  it("ignores invalid values", () => {
    expect(
      parse(
        "status=done&dataset_id=0&edited=yes&boxes=1&inference=ok&dataset_id=x"
      )
    ).toEqual(ALL_REVIEW_IMAGES)
    expect(parse("dataset_id=-3").datasetId).toBeNull()
    expect(parse("dataset_id=99999999999999999").datasetId).toBeNull()
  })
})

describe("updateReviewParams", () => {
  it("changes the image and keeps the filters and other parameters", () => {
    const params = new URLSearchParams("image=3&status=pending&tab=x")
    expect(updateReviewParams(params, { image: 4 }).toString()).toBe(
      "image=4&status=pending&tab=x"
    )
    expect(params.toString()).toBe("image=3&status=pending&tab=x")
  })

  it("writes the filters, dropping the defaults", () => {
    const params = new URLSearchParams("image=3&status=pending&q=a&other=1")
    const next = updateReviewParams(params, {
      status: "all",
      datasetId: 5,
      edited: "edited",
    })
    expect(next.get("image")).toBe("3")
    expect(next.get("other")).toBe("1")
    expect(next.has("status")).toBe(false)
    expect(parseReviewFilters(next)).toEqual({
      ...ALL_REVIEW_IMAGES,
      datasetId: 5,
      q: "a",
      edited: "edited",
    })
    expect(
      updateReviewParams(next, {
        datasetId: null,
        q: "",
        edited: "all",
      }).toString()
    ).toBe("image=3&other=1")
  })

  it("drops invalid filter values it rewrites", () => {
    const next = updateReviewParams(new URLSearchParams("status=bogus"), {
      label: "dent",
    })
    expect(next.toString()).toBe("label=dent")
  })
})

describe("hasReviewFilters", () => {
  it("is true for any filter other than the defaults", () => {
    expect(hasReviewFilters(ALL_REVIEW_IMAGES)).toBe(false)
    for (const change of [
      { status: "pending" },
      { datasetId: 1 },
      { label: "a" },
      { q: "a" },
      { edited: "unedited" },
      { boxes: "with" },
      { inference: "failed" },
    ] as const) {
      expect(hasReviewFilters({ ...ALL_REVIEW_IMAGES, ...change })).toBe(true)
    }
  })
})

describe("sameReviewFilters", () => {
  it("compares the filters the address bar would carry", () => {
    expect(sameReviewFilters(ALL_REVIEW_IMAGES, { ...ALL_REVIEW_IMAGES })).toBe(
      true
    )
    expect(
      sameReviewFilters(
        parse("status=pending&q=NG&image=3"),
        parse("q=NG&status=pending&image=9")
      )
    ).toBe(true)
  })

  it("notices a filter changed while an image was being marked", () => {
    const pending = parse("status=pending&dataset_id=2")
    for (const search of [
      "status=needs_sample&dataset_id=2",
      "status=pending",
      "status=pending&dataset_id=2&q=NG",
      "status=pending&dataset_id=2&label=dent",
    ]) {
      expect(sameReviewFilters(pending, parse(search)), search).toBe(false)
    }
  })
})

describe("paths", () => {
  it("builds the detail and workspace paths", () => {
    expect(reviewDetailPath(5)).toBe("/datasets/reviews/5")
    expect(reviewWorkspacePath(5)).toBe("/datasets/reviews/5/workspace")
    expect(reviewWorkspacePath(5, { status: "pending" })).toBe(
      "/datasets/reviews/5/workspace?status=pending"
    )
    expect(
      reviewWorkspacePath(5, { image: 9, status: "pending", datasetId: 2 })
    ).toBe("/datasets/reviews/5/workspace?image=9&status=pending&dataset_id=2")
    expect(reviewWorkspacePath(5, { status: "all", q: "" })).toBe(
      "/datasets/reviews/5/workspace"
    )
  })

  it("round-trips the filters through the workspace path", () => {
    const filters = {
      ...ALL_REVIEW_IMAGES,
      status: "reviewed",
      label: "a&b",
      q: "夜班/NG",
    } as const
    const search = reviewWorkspacePath(1, filters).split("?")[1]
    expect(parse(search)).toEqual(filters)
  })
})

describe("pickStartImage", () => {
  type Row = { id: number; status: ReviewImageStatus; datasetId: number }
  /** 审图任务中的图片（按审图任务中的顺序），只按审核状态与数据集筛选。 */
  const images = (rows: Row[]) => {
    const asked: ReviewImageFilters[] = []
    const first = async (filters: ReviewImageFilters) => {
      asked.push(filters)
      const row = rows.find(
        (r) =>
          (filters.status === "all" ||
            (filters.status === "reviewed"
              ? r.status !== "pending"
              : r.status === filters.status)) &&
          (filters.datasetId === null || r.datasetId === filters.datasetId)
      )
      return row ? { id: row.id } : null
    }
    return { first, asked }
  }
  const rows: Row[] = [
    { id: 1, status: "approved", datasetId: 1 },
    { id: 2, status: "needs_sample", datasetId: 1 },
    { id: 3, status: "pending", datasetId: 1 },
    { id: 4, status: "approved", datasetId: 2 },
    { id: 5, status: "pending", datasetId: 2 },
  ]
  const all = ALL_REVIEW_IMAGES

  it("opens the first pending image when every status is shown", async () => {
    const { first, asked } = images(rows)
    expect(await pickStartImage(all, first)).toEqual({ id: 3, notice: null })
    expect(asked).toEqual([{ ...all, status: "pending" }])
    expect(
      await pickStartImage({ ...all, datasetId: 2 }, images(rows).first)
    ).toEqual({ id: 5, notice: null })
  })

  it("opens the first image when nothing is pending", async () => {
    const reviewed = rows.map((r) => ({ ...r, status: "approved" as const }))
    expect(await pickStartImage(all, images(reviewed).first)).toEqual({
      id: 1,
      notice: "没有待审核的图片了，已打开第一张",
    })
    // 只看一个数据集：这个数据集中没有待审核的
    expect(
      await pickStartImage(
        { ...all, datasetId: 2 },
        images(rows.slice(0, 4)).first
      )
    ).toEqual({ id: 4, notice: "筛选出的图片中没有待审核的，已打开第一张" })
  })

  it("keeps the status filter the address asks for", async () => {
    expect(
      await pickStartImage({ ...all, status: "reviewed" }, images(rows).first)
    ).toEqual({ id: 1, notice: null })
    const pending = images(rows)
    expect(
      await pickStartImage({ ...all, status: "pending" }, pending.first)
    ).toEqual({ id: 3, notice: null })
    expect(pending.asked).toEqual([{ ...all, status: "pending" }])
  })

  it("falls back to the first image when nothing matches", async () => {
    const reviewed = rows.map((r) => ({ ...r, status: "approved" as const }))
    expect(
      await pickStartImage(
        { ...all, status: "pending" },
        images(reviewed).first
      )
    ).toEqual({ id: 1, notice: "没有待审核的图片了，已打开第一张" })
    expect(
      await pickStartImage({ ...all, datasetId: 9 }, images(rows).first)
    ).toEqual({ id: 1, notice: "没有符合筛选条件的图片，已打开第一张" })
  })

  it("finds nothing in an empty review", async () => {
    const empty = images([])
    expect(await pickStartImage(all, empty.first)).toEqual({
      id: null,
      notice: null,
    })
    expect(empty.asked).toEqual([{ ...all, status: "pending" }, all])
    expect(
      await pickStartImage({ ...all, status: "pending" }, images([]).first)
    ).toEqual({ id: null, notice: null })
  })
})
