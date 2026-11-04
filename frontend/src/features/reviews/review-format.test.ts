import { describe, expect, it } from "vitest"

import type { Dataset } from "@/features/datasets/api"
import type { Model } from "@/features/models/api"
import type { ReviewCounts } from "@/features/reviews/api"
import {
  changedOptionCount,
  datasetNamesText,
  datasetSelectionError,
  defaultOptionsDraft,
  defaultReviewName,
  groupModelsByProject,
  inferenceReport,
  inferenceSpeedText,
  inferredImageCount,
  mergeDatasetsText,
  mergeInput,
  mergeSummaryText,
  optionsDraftErrors,
  optionsFromDraft,
  orderedStatuses,
  packageInput,
  projectDatasetIds,
  REVIEW_RESULTS_MAX_BYTES,
  reviewEntryPath,
  reviewModelBlocker,
  reviewModelText,
  reviewModelTitle,
  reviewOptionsText,
  selectedDatasets,
  statusesText,
  uploadSummaryText,
  validateConf,
  validateIoU,
  validateMaxDet,
  validateResultsFile,
  validateReviewDescription,
  validateReviewName,
} from "@/features/reviews/review-format"

const counts = (changes: Partial<ReviewCounts> = {}): ReviewCounts => ({
  total: 10,
  inferred: 8,
  inference_failed: 1,
  pending: 6,
  approved: 3,
  needs_sample: 1,
  edited: 2,
  merged: 0,
  ...changes,
})

function model(changes: Partial<Model> & { id: number }): Model {
  return {
    project: { id: 1, name: "外观检测" },
    version: 1,
    description: "",
    source: "upload",
    training_job_id: null,
    yolo_version: "yolo11",
    task: "detect",
    file_name: "best.pt",
    size_bytes: 1,
    sha256: "",
    info: {} as Model["info"],
    file_missing: false,
    export_count: 0,
    download_url: "",
    created_at: "",
    updated_at: "",
    ...changes,
  }
}

function dataset(
  id: number,
  name: string,
  imageCount: number,
  projectIds: number[] = []
): Dataset {
  return {
    id,
    name,
    image_count: imageCount,
    projects: projectIds.map((projectId) => ({
      id: projectId,
      name: `项目 ${projectId}`,
    })),
  } as Dataset
}

describe("models", () => {
  it("names the model snapshot", () => {
    const ref = {
      project_name: "外观检测",
      version: 3,
      yolo_version: "yolo11",
      task: "detect",
    }
    expect(reviewModelTitle(ref)).toBe("外观检测 v3")
    expect(reviewModelText(ref)).toBe("YOLO11 · 检测")
    expect(reviewModelText({ yolo_version: "yolov99", task: "" })).toBe(
      "yolov99 · 未知任务"
    )
  })

  it("explains why a model cannot be used", () => {
    expect(reviewModelBlocker({ task: "detect", file_missing: false })).toBe(
      null
    )
    for (const task of ["segment", "pose", "obb"] as const) {
      expect(reviewModelBlocker({ task, file_missing: false })).toBeNull()
    }
    expect(reviewModelBlocker({ task: "classify", file_missing: false })).toBe(
      "分类模型没有检测框"
    )
    expect(reviewModelBlocker({ task: "", file_missing: false })).toBe(
      "无法识别任务，不能用于审图"
    )
    expect(reviewModelBlocker({ task: "detect", file_missing: true })).toBe(
      "模型文件缺失"
    )
  })

  it("groups models by project, newest version first", () => {
    const groups = groupModelsByProject([
      model({ id: 1, version: 1 }),
      model({ id: 2, version: 1, project: { id: 2, name: "白班检测" } }),
      model({ id: 3, version: 3 }),
      model({ id: 4, version: 2 }),
    ])
    expect(groups.map((group) => group.project.name)).toEqual([
      "白班检测",
      "外观检测",
    ])
    expect(groups[1].models.map((item) => item.id)).toEqual([3, 4, 1])
  })
})

