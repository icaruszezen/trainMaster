import { describe, expect, it } from "vitest"

import { trayTitle } from "@/features/datasets/upload-tray"

const idle = {
  uploading: 0,
  running: 0,
  waiting: 0,
  failedImports: 0,
  failedModels: 0,
  hasImports: false,
  hasModels: false,
}

describe("trayTitle", () => {
  it("puts running work first", () => {
    expect(trayTitle({ ...idle, uploading: 2, running: 1, waiting: 1 })).toBe(
      "正在上传 2 项"
    )
    expect(trayTitle({ ...idle, running: 1, failedModels: 1 })).toBe(
      "正在处理 1 项"
    )
  })

  it("asks to confirm the YOLO version before reporting failures", () => {
    expect(trayTitle({ ...idle, waiting: 1, failedImports: 2 })).toBe(
      "1 个模型需要确认版本"
    )
  })

  it("names the kind of failure", () => {
    expect(trayTitle({ ...idle, failedImports: 2, hasImports: true })).toBe(
      "2 项导入失败"
    )
    expect(trayTitle({ ...idle, failedModels: 1, hasModels: true })).toBe(
      "1 项上传失败"
    )
    expect(
      trayTitle({
        ...idle,
        failedImports: 1,
        failedModels: 1,
        hasImports: true,
        hasModels: true,
      })
    ).toBe("2 项失败")
  })

  it("reports finished uploads", () => {
    expect(trayTitle({ ...idle, hasImports: true })).toBe("导入已完成")
    expect(trayTitle({ ...idle, hasModels: true })).toBe("上传已完成")
    expect(trayTitle({ ...idle, hasImports: true, hasModels: true })).toBe(
      "全部已完成"
    )
  })
})
