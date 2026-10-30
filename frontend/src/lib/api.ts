/**
 * 后端 API 客户端。
 *
 * 后端约定：成功时直接返回资源 JSON；失败时返回统一的错误信封
 * `{"error": {"code": "not_found", "message": "接口不存在"}}`。
 */

export class ApiError extends Error {
  /** HTTP 状态码；网络错误时为 0。 */
  readonly status: number
  /** 机器可读的错误码，如 `not_found`、`not_implemented`、`network_error`。 */
  readonly code: string

  constructor(status: number, code: string, message: string) {
    super(message)
    this.name = "ApiError"
    this.status = status
    this.code = code
  }
}

/** 列表接口的统一响应：`{"items": [], "total": 0}`。 */
export type ListResponse<T> = { items: T[]; total: number }

/** 再请求一次结果也不会变的错误：4xx 与 501（功能尚未实现）。 */
export function isPermanentError(error: unknown) {
  return (
    error instanceof ApiError &&
    ((error.status >= 400 && error.status < 500) || error.status === 501)
  )
}

/** 主动取消请求（AbortController）时抛出的错误。 */
export function isAbortError(error: unknown) {
  return error instanceof DOMException && error.name === "AbortError"
}

type ErrorEnvelope = { error: { code: string; message: string } }

function isErrorEnvelope(body: unknown): body is ErrorEnvelope {
  if (typeof body !== "object" || body === null || !("error" in body)) {
    return false
  }
  const { error } = body
  return (
    typeof error === "object" &&
    error !== null &&
    "code" in error &&
    typeof error.code === "string" &&
    "message" in error &&
    typeof error.message === "string"
  )
}

/** 把非 2xx 响应转换成 ApiError；响应体不是错误信封时给出通用提示。 */
export async function parseErrorResponse(response: Response) {
  const body: unknown = await response.json().catch(() => undefined)
  if (isErrorEnvelope(body)) {
    return new ApiError(response.status, body.error.code, body.error.message)
  }
  // 没有错误信封的 5xx 通常来自网关 / 反向代理，说明后端暂时不可达。
  const message =
    response.status >= 500
      ? `服务器暂时不可用（HTTP ${response.status}）`
      : `请求失败（HTTP ${response.status}）`
  return new ApiError(response.status, "http_error", message)
}

function networkError() {
  return new ApiError(0, "network_error", "无法连接到服务器")
}

function invalidResponse(status: number) {
  return new ApiError(status, "invalid_response", "服务器返回了无法解析的数据")
}

async function request<T>(path: string, init: RequestInit): Promise<T> {
  const headers = new Headers(init.headers)
  if (!headers.has("Accept")) {
    headers.set("Accept", "application/json")
  }

  let response: Response
  try {
    response = await fetch(path, { ...init, headers })
  } catch (error) {
    // 主动取消（如 React Query 卸载组件）保持原样抛出。
    if (init.signal?.aborted) {
      throw error
    }
    throw networkError()
  }

  if (!response.ok) {
    throw await parseErrorResponse(response)
  }
  if (response.status === 204) {
    return undefined as T
  }

  try {
    return (await response.json()) as T
  } catch {
    throw invalidResponse(response.status)
  }
}

/** GET 一个 JSON 资源，例如 `apiGet<SystemInfo>("/api/v1/system/info")`。 */
export function apiGet<T>(path: string, init?: RequestInit): Promise<T> {
  return request<T>(path, { ...init, method: "GET" })
}

export type SendMethod = "POST" | "PUT" | "PATCH" | "DELETE"

/**
 * 发送一个写请求，`body` 编码为 JSON（省略时不带请求体）。
 * 返回解析后的响应 JSON；`204 No Content` 时返回 `undefined`。
 */
export function apiSend<T = void>(
  method: SendMethod,
  path: string,
  body?: unknown,
  init?: RequestInit
): Promise<T> {
  const headers = new Headers(init?.headers)
  if (body !== undefined) {
    headers.set("Content-Type", "application/json")
  }
  return request<T>(path, {
    ...init,
    method,
    headers,
    body: body === undefined ? undefined : JSON.stringify(body),
  })
}

export type UploadOptions = {
  signal?: AbortSignal
  /** 请求体的上传进度（字节），`total` 为整个请求体的大小。 */
  onProgress?: (loaded: number, total: number) => void
  contentType?: string
}

/**
 * 用 XMLHttpRequest 上传请求体（fetch 无法报告上传进度）。
 * 响应与错误的处理和 `apiSend` 一致；取消时以 `AbortError` 拒绝。
 */
export function apiUpload<T>(
  method: "POST" | "PUT",
  path: string,
  body: XMLHttpRequestBodyInit,
  options: UploadOptions = {}
): Promise<T> {
  const { signal, onProgress, contentType } = options

  return new Promise<T>((resolve, reject) => {
    if (signal?.aborted) {
      reject(new DOMException("上传已取消", "AbortError"))
      return
    }

    const xhr = new XMLHttpRequest()
    const onAbort = () => xhr.abort()
    const cleanup = () => signal?.removeEventListener("abort", onAbort)

    xhr.open(method, path)
    xhr.setRequestHeader("Accept", "application/json")
    if (contentType) {
      xhr.setRequestHeader("Content-Type", contentType)
    }
    if (onProgress) {
      xhr.upload.onprogress = (event) => {
        onProgress(event.loaded, event.lengthComputable ? event.total : 0)
      }
    }

    xhr.onload = () => {
      cleanup()
      const { status, responseText } = xhr
      if (status < 200 || status > 599) {
        reject(networkError())
        return
      }
      if (status >= 300) {
        parseErrorResponse(new Response(responseText || null, { status })).then(
          reject,
          reject
        )
        return
      }
      if (status === 204 || responseText === "") {
        resolve(undefined as T)
        return
      }
      try {
        resolve(JSON.parse(responseText) as T)
      } catch {
        reject(invalidResponse(status))
      }
    }
    xhr.onerror = () => {
      cleanup()
      reject(networkError())
    }
    xhr.ontimeout = xhr.onerror
    xhr.onabort = () => {
      cleanup()
      reject(new DOMException("上传已取消", "AbortError"))
    }

    signal?.addEventListener("abort", onAbort)
    xhr.send(body)
  })
}