describe("names and descriptions", () => {
  it("builds the default name like the server", () => {
    expect(defaultReviewName("外观检测", 3)).toBe("外观检测 v3 审图")
    expect(defaultReviewName("  外观检测 ", 12)).toBe("外观检测 v12 审图")
    const long = "检".repeat(64)
    const name = defaultReviewName(long, 7)
    expect(Array.from(name)).toHaveLength(64)
    expect(name.endsWith(" v7 审图")).toBe(true)
    // 截断时不会切开代理对。
    expect(defaultReviewName("😀".repeat(70), 1)).toBe(
      `${"😀".repeat(58)} v1 审图`
    )
  })

  it("validates names like project names", () => {
    expect(validateReviewName("A 线 v3 审图")).toBeNull()
    expect(validateReviewName("  ")).toBe("名称不能为空")
    expect(validateReviewName("  ", { optional: true })).toBeNull()
    expect(validateReviewName("a".repeat(65))).toBe("名称不能超过 64 个字符")
    expect(validateReviewName("a\tb", { optional: true })).toBe(
      "名称不能包含换行、制表符等控制字符"
    )
  })

  it("validates descriptions", () => {
    expect(validateReviewDescription("夜班\n第二批\t复查")).toBeNull()
    expect(validateReviewDescription("a\u0001")).toBe("说明包含无效字符")
    expect(validateReviewDescription(` ${"说".repeat(500)} `)).toBeNull()
    expect(validateReviewDescription("说".repeat(501))).toBe(
      "说明不能超过 500 个字符"
    )
  })
})

describe("inference options", () => {
  it("starts from the defaults and the model's training size", () => {
    expect(defaultOptionsDraft(null)).toEqual({
      conf: "0.25",
      iou: "0.7",
      height: "640",
      width: "640",
      maxDet: "300",
    })
    expect(defaultOptionsDraft({ info: { image_size: 1000 } }).height).toBe(
      "1024"
    )
    expect(defaultOptionsDraft({ info: { image_size: null } }).width).toBe(
      "640"
    )
  })

  it("validates each option with the server's ranges", () => {
    expect(validateConf("0.25")).toBeNull()
    expect(validateConf(".5")).toBeNull()
    expect(validateConf("1")).toBeNull()
    expect(validateConf("0.01")).toBeNull()
    expect(validateConf("")).toBe("请填写置信度阈值")
    expect(validateConf("abc")).toBe("请填写数字，如 0.25")
    expect(validateConf("-0.1")).toBe("请填写数字，如 0.25")
    expect(validateConf("0.001")).toBe("置信度阈值应在 0.01–1 之间")
    expect(validateConf("1.5")).toBe("置信度阈值应在 0.01–1 之间")
    expect(validateIoU("0.7")).toBeNull()
    expect(validateIoU("0.05")).toBe("IoU 阈值应在 0.1–0.95 之间")
    expect(validateIoU("0.96")).toBe("IoU 阈值应在 0.1–0.95 之间")
    expect(validateMaxDet("300")).toBeNull()
    expect(validateMaxDet("1000")).toBeNull()
    expect(validateMaxDet("")).toBe("请填写最多检测框数")
    expect(validateMaxDet("1.5")).toBe("请填写整数")
    expect(validateMaxDet("0")).toBe("最多检测框数应在 1–1000 之间")
    expect(validateMaxDet("1001")).toBe("最多检测框数应在 1–1000 之间")
  })

  it("turns a valid draft into request options", () => {
    const draft = {
      conf: " 0.4 ",
      iou: "0.5",
      height: "480",
      width: "640",
      maxDet: "100",
    }
    expect(optionsFromDraft(draft)).toEqual({
      conf: 0.4,
      iou: 0.5,
      imgsz: [480, 640],
      max_det: 100,
    })
    expect(optionsDraftErrors(draft)).toEqual({
      conf: null,
      iou: null,
      imgsz: null,
      maxDet: null,
    })
    const bad = { ...draft, width: "600" }
    expect(optionsFromDraft(bad)).toBeNull()
    expect(optionsDraftErrors(bad).imgsz).toMatch(/32 的倍数/)
  })

  it("counts the options changed from the defaults", () => {
    const defaults = defaultOptionsDraft(null)
    expect(changedOptionCount(defaults, defaults)).toBe(0)
    expect(
      changedOptionCount({ ...defaults, conf: "0.250", iou: ".7" }, defaults)
    ).toBe(0)
    expect(
      changedOptionCount(
        { ...defaults, conf: "0.5", width: "320", maxDet: "50" },
        defaults
      )
    ).toBe(3)
  })

  it("describes the options", () => {
    expect(
      reviewOptionsText({
        conf: 0.25,
        iou: 0.7,
        imgsz: [640, 640],
        max_det: 300,
      })
    ).toBe("置信度 ≥ 0.25 · IoU 0.7 · 640 · 最多 300 个框")
    expect(
      reviewOptionsText({
        conf: 0.5,
        iou: 0.45,
        imgsz: [480, 640],
        max_det: 1000,
      })
    ).toBe("置信度 ≥ 0.5 · IoU 0.45 · 480×640 · 最多 1,000 个框")
  })
})

