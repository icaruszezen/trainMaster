import { describe, expect, it } from "vitest"

import type { SourceRef } from "@/features/evaluation/api"
import {
  colorForIndex,
  evaluationDurationSeconds,
  evaluationModelBlocker,
  evaluationModelLabel,
  evaluationProgressPercent,
  evaluationSourceSummary,
  evaluationTitle,
  formatFps,
  formatMetricDelta,
  formatMetricPercent,
  formatMs,
  formatThreshold,
} from "@/features/evaluation/format"
import {
  evaluationComparePath,
  evaluationDetailPath,
  evaluationsListPath,
  newEvaluationPath,
  parseCompareIds,
  parsePositiveId,
} from "@/features/evaluation/paths"
import type { Model, ModelTask } from "@/features/models/api"

function mockModel(overrides: Partial<Model> = {}): Model {
  return {
    id: 10,
    project: { id: 1, name: "金属件检测" },
    version: 3,
    yolo_version: "yolo11",
    task: "detect",
    source: "training",
    training_job_id: 2,
    description: "",
    file_name: "best.pt",
    size_bytes: 1024,
    sha256: "abc",
    file_missing: false,
    export_count: 0,
    download_url: "/api/v1/models/10/file",
    info: {
      task: "detect",
      scale: "m",
      detected_yolo_version: "yolo11",
      version_source: "yaml_file",
      yaml_file: "yolo11m.yaml",
      model_class: "DetectionModel",
      head: "Detect",
      end2end: false,
      channels: 3,
      image_size: 640,
      parameters: 20_000_000,
      class_names: ["划痕", "凹坑"],
      ultralytics_version: "8.3.0",
      trained_at: "",
      epochs: 100,
      epoch: 100,
      best_fitness: 0.9,
      base_model: "yolo11m.pt",
      train_data: "data.yaml",
      training_checkpoint: false,
      metrics: {},
    },
    created_at: "2026-11-18T00:00:00Z",
    updated_at: "2026-11-18T00:00:00Z",
    ...overrides,
  }
}

describe("evaluationModelBlocker", () => {
  it("allows box detection tasks when file exists", () => {
    const tasks: ModelTask[] = ["", "detect", "segment", "pose", "obb"]
    for (const task of tasks) {
      expect(evaluationModelBlocker(mockModel({ task }))).toBeNull()
    }
  })

  it("blocks missing model files and classification models", () => {
    expect(evaluationModelBlocker(mockModel({ file_missing: true }))).toContain(
      "缺失"
    )
    expect(evaluationModelBlocker(mockModel({ task: "classify" }))).toContain(
      "classify"
    )
  })
})

