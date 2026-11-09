import { QueryObserver } from "@tanstack/react-query"
import { afterEach, describe, expect, it, vi } from "vitest"

import {
  createExport,
  createImport,
  datasetsQuery,
  deleteDataset,
  imagesQuery,
  importRenamesQuery,
  lastYoloImportQuery,
  liveDatasetQuery,
  parseProjectFilter,
  setDatasetProjects,
  type Dataset,
  type DatasetProjectRef,
  type ImageRename,
} from "@/features/datasets/api"
import { refreshMembership } from "@/features/projects/api"
import { queryClient } from "@/lib/query-client"

afterEach(() => {
  vi.unstubAllGlobals()
})

function mockFetch(response: () => Response) {
  const fetchMock = vi.fn(async () => response())
  vi.stubGlobal("fetch", fetchMock)
  return fetchMock
}

function requestOf(fetchMock: ReturnType<typeof mockFetch>) {
  const [path, init] = fetchMock.mock.calls[0] as unknown as [
    string,
    RequestInit,
  ]
  return { path, method: init.method }
}

describe("deleteDataset", () => {
  it("moves the dataset to the recycle bin (204)", async () => {
    const fetchMock = mockFetch(() => new Response(null, { status: 204 }))
    await expect(deleteDataset(7, true)).resolves.toBeUndefined()
    expect(requestOf(fetchMock)).toEqual({
      path: "/api/v1/datasets/7?delete_files=true",
      method: "DELETE",
    })
  })

  it("keeps the folder unless asked", async () => {
    const fetchMock = mockFetch(() => new Response(null, { status: 204 }))
    await deleteDataset(7, false)
    expect(requestOf(fetchMock).path).toBe("/api/v1/datasets/7")
  })
})

describe("imagesQuery", () => {
  it("passes the invalid status filter through", async () => {
    const fetchMock = mockFetch(() => Response.json({ items: [], total: 0 }))
    const { queryFn } = imagesQuery(3, {
      status: "invalid",
      label: "",
      q: "",
    })
    await (queryFn as (context: unknown) => Promise<unknown>)({
      pageParam: 0,
      signal: new AbortController().signal,
    })
    expect(requestOf(fetchMock).path).toBe(
      "/api/v1/datasets/3/images?offset=0&limit=60&status=invalid"
    )
  })
})

describe("datasetsQuery", () => {
  async function pathOf(options: { queryFn?: unknown }) {
    const fetchMock = mockFetch(() => Response.json({ items: [], total: 0 }))
    await (options.queryFn as (context: unknown) => Promise<unknown>)({
      signal: new AbortController().signal,
    })
    return requestOf(fetchMock).path
  }

  it("filters by project", async () => {
    expect(await pathOf(datasetsQuery())).toBe("/api/v1/datasets")
    expect(await pathOf(datasetsQuery("", 12))).toBe(
      "/api/v1/datasets?project_id=12"
    )
    expect(await pathOf(datasetsQuery("划痕", "none"))).toBe(
      "/api/v1/datasets?q=%E5%88%92%E7%97%95&project_id=none"
    )
  })

  it("caches each filter separately", () => {
    const keys = [
      datasetsQuery().queryKey,
      datasetsQuery("", 12).queryKey,
      datasetsQuery("", "none").queryKey,
    ].map((key) => JSON.stringify(key))
    expect(new Set(keys).size).toBe(3)
  })
})

describe("setDatasetProjects", () => {
  it("replaces the projects of a dataset", async () => {
    const fetchMock = mockFetch(() => Response.json({ id: 3, projects: [] }))
    await setDatasetProjects(3, [])
    const [path, init] = fetchMock.mock.calls[0] as unknown as [
      string,
      RequestInit,
    ]
    expect(path).toBe("/api/v1/datasets/3/projects")
    expect(init.method).toBe("PUT")
    expect(JSON.parse(String(init.body))).toEqual({ project_ids: [] })
  })
})

describe("parseProjectFilter", () => {
  it("accepts project ids and none", () => {
    expect(parseProjectFilter("12")).toBe(12)
    expect(parseProjectFilter("none")).toBe("none")
  })

  it("ignores anything else", () => {
    for (const value of [null, "", "0", "-1", "012", "1.5", "abc", "None"]) {
      expect(parseProjectFilter(value), String(value)).toBeNull()
    }
    expect(parseProjectFilter("99999999999999999")).toBeNull()
  })
})

describe("createImport", () => {
  it("sends the annotation format and class order", async () => {
    const fetchMock = mockFetch(() => Response.json({}, { status: 201 }))
    await createImport(4, {
      source: "folder",
      file_name: "batch",
      file_size: 100,
      file_count: 3,
      annotation_format: "yolo",
      classes: ["scratch", "dent"],
    })
    const [path, init] = fetchMock.mock.calls[0] as unknown as [
      string,
      RequestInit,
    ]
    expect(path).toBe("/api/v1/datasets/4/imports")
    expect(JSON.parse(init.body as string)).toMatchObject({
      annotation_format: "yolo",
      classes: ["scratch", "dent"],
    })
  })
})

