import { afterEach, describe, expect, it, vi } from "vitest"

import {
  ApiError,
  apiGet,
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
