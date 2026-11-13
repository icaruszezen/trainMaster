import { describe, expect, it } from "vitest"

import {
  formatLoss,
  formatLr,
  formatMap,
  jobDurationSeconds,
  trainingArchText,
  trainingJobTitle,
  trainingProgressPercent,
  trainingProgressText,
  trainingQueueText,
} from "@/features/training/format"

describe("training formatting helpers", () => {
  it("formats titles and architecture strings", () => {
    expect(
      trainingJobTitle({
        id: 7,
        name: "  焊点检测实验  ",
        yolo_version: "yolo11",
        model_scale: "s",
      })
    ).toBe("焊点检测实验")

    expect(
      trainingJobTitle({
        id: 7,
        name: "",
        yolo_version: "yolo11",
        model_scale: "s",
      })
    ).toBe("训练 #7 · YOLO11s")

    expect(
      trainingArchText({
        yolo_version: "yolov9",
        model_scale: "c",
        task: "detect",
      })
    ).toBe("YOLOv9c")

    expect(
      trainingArchText({
        yolo_version: "yolo11",
        model_scale: "m",
        task: "segment",
      })
    ).toBe("YOLO11m · 分割")
  })

  it("computes training progress percent and text across stages", () => {
    expect(
      trainingProgressPercent({
        status: "running",
        stage: "downloading",
        current_epoch: 0,
        total_epochs: 100,
        batch_done: 0,
        batch_total: 0,
      })
    ).toBeNull()

    expect(
      trainingProgressPercent({
        status: "running",
        stage: "training",
        current_epoch: 26,
        total_epochs: 100,
        batch_done: 50,
        batch_total: 100,
      })
    ).toBeCloseTo(25.5)

    expect(
      trainingProgressText({
        stage: "training",
        current_epoch: 5,
        total_epochs: 50,
        batch_done: 10,
        batch_total: 40,
        eta_seconds: 120,
      })
    ).toBe("轮次 5 / 50 · 批次 10/40 · 剩余约 2 分钟")

    expect(trainingQueueText(2, 0)).toBe("排队第 2 位 · 暂无在线训练节点")
    expect(trainingQueueText(1, 2)).toBe("排队第 1 位")
  })

  it("formats mAP, loss, learning rate, and job duration", () => {
    expect(formatMap(0.8764)).toBe("87.6%")
    expect(formatMap(undefined)).toBe("—")
    expect(formatLoss(1.23456)).toBe("1.2346")
    expect(formatLr(0.01)).toBe("0.01")
    expect(formatLr(0.00005)).toBe("5.00e-5")

    expect(
      jobDurationSeconds({
        status: "succeeded",
        started_at: "2026-11-12T10:00:00Z",
        finished_at: "2026-11-12T10:02:35Z",
      })
    ).toBe(155)
  })
})
