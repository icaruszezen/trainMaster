import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import type { Model, ModelInfo, ModelUpload } from "@/features/models/api"

type Reply = { status: number; body?: unknown }
type Handler = (method: string, url: string, body: unknown) => Reply

/** 请求日志，如 "PUT /api/v1/models/uploads/7?offset=0"。 */
let requests: string[] = []
/** 带 JSON 请求体的请求（提交时的 yolo_version）。 */
let bodies: { request: string; body: unknown }[] = []
let handler: Handler

const MiB = 1024 * 1024
const UPLOAD = "/api/v1/models/uploads/7"

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
      const reply = handler(this.method, this.url, undefined)
      this.status = reply.status
      this.responseText = encode(reply.body)
      this.onload?.()
    })
  }
}

function upload(overrides: Partial<ModelUpload> = {}): ModelUpload {
  return {
    id: 7,
    project_id: 3,
    yolo_version: "yolo11",
    description: "",
    file_name: "best.pt",
    file_size: 20,
    received_bytes: 0,
    status: "uploading",
    detected_yolo_version: null,
    error: "",
    model_id: null,
    created_at: "2026-11-03T08:00:00Z",
    updated_at: "2026-11-03T08:00:00Z",
    ...overrides,
  }
}

const info: ModelInfo = {
  task: "detect",
  model_class: "DetectionModel",
  head: "Detect",
  yaml_file: "yolo11n.yaml",
  scale: "n",
  detected_yolo_version: "yolo11",
  version_source: "yaml_file",
  class_names: ["scratch", "dent"],
  image_size: 640,
  channels: 3,
  parameters: 2_590_000,
  end2end: false,
  ultralytics_version: "8.4.198",
  trained_at: "2026-11-01T10:00:00",
  epochs: 100,
  epoch: -1,
  best_fitness: 0.8,
  metrics: {},
  train_data: "data.yaml",
  base_model: "yolo11n.pt",
  training_checkpoint: false,
}

function model(overrides: Partial<Model> = {}): Model {
  return {
    id: 5,
    project: { id: 3, name: "划痕检测" },
    version: 4,
    description: "",
    source: "upload",
    training_job_id: null,
    yolo_version: "yolo11",
    task: "detect",
    file_name: "best.pt",
    size_bytes: 20,
    sha256: "ab".repeat(32),
    info,
    file_missing: false,
    export_count: 0,
    download_url: "/api/v1/models/5/file",
    created_at: "2026-11-03T08:00:00Z",
    updated_at: "2026-11-03T08:00:00Z",
    ...overrides,
  }
}

const diskFull: Reply = {
  status: 507,
  body: { error: { code: "insufficient_storage", message: "磁盘空间不足" } },
}

function apiError(status: number, code: string, message: string): Reply {
  return { status, body: { error: { code, message } } }
}

/** 已上传的字节数：PUT 的 offset 加上 8 MiB 分块（不超过文件大小）。 */
function receivedAfter(url: string, size: number) {
  const offset = Number(
    new URL(url, "http://localhost").searchParams.get("offset")
  )
  return Math.min(size, offset + 8 * MiB)
}

beforeEach(() => {
  requests = []
  bodies = []
  vi.resetModules()
  vi.stubGlobal("window", {
    addEventListener() {},
    removeEventListener() {},
  })
  vi.stubGlobal("XMLHttpRequest", FakeXMLHttpRequest)
  vi.stubGlobal(
    "fetch",
    vi.fn(async (url: string, init: RequestInit = {}) => {
      const method = init.method ?? "GET"
      const request = `${method} ${url}`
      requests.push(request)
      const body =
        typeof init.body === "string" ? JSON.parse(init.body) : undefined
      if (body !== undefined) {
        bodies.push({ request, body })
      }
      const reply = handler(method, url, body)
      return new Response(encode(reply.body) || null, { status: reply.status })
    })
  )
})

afterEach(() => {
  vi.unstubAllGlobals()
})

/** 每个用例重新加载模块，任务列表与 QueryClient 互不影响。 */
async function load() {
  const manager = await import("@/features/models/upload-manager")
  const { queryClient } = await import("@/lib/query-client")
  const invalidate = vi.spyOn(queryClient, "invalidateQueries")
  return { manager, queryClient, invalidate }
}

type Manager = Awaited<ReturnType<typeof load>>["manager"]

function start(manager: Manager, size = 20) {
  return manager.startModelUpload({
    project: { id: 3, name: "划痕检测" },
    upload: upload({ file_size: size }),
    file: new File([new Uint8Array(size)], "best.pt"),
  })
}

async function waitForStatus(manager: Manager, status: string) {
  await vi.waitFor(() =>
    expect(manager.getModelUploadTasks()[0]?.status).toBe(status)
  )
  return manager.getModelUploadTasks()[0]
}

