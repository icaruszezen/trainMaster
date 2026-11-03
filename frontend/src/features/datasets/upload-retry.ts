/**
 * 上传请求的重试：数据集导入与模型上传的管理器共用。暂时性错误按
 * upload-plan.ts 中的节奏重试，取消（AbortSignal）时立即结束。
 */
import { MAX_RETRIES, retryDelayMs } from "@/features/datasets/upload-plan"
import { ApiError } from "@/lib/api"

/** 值得重试的错误：网络中断、超时、限流与网关 / 服务端的暂时性错误。 */
export function isTransientError(error: unknown) {
  if (!(error instanceof ApiError)) {
    return false
  }
  const { status } = error
  return (
    status === 0 ||
    status === 408 ||
    status === 429 ||
    (status >= 500 && status !== 501 && status !== 507)
  )
}

/** 等待 `ms` 毫秒；取消时以 `AbortError` 拒绝。 */
export function sleep(ms: number, signal: AbortSignal) {
  return new Promise<void>((resolve, reject) => {
    if (signal.aborted) {
      reject(new DOMException("上传已取消", "AbortError"))
      return
    }
    const timer = setTimeout(() => {
      signal.removeEventListener("abort", onAbort)
      resolve()
    }, ms)
    const onAbort = () => {
      clearTimeout(timer)
      reject(new DOMException("上传已取消", "AbortError"))
    }
    signal.addEventListener("abort", onAbort, { once: true })
  })
}

/** 执行请求，暂时性错误最多重试 MAX_RETRIES 次。 */
export async function withRetry<T>(
  request: () => Promise<T>,
  signal: AbortSignal
): Promise<T> {
  for (let attempt = 1; ; attempt += 1) {
    try {
      return await request()
    } catch (error) {
      if (signal.aborted || attempt > MAX_RETRIES || !isTransientError(error)) {
        throw error
      }
      await sleep(retryDelayMs(attempt), signal)
    }
  }
}
