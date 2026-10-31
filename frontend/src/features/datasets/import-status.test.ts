import { describe, expect, it } from "vitest"

import type { DatasetImport } from "@/features/datasets/api"
import {
  importFormatLabel,
  jobDurationSeconds,
  jobPercent,
  progressDetail,
  stageLabel,
  summaryText,
} from "@/features/datasets/import-status"

function job(overrides: Partial<DatasetImport> = {}): DatasetImport {
  return {
    id: 1,
    dataset_id: 1,
    source: "archive",
    status: "running",
    on_conflict: "skip",
    annotation_format: "voc",
    classes: [],
    file_name: "batch.zip",
    file_size: 1000,
    file_count: 0,
    received_bytes: 1000,
    received_files: 0,
    stage: "importing",
    progress: { done: 0, total: 0, unit: "" },
    summary: {},
    error: "",
    created_at: "2026-10-30T08:00:00Z",
    updated_at: "2026-10-30T08:00:00Z",
    started_at: null,
    finished_at: null,
    ...overrides,
  }
}

describe("stageLabel", () => {
  it("describes each status and stage", () => {
    expect(stageLabel(job({ status: "uploading" }))).toBe("等待上传")
    expect(stageLabel(job({ status: "queued", stage: "" }))).toBe("等待处理")
    expect(stageLabel(job({ stage: "extracting" }))).toBe("正在解压")
    expect(stageLabel(job({ stage: "converting" }))).toBe("正在转换标注")
    expect(stageLabel(job({ stage: "importing" }))).toBe("正在导入")
    expect(stageLabel(job({ stage: "scanning" }))).toBe("正在扫描")
    expect(stageLabel(job({ stage: "" }))).toBe("正在处理")
    expect(stageLabel(job({ status: "failed" }))).toBe("失败")
  })
})

describe("importFormatLabel", () => {
  it("names the annotation format of archive and folder imports", () => {
    expect(importFormatLabel(job({ annotation_format: "yolo" }))).toBe("YOLO")
    expect(
      importFormatLabel(
        job({ source: "folder", annotation_format: "xanylabeling" })
      )
    ).toBe("X-AnyLabeling")
    expect(importFormatLabel(job({ annotation_format: "voc" }))).toBe("VOC")
  })

  it("returns null for scans", () => {
    expect(importFormatLabel(job({ source: "scan" }))).toBeNull()
  })
})

describe("jobPercent and progressDetail", () => {
  it("uses received bytes while an archive is uploading", () => {
    const uploading = job({
      status: "uploading",
      file_size: 4 * 1024 ** 3,
      received_bytes: 1024 ** 3,
    })
    expect(jobPercent(uploading)).toBe(25)
    expect(progressDetail(uploading)).toBe("1 GB / 4 GB")
  })

  it("uses received files while a folder is uploading", () => {
    const uploading = job({
      status: "uploading",
      source: "folder",
      file_count: 5000,
      received_files: 1234,
    })
    expect(jobPercent(uploading)).toBeCloseTo(24.68)
    expect(progressDetail(uploading)).toBe("1,234 / 5,000 个文件")
  })

  it("formats running progress by unit", () => {
    expect(
      progressDetail(
        job({ progress: { done: 1234, total: 5000, unit: "files" } })
      )
    ).toBe("1,234 / 5,000 个文件")
    expect(
      progressDetail(
        job({ progress: { done: 10, total: 20, unit: "entries" } })
      )
    ).toBe("10 / 20 个条目")
    expect(
      progressDetail(
        job({
          stage: "extracting",
          progress: {
            done: 1.2 * 1024 ** 3,
            total: 3.4 * 1024 ** 3,
            unit: "bytes",
          },
        })
      )
    ).toBe("1.2 GB / 3.4 GB")
    expect(
      jobPercent(job({ progress: { done: 1, total: 4, unit: "files" } }))
    ).toBe(25)
    const converting = job({
      stage: "converting",
      progress: { done: 30, total: 120, unit: "files" },
    })
    expect(jobPercent(converting)).toBe(25)
    expect(progressDetail(converting)).toBe("30 / 120 个文件")
  })

  it("is indeterminate when the total is unknown or the job is queued", () => {
    expect(jobPercent(job({ status: "queued" }))).toBeNull()
    expect(
      jobPercent(job({ progress: { done: 3, total: 0, unit: "" } }))
    ).toBeNull()
    expect(progressDetail(job({ status: "queued" }))).toBe("")
  })
})

