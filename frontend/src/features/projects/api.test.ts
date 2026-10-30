import { afterEach, describe, expect, it, vi } from "vitest"

import { datasetKeys } from "@/features/datasets/api"
import {
  addProjectDatasets,
  cacheNewProject,
  createProject,
  deleteProject,
  projectDatasetsQuery,
  projectKeys,
  projectsQuery,
  refreshMembership,
  removeProjectDataset,
  type Project,
} from "@/features/projects/api"
import type { ListResponse } from "@/lib/api"
import { queryClient } from "@/lib/query-client"

afterEach(() => {
  vi.unstubAllGlobals()
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
    method: init.method,
    body: init.body === undefined ? undefined : JSON.parse(String(init.body)),
  }
}

async function runQuery(options: { queryFn?: unknown }) {
  await (options.queryFn as (context: unknown) => Promise<unknown>)({
    signal: new AbortController().signal,
  })
}

describe("project queries", () => {
  it("searches projects by name and description", async () => {
    const fetchMock = mockFetch(() => Response.json({ items: [], total: 0 }))
    await runQuery(projectsQuery("产线 A"))
    expect(requestOf(fetchMock).path).toBe(
      "/api/v1/projects?q=%E4%BA%A7%E7%BA%BF+A"
    )
  })

  it("lists all projects without a query string", async () => {
    const fetchMock = mockFetch(() => Response.json({ items: [], total: 0 }))
    await runQuery(projectsQuery())
    expect(requestOf(fetchMock).path).toBe("/api/v1/projects")
  })

  it("loads the datasets of a project", async () => {
    const fetchMock = mockFetch(() => Response.json({ items: [], total: 0 }))
    await runQuery(projectDatasetsQuery(4))
    expect(requestOf(fetchMock).path).toBe("/api/v1/projects/4/datasets")
  })
})

describe("project writes", () => {
  it("creates a project with its first datasets", async () => {
    const fetchMock = mockFetch(() => Response.json({ id: 1 }))
    await createProject({ name: "P", dataset_ids: [3, 5] })
    expect(requestOf(fetchMock)).toEqual({
      path: "/api/v1/projects",
      method: "POST",
      body: { name: "P", dataset_ids: [3, 5] },
    })
  })

  it("adds and removes datasets", async () => {
    let fetchMock = mockFetch(() => Response.json({ id: 2 }))
    await addProjectDatasets(2, [7, 8])
    expect(requestOf(fetchMock)).toEqual({
      path: "/api/v1/projects/2/datasets",
      method: "POST",
      body: { dataset_ids: [7, 8] },
    })

    fetchMock = mockFetch(() => new Response(null, { status: 204 }))
    await expect(removeProjectDataset(2, 7)).resolves.toBeUndefined()
    expect(requestOf(fetchMock)).toMatchObject({
      path: "/api/v1/projects/2/datasets/7",
      method: "DELETE",
    })
  })

  it("deletes a project", async () => {
    const fetchMock = mockFetch(() => new Response(null, { status: 204 }))
    await expect(deleteProject(9)).resolves.toBeUndefined()
    expect(requestOf(fetchMock)).toMatchObject({
      path: "/api/v1/projects/9",
      method: "DELETE",
    })
  })
})

function project(id: number, name: string): Project {
  return {
    id,
    name,
    description: "",
    dataset_count: 0,
    image_count: 0,
    annotated_count: 0,
    empty_count: 0,
    invalid_count: 0,
    object_count: 0,
    total_bytes: 0,
    covers: [],
    created_at: "2026-10-30T08:00:00Z",
    updated_at: "2026-10-30T08:00:00Z",
  }
}

describe("project cache", () => {
  it("adds a new project to the cached list at once", () => {
    queryClient.setQueryData<ListResponse<Project>>(projectKeys.list(""), {
      items: [project(1, "A")],
      total: 1,
    })
    cacheNewProject(project(2, "B"))
    cacheNewProject(project(2, "B"))
    expect(
      queryClient.getQueryData<ListResponse<Project>>(projectKeys.list(""))
    ).toEqual({ items: [project(2, "B"), project(1, "A")], total: 2 })
    expect(queryClient.getQueryData(projectKeys.detail(2))).toEqual(
      project(2, "B")
    )
  })

  it("refreshes project queries and dataset lists and details, not their images", () => {
    const keys = [
      projectKeys.list(""),
      projectKeys.labels(3),
      datasetKeys.list("", 3),
      datasetKeys.detail(7),
      datasetKeys.labels(7),
      datasetKeys.directories(),
    ]
    for (const key of keys) {
      queryClient.setQueryData(key, {})
    }
    refreshMembership()
    const invalidated = keys.map(
      (key) => queryClient.getQueryState(key)?.isInvalidated
    )
    expect(invalidated).toEqual([true, true, true, true, false, false])
  })
})
