import { afterEach, describe, expect, it, vi } from "vitest"

import { dataVersionKeys } from "@/features/data-versions/api"
import { datasetKeys } from "@/features/datasets/api"
import { modelKeys } from "@/features/models/api"
import { projectKeys } from "@/features/projects/api"
import {
  recycleBinKeys,
  recycleBinQuery,
  refreshAfterDatasetMove,
  refreshAfterDataVersionMove,
  refreshAfterModelMove,
  refreshAfterVersionMove,
  restoreDataset,
  restoreDataVersion,
  restoreModel,
  restoreVersion,
} from "@/features/recycle-bin/api"
import { reviewKeys } from "@/features/reviews/api"
import { queryClient } from "@/lib/query-client"

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
  queryClient.clear()
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
  return {
    path,
    method: init.method ?? "GET",
    body: init.body === undefined ? undefined : JSON.parse(String(init.body)),
  }
}

describe("recycle bin requests", () => {
  it("lists the recycle bin", async () => {
    const bin = {
      retention_days: 7,
      datasets: [],
      versions: [],
      data_versions: [],
      models: [],
    }
    const fetchMock = mockFetch(() => Response.json(bin))
    const options = recycleBinQuery()
    expect(options.queryKey).toEqual(recycleBinKeys.all)
    const queryFn = options.queryFn as (context: unknown) => Promise<unknown>
    await expect(
      queryFn({ signal: new AbortController().signal })
    ).resolves.toEqual(bin)
    expect(requestOf(fetchMock)).toMatchObject({
      path: "/api/v1/recycle-bin",
      method: "GET",
    })
  })

  it("restores a dataset, under another name when asked", async () => {
    const fetchMock = mockFetch(() => Response.json({ id: 3 }))
    await restoreDataset(3)
    expect(requestOf(fetchMock)).toEqual({
      path: "/api/v1/recycle-bin/datasets/3/restore",
      method: "POST",
      body: undefined,
    })

    fetchMock.mockClear()
    await restoreDataset(3, "划痕（恢复）")
    expect(requestOf(fetchMock).body).toEqual({ name: "划痕（恢复）" })
  })

  it("restores a version", async () => {
    const fetchMock = mockFetch(() => Response.json({ id: 8 }))
    await restoreVersion(8)
    expect(requestOf(fetchMock)).toMatchObject({
      path: "/api/v1/recycle-bin/versions/8/restore",
      method: "POST",
    })
  })

  it("restores a model, renaming its project when asked", async () => {
    const fetchMock = mockFetch(() => Response.json({ id: 5 }))
    await restoreModel(5)
    expect(requestOf(fetchMock)).toEqual({
      path: "/api/v1/recycle-bin/models/5/restore",
      method: "POST",
      body: undefined,
    })

    fetchMock.mockClear()
    await restoreModel(5, "A 线（恢复）")
    expect(requestOf(fetchMock).body).toEqual({ project_name: "A 线（恢复）" })
  })

  it("restores a data version, renaming its project when asked", async () => {
    const fetchMock = mockFetch(() => Response.json({ id: 12 }))
    await restoreDataVersion(12)
    expect(requestOf(fetchMock)).toEqual({
      path: "/api/v1/recycle-bin/data-versions/12/restore",
      method: "POST",
      body: undefined,
    })

    fetchMock.mockClear()
    await restoreDataVersion(12, "A 线（恢复）")
    expect(requestOf(fetchMock).body).toEqual({ project_name: "A 线（恢复）" })
  })

  it("reports a taken name with its code", async () => {
    mockFetch(() =>
      Response.json(
        { error: { code: "name_taken", message: "已有名为“a”的数据集" } },
        { status: 409 }
      )
    )
    await expect(restoreDataset(3)).rejects.toMatchObject({
      status: 409,
      code: "name_taken",
      message: "已有名为“a”的数据集",
    })
  })
})

describe("refreshing", () => {
  it("refreshes what a dataset moving in or out of the bin changes", () => {
    const invalidate = vi.spyOn(queryClient, "invalidateQueries")
    refreshAfterDatasetMove()
    for (const queryKey of [
      datasetKeys.lists(),
      datasetKeys.directories(),
      projectKeys.all,
      reviewKeys.all,
      recycleBinKeys.all,
    ]) {
      expect(invalidate).toHaveBeenCalledWith(
        expect.objectContaining({ queryKey })
      )
    }
  })

  it("refreshes a dataset's versions", () => {
    const invalidate = vi.spyOn(queryClient, "invalidateQueries")
    refreshAfterVersionMove(4)
    expect(invalidate).toHaveBeenCalledWith({
      queryKey: datasetKeys.versions(4),
    })
    expect(invalidate).toHaveBeenCalledWith({ queryKey: recycleBinKeys.all })
  })

  it("refreshes models, projects and reviews after a model moves", () => {
    const invalidate = vi.spyOn(queryClient, "invalidateQueries")
    refreshAfterModelMove()
    for (const queryKey of [
      modelKeys.all,
      projectKeys.all,
      reviewKeys.all,
      recycleBinKeys.all,
    ]) {
      expect(invalidate).toHaveBeenCalledWith({ queryKey })
    }
  })

  it("refreshes data versions, projects and the bin after a data version moves", () => {
    const invalidate = vi.spyOn(queryClient, "invalidateQueries")
    refreshAfterDataVersionMove()
    for (const queryKey of [
      dataVersionKeys.all,
      projectKeys.all,
      datasetKeys.lists(),
      recycleBinKeys.all,
    ]) {
      expect(invalidate).toHaveBeenCalledWith(
        expect.objectContaining({ queryKey })
      )
    }
  })
})