describe("uploading a model", () => {
  it("uploads 8 MiB chunks in order and commits with the selected version", async () => {
    const size = 8 * MiB + 10
    handler = (method, url) => {
      if (method === "PUT") {
        return {
          status: 200,
          body: upload({
            file_size: size,
            received_bytes: receivedAfter(url, size),
          }),
        }
      }
      if (url === `${UPLOAD}/commit`) {
        return { status: 201, body: model() }
      }
      return { status: 404 }
    }
    const { manager, queryClient, invalidate } = await load()
    start(manager, size)

    const task = await waitForStatus(manager, "done")
    expect(task).toMatchObject({
      uploadedBytes: size,
      model: { id: 5, version: 4 },
      error: "",
    })
    expect(requests).toEqual([
      `PUT ${UPLOAD}?offset=0`,
      `PUT ${UPLOAD}?offset=${8 * MiB}`,
      `POST ${UPLOAD}/commit`,
    ])
    expect(bodies).toEqual([
      { request: `POST ${UPLOAD}/commit`, body: { yolo_version: "yolo11" } },
    ])
    // 新模型写入详情缓存；模型列表与项目（模型数、最新版本）刷新。
    expect(queryClient.getQueryData(["models", "detail", 5])).toMatchObject({
      version: 4,
    })
    expect(invalidate).toHaveBeenCalledWith({ queryKey: ["models"] })
    expect(invalidate).toHaveBeenCalledWith({ queryKey: ["projects"] })
  })

  it("continues from the server's offset after an offset mismatch", async () => {
    const size = 8 * MiB + 10
    let first = true
    handler = (method, url) => {
      if (method === "PUT") {
        if (first) {
          // 上一个分块其实已经写入，只是响应丢失了。
          first = false
          return apiError(
            409,
            "upload_offset_mismatch",
            "偏移量与已上传的不一致"
          )
        }
        return {
          status: 200,
          body: upload({
            file_size: size,
            received_bytes: receivedAfter(url, size),
          }),
        }
      }
      if (url === UPLOAD) {
        return {
          status: 200,
          body: upload({ file_size: size, received_bytes: 8 * MiB }),
        }
      }
      if (url === `${UPLOAD}/commit`) {
        return { status: 201, body: model() }
      }
      return { status: 404 }
    }
    const { manager } = await load()
    start(manager, size)

    await waitForStatus(manager, "done")
    expect(requests).toEqual([
      `PUT ${UPLOAD}?offset=0`,
      `GET ${UPLOAD}`,
      `PUT ${UPLOAD}?offset=${8 * MiB}`,
      `POST ${UPLOAD}/commit`,
    ])
  })

  it("resumes a failed upload from where the server stopped", async () => {
    let full = true
    handler = (method, url) => {
      if (method === "PUT") {
        if (full) {
          return diskFull
        }
        return { status: 200, body: upload({ received_bytes: 20 }) }
      }
      if (url === UPLOAD) {
        return { status: 200, body: upload() }
      }
      if (url === `${UPLOAD}/commit`) {
        return { status: 201, body: model() }
      }
      return { status: 404 }
    }
    const { manager } = await load()
    const id = start(manager)

    const failed = await waitForStatus(manager, "error")
    expect(failed).toMatchObject({ retryable: true, error: "磁盘空间不足" })

    full = false
    manager.retryModelUpload(id)
    await waitForStatus(manager, "done")
    expect(requests).toEqual([
      `PUT ${UPLOAD}?offset=0`,
      `GET ${UPLOAD}`,
      `PUT ${UPLOAD}?offset=0`,
      `POST ${UPLOAD}/commit`,
    ])
  })
})

