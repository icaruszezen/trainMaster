import { describe, expect, it } from "vitest"

import {
  PROJECT_NAME_MAX_LENGTH,
  validateProjectName,
} from "@/features/projects/naming"

describe("validateProjectName", () => {
  it("accepts names that would not be valid folder names", () => {
    for (const name of [
      "A 线外观检测",
      "a/b:c*?",
      ".hidden.",
      "CON",
      "  trimmed  ",
    ]) {
      expect(validateProjectName(name), name).toBeNull()
    }
  })

  it("rejects empty names", () => {
    expect(validateProjectName("")).toBe("名称不能为空")
    expect(validateProjectName(" \t ")).toBe("名称不能为空")
  })

  it("counts code points like the server", () => {
    expect(validateProjectName("😀".repeat(PROJECT_NAME_MAX_LENGTH))).toBeNull()
    expect(
      validateProjectName("项".repeat(PROJECT_NAME_MAX_LENGTH + 1))
    ).toMatch(/不能超过 64 个字符/)
  })

  it("rejects control characters", () => {
    expect(validateProjectName("a\nb")).toMatch(/控制字符/)
    expect(validateProjectName("a\u007fb")).toMatch(/控制字符/)
  })
})
