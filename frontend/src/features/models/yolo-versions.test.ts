import { describe, expect, it } from "vitest"

import type {
  ExportOptions,
  ModelTask,
  YoloVersion,
} from "@/features/models/api"
import {
  allowedPostprocess,
  anchorCount,
  defaultExportOptions,
  describePostprocess,
  exportFileName,
  exportOptionsText,
  findYoloVersion,
  fitExportOptions,
  imgSizeText,
  modelArchLabel,
  modelFileName,
  opsetChoices,
  postprocessConflict,
  rawOnlyNote,
  roundImgSize,
  safeFileStem,
  sameExportOptions,
  supportsEnd2end,
  taskLabel,
  validateImgSize,
  yoloVersionLabel,
  yoloVersions,
} from "@/features/models/yolo-versions"

function target(
  yolo_version: YoloVersion,
  task: ModelTask = "detect",
  { end2end = false, image_size = 640 as number | null } = {}
) {
  return { yolo_version, task, info: { end2end, image_size } }
}

const options = (overrides: Partial<ExportOptions> = {}): ExportOptions => ({
  imgsz: [640, 640],
  dynamic: false,
  postprocess: "raw",
  opset: null,
  ...overrides,
})

describe("yolo versions", () => {
  it("lists the six supported versions from old to new", () => {
    expect(yoloVersions.map((version) => version.value)).toEqual([
      "yolov8",
      "yolov9",
      "yolov10",
      "yolo11",
      "yolo12",
      "yolo26",
    ])
    expect(new Set(yoloVersions.map((v) => v.label)).size).toBe(6)
  })

  it("marks only YOLOv10 and YOLO26 as end-to-end", () => {
    expect(yoloVersions.filter((v) => v.end2end).map((v) => v.value)).toEqual([
      "yolov10",
      "yolo26",
    ])
  })

  it("labels versions, architectures and tasks", () => {
    expect(yoloVersionLabel("yolov8")).toBe("YOLOv8")
    expect(yoloVersionLabel("yolo11")).toBe("YOLO11")
    expect(yoloVersionLabel("yolo99")).toBe("yolo99")
    expect(yoloVersionLabel(null)).toBe("未知版本")
    expect(findYoloVersion("YOLO11")).toBeUndefined()

    expect(
      modelArchLabel({ yolo_version: "yolo11", info: { scale: "s" } })
    ).toBe("YOLO11s")
    expect(
      modelArchLabel({ yolo_version: "yolov8", info: { scale: "" } })
    ).toBe("YOLOv8")

    expect(taskLabel("detect")).toBe("检测")
    expect(taskLabel("obb")).toBe("旋转框")
    // 与后端的任务名称一致。
    expect(taskLabel("semantic")).toBe("语义分割")
    expect(taskLabel("depth")).toBe("深度估计")
    expect(taskLabel("")).toBe("未知任务")
    expect(taskLabel("track")).toBe("track")
  })
})

describe("allowedPostprocess", () => {
  it("defaults to raw output with NMS available for NMS-based versions", () => {
    for (const version of ["yolov8", "yolov9", "yolo11", "yolo12"] as const) {
      expect(allowedPostprocess(target(version)), version).toEqual([
        "raw",
        "nms",
      ])
    }
  })

  it("defaults to end-to-end for YOLOv10 and YOLO26", () => {
    expect(allowedPostprocess(target("yolov10"))).toEqual([
      "end2end",
      "raw",
      "nms",
    ])
    expect(allowedPostprocess(target("yolo26", "segment"))).toEqual([
      "end2end",
      "raw",
      "nms",
    ])
  })

  it("trusts an end-to-end head found in the file", () => {
    const model = target("yolo11", "detect", { end2end: true })
    expect(supportsEnd2end(model)).toBe(true)
    expect(allowedPostprocess(model)[0]).toBe("end2end")
  })

  it("only offers raw output for classification", () => {
    expect(allowedPostprocess(target("yolo26", "classify"))).toEqual(["raw"])
    expect(allowedPostprocess(target("yolov8", "classify"))).toEqual(["raw"])
  })

  it("offers NMS only for tasks with boxes", () => {
    expect(allowedPostprocess(target("yolo11", "pose"))).toContain("nms")
    expect(allowedPostprocess(target("yolo11", "obb"))).toContain("nms")
    expect(allowedPostprocess(target("yolo11", "semantic"))).toEqual(["raw"])
    expect(allowedPostprocess(target("yolo11", ""))).toEqual(["raw"])
  })

  it("offers end-to-end only for tasks with boxes", () => {
    // YOLO26 的语义分割、深度估计模型没有端到端检测头，只有原始输出。
    for (const task of ["semantic", "depth", "classify", ""] as const) {
      expect(allowedPostprocess(target("yolo26", task)), task).toEqual(["raw"])
      expect(
        allowedPostprocess(target("yolo11", task, { end2end: true })),
        task
      ).toEqual(["raw"])
    }
    for (const task of ["detect", "segment", "pose", "obb"] as const) {
      expect(allowedPostprocess(target("yolo26", task)), task).toEqual([
        "end2end",
        "raw",
        "nms",
      ])
    }
    expect(defaultExportOptions(target("yolo26", "semantic")).postprocess).toBe(
      "raw"
    )
    expect(defaultExportOptions(target("yolo26", "depth")).postprocess).toBe(
      "raw"
    )
  })

  it("explains why only raw output is offered", () => {
    expect(rawOnlyNote(target("yolo26", "detect"))).toBeNull()
    expect(rawOnlyNote(target("yolo11", "obb"))).toBeNull()
    expect(rawOnlyNote(target("yolo26", "classify"))).toBe(
      "分类模型只有原始输出。"
    )
    expect(rawOnlyNote(target("yolo26", "semantic"))).toBe(
      "语义分割模型只有原始输出。"
    )
    expect(rawOnlyNote(target("yolo26", "depth"))).toBe(
      "深度估计模型只有原始输出。"
    )
    expect(rawOnlyNote(target("yolo11", ""))).toBe(
      "无法识别模型的任务，只能导出原始输出。"
    )
  })
})

