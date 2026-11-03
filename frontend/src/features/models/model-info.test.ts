import { describe, expect, it } from "vitest"

import {
  allMetrics,
  epochText,
  exportStatusText,
  exportStep,
  formatMetric,
  formatParameters,
  metricLabel,
  modelTitle,
  shapeText,
  summaryMetrics,
  trainedAt,
} from "@/features/models/model-info"

describe("model info", () => {
  it("titles a model by project and version", () => {
    expect(modelTitle({ project: { name: "划痕检测" }, version: 3 })).toBe(
      "划痕检测 v3"
    )
  })

  it("abbreviates parameter counts", () => {
    expect(formatParameters(2_590_000)).toBe("2.59M")
    expect(formatParameters(25_300)).toBe("25.3K")
    expect(formatParameters(1_070_000_000)).toBe("1.07B")
    expect(formatParameters(999)).toBe("999")
    expect(formatParameters(null)).toBe("—")
    expect(formatParameters(-1)).toBe("—")
  })

  it("describes the training epoch", () => {
    expect(epochText({ epoch: -1, epochs: 100 })).toBe("100 轮（已完成）")
    expect(epochText({ epoch: -1, epochs: null })).toBe("已完成")
    expect(epochText({ epoch: 49, epochs: 100 })).toBe(
      "第 50 / 100 轮（训练中途保存）"
    )
    expect(epochText({ epoch: null, epochs: 300 })).toBe("300 轮")
    expect(epochText({ epoch: null, epochs: null })).toBe("—")
  })

  it("parses the training time", () => {
    expect(trainedAt({ trained_at: "" })).toBeNull()
    expect(trainedAt({ trained_at: "2026-11-01T10:00:00" })).toBeInstanceOf(
      Date
    )
    expect(trainedAt({ trained_at: "昨天" })).toBe("昨天")
  })
})

describe("metrics", () => {
  const detect = {
    "metrics/precision(B)": 0.91,
    "metrics/recall(B)": 0.87,
    "metrics/mAP50(B)": 0.93,
    "metrics/mAP50-95(B)": 0.71,
    "metrics/mAP50(M)": 0.9,
    fitness: 0.73,
  }

  it("labels metric keys", () => {
    expect(metricLabel("metrics/mAP50(B)")).toBe("mAP50（框）")
    expect(metricLabel("metrics/mAP50-95(M)")).toBe("mAP50-95（掩码）")
    expect(metricLabel("metrics/precision(P)")).toBe("Precision（关键点）")
    expect(metricLabel("metrics/accuracy_top1")).toBe("Top-1 准确率")
    expect(metricLabel("metrics/foo(X)")).toBe("foo（X）")
    expect(metricLabel("val/box_loss")).toBe("val/box_loss")
  })

  it("summarizes box metrics first", () => {
    expect(summaryMetrics(detect).map((m) => [m.label, m.value])).toEqual([
      ["mAP50", 0.93],
      ["mAP50-95", 0.71],
      ["P", 0.91],
      ["R", 0.87],
    ])
    expect(summaryMetrics({ "metrics/mAP50(M)": 0.8 })).toEqual([
      { key: "metrics/mAP50(M)", label: "mAP50", value: 0.8 },
    ])
    expect(summaryMetrics({})).toEqual([])
  })

  it("summarizes classification accuracy", () => {
    expect(
      summaryMetrics({
        "metrics/accuracy_top1": 0.95,
        "metrics/accuracy_top5": 0.99,
      }).map((m) => m.label)
    ).toEqual(["Top-1", "Top-5"])
  })

  it("orders all metrics by target and importance", () => {
    expect(allMetrics(detect).map((m) => m.key)).toEqual([
      "metrics/mAP50(B)",
      "metrics/mAP50-95(B)",
      "metrics/precision(B)",
      "metrics/recall(B)",
      "metrics/mAP50(M)",
      "fitness",
    ])
  })

  it("formats metric values", () => {
    expect(formatMetric(0.91234)).toBe("0.912")
    expect(formatMetric(1)).toBe("1.000")
    expect(formatMetric(1.23456)).toBe("1.235")
    expect(formatMetric(Number.NaN)).toBe("—")
  })
})

describe("export status", () => {
  it("describes the queue position and stages", () => {
    expect(
      exportStatusText({ status: "queued", stage: "", queue_position: 2 })
    ).toBe("排队中（第 2 个）")
    expect(
      exportStatusText({ status: "queued", stage: "", queue_position: null })
    ).toBe("排队中")
    expect(
      exportStatusText({
        status: "running",
        stage: "converting",
        queue_position: null,
      })
    ).toBe("正在转换")
    expect(
      exportStatusText({
        status: "running",
        stage: "uploading",
        queue_position: null,
      })
    ).toBe("正在上传结果")
    expect(
      exportStatusText({ status: "failed", stage: "", queue_position: null })
    ).toBe("失败")
  })

  it("maps the status to a step", () => {
    expect(exportStep({ status: "queued", stage: "" })).toBe(0)
    expect(exportStep({ status: "running", stage: "" })).toBe(1)
    expect(exportStep({ status: "running", stage: "downloading" })).toBe(1)
    expect(exportStep({ status: "running", stage: "uploading" })).toBe(3)
    expect(exportStep({ status: "succeeded", stage: "" })).toBe(4)
    expect(exportStep({ status: "failed", stage: "converting" })).toBe(-1)
  })

  it("writes tensor shapes", () => {
    expect(shapeText([1, 3, 640, 640])).toBe("1 × 3 × 640 × 640")
    expect(shapeText(["batch", 3, "height", null])).toBe(
      "batch × 3 × height × ?"
    )
    expect(shapeText([])).toBe("标量")
  })
})
