import { describe, expect, it } from "vitest"

import { CLASS_LIST_MAX_COUNT } from "@/features/datasets/class-list"
import {
  addClass,
  arrangeByNames,
  labelsNotInList,
  mergeLabels,
  moveClass,
  removeClass,
  renameClass,
  setClassColor,
} from "@/features/projects/class-edit"

const classes = [
  { name: "划痕", color: "#ef4444" },
  { name: "dent", color: "" },
]

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

describe("setClassColor", () => {
  it("accepts hex colors only", () => {
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

describe("arrangeByNames", () => {
  const create = (name: string) => ({ name, color: "" })
  const list = [
    { name: "a", color: "#111111" },
    { name: "b", color: "" },
    { name: "c", color: "#333333" },
  ]

  it("puts the names first in their order, keeping the existing classes", () => {
    const result = arrangeByNames(list, ["c", "x", "a"], create)
    expect(result.classes).toEqual([
      { name: "c", color: "#333333" },
      { name: "x", color: "" },
      { name: "a", color: "#111111" },
      { name: "b", color: "" },
    ])
    expect(result.classes[0]).toBe(list[2]) // the same entry, not a copy
    expect(result).toMatchObject({ added: 1, rest: 1, dropped: 0 })
    expect(list.map((cls) => cls.name)).toEqual(["a", "b", "c"]) // unchanged
  })

  it("matches trimmed names and moves only the first of duplicates", () => {
    const rows = [
      { name: " b ", color: "", key: 1 },
      { name: "a", color: "", key: 2 },
      { name: "b", color: "", key: 3 },
    ]
    const result = arrangeByNames(rows, ["b", "b"], (name) => ({
      name,
      color: "",
      key: 9,
    }))
    expect(result.classes.map((row) => row.key)).toEqual([1, 2, 3])
    expect(result).toMatchObject({ added: 0, rest: 2 })
  })

  it("keeps the order when the names are the list", () => {
    const result = arrangeByNames(list, ["a", "b", "c"], create)
    expect(result.classes).toEqual(list)
    expect(result).toMatchObject({ added: 0, rest: 0, dropped: 0 })
  })

  it("drops the last classes beyond the limit", () => {
    const names = Array.from(
      { length: CLASS_LIST_MAX_COUNT - 1 },
      (_, i) => `n${i}`
    )
    const result = arrangeByNames(list, names, create)
    expect(result.classes).toHaveLength(CLASS_LIST_MAX_COUNT)
    expect(result.classes.at(-1)).toBe(list[0])
    expect(result).toMatchObject({
      added: CLASS_LIST_MAX_COUNT - 1,
      rest: 1,
      dropped: 2,
    })
  })
})

describe("labelsNotInList", () => {
  it("lists the labels missing from the list in the order they are merged", () => {
    const labels = [
      { name: "dent", object_count: 9 },
      { name: "b", object_count: 2 },
      { name: "a", object_count: 2 },
      { name: "c", object_count: 5 },
    ]
    const missing = labelsNotInList(classes, labels)
    expect(missing.map((label) => label.name)).toEqual(["c", "a", "b"])
    expect(missing[0]).toBe(labels[3])
    expect(mergeLabels(classes, labels).classes.slice(classes.length)).toEqual(
      missing.map((label) => ({ name: label.name, color: "" }))
    )
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