describe("datasets", () => {
  it("lists dataset names briefly", () => {
    expect(datasetNamesText([])).toBe("—")
    expect(datasetNamesText([{ name: "A" }])).toBe("A")
    expect(datasetNamesText([{ name: "A" }, { name: "B" }])).toBe("A、B")
    expect(
      datasetNamesText([{ name: "A" }, { name: "B" }, { name: "C" }])
    ).toBe("A、B 等 3 个")
  })

  it("picks the project's datasets with images by default", () => {
    const datasets = [
      dataset(1, "夜班", 10, [1]),
      dataset(2, "白班", 5, [1, 2]),
      dataset(3, "空的", 0, [1]),
      dataset(4, "其它", 3, [2]),
    ]
    // 按名称（拼音）排序：白班在夜班前。
    expect(projectDatasetIds(datasets, 1)).toEqual([2, 1])
    expect(projectDatasetIds(datasets, 9)).toEqual([])
  })

  it("keeps the selection order and drops datasets no longer listed", () => {
    const datasets = [dataset(1, "A", 10), dataset(2, "B", 5)]
    const chosen = selectedDatasets(datasets, [2, 9, 1])
    expect(chosen.datasets.map((item) => item.id)).toEqual([2, 1])
    expect(chosen.images).toBe(15)
  })

  it("checks the selection like the server", () => {
    expect(datasetSelectionError(1, 10)).toBeNull()
    expect(datasetSelectionError(0, 0)).toBe("请至少选择一个数据集")
    expect(datasetSelectionError(51, 100)).toBe(
      "一个审图任务最多选择 50 个数据集"
    )
    expect(datasetSelectionError(2, 0)).toBe("所选数据集中没有图片")
    expect(datasetSelectionError(2, 200_001)).toBe(
      "所选数据集共有 200,001 张图片，一个审图任务最多 200,000 张，请少选一些数据集"
    )
  })
})

describe("image counts", () => {
  it("counts only inferred images", () => {
    // 10 张中 2 张还没有识别，它们都是待审核。
    expect(inferredImageCount(counts(), ["pending"])).toBe(4)
    expect(inferredImageCount(counts(), ["approved", "needs_sample"])).toBe(4)
    expect(
      inferredImageCount(counts(), ["needs_sample", "pending", "approved"])
    ).toBe(8)
    expect(inferredImageCount(counts(), [])).toBe(0)
    expect(
      inferredImageCount(counts({ inferred: 0, pending: 10 }), ["pending"])
    ).toBe(0)
  })

  it("orders and names statuses", () => {
    expect(
      orderedStatuses(["needs_sample", "pending", "needs_sample"])
    ).toEqual(["pending", "needs_sample"])
    expect(statusesText(["needs_sample", "approved"])).toBe("已审核、需加样")
  })
})

describe("requests and links", () => {
  it("omits the filters that select everything", () => {
    expect(packageInput(["needs_sample", "pending", "approved"], null)).toEqual(
      {}
    )
    expect(packageInput(["needs_sample", "pending"], 3)).toEqual({
      statuses: ["pending", "needs_sample"],
      dataset_ids: [3],
    })
    expect(mergeInput(["needs_sample"], null, "skip")).toEqual({
      statuses: ["needs_sample"],
      on_existing: "skip",
    })
    expect(mergeInput(["needs_sample", "approved"], 2, "overwrite")).toEqual({
      statuses: ["approved", "needs_sample"],
      on_existing: "overwrite",
      dataset_ids: [2],
    })
  })

  it("opens the first pending image while there is one", () => {
    const review = {
      id: 4,
      counts: counts(),
      datasets: [
        { id: 1, name: "A", folder: "A", counts: counts() },
        {
          id: 2,
          name: "B",
          folder: "B",
          counts: counts({ total: 3, inferred: 3, pending: 0, approved: 3 }),
        },
      ],
    }
    expect(reviewEntryPath(review)).toBe(
      "/models/reviews/4/workspace?status=pending"
    )
    expect(reviewEntryPath(review, 1)).toBe(
      "/models/reviews/4/workspace?status=pending&dataset_id=1"
    )
    expect(reviewEntryPath(review, 2)).toBe(
      "/models/reviews/4/workspace?dataset_id=2"
    )
  })
})

