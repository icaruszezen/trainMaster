import { describe, expect, it } from "vitest"

import {
  hashString,
  LABEL_PALETTE,
  labelColor,
  readableTextColor,
  relativeLuminance,
} from "@/features/datasets/label-colors"

describe("labelColor", () => {
  it("is deterministic", () => {
    expect(labelColor("scratch")).toBe(labelColor("scratch"))
    expect(labelColor("划痕")).toBe(labelColor("划痕"))
  })

  it("always returns a palette colour", () => {
    for (const name of ["", "a", "dent", "划痕", "😀", "x".repeat(200)]) {
      expect(LABEL_PALETTE).toContain(labelColor(name))
    }
  })

  it("spreads typical label names over the palette", () => {
    const names = Array.from({ length: 200 }, (_, i) => `label_${i}`)
    const used = new Set(names.map(labelColor))
    expect(used.size).toBe(LABEL_PALETTE.length)
  })

  it("uses a stable hash", () => {
    // FNV-1a 32 位的已知值，防止哈希被无意修改导致颜色整体变化。
    expect(hashString("")).toBe(0x811c9dc5)
    expect(hashString("a")).toBe(0xe40c292c)
  })

  it("uses well-formed hex colours without duplicates", () => {
    for (const color of LABEL_PALETTE) {
      expect(color).toMatch(/^#[0-9a-f]{6}$/)
    }
    expect(new Set(LABEL_PALETTE).size).toBe(LABEL_PALETTE.length)
  })
})

describe("readableTextColor", () => {
  it("picks the text colour with the higher contrast", () => {
    expect(readableTextColor("#ffffff")).toBe("#0a0a0a")
    expect(readableTextColor("#000000")).toBe("#ffffff")
    expect(readableTextColor("#eab308")).toBe("#0a0a0a")
    expect(readableTextColor("#6366f1")).toBe("#ffffff")
  })

  it("computes WCAG relative luminance", () => {
    expect(relativeLuminance("#000000")).toBe(0)
    expect(relativeLuminance("#ffffff")).toBeCloseTo(1)
  })
})
