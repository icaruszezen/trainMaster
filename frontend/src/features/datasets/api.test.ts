import { afterEach, describe, expect, it, vi } from "vitest"

import { deleteDataset, imagesQuery } from "@/features/datasets/api"

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