describe("summaryText", () => {
  it("summarises an archive or folder import", () => {
    expect(
      summaryText(
        job({
          status: "succeeded",
          summary: {
            images_added: 120,
            images_skipped: 2,
            annotations_matched: 118,
            files_ignored: 0,
          },
        })
      )
    ).toBe("新增 120 张图片，匹配 118 个标注，跳过 2 张同名图片")
  })

  it("summarises converted annotations and conversion problems", () => {
    expect(
      summaryText(
        job({
          status: "succeeded",
          annotation_format: "yolo",
          summary: {
            images_added: 120,
            annotations_converted: 117,
            annotations_matched: 117,
            annotations_failed: 2,
            objects_skipped: 5,
            annotations_orphaned: 1,
            files_ignored: 3,
          },
        })
      )
    ).toBe(
      "新增 120 张图片，转换 117 个标注为 VOC，匹配 117 个标注，" +
        "2 个标注文件无法转换（对应图片按未标注导入），" +
        "跳过 5 个无法转换为矩形框的目标，1 个标注没有对应图片，忽略 3 个其他文件"
    )
  })

  it("always mentions added images for imports", () => {
    expect(summaryText(job({ status: "succeeded", summary: {} }))).toBe(
      "新增 0 张图片"
    )
  })

  it("summarises a scan", () => {
    expect(
      summaryText(
        job({
          source: "scan",
          status: "succeeded",
          summary: { images_total: 1204, images_added: 3, images_removed: 1 },
        })
      )
    ).toBe("共 1,204 张图片，新增 3 张，移除 1 张")
    expect(
      summaryText(
        job({
          source: "scan",
          status: "succeeded",
          summary: { images_total: 10, images_unchanged: 10 },
        })
      )
    ).toBe("共 10 张图片，没有变化")
  })

  it("reports files skipped because their names are not UTF-8", () => {
    expect(
      summaryText(
        job({
          status: "succeeded",
          summary: { images_added: 3, names_invalid: 2 },
        })
      )
    ).toBe(
      "新增 3 张图片，2 个文件名不是 UTF-8 编码，已跳过（请在服务器上重命名）"
    )
    expect(
      summaryText(
        job({
          source: "scan",
          status: "succeeded",
          summary: { images_total: 10, names_invalid: 1 },
        })
      )
    ).toBe(
      "共 10 张图片，1 个文件名不是 UTF-8 编码，已跳过（请在服务器上重命名）"
    )
  })

  it("reports failures and cancellations", () => {
    expect(
      summaryText(job({ status: "failed", error: "压缩包已加密，暂不支持" }))
    ).toBe("压缩包已加密，暂不支持")
    expect(summaryText(job({ status: "canceled" }))).toBe("已取消")
    expect(summaryText(job({ status: "running" }))).toBe("")
  })
})

describe("jobDurationSeconds", () => {
  it("measures from start to finish", () => {
    expect(
      jobDurationSeconds(
        job({
          started_at: "2026-10-30T08:00:00Z",
          finished_at: "2026-10-30T08:02:05Z",
        })
      )
    ).toBe(125)
  })

  it("measures running jobs up to now, falling back to created_at", () => {
    const now = Date.parse("2026-10-30T08:00:30Z")
    expect(jobDurationSeconds(job(), now)).toBe(30)
  })
})
