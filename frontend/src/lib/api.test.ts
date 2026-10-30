import { afterEach, describe, expect, it, vi } from "vitest"

import {
  ApiError,
  apiGet,
  apiSend,
  apiUpload,
  isAbortError,
  isPermanentError,
  parseErrorResponse,
} from "@/lib/api"

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json; charset=utf-8" },
  })
}

function mockFetch(implementation: () => Promise<Response>) {
  const fetchMock = vi.fn(implementation)
  vi.stubGlobal("fetch", fetchMock)
  return fetchMock
}

afterEach(() => {
  vi.unstubAllGlobals()
})

describe("parseErrorResponse", () => {
  it("reads the error envelope", async () => {
    const error = await parseErrorResponse(
      jsonResponse(
        { error: { code: "not_implemented", message: "该功能尚未实现" } },
        501
      )
    )
    expect(error).toBeInstanceOf(ApiError)
    expect(error).toMatchObject({
      status: 501,
      code: "not_implemented",
      message: "该功能尚未实现",
    })
  })

  it("falls back to a generic error for non-envelope bodies", async () => {
    const error = await parseErrorResponse(
      new Response("Bad Gateway", { status: 502 })
    )
    expect(error).toMatchObject({ status: 502, code: "http_error" })
    expect(error.message).toContain("502")
  })

  it("rejects envelopes with the wrong shape", async () => {
    const error = await parseErrorResponse(jsonResponse({ error: "boom" }, 500))
    expect(error.code).toBe("http_error")
  })
})

describe("apiGet", () => {
  it("returns the parsed JSON body", async () => {
    const fetchMock = mockFetch(async () => jsonResponse({ status: "ok" }))
    await expect(apiGet("/api/healthz")).resolves.toEqual({ status: "ok" })
    expect(fetchMock).toHaveBeenCalledWith(
      "/api/healthz",
      expect.objectContaining({ method: "GET" })
    )
  })

  it("throws ApiError for non-2xx responses", async () => {
    mockFetch(async () =>
      jsonResponse({ error: { code: "not_found", message: "接口不存在" } }, 404)
    )
    await expect(apiGet("/api/nope")).rejects.toMatchObject({
      name: "ApiError",
      status: 404,
      code: "not_found",
      message: "接口不存在",
    })
  })

  it("turns network failures into network_error", async () => {
    mockFetch(async () => {
      throw new TypeError("Failed to fetch")
    })
    await expect(apiGet("/api/v1/system/info")).rejects.toMatchObject({
      status: 0,
      code: "network_error",
      message: "无法连接到服务器",
    })
  })

  it("rethrows aborts unchanged", async () => {
    const controller = new AbortController()
    controller.abort()
    const abortError = new DOMException("aborted", "AbortError")
    mockFetch(async () => {
      throw abortError
    })
    await expect(
      apiGet("/api/v1/system/info", { signal: controller.signal })
    ).rejects.toBe(abortError)
  })

  it("reports unparsable success bodies", async () => {
    mockFetch(async () => new Response("<!doctype html>", { status: 200 }))
    await expect(apiGet("/api/v1/system/info")).rejects.toMatchObject({
      code: "invalid_response",
    })
  })
})

describe("apiSend", () => {
  it("sends the body as JSON and returns the parsed response", async () => {
    const fetchMock = mockFetch(async () => jsonResponse({ id: 1 }, 201))
    await expect(
      apiSend("POST", "/api/v1/datasets", { name: "划痕" })
    ).resolves.toEqual({ id: 1 })

    const [path, init] = fetchMock.mock.calls[0] as unknown as [
      string,
      RequestInit,
    ]
    expect(path).toBe("/api/v1/datasets")
    expect(init.method).toBe("POST")
    expect(init.body).toBe(JSON.stringify({ name: "划痕" }))
    const headers = new Headers(init.headers)
    expect(headers.get("Content-Type")).toBe("application/json")
    expect(headers.get("Accept")).toBe("application/json")
  })

  it("omits the body and content type when there is no payload", async () => {
    const fetchMock = mockFetch(async () => jsonResponse({ status: "queued" }))
    await apiSend("POST", "/api/v1/datasets/1/scan")
    const [, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit]
    expect(init.body).toBeUndefined()
    expect(new Headers(init.headers).has("Content-Type")).toBe(false)
  })

  it("resolves to undefined for 204 No Content", async () => {
    mockFetch(async () => new Response(null, { status: 204 }))
    await expect(
      apiSend("DELETE", "/api/v1/datasets/1")
    ).resolves.toBeUndefined()
  })

  it("throws ApiError with the envelope message", async () => {
    mockFetch(async () =>
      jsonResponse(
        { error: { code: "conflict", message: "已存在同名数据集" } },
        409
      )
    )
    await expect(
      apiSend("PATCH", "/api/v1/datasets/1", { name: "a" })
    ).rejects.toMatchObject({ status: 409, code: "conflict" })
  })

  it("turns network failures into network_error", async () => {
    mockFetch(async () => {
      throw new TypeError("Failed to fetch")
    })
    await expect(apiSend("POST", "/api/v1/datasets", {})).rejects.toMatchObject(
      { code: "network_error" }
    )
  })
})

