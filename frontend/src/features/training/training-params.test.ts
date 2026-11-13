import { describe, expect, it } from "vitest"

import type { ParamSpec } from "@/features/training/api"
import {
  buildDefaultParams,
  countModifiedParams,
  filterParamSpecs,
  formatParamsYaml,
  isParamModified,
  parseFreezeText,
  parseIntListText,
  validateTrainingParams,
} from "@/features/training/training-params"

const sampleSpecs: ParamSpec[] = [
  {
    key: "epochs",
    label: "训练轮数 (epochs)",
    group: "train",
    type: "int",
    default: 100,
    min: 1,
    max: 10000,
    description: "总训练轮数",
  },
  {
    key: "batch",
    label: "批次大小 (batch)",
    group: "train",
    type: "batch",
    default: 16,
    description: "固定批大小或自动显存占比",
  },
  {
    key: "imgsz",
    label: "输入图像尺寸 (imgsz)",
    group: "train",
    type: "imgsz",
    default: 640,
    description: "32 的倍数",
  },
  {
    key: "pretrained",
    label: "预训练权重 (pretrained)",
    group: "train",
    type: "bool",
    default: true,
    description: "是否启用预训练权重",
  },
  {
    key: "resume",
    label: "断点续训 (resume)",
    group: "train",
    type: "bool",
    default: false,
    description: "恢复优化器状态",
  },
  {
    key: "lr0",
    label: "初始学习率 (lr0)",
    group: "hyperparameters",
    type: "float",
    default: 0.01,
    min: 1e-7,
    max: 10,
    description: "SGD 初始学习率",
  },
  {
    key: "mosaic",
    label: "Mosaic 增强 (mosaic)",
    group: "augmentation",
    type: "float",
    default: 1.0,
    min: 0,
    max: 1,
    description: "四图拼接增强概率",
  },
  {
    key: "overlap_mask",
    label: "合并掩码 (overlap_mask)",
    group: "train",
    type: "bool",
    default: true,
    task_scope: "segment",
    description: "实例分割专用参数",
  },
  {
    key: "classes",
    label: "指定评估类别 (classes)",
    group: "val",
    type: "int_list",
    default: null,
    description: "评估类别列表",
  },
]

describe("buildDefaultParams & countModifiedParams", () => {
  it("populates official defaults and aligns resume/pretrained with initMode", () => {
    const finetuneDefaults = buildDefaultParams(sampleSpecs, "finetune")
    expect(finetuneDefaults.epochs).toBe(100)
    expect(finetuneDefaults.batch).toBe(16)
    expect(finetuneDefaults.imgsz).toBe(640)
    expect(finetuneDefaults.pretrained).toBe(true)
    expect(finetuneDefaults.resume).toBe(false)
    expect(countModifiedParams(sampleSpecs, finetuneDefaults)).toBe(0)

    const resumeDefaults = buildDefaultParams(sampleSpecs, "resume")
    expect(resumeDefaults.resume).toBe(true)
    expect(isParamModified(sampleSpecs[4], resumeDefaults)).toBe(true)
  })

  it("counts modified params globally and per group", () => {
    const values = {
      ...buildDefaultParams(sampleSpecs, "finetune"),
      epochs: 300,
      lr0: 0.001,
      classes: [0, 2],
    }
    expect(countModifiedParams(sampleSpecs, values)).toBe(3)
    expect(countModifiedParams(sampleSpecs, values, "train")).toBe(1)
    expect(countModifiedParams(sampleSpecs, values, "hyperparameters")).toBe(1)
    expect(countModifiedParams(sampleSpecs, values, "val")).toBe(1)
    expect(countModifiedParams(sampleSpecs, values, "augmentation")).toBe(0)
  })
})

describe("filterParamSpecs", () => {
  it("filters by group, search query, task_scope, and onlyModified", () => {
    const values = {
      ...buildDefaultParams(sampleSpecs, "finetune"),
      lr0: 0.005,
    }
    expect(
      filterParamSpecs(sampleSpecs, values, { group: "hyperparameters" }).map(
        (s) => s.key
      )
    ).toEqual(["lr0"])

    expect(
      filterParamSpecs(sampleSpecs, values, {
        group: "all",
        query: "segment",
      }).map((s) => s.key)
    ).toEqual(["overlap_mask"])

    expect(
      filterParamSpecs(sampleSpecs, values, {
        group: "all",
        onlyModified: true,
      }).map((s) => s.key)
    ).toEqual(["lr0"])
  })
})

describe("parseIntListText & parseFreezeText", () => {
  it("parses comma/space separated non-negative integer lists", () => {
    expect(parseIntListText("  ")).toEqual({ value: null, error: null })
    expect(parseIntListText("0, 2，5  2")).toEqual({
      value: [0, 2, 5],
      error: null,
    })
    expect(parseIntListText("0, abc").error).toContain("不是非负整数")
  })

  it("parses freeze as null, single layer count, or layer index list", () => {
    expect(parseFreezeText("")).toEqual({ value: null, error: null })
    expect(parseFreezeText("10")).toEqual({ value: 10, error: null })
    expect(parseFreezeText("0")).toMatchObject({ value: null })
    expect(parseFreezeText("0, 1, 2")).toEqual({
      value: [0, 1, 2],
      error: null,
    })
  })
})

describe("validateTrainingParams & formatParamsYaml", () => {
  it("validates imgsz stride, batch modes, and numeric ranges", () => {
    const base = buildDefaultParams(sampleSpecs, "finetune")
    expect(validateTrainingParams(sampleSpecs, base)).toBeNull()

    expect(
      validateTrainingParams(sampleSpecs, { ...base, imgsz: 650 })?.key
    ).toBe("imgsz")
    expect(
      validateTrainingParams(sampleSpecs, { ...base, imgsz: [640, 1280] })
    ).toBeNull()
    expect(
      validateTrainingParams(sampleSpecs, { ...base, batch: -1 })
    ).toBeNull()
    expect(
      validateTrainingParams(sampleSpecs, { ...base, batch: 0.7 })
    ).toBeNull()
    expect(
      validateTrainingParams(sampleSpecs, { ...base, batch: 0.01 })?.key
    ).toBe("batch")
    expect(
      validateTrainingParams(sampleSpecs, { ...base, epochs: 0 })?.key
    ).toBe("epochs")
  })

  it("exports Ultralytics-compatible args.yaml text", () => {
    const values = {
      ...buildDefaultParams(sampleSpecs, "finetune"),
      imgsz: [640, 960],
      classes: null,
    }
    const yaml = formatParamsYaml(sampleSpecs, values)
    expect(yaml).toContain("epochs: 100\n")
    expect(yaml).toContain("imgsz: [640, 960]\n")
    expect(yaml).toContain("classes: null\n")
  })
})
