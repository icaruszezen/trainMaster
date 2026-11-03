import { describe, expect, it } from "vitest"

import {
  MODEL_MAX_BYTES,
  validateModelDescription,
  validateModelFile,
} from "@/features/models/naming"

describe("validateModelFile", () => {
  it("accepts .pt files in any case", () => {
    expect(validateModelFile({ name: "best.pt", size: 1 })).toBeNull()
    expect(validateModelFile({ name: "最终权重.PT", size: 1 })).toBeNull()
    expect(
      validateModelFile({ name: "x.pt", size: MODEL_MAX_BYTES })
    ).toBeNull()
  })

  it("explains other model formats", () => {
    expect(validateModelFile({ name: "best.onnx", size: 1 })).toMatch(
      /ONNX 可在下载时自动转换/
    )
    expect(validateModelFile({ name: "best.engine", size: 1 })).toMatch(
      /TensorRT/
    )
    expect(validateModelFile({ name: "model.pth", size: 1 })).toMatch(
      /Ultralytics 训练得到的 \.pt/
    )
    expect(validateModelFile({ name: "weights.zip", size: 1 })).toMatch(
      /请选择 \.pt 格式/
    )
  })

  it("rejects bad names, empty and oversized files", () => {
    expect(validateModelFile({ name: "a\u0001.pt", size: 1 })).toMatch(
      /无效字符/
    )
    expect(
      validateModelFile({ name: `${"a".repeat(253)}.pt`, size: 1 })
    ).toMatch(/不能超过 255 个字符/)
    expect(
      validateModelFile({ name: `${"a".repeat(252)}.pt`, size: 1 })
    ).toBeNull()
    expect(validateModelFile({ name: "best.pt", size: 0 })).toBe(
      "模型文件是空文件"
    )
    expect(
      validateModelFile({ name: "best.pt", size: MODEL_MAX_BYTES + 1 })
    ).toMatch(/模型文件不能超过/)
  })
})

describe("validateModelDescription", () => {
  it("allows line breaks and tabs up to 500 characters", () => {
    expect(validateModelDescription("")).toBeNull()
    expect(validateModelDescription("第一行\n第二行\t备注")).toBeNull()
    expect(validateModelDescription("模".repeat(500))).toBeNull()
    expect(validateModelDescription(`  ${"模".repeat(500)}  `)).toBeNull()
  })

  it("rejects control characters and long text", () => {
    expect(validateModelDescription("a\u0000b")).toBe("说明包含无效字符")
    expect(validateModelDescription("模".repeat(501))).toBe(
      "说明不能超过 500 个字符"
    )
  })
})