describe("dynamic size and embedded NMS", () => {
  it("does not combine embedded NMS with a dynamic size", () => {
    expect(postprocessConflict("nms", true)).toContain("batch 为 1")
    expect(postprocessConflict("nms", false)).toBeNull()
    expect(postprocessConflict("raw", true)).toBeNull()
    expect(postprocessConflict("end2end", true)).toBeNull()
  })

  it("switches embedded NMS to the default when the size becomes dynamic", () => {
    expect(
      fitExportOptions(
        options({ dynamic: true, postprocess: "nms" }),
        target("yolo11")
      )
    ).toEqual(options({ dynamic: true, postprocess: "raw" }))
    expect(
      fitExportOptions(
        options({ dynamic: true, postprocess: "nms" }),
        target("yolo26")
      )
    ).toEqual(options({ dynamic: true, postprocess: "end2end" }))
    // 其它组合保持不变（同一个对象）。
    const raw = options({ dynamic: true, postprocess: "raw" })
    expect(fitExportOptions(raw, target("yolo26"))).toBe(raw)
    const nms = options({ postprocess: "nms" })
    expect(fitExportOptions(nms, target("yolo11"))).toBe(nms)
    const end2end = options({ dynamic: true, postprocess: "end2end" })
    expect(fitExportOptions(end2end, target("yolov10"))).toBe(end2end)
  })

  it("replaces modes the model does not allow", () => {
    // 规则调整前的记录：YOLO26 语义分割默认为 end2end。
    expect(
      fitExportOptions(
        options({ postprocess: "end2end" }),
        target("yolo26", "semantic")
      )
    ).toEqual(options({ postprocess: "raw" }))
    expect(
      fitExportOptions(
        options({ postprocess: "nms" }),
        target("yolo26", "depth")
      )
    ).toEqual(options({ postprocess: "raw" }))
    expect(
      fitExportOptions(options({ postprocess: "end2end" }), target("yolo11"))
    ).toEqual(options({ postprocess: "raw" }))
  })

  it("keeps the other fields of a draft", () => {
    const draft = {
      height: "640",
      width: "480",
      dynamic: true,
      postprocess: "nms" as const,
      opset: "auto",
    }
    expect(fitExportOptions(draft, target("yolo11"))).toEqual({
      ...draft,
      postprocess: "raw",
    })
  })
})

describe("export options", () => {
  it("rounds the training size up to a multiple of 32 within range", () => {
    expect(roundImgSize(640)).toBe(640)
    expect(roundImgSize(600)).toBe(608)
    expect(roundImgSize(1)).toBe(32)
    expect(roundImgSize(5000)).toBe(4096)
  })

  it("defaults to the training size, static input and the version's postprocess", () => {
    expect(defaultExportOptions(target("yolo11"))).toEqual(
      options({ imgsz: [640, 640], postprocess: "raw" })
    )
    expect(
      defaultExportOptions(target("yolo26", "detect", { image_size: 1000 }))
    ).toEqual(options({ imgsz: [1024, 1024], postprocess: "end2end" }))
    expect(
      defaultExportOptions(target("yolov8", "detect", { image_size: null }))
    ).toEqual(options({ imgsz: [640, 640] }))
  })

  it("validates sizes with suggestions", () => {
    expect(validateImgSize("640")).toBeNull()
    expect(validateImgSize(" 1280 ")).toBeNull()
    expect(validateImgSize("")).toBe("请填写尺寸")
    expect(validateImgSize("64.5")).toBe("请填写整数")
    expect(validateImgSize("-32")).toBe("请填写整数")
    expect(validateImgSize("16")).toBe("尺寸应在 32–4096 之间")
    expect(validateImgSize("8192")).toBe("尺寸应在 32–4096 之间")
    expect(validateImgSize("600")).toBe("尺寸必须是 32 的倍数，如 576 或 608")
    expect(validateImgSize("33")).toBe("尺寸必须是 32 的倍数，如 32 或 64")
  })

  it("offers opset 21 down to 11", () => {
    expect(opsetChoices[0]).toBe(21)
    expect(opsetChoices.at(-1)).toBe(11)
    expect(opsetChoices).toHaveLength(11)
  })

  it("compares normalized options", () => {
    expect(sameExportOptions(options(), options())).toBe(true)
    expect(sameExportOptions(options(), options({ imgsz: [640, 480] }))).toBe(
      false
    )
    expect(sameExportOptions(options(), options({ opset: 17 }))).toBe(false)
    expect(sameExportOptions(options(), options({ dynamic: true }))).toBe(false)
  })

  it("describes options briefly", () => {
    expect(imgSizeText([640, 640])).toBe("640")
    expect(imgSizeText([480, 640])).toBe("480×640")
    expect(exportOptionsText(options({ postprocess: "end2end" }))).toBe(
      "640 · 端到端 · opset 自动"
    )
    expect(
      exportOptionsText(
        options({
          imgsz: [480, 640],
          dynamic: true,
          postprocess: "nms",
          opset: 17,
        })
      )
    ).toBe("480×640 · 动态尺寸 · 嵌入 NMS · opset 17")
  })
})

