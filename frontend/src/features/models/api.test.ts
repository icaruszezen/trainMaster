import { afterEach, describe, expect, it, vi } from "vitest"

import {
  ALL_MODELS,
  commitModelUpload,
  createModelUpload,
  deleteModel,
  deleteModelExport,
  modelExportQuery,
  modelExportsQuery,
  modelKeys,
  modelsQuery,
  projectModelsQuery,
  refreshModelExports,
  refreshModels,
  requestModelExport,
  updateModel,
  type ModelExport,
} from "@/features/models/api"
import { projectKeys } from "@/features/projects/api"
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

async function runQuery(options: { queryFn?: unknown }) {
  await (options.queryFn as (context: unknown) => Promise<unknown>)({
    signal: new AbortController().signal,
  })
}

const emptyList = () => Response.json({ items: [], total: 0 })

describe("model queries", () => {
  it("lists all models without a query string", async () => {
    const fetchMock = mockFetch(emptyList)
    await runQuery(modelsQuery())
    expect(requestOf(fetchMock).path).toBe("/api/v1/models")
  })

  it("filters by project, YOLO version, source and keyword", async () => {
    const fetchMock = mockFetch(emptyList)
    await runQuery(
      modelsQuery({
        projectId: 3,
        yoloVersion: "yolo11",
        source: "upload",
        q: "夜班",
      })
    )
    expect(requestOf(fetchMock).path).toBe(
      "/api/v1/models?project_id=3&yolo_version=yolo11&source=upload&q=%E5%A4%9C%E7%8F%AD"
    )
  })

  it("lists a project's models", async () => {
    const fetchMock = mockFetch(emptyList)
    await runQuery(projectModelsQuery(3))
    expect(requestOf(fetchMock).path).toBe("/api/v1/projects/3/models")
  })

  it("keeps every model query under one key so a single refresh covers them", () => {
    for (const key of [
      modelKeys.list(ALL_MODELS),
      modelKeys.project(3),
      modelKeys.detail(5),
      modelKeys.exports(5),
      modelKeys.export(5, 9),
    ]) {
      expect(key[0]).toBe("models")
    }
    // 转换记录在模型详情下面：刷新详情时一起刷新。
    expect(modelKeys.exports(5).slice(0, 3)).toEqual(modelKeys.detail(5))
  })

  it("polls conversions only while they are active", () => {
    const job = (status: ModelExport["status"]) => ({ status }) as ModelExport
    const interval = (options: { refetchInterval?: unknown }, data: unknown) =>
      (options.refetchInterval as (query: unknown) => number | false)({
        state: { data },
      })

    const list = modelExportsQuery(5)
    expect(interval(list, { items: [job("succeeded")], total: 1 })).toBe(false)
    expect(
      interval(list, { items: [job("failed"), job("running")], total: 2 })
    ).toBe(2_000)

    const single = modelExportQuery(5, 9)
    expect(interval(single, undefined)).toBe(1_000)
    expect(interval(single, job("queued"))).toBe(1_000)
    expect(interval(single, job("succeeded"))).toBe(false)
    expect(interval(single, job("failed"))).toBe(false)
  })
})

describe("model writes", () => {
  it("updates the description", async () => {
    const fetchMock = mockFetch(() => Response.json({}))
    await updateModel(5, { description: "补充夜班数据" })
    expect(requestOf(fetchMock)).toEqual({
      path: "/api/v1/models/5",
      method: "PATCH",
      body: { description: "补充夜班数据" },
    })
  })

  it("deletes a model", async () => {
    const fetchMock = mockFetch(() => new Response(null, { status: 204 }))
    await expect(deleteModel(5)).resolves.toBeUndefined()
    expect(requestOf(fetchMock)).toMatchObject({
      path: "/api/v1/models/5",
      method: "DELETE",
    })
  })

  it("reports model files that could not be removed", async () => {
    mockFetch(() =>
      Response.json({ files_removed: false, message: "文件夹未能删除" })
    )
    await expect(deleteModel(5)).resolves.toEqual({
      files_removed: false,
      message: "文件夹未能删除",
    })
  })

  it("creates and commits an upload with the YOLO version", async () => {
    let fetchMock = mockFetch(() => Response.json({ id: 7 }, { status: 201 }))
    await createModelUpload({
      project_id: 3,
      yolo_version: "yolo26",
      file_name: "best.pt",
      file_size: 1024,
      description: "第一版",
    })
    expect(requestOf(fetchMock)).toEqual({
      path: "/api/v1/models/uploads",
      method: "POST",
      body: {
        project_id: 3,
        yolo_version: "yolo26",
        file_name: "best.pt",
        file_size: 1024,
        description: "第一版",
      },
    })

    fetchMock = mockFetch(() => Response.json({ id: 5 }, { status: 201 }))
    await commitModelUpload(7, { yolo_version: "yolov8" })
    expect(requestOf(fetchMock)).toEqual({
      path: "/api/v1/models/uploads/7/commit",
      method: "POST",
      body: { yolo_version: "yolov8" },
    })
  })

  it("surfaces the mismatch error code from the commit", async () => {
    mockFetch(() =>
      Response.json(
        {
          error: {
            code: "yolo_version_mismatch",
            message: "文件中的模型是 YOLOv8，与所选的 YOLO11 不同",
          },
        },
        { status: 422 }
      )
    )
    await expect(commitModelUpload(7)).rejects.toMatchObject({
      status: 422,
      code: "yolo_version_mismatch",
    })
  })

  it("requests and deletes conversions", async () => {
    let fetchMock = mockFetch(() => Response.json({ id: 9 }, { status: 202 }))
    await requestModelExport(5, {
      format: "onnx",
      imgsz: [480, 640],
      dynamic: true,
      postprocess: "end2end",
      opset: null,
    })
    expect(requestOf(fetchMock)).toEqual({
      path: "/api/v1/models/5/exports",
      method: "POST",
      body: {
        format: "onnx",
        imgsz: [480, 640],
        dynamic: true,
        postprocess: "end2end",
        opset: null,
      },
    })

    fetchMock = mockFetch(() => new Response(null, { status: 204 }))
    await deleteModelExport(5, 9)
    expect(requestOf(fetchMock)).toMatchObject({
      path: "/api/v1/models/5/exports/9",
      method: "DELETE",
    })
  })
})

describe("refreshing", () => {
  it("refreshes models and the projects that show model counts", () => {
    const invalidate = vi.spyOn(queryClient, "invalidateQueries")
    refreshModels()
    expect(invalidate).toHaveBeenCalledWith({ queryKey: modelKeys.all })
    expect(invalidate).toHaveBeenCalledWith({ queryKey: projectKeys.all })
  })

  it("refreshes a model's conversions and the lists showing its count", () => {
    const invalidate = vi.spyOn(queryClient, "invalidateQueries")
    refreshModelExports(5)
    expect(invalidate).toHaveBeenCalledWith({
      queryKey: modelKeys.detail(5),
    })
    expect(invalidate).toHaveBeenCalledWith({ queryKey: modelKeys.lists() })
    expect(invalidate).toHaveBeenCalledWith({
      queryKey: ["models", "project"],
    })
  })
})
