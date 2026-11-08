import { describe, expect, it } from "vitest"

import {
  EMPTY_IMAGE_FILTERS,
  type DataVersionImage,
} from "@/features/data-versions/api"
import {
  flattenImagePages,
  imageFilterParams,
  isFiltered,
  largeThumbnailUrl,
  loadMoreImages,
  parseImageFilters,
} from "@/features/data-versions/images"

const image = (id: number) => ({ id }) as DataVersionImage

describe("parseImageFilters", () => {
  it("reads the filters of the address", () => {
    expect(
      parseImageFilters(
        new URLSearchParams(
          "tab=images&split=val&label=scratch&dataset=3&augmented=true&q=NG"
        )
      )
    ).toEqual({
      split: "val",
      label: "scratch",
      dataset_id: 3,
      augmented: true,
      q: "NG",
    })
    expect(parseImageFilters(new URLSearchParams("augmented=false"))).toEqual({
      ...EMPTY_IMAGE_FILTERS,
      augmented: false,
    })
  })

  it("ignores invalid values", () => {
    expect(
      parseImageFilters(
        new URLSearchParams("split=all&dataset=abc&augmented=yes")
      )
    ).toEqual(EMPTY_IMAGE_FILTERS)
    expect(parseImageFilters(new URLSearchParams("dataset=0"))).toEqual(
      EMPTY_IMAGE_FILTERS
    )
  })
})

describe("imageFilterParams", () => {
  it("writes only the changed filters, defaults as empty", () => {
    expect(imageFilterParams({ split: "train" })).toEqual({ split: "train" })
    expect(imageFilterParams({ dataset_id: 7, augmented: false })).toEqual({
      dataset: "7",
      augmented: "false",
    })
    expect(imageFilterParams(EMPTY_IMAGE_FILTERS)).toEqual({
      split: "",
      label: "",
      dataset: "",
      augmented: "",
      q: "",
    })
  })

  it("round-trips through the address", () => {
    const filters = {
      split: "test" as const,
      label: "dent",
      dataset_id: 2,
      augmented: null,
      q: "cam1",
    }
    const params = new URLSearchParams()
    for (const [key, value] of Object.entries(imageFilterParams(filters))) {
      if (value) {
        params.set(key, value)
      }
    }
    expect(parseImageFilters(params)).toEqual(filters)
    expect(isFiltered(filters)).toBe(true)
    expect(isFiltered(EMPTY_IMAGE_FILTERS)).toBe(false)
  })
})

describe("pages", () => {
  it("merges the pages without duplicates", () => {
    expect(
      flattenImagePages([
        { items: [image(1), image(2)], total: 4 },
        { items: [image(2), image(3)], total: 4 },
      ]).map((i) => i.id)
    ).toEqual([1, 2, 3])
    expect(flattenImagePages(undefined)).toEqual([])
  })

  it("throws when the next page fails", async () => {
    await expect(
      loadMoreImages(() =>
        Promise.resolve({
          data: { pages: [{ items: [image(1), image(2)], total: 2 }] },
          isFetchNextPageError: false,
          error: null,
        })
      )
    ).resolves.toBe(2)
    await expect(
      loadMoreImages(() =>
        Promise.resolve({
          isFetchNextPageError: true,
          error: new Error("网络错误"),
        })
      )
    ).rejects.toThrow("网络错误")
  })

  it("builds the large thumbnail address", () => {
    expect(largeThumbnailUrl(12, 77)).toBe(
      "/api/v1/data-versions/12/images/77/thumbnail?size=lg"
    )
  })
})