/** 只实现 apiUpload 用到的部分。 */
class FakeXMLHttpRequest {
  static last: FakeXMLHttpRequest | undefined
  method = ""
  url = ""
  status = 0
  responseText = ""
  headers: Record<string, string> = {}
  body: unknown
  aborted = false
  upload: { onprogress: ((event: ProgressEvent) => void) | null } = {
    onprogress: null,
  }
  onload: (() => void) | null = null
  onerror: (() => void) | null = null
  ontimeout: (() => void) | null = null
  onabort: (() => void) | null = null

  constructor() {
    FakeXMLHttpRequest.last = this
  }
  open(method: string, url: string) {
    this.method = method
    this.url = url
  }
  setRequestHeader(name: string, value: string) {
    this.headers[name] = value
  }
  send(body: unknown) {
    this.body = body
  }
  abort() {
    this.aborted = true
    this.onabort?.()
  }
  respond(status: number, body: unknown) {
    this.status = status
    this.responseText = typeof body === "string" ? body : JSON.stringify(body)
    this.onload?.()
  }
}

function lastXhr() {
  const xhr = FakeXMLHttpRequest.last
  if (!xhr) {
    throw new Error("no request sent")
  }
  return xhr
}

describe("apiUpload", () => {
  afterEach(() => {
    FakeXMLHttpRequest.last = undefined
  })

  function stubXhr() {
    vi.stubGlobal("XMLHttpRequest", FakeXMLHttpRequest)
  }

  it("uploads the body, reports progress and parses the response", async () => {
    stubXhr()
    const onProgress = vi.fn()
    const promise = apiUpload("PUT", "/api/v1/x?offset=0", "abc", {
      onProgress,
      contentType: "application/octet-stream",
    })
    const xhr = lastXhr()
    expect(xhr.method).toBe("PUT")
    expect(xhr.url).toBe("/api/v1/x?offset=0")
    expect(xhr.body).toBe("abc")
    expect(xhr.headers["Content-Type"]).toBe("application/octet-stream")

    xhr.upload.onprogress?.({
      loaded: 2,
      total: 3,
      lengthComputable: true,
    } as ProgressEvent)
    expect(onProgress).toHaveBeenCalledWith(2, 3)

    xhr.respond(200, { received_bytes: 3 })
    await expect(promise).resolves.toEqual({ received_bytes: 3 })
  })

  it("parses error envelopes", async () => {
    stubXhr()
    const promise = apiUpload("PUT", "/api/v1/x", "abc")
    lastXhr().respond(409, {
      error: { code: "upload_offset_mismatch", message: "偏移量不一致" },
    })
    await expect(promise).rejects.toMatchObject({
      status: 409,
      code: "upload_offset_mismatch",
    })
  })

  it("reports network errors", async () => {
    stubXhr()
    const promise = apiUpload("POST", "/api/v1/x", "abc")
    lastXhr().onerror?.()
    await expect(promise).rejects.toMatchObject({ code: "network_error" })
  })

  it("aborts the request when the signal fires", async () => {
    stubXhr()
    const controller = new AbortController()
    const promise = apiUpload("POST", "/api/v1/x", "abc", {
      signal: controller.signal,
    })
    controller.abort()
    expect(lastXhr().aborted).toBe(true)
    await expect(promise).rejects.toSatisfy(isAbortError)
  })

  it("rejects immediately when already aborted", async () => {
    stubXhr()
    const controller = new AbortController()
    controller.abort()
    await expect(
      apiUpload("POST", "/api/v1/x", "abc", { signal: controller.signal })
    ).rejects.toSatisfy(isAbortError)
    expect(FakeXMLHttpRequest.last).toBeUndefined()
  })
})

describe("isPermanentError", () => {
  it("treats 4xx and 501 as permanent", () => {
    expect(isPermanentError(new ApiError(404, "not_found", ""))).toBe(true)
    expect(isPermanentError(new ApiError(501, "not_implemented", ""))).toBe(
      true
    )
  })

  it("treats network errors and other 5xx as transient", () => {
    expect(isPermanentError(new ApiError(0, "network_error", ""))).toBe(false)
    expect(isPermanentError(new ApiError(502, "http_error", ""))).toBe(false)
    expect(isPermanentError(new Error("boom"))).toBe(false)
  })
})
