import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import type { DatasetImport } from "@/features/datasets/api"

type Reply = { status: number; body?: unknown }
type Handler = (method: string, url: string) => Reply

/** 请求日志，如 "PUT /api/v1/datasets/3/imports/7/archive?offset=0"。 */
let requests: string[] = []
let handler: Handler

function encode(body: unknown) {
  return body === undefined ? "" : JSON.stringify(body)
}

/** 只实现 apiUpload 用到的部分。 */
class FakeXMLHttpRequest {
  method = ""
  url = ""
  status = 0
  responseText = ""
  upload = { onprogress: null }
  onload: (() => void) | null = null
  onerror: (() => void) | null = null
  ontimeout: (() => void) | null = null
  onabort: (() => void) | null = null
  open(method: string, url: string) {
    this.method = method
    this.url = url
  }
  setRequestHeader() {}
  abort() {
    this.onabort?.()
  }
  send() {
    requests.push(`${this.method} ${this.url}`)
    setTimeout(() => {
      const reply = handler(this.method, this.url)
      this.status = reply.status
      this.responseText = encode(reply.body)
      this.onload?.()
    })
  }
}

function job(overrides: Partial<DatasetImport> = {}): DatasetImport {
  return {
    id: 7,
    dataset_id: 3,
    source: "archive",
    status: "uploading",
    on_conflict: "skip",
    annotation_format: "voc",
    classes: [],
    file_name: "batch.zip",
    file_size: 20,
    file_count: 0,
    received_bytes: 0,
    received_files: 0,
    stage: "",
    progress: { done: 0, total: 0, unit: "" },
    summary: {},
    error: "",
    created_at: "2026-10-30T08:00:00Z",
    updated_at: "2026-10-30T08:00:00Z",
    started_at: null,
    finished_at: null,
    ...overrides,
  }
}

const IMPORT = "/api/v1/datasets/3/imports/7"
const diskFull: Reply = {
  status: 507,
  body: { error: { code: "insufficient_storage", message: "磁盘空间不足" } },
}

beforeEach(() => {
  requests = []
  vi.resetModules()
  vi.stubGlobal("window", {
    addEventListener() {},
    removeEventListener() {},
  })
  vi.stubGlobal("XMLHttpRequest", FakeXMLHttpRequest)
  vi.stubGlobal(
    "fetch",
    vi.fn(async (url: string, init: RequestInit) => {
      const method = init.method ?? "GET"
      requests.push(`${method} ${url}`)
      const reply = handler(method, url)
      return new Response(encode(reply.body) || null, { status: reply.status })
    })
  )
})

afterEach(() => {
  vi.unstubAllGlobals()
})

/** 每个用例重新加载模块，任务列表与 QueryClient 互不影响。 */
async function load() {
  const manager = await import("@/features/datasets/upload-manager")
  const { queryClient } = await import("@/lib/query-client")
  const invalidate = vi.spyOn(queryClient, "invalidateQueries")
  return { manager, invalidate }
}

async function startFailingUpload(
  manager: Awaited<ReturnType<typeof load>>["manager"]
) {
  const id = manager.startArchiveUpload({
    dataset: { id: 3, name: "划痕" },
    job: job(),
    file: new File([new Uint8Array(20)], "batch.zip"),
  })
  await vi.waitFor(() =>
    expect(manager.getUploadTasks()[0]).toMatchObject({
      status: "error",
      retryable: true,
    })
  )
  return id
}

describe("dismissing a failed upload", () => {
  it("cancels the server job that is still waiting for data", async () => {
    handler = (method, url) => {
      if (method === "PUT") return diskFull
      if (url === `${IMPORT}/cancel`) {
        return { status: 200, body: job({ status: "canceled" }) }
      }
      return { status: 200, body: job() }
    }
    const { manager, invalidate } = await load()
    const id = await startFailingUpload(manager)

    manager.dismissUpload(id)

    expect(manager.getUploadTasks()).toEqual([])
    await vi.waitFor(() =>
      expect(invalidate).toHaveBeenCalledWith({ queryKey: ["datasets"] })
    )
    expect(requests).toEqual([
      `PUT ${IMPORT}/archive?offset=0`,
      `GET ${IMPORT}`,
      `POST ${IMPORT}/cancel`,
    ])
  })

  it("leaves a job alone that the server is already processing", async () => {
    // 提交请求其实成功了，只是响应丢失：任务已在排队，不能取消。
    handler = (method) =>
      method === "PUT"
        ? diskFull
        : { status: 200, body: job({ status: "queued" }) }
    const { manager, invalidate } = await load()
    await startFailingUpload(manager)

    manager.dismissFinishedUploads()

    await vi.waitFor(() =>
      expect(invalidate).toHaveBeenCalledWith({ queryKey: ["datasets"] })
    )
    expect(requests).toEqual([
      `PUT ${IMPORT}/archive?offset=0`,
      `GET ${IMPORT}`,
    ])
  })
})

describe("committing an upload", () => {
  it("refreshes the import records so the new job shows up", async () => {
    handler = (method, url) => {
      if (method === "PUT") {
        return { status: 200, body: job({ received_bytes: 20 }) }
      }
      if (url === `${IMPORT}/commit`) {
        return { status: 200, body: job({ status: "queued" }) }
      }
      return { status: 200, body: job({ status: "succeeded" }) }
    }
    const { manager, invalidate } = await load()
    manager.startArchiveUpload({
      dataset: { id: 3, name: "划痕" },
      job: job(),
      file: new File([new Uint8Array(20)], "batch.zip"),
    })

    await vi.waitFor(() =>
      expect(manager.getUploadTasks()[0]?.status).toBe("done")
    )
    expect(invalidate).toHaveBeenCalledWith({
      queryKey: ["datasets", "detail", 3, "imports"],
    })
  })
})
