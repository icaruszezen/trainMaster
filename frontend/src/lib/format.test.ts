import { describe, expect, it } from "vitest"

import { formatBytes, formatDuration, percentOf } from "@/lib/format"

describe("formatBytes", () => {
  it("formats with binary units", () => {
    expect(formatBytes(0)).toBe("0 B")
    expect(formatBytes(512)).toBe("512 B")
    expect(formatBytes(1024)).toBe("1 KB")
    expect(formatBytes(1536)).toBe("1.5 KB")
    expect(formatBytes(5 * 1024 ** 3)).toBe("5 GB")
    expect(formatBytes(1.25 * 1024 ** 4, 2)).toBe("1.25 TB")
  })

  it("carries over when rounding reaches the next unit", () => {
    expect(formatBytes(1024 * 1024 - 1)).toBe("1 MB")
  })

  it("caps at the largest unit", () => {
    expect(formatBytes(2048 * 1024 ** 5)).toBe("2048 PB")
  })

  it("returns a dash for invalid input", () => {
    expect(formatBytes(-1)).toBe("—")
    expect(formatBytes(Number.NaN)).toBe("—")
  })
})

describe("percentOf", () => {
  it("computes a clamped percentage", () => {
    expect(percentOf(600, 1000)).toBe(60)
    expect(percentOf(2000, 1000)).toBe(100)
    expect(percentOf(-5, 1000)).toBe(0)
  })

  it("returns 0 when the total is unknown", () => {
    expect(percentOf(10, 0)).toBe(0)
  })
})

describe("formatDuration", () => {
  it("keeps the two largest units", () => {
    expect(formatDuration(0)).toBe("0 秒")
    expect(formatDuration(45)).toBe("45 秒")
    expect(formatDuration(125)).toBe("2 分钟 5 秒")
    expect(formatDuration(3 * 86400 + 2 * 3600 + 59)).toBe("3 天 2 小时")
    expect(formatDuration(86400 + 30)).toBe("1 天")
  })

  it("returns a dash for invalid input", () => {
    expect(formatDuration(-1)).toBe("—")
  })
})