describe("createExport", () => {
  it("posts the format and returns the download link", async () => {
    const link = {
      url: "/api/v1/datasets/4/exports/abc",
      file_name: "划痕-A-yolo.zip",
      expires_at: "2026-10-31T03:00:00Z",
    }
    const fetchMock = mockFetch(() => Response.json(link, { status: 201 }))
    await expect(
      createExport(4, { format: "yolo", project_id: 3 })
    ).resolves.toEqual(link)
    const [path, init] = fetchMock.mock.calls[0] as unknown as [
      string,
      RequestInit,
    ]
    expect(path).toBe("/api/v1/datasets/4/exports")
    expect(init.method).toBe("POST")
    expect(JSON.parse(init.body as string)).toEqual({
      format: "yolo",
      project_id: 3,
    })
  })
})

describe("lastYoloImportQuery", () => {
  const run = (id: number) =>
    (lastYoloImportQuery(id).queryFn as (context: unknown) => Promise<unknown>)(
      { signal: new AbortController().signal }
    )

  it("asks for the newest successful YOLO import", async () => {
    const job = { id: 7, annotation_format: "yolo", classes: ["划痕", "凹坑"] }
    const fetchMock = mockFetch(() => Response.json({ items: [job], total: 3 }))
    await expect(run(5)).resolves.toEqual(job)
    expect(requestOf(fetchMock).path).toBe(
      "/api/v1/datasets/5/imports?annotation_format=yolo&status=succeeded&limit=1"
    )
  })

  it("is null when there is none", async () => {
    mockFetch(() => Response.json({ items: [], total: 0 }))
    await expect(run(5)).resolves.toBeNull()
  })
})

describe("importRenamesQuery", () => {
  const options = importRenamesQuery(5, 9)
  const run = (pageParam: number) =>
    (options.queryFn as (context: unknown) => Promise<unknown>)({
      signal: new AbortController().signal,
      pageParam,
    })
  const page = (count: number, total: number) => ({
    items: Array.from({ length: count }, () => ({}) as ImageRename),
    total,
  })

  it("loads a page of renames of one import", async () => {
    const fetchMock = mockFetch(() => Response.json(page(0, 0)))
    await run(100)
    expect(requestOf(fetchMock).path).toBe(
      "/api/v1/datasets/5/imports/9/renames?offset=100&limit=100"
    )
  })

  it("continues until every rename is loaded", () => {
    const next = options.getNextPageParam
    expect(next(page(100, 250), [page(100, 250)], 0, [0])).toBe(100)
    expect(
      next(
        page(50, 250),
        [page(100, 250), page(100, 250), page(50, 250)],
        200,
        [0, 100, 200]
      )
    ).toBeUndefined()
    expect(
      next(page(0, 250), [page(100, 250), page(0, 250)], 100, [0, 100])
    ).toBeUndefined()
  })
})

describe("liveDatasetQuery", () => {
  // refreshMembership 刷新的是全局的 queryClient。
  afterEach(() => {
    queryClient.clear()
  })

  const dataset = (projects: DatasetProjectRef[]) =>
    ({ id: 7, name: "批次1", projects }) as unknown as Dataset
  const inP = [{ id: 3, name: "P" }]

  function observe(opened: Dataset) {
    const observer = new QueryObserver(queryClient, liveDatasetQuery(opened))
    return { observer, unsubscribe: observer.subscribe(() => undefined) }
  }

  it("starts from the dataset it was opened with and reads the latest one", async () => {
    // 列表是项目 P 删除之前读取的。
    const opened = dataset(inP)
    const fetchMock = mockFetch(() => Response.json(dataset([])))
    const { observer, unsubscribe } = observe(opened)
    try {
      expect(observer.getCurrentResult().data).toBe(opened)
      await vi.waitFor(() =>
        expect(observer.getCurrentResult().data?.projects).toEqual([])
      )
      expect(requestOf(fetchMock).path).toBe("/api/v1/datasets/7")
    } finally {
      unsubscribe()
    }
  })

  it("follows refreshMembership while the dialog is open", async () => {
    let projects = inP
    const fetchMock = mockFetch(() => Response.json(dataset(projects)))
    const { observer, unsubscribe } = observe(dataset(projects))
    try {
      await vi.waitFor(() => {
        expect(fetchMock).toHaveBeenCalledTimes(1)
        expect(observer.getCurrentResult().isFetching).toBe(false)
      })
      expect(observer.getCurrentResult().data?.projects).toEqual(inP)

      // 下载时服务端回答项目已不存在：刷新后对话框改为手动指定类别顺序。
      projects = []
      refreshMembership()
      await vi.waitFor(() =>
        expect(observer.getCurrentResult().data?.projects).toEqual([])
      )
      expect(fetchMock).toHaveBeenCalledTimes(2)
    } finally {
      unsubscribe()
    }
  })
})
