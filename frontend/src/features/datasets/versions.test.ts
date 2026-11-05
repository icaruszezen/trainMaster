import { describe, expect, it } from "vitest"

import type { DatasetVersion } from "@/features/datasets/api"
import {
  BASE_ONLY,
  previewNotes,
  previewText,
  readyVersions,
  selectionInput,
  selectionMode,
  selectionModeText,
  versionCountsText,
  versionName,
  versionSelection,
  versionSourceText,
} from "@/features/datasets/versions"

function version(changes: Partial<DatasetVersion> = {}): DatasetVersion {
  return {
    id: 7,
    dataset_id: 3,
    kind: "review",
    name: "审图1.2",
    major: 1,
    minor: 2,
    note: "第二批复核",
    status: "ready",
    image_count: 12,
    needs_sample_count: 10,
    approved_count: 2,
    object_count: 35,
    review: { id: 5, name: "第二批复核" },
    merge_id: 9,
    source: {
      review_id: 5,
      review_name: "复核",
      merge_id: 9,
      statuses: ["needs_sample", "approved"],
      model: {
        id: 2,
        project_id: 1,
        project_name: "外观检测",
        version: 3,
        yolo_version: "yolo11",
        task: "detect",
        file_name: "best.pt",
      },
    },
    created_at: "2026-11-01T00:00:00Z",
    updated_at: "2026-11-01T00:00:00Z",
    finished_at: "2026-11-01T00:01:00Z",
    ...changes,
  }
}

describe("selectionMode", () => {
  it("names the four ways of combining versions", () => {
    expect(selectionMode(BASE_ONLY)).toBe("base")
    expect(selectionMode({ include_base: true, version_ids: [1] })).toBe(
      "overlay"
    )
    expect(selectionMode({ include_base: false, version_ids: [1] })).toBe(
      "versions"
    )
    expect(selectionMode({ include_base: false, version_ids: [] })).toBe("none")
  })

  it("explains each mode", () => {
    for (const mode of ["base", "overlay", "versions", "none"] as const) {
      expect(selectionModeText(mode)).not.toBe("")
    }
    expect(selectionModeText("versions")).toContain("需加样")
    expect(selectionModeText("overlay")).toContain("新版本为准")
  })
})

describe("versionSelection", () => {
  it("dedupes and sorts the version ids (a stable query key)", () => {
    expect(versionSelection(true, [5, 2, 5, 9, 2])).toEqual({
      include_base: true,
      version_ids: [2, 5, 9],
    })
    expect(versionSelection(false, new Set([3, 1]))).toEqual({
      include_base: false,
      version_ids: [1, 3],
    })
  })
})

describe("selectionInput", () => {
  it("omits the versions for the base version alone", () => {
    expect(selectionInput(BASE_ONLY)).toEqual({})
    expect(selectionInput(versionSelection(true, []))).toEqual({})
  })

  it("sends other selections as they are", () => {
    const overlay = versionSelection(true, [4, 2])
    expect(selectionInput(overlay)).toEqual({
      include_base: true,
      version_ids: [2, 4],
    })
    const versions = versionSelection(false, [4])
    expect(selectionInput(versions)).toEqual({
      include_base: false,
      version_ids: [4],
    })
  })
})

describe("version text", () => {
  it("names versions that are still being built", () => {
    expect(versionName(version())).toBe("审图1.2")
    expect(versionName(version({ name: "", status: "building" }))).toBe(
      "正在生成的版本"
    )
  })

  it("counts the images of a version by review status", () => {
    expect(versionCountsText(version())).toBe(
      "12 张图片（需加样 10、已审核 2），35 个目标"
    )
    expect(
      versionCountsText(
        version({
          image_count: 3,
          needs_sample_count: 0,
          approved_count: 1,
          object_count: 0,
        })
      )
    ).toBe("3 张图片（已审核 1、待审核 2），0 个目标")
    expect(
      versionCountsText(
        version({
          image_count: 0,
          needs_sample_count: 0,
          approved_count: 0,
          object_count: 0,
        })
      )
    ).toBe("0 张图片，0 个目标")
  })

  it("names the review a version came from, also once deleted", () => {
    expect(versionSourceText(version())).toBe(
      "审图任务“第二批复核” · 外观检测 v3"
    )
    expect(versionSourceText(version({ review: null }))).toBe(
      "审图任务“复核”（已删除） · 外观检测 v3"
    )
  })

  it("lists only the versions that can be downloaded", () => {
    const items = [
      version({ id: 3, status: "building", name: "" }),
      version({ id: 2 }),
      version({ id: 1 }),
    ]
    expect(readyVersions(items).map((item) => item.id)).toEqual([2, 1])
  })
})

describe("preview text", () => {
  const preview = {
    image_count: 1234,
    from_versions: 56,
    object_count: 3456,
    missing_count: 0,
    changed_count: 0,
  }

  it("describes what a download contains", () => {
    expect(previewText("overlay", preview)).toBe(
      "将导出 1,234 张图片，其中 56 张用审图版本的标注，其余用基础版本的标注，共 3,456 个目标。"
    )
    expect(previewText("versions", { ...preview, image_count: 56 })).toBe(
      "将导出 56 张需加样的图片，共 3,456 个目标。"
    )
  })

  it("notes the images a version no longer applies to", () => {
    expect(previewNotes("overlay", preview)).toEqual([])
    expect(
      previewNotes("overlay", {
        ...preview,
        missing_count: 2,
        changed_count: 3,
      })
    ).toEqual([
      "所选版本中有 2 张图片已不在数据集中，不会导出。",
      "3 张图片在合并后被替换了文件，审图标注已不适用，使用基础版本的标注。",
    ])
    expect(previewNotes("versions", { ...preview, changed_count: 1 })).toEqual([
      "1 张图片在合并后被替换了文件，审图标注已不适用，不会导出。",
    ])
  })
})
