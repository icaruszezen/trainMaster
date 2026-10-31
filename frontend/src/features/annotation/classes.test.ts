import { describe, expect, it } from "vitest"

import {
  addClass,
  appendClass,
  classColor,
  classNameError,
  colorResolver,
  filterClasses,
  mergeLabels,
  moveClass,
  removeClass,
  renameClass,
  setClassColor,
} from "@/features/annotation/classes"
import { CLASS_LIST_MAX_COUNT } from "@/features/datasets/class-list"
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

describe("addClass and renameClass", () => {
  it("trims and appends", () => {
    expect(addClass(classes, "  凹坑 ")).toEqual({
      classes: [...classes, { name: "凹坑", color: "" }],
      error: null,
    })
    expect(addClass(classes, "dent").error).toBe("类别“dent”已存在")
  })

  it("limits the number of classes", () => {
    const full = Array.from({ length: CLASS_LIST_MAX_COUNT }, (_, i) => ({
      name: `c${i}`,
      color: "",
    }))
    expect(addClass(full, "new").error).toContain(String(CLASS_LIST_MAX_COUNT))
  })

  it("renames, allowing the class's own name", () => {
    expect(renameClass(classes, 1, " dent ").classes).toEqual(classes)
    expect(renameClass(classes, 1, "划痕").error).toBe("类别“划痕”已存在")
    expect(renameClass(classes, 1, "凹坑").classes?.[1]).toEqual({
      name: "凹坑",
      color: "",
    })
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

  it("setClassColor accepts hex colors only", () => {
    expect(setClassColor(classes, 1, "#00FF00")[1].color).toBe("#00ff00")
    expect(setClassColor(classes, 0, "nope")[0].color).toBe("")
  })
})

describe("moveClass and removeClass", () => {
  it("reorders within bounds", () => {
    expect(moveClass(["a", "b", "c"], 0, 2)).toEqual(["b", "c", "a"])
    expect(moveClass(["a", "b", "c"], 2, 0)).toEqual(["c", "a", "b"])
    expect(moveClass(["a", "b"], 0, 5)).toEqual(["a", "b"])
    expect(removeClass(["a", "b", "c"], 1)).toEqual(["a", "c"])
  })
})

describe("mergeLabels", () => {
  it("appends labels missing from the list by count, then name", () => {
    expect(
      mergeLabels(classes, [
        { name: "dent", object_count: 9 },
        { name: "b", object_count: 2 },
        { name: "a", object_count: 2 },
        { name: "c", object_count: 5 },
      ])
    ).toEqual({
      classes: [
        ...classes,
        { name: "c", color: "" },
        { name: "a", color: "" },
        { name: "b", color: "" },
      ],
      added: 3,
    })
  })
})

describe("mergeLabels with an order", () => {
  it("puts the classes of the order first, even without objects", () => {
    expect(
      mergeLabels(
        [{ name: "b", color: "#000000" }],
        [
          { name: "x", object_count: 9 },
          { name: "a", object_count: 1 },
        ],
        ["a", "b", "c"]
      ).classes.map((cls) => cls.name)
    ).toEqual(["b", "a", "c", "x"])
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

describe("appendClass", () => {
  it("adds a class at the end, keeping the other fields", () => {
    const list = { items: classes, total: classes.length, version: "v1" }
    const next = appendClass(list, "crack")
    expect(next.items.map((cls) => cls.name)).toEqual([
      ...classes.map((cls) => cls.name),
      "crack",
    ])
    expect(next.items.at(-1)).toEqual({ name: "crack", color: "" })
    expect(next.total).toBe(classes.length + 1)
    expect(next.version).toBe("v1")
    expect(list.items).toBe(classes) // not changed in place
  })

  it("returns the same list when the class exists", () => {
    const list = { items: classes, total: classes.length, version: "v1" }
    expect(appendClass(list, "dent")).toBe(list)
  })
})
