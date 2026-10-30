import { describe, expect, it } from "vitest"

import {
  formatBytes,
  formatDateTime,
  formatDuration,
  formatNumber,
  formatPercent,
  formatRelativeTime,
  percentOf,
} from "@/lib/format"

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

describe("formatNumber", () => {
  it("groups thousands", () => {
    expect(formatNumber(0)).toBe("0")
    expect(formatNumber(1234)).toBe("1,234")
    expect(formatNumber(1234567)).toBe("1,234,567")
  })

  it("returns a dash for invalid input", () => {
    expect(formatNumber(Number.NaN)).toBe("—")
    expect(formatNumber(Number.POSITIVE_INFINITY)).toBe("—")
  })
})

describe("formatPercent", () => {
  it("rounds to the requested precision", () => {
    expect(formatPercent(82.4)).toBe("82%")
    expect(formatPercent(82.46, 1)).toBe("82.5%")
    expect(formatPercent(100)).toBe("100%")
    expect(formatPercent(50, 1)).toBe("50%")
  })

  it("returns a dash for invalid input", () => {
    expect(formatPercent(Number.NaN)).toBe("—")
  })
})

describe("formatRelativeTime", () => {
  const now = new Date(2026, 9, 30, 12, 0, 0).getTime()
  const ago = (ms: number) => new Date(now - ms).toISOString()

  it("describes recent times", () => {
    expect(formatRelativeTime(ago(10_000), now)).toBe("刚刚")
    expect(formatRelativeTime(ago(3 * 60_000), now)).toBe("3 分钟前")
    expect(formatRelativeTime(ago(59 * 60_000), now)).toBe("59 分钟前")
    expect(formatRelativeTime(ago(2 * 3_600_000), now)).toBe("2 小时前")
  })

  it("describes older times", () => {
    const day = 86_400_000
    expect(formatRelativeTime(ago(day), now)).toBe("1 天前")
    expect(formatRelativeTime(ago(29 * day), now)).toBe("29 天前")
    expect(formatRelativeTime(ago(65 * day), now)).toBe("2 个月前")
    expect(formatRelativeTime(ago(800 * day), now)).toBe("2 年前")
  })

  it("treats future times as now and rejects invalid input", () => {
    expect(formatRelativeTime(ago(-60_000), now)).toBe("刚刚")
    expect(formatRelativeTime("not a date", now)).toBe("—")
    expect(formatRelativeTime(null, now)).toBe("—")
  })
})

describe("formatDateTime", () => {
  it("formats local time with zero padding", () => {
    const date = new Date(2026, 0, 5, 8, 3, 9)
    expect(formatDateTime(date)).toBe("2026-01-05 08:03")
    expect(formatDateTime(date.toISOString(), { withSeconds: true })).toBe(
      "2026-01-05 08:03:09"
    )
  })

  it("returns a dash for missing or invalid input", () => {
    expect(formatDateTime(null)).toBe("—")
    expect(formatDateTime("nope")).toBe("—")
  })
})