describe("describePostprocess", () => {
  const context = {
    task: "detect",
    classCount: 2,
    imgsz: [640, 640] as const,
    dynamic: false,
  }

  it("counts the candidate boxes of the three detection layers", () => {
    expect(anchorCount(640, 640)).toBe(8400)
    expect(anchorCount(320, 320)).toBe(2100)
    expect(anchorCount(480, 640)).toBe(6300)
  })

  it("gives raw detection output with the class count", () => {
    const description = describePostprocess("raw", context)
    expect(description.summary).toBe("需要自行 NMS")
    expect(description.outputs.map((o) => o.shape)).toEqual(["1 × 6 × 8400"])
  })

  it("writes the formula when the class count is unknown or the size dynamic", () => {
    const description = describePostprocess("raw", {
      ...context,
      classCount: null,
      dynamic: true,
    })
    expect(description.outputs[0].shape).toBe("1 × (4 + 类别数) × N")
  })

  it("gives 300 results for NMS and end-to-end", () => {
    expect(describePostprocess("end2end", context)).toMatchObject({
      label: "端到端",
      summary: "不需要 NMS",
      outputs: [{ shape: "1 × 300 × 6" }],
    })
    expect(describePostprocess("nms", context)).toMatchObject({
      label: "嵌入 NMS",
      summary: "NMS 已嵌入模型",
      outputs: [{ shape: "1 × 300 × 6" }],
    })
  })

  it("adds the mask prototypes for segmentation", () => {
    const raw = describePostprocess("raw", { ...context, task: "segment" })
    expect(raw.outputs.map((o) => o.shape)).toEqual([
      "1 × 38 × 8400",
      "1 × 32 × 160 × 160",
    ])
    const end2end = describePostprocess("end2end", {
      ...context,
      task: "segment",
      dynamic: true,
    })
    expect(end2end.outputs.map((o) => o.shape)).toEqual([
      "1 × 300 × 38",
      "1 × 32 × H/4 × W/4",
    ])
  })

  it("describes classification, rotated boxes and unknown tasks", () => {
    expect(
      describePostprocess("raw", {
        ...context,
        task: "classify",
        classCount: 5,
      }).outputs[0].shape
    ).toBe("1 × 5")
    expect(
      describePostprocess("raw", { ...context, task: "obb" }).outputs[0].shape
    ).toBe("1 × 7 × 8400")
    expect(
      describePostprocess("nms", { ...context, task: "obb" }).outputs[0].shape
    ).toBe("1 × 300 × 7")
    expect(
      describePostprocess("raw", { ...context, task: "" }).outputs
    ).toEqual([])
  })
})

describe("download file names", () => {
  it("replaces characters Windows does not allow", () => {
    expect(safeFileStem("划痕检测")).toBe("划痕检测")
    expect(safeFileStem('a/b\\c:d*e?f"g<h>i|j')).toBe("a_b_c_d_e_f_g_h_i_j")
    expect(safeFileStem("tab\there\u007f")).toBe("tab_here_")
    expect(safeFileStem("")).toBe("model")
    // 空格保留（与后端一致）。
    expect(safeFileStem("line A")).toBe("line A")
  })

  it("names PT downloads after the project and version", () => {
    expect(modelFileName("划痕检测", 3)).toBe("划痕检测_v3.pt")
    expect(modelFileName("a/b", 1)).toBe("a_b_v1.pt")
  })

  it("names ONNX downloads after the options", () => {
    expect(exportFileName("划痕检测", 3, options())).toBe(
      "划痕检测_v3_640.onnx"
    )
    expect(
      exportFileName(
        "划痕检测",
        3,
        options({
          imgsz: [480, 640],
          dynamic: true,
          postprocess: "end2end",
          opset: 17,
        })
      )
    ).toBe("划痕检测_v3_480x640_dynamic_end2end_opset17.onnx")
    expect(exportFileName("p", 1, options({ postprocess: "nms" }))).toBe(
      "p_v1_640_nms.onnx"
    )
  })
})
