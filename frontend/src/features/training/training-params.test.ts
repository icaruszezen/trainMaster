import { describe, expect, it } from "vitest"

import type { ParamSpec } from "@/features/training/api"
import {
  applyTemplateParams,
  buildDefaultParams,
  buildTemplateDefaultParams,
  buildTemplateParamsPayload,
  cleanParamLabel,
  countModifiedParams,
  countTemplateParamDiffs,
  filterParamSpecs,
  filterTemplatesBySource,
  formatCompactParamValue,
  formatParamsYaml,
  getAutoOptimizerHint,
  groupCommonSpecs,
  isParamModified,
  isParamModifiedForMode,
  listModifiedParams,
  parseFreezeText,
  parseIntListText,
  partitionAdvancedSpecs,
  partitionTemplatesBySource,
  splitSpecsByControlKind,
  splitSpecsByTier,
  suggestedTemplateCopyName,
  validateTrainingParams,
} from "@/features/training/training-params"

const sampleSpecs: ParamSpec[] = [
  {
    key: "lr0",
    label: "初始学习率 (lr0)",
    group: "hyperparameters",
    common: true,
    type: "float",
    default: 0.01,
    min: 1e-7,
    max: 10,
    description: "SGD 初始学习率",
  },
  {
    key: "epochs",
    label: "训练轮数 (epochs)",
    group: "train",
    common: true,
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
    common: true,
    type: "batch",
    default: 16,
    description: "固定批大小或自动显存占比",
  },
  {
    key: "imgsz",
    label: "输入图像尺寸 (imgsz)",
    group: "train",
    common: true,
    type: "imgsz",
    default: 640,
    description: "32 的倍数",
  },
  {
    key: "pretrained",
    label: "预训练权重 (pretrained)",
    group: "train",
    common: false,
    type: "bool",
    default: true,
    description: "是否启用预训练权重",
  },
  {
    key: "resume",
    label: "断点续训 (resume)",
    group: "train",
    common: false,
    type: "bool",
    default: false,
    description: "恢复优化器状态",
  },
  {
    key: "mosaic",
    label: "Mosaic 增强 (mosaic)",
    group: "augmentation",
    common: true,
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
    common: false,
    type: "bool",
    default: true,
    task_scope: "segment",
    description: "实例分割专用参数",
  },
  {
    key: "classes",
    label: "指定评估类别 (classes)",
    group: "val",
    common: false,
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

    const scratchDefaults = buildDefaultParams(sampleSpecs, "scratch")
    expect(scratchDefaults.pretrained).toBe(false)
    expect(countModifiedParams(sampleSpecs, scratchDefaults, undefined, "scratch")).toBe(0)

    const resumeDefaults = buildDefaultParams(sampleSpecs, "resume")
    expect(resumeDefaults.resume).toBe(true)
    const resumeSpec = sampleSpecs.find((s) => s.key === "resume")!
    expect(isParamModified(resumeSpec, resumeDefaults)).toBe(true)
    expect(isParamModifiedForMode(resumeSpec, resumeDefaults, "resume")).toBe(
      false
    )
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

describe("splitSpecsByTier & template parameter helpers", () => {
  it("splits specs into ordered common params and advanced params", () => {
    const { common, advanced } = splitSpecsByTier(sampleSpecs)
    expect(common.map((s) => s.key)).toEqual([
      "epochs",
      "batch",
      "imgsz",
      "lr0",
      "mosaic",
    ])
    expect(advanced.map((s) => s.key)).toEqual([
      "pretrained",
      "resume",
      "overlap_mask",
      "classes",
    ])
  })

  it("applies templates while respecting initMode bindings and counts template diffs", () => {
    const tpl = {
      ...buildTemplateDefaultParams(sampleSpecs),
      epochs: 200,
      imgsz: 1280,
      classes: [0, 1],
      resume: true,
    }

    const scratchApplied = applyTemplateParams(sampleSpecs, tpl, "scratch")
    expect(scratchApplied.epochs).toBe(200)
    expect(scratchApplied.imgsz).toBe(1280)
    expect(scratchApplied.classes).toEqual([0, 1])
    expect(scratchApplied.resume).toBe(false)
    expect(scratchApplied.pretrained).toBe(false)

    const resumeApplied = applyTemplateParams(sampleSpecs, tpl, "resume")
    expect(resumeApplied.resume).toBe(true)
    expect(resumeApplied.pretrained).toBe(true)

    expect(
      countTemplateParamDiffs(sampleSpecs, scratchApplied, tpl, "scratch")
    ).toBe(0)
    expect(
      countTemplateParamDiffs(
        sampleSpecs,
        { ...scratchApplied, epochs: 250, lr0: 0.002 },
        tpl,
        "scratch"
      )
    ).toBe(2)
  })

  it("normalizes payload when saving a template from a scratch or resume job", () => {
    const scratchValues = {
      ...buildDefaultParams(sampleSpecs, "scratch"),
      epochs: 150,
      classes: [1],
    }
    const payload = buildTemplateParamsPayload(
      sampleSpecs,
      scratchValues,
      "scratch"
    )
    expect(payload.epochs).toBe(150)
    expect(payload.classes).toEqual([1])
    expect(payload.pretrained).toBe(true)
    expect(payload.resume).toBe(false)

    const { advanced } = splitSpecsByTier(sampleSpecs)
    const modAdvanced = listModifiedParams(advanced, scratchValues, "scratch")
    expect(modAdvanced.map((item) => item.spec.key)).toEqual(["classes"])

    const pretrainedSpec = sampleSpecs.find((s) => s.key === "pretrained")!
    // In scratch mode, neither pretrained=false (UI default) nor pretrained=true (API/official default) is a user override.
    expect(
      isParamModifiedForMode(pretrainedSpec, { pretrained: false }, "scratch")
    ).toBe(false)
    expect(
      isParamModifiedForMode(pretrainedSpec, { pretrained: true }, "scratch")
    ).toBe(false)
    expect(
      isParamModifiedForMode(pretrainedSpec, { pretrained: false }, "finetune")
    ).toBe(true)

    expect(formatCompactParamValue(null)).toBe("自动")
    expect(formatCompactParamValue("")).toBe("禁用")
    expect(formatCompactParamValue([640, 960])).toBe("[640,960]")
    expect(formatCompactParamValue(false)).toBe("false")
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

describe("template source helpers", () => {
  const templates = [
    { id: 10, name: "产线焊点检测", is_builtin: false },
    { id: 1, name: "快速验证 / 试跑", is_builtin: true },
    { id: 11, name: "自定义高分模板", is_builtin: false },
    { id: 2, name: "通用工业检测基准", is_builtin: true },
  ]

  it("partitions custom and builtin templates while preserving order", () => {
    const { custom, builtin } = partitionTemplatesBySource(templates)
    expect(custom.map((t) => t.id)).toEqual([10, 11])
    expect(builtin.map((t) => t.id)).toEqual([1, 2])
  })

  it("filters templates by source tab", () => {
    expect(filterTemplatesBySource(templates, "all").map((t) => t.id)).toEqual([
      10, 1, 11, 2,
    ])
    expect(
      filterTemplatesBySource(templates, "custom").map((t) => t.id)
    ).toEqual([10, 11])
    expect(
      filterTemplatesBySource(templates, "builtin").map((t) => t.id)
    ).toEqual([1, 2])
  })

  it("suggests distinct copy names for built-in and custom templates", () => {
    expect(
      suggestedTemplateCopyName({ name: "高分辨率微小缺陷", is_builtin: true })
    ).toBe("高分辨率微小缺陷-自定义")
    expect(
      suggestedTemplateCopyName({ name: "产线焊点检测", is_builtin: false })
    ).toBe("产线焊点检测-副本")
  })
})

describe("UI layout & grouping helpers", () => {
  it("strips duplicate key suffixes from labels while keeping units", () => {
    expect(cleanParamLabel("训练轮数 (epochs)", "epochs")).toBe("训练轮数")
    expect(cleanParamLabel("最大训练时长 (time, 小时)", "time")).toBe(
      "最大训练时长 (小时)"
    )
    expect(cleanParamLabel("动量 / Beta1 (momentum)", "momentum")).toBe(
      "动量 / Beta1"
    )
    expect(cleanParamLabel("无括号名称", "custom_param")).toBe("无括号名称")
  })

  it("groups common params into semantic subgroups with a fallback bucket", () => {
    const common = [
      ...splitSpecsByTier(sampleSpecs).common,
      {
        key: "future_common",
        label: "未来常用项 (future_common)",
        group: "train" as const,
        common: true,
        type: "int" as const,
        default: 1,
        description: "未知新增常用项",
      },
    ]
    const groups = groupCommonSpecs(common)
    expect(groups.map((g) => g.label)).toEqual([
      "训练节奏与输入",
      "优化器与增强",
      "其他常用参数",
    ])
    expect(groups[0].wide).toBe(true)
    expect(groups[0].specs.map((s) => s.key)).toEqual([
      "epochs",
      "batch",
      "imgsz",
    ])
    expect(groups[1].specs.map((s) => s.key)).toEqual(["lr0", "mosaic"])
    expect(groups[2].specs.map((s) => s.key)).toEqual(["future_common"])
  })

  it("partitions advanced params into semantic subgroups and collapsible task-specific specs", () => {
    const trainAdvanced: ParamSpec[] = [
      ...sampleSpecs.filter((s) => !s.common && s.group === "train"),
      {
        key: "close_mosaic",
        label: "末尾关闭 Mosaic 轮数 (close_mosaic)",
        group: "train",
        common: false,
        type: "int",
        default: 10,
        description: "关闭 Mosaic",
      },
      {
        key: "dropout",
        label: "分类 Dropout 概率 (dropout)",
        group: "train",
        common: false,
        type: "float",
        default: 0,
        task_scope: "classify",
        description: "分类专用",
      },
    ]

    const detectPartition = partitionAdvancedSpecs(
      trainAdvanced,
      "train",
      "detect"
    )
    expect(detectPartition.subgroups.map((g) => g.label)).toEqual([
      "检查点与权重",
      "训练控制与采样",
    ])
    expect(detectPartition.otherTaskSpecs.map((s) => s.key)).toEqual([
      "overlap_mask",
      "dropout",
    ])

    const segPartition = partitionAdvancedSpecs(
      trainAdvanced,
      "train",
      "segment"
    )
    expect(segPartition.subgroups.map((g) => g.label)).toEqual([
      "检查点与权重",
      "训练控制与采样",
      "当前任务专用",
    ])
    expect(segPartition.subgroups[2].specs.map((s) => s.key)).toEqual([
      "overlap_mask",
    ])
    expect(segPartition.otherTaskSpecs.map((s) => s.key)).toEqual(["dropout"])
  })

  it("splits pure bool toggles from regular controls including nullable_bool", () => {
    const mixed: ParamSpec[] = [
      sampleSpecs[0], // float
      sampleSpecs[4], // bool
      {
        key: "nms",
        label: "验证启用 NMS (nms)",
        group: "val",
        common: false,
        type: "nullable_bool",
        default: null,
        description: "三态下拉",
      },
      sampleSpecs[5], // bool
    ]
    const { fields, toggles } = splitSpecsByControlKind(mixed)
    expect(fields.map((s) => s.key)).toEqual(["lr0", "nms"])
    expect(toggles.map((s) => s.key)).toEqual(["pretrained", "resume"])
  })

  it("returns auto optimizer hints only for lr0 and momentum when optimizer is auto", () => {
    expect(getAutoOptimizerHint("lr0", { optimizer: "auto" })?.badge).toBe(
      "auto 接管"
    )
    expect(getAutoOptimizerHint("momentum", {})?.badge).toBe("auto 接管")
    expect(getAutoOptimizerHint("lr0", { optimizer: "AdamW" })).toBeNull()
    expect(getAutoOptimizerHint("epochs", { optimizer: "auto" })).toBeNull()
  })
})

