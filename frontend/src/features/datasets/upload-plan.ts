/** 上传的分块 / 分批策略与重试节奏（纯函数，便于测试）。 */

const MiB = 1024 * 1024

/** 压缩包每个分块 8 MiB（服务端上限 64 MiB），失败重传的代价小。 */
export const ARCHIVE_CHUNK_BYTES = 8 * MiB
/** 文件夹每批最多 64 个文件或 32 MiB（服务端单次请求上限 256 MiB）。 */
export const FOLDER_BATCH_MAX_FILES = 64
export const FOLDER_BATCH_MAX_BYTES = 32 * MiB
/** 同时进行的文件夹批量请求数。 */
export const FOLDER_CONCURRENCY = 3
/** 单个分块 / 批次在暂时性错误后最多重试的次数。 */
export const MAX_RETRIES = 3

export type BatchLimits = { maxFiles: number; maxBytes: number }

/**
 * 按顺序把文件分成批次：一批达到文件数上限，或再加一个文件就会超过字节上限时
 * 另起一批。单个文件超过字节上限时独占一批。
 */
export function planBatches<T extends { size: number }>(
  files: readonly T[],
  {
    maxFiles = FOLDER_BATCH_MAX_FILES,
    maxBytes = FOLDER_BATCH_MAX_BYTES,
  }: Partial<BatchLimits> = {}
): T[][] {
  const batches: T[][] = []
  let current: T[] = []
  let currentBytes = 0

  for (const file of files) {
    if (
      current.length > 0 &&
      (current.length >= maxFiles || currentBytes + file.size > maxBytes)
    ) {
      batches.push(current)
      current = []
      currentBytes = 0
    }
    current.push(file)
    currentBytes += file.size
  }
  if (current.length > 0) {
    batches.push(current)
  }
  return batches
}

/** 从 `offset` 开始的下一个分块 `[start, end)`。 */
export function nextChunk(
  offset: number,
  size: number,
  chunkBytes = ARCHIVE_CHUNK_BYTES
) {
  return { start: offset, end: Math.min(size, offset + chunkBytes) }
}

/** 第 `attempt` 次重试（从 1 开始）前的等待时间：1 s、2 s、4 s…，最多 8 s。 */
export function retryDelayMs(attempt: number) {
  return Math.min(8_000, 1_000 * 2 ** Math.max(0, attempt - 1))
}

/**
 * 平滑后的上传速度（字节/秒）：指数移动平均，避免数字跳动。
 * `previous` 为 0 表示还没有速度，直接取本次采样。
 */
export function smoothSpeed(
  previous: number,
  sampleBytes: number,
  sampleMs: number
) {
  if (sampleMs <= 0) {
    return previous
  }
  const current = (Math.max(0, sampleBytes) / sampleMs) * 1000
  return previous > 0 ? previous * 0.7 + current * 0.3 : current
}

/** 剩余时间（秒）；速度未知时返回 null。 */
export function etaSeconds(remainingBytes: number, speed: number) {
  if (!(speed > 0) || remainingBytes < 0) {
    return null
  }
  return remainingBytes / speed
}
