import { describe, expect, it } from "vitest"

import {
  buildClassOptions,
  classColor,
  classNameError,
  colorResolver,
  filterClasses,
  unlistedNames,
} from "@/features/annotation/classes"
import { labelColor } from "@/features/datasets/label-colors"

const classes = [
  { name: "划痕", color: "#ef4444" },
  { name: "dent", color: "" },
]

describe("classNameError", () => {
  it("accepts valid names and rejects the rest", () => {
    expect(classNameError("凹坑", ["dent"])).toBeNull()
    expect(classNameError("", [])).toBe("请输入类别名称")
    expect(classNameError("a\tb", [])).toContain("控制字符")
    expect(classNameError("x".repeat(256), [])).toContain("255")
    expect(classNameError("dent", ["dent"])).toBe("类别“dent”已存在")
    // Names are case-sensitive, like the labels in VOC files.
    expect(classNameError("Dent", ["dent"])).toBeNull()
  })
})

describe("colors", () => {
  it("uses the set color, else a color from the name", () => {
    expect(classColor({ name: "x", color: "#ABCDEF" })).toBe("#abcdef")
    expect(classColor({ name: "x", color: "" })).toBe(labelColor("x"))
    expect(classColor({ name: "x", color: "red" })).toBe(labelColor("x"))
    const color = colorResolver(classes)
    expect(color("划痕")).toBe("#ef4444")
    expect(color("other")).toBe(labelColor("other"))
  })
})

describe("filterClasses", () => {
  it("matches case-insensitively, exact matches first", () => {
    const items = [{ name: "dent-big" }, { name: "Dent" }, { name: "scratch" }]
    expect(filterClasses(items, " dent ").map((c) => c.name)).toEqual([
      "Dent",
      "dent-big",
    ])
    expect(filterClasses(items, "")).toEqual(items)
    expect(filterClasses(items, "x")).toEqual([])
  })
})

describe("buildClassOptions", () => {
  const label = (name: string, object_count: number) => ({
    name,
    object_count,
  })

  it("puts the project classes first, then the labels by count, then the extra names", () => {
    const options = buildClassOptions({
      projectClasses: classes,
      datasetLabels: [
        label("hole", 3),
        label("dent", 50), // 在类别列表中：保持列表中的位置
        label("Crack", 7),
        label("burr", 7),
        label("划痕", 1),
      ],
      extraNames: ["新类别", "hole", "凹坑", "新类别"],
    })
    expect(options.map((option) => [option.name, option.source])).toEqual([
      ["划痕", "project"],
      ["dent", "project"],
      ["Crack", "dataset"], // 目标数相同时按名称（码点：大写字母在前）
      ["burr", "dataset"],
      ["hole", "dataset"],
      ["新类别", "dataset"],
      ["凹坑", "dataset"],
    ])
  })

  it("colors the project classes by the list, the rest by name", () => {
    const options = buildClassOptions({
      projectClasses: classes,
      datasetLabels: [label("hole", 1)],
      extraNames: ["凹坑"],
    })
    const color = colorResolver(classes)
    for (const option of options) {
      expect(option.color).toBe(color(option.name))
    }
    expect(options[0].color).toBe("#ef4444")
    expect(options[1].color).toBe(labelColor("dent"))
  })

  it("works with nothing loaded yet and skips empty names", () => {
    expect(
      buildClassOptions({ projectClasses: [], datasetLabels: [] })
    ).toEqual([])
    expect(
      buildClassOptions({
        projectClasses: [],
        datasetLabels: [label("", 2)],
        extraNames: ["", "dent"],
      }).map((option) => option.name)
    ).toEqual(["dent"])
  })

  it("keeps the order of the candidates shown before", () => {
    const first = buildClassOptions({
      projectClasses: [{ name: "划痕", color: "" }],
      datasetLabels: [label("hole", 2), label("burr", 1)],
    })
    expect(first.map((option) => option.name)).toEqual(["划痕", "hole", "burr"])
    // 保存后 burr 的目标数超过了 hole，又多了 dent，hole 不再有目标；有人把
    // burr 加入了类别列表。
    const next = buildClassOptions({
      projectClasses: [
        { name: "划痕", color: "" },
        { name: "burr", color: "" },
      ],
      datasetLabels: [label("burr", 5), label("dent", 1)],
      extraNames: ["新类别"],
      shown: [...unlistedNames(first), "凹坑"],
    })
    expect(next.map((option) => [option.name, option.source])).toEqual([
      ["划痕", "project"],
      ["burr", "project"],
      ["hole", "dataset"],
      ["凹坑", "dataset"],
      ["dent", "dataset"],
      ["新类别", "dataset"],
    ])
    expect(unlistedNames(next)).toEqual(["hole", "凹坑", "dent", "新类别"])
  })

  it("does not reorder its inputs", () => {
    const labels = [label("a", 1), label("b", 2)]
    buildClassOptions({ projectClasses: [], datasetLabels: labels })
    expect(labels.map((l) => l.name)).toEqual(["a", "b"])
  })
})