describe("a YOLO version mismatch", () => {
  function mismatchHandler(): Handler {
    let commits = 0
    return (method, url, body) => {
      if (method === "PUT") {
        return { status: 200, body: upload({ received_bytes: 20 }) }
      }
      if (url === UPLOAD) {
        return {
          status: 200,
          body: upload({ received_bytes: 20, detected_yolo_version: "yolov8" }),
        }
      }
      if (url === `${UPLOAD}/commit`) {
        commits += 1
        const version = (body as { yolo_version?: string } | undefined)
          ?.yolo_version
        return commits === 1 || version !== "yolov8"
          ? apiError(
              422,
              "yolo_version_mismatch",
              "文件中的模型是 YOLOv8，与所选的 YOLO11 不同"
            )
          : { status: 201, body: model({ yolo_version: "yolov8" }) }
      }
      if (url === `${UPLOAD}/cancel`) {
        return { status: 200, body: upload({ status: "canceled" }) }
      }
      return { status: 404 }
    }
  }

  it("offers the detected version and commits again without re-uploading", async () => {
    handler = mismatchHandler()
    const { manager } = await load()
    const id = start(manager)

    const task = await waitForStatus(manager, "mismatch")
    expect(task).toMatchObject({
      detectedVersion: "yolov8",
      errorCode: "yolo_version_mismatch",
      retryable: false,
    })
    // 等待改选版本时服务端仍保留着文件：任务没有结束。
    expect(manager.isModelUploadActive(task)).toBe(true)

    manager.commitWithYoloVersion(id, "yolov8")
    const done = await waitForStatus(manager, "done")
    expect(done).toMatchObject({
      yoloVersion: "yolov8",
      model: { yolo_version: "yolov8" },
    })
    expect(requests).toEqual([
      `PUT ${UPLOAD}?offset=0`,
      `POST ${UPLOAD}/commit`,
      `GET ${UPLOAD}`,
      `GET ${UPLOAD}`,
      `POST ${UPLOAD}/commit`,
    ])
    expect(bodies.map((entry) => entry.body)).toEqual([
      { yolo_version: "yolo11" },
      { yolo_version: "yolov8" },
    ])
  })

  it("cancels the server upload when the user gives up", async () => {
    handler = mismatchHandler()
    const { manager } = await load()
    const id = start(manager)
    await waitForStatus(manager, "mismatch")

    await manager.cancelModelUploadTask(id)

    expect(manager.getModelUploadTasks()[0]).toMatchObject({
      status: "canceled",
    })
    expect(requests.at(-1)).toBe(`POST ${UPLOAD}/cancel`)
  })

  it("keeps the model when it was saved before the cancel arrived", async () => {
    const base = mismatchHandler()
    handler = (method, url, body) => {
      if (url === `${UPLOAD}/cancel`) {
        // 另一个标签页已经改选版本并保存了。
        return {
          status: 200,
          body: upload({ status: "completed", model_id: 5 }),
        }
      }
      if (url === "/api/v1/models/5") {
        return { status: 200, body: model({ yolo_version: "yolov8" }) }
      }
      return base(method, url, body)
    }
    const { manager } = await load()
    const id = start(manager)
    await waitForStatus(manager, "mismatch")

    await manager.cancelModelUploadTask(id)

    expect(manager.getModelUploadTasks()[0]).toMatchObject({
      status: "done",
      model: { id: 5 },
    })
  })
})

describe("a file that is not a model", () => {
  it("shows the reason and cannot be retried", async () => {
    handler = (method, url) => {
      if (method === "PUT") {
        return { status: 200, body: upload({ received_bytes: 20 }) }
      }
      if (url === `${UPLOAD}/commit`) {
        return apiError(422, "invalid_model", "文件不是 PyTorch 模型（.pt）")
      }
      return { status: 404 }
    }
    const { manager } = await load()
    const id = start(manager)

    const task = await waitForStatus(manager, "error")
    expect(task).toMatchObject({
      errorCode: "invalid_model",
      error: "文件不是 PyTorch 模型（.pt）",
      retryable: false,
    })

    const before = requests.length
    manager.retryModelUpload(id)
    expect(manager.getModelUploadTasks()[0]?.status).toBe("error")
    expect(requests).toHaveLength(before)
  })
})

describe("dismissing a failed upload", () => {
  it("cancels the server upload that is still waiting for data", async () => {
    handler = (method, url) => {
      if (method === "PUT") return diskFull
      if (url === `${UPLOAD}/cancel`) {
        return { status: 200, body: upload({ status: "canceled" }) }
      }
      return { status: 200, body: upload() }
    }
    const { manager } = await load()
    const id = start(manager)
    await waitForStatus(manager, "error")

    manager.dismissModelUpload(id)

    expect(manager.getModelUploadTasks()).toEqual([])
    await vi.waitFor(() =>
      expect(requests.at(-1)).toBe(`POST ${UPLOAD}/cancel`)
    )
    expect(requests).toEqual([
      `PUT ${UPLOAD}?offset=0`,
      `GET ${UPLOAD}`,
      `POST ${UPLOAD}/cancel`,
    ])
  })

  it("does not remove uploads that are still running", async () => {
    handler = () => ({ status: 200, body: upload() })
    // 分块请求一直没有响应。
    vi.stubGlobal(
      "XMLHttpRequest",
      class extends FakeXMLHttpRequest {
        send() {
          requests.push(`${this.method} ${this.url}`)
        }
      }
    )
    const { manager } = await load()
    start(manager)

    manager.dismissFinishedModelUploads()

    expect(manager.getModelUploadTasks()).toHaveLength(1)
    expect(manager.getModelUploadTasks()[0]?.status).toBe("uploading")
  })
})
