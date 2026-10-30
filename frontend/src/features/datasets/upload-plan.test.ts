import { describe, expect, it } from "vitest"

import {
  ARCHIVE_CHUNK_BYTES,
  etaSeconds,
  FOLDER_BATCH_MAX_BYTES,
  FOLDER_BATCH_MAX_FILES,
  nextChunk,
  planBatches,
  retryDelayMs,
  smoothSpeed,
} from "@/features/datasets/upload-plan"

const MiB = 1024 * 1024
const files = (...sizes: number[]) =>
  sizes.map((size, index) => ({ path: `f${index}`, size }))
const shape = (batches: { path: string }[][]) =>
  batches.map((batch) => batch.map((file) => file.path))

describe("planBatches", () => {
  it("returns no batches for no files", () => {
    expect(planBatches([])).toEqual([])
  })

  it("keeps small folders in one batch", () => {
    expect(shape(planBatches(files(10, 20, 30)))).toEqual([["f0", "f1", "f2"]])
  })

  it("splits by file count", () => {
    const many = files(...Array.from({ length: 130 }, () => 1))
    const batches = planBatches(many)
    expect(batches.map((b) => b.length)).toEqual([64, 64, 2])
    expect(FOLDER_BATCH_MAX_FILES).toBe(64)
  })

  it("splits by size without exceeding the byte limit", () => {
    const batches = planBatches(files(20 * MiB, 10 * MiB, 5 * MiB, 30 * MiB))
    expect(shape(batches)).toEqual([["f0", "f1"], ["f2"], ["f3"]])
    for (const batch of batches) {
      const bytes = batch.reduce((sum, f) => sum + f.size, 0)
      expect(bytes).toBeLessThanOrEqual(FOLDER_BATCH_MAX_BYTES)
    }
  })

  it("puts an oversized file into a batch of its own", () => {
    expect(shape(planBatches(files(1, 100 * MiB, 1)))).toEqual([
      ["f0"],
      ["f1"],
      ["f2"],
    ])
  })

  it("fills a batch exactly up to the limits", () => {
    expect(
      shape(planBatches(files(16, 16, 1), { maxBytes: 32, maxFiles: 10 }))
    ).toEqual([["f0", "f1"], ["f2"]])
    expect(
      shape(planBatches(files(1, 1, 1), { maxBytes: 100, maxFiles: 2 }))
    ).toEqual([["f0", "f1"], ["f2"]])
  })

  it("preserves order and covers every file exactly once", () => {
    const input = files(...Array.from({ length: 500 }, (_, i) => (i % 7) * MiB))
    const flattened = planBatches(input).flat()
    expect(flattened).toEqual(input)
  })

  it("is deterministic, so retries map batch indices to the same files", () => {
    const input = files(...Array.from({ length: 200 }, (_, i) => i * 1000))
    expect(shape(planBatches(input))).toEqual(shape(planBatches(input)))
  })
})

describe("nextChunk", () => {
  it("returns consecutive chunks clamped to the file size", () => {
    const size = ARCHIVE_CHUNK_BYTES * 2 + 5
    expect(nextChunk(0, size)).toEqual({ start: 0, end: ARCHIVE_CHUNK_BYTES })
    expect(nextChunk(ARCHIVE_CHUNK_BYTES * 2, size)).toEqual({
      start: ARCHIVE_CHUNK_BYTES * 2,
      end: size,
    })
  })

  it("uses 8 MiB chunks, well below the 64 MiB server limit", () => {
    expect(ARCHIVE_CHUNK_BYTES).toBe(8 * MiB)
  })
})

describe("retryDelayMs", () => {
  it("backs off exponentially up to 8 seconds", () => {
    expect([1, 2, 3, 4, 5].map(retryDelayMs)).toEqual([
      1000, 2000, 4000, 8000, 8000,
    ])
  })
})

describe("smoothSpeed", () => {
  it("starts with the first sample", () => {
    expect(smoothSpeed(0, 1000, 500)).toBe(2000)
  })

  it("moves towards new samples gradually", () => {
    expect(smoothSpeed(1000, 3000, 1000)).toBeCloseTo(1600)
  })

  it("ignores empty intervals", () => {
    expect(smoothSpeed(1234, 100, 0)).toBe(1234)
  })
})

describe("etaSeconds", () => {
  it("divides the remaining bytes by the speed", () => {
    expect(etaSeconds(1000, 100)).toBe(10)
  })

  it("is unknown without a speed", () => {
    expect(etaSeconds(1000, 0)).toBeNull()
  })
})
