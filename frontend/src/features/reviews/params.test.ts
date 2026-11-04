import { describe, expect, it } from "vitest"

import { ALL_REVIEW_IMAGES } from "@/features/reviews/api"
import {
  hasReviewFilters,
  parseReviewFilters,
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
