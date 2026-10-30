import { describe, expect, it } from "vitest"

import {
  archiveBaseName,
  characterCount,
  isNameTaken,
  suggestDatasetName,
  validateDatasetName,
  validateDescription,
} from "@/features/datasets/naming"

describe("validateDatasetName", () => {
  it("accepts ordinary names", () => {
    for (const name of [
      "划痕-A",
      "batch_2026.10",
      "OK NG 样本",
      "a",
      "x".repeat(64),
      "  前后有空格  ",
      "console",
      "com10",
    ]) {
      expect(validateDatasetName(name), name).toBeNull()
    }
  })

  it("rejects empty names", () => {
    expect(validateDatasetName("")).toBe("名称不能为空")
    expect(validateDatasetName("   ")).toBe("名称不能为空")
  })

  it("limits the length in characters and bytes", () => {
    expect(validateDatasetName("x".repeat(65))).toBe("名称不能超过 64 个字符")
    // 64 个汉字 = 192 字节，未超过 200 字节。
    expect(validateDatasetName("划".repeat(64))).toBeNull()
    // 表情符号 4 字节：60 个 = 240 字节，字符数却只有 60。
    expect(validateDatasetName("😀".repeat(60))).toMatch(/名称过长/)
  })

  it("rejects forbidden and control characters", () => {
    for (const name of [
      "a/b",
      "a\\b",
      "a:b",
      "a*b",
      "a?b",
      'a"b',
      "a<b",
      "a>b",
      "a|b",
      "a\tb",
      "a\u0000b",
      "a\u007fb",
    ]) {
      expect(validateDatasetName(name), JSON.stringify(name)).toBe(
        '名称不能包含 \\ / : * ? " < > | 等字符'
      )
    }
  })

  it("rejects leading and trailing dots", () => {
    expect(validateDatasetName(".hidden")).toBe("名称不能以“.”开头")
    expect(validateDatasetName("..")).toBe("名称不能以“.”开头")
    expect(validateDatasetName("name.")).toBe("名称不能以“.”或空格结尾")
    expect(validateDatasetName("name. ")).toBe("名称不能以“.”或空格结尾")
  })

  it("rejects Windows reserved device names", () => {
    for (const name of ["CON", "con", "Prn", "aux", "NUL", "com1", "LPT9"]) {
      expect(validateDatasetName(name), name).toBe("该名称是系统保留名称")
    }
    expect(validateDatasetName("con.txt")).toBe("该名称是系统保留名称")
    expect(validateDatasetName("nul.tar.gz")).toBe("该名称是系统保留名称")
  })

  it("treats spaces before the extension like the backend does", () => {
    // Windows 忽略扩展名前的空格（后端 naming_test.go 断言 "LPT9 .zip"）。
    for (const name of ["LPT9 .zip", "CON .txt", "aux  .tar.gz", "Nul .v2"]) {
      expect(validateDatasetName(name), name).toBe("该名称是系统保留名称")
    }
    for (const name of ["console.txt", "con-1", "aux_ .zip", "com10 .zip"]) {
      expect(validateDatasetName(name), name).toBeNull()
    }
  })
})

describe("validateDescription", () => {
  it("limits descriptions to 500 characters", () => {
    expect(validateDescription("")).toBeNull()
    expect(validateDescription("描".repeat(500))).toBeNull()
    expect(validateDescription("描".repeat(501))).toBe(
      "描述不能超过 500 个字符"
    )
  })
})

describe("characterCount", () => {
  it("counts code points, not UTF-16 units", () => {
    expect(characterCount("abc")).toBe(3)
    expect(characterCount("😀😀")).toBe(2)
  })
})

describe("archiveBaseName", () => {
  it("strips compound archive extensions", () => {
    expect(archiveBaseName("批次1.tar.gz")).toBe("批次1")
    expect(archiveBaseName("a.TAR.BZ2")).toBe("a")
    expect(archiveBaseName("a.tar.xz")).toBe("a")
    expect(archiveBaseName("a.tar.zst")).toBe("a")
    expect(archiveBaseName("a.tgz")).toBe("a")
    expect(archiveBaseName("a.tbz2")).toBe("a")
    expect(archiveBaseName("a.txz")).toBe("a")
    expect(archiveBaseName("a.tzst")).toBe("a")
  })

  it("strips only the final simple extension", () => {
    expect(archiveBaseName("data.v2.zip")).toBe("data.v2")
    expect(archiveBaseName("images.7z")).toBe("images")
    expect(archiveBaseName("images.RAR")).toBe("images")
    expect(archiveBaseName("plain.tar")).toBe("plain")
  })

  it("keeps names without an extension", () => {
    expect(archiveBaseName("README")).toBe("README")
    expect(archiveBaseName(".zip")).toBe(".zip")
    expect(archiveBaseName(".tar.gz")).toBe(".tar")
  })
})

describe("suggestDatasetName", () => {
  it("replaces forbidden characters and trims dots and spaces", () => {
    expect(suggestDatasetName("a:b*c")).toBe("a_b_c")
    expect(suggestDatasetName("..hidden")).toBe("hidden")
    expect(suggestDatasetName("name. . ")).toBe("name")
    expect(suggestDatasetName("  批次 1  ")).toBe("批次 1")
  })

  it("truncates to the length limits", () => {
    expect(suggestDatasetName("x".repeat(80))).toBe("x".repeat(64))
    const emoji = suggestDatasetName("😀".repeat(64))
    expect(new TextEncoder().encode(emoji).length).toBeLessThanOrEqual(200)
    expect(validateDatasetName(emoji)).toBeNull()
  })

  it("produces names that pass validation for typical sources", () => {
    for (const source of ["2026/10/30", "OK|NG", ".DS", "图片 (1)."]) {
      const name = suggestDatasetName(source)
      expect(validateDatasetName(name), source).toBeNull()
    }
  })
})

describe("isNameTaken", () => {
  it("compares case-insensitively after trimming", () => {
    expect(isNameTaken("Batch", ["batch"])).toBe(true)
    expect(isNameTaken(" 划痕 ", ["划痕"])).toBe(true)
    expect(isNameTaken("划痕-B", ["划痕"])).toBe(false)
  })
})
