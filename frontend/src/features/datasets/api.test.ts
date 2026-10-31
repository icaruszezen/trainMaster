import { afterEach, describe, expect, it, vi } from "vitest"

import {
  datasetsQuery,
  deleteDataset,
  imagesQuery,
  parseProjectFilter,
  setDatasetProjects,
} from "@/features/datasets/api"

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
  it("resolves to undefined when everything was removed (204)", async () => {
    const fetchMock = mockFetch(() => new Response(null, { status: 204 }))
    await expect(deleteDataset(7, true)).resolves.toBeUndefined()
    expect(requestOf(fetchMock)).toEqual({
      path: "/api/v1/datasets/7?delete_files=true",
      method: "DELETE",
    })
  })

  it("returns the explanation when the folder could not be removed", async () => {
    const body = {
      files_removed: false,
      message:
        "数据集记录已删除，但文件夹未能完全删除（权限不足）：/data/datasets/批次1",
    }
    mockFetch(() => Response.json(body))
    await expect(deleteDataset(7, true)).resolves.toEqual(body)
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
      sort: "path",
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
