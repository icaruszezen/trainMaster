import { describe, expect, it } from "vitest"

import type { AugmentMethod } from "@/features/data-versions/api"
import {
  AUGMENT_METHODS,
  augmentMethodsText,
  copiesLimit,
  maxCopies,
  normalizeMethods,
} from "@/features/data-versions/augmentations"

describe("AUGMENT_METHODS", () => {
  it("lists the six backend methods in display order", () => {
    expect(AUGMENT_METHODS.map((m) => m.id)).toEqual([
      "hflip",
      "vflip",
      "rotate90",
      "brightness_contrast",
      "blur",
      "noise",
    ])
    expect(AUGMENT_METHODS.filter((m) => m.geometric).map((m) => m.id)).toEqual(
      ["hflip", "vflip", "rotate90"]
    )
  })

  it("describes the risks", () => {
    const text = (id: AugmentMethod) =>
      AUGMENT_METHODS.find((m) => m.id === id)?.description ?? ""
    expect(text("hflip")).toContain("装反")
    expect(text("blur")).toContain("8 像素")
    for (const method of AUGMENT_METHODS) {
      expect(method.label).not.toBe("")
      expect(method.description.length).toBeGreaterThan(10)
    }
  })
})

describe("maxCopies", () => {
  it.each<[AugmentMethod[], number]>([
    [[], 0],
    [["hflip"], 1],
    [["vflip"], 1],
    [["hflip", "vflip"], 3],
    [["rotate90"], 3],
    [["rotate90", "hflip"], 7],
    [["rotate90", "vflip"], 7],
    [["hflip", "vflip", "rotate90"], 7],
    [["brightness_contrast"], 5],
    [["hflip", "noise"], 5],
    [["blur", "noise", "rotate90"], 5],
  ])("%j → %d", (methods, expected) => {
    expect(maxCopies(methods)).toBe(expected)
  })

  it("caps the copies of the form at 5", () => {
    expect(copiesLimit(["rotate90", "hflip"])).toBe(5)
    expect(copiesLimit(["hflip", "vflip"])).toBe(3)
    expect(copiesLimit([])).toBe(0)
  })
})

describe("normalizeMethods", () => {
  it("removes duplicates and sorts in display order", () => {
    expect(normalizeMethods(["noise", "hflip", "noise", "rotate90"])).toEqual([
      "hflip",
      "rotate90",
      "noise",
    ])
  })

  it("names the methods", () => {
    expect(augmentMethodsText(["hflip", "brightness_contrast"])).toBe(
      "水平翻转、亮度 / 对比度"
    )
  })
})
