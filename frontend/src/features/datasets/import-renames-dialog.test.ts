import { describe, expect, it } from "vitest"

import type { ImageRename } from "@/features/datasets/api"
import { renameDetail } from "@/features/datasets/import-renames-dialog"

function rename(overrides: Partial<ImageRename> = {}): ImageRename {
  return {
    kind: "renamed",
    original_path: "cam2/0001.jpg",
    path: "cam2/0001_2.jpg",
    same_name_as: "cam1/0001.jpg",
    annotation_path: "cam2/0001_2.xml",
    ...overrides,
  }
}

describe("renameDetail", () => {
  it("names the image it clashed with and the renamed annotation", () => {
    expect(renameDetail(rename())).toBe(
      "与 cam1/0001.jpg 同名，标注文件一并改为 cam2/0001_2.xml"
    )
  })

  it("leaves out the annotation of an unlabelled image", () => {
    expect(renameDetail(rename({ annotation_path: null }))).toBe(
      "与 cam1/0001.jpg 同名"
    )
  })

  it("explains why an identical image keeps its name", () => {
    expect(
      renameDetail(
        rename({
          kind: "identical",
          original_path: "cam3/0001.jpg",
          path: "cam3/0001.jpg",
          annotation_path: null,
        })
      )
    ).toBe("与 cam1/0001.jpg 同名且内容完全相同，保留原名")
  })
})