describe("uploads and merges", () => {
  it("validates the results file", () => {
    expect(validateResultsFile({ name: "审图结果.ZIP", size: 10 })).toBeNull()
    expect(validateResultsFile({ name: "review.json", size: 10 })).toMatch(
      /请选择 \.zip 文件/
    )
    expect(validateResultsFile({ name: "a.zip", size: 0 })).toBe("文件是空的")
    expect(
      validateResultsFile({ name: "a.zip", size: REVIEW_RESULTS_MAX_BYTES + 1 })
    ).toBe("审图结果不能超过 4 GiB")
  })

  it("summarizes an upload", () => {
    expect(
      uploadSummaryText({
        images: 30,
        applied: 5,
        status_changes: 5,
        annotation_changes: 1,
        unchanged: 24,
        conflicts: 1,
        invalid: 0,
        unknown: 0,
      })
    ).toBe("应用 5 张（审核状态 5、标注 1），没有变化 24 张，冲突 1 张")
    expect(
      uploadSummaryText({
        images: 3,
        applied: 0,
        status_changes: 0,
        annotation_changes: 0,
        unchanged: 0,
        conflicts: 0,
        invalid: 2,
        unknown: 1,
      })
    ).toBe("应用 0 张，无效 2 张，不在任务中 1 张")
  })

  it("summarizes a merge", () => {
    expect(
      mergeSummaryText({
        merged: 16,
        skipped_existing: 0,
        skipped_empty: 0,
        conflicts: 0,
        failed: 0,
      })
    ).toBe("合并 16 张")
    expect(
      mergeSummaryText({
        merged: 1,
        skipped_existing: 2,
        skipped_empty: 3,
        conflicts: 4,
        failed: 5,
      })
    ).toBe(
      "合并 1 张，跳过已有标注 2 张，没有审图标注 3 张，冲突 4 张，失败 5 张"
    )
  })

  it("names the merged datasets", () => {
    const datasets = [
      { id: 1, name: "A" },
      { id: 2, name: "B" },
    ]
    expect(mergeDatasetsText({ dataset_ids: [1, 2] }, datasets)).toBe(
      "全部数据集"
    )
    expect(mergeDatasetsText({ dataset_ids: [2] }, datasets)).toBe("B")
    expect(mergeDatasetsText({ dataset_ids: [1] }, [datasets[0]])).toBe("A")
    expect(mergeDatasetsText({ dataset_ids: [7] }, datasets)).toBe("#7")
  })
})

describe("inference report", () => {
  it("reads the node's report defensively", () => {
    expect(inferenceReport(null)).toBeNull()
    const report = inferenceReport({
      device: "cuda:0",
      torch_version: "2.5.1",
      ultralytics_version: "",
      node_name: "gpu-01",
      elapsed_seconds: 192.4,
      images_per_second: 12.46,
      extra: true,
    })
    expect(report).toEqual({
      device: "cuda:0",
      torchVersion: "2.5.1",
      ultralyticsVersion: null,
      nodeName: "gpu-01",
      elapsedSeconds: 192.4,
      imagesPerSecond: 12.46,
    })
    expect(inferenceSpeedText(report)).toBe("3 分钟 12 秒（12.5 张/秒）")
    expect(
      inferenceSpeedText(
        inferenceReport({ elapsed_seconds: "x", images_per_second: 3 })
      )
    ).toBeNull()
    expect(inferenceSpeedText(inferenceReport({ elapsed_seconds: 5 }))).toBe(
      "5 秒"
    )
  })
})
