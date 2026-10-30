import { InfiniteQueryObserver, QueryClient } from "@tanstack/react-query"
import { afterEach, describe, expect, it, vi } from "vitest"

import {
  imagesQuery,
  type DatasetImage,
  type DatasetImageDetail,
} from "@/features/datasets/api"
import {
  annotationPanel,
  flattenImagePages,
  imageStatusOptions,
  loadMoreImages,
  unannotatedCount,
} from "@/features/datasets/images"

function image(
  id: number,
  overrides: Partial<DatasetImage> = {}
): DatasetImage {
  return {
    id,
    dataset_id: 1,
    path: `NG/${id}.jpg`,
    file_name: `${id}.jpg`,
    size_bytes: 1024,
    width: 640,
    height: 480,
    mod_time: "2026-10-30T08:00:00Z",
    version: "v1",
    annotation_state: "none",
    annotation_path: null,
    annotation: null,
    ...overrides,
  }
}

function detail(
  base: DatasetImage,
  overrides: Partial<DatasetImageDetail> = {}
): DatasetImageDetail {
  return { ...base, objects: [], annotation_error: "", ...overrides }
}

const annotated = (id: number) =>
  image(id, {
    annotation_state: "annotated",
    annotation_path: `NG/${id}.xml`,
    annotation: {
      path: `NG/${id}.xml`,
      object_count: 2,
      labels: [{ name: "scratch", count: 2 }],
    },
  })

const counts = {
  image_count: 20,
  annotated_count: 10,
  empty_count: 3,
  invalid_count: 2,
}

describe("unannotatedCount", () => {
  it("counts only images without an xml", () => {
    expect(unannotatedCount(counts)).toBe(5)
  })

  it("never goes negative while counters are being recomputed", () => {
    expect(unannotatedCount({ ...counts, image_count: 12 })).toBe(0)
  })
})

describe("imageStatusOptions", () => {
  it("offers an invalid filter with its count when some xml are invalid", () => {
    expect(imageStatusOptions(counts, "all")).toEqual([
      { value: "all", label: "全部", count: 20 },
      { value: "annotated", label: "已标注", count: 10 },
      { value: "empty", label: "空标注", count: 3 },
      { value: "unannotated", label: "未标注", count: 5 },
      { value: "invalid", label: "标注无效", count: 2 },
    ])
  })

  it("hides the invalid filter when there is nothing to show", () => {
    const options = imageStatusOptions({ ...counts, invalid_count: 0 }, "all")
    expect(options.map((option) => option.value)).toEqual([
      "all",
      "annotated",
      "empty",
      "unannotated",
    ])
  })

  it("keeps the invalid filter while it is selected", () => {
    const options = imageStatusOptions(
      { ...counts, invalid_count: 0 },
      "invalid"
    )
    expect(options.at(-1)).toEqual({
      value: "invalid",
      label: "标注无效",
      count: 0,
    })
  })
})

describe("flattenImagePages", () => {
  it("drops images repeated by a page that shifted during indexing", () => {
    const images = flattenImagePages([
      { items: [image(1), image(2), image(3)], total: 6 },
      { items: [image(3), image(4), image(2), image(5)], total: 6 },
    ])
    expect(images.map((item) => item.id)).toEqual([1, 2, 3, 4, 5])
  })

  it("handles a query without data", () => {
    expect(flattenImagePages(undefined)).toEqual([])
  })
})

describe("loadMoreImages", () => {
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it("returns the number of loaded images", async () => {
    const count = await loadMoreImages(async () => ({
      data: {
        pages: [
          { items: [image(1), image(2)], total: 3 },
          { items: [image(2), image(3)], total: 3 },
        ],
      },
      isFetchNextPageError: false,
      error: null,
    }))
    expect(count).toBe(3)
  })

  it("rejects when React Query reports a failed next page", async () => {
    // 用真实的 InfiniteQueryObserver：fetchNextPage() 失败时也会 resolve。
    let requests = 0
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => {
        requests += 1
        return requests === 1
          ? Response.json({ items: [image(1), image(2)], total: 5 })
          : new Response("Bad Gateway", { status: 502 })
      })
    )
    const client = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    })
    const observer = new InfiniteQueryObserver(
      client,
      imagesQuery(1, { status: "all", label: "", q: "", sort: "path" })
    )
    const unsubscribe = observer.subscribe(() => {})
    try {
      await vi.waitFor(() =>
        expect(observer.getCurrentResult().isSuccess).toBe(true)
      )
      const raw = await observer.fetchNextPage()
      expect(raw.isFetchNextPageError).toBe(true)

      await expect(
        loadMoreImages(() => observer.fetchNextPage())
      ).rejects.toMatchObject({ name: "ApiError", status: 502 })
    } finally {
      unsubscribe()
      client.clear()
    }
  })
})

describe("annotationPanel", () => {
  it("says there is no annotation file only when no xml is paired", () => {
    expect(annotationPanel(image(1), undefined)).toEqual({
      path: null,
      error: "",
      noFile: true,
      empty: false,
    })
  })

  it("names the broken xml instead of claiming there is none", () => {
    const invalid = image(1, {
      annotation_state: "invalid",
      annotation_path: "NG/1.xml",
    })
    // 详情还没加载到时也显示路径。
    expect(annotationPanel(invalid, undefined)).toMatchObject({
      path: "NG/1.xml",
      noFile: false,
    })
    expect(
      annotationPanel(
        invalid,
        detail(invalid, {
          annotation_error: "标注文件无法解析（XML 格式错误）：unexpected EOF",
        })
      )
    ).toEqual({
      path: "NG/1.xml",
      error: "标注文件无法解析（XML 格式错误）：unexpected EOF",
      noFile: false,
      empty: false,
    })
  })

  it("prefers the fresh detail over the indexed list item", () => {
    // 扫描后 xml 被改坏：列表仍是 2 个目标，详情是此刻的解析结果。
    const listed = annotated(1)
    const broken = detail(listed, {
      annotation_state: "invalid",
      annotation: null,
      annotation_error: "标注文件无法解析（XML 格式错误）：unexpected EOF",
    })
    expect(annotationPanel(listed, broken)).toMatchObject({
      path: "NG/1.xml",
      noFile: false,
      empty: false,
    })

    // 反过来：扫描时无法解析的 xml 已经修好。
    const wasInvalid = image(2, {
      annotation_state: "invalid",
      annotation_path: "NG/2.xml",
    })
    expect(annotationPanel(wasInvalid, detail(annotated(2)))).toEqual({
      path: "NG/2.xml",
      error: "",
      noFile: false,
      empty: false,
    })
  })

  it("marks negative samples as empty", () => {
    const empty = image(1, {
      annotation_state: "empty",
      annotation_path: "NG/1.xml",
      annotation: { path: "NG/1.xml", object_count: 0, labels: [] },
    })
    expect(annotationPanel(empty, detail(empty))).toMatchObject({
      empty: true,
      noFile: false,
    })
  })

  it("explains an indexed xml that no longer exists", () => {
    const listed = annotated(1)
    const gone = detail(listed, {
      annotation_state: "none",
      annotation: null,
      annotation_error: "标注文件已不存在，请重新扫描",
    })
    expect(annotationPanel(listed, gone)).toEqual({
      path: "NG/1.xml",
      error: "标注文件已不存在，请重新扫描",
      noFile: false,
      empty: false,
    })
    expect(
      annotationPanel(listed, { ...gone, annotation_path: null })
    ).toMatchObject({ path: null, noFile: false })
  })
})
