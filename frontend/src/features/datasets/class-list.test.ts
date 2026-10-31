import { describe, expect, it } from "vitest"

import type { DatasetImport } from "@/features/datasets/api"
import {
  CLASS_FILE_MAX_BYTES,
  CLASS_LIST_REQUIRED_MESSAGE,
  compareClassesWithLabels,
  decodeClassFile,
  defaultClassOrder,
  lastYoloImport,
  parseClassList,
  readClassFile,
  toClassListText,
} from "@/features/datasets/class-list"

describe("parseClassList", () => {
  it("splits lines and trims each name", () => {
    expect(parseClassList("scratch\n  dent \r\ncrack\rhole")).toEqual({
      classes: ["scratch", "dent", "crack", "hole"],
      error: null,
    })
  })

  it("ignores leading and trailing blank lines and a BOM", () => {
    expect(parseClassList("\uFEFF\n  \nscratch\ndent\n\n \n")).toEqual({
      classes: ["scratch", "dent"],
      error: null,
    })
  })

  it("requires at least one class", () => {
    for (const text of ["", "  ", "\n\n", "\uFEFF"]) {
      expect(parseClassList(text)).toEqual({
        classes: [],
        error: CLASS_LIST_REQUIRED_MESSAGE,
      })
    }
  })

  it("rejects blank lines in the middle, reporting the original line", () => {
    expect(parseClassList("\nscratch\n\ndent").error).toBe(
      "第 3 行为空：类别编号按行计算，请删除空行或填写类别名称"
    )
    expect(parseClassList("a\n   \nb").error).toBe(
      "第 2 行为空：类别编号按行计算，请删除空行或填写类别名称"
    )
  })

  it("rejects duplicates case-sensitively", () => {
    expect(parseClassList("dog\ncat\ndog").error).toBe(
      "类别“dog”重复（第 1 行和第 3 行）"
    )
    expect(parseClassList("Dog\ndog").error).toBeNull()
  })

  it("limits the name length in code points", () => {
    expect(parseClassList("划".repeat(255)).error).toBeNull()
    expect(parseClassList("😀".repeat(255)).error).toBeNull()
    expect(parseClassList(`a\n${"x".repeat(256)}`).error).toBe(
      "第 2 行的类别名称不能超过 255 个字符"
    )
  })

  it("rejects control characters inside a name", () => {
    expect(parseClassList("a\tb").error).toBe(
      "第 1 行的类别名称包含制表符或控制字符"
    )
    expect(parseClassList("ok\nbad\u0007").error).toBe(
      "第 2 行的类别名称包含制表符或控制字符"
    )
  })

  it("allows up to 1000 classes", () => {
    const names = (count: number) =>
      Array.from({ length: count }, (_, i) => `c${i}`).join("\n")
    expect(parseClassList(names(1000)).classes).toHaveLength(1000)
    expect(parseClassList(names(1001))).toEqual({
      classes: [],
      error: "类别数量不能超过 1000 个",
    })
  })

  it("round-trips through toClassListText", () => {
    const classes = ["划痕", "凹坑", "scratch"]
    expect(toClassListText(classes)).toBe("划痕\n凹坑\nscratch")
    expect(parseClassList(toClassListText(classes)).classes).toEqual(classes)
  })
})

describe("decodeClassFile", () => {
  it("decodes UTF-8 and strips the BOM", () => {
    const bytes = new Uint8Array([
      0xef,
      0xbb,
      0xbf,
      ...new TextEncoder().encode("划痕\r\n凹坑\r\n"),
    ])
    expect(decodeClassFile(bytes)).toBe("划痕\n凹坑\n")
  })

  it("falls back to GB18030 for invalid UTF-8", () => {
    // “划痕\n凹坑”的 GBK 编码
    const bytes = new Uint8Array([
      0xbb, 0xae, 0xba, 0xdb, 0x0a, 0xb0, 0xbc, 0xbf, 0xd3,
    ])
    expect(decodeClassFile(bytes)).toBe("划痕\n凹坑")
  })
})

describe("readClassFile", () => {
  it("reads and decodes a file", async () => {
    await expect(readClassFile(new Blob(["a\r\nb"]))).resolves.toBe("a\nb")
  })

  it("rejects files that are too large", async () => {
    const big = new Blob([new Uint8Array(CLASS_FILE_MAX_BYTES + 1)])
    await expect(readClassFile(big)).rejects.toThrow("类别文件不能超过 2 MB")
  })
})

describe("defaultClassOrder", () => {
  it("sorts label names by code point", () => {
    expect(
      defaultClassOrder([{ name: "dent" }, { name: "Crack" }, { name: "划痕" }])
    ).toEqual(["Crack", "dent", "划痕"])
  })

  it("keeps a base order and appends the other labels", () => {
    expect(
      defaultClassOrder(
        [{ name: "dent" }, { name: "Crack" }, { name: "划痕" }],
        ["划痕", "hole", "dent"]
      )
    ).toEqual(["划痕", "hole", "dent", "Crack"])
  })
})

describe("lastYoloImport", () => {
  it("finds the newest successful YOLO import with classes", () => {
    const imports: Pick<
      DatasetImport,
      "id" | "annotation_format" | "status" | "classes"
    >[] = [
      { id: 4, annotation_format: "yolo", status: "failed", classes: ["x"] },
      { id: 3, annotation_format: "voc", status: "succeeded", classes: [] },
      {
        id: 2,
        annotation_format: "yolo",
        status: "succeeded",
        classes: ["a", "b"],
      },
      {
        id: 1,
        annotation_format: "yolo",
        status: "succeeded",
        classes: ["b", "a"],
      },
    ]
    expect(lastYoloImport(imports)?.id).toBe(2)
    expect(lastYoloImport(imports.slice(0, 2))).toBeUndefined()
  })
})

describe("compareClassesWithLabels", () => {
  it("finds labels that will not be exported and unused classes", () => {
    const labels = [
      { name: "scratch", object_count: 10, image_count: 4 },
      { name: "dent", object_count: 3, image_count: 2 },
      { name: "Hole", object_count: 1, image_count: 1 },
    ]
    expect(
      compareClassesWithLabels(["scratch", "hole", "crack"], labels)
    ).toEqual({
      missing: [labels[1], labels[2]],
      unused: ["hole", "crack"],
    })
    expect(
      compareClassesWithLabels(["scratch", "dent", "Hole"], labels)
    ).toEqual({ missing: [], unused: [] })
  })
})
