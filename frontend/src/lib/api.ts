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

/** 再请求一次结果也不会变的错误：4xx 与 501（功能尚未实现）。 */
export function isPermanentError(error: unknown) {
  return (
    error instanceof ApiError &&
    ((error.status >= 400 && error.status < 500) || error.status === 501)
  )
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

/** GET 一个 JSON 资源，例如 `apiGet<SystemInfo>("/api/v1/system/info")`。 */
export async function apiGet<T>(path: string, init?: RequestInit): Promise<T> {
  const headers = new Headers(init?.headers)
  if (!headers.has("Accept")) {
    headers.set("Accept", "application/json")
  }

  let response: Response
  try {
    response = await fetch(path, { ...init, method: "GET", headers })
  } catch (error) {
    // 主动取消（如 React Query 卸载组件）保持原样抛出。
    if (init?.signal?.aborted) {
      throw error
    }
    throw new ApiError(0, "network_error", "无法连接到服务器")
  }

  if (!response.ok) {
    throw await parseErrorResponse(response)
  }

  try {
    return (await response.json()) as T
  } catch {
    throw new ApiError(
      response.status,
      "invalid_response",
      "服务器返回了无法解析的数据"
    )
  }
}