describe("evaluation formatters", () => {
  it("formats metric percentages and deltas", () => {
    expect(formatMetricPercent(0.9125)).toBe("91.3%")
    expect(formatMetricPercent(null)).toBe("—")
    expect(formatMetricPercent(undefined)).toBe("—")
    expect(formatMetricDelta(0.0245)).toBe("+2.5%")
    expect(formatMetricDelta(-0.015)).toBe("-1.5%")
    expect(formatMetricDelta(0.0002)).toBe("0.0%")
    expect(formatMetricDelta(null)).toBe("—")
  })

  it("formats threshold, latency, and throughput", () => {
    expect(formatThreshold(0.25)).toBe("0.25")
    expect(formatThreshold(0.5)).toBe("0.50")
    expect(formatMs(12.34)).toBe("12.3 ms")
    expect(formatMs(142.8)).toBe("143 ms")
    expect(formatMs(0)).toBe("—")
    expect(formatFps(82.45)).toBe("82.5 FPS")
    expect(formatFps(null)).toBe("—")
  })

  it("builds titles, model labels, and source summaries", () => {
    expect(
      evaluationTitle({
        id: 7,
        name: " 基线验证 ",
        model: {
          id: 1,
          project_id: 1,
          project_name: "外观检测",
          version: 2,
          yolo_version: "yolo11",
          task: "detect",
          scale: "m",
          file_name: "best.pt",
          size_bytes: 100,
          sha256: "x",
          class_names: [],
        },
      })
    ).toBe("基线验证")

    expect(
      evaluationTitle({
        id: 7,
        name: "",
        model: {
          id: 1,
          project_id: 1,
          project_name: "外观检测",
          version: 2,
          yolo_version: "yolo11",
          task: "detect",
          scale: "m",
          file_name: "best.pt",
          size_bytes: 100,
          sha256: "x",
          class_names: [],
        },
      })
    ).toBe("外观检测 v2 · 评估 #7")

    expect(
      evaluationModelLabel({
        model: {
          id: 1,
          project_id: 1,
          project_name: "外观检测",
          version: 3,
          yolo_version: "yolo11",
          task: "detect",
          scale: "l",
          file_name: "best.pt",
          size_bytes: 100,
          sha256: "x",
          class_names: [],
        },
      })
    ).toBe("v3 · YOLO11l")

    const dvSource: SourceRef = {
      type: "data_version",
      data_version: {
        id: 4,
        project_id: 1,
        project_name: "外观检测",
        version: 2,
        dir_name: "v2",
        splits: ["val", "test"],
        image_count: 100,
        ok_count: 20,
        ng_count: 80,
        object_count: 150,
      },
      total_images: 100,
      ok_images: 20,
      ng_images: 80,
      total_objects: 150,
    }
    expect(evaluationSourceSummary(dvSource)).toBe("数据版本 v2 (val/test)")

    const dsSource: SourceRef = {
      type: "datasets",
      datasets: [
        {
          dataset_id: 1,
          dataset_name: "A线批次1",
          dir_name: "d1",
          include_base: true,
          versions: [],
          image_mode: "all",
          include_empty: true,
          image_count: 50,
          ok_count: 10,
          ng_count: 40,
          object_count: 60,
        },
        {
          dataset_id: 2,
          dataset_name: "A线批次2",
          dir_name: "d2",
          include_base: true,
          versions: [],
          image_mode: "all",
          include_empty: true,
          image_count: 50,
          ok_count: 10,
          ng_count: 40,
          object_count: 60,
        },
      ],
      total_images: 100,
      ok_images: 20,
      ng_images: 80,
      total_objects: 120,
    }
    expect(evaluationSourceSummary(dsSource)).toBe("A线批次1 等 2 个数据集")
  })

  it("computes progress, duration, and stable colors", () => {
    expect(
      evaluationProgressPercent({
        status: "succeeded",
        progress_done: 0,
        progress_total: 0,
      })
    ).toBe(100)
    expect(
      evaluationProgressPercent({
        status: "running",
        progress_done: 25,
        progress_total: 100,
      })
    ).toBe(25)
    expect(
      evaluationDurationSeconds({
        started_at: "2026-11-18T10:00:00Z",
        finished_at: "2026-11-18T10:01:35Z",
      })
    ).toBe(95)
    expect(colorForIndex(0, "#123456")).toBe("#123456")
    expect(colorForIndex(0, "invalid")).toMatch(/^#/)
  })
})

describe("evaluation paths and parsers", () => {
  it("parses positive ids and deduplicated compare ids up to 6", () => {
    expect(parsePositiveId("42")).toBe(42)
    expect(parsePositiveId("0")).toBeNull()
    expect(parsePositiveId("-3")).toBeNull()
    expect(parsePositiveId("abc")).toBeNull()
    expect(parseCompareIds("3, 1, 3, 2, 0, abc, 4, 5, 6, 7")).toEqual([
      3, 1, 2, 4, 5, 6,
    ])
  })

  it("builds evaluation detail, list, new, and compare URLs", () => {
    expect(evaluationDetailPath(5)).toBe("/evaluation/5")
    expect(
      evaluationDetailPath(5, { tab: "confusion", conf: 0.35, iou: 0.5 })
    ).toBe("/evaluation/5?tab=confusion&conf=0.35&iou=0.50")
    expect(evaluationsListPath({ projectId: 2, modelId: 8 })).toBe(
      "/evaluation?project=2&model=8"
    )
    expect(
      newEvaluationPath({ projectId: 2, modelId: 8, dataVersionId: 4 })
    ).toBe("/evaluation/new?project_id=2&model_id=8&data_version_id=4")
    expect(
      evaluationComparePath([1, 2, 3], {
        mode: "unified",
        conf: 0.4,
        iou: 0.55,
      })
    ).toBe("/evaluation/compare?ids=1%2C2%2C3&mode=unified&conf=0.40&iou=0.55")
  })
})
